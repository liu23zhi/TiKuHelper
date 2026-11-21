package service

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestScriptFullAuthorizationKeepsDefaultAndAtomicallyBudgetsOnlyLaunchEntry(t *testing.T) {
	db, owner, admin, script := shortAuthorizationFixture(t)
	legacyCapability := scriptLauncherCapability(script.UserScript)
	view, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, DurationMinutes: 10, MaxUses: 2})
	if err != nil || view.MaxUses != 2 || view.UsedCount != 0 || view.Command == "" {
		t.Fatalf("full authorization invalid: %+v %v", view, err)
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, legacyCapability, 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("old full capability survived update: %v", err)
	}
	var fresh entity.UserScript
	if err := db.First(&fresh, script.ID).Error; err != nil {
		t.Fatal(err)
	}
	capability := scriptLauncherCapability(fresh)
	first, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, capability, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, capability, 0); !errors.Is(err, ErrScriptExchangeAlreadyIssued) {
		t.Fatalf("pending exchange should not count a launch: %v", err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, first.ShortCode, first.Token); err != nil {
		t.Fatal(err)
	}
	second, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, capability, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, second.ShortCode, second.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, capability, 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("full use budget exceeded: %v", err)
	}
	if err := db.First(&fresh, script.ID).Error; err != nil || fresh.LauncherUsedCount != 2 {
		t.Fatalf("expected exactly two starts: %d %v", fresh.LauncherUsedCount, err)
	}
	if _, err := ConfigureScriptLauncherAuthorizationWithDB(db, admin, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: false}); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("admin changed another owner's launcher capability: %v", err)
	}
}

func TestScriptAuthorizationConcurrentRequestsNeverExceedBudget(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "short"}[short], func(t *testing.T) {
			db, owner, _, script := shortAuthorizationFixture(t)
			input := ConfigureScriptShortAuthorizationInput{Enabled: true, DurationMinutes: 5, MaxUses: 1, Address: "muioi.com/a"}
			if short {
				if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, input); err != nil {
					t.Fatal(err)
				}
			}
			var fresh entity.UserScript
			if err := db.First(&fresh, script.ID).Error; err != nil {
				t.Fatal(err)
			}
			capability := scriptLauncherCapability(fresh)
			const requests = 12
			start := make(chan struct{})
			results := make(chan ScriptExchangeGrant, requests)
			var workers sync.WaitGroup
			for i := 0; i < requests; i++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					<-start
					if short {
						_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a")
						if err == nil {
							results <- grant
						}
					} else {
						grant, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, capability, 0)
						if err == nil {
							results <- grant
						}
					}
				}()
			}
			close(start)
			workers.Wait()
			close(results)
			if len(results) != 1 {
				t.Fatalf("concurrent entry issued %d grants for one use", len(results))
			}
			grant := <-results
			result, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DownloadScriptVersionByActivationTokenWithDB(db, grant.ShortCode, result.ActivationToken); err != nil {
				t.Fatal(err)
			}
			if _, err := DownloadScriptVersionByActivationTokenWithDB(db, grant.ShortCode, result.ActivationToken); err != nil {
				t.Fatal(err)
			}
			if _, err := ActivateScriptRunSessionWithDB(db, grant.ShortCode, result.ActivationToken, "counter-fixture"); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&fresh, script.ID).Error; err != nil {
				t.Fatal(err)
			}
			if (short && (fresh.ShortEntryUsedCount != 1 || fresh.LauncherUsedCount != 0)) || (!short && (fresh.LauncherUsedCount != 1 || fresh.ShortEntryUsedCount != 0)) {
				t.Fatalf("later exchange/download/activation changed entry budget: long=%d short=%d", fresh.LauncherUsedCount, fresh.ShortEntryUsedCount)
			}
		})
	}
}

func TestScriptShortAuthorizationScheduleAndIssuedTicketRevocation(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	starts := time.Now().Add(time.Hour)
	input := ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/a", DurationMinutes: 15, MaxUses: 2, StartsAt: &starts}
	view, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input)
	if err != nil || view.Status != "scheduled" {
		t.Fatalf("scheduled short authorization invalid: %+v %v", view, err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a"); !errors.Is(err, ErrScriptShortAuthorizationInactive) {
		t.Fatalf("scheduled short started early: %v", err)
	}
	input.StartsAt = nil
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil {
		t.Fatal(err)
	}
	_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("revoked short ticket exchanged: %v", err)
	}
}

func TestScriptShortAuthorizationAcceptsUTCInputOnServerWithLocalTimezone(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("authorization-fixture", 8*60*60)
	t.Cleanup(func() { time.Local = previous })
	db, owner, _, script := shortAuthorizationFixture(t)
	starts, expires := time.Now().Add(-time.Minute).UTC(), time.Now().Add(5*time.Minute).UTC()
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/a", StartsAt: &starts, ExpiresAt: &expires, MaxUses: 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a"); err != nil {
		t.Fatalf("valid UTC expiry must remain usable in the server's local timezone: %v", err)
	}
}

func TestScriptFullAuthorizationExpiryRevokesUnactivatedTicketAndDownload(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	if _, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, DurationMinutes: 10, MaxUses: 1}); err != nil {
		t.Fatal(err)
	}
	var fresh entity.UserScript
	if err := db.First(&fresh, script.ID).Error; err != nil {
		t.Fatal(err)
	}
	grant, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, scriptLauncherCapability(fresh), 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.UserScript{}).Where("id = ?", script.ID).Update("launcher_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadScriptVersionByActivationTokenWithDB(db, grant.ShortCode, result.ActivationToken); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("expired full authorization still downloaded: %v", err)
	}
	if _, err := ActivateScriptRunSessionWithDB(db, grant.ShortCode, result.ActivationToken, "expired-fixture"); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("expired full authorization still activated: %v", err)
	}
}

func TestScriptFullAndShortAuthorizationHaveIndependentSchedulesAndBudgets(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	starts := time.Now().Add(time.Hour)
	if _, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, StartsAt: &starts, DurationMinutes: 30, MaxUses: 3}); err != nil {
		t.Fatal(err)
	}
	var fresh entity.UserScript
	if err := db.First(&fresh, script.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, scriptLauncherCapability(fresh), 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("scheduled full command started early: %v", err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/a", DurationMinutes: 5, MaxUses: 2}); err != nil {
		t.Fatal(err)
	}
	_, first, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, first.ShortCode, first.Token); err != nil {
		t.Fatal(err)
	}
	_, second, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, second.ShortCode, second.Token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("short use budget exceeded: %v", err)
	}
	if err := db.First(&fresh, script.ID).Error; err != nil || fresh.LauncherUsedCount != 0 || fresh.ShortEntryUsedCount != 2 {
		t.Fatalf("independent budgets failed: %+v %v", fresh, err)
	}
	if _, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&fresh, script.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, scriptLauncherCapability(fresh), 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("revoked full command accepted: %v", err)
	}
}
