package service

import (
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrDeviceHostInvalidIdentity = errors.New("缺少或无效的设备标识/客户端实例")
	ErrDeviceHostUnavailable     = errors.New("设备登记暂不可用")
	ErrDeviceHostAccountBanned   = errors.New("账号已被封禁")
	errDeviceRegistrationDenied  = errors.New("registration decision rejected")
)

// Capacity is account-scoped, not device-scoped. Use the existing user row
// for cross-process ordering on MariaDB and the T17 device transaction for
// same-device state transitions. Compatibility callers may have an old schema;
// only explicit admission requires both protections to be ready.
var sqliteDeviceAccountLocks [64]sync.Mutex

func withDeviceRegistrationWrite(db *gorm.DB, userID int32, deviceID string, strict bool, action func(*gorm.DB) error) error {
	isMySQL := db.Dialector.Name() == "mysql"
	if !isMySQL {
		lock := &sqliteDeviceAccountLocks[uint32(userID)%uint32(len(sqliteDeviceAccountLocks))]
		lock.Lock()
		defer lock.Unlock()
	}
	hasUserTable := db.Migrator().HasTable(&entity.User{})
	if strict && (!hasUserTable || !db.Migrator().HasTable(&entity.DeviceLifecycleLock{})) {
		return ErrDeviceHostUnavailable
	}

	return withDeviceLifecycleWrite(db, deviceID, func(tx *gorm.DB) error {
		if isMySQL && hasUserTable {
			var owner struct{ ID int32 }
			err := devicePoolSession(tx).Table(entity.TableNameUser).
				Select("id").Where("id = ?", userID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&owner).Error
			if errors.Is(err, gorm.ErrRecordNotFound) && strict {
				return ErrInvalidAccessTokenAuthorization
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		return action(tx)
	})
}

func checkDeviceHostAccount(db *gorm.DB, user entity.User) error {
	banned, err := IsBanned(db, user.ID)
	if err != nil {
		return err
	}
	if banned {
		return ErrDeviceHostAccountBanned
	}
	if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") {
		return ErrInvalidAccessTokenAuthorization
	}
	return nil
}

func validDeviceHostIdentifier(raw string) bool {
	return utf8.ValidString(raw) && !strings.ContainsRune(raw, utf8.RuneError) &&
		strings.IndexFunc(raw, unicode.IsControl) < 0
}

func hostAdmissionSchemaReady(db *gorm.DB) bool {
	return deviceRevocationColumnsAvailable(db) && deviceConflictInstanceColumnAvailable(db) && userLevelSchemaAvailable(db) &&
		devicePoolSessionCountColumnAvailable(db) && db.Migrator().HasTable(&entity.UserBan{}) &&
		db.Migrator().HasTable(&entity.UserDeviceBlock{}) && db.Migrator().HasTable(&entity.BlacklistEntry{}) &&
		db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "owner_kind") &&
		db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "token_id") &&
		db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override")
}

// AdmitDeviceHost never confuses a compatibility allow/fail-open with a
// registered device. The caller's identity is from the ordinary access-key resolver.
func AdmitDeviceHost(db *gorm.DB, auth AccessTokenAuthContext, rawDevice, rawInstance, clientIP string) (string, DevicePoolDecision, error) {
	deviceID, instance := NormalizeDevicePoolDeviceID(rawDevice), NormalizeDeviceClientInstance(rawInstance)
	if deviceID == "" || instance == "" || !validDeviceHostIdentifier(rawDevice) || !validDeviceHostIdentifier(rawInstance) {
		return "", DevicePoolDecision{}, ErrDeviceHostInvalidIdentity
	}
	userID, tokenID := auth.User.ID, auth.AccessToken.ID
	if userID <= 0 || tokenID <= 0 || auth.AccessToken.UserID != userID || auth.AccessToken.ScriptID != 0 {
		return "", DevicePoolDecision{}, ErrInvalidAccessTokenAuthorization
	}
	if db == nil {
		return "", DevicePoolDecision{}, ErrDeviceHostUnavailable
	}

	var decision DevicePoolDecision
	err := withDeviceRegistrationWrite(db, userID, deviceID, true, func(tx *gorm.DB) error {
		if !hostAdmissionSchemaReady(tx) {
			return ErrDeviceHostUnavailable
		}
		fresh := devicePoolSession(tx)

		// Validate the previously authenticated token again inside the commit
		// boundary. Explicit admission does not extend its credential scope.
		var token entity.AccessToken
		if err := fresh.Where("id = ? AND user_id = ?", tokenID, userID).
			Clauses(clause.Locking{Strength: "UPDATE"}).Take(&token).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidAccessTokenAuthorization
			}
			return err
		}
		if token.ScriptID != 0 || !strings.EqualFold(strings.TrimSpace(token.Status), "enabled") ||
			!AccessTokenQuotaLimitValid(token.TotalQuotaLimit) || token.TokenHash != auth.AccessToken.TokenHash {
			return ErrInvalidAccessTokenAuthorization
		}
		var user entity.User
		if err := fresh.Where("id = ?", userID).Take(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvalidAccessTokenAuthorization
			}
			return err
		}
		if err := checkDeviceHostAccount(fresh, user); err != nil {
			return err
		}

		var policyErr error
		decision, policyErr = inspectHostAdmission(tx, userID, deviceID, instance)
		if policyErr != nil {
			return policyErr
		}
		if !decision.Allowed {
			// A rejected device must not leave even a new lock row behind.
			return errDeviceRegistrationDenied
		}
		return persistAdmittedHost(tx, userID, deviceID, instance, clientIP, tokenID)
	})
	if errors.Is(err, errDeviceRegistrationDenied) {
		err = nil
	}
	return deviceID, decision, err
}

