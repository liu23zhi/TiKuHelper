package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestScriptAuthorizationWindowsNormalizeSortsWithoutChangingInput(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	earlier := ScriptAuthorizationWindow{StartsAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	later := ScriptAuthorizationWindow{StartsAt: earlier.ExpiresAt, ExpiresAt: now.Add(time.Hour)}
	input := []ScriptAuthorizationWindow{later, earlier}

	windows, err := normalizeScriptAuthorizationWindows(input, true, now)
	if err != nil || len(windows) != 2 || !windows[0].StartsAt.Equal(earlier.StartsAt) || !windows[1].StartsAt.Equal(later.StartsAt) || input[0] != later {
		t.Fatalf("adjacent windows including an elapsed window did not normalize: %+v, %v", windows, err)
	}
}

func TestScriptAuthorizationWindowsRejectInvalidSchedules(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	tooMany := make([]ScriptAuthorizationWindow, 33)
	for i := range tooMany {
		starts := now.Add(time.Duration(i) * time.Hour)
		tooMany[i] = ScriptAuthorizationWindow{StartsAt: starts, ExpiresAt: starts.Add(time.Hour)}
	}
	tests := []struct {
		name    string
		short   bool
		windows []ScriptAuthorizationWindow
	}{
		{"missing start", false, []ScriptAuthorizationWindow{{ExpiresAt: now.Add(time.Hour)}}},
		{"missing end", false, []ScriptAuthorizationWindow{{StartsAt: now}}},
		{"zero-length window", false, []ScriptAuthorizationWindow{{StartsAt: now, ExpiresAt: now}}},
		{"past only", false, []ScriptAuthorizationWindow{{StartsAt: now.Add(-time.Hour), ExpiresAt: now}}},
		{"short duration beyond 24h", true, []ScriptAuthorizationWindow{{StartsAt: now, ExpiresAt: now.Add(24*time.Hour + time.Second)}}},
		{"long duration beyond 365d", false, []ScriptAuthorizationWindow{{StartsAt: now, ExpiresAt: now.Add(365*24*time.Hour + time.Second)}}},
		{"start beyond 365d", false, []ScriptAuthorizationWindow{{StartsAt: now.Add(366 * 24 * time.Hour), ExpiresAt: now.Add(367 * 24 * time.Hour)}}},
		{"overlap", false, []ScriptAuthorizationWindow{
			{StartsAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour)},
			{StartsAt: now, ExpiresAt: now.Add(2 * time.Hour)},
		}},
		{"over 32 windows", false, tooMany},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := normalizeScriptAuthorizationWindows(tt.windows, tt.short, now); !errors.Is(err, ErrScriptAuthorizationWindowInvalid) {
				t.Fatalf("expected a window validation error, got %v", err)
			}
		})
	}
	if windows, err := normalizeScriptAuthorizationWindows(tooMany[:32], false, now); err != nil || len(windows) != 32 {
		t.Fatalf("32 adjacent windows should be accepted: %v, %v", windows, err)
	}
}

func TestScriptAuthorizationWindowsStatusUsesHalfOpenIntervalsAndSharedBudget(t *testing.T) {
	firstStart := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	firstEnd := firstStart.Add(time.Hour)
	secondStart := firstEnd.Add(time.Hour)
	secondEnd := secondStart.Add(time.Hour)
	windows := []ScriptAuthorizationWindow{{StartsAt: firstStart, ExpiresAt: firstEnd}, {StartsAt: secondStart, ExpiresAt: secondEnd}}
	tests := []struct {
		name    string
		at      time.Time
		enabled bool
		used    int64
		status  string
	}{
		{"ahead of first window", firstStart.Add(-time.Nanosecond), true, 0, "scheduled"},
		{"first start included", firstStart, true, 0, "active"},
		{"first end excluded", firstEnd, true, 0, "paused"},
		{"second start included", secondStart, true, 0, "active"},
		{"shared budget still exhausted in second window", secondStart, true, 1, "exhausted"},
		{"final end excluded", secondEnd, true, 0, "expired"},
		{"disabled during window", firstStart, false, 0, "disabled"},
		{"disabled after end", secondEnd, false, 0, "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scriptAuthorizationWindowsStatus(tt.enabled, windows, 1, tt.used, tt.at); got != tt.status {
				t.Fatalf("status = %q, want %q", got, tt.status)
			}
		})
	}
}

func TestScriptAuthorizationWindowsDecodePreservesElapsedSchedulesAndRejectsCorruption(t *testing.T) {
	start := time.Date(2025, 5, 6, 7, 8, 9, 123, time.FixedZone("fixture", 8*60*60))
	windows := []ScriptAuthorizationWindow{{StartsAt: start, ExpiresAt: start.Add(time.Hour)}}
	raw, err := encodeScriptAuthorizationWindows(windows)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeScriptAuthorizationWindows(raw, true)
	if err != nil || len(decoded) != 1 || !decoded[0].StartsAt.Equal(windows[0].StartsAt) || !decoded[0].ExpiresAt.Equal(windows[0].ExpiresAt) {
		t.Fatalf("round trip changed an elapsed window: %+v, %v", decoded, err)
	}
	if raw, err := encodeScriptAuthorizationWindows(nil); err != nil || raw != "" {
		t.Fatalf("legacy schedule encoding = %q, %v", raw, err)
	}
	if windows, err := decodeScriptAuthorizationWindows("", true); err != nil || windows != nil {
		t.Fatalf("legacy schedule decoded as %+v, %v", windows, err)
	}
	for _, corrupt := range []string{
		" ", "[]", "null", "{", raw + " null",
		`[{"startsAt":"2026-03-01T01:00:00Z"}]`,
		`[{"startsAt":"2026-03-01T01:00:00Z","expiresAt":"2026-03-01T02:00:00Z","unknown":true}]`,
		`[{"startsAt":"2026-03-01T01:00:00Z","expiresAt":"2026-03-03T01:00:00Z"}]`,
		`[{"startsAt":"2026-03-02T01:00:00Z","expiresAt":"2026-03-02T02:00:00Z"},{"startsAt":"2026-03-01T01:00:00Z","expiresAt":"2026-03-01T02:00:00Z"}]`,
	} {
		if decoded, err := decodeScriptAuthorizationWindows(corrupt, true); !errors.Is(err, ErrScriptAuthorizationWindowInvalid) || decoded != nil {
			t.Fatalf("corrupt stored schedule accepted %q: %+v, %v", corrupt, decoded, err)
		}
	}
}

func TestScriptAuthorizationWindowsMigrationKeepsLegacySingleWindow(t *testing.T) {
	db := newScriptTestDB(t)
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	expires := start.Add(time.Hour)
	legacy := entity.UserScript{UserID: 1, VersionID: 1, Name: "legacy", ShortCode: "legacy-code", AccessKey: "legacy-key", LauncherStartsAt: &start, ShortEntryExpiresAt: &expires}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"launcher_windows", "short_entry_windows"} {
		if err := db.Exec("ALTER TABLE user_scripts DROP COLUMN " + column).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&entity.UserScript{}); err != nil {
		t.Fatal(err)
	}
	var script entity.UserScript
	if err := db.First(&script, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if script.LauncherWindows != "" || script.ShortEntryWindows != "" || script.LauncherStartsAt == nil || !script.LauncherStartsAt.Equal(start) || script.ShortEntryExpiresAt == nil || !script.ShortEntryExpiresAt.Equal(expires) {
		t.Fatalf("migration changed the legacy single-window schedule: %+v", script)
	}
}
