package service

import (
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// Legacy fields remain a coarse outer envelope for old readers and SQL guards.
// Every entry and unused ticket must also pass the exact window membership check.
func configureScriptAuthorizationSchedule(db *gorm.DB, input ConfigureScriptShortAuthorizationInput, short bool) (*time.Time, *time.Time, string, error) {
	if input.Windows == nil {
		starts, expires, err := scriptAuthorizationWindow(db, input, short)
		return starts, expires, "", err
	}
	if input.StartsAt != nil || input.ExpiresAt != nil || input.DurationMinutes != 0 || len(input.Windows) == 0 {
		return nil, nil, "", ErrScriptAuthorizationWindowInvalid
	}
	if input.MaxUses < 0 || input.MaxUses > 1000000 {
		return nil, nil, "", ErrScriptAuthorizationLimitInvalid
	}
	windows, err := normalizeScriptAuthorizationWindows(input.Windows, short, time.Now())
	if err != nil {
		return nil, nil, "", err
	}
	raw, err := encodeScriptAuthorizationWindows(windows)
	if err != nil {
		return nil, nil, "", err
	}
	// SQL DATETIME has no zone; keep its outer envelope on the same local
	// clock as the existing conditional writes. JSON retains exact UTC instants.
	starts, expires := windows[0].StartsAt.In(time.Local), windows[len(windows)-1].ExpiresAt.In(time.Local)
	return &starts, &expires, raw, nil
}

func storedScriptAuthorizationStatus(script entity.UserScript, short bool, now time.Time) (string, []ScriptAuthorizationWindow) {
	starts, expires, raw := script.LauncherStartsAt, script.LauncherExpiresAt, script.LauncherWindows
	maximum, used := script.LauncherMaxUses, script.LauncherUsedCount
	enabled := !script.LauncherDisabled
	if short {
		starts, expires, raw = script.ShortEntryStartsAt, script.ShortEntryExpiresAt, script.ShortEntryWindows
		maximum, used, enabled = script.ShortEntryMaxUses, script.ShortEntryUsedCount, script.ShortEntryAddressKey != nil
	}
	enabled = enabled && script.Enabled && script.DeletedAt == nil && script.EscrowClosingAt == nil
	if raw == "" {
		return scriptAuthorizationStatus(enabled, starts, expires, maximum, used, now), nil
	}
	windows, err := decodeScriptAuthorizationWindows(raw, short)
	if err != nil {
		return "disabled", nil
	}
	return scriptAuthorizationWindowsStatus(enabled, windows, maximum, used, now), windows
}