func inspectHostAdmission(db *gorm.DB, userID int32, deviceID, instance string) (DevicePoolDecision, error) {
	bans, err := LoadActiveDeviceBansWithDB(db, []string{deviceID}, time.Now())
	if err != nil {
		return DevicePoolDecision{}, err
	}
	if ban, blocked := bans[deviceID]; blocked {
		return DevicePoolDecision{Reason: DevicePoolReasonDeviceBlacklisted, Used: DevicePoolUsedUnknown, Message: "该设备已被限制使用", BlacklistEntryID: ban.EntryID}, nil
	}
	blocked, err := AccountDeviceHasBlock(db, userID, deviceID)
	if err != nil {
		return DevicePoolDecision{}, err
	}
	if blocked {
		return DevicePoolDecision{Reason: DevicePoolReasonUserBlocked, Used: DevicePoolUsedUnknown, Message: "你已拉黑该设备"}, nil
	}

	_, bound, err := activeDeviceBindingID(db, userID, deviceID)
	if err != nil {
		return DevicePoolDecision{}, err
	}
	if !bound {
		claimed, err := loadDeviceConflictClaimedInstances(db, deviceID)
		if err != nil {
			return DevicePoolDecision{}, err
		}
		for _, other := range claimed {
			if other != "" && other != instance {
				return DevicePoolDecision{Reason: DevicePoolReasonDeviceIDConflict, Used: DevicePoolUsedUnknown, Message: DeviceConflictMessage}, nil
			}
		}
	}
	limit, source, err := EffectiveDeviceLimit(db, userID)
	if err != nil {
		return DevicePoolDecision{}, err
	}
	used, err := CountDevices(db, userID)
	if err != nil {
		return DevicePoolDecision{}, err
	}
	decision := DevicePoolDecision{Limit: limit, Used: used, LimitSource: source, Enforced: limit != 0}
	switch {
	case limit < 0:
		decision.Reason, decision.Message = DevicePoolReasonForbidden, "当前账号等级不允许在任何设备上使用，请联系管理员"
	case limit > 0 && !bound && used >= int(limit):
		decision.Reason, decision.Message = DevicePoolReasonLimitExceeded, BuildDevicePoolLimitExceededMessage(limit, used)
	default:
		decision.Allowed = true
		decision.Reason = DevicePoolReasonWithinLimit
		if bound {
			decision.Reason = DevicePoolReasonAlreadyRegistered
		}
	}
	return decision, nil
}

func persistAdmittedHost(db *gorm.DB, userID int32, deviceID, instance, clientIP string, tokenID int32) error {
	if err := persistDeviceBinding(db, userID, deviceID, DeviceBindingRegisterOptions{
		OwnerKind: entity.DeviceBindingOwnerKindToken, TokenID: tokenID, ClientInstance: instance, ClientIP: clientIP,
	}); err != nil {
		return err
	}
	_, bound, err := activeDeviceBindingID(db, userID, deviceID)
	if err != nil {
		return err
	}
	if !bound {
		return ErrDeviceHostUnavailable
	}
	return nil
}
