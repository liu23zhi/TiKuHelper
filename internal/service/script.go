package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ScriptReservationFee = 10.0
	// ScriptExchangeMinimumBalance is the spendable balance that must remain
	// after a script's reservation before another launcher ticket can be issued.
	// The reservation itself is returned only when the script is deleted.
	ScriptExchangeMinimumBalance       = 9.0
	ScriptExchangeTokenTTL             = 5 * time.Minute
	ScriptRunSessionTTL                = 150 * time.Minute
	ScriptVersionMaxBytes        int64 = 64 << 20
	scriptCreateOperation              = "script-create"
)

var (
	ErrScriptForbidden                = errors.New("无权限操作脚本")
	ErrScriptBindingsRequired         = errors.New("创建脚本前请绑定手机号和微信")
	ErrScriptNotFound                 = errors.New("脚本不存在")
	ErrScriptVersionNotFound          = errors.New("脚本版本不存在")
	ErrScriptNameRequired             = errors.New("脚本名称不能为空")
	ErrScriptDomainRequired           = errors.New("脚本域名不能为空")
	ErrScriptVersionRequired          = errors.New("脚本版本不能为空")
	ErrScriptVersionInvalid           = errors.New("脚本版本无效")
	ErrScriptNativeVersionRequired    = errors.New("请先发布已验证支持新版脚本租约的原生 C 客户端")
	ErrScriptBalanceInsufficient      = errors.New("余额不足，创建脚本需要预扣 ¥10")
	ErrScriptExchangeAlreadyIssued    = errors.New("该脚本已有未使用的兑换令牌，请先使用或等待令牌过期")
	ErrScriptTokenBalanceInsufficient = errors.New("可用余额不足 ¥9，暂不能下发脚本兑换令牌；请充值后重试")
	ErrScriptTokenInvalid             = errors.New("脚本兑换令牌无效或已过期")
	ErrScriptTokenRequired            = errors.New("脚本兑换令牌不能为空")
	ErrScriptPriceInvalid             = errors.New("脚本答题计费金额超出可存储范围")
	ErrScriptBillingModeInvalid       = errors.New("脚本计费模式无效")
	ErrScriptEscrowClosePending       = errors.New("冻结金脚本尚需安全结算后关闭")
)

type CreateUserScriptInput struct {
	IdempotencyKey        string         `json:"-"`
	Name                  string         `json:"name"`
	Instruction           string         `json:"instruction"`
	Command               string         `json:"command"`
	VersionID             int32          `json:"versionId"`
	Parameters            map[string]any `json:"parameters"`
	Description           string         `json:"description"`
	BaseDomain            string         `json:"baseDomain"`
	Scheme                string         `json:"scheme"`
	UseWildcardDomain     *bool          `json:"useWildcardDomain"`
	Enabled               *bool          `json:"enabled"`
	BillingEnabled        *bool          `json:"billingEnabled"`
	UseTikuPrice          *bool          `json:"useTikuPrice"`
	BillingUseTikuPrice   *bool          `json:"billingUseTikuPrice"`
	PresetParams          map[string]any `json:"presetParams"`
	PriceMultiplier       *float64       `json:"priceMultiplier"`
	AnswerPriceMultiplier *float64       `json:"answerPriceMultiplier"`
	AnswerPrice           float64        `json:"answerPrice"`
	BillingMode           string         `json:"billingMode,omitempty"`
	EntitlementID         int64          `json:"entitlementId,omitempty"`
	AutoTopUp             bool           `json:"autoTopUp,omitempty"`
	RunDurationMinutes    int            `json:"runDurationMinutes,omitempty"`
	NotifyOnDenied        bool           `json:"notifyOnDenied,omitempty"`
	// SkipOwnerBindingCheck 由**管理员管理视图**的调用方置真（诉求 ②）。
	//
	// 「脚本管理」是管理员看的运维视图，给用户设手机的合规门槛放这里没有意义：
	// 管理员一旦没绑手机/微信就整块功能不可用，而他做的是账号侧的管理动作。
	//
	// ⚠️ 它**不是**从请求体读的（json:"-"），必须由控制器在确认「调用者确为管理员
	// 且走的是管理入口」之后显式置真。否则用户只要在 payload 里加一个字段
	// 就能绕过自己的绑定约束 —— 那正是这一条最容易写错的地方。
	SkipOwnerBindingCheck bool `json:"-"`
}

type UpdateUserScriptInput struct {
	Name                  string         `json:"name"`
	Instruction           string         `json:"instruction"`
	Command               string         `json:"command"`
	Parameters            map[string]any `json:"parameters"`
	PresetParams          map[string]any `json:"presetParams"`
	Description           string         `json:"description"`
	BaseDomain            string         `json:"baseDomain"`
	Scheme                string         `json:"scheme"`
	UseWildcardDomain     *bool          `json:"useWildcardDomain"`
	Enabled               *bool          `json:"enabled"`
	BillingEnabled        *bool          `json:"billingEnabled"`
	UseTikuPrice          *bool          `json:"useTikuPrice"`
	BillingUseTikuPrice   *bool          `json:"billingUseTikuPrice"`
	PriceMultiplier       *float64       `json:"priceMultiplier"`
	AnswerPriceMultiplier *float64       `json:"answerPriceMultiplier"`
	AnswerPrice           *float64       `json:"answerPrice"`
	AutoTopUp             *bool          `json:"autoTopUp,omitempty"`
	RunDurationMinutes    *int           `json:"runDurationMinutes,omitempty"`
	NotifyOnDenied        *bool          `json:"notifyOnDenied,omitempty"`
}

type ScriptListItem struct {
	entity.UserScript
	Username              string `json:"username,omitempty"`
	Version               string `json:"version"`
	VersionSHA256         string `json:"versionSha256"`
	Command               string `json:"command"`
	DownloadURL           string `json:"downloadUrl"`
	EscrowAvailableMicros *int64 `json:"escrowAvailableMicros,omitempty"`
}

