package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ScriptShortAuthorizationMaxMinutes = 24 * 60

var (
	ErrScriptShortDurationInvalid       = errors.New("短命令有效期须为 1 至 1440 分钟的整数")
	ErrScriptShortAddressInvalid        = errors.New("请选择管理员允许的 HTTPS 子域或路径作为短命令入口")
	ErrScriptShortAddressInUse          = errors.New("此短命令入口已被使用，请更换子域或路径")
	ErrScriptShortAuthorizationInactive = fmt.Errorf("%w：短命令尚未生效、已过期、次数已用完或已撤销", ErrScriptNotFound)
	ErrScriptAuthorizationLimitInvalid  = errors.New("使用次数须为 0 至 1000000 的整数，0 表示不限次数")
	ErrScriptAuthorizationWindowInvalid = errors.New("授权时间无效：结束时间须晚于生效时间；长命令最长 365 天，短命令最长 24 小时")
)

type ConfigureScriptShortAuthorizationInput struct {
	Enabled         bool       `json:"enabled"`
	Address         string     `json:"address"`
	DomainFormat    string     `json:"domainFormat"`
	DurationMinutes int        `json:"durationMinutes"`
	StartsAt        *time.Time `json:"startsAt"`
	ExpiresAt       *time.Time `json:"expiresAt"`
	MaxUses         int64      `json:"maxUses"`
	RequestHost     string     `json:"-"`
	RequestHTTPS    bool       `json:"-"`
}

