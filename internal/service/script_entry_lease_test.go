package service

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestScriptLeaseLongShortEntriesShareOneClientAndLeaveRejectedTicketUnused(t *testing.T) {
	for _, shortFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "long_then_short", true: "short_then_long"}[shortFirst], func(t *testing.T) {
			db, owner, _, version := newManagedScriptFixture(t, 500)
			_, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"lease.example/*"}})
			if err != nil {
				t.Fatal(err)
			}
			input := CreateUserScriptInput{Name: "shared lease", VersionID: version.ID, BillingMode: entity.ScriptBillingModeEscrow, LauncherOrigin: &ScriptLauncherOrigin{Host: "lease.example", HTTPS: true}, DomainFormat: "lease.example/*"}
			script, err := CreateUserScriptWithDB(db, owner, input)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "lease.example/custom", DurationMinutes: 20})
			if err != nil {
				t.Fatal(err)
			}
			longURL, _ := url.Parse(script.DownloadURL)
			activation := func(short bool) ScriptExchangeResult {
				t.Helper()
				var grant ScriptExchangeGrant
				var err error
				if short {
					_, grant, err = ConsumeScriptShortAuthorizationWithDB(db, "lease.example", "/custom")
				} else {
					grant, err = IssuePublicScriptExchangeTokenByLocatorWithDB(db, longURL.Host, longURL.Path, 0)
				}
				if err != nil {
					t.Fatal(err)
				}
				result, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			firstTicket := activation(shortFirst)
			secondTicket := activation(!shortFirst)
			firstID, secondID := strings.Repeat("1", 64), strings.Repeat("2", 64)
			first, err := ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, firstTicket.ActivationToken, firstID, "127.0.0.1", 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, secondTicket.ActivationToken, secondID, "127.0.0.1", 0); !errors.Is(err, ErrScriptDeviceBusy) {
				t.Fatalf("second entry must not replace live client: %v", err)
			}
			var prior entity.ScriptRunSession
			if err := db.Where("token_hash = ?", hashScriptSecret(first.Token)).First(&prior).Error; err != nil || prior.RevokedAt != nil || prior.LeaseSlot == nil {
				t.Fatalf("rejected client revoked active lease: %v", err)
			}
			if _, _, err := ResolveScriptRunSessionWithDB(db, "Bearer "+first.Token, firstID, "127.0.0.1"); err != nil {
				t.Fatal(err)
			}
			if _, err := HeartbeatScriptRunSessionWithDB(db, script.ShortCode, "Bearer "+first.Token, firstID, "127.0.0.1"); err != nil {
				t.Fatal(err)
			}
			var rejected entity.ScriptExchangeToken
			if err := db.Where("token_hash = ?", hashScriptSecret(secondTicket.ActivationToken)).First(&rejected).Error; err != nil || rejected.UsedAt != nil {
				t.Fatalf("busy entry burned activation ticket: %v", err)
			}
			// A different script remains independently usable.
			input.Name = "independent lease"
			other, err := CreateUserScriptWithDB(db, owner, input)
			if err != nil {
				t.Fatal(err)
			}
			otherURL, _ := url.Parse(other.DownloadURL)
			otherGrant, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, otherURL.Host, otherURL.Path, 0)
			if err != nil {
				t.Fatal(err)
			}
			otherTicket, err := ExchangeScriptTokenWithDB(db, otherGrant.ShortCode, otherGrant.Token)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ActivateLeasedScriptRunSessionWithDB(db, other.ShortCode, otherTicket.ActivationToken, strings.Repeat("3", 64), "127.0.0.1", 0); err != nil {
				t.Fatalf("different script cannot acquire independent lease: %v", err)
			}
			stale := time.Now().Add(-5*time.Minute - time.Second)
			if err := db.Model(&entity.ScriptRunSession{}).Where("id = ?", prior.ID).Update("last_heartbeat_at", stale).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, secondTicket.ActivationToken, secondID, "127.0.0.1", 0); err != nil {
				t.Fatalf("unused ticket could not acquire stale lease: %v", err)
			}
			if _, _, err := ResolveScriptRunSessionWithDB(db, "Bearer "+first.Token, firstID, "127.0.0.1"); err == nil {
				t.Fatal("retired client remains authorized")
			}
			var count int64
			if err := db.Model(&entity.ScriptRunSession{}).Where("lease_slot = ?", script.ID).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("expected one shared slot, got %d: %v", count, err)
			}
		})
	}
}

func TestScriptLeaseHeartbeatHasLiteralFiveMinuteBoundary(t *testing.T) {
	now := time.Now()
	id := int32(1)
	for _, test := range []struct {
		age  time.Duration
		want bool
	}{{5*time.Minute - time.Nanosecond, true}, {5 * time.Minute, false}, {5*time.Minute + time.Nanosecond, false}} {
		heartbeat := now.Add(-test.age)
		row := entity.ScriptRunSession{ScriptID: id, LeaseSlot: &id, SourceZoneHash: "fixture", LastHeartbeatAt: &heartbeat, ExpiresAt: now.Add(time.Hour)}
		if got := ScriptRunSessionUsableAt(row, now); got != test.want {
			t.Fatalf("heartbeat age %s usable=%t want=%t", test.age, got, test.want)
		}
	}
}