type ScriptExchangeGrant struct {
	ShortCode string    `json:"shortCode"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ScriptExchangeResult struct {
	ScriptID            int32          `json:"scriptId"`
	Name                string         `json:"name"`
	Instruction         string         `json:"instruction"`
	Parameters          map[string]any `json:"parameters"`
	Version             string         `json:"version"`
	Filename            string         `json:"filename"`
	DownloadURL         string         `json:"downloadUrl"`
	ActivationToken     string         `json:"activationToken"`
	ActivationExpiresAt time.Time      `json:"activationExpiresAt"`
	CommandURL          string         `json:"commandUrl"`
	BootstrapURL        string         `json:"bootstrapUrl"`
	LaunchOrigin        string         `json:"-"`
	ExecutableSHA256    string         `json:"-"`
	BillingMode         string         `json:"-"`
	NotifyOnDenied      bool           `json:"-"`
}

func ListScriptVersionsWithDB(db *gorm.DB) ([]entity.ScriptVersion, error) {
	var versions []entity.ScriptVersion
	err := db.Session(&gorm.Session{NewDB: true}).Select("id", "version", "filename", "sha256", "size_bytes", "is_current", "native_lease_protocol", "created_by", "created_at").Order("id desc").Find(&versions).Error
	return versions, err
}

func UploadScriptVersionWithDB(db *gorm.DB, admin entity.User, version, filename string, data []byte, nativeLease ...bool) (entity.ScriptVersion, error) {
	if !isAdminUser(admin) {
		return entity.ScriptVersion{}, ErrScriptForbidden
	}
	version = strings.TrimSpace(version)
	filename = strings.TrimSpace(filename)
	if version == "" {
		return entity.ScriptVersion{}, ErrScriptVersionRequired
	}
	if len(version) > 64 || strings.ContainsAny(version, "\\/\x00") {
		return entity.ScriptVersion{}, ErrScriptVersionInvalid
	}
	if !strings.EqualFold(filepathExt(filename), ".exe") || len(data) < 2 || data[0] != 'M' || data[1] != 'Z' || int64(len(data)) > ScriptVersionMaxBytes {
		return entity.ScriptVersion{}, ErrScriptVersionInvalid
	}
	sum := sha256.Sum256(data)
	native := len(nativeLease) == 1 && nativeLease[0]
	result := entity.ScriptVersion{Version: version, Filename: safeScriptFilename(filename), SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data)), Data: append([]byte(nil), data...), IsCurrent: true, NativeLeaseProtocol: native, CreatedBy: admin.ID}
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entity.ScriptVersion{}).Where("is_current = ? AND native_lease_protocol = ?", true, native).Update("is_current", false).Error; err != nil {
			return err
		}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		// Only advance the compatible lane. Uploading a frozen/legacy binary
		// cannot silently replace the native runtime for managed scripts.
		target := tx.Model(&entity.UserScript{}).Where("deleted_at IS NULL")
		if native {
			target = target.Where("billing_mode IN ?", []string{entity.ScriptBillingModeEscrow, entity.ScriptBillingModeActivation})
		} else {
			target = target.Where("billing_mode = ?", "")
		}
		return target.Updates(map[string]any{"version_id": result.ID, "updated_at": time.Now()}).Error
	})
	return result, err
}

func CreateUserScriptWithDB(db *gorm.DB, operator entity.User, input CreateUserScriptInput) (ScriptListItem, error) {
	if db == nil {
		return ScriptListItem{}, errors.New("数据库不可用")
	}
	mode := input.BillingMode
	if mode != "" && mode != entity.ScriptBillingModeEscrow && mode != entity.ScriptBillingModeActivation {
		return ScriptListItem{}, ErrScriptBillingModeInvalid
	}
	if input.RunDurationMinutes < 0 || input.RunDurationMinutes > int(ScriptRunSessionTTL/time.Minute) {
		return ScriptListItem{}, ErrScriptDurationInvalid
	}
	if mode != "" && normalizeScriptScheme(input.Scheme) != "https" {
		return ScriptListItem{}, ErrScriptDomainUnavailable
	}
	if mode == entity.ScriptBillingModeActivation && input.EntitlementID <= 0 {
		return ScriptListItem{}, ErrScriptBillingModeInvalid
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ScriptListItem{}, ErrScriptNameRequired
	}
	baseDomain := normalizeScriptDomain(input.BaseDomain)
	if baseDomain == "" {
		return ScriptListItem{}, ErrScriptDomainRequired
	}
	// Q72-3：脚本自身的域名必须是具体主机名，`a.*.a.com` / `*.a.com` 在创建时拒绝，
	// 而不是等匹配阶段悄悄不生效。通配符属于管理员允许的域名列表。
	if err := validateScriptBaseDomain(baseDomain); err != nil {
		return ScriptListItem{}, err
	}
	if err := ensureScriptDomainRulePrefixAvailable(db, baseDomain); err != nil {
		return ScriptListItem{}, err
	}
	if input.VersionID <= 0 {
		return ScriptListItem{}, ErrScriptVersionNotFound
	}
	if math.IsNaN(input.AnswerPrice) || math.IsInf(input.AnswerPrice, 0) || input.AnswerPrice < 0 {
		return ScriptListItem{}, errors.New("脚本答题单价必须是大于或等于 0 的有效金额")
	}
	if input.AnswerPrice >= 1_000_000_000_000 {
		return ScriptListItem{}, errors.New("脚本答题单价超出可存储范围")
	}
	multiplier := 1.0
	if input.PriceMultiplier != nil {
		multiplier = *input.PriceMultiplier
	} else if input.AnswerPriceMultiplier != nil {
		multiplier = *input.AnswerPriceMultiplier
	}
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier < 0 {
		return ScriptListItem{}, errors.New("脚本答题倍率必须是大于或等于 0 的有效数字")
	}
	if multiplier >= 1_000_000_000_000 {
		return ScriptListItem{}, errors.New("脚本答题倍率超出可存储范围")
	}
	parameterValues := input.Parameters
	if parameterValues == nil {
		parameterValues = input.PresetParams
	}
	if parameterValues == nil {
		parameterValues = map[string]any{}
	}
	parameters, err := json.Marshal(parameterValues)
	if err != nil {
		return ScriptListItem{}, err
	}
	var requestFingerprint []byte
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey != "" {
		requestFingerprint, err = json.Marshal(input)
		if err != nil {
			return ScriptListItem{}, err
		}
	}
	var result ScriptListItem
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, operator.ID).Error; err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") {
			return ErrScriptForbidden
		}
		scope := fmt.Sprintf("script-create:user:%d", user.ID)
		// 诉求 ②：管理员管理视图不校验被管理账号的手机/微信绑定。
		//
		// 这是一条**共享**的服务层校验，所以按调用来源（SkipOwnerBindingCheck）
		// 分流，而不是一刀切删掉 —— 用户侧「我的脚本」的绑定门槛保留，
		// 理由见 ValidateScriptOwnerBinding 的注释。
		if input.SkipOwnerBindingCheck {
			// 管理视图：跳过绑定门槛，其余校验（版本有效性、余额、幂等）一律照旧。
		} else if user.Phone == nil || strings.TrimSpace(*user.Phone) == "" ||
			user.WechatID == nil || strings.TrimSpace(*user.WechatID) == "" {
			// A binding can change after authentication. A matching completed
			// receipt is a read, but an expired/missing receipt cannot create a
			// claim or mutate the wallet while the account is unbound.
			result, err = readScriptCreationReceiptWithDB(tx, user.ID, scope, idempotencyKey, requestFingerprint)
			return err
		}
		var claimID int64
		if idempotencyKey != "" {
			claim, created, err := ClaimIdempotencyWithDB(tx, scope, scriptCreateOperation, idempotencyKey, requestFingerprint)
			if err != nil {
				return err
			}
			if !created {
				// Store only the object ID, not parameters or the launcher
				// capability. Read the same owner's current object on replay.
				result, err = replayScriptCreationReceiptWithDB(tx, user.ID, claim)
				return err
			}
			claimID = claim.ID
		}
		var version entity.ScriptVersion
		if err := tx.First(&version, input.VersionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptVersionNotFound
			}
			return err
		}
		if !version.IsCurrent {
			return ErrScriptVersionInvalid
		}
		if mode != "" && !version.NativeLeaseProtocol {
			return ErrScriptNativeVersionRequired
		}
		before := util.RoundMoney(user.Balance)
		after := before
		if mode == "" {
			if before < ScriptReservationFee {
				return ErrScriptBalanceInsufficient
			}
			after = util.RoundMoney(before - ScriptReservationFee)
			updated := tx.Model(&entity.User{}).Where("id = ? AND balance = ?", user.ID, user.Balance).Update("balance", after)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return errors.New("脚本预扣余额已变化，请重试")
			}
		}
		key, err := randomScriptSecret(32)
		if err != nil {
			return err
		}
		shortCode, err := uniqueScriptShortCode(tx)
		if err != nil {
			return err
		}
		// 公开地址形态由**管理员配置的域名格式**决定，不是由调用方随便传的布尔决定。
		//
		// 为什么必须这样：公开 URL 是 `ScriptCommandOrigin + (useWildcard ? "" : "/"+shortCode)`。
		//   - `*.a.com` 泛域名格式：脚本住在 `<shortCode>.<baseDomain>` 这个独立主机上，
		//     根路径就是脚本入口 ⇒ useWildcard 必须为 true。
		//   - `a.com/*` 路径格式：脚本住在**共享**主机 baseDomain 下，靠路径段区分
		//     ⇒ 必须有 `/<shortCode>` 这一段，useWildcard 必须为 false。
		//     ⚠️ 这正是本轮的实时 bug：默认恒为 true 时，按 `a.com/*` 建的脚本
		//     公开地址是根路径 "",而 authorizeScriptPublicPath 的路径规则要求至少一段
		//     ⇒ 该类脚本**永远解析不到**（实测 Resolve 恒 ErrScriptNotFound）。
		useWildcard := defaultScriptUseWildcardWithDB(tx, baseDomain)
		if input.UseWildcardDomain != nil {
			useWildcard = *input.UseWildcardDomain
		}
		if err := ValidateScriptDomainWithDB(tx, baseDomain); err != nil {
			return err
		}
		script := entity.UserScript{UserID: user.ID, VersionID: version.ID, Name: name, Parameters: string(parameters), Description: strings.TrimSpace(input.Description), BaseDomain: baseDomain, Scheme: normalizeScriptScheme(input.Scheme), UseWildcardDomain: useWildcard, Enabled: true, BillingEnabled: true, UseTikuPrice: true, AnswerPriceMultiplier: util.RoundMoney(multiplier), AnswerPrice: util.RoundMoney(input.AnswerPrice), ShortCode: shortCode, AccessKey: key, ReservedFee: ScriptReservationFee}
		if input.Enabled != nil {
			script.Enabled = *input.Enabled
		}
		if input.BillingEnabled != nil {
			script.BillingEnabled = *input.BillingEnabled
		}
		if useTikuPrice := input.UseTikuPrice; useTikuPrice != nil {
			script.UseTikuPrice = *useTikuPrice
		} else if input.BillingUseTikuPrice != nil {
			script.UseTikuPrice = *input.BillingUseTikuPrice
		}
		if mode != "" {
			script.BillingMode = mode
			script.EscrowWorkTracked = mode == entity.ScriptBillingModeEscrow
			script.RunDurationMinutes = input.RunDurationMinutes
			script.NotifyOnDenied = input.NotifyOnDenied
			script.AutoTopUp = mode == entity.ScriptBillingModeEscrow && input.AutoTopUp
			script.BillingEnabled = mode == entity.ScriptBillingModeEscrow
			script.UseTikuPrice = false
			script.AnswerPrice, script.AnswerPriceMultiplier = 0, 0
		}
		// Store only the locator. The owner view derives the purpose-separated
		// launcher capability; a one-use exchange must never disclose it.
		script.Instruction = BuildScriptCommandURL(script)
		if err := tx.Create(&script).Error; err != nil {
			return err
		}
		if mode != "" {
			// GORM's legacy default is 10. Clear it explicitly before commit;
			// the new holds are tracked only in the dedicated ledger.
			if err := tx.Model(&script).UpdateColumn("reserved_fee", 0).Error; err != nil {
				return err
			}
			script.ReservedFee = 0
			switch mode {
			case entity.ScriptBillingModeEscrow:
				if _, err := FreezeScriptEscrowOnCreateInTx(tx, script.ID, user.ID); err != nil {
					return err
				}
			case entity.ScriptBillingModeActivation:
				if err := BindScriptActivationEntitlementInTx(tx, user.ID, input.EntitlementID, script.ID); err != nil {
					return err
				}
			}
		}
		plainToken := "tk_" + key
		tokenPrefix := plainToken
		if len(tokenPrefix) > 12 {
			tokenPrefix = tokenPrefix[:12]
		}
		accessToken := entity.AccessToken{UserID: user.ID, ScriptID: script.ID, Name: "script:" + fmt.Sprint(script.ID), TokenHash: hashScriptSecret(plainToken), TokenPrefix: tokenPrefix, TokenValue: plainToken, Status: "enabled", Remark: "脚本专用令牌"}
		if err := tx.Create(&accessToken).Error; err != nil {
			return err
		}
		if err := tx.Model(&script).Update("access_token_id", accessToken.ID).Error; err != nil {
			return err
		}
		script.AccessTokenID = accessToken.ID
		ledger := entity.BillingRecord{UserID: user.ID, Type: "script_reservation", Provider: "script", Amount: ScriptReservationFee, BalanceBefore: before, BalanceAfter: after, RequestID: fmt.Sprintf("script_create_%d", script.ID), Remark: "创建脚本预扣费用"}
		if mode == "" {
			if err := tx.Create(&ledger).Error; err != nil {
				return err
			}
		}
		result = decorateScriptItem(ScriptListItem{UserScript: script, Username: user.Username, Version: version.Version, VersionSHA256: version.SHA256})
		if mode == entity.ScriptBillingModeEscrow {
			if err := attachScriptEscrowBalanceWithDB(tx, &result); err != nil {
				return err
			}
		}
		if claimID > 0 {
			return CompleteIdempotencyWithDB(tx, claimID, 200, []byte(fmt.Sprintf("{\"ID\":%d}", script.ID)))
		}
		return nil
	})
	return result, err
}

func readScriptCreationReceiptWithDB(db *gorm.DB, ownerID int32, scope, key string, request []byte) (ScriptListItem, error) {
	if key == "" || len(key) > 256 {
		return ScriptListItem{}, ErrScriptBindingsRequired
	}
	var receipt entity.IdempotencyRecord
	err := db.Where("scope_hash = ? AND operation = ? AND key_hash = ? AND status = ? AND expires_at > ?",
		hashIdempotencyValue(scope), scriptCreateOperation, hashIdempotencyValue(key), IdempotencyStatusSucceeded, time.Now()).First(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ScriptListItem{}, ErrScriptBindingsRequired
	}
	if err != nil {
		return ScriptListItem{}, err
	}
	if receipt.RequestHash != hashIdempotencyValue(string(request)) {
		return ScriptListItem{}, ErrIdempotencyConflict
	}
	return replayScriptCreationReceiptWithDB(db, ownerID, receipt)
}

func replayScriptCreationReceiptWithDB(db *gorm.DB, ownerID int32, claim entity.IdempotencyRecord) (ScriptListItem, error) {
	var receipt struct{ ID int32 }
	if claim.Status != IdempotencyStatusSucceeded || json.Unmarshal([]byte(claim.ResponseJSON), &receipt) != nil || receipt.ID <= 0 {
		return ScriptListItem{}, ErrIdempotencyPending
	}
	return loadScriptListItemWithDB(db, receipt.ID, ownerID)
}

func loadScriptListItemWithDB(db *gorm.DB, scriptID, userID int32) (ScriptListItem, error) {
	var item ScriptListItem
	err := db.Session(&gorm.Session{NewDB: true}).Table(entity.TableNameUserScript+" AS user_scripts").
		Select("user_scripts.*, users.username AS username, script_versions.version AS version, script_versions.sha256 AS version_sha256").
		Joins("LEFT JOIN users ON users.id = user_scripts.user_id").
		Joins("LEFT JOIN script_versions ON script_versions.id = user_scripts.version_id").
		Where("user_scripts.id = ? AND user_scripts.user_id = ? AND user_scripts.deleted_at IS NULL", scriptID, userID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ScriptListItem{}, ErrScriptNotFound
	}
	if err != nil {
		return ScriptListItem{}, err
	}
	if err := attachScriptEscrowBalanceWithDB(db, &item); err != nil {
		return ScriptListItem{}, err
	}
	return decorateScriptItem(item), nil
}

func attachScriptEscrowBalanceWithDB(db *gorm.DB, item *ScriptListItem) error {
	if item.BillingMode != entity.ScriptBillingModeEscrow {
		return nil
	}
	var escrow entity.ScriptEscrow
	if err := db.Session(&gorm.Session{NewDB: true}).Select("available_micros").
		Where("script_id = ? AND user_id = ?", item.ID, item.UserID).Take(&escrow).Error; err != nil {
		return err
	}
	item.EscrowAvailableMicros = &escrow.AvailableMicros
	return nil
}

func ListUserScriptsWithDB(db *gorm.DB, operator entity.User, all bool) ([]ScriptListItem, error) {
	query := db.Session(&gorm.Session{NewDB: true}).Table(entity.TableNameUserScript + " AS user_scripts").
		Select("user_scripts.*, users.username AS username, script_versions.version AS version, script_versions.sha256 AS version_sha256").
		Joins("LEFT JOIN users ON users.id = user_scripts.user_id").
		Joins("LEFT JOIN script_versions ON script_versions.id = user_scripts.version_id").
		Where("user_scripts.deleted_at IS NULL").Order("user_scripts.id desc")
	if !(all && isAdminUser(operator)) {
		query = query.Where("user_scripts.user_id = ?", operator.ID)
	}
	var result []ScriptListItem
	if err := query.Find(&result).Error; err != nil {
		return nil, err
	}
	escrowIDs := make([]int32, 0)
	for _, item := range result {
		if item.BillingMode == entity.ScriptBillingModeEscrow {
			escrowIDs = append(escrowIDs, item.ID)
		}
	}
	escrowByID := make(map[int32]entity.ScriptEscrow, len(escrowIDs))
	if len(escrowIDs) > 0 {
		var balances []entity.ScriptEscrow
		if err := db.Session(&gorm.Session{NewDB: true}).Select("script_id", "user_id", "available_micros").
			Where("script_id IN ?", escrowIDs).Find(&balances).Error; err != nil {
			return nil, err
		}
		for _, balance := range balances {
			escrowByID[balance.ScriptID] = balance
		}
	}
	for i := range result {
		result[i] = decorateScriptItem(result[i])
		if balance, ok := escrowByID[result[i].ID]; ok && balance.UserID == result[i].UserID {
			result[i].EscrowAvailableMicros = &balance.AvailableMicros
		}
		if result[i].UserID != operator.ID {
			result[i].Command, result[i].Instruction = "", ""
		}
	}
	return result, nil
}

func UpdateUserScriptWithDB(db *gorm.DB, operator entity.User, scriptID int32, input UpdateUserScriptInput) (ScriptListItem, error) {
	var item ScriptListItem
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", scriptID)
		if !isAdminUser(operator) {
			query = query.Where("user_id = ?", operator.ID)
		}
		if err := query.First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		if script.EscrowClosingAt != nil {
			return ErrScriptEscrowClosePending
		}
		baseDomain := normalizeScriptDomain(input.BaseDomain)
		if baseDomain == "" {
			return ErrScriptDomainRequired
		}
		if err := validateScriptBaseDomain(baseDomain); err != nil {
			return err
		}
		if err := ensureScriptDomainRulePrefixAvailable(tx, baseDomain); err != nil {
			return err
		}
		scheme := normalizeScriptScheme(input.Scheme)
		if script.BillingMode != "" && scheme != "https" {
			return ErrScriptDomainUnavailable
		}
		updates := map[string]any{"description": strings.TrimSpace(input.Description), "base_domain": baseDomain, "scheme": scheme}
		if value := strings.TrimSpace(input.Name); value != "" {
			updates["name"] = value
		}
		parameterValues := input.Parameters
		if parameterValues == nil {
			parameterValues = input.PresetParams
		}
		if parameterValues != nil {
			raw, err := json.Marshal(parameterValues)
			if err != nil {
				return err
			}
			updates["parameters"] = string(raw)
		}
		if input.UseWildcardDomain != nil {
			updates["use_wildcard_domain"] = *input.UseWildcardDomain
		}
		if input.Enabled != nil {
			updates["enabled"] = *input.Enabled
		}
		if input.BillingEnabled != nil {
			updates["billing_enabled"] = *input.BillingEnabled
		}
		if useTikuPrice := input.UseTikuPrice; useTikuPrice != nil {
			updates["use_tiku_price"] = *useTikuPrice
		} else if input.BillingUseTikuPrice != nil {
			updates["use_tiku_price"] = *input.BillingUseTikuPrice
		}
		if input.AnswerPrice != nil {
			if math.IsNaN(*input.AnswerPrice) || math.IsInf(*input.AnswerPrice, 0) || *input.AnswerPrice < 0 || *input.AnswerPrice >= 1_000_000_000_000 {
				return errors.New("脚本答题单价必须为有效的非负金额")
			}
			updates["answer_price"] = util.RoundMoney(*input.AnswerPrice)
		}
		if input.PriceMultiplier != nil || input.AnswerPriceMultiplier != nil {
			multiplier := input.PriceMultiplier
			if multiplier == nil {
				multiplier = input.AnswerPriceMultiplier
			}
			if math.IsNaN(*multiplier) || math.IsInf(*multiplier, 0) || *multiplier < 0 || *multiplier >= 1_000_000_000_000 {
				return errors.New("脚本答题倍率必须是有效的非负数字")
			}
			updates["answer_price_multiplier"] = util.RoundMoney(*multiplier)
		}
		if script.BillingMode != "" {
			for _, field := range []string{"billing_enabled", "use_tiku_price", "answer_price", "answer_price_multiplier"} {
				delete(updates, field)
			}
			if input.RunDurationMinutes != nil {
				if *input.RunDurationMinutes < 0 || *input.RunDurationMinutes > int(ScriptRunSessionTTL/time.Minute) {
					return ErrScriptDurationInvalid
				}
				updates["run_duration_minutes"] = *input.RunDurationMinutes
			}
			if input.NotifyOnDenied != nil {
				updates["notify_on_denied"] = *input.NotifyOnDenied
			}
			if script.BillingMode == entity.ScriptBillingModeEscrow && input.AutoTopUp != nil {
				updates["auto_top_up"] = *input.AutoTopUp
			}
		}
		updatedScript := script
		updatedScript.BaseDomain = baseDomain
		updatedScript.Scheme = scheme
		if input.UseWildcardDomain != nil {
			updatedScript.UseWildcardDomain = *input.UseWildcardDomain
		}
		if err := ValidateScriptDomainWithDB(tx, baseDomain); err != nil {
			return err
		}
		updates["instruction"] = BuildScriptCommandURL(updatedScript)
		if err := tx.Model(&script).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Table(entity.TableNameUserScript+" AS user_scripts").Select("user_scripts.*, users.username AS username, script_versions.version AS version, script_versions.sha256 AS version_sha256").Joins("LEFT JOIN users ON users.id = user_scripts.user_id").Joins("LEFT JOIN script_versions ON script_versions.id = user_scripts.version_id").Where("user_scripts.id = ?", script.ID).First(&item).Error; err != nil {
			return err
		}
		item = decorateScriptItem(item)
		if err := attachScriptEscrowBalanceWithDB(tx, &item); err != nil {
			return err
		}
		if item.UserID != operator.ID {
			item.Command, item.Instruction = "", ""
		}
		return nil
	})
	return item, err
}

func DeleteUserScriptWithDB(db *gorm.DB, operator entity.User, scriptID int32) error {
	_, err := CloseUserScriptWithDB(db, operator, scriptID, 0)
	if errors.Is(err, ErrScriptActiveCloseConfirmationRequired) {
		return ErrScriptEscrowClosePending
	}
	return err
}

func IssueScriptExchangeTokenWithDB(db *gorm.DB, operator entity.User, scriptID int32, ttl time.Duration) (ScriptExchangeGrant, error) {
	if ttl <= 0 || ttl > ScriptExchangeTokenTTL {
		ttl = ScriptExchangeTokenTTL
	}
	token, err := randomScriptSecret(24)
	if err != nil {
		return ScriptExchangeGrant{}, err
	}
	expires := time.Now().Add(ttl)
	shortCode := ""
	if err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND deleted_at IS NULL AND enabled = ?", scriptID, operator.ID, true)
		if err := query.First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		shortCode = script.ShortCode
		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, script.UserID).Error; err != nil {
			return err
		}
		// A launcher ticket is a capability to consume the reserved script
		// balance. Keep at least ¥9 available after prior script answers so a
		// user cannot keep minting tickets while their account is overdrawn.
		if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") {
			return ErrScriptTokenInvalid
		}
		if err := validateScriptLaunchKeyWithDB(tx, script); err != nil {
			return err
		}
		if err := validateScriptEntitlementUnusedWithDB(tx, script); err != nil {
			return err
		}
		if script.BillingMode == "" && util.RoundMoney(user.Balance) < ScriptExchangeMinimumBalance {
			return ErrScriptTokenBalanceInsufficient
		}
		if err := deleteExpiredScriptExchangeTokens(tx, script.ID); err != nil {
			return err
		}
		var active int64
		if err := tx.Model(&entity.ScriptExchangeToken{}).
			Where("script_id = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", script.ID, "exchange", time.Now()).
			Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return ErrScriptExchangeAlreadyIssued
		}
		return tx.Create(&entity.ScriptExchangeToken{ScriptID: script.ID, VersionID: script.VersionID, Purpose: "exchange", TokenHash: hashScriptSecret(token), ExpiresAt: expires}).Error
	}); err != nil {
		return ScriptExchangeGrant{}, err
	}
	return ScriptExchangeGrant{ShortCode: shortCode, Token: token, ExpiresAt: expires}, nil
}

func ExchangeScriptTokenWithDB(db *gorm.DB, shortCode, token string) (ScriptExchangeResult, error) {
	shortCode, token = strings.TrimSpace(shortCode), strings.TrimSpace(token)
	if shortCode == "" || token == "" {
		return ScriptExchangeResult{}, ErrScriptTokenRequired
	}
	var result ScriptExchangeResult
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var grant entity.ScriptExchangeToken
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_hash = ?", hashScriptSecret(token)).First(&grant).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		if grant.Purpose != "exchange" || grant.UsedAt != nil || !time.Now().Before(grant.ExpiresAt) {
			return ErrScriptTokenInvalid
		}
		var script entity.UserScript
		if err := tx.Where("id = ? AND short_code = ? AND deleted_at IS NULL AND enabled = ?", grant.ScriptID, shortCode, true).First(&script).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		if err := validateScriptLaunchActorWithDB(tx, script); err != nil {
			return err
		}
		versionID := grant.VersionID
		if versionID <= 0 {
			versionID = script.VersionID
		}
		var version entity.ScriptVersion
		// The exchange response only needs release metadata. Avoid loading a
		// potentially 64 MiB executable into memory before the actual download.
		if err := tx.Select("id", "version", "filename", "sha256", "native_lease_protocol").First(&version, versionID).Error; err != nil {
			return ErrScriptTokenInvalid
		}
		if script.BillingMode != "" && !version.NativeLeaseProtocol {
			return ErrScriptNativeVersionRequired
		}
		now := time.Now()
		if err := consumeScriptExchangeTokenWithDB(tx, grant.ID, now); err != nil {
			return err
		}
		var parameters map[string]any
		if strings.TrimSpace(script.Parameters) != "" && json.Unmarshal([]byte(script.Parameters), &parameters) != nil {
			return errors.New("脚本参数损坏")
		}
		if parameters == nil {
			parameters = map[string]any{}
		}
		activationToken, err := randomScriptSecret(24)
		if err != nil {
			return err
		}
		activationExpiresAt := now.Add(ScriptExchangeTokenTTL)
		if err := tx.Create(&entity.ScriptExchangeToken{ScriptID: script.ID, VersionID: version.ID, Purpose: "activation", TokenHash: hashScriptSecret(activationToken), ExpiresAt: activationExpiresAt}).Error; err != nil {
			return err
		}
		result = ScriptExchangeResult{ScriptID: script.ID, Name: script.Name, Instruction: BuildScriptCommandURL(script), Parameters: parameters, Version: version.Version, Filename: version.Filename, DownloadURL: "/api/script-command/" + script.ShortCode + "/file", ActivationToken: activationToken, ActivationExpiresAt: activationExpiresAt, CommandURL: BuildScriptCommandURL(script), BootstrapURL: "/api/script-command/" + script.ShortCode + "/bootstrap", LaunchOrigin: ScriptCommandOrigin(script), ExecutableSHA256: version.SHA256, BillingMode: script.BillingMode, NotifyOnDenied: script.NotifyOnDenied}
		return nil
	})
	return result, err
}

func validateScriptLaunchKeyWithDB(db *gorm.DB, script entity.UserScript) error {
	if err := ValidateScriptDomainWithDB(db, script.BaseDomain); err != nil {
		return err
	}
	var key entity.AccessToken
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND script_id = ? AND user_id = ? AND status = ?", script.AccessTokenID, script.ID, script.UserID, "enabled").First(&key).Error; err != nil {
		return ErrScriptTokenInvalid
	}
	if !AccessTokenQuotaLimitValid(key.TotalQuotaLimit) {
		return ErrScriptTokenInvalid
	}
	return nil
}

func validateScriptLaunchActorWithDB(db *gorm.DB, script entity.UserScript) error {
	if err := validateScriptLaunchKeyWithDB(db, script); err != nil {
		return err
	}
	var user entity.User
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND status = ?", script.UserID, "enabled").First(&user).Error; err != nil {
		return ErrScriptTokenInvalid
	}
	return validateScriptEntitlementUnusedWithDB(db, script)
}

func validateScriptEntitlementUnusedWithDB(db *gorm.DB, script entity.UserScript) error {
	if script.BillingMode != entity.ScriptBillingModeActivation {
		return nil
	}
	grant, found, err := GetScriptActivationEntitlementByScriptWithDB(db, script.UserID, script.ID)
	if err != nil {
		return err
	}
	if !found {
		return ErrScriptActivationEntitlementUnavailable
	}
	if grant.RunConsumedAt != nil {
		return ErrScriptActivationRunConsumed
	}
	return nil
}

func consumeScriptExchangeTokenWithDB(db *gorm.DB, tokenID int64, now time.Time) error {
	used := db.Model(&entity.ScriptExchangeToken{}).Where("id = ? AND used_at IS NULL AND expires_at > ?", tokenID, now).Update("used_at", now)
	if used.Error != nil {
		return used.Error
	}
	if used.RowsAffected != 1 {
		return ErrScriptTokenInvalid
	}
	return nil
}

func normalizeScriptDomain(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return ""
	}
	host := strings.TrimRight(strings.TrimSpace(parsed.Host), ".")
	// ⚠️ 不要在这里剥 `*.`：旧实现 `strings.TrimPrefix(strings.ToLower(host), "*.")`
	// 会把 `*.a.zelly.cn` 归一化成 `a.zelly.cn`，于是「管理员允许泛域名」在入库时
	// 就退化成「只允许该精确域名」，通配语义永远无法生效
	// （45-*.md §0.25.2 / 72-*.md 三·红线 1）。`*` 的位置合法性由
	// validateScriptDomainWildcardPlacement 单独判定，这里只做小写与去空格。
	host = strings.ToLower(host)
	if host == "" || strings.ContainsAny(host, "'\"\\\r\n/?# \t") {
		return ""
	}
	return host
}
func normalizeScriptScheme(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "http" && value != "https" {
		return "https"
	}
	return value
}

// BuildScriptCommandURL returns the shortest public URL for a script.
// Wildcard mode uses a short host; path mode keeps the host unchanged and
// appends the short code as the only path segment.
func BuildScriptCommandURL(script entity.UserScript) string {
	origin := ScriptCommandOrigin(script)
	if origin == "" {
		return "/api/script-command/" + url.PathEscape(strings.TrimSpace(script.ShortCode)) + "/bootstrap"
	}
	if script.UseWildcardDomain {
		return origin
	}
	return origin + "/" + url.PathEscape(strings.TrimSpace(script.ShortCode))
}

// ScriptCommandOrigin only uses the validated stored script settings. Incoming
// Host or X-Forwarded-Proto headers cannot redirect bearer tickets/downloads.
func ScriptCommandOrigin(script entity.UserScript) string {
	domain := normalizeScriptDomain(script.BaseDomain)
	shortCode := strings.TrimSpace(script.ShortCode)
	if domain == "" || shortCode == "" {
		return ""
	}
	return normalizeScriptScheme(script.Scheme) + "://" + shortCode + "." + domain
}

const ScriptLauncherHeader = "X-Script-Launcher"

func scriptLauncherCapability(script entity.UserScript) string {
	if script.AccessKey == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(script.AccessKey))
	_, _ = mac.Write([]byte("tikuhelper/script-launcher/v1\x00" + script.ShortCode))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// BuildScriptCommand emits an owner-held, PowerShell 2 compatible command. The
// purpose-specific launcher capability goes in a header, never the URL. The
// encoded form works in Windows Run, cmd.exe and PowerShell without variable
// interpolation by the parent shell; it is not encryption.
func BuildScriptCommand(script entity.UserScript) string {
	commandURL := BuildScriptCommandURL(script)
	capability := scriptLauncherCapability(script)
	if !strings.HasPrefix(commandURL, "https://") || capability == "" {
		return ""
	}
	commandURL = strings.ReplaceAll(commandURL, "'", "''")
	program := "$w=New-Object Net.WebClient;$w.Headers.Add('" + ScriptLauncherHeader + "','" + capability + "');iex ($w.DownloadString('" + commandURL + "'))"
	if script.BillingMode != "" {
		program = BuildManagedScriptTextLoader(commandURL, ScriptLauncherHeader, capability)
		if program == "" {
			return ""
		}
	}
	units := utf16.Encode([]rune(program))
	wire := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(wire[i*2:], unit)
	}
	return "powershell -EncodedCommand " + base64.StdEncoding.EncodeToString(wire)
}

// BuildManagedScriptTextLoader never carries a script credential across an
// HTTP redirect, even if the configured origin returns one unexpectedly.
func BuildManagedScriptTextLoader(address, header, credential string) string {
	if !strings.HasPrefix(address, "https://") || credential == "" ||
		(header != ScriptLauncherHeader && header != "X-Script-Token") {
		return ""
	}
	quote := func(value string) string { return strings.ReplaceAll(value, "'", "''") }
	return "$q=[Net.HttpWebRequest]::Create('" + quote(address) + "');$q.AllowAutoRedirect=$false;$q.Timeout=15000;$q.ReadWriteTimeout=15000;" +
		"$q.Headers.Add('" + header + "','" + quote(credential) + "');$r=$q.GetResponse();" +
		"try{if([int]$r.StatusCode -ne 200){throw 'Script response rejected'};$s=New-Object IO.StreamReader($r.GetResponseStream());try{$body=$s.ReadToEnd()}finally{$s.Close()}}finally{$r.Close()};iex $body"
}

func decorateScriptItem(item ScriptListItem) ScriptListItem {
	item.Command = BuildScriptCommand(item.UserScript)
	item.DownloadURL = BuildScriptCommandURL(item.UserScript)
	item.Instruction = item.Command
	return item
}

// ResolvePublicScriptWithDB maps a request host/path to an enabled script.
// It intentionally compares normalized values in Go so wildcard and regular
// domain behavior remains identical on SQLite and MySQL.
//
// 匹配顺序（顺序本身就是安全边界，改顺序前先读 45-*.md §0.25.3）：
//
//  1. 请求路径规范化：`..` 段直接判不匹配（不做「Clean 后放行」）；
//  2. **保留前缀优先**：`/adapter-service/...` `/sqp/...` `/assets/...` 等命中即返回
//     ErrScriptNotFound —— 即使管理员配了 `*/*` 也不交给脚本入口；
//  3. 推导 shortCode：根路径用 host 首段（泛域名模式），否则用路径**最后一段**
//     （段级 glob 允许 `a.com/a/*` 这类更深的公开路径）；
//  4. 命中 shortCode 的脚本再校验 host == `<shortCode>.<baseDomain>`；
//  5. 最后过管理员域名规则（含路径 glob）——多段路径必须有显式规则才放行。
func ResolvePublicScriptWithDB(db *gorm.DB, requestHost, requestPath string) (entity.UserScript, error) {
	host := normalizePublicScriptHost(requestHost)
	cleanPath, pathErr := cleanScriptRequestPath(requestPath)
	if pathErr != nil {
		// 含 `..` 或非绝对路径：一律当作「不是脚本入口」，不给穿越任何机会。
		return entity.UserScript{}, ErrScriptNotFound
	}
	// 保留前缀判定必须在任何通配匹配之前：`/a/**` 不能吞掉 `/adapter-service`。
	if segment := scriptFirstPathSegment(cleanPath); segment != "" && scriptReservedPrefix(segment) {
		return entity.UserScript{}, ErrScriptNotFound
	}
	shortCode, wildcard := "", false
	if cleanPath == "" {
		separator := strings.IndexByte(host, '.')
		if separator <= 0 || separator == len(host)-1 {
			return entity.UserScript{}, ErrScriptNotFound
		}
		shortCode, wildcard = host[:separator], true
	} else {
		segments := scriptPathSegments(cleanPath)
		shortCode = segments[len(segments)-1]
		// 短码只可能是字母数字（uniqueScriptShortCode 的字母表是 a-z0-9），
		// 这里顺手挡掉 `/a/b/`、`/a/%2e%2e` 之类的畸形末段。
		if !isScriptShortCodeSegment(shortCode) {
			return entity.UserScript{}, ErrScriptNotFound
		}
	}
	var script entity.UserScript
	if err := db.Session(&gorm.Session{NewDB: true}).Where("short_code = ? AND use_wildcard_domain = ? AND deleted_at IS NULL AND enabled = ?", shortCode, wildcard, true).First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.UserScript{}, ErrScriptNotFound
		}
		return entity.UserScript{}, err
	}
	domain := normalizeScriptDomain(script.BaseDomain)
	scriptHost := normalizePublicScriptHost(strings.ToLower(script.ShortCode) + "." + domain)
	if domain == "" || !strings.EqualFold(host, scriptHost) {
		return entity.UserScript{}, ErrScriptNotFound
	}
	rules, err := scriptDomainRulesWithDB(db)
	if err != nil {
		return entity.UserScript{}, err
	}
	if !authorizeScriptPublicPath(rules, domain, cleanPath) {
		return entity.UserScript{}, ErrScriptNotFound
	}
	return script, nil
}

// isScriptShortCodeSegment 判断路径末段能否充当 shortCode。
// 与 uniqueScriptShortCode 的字母表保持一致（a-z0-9，最长 12 位）。
func isScriptShortCodeSegment(segment string) bool {
	if segment == "" || len(segment) > 16 {
		return false
	}
	for _, char := range segment {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func normalizePublicScriptHost(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else {
		value = strings.TrimPrefix(value, "[")
		value = strings.TrimSuffix(value, "]")
	}
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), ".")
}

// IssuePublicScriptExchangeTokenWithDB requires an owner-held launcher
// capability. Neither the short path nor the request Host is authentication.
func IssuePublicScriptExchangeTokenWithDB(db *gorm.DB, shortCode, capability string, ttl time.Duration) (ScriptExchangeGrant, error) {
	if ttl <= 0 || ttl > ScriptExchangeTokenTTL {
		ttl = ScriptExchangeTokenTTL
	}
	token, err := randomScriptSecret(24)
	if err != nil {
		return ScriptExchangeGrant{}, err
	}
	expires := time.Now().Add(ttl)
	resolvedShortCode := ""
	if err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("short_code = ? AND deleted_at IS NULL AND enabled = ?", strings.TrimSpace(shortCode), true).First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		expected := scriptLauncherCapability(script)
		if normalizeScriptScheme(script.Scheme) != "https" || expected == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(capability)) != 1 {
			return ErrScriptNotFound
		}
		resolvedShortCode = script.ShortCode
		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, script.UserID).Error; err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") {
			return ErrScriptTokenInvalid
		}
		if err := validateScriptLaunchKeyWithDB(tx, script); err != nil {
			return err
		}
		if util.RoundMoney(user.Balance) < ScriptExchangeMinimumBalance {
			return ErrScriptTokenBalanceInsufficient
		}
		if err := deleteExpiredScriptExchangeTokens(tx, script.ID); err != nil {
			return err
		}
		var active int64
		if err := tx.Model(&entity.ScriptExchangeToken{}).
			Where("script_id = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", script.ID, "exchange", time.Now()).
			Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return ErrScriptExchangeAlreadyIssued
		}
		return tx.Create(&entity.ScriptExchangeToken{ScriptID: script.ID, VersionID: script.VersionID, Purpose: "exchange", TokenHash: hashScriptSecret(token), ExpiresAt: expires}).Error
	}); err != nil {
		return ScriptExchangeGrant{}, err
	}
	return ScriptExchangeGrant{ShortCode: resolvedShortCode, Token: token, ExpiresAt: expires}, nil
}

func deleteExpiredScriptExchangeTokens(tx *gorm.DB, scriptID int32) error {
	return tx.Where("script_id = ? AND (used_at IS NOT NULL OR expires_at < ?)", scriptID, time.Now()).Delete(&entity.ScriptExchangeToken{}).Error
}

func DownloadScriptVersionWithDB(db *gorm.DB, id int32) (entity.ScriptVersion, error) {
	var version entity.ScriptVersion
	err := db.Session(&gorm.Session{NewDB: true}).First(&version, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.ScriptVersion{}, ErrScriptVersionNotFound
	}
	return version, err
}

// DownloadPublicScriptVersionWithDB validates the dedicated script key before
// returning the executable bound to the active script version.
func DownloadPublicScriptVersionWithDB(db *gorm.DB, shortCode, accessKey string) (entity.ScriptVersion, error) {
	shortCode = strings.TrimSpace(shortCode)
	accessKey = strings.TrimSpace(accessKey)
	if shortCode == "" || accessKey == "" {
		return entity.ScriptVersion{}, ErrScriptTokenRequired
	}
	var script entity.UserScript
	if err := db.Session(&gorm.Session{NewDB: true}).Where("short_code = ? AND deleted_at IS NULL AND enabled = ?", shortCode, true).First(&script).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.ScriptVersion{}, ErrScriptNotFound
		}
		return entity.ScriptVersion{}, err
	}
	if script.AccessTokenID <= 0 {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	var accessToken entity.AccessToken
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND script_id = ? AND status = ? AND token_hash = ?", script.AccessTokenID, script.ID, "enabled", hashScriptSecret(accessKey)).First(&accessToken).Error; err != nil {
		return entity.ScriptVersion{}, ErrScriptTokenInvalid
	}
	return DownloadScriptVersionWithDB(db, script.VersionID)
}

func randomScriptSecret(bytesLen int) (string, error) {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashScriptSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func uniqueScriptShortCode(tx *gorm.DB) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	for length, total := 1, 36; length <= 3; length, total = length+1, total*36 {
		for index := 0; index < total; index++ {
			code := shortCodeFromIndex(index, length, alphabet)
			var count int64
			if err := tx.Model(&entity.UserScript{}).Where("short_code = ?", code).Count(&count).Error; err != nil {
				return "", err
			}
			if count == 0 {
				return code, nil
			}
		}
	}
	for i := 0; i < 8; i++ {
		code, err := randomScriptSecret(6)
		if err != nil {
			return "", err
		}
		code = code[:12]
		var count int64
		if err := tx.Model(&entity.UserScript{}).Where("short_code = ?", code).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return code, nil
		}
	}
	return "", errors.New("生成脚本短地址失败")
}

func shortCodeFromIndex(index, length int, alphabet string) string {
	result := make([]byte, length)
	for position := length - 1; position >= 0; position-- {
		result[position] = alphabet[index%len(alphabet)]
		index /= len(alphabet)
	}
	return string(result)
}

func filepathExt(name string) string {
	name = strings.TrimSpace(name)
	idx := strings.LastIndexByte(name, '.')
	if idx < 0 {
		return ""
	}
	return name[idx:]
}

func safeScriptFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return "script.exe"
	}
	return name
}
