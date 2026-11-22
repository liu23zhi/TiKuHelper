package service

import (
	"errors"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func scriptAuthorizationStatus(enabled bool, starts, expires *time.Time, maximum, used int64, now time.Time) string {
	if !enabled {
		return "disabled"
	}
	if expires != nil && !now.Before(*expires) {
		return "expired"
	}
	if maximum > 0 && used >= maximum {
		return "exhausted"
	}
	if starts != nil && now.Before(*starts) {
		return "scheduled"
	}
	return "active"
}

var ErrScriptFullNeverExpireDisabled = errors.New("管理员未允许长命令永不过期，请设置结束时间")

func scriptAuthorizationWindow(db *gorm.DB, input ConfigureScriptShortAuthorizationInput, short bool) (*time.Time, *time.Time, error) {
	if input.MaxUses < 0 || input.MaxUses > 1000000 {
		return nil, nil, ErrScriptAuthorizationLimitInvalid
	}
	limit := 365 * 24 * 60
	if short {
		limit = ScriptShortAuthorizationMaxMinutes
	}
	if input.DurationMinutes < 0 || input.DurationMinutes > limit {
		if short {
			return nil, nil, ErrScriptShortDurationInvalid
		}
		return nil, nil, ErrScriptAuthorizationWindowInvalid
	}
	if input.DurationMinutes == 0 && input.ExpiresAt == nil {
		settings, err := GetScriptDomainSettingsWithDB(db)
		if err != nil {
			return nil, nil, err
		}
		if short && !*settings.AllowShortNeverExpire {
			return nil, nil, ErrScriptShortDurationInvalid
		}
		if !short && !*settings.AllowFullNeverExpire {
			return nil, nil, ErrScriptFullNeverExpireDisabled
		}
	}
	now, base := time.Now(), time.Now()
	var starts, expires *time.Time
	if input.StartsAt != nil {
		// Existing GORM timestamps and conditional SQL writes use the server's
		// local clock. Preserve the instant while normalizing RFC3339 input so
		// SQLite does not compare UTC and local wall-clock strings as different
		// deadlines (MySQL DATETIME likewise has no stored timezone).
		value := input.StartsAt.In(time.Local)
		starts = &value
		if value.After(now.Add(365 * 24 * time.Hour)) {
			return nil, nil, ErrScriptAuthorizationWindowInvalid
		}
		if value.After(base) {
			base = value
		}
	}
	if input.ExpiresAt != nil {
		value := input.ExpiresAt.In(time.Local)
		expires = &value
	} else if input.DurationMinutes > 0 {
		value := base.Add(time.Duration(input.DurationMinutes) * time.Minute)
		expires = &value
	}
	if expires != nil && (!expires.After(base) || expires.After(base.Add(time.Duration(limit)*time.Minute))) {
		return nil, nil, ErrScriptAuthorizationWindowInvalid
	}
	return starts, expires, nil
}

func scriptLauncherAuthorizationView(script entity.UserScript) *ScriptShortAuthorizationView {
	view := &ScriptShortAuthorizationView{Address: BuildScriptCommandURL(script), StartsAt: script.LauncherStartsAt, ExpiresAt: script.LauncherExpiresAt, MaxUses: script.LauncherMaxUses, UsedCount: script.LauncherUsedCount}
	view.Status, view.Windows = storedScriptAuthorizationStatus(script, false, time.Now())
	if view.Status == "active" || view.Status == "scheduled" || view.Status == "paused" {
		view.Command = BuildScriptCommand(script)
	}
	return view
}

func ConfigureScriptLauncherAuthorizationWithDB(db *gorm.DB, operator entity.User, scriptID int32, input ConfigureScriptShortAuthorizationInput) (ScriptShortAuthorizationView, error) {
	var result ScriptShortAuthorizationView
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND deleted_at IS NULL", scriptID, operator.ID).First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		updates := map[string]any{"launcher_disabled": !input.Enabled, "launcher_generation": gorm.Expr("launcher_generation + 1")}
		if input.Enabled {
			if !script.Enabled || script.EscrowClosingAt != nil || normalizeScriptScheme(script.Scheme) != "https" {
				return ErrScriptNotFound
			}
			if err := validateScriptLaunchActorWithDB(tx, script); err != nil {
				return err
			}
			starts, expires, windows, err := configureScriptAuthorizationSchedule(tx, input, false)
			if err != nil {
				return err
			}
			updates["launcher_starts_at"], updates["launcher_expires_at"], updates["launcher_max_uses"], updates["launcher_used_count"] = starts, expires, input.MaxUses, 0
			updates["launcher_windows"] = windows
			if script.LauncherLocator != nil {
				locator, err := rotateScriptLocatorInTx(tx, script)
				if err != nil {
					return err
				}
				updates["launcher_locator"] = locator
				updates["launcher_bearer_enabled"] = true
			}
		}
		if err := tx.Model(&script).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.First(&script, script.ID).Error; err != nil {
			return err
		}
		result = *scriptLauncherAuthorizationView(script)
		return nil
	})
	return result, err
}

func consumeScriptLauncherAuthorizationInTx(tx *gorm.DB, script entity.UserScript, now time.Time) error {
	status, _ := storedScriptAuthorizationStatus(script, false, now)
	if status != "active" {
		return ErrScriptNotFound
	}
	claimed := tx.Model(&entity.UserScript{}).Where("id = ? AND launcher_generation = ? AND launcher_used_count = ? AND launcher_disabled = ?", script.ID, script.LauncherGeneration, script.LauncherUsedCount, false).UpdateColumn("launcher_used_count", gorm.Expr("launcher_used_count + 1"))
	if claimed.Error != nil {
		return claimed.Error
	}
	if claimed.RowsAffected != 1 {
		return ErrScriptNotFound
	}
	return nil
}

// The entry already consumed its count when the ticket was issued. Subsequent
// exchange checks only the authorization generation/time, so revocation and
// expiry still apply without charging a second use for the same start.
func validateIssuedScriptAuthorization(script entity.UserScript, grant entity.ScriptExchangeToken, now time.Time) error {
	var starts, expires *time.Time
	var raw string
	switch grant.AuthorizationKind {
	case "full":
		if script.LauncherDisabled || script.LauncherGeneration != grant.AuthorizationGeneration {
			return ErrScriptTokenInvalid
		}
		starts, expires = script.LauncherStartsAt, script.LauncherExpiresAt
		raw = script.LauncherWindows
	case "short":
		if script.ShortEntryGeneration != grant.AuthorizationGeneration {
			return ErrScriptTokenInvalid
		}
		starts, expires = script.ShortEntryStartsAt, script.ShortEntryExpiresAt
		raw = script.ShortEntryWindows
	default:
		return nil
	}
	if raw != "" {
		windows, err := decodeScriptAuthorizationWindows(raw, grant.AuthorizationKind == "short")
		if err != nil || scriptAuthorizationWindowsStatus(true, windows, 0, 0, now) != "active" {
			return ErrScriptTokenInvalid
		}
		return nil
	}
	if (starts != nil && now.Before(*starts)) || (expires != nil && !now.Before(*expires)) {
		return ErrScriptTokenInvalid
	}
	return nil
}
