package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestScriptMultiWindowConfigurationRoundTripsAndRejectsConflictingModes(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{true: "full", false: "short"}[full], func(t *testing.T) {
			db, owner, _, script := shortAuthorizationFixture(t)
			no := false
			if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{AllowFullNeverExpire: &no, AllowShortNeverExpire: &no}); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			windows := []map[string]time.Time{
				{"startsAt": now.Add(time.Hour), "expiresAt": now.Add(2 * time.Hour)},
				{"startsAt": now.Add(4 * time.Hour), "expiresAt": now.Add(5 * time.Hour)},
			}
			wire, _ := json.Marshal(map[string]any{"enabled": true, "address": "quick.muioi.com", "windows": windows, "maxUses": 3})
			var input ConfigureScriptShortAuthorizationInput
			if err := json.Unmarshal(wire, &input); err != nil {
				t.Fatal(err)
			}
			configure := ConfigureScriptShortAuthorizationWithDB
			if full {
				configure = ConfigureScriptLauncherAuthorizationWithDB
			}
			view, err := configure(db, owner, script.ID, input)
			if err != nil {
				t.Fatalf("finite windows must not require permanent authorization: %v", err)
			}
			data, _ := json.Marshal(view)
			var actual map[string]json.RawMessage
			_ = json.Unmarshal(data, &actual)
			var returned []map[string]time.Time
			if err := json.Unmarshal(actual["windows"], &returned); err != nil || len(returned) != 2 {
				t.Fatalf("multiple windows lost in response: %s", data)
			}
			if view.Status != "scheduled" || view.MaxUses != 3 || view.Command == "" {
				t.Fatalf("wrong scheduled view: %+v", view)
			}
			rows, err := ListUserScriptsWithDB(db, owner, false)
			if err != nil || len(rows) != 1 {
				t.Fatal(err)
			}
			roundtrip, _ := json.Marshal(rows[0])
			if !strings.Contains(string(roundtrip), `"windows":[`) {
				t.Fatal("persisted schedule absent from list")
			}
			input.DurationMinutes = 5
			if _, err := configure(db, owner, script.ID, input); !errors.Is(err, ErrScriptAuthorizationWindowInvalid) {
				t.Fatalf("mixed single/multiple authorization silently accepted: %v", err)
			}
			input.DurationMinutes = 0
			if err := json.Unmarshal([]byte(`{"enabled":true,"address":"quick.muioi.com","windows":[]}`), &input); err != nil {
				t.Fatal(err)
			}
			if _, err := configure(db, owner, script.ID, input); !errors.Is(err, ErrScriptAuthorizationWindowInvalid) {
				t.Fatalf("empty windows accepted: %v", err)
			}
		})
	}
}

func TestScriptWindowGapRejectsEntryAndTicketsButRetainsShortAddress(t *testing.T) {
	db, owner, _, item := shortAuthorizationFixture(t)
	view, err := ConfigureScriptShortAuthorizationWithDB(db, owner, item.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "quick.muioi.com", DurationMinutes: 60, MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	wire, _ := json.Marshal([]map[string]time.Time{
		{"startsAt": now.Add(-2 * time.Hour), "expiresAt": now.Add(-time.Hour)},
		{"startsAt": now.Add(time.Hour), "expiresAt": now.Add(2 * time.Hour)},
	})
	// Simulate moving from a completed first window into the gap without sleeping.
	if err := db.Model(&entity.UserScript{}).Where("id = ?", item.ID).Update("short_entry_windows", string(wire)).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "quick.muioi.com", ""); !errors.Is(err, ErrScriptShortAuthorizationInactive) {
		t.Fatalf("gap accepted launch: %v", err)
	}
	var script entity.UserScript
	if err := db.First(&script, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if script.ShortEntryAddressKey == nil || script.ShortEntryUsedCount != 0 {
		t.Fatal("gap destroyed reservation or consumed budget")
	}
	paused := scriptShortAuthorizationView(script)
	if paused.Status != "paused" || paused.Command == "" {
		t.Fatalf("gap should preserve a copyable scheduled command: %+v", paused)
	}
	grant := entity.ScriptExchangeToken{AuthorizationKind: "short", AuthorizationGeneration: script.ShortEntryGeneration}
	if err := validateIssuedScriptAuthorization(script, grant, now); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatal("gap allowed issued activation ticket")
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, item.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: view.Address, DurationMinutes: 20}); err != nil {
		t.Fatalf("paused address could not be retained: %v", err)
	}
	if err := db.First(&script, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script.ShortEntryWindows, "startsAt") {
		t.Fatal("single-period update retained old multiple windows")
	}
}

func TestScriptMultipleWindowsShareBudgetAcrossLaterWindowInLocalSQLZone(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("fixture-CST", 8*60*60)
	t.Cleanup(func() { time.Local = previous })
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{true: "full", false: "short"}[full], func(t *testing.T) {
			db, owner, _, item := shortAuthorizationFixture(t)
			now := time.Now()
			windows := []ScriptAuthorizationWindow{
				{StartsAt: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute)},
				{StartsAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour)},
			}
			configure := ConfigureScriptShortAuthorizationWithDB
			if full {
				configure = ConfigureScriptLauncherAuthorizationWithDB
			}
			if _, err := configure(db, owner, item.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "quick.muioi.com", Windows: windows, MaxUses: 2}); err != nil {
				t.Fatal(err)
			}
			var current entity.UserScript
			if err := db.First(&current, item.ID).Error; err != nil {
				t.Fatal(err)
			}
			issue := func() (ScriptExchangeGrant, error) {
				if full {
					return IssuePublicScriptExchangeTokenWithDB(db, current.ShortCode, scriptLauncherCapability(current), 0)
				}
				_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "quick.muioi.com", "")
				return grant, err
			}
			first, err := issue()
			if err != nil {
				t.Fatalf("first UTC-JSON/local-SQL window failed: %v", err)
			}
			if _, err := ExchangeScriptTokenWithDB(db, first.ShortCode, first.Token); err != nil {
				t.Fatal(err)
			}
			// Advance schedule around the real clock without resetting generation or
			// counters: this is the later window of the same authorization.
			windows[0] = ScriptAuthorizationWindow{StartsAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
			windows[1].StartsAt = now.Add(-time.Minute)
			raw, err := encodeScriptAuthorizationWindows(windows)
			if err != nil {
				t.Fatal(err)
			}
			column := "short_entry_windows"
			if full {
				column = "launcher_windows"
			}
			if err := db.Model(&entity.UserScript{}).Where("id = ?", item.ID).UpdateColumn(column, raw).Error; err != nil {
				t.Fatal(err)
			}
			second, err := issue()
			if err != nil {
				t.Fatalf("later window did not retain address/budget: %v", err)
			}
			// The final issued ticket must remain exchangeable after the shared
			// limit is reached; consuming it cannot count another use.
			if _, err := ExchangeScriptTokenWithDB(db, second.ShortCode, second.Token); err != nil {
				t.Fatal(err)
			}
			if _, err := issue(); err == nil {
				t.Fatal("later window reset shared maximum uses")
			}
			if err := db.First(&current, item.ID).Error; err != nil {
				t.Fatal(err)
			}
			used := current.ShortEntryUsedCount
			if full {
				used = current.LauncherUsedCount
			}
			if used != 2 {
				t.Fatalf("expected exactly two counted launches, got %d", used)
			}
		})
	}
}