type ScriptShortAuthorizationView struct {
	Address   string     `json:"address"`
	Command   string     `json:"command"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	StartsAt  *time.Time `json:"startsAt,omitempty"`
	MaxUses   int64      `json:"maxUses"`
	UsedCount int64      `json:"usedCount"`
}

func scriptShortAuthorizationView(script entity.UserScript) *ScriptShortAuthorizationView {
	if script.ShortEntryAddress == "" {
		return nil
	}
	view := &ScriptShortAuthorizationView{Address: script.ShortEntryAddress, ExpiresAt: script.ShortEntryExpiresAt, StartsAt: script.ShortEntryStartsAt, UsedAt: script.ShortEntryUsedAt, MaxUses: script.ShortEntryMaxUses, UsedCount: script.ShortEntryUsedCount}
	view.Status = scriptAuthorizationStatus(script.ShortEntryAddressKey != nil && script.Enabled && script.DeletedAt == nil && script.EscrowClosingAt == nil, view.StartsAt, view.ExpiresAt, view.MaxUses, view.UsedCount, time.Now())
	if script.ShortEntryMaxUses > 0 && script.ShortEntryUsedCount >= script.ShortEntryMaxUses {
		view.Status = "exhausted"
	}
	if view.Status == "active" || view.Status == "scheduled" {
		view.Command = `powershell -c "iex(irm ` + script.ShortEntryAddress + `)"`
	}
	return view
}

// fallbackHost limits a new short entry to the console host when no formats
// are configured. A persisted grant passes its own already validated host.
func validateScriptShortAddressWithDB(db *gorm.DB, address, fallbackHost string) (string, string, error) {
	address = strings.TrimSpace(address)
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || strings.Contains(address, "%") {
		return "", "", ErrScriptShortAddressInvalid
	}
	host := normalizeScriptLauncherHost(parsed.Host)
	if host == "" {
		return "", "", ErrScriptShortAddressInvalid
	}
	path, err := cleanScriptRequestPath(parsed.Path)
	if err != nil || (path != "" && path != parsed.Path) || scriptReservedPrefix(scriptFirstPathSegment(path)) {
		return "", "", ErrScriptShortAddressInvalid
	}
	if path != "" {
		normalized, err := normalizeScriptDomainSlot(ScriptDomainFormatPath, strings.TrimPrefix(path, "/"))
		if err != nil || "/"+normalized != path {
			return "", "", ErrScriptShortAddressInvalid
		}
	}
	formats, err := ScriptDomainFormatsWithDB(db)
	if err != nil {
		return "", "", err
	}
	if len(formats) == 0 && path != "" && host == normalizeScriptLauncherHost(fallbackHost) {
		return "https://" + host + path, "https://" + host, nil
	}
	if parsed.Port() != "" {
		return "", "", ErrScriptShortAddressInvalid
	}
	for _, format := range formats {
		if scriptDomainEntryCovers(format, host, path) {
			return "https://" + host + path, "https://" + host, nil
		}
	}
	return "", "", ErrScriptShortAddressInvalid
}

func shortEntryAddressKey(host, path string) string { return hashScriptSecret(host + "\x00" + path) }

func allocateScriptShortAddress(tx *gorm.DB, scriptID int32, input ConfigureScriptShortAuthorizationInput) (string, string, error) {
	reserve := func(address string) (string, string, error) {
		if err := checkScriptAddressAvailable(tx, address, scriptAddressShort, scriptID); err != nil {
			return "", "", err
		}
		if err := reserveScriptAddress(tx, address, scriptAddressShort, scriptID); err != nil {
			return "", "", err
		}
		_, _, key := parseScriptReservedAddress(address)
		return address, key, nil
	}
	if strings.TrimSpace(input.Address) != "" {
		address, _, err := validateScriptShortAddressWithDB(tx, input.Address, input.RequestHost)
		if err != nil {
			return "", "", err
		}
		return reserve(address)
	}
	const entropyBytes = 16
	const prefix = "s-"
	origin := ScriptLauncherOrigin{Host: input.RequestHost, HTTPS: input.RequestHTTPS}
	domain, wildcard, err := selectScriptAddressDomain(tx, input.DomainFormat, origin, len(prefix)+2*entropyBytes)
	if err != nil {
		return "", "", err
	}
	for range 10 {
		var secret [entropyBytes]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return "", "", err
		}
		address := scriptAddressWithSlot(domain, prefix+hex.EncodeToString(secret[:]), wildcard)
		// Automatic issuance always rotates the address, even for the same script.
		if err := checkScriptAddressAvailable(tx, address, scriptAddressShort, 0); err != nil {
			if errors.Is(err, ErrScriptShortAddressInUse) {
				continue
			}
			return "", "", err
		}
		if err := reserveScriptAddress(tx, address, scriptAddressShort, scriptID); err != nil {
			if errors.Is(err, ErrScriptShortAddressInUse) {
				continue
			}
			return "", "", err
		}
		_, _, key := parseScriptReservedAddress(address)
		return address, key, nil
	}
	return "", "", ErrScriptShortAddressInUse
}

// Only the owner may make a script publicly launchable. Administrators may
// still manage scripts through the normal endpoints, which never grant them
// another owner's launcher capability.
func ConfigureScriptShortAuthorizationWithDB(db *gorm.DB, operator entity.User, scriptID int32, input ConfigureScriptShortAuthorizationInput) (ScriptShortAuthorizationView, error) {
	var result ScriptShortAuthorizationView
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND deleted_at IS NULL", scriptID, operator.ID).First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		updates := map[string]any{"short_entry_address_key": nil, "short_entry_generation": gorm.Expr("short_entry_generation + 1")}
		if input.Enabled {
			starts, expires, err := scriptAuthorizationWindow(tx, input, true)
			if err != nil {
				return err
			}
			if !script.Enabled || script.EscrowClosingAt != nil || normalizeScriptScheme(script.Scheme) != "https" {
				return ErrScriptNotFound
			}
			if err := validateScriptLaunchActorWithDB(tx, script); err != nil {
				return err
			}
			if err := preservePreviousScriptAddress(tx, script.ShortEntryAddress, scriptAddressShort, script.ID); err != nil {
				return err
			}
			address, key, err := allocateScriptShortAddress(tx, script.ID, input)
			if err != nil {
				return err
			}
			updates["short_entry_address"], updates["short_entry_address_key"], updates["short_entry_expires_at"] = address, key, expires
			updates["short_entry_starts_at"], updates["short_entry_max_uses"], updates["short_entry_used_count"], updates["short_entry_used_at"] = starts, input.MaxUses, 0, nil
		}
		if err := tx.Model(&script).Updates(updates).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrScriptShortAddressInUse
			}
			return err
		}
		if err := tx.First(&script, script.ID).Error; err != nil {
			return err
		}
		if view := scriptShortAuthorizationView(script); view != nil {
			result = *view
		} else {
			result.Status = "disabled"
		}
		return nil
	})
	return result, err
}

// The compact URL is itself the explicitly enabled, limited capability. This
// separate flow does not bypass or relax the full launcher's header check.
func ConsumeScriptShortAuthorizationWithDB(db *gorm.DB, requestHost, requestPath string) (entity.UserScript, ScriptExchangeGrant, error) {
	var script entity.UserScript
	var grant ScriptExchangeGrant
	host := normalizeScriptLauncherHost(requestHost)
	path, err := cleanScriptRequestPath(requestPath)
	if host == "" || err != nil || scriptReservedPrefix(scriptFirstPathSegment(path)) {
		return script, grant, ErrScriptNotFound
	}
	key := shortEntryAddressKey(host, path)
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("short_entry_address_key = ? AND deleted_at IS NULL AND enabled = ?", key, true).First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				var known int64
				if lookupErr := tx.Model(&entity.ScriptAddressReservation{}).Where("address_key = ? AND kind = ?", key, scriptAddressShort).Count(&known).Error; lookupErr != nil {
					return lookupErr
				}
				if known > 0 {
					return ErrScriptShortAuthorizationInactive
				}
				if lookupErr := tx.Model(&entity.UserScript{}).Where("short_entry_address = ?", "https://"+host+path).Count(&known).Error; lookupErr != nil {
					return lookupErr
				}
				if known > 0 {
					return ErrScriptShortAuthorizationInactive
				}
				return ErrScriptNotFound
			}
			return err
		}
		now := time.Now()
		var blocked int64
		if err := tx.Model(&entity.ScriptAddressReservation{}).Where("address_key = ? AND script_id = ?", key, blockedScriptAddressOwnerID).Count(&blocked).Error; err != nil {
			return err
		}
		if blocked > 0 {
			return ErrScriptShortAuthorizationInactive
		}
		if scriptAuthorizationStatus(true, script.ShortEntryStartsAt, script.ShortEntryExpiresAt, script.ShortEntryMaxUses, script.ShortEntryUsedCount, now) != "active" || script.EscrowClosingAt != nil {
			return ErrScriptShortAuthorizationInactive
		}
		address, origin, err := validateScriptShortAddressWithDB(tx, script.ShortEntryAddress, host)
		if err != nil {
			return ErrScriptShortAuthorizationInactive
		}
		parsed, _ := url.Parse(address)
		if parsed.Host != host || parsed.Path != path {
			return ErrScriptNotFound
		}
		var owner entity.User
		if err := tx.First(&owner, script.UserID).Error; err != nil {
			return err
		}
		// Conditional counters protect the same budget on SQLite and MySQL.
		updates := map[string]any{"short_entry_used_at": now, "short_entry_used_count": gorm.Expr("short_entry_used_count + 1")}
		if script.ShortEntryMaxUses > 0 && script.ShortEntryUsedCount+1 >= script.ShortEntryMaxUses {
			updates["short_entry_address_key"] = nil
		}
		claimed := tx.Model(&entity.UserScript{}).Where("id = ? AND short_entry_address_key = ? AND short_entry_used_count = ? AND (short_entry_expires_at IS NULL OR short_entry_expires_at > ?)", script.ID, key, script.ShortEntryUsedCount, now).Updates(updates)
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return ErrScriptNotFound
		}
		grant, err = IssueScriptExchangeTokenWithDB(tx, owner, script.ID, ScriptExchangeTokenTTL)
		if err != nil {
			return err
		}
		if err := tx.Model(&entity.ScriptExchangeToken{}).Where("token_hash = ?", hashScriptSecret(grant.Token)).Updates(map[string]any{"launch_address": address, "authorization_kind": "short", "authorization_generation": script.ShortEntryGeneration}).Error; err != nil {
			return err
		}
		grant.LaunchOrigin = origin
		return nil
	})
	return script, grant, err
}
