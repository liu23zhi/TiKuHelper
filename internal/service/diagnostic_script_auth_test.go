package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// Isolated first-party lease/account data. No external program or client runs.
func TestDiagnosticCredentialsDoNotActivateRenewOrBill(t *testing.T) {
	d, item := newScriptLeaseFixture(t)
	const normal = "diagnostic-standard-fixture"
	const rawScript = "diagnostic-script-key-fixture"
	const ip = "127.0.0.1"
	firstID := strings.Repeat("a1", 32)
	secondID := strings.Repeat("b2", 32)
	account := entity.AccessToken{UserID: item.UserID, TokenHash: hashAccessTokenValue(normal), Status: "enabled", TotalQuotaLimit: 25}
	if err := d.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.Model(&entity.AccessToken{}).Where("id = ?", item.AccessTokenID).Update("token_hash", hashAccessTokenValue(rawScript)).Error; err != nil {
		t.Fatal(err)
	}
	auth, err := ResolveAccessTokenFromBasicAuth(d, "Bearer "+normal)
	if err != nil || auth.User.ID != item.UserID {
		t.Fatal("ordinary fixture must actually be authorized as an ordinary bearer")
	}
	if _, err = ResolveAccessTokenFromBasicAuth(d, "Bearer "+rawScript); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatal("dedicated raw script key accepted as ordinary bearer")
	}
	for _, key := range []string{normal, rawScript} {
		if _, err = ActivateLeasedScriptRunSessionWithDB(d, item.ShortCode, key, firstID, ip, 5); !errors.Is(err, ErrScriptTokenInvalid) {
			t.Fatal("ordinary/dedicated raw key used as an activation ticket")
		}
		if _, err = ResolveLeasedDiagnosticOwner(d, "Bearer "+key, firstID, ip); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
			t.Fatal("raw key admitted as an active exam lease")
		}
	}
	var orig int64
	if err = d.Model(&entity.ScriptRunSession{}).Count(&orig).Error; err != nil || orig != 0 {
		t.Fatal("invalid activation or telemetry created a run")
	}
	insertLeaseTicket(t, d, item, "diagnostic-initial-ticket")
	grant, err := ActivateLeasedScriptRunSessionWithDB(d, item.ShortCode, "diagnostic-initial-ticket", firstID, ip, 5)
	if err != nil {
		t.Fatal(err)
	}
	insertLeaseTicket(t, d, item, "diagnostic-second-ticket")
	if _, err = ActivateLeasedScriptRunSessionWithDB(d, item.ShortCode, "diagnostic-second-ticket", secondID, ip, 5); !errors.Is(err, ErrScriptDeviceBusy) {
		t.Fatal("another device claimed the same active script")
	}
	snapshot := func() (entity.ScriptRunSession, entity.User, entity.AccessToken) {
		t.Helper()
		var row entity.ScriptRunSession
		var owner entity.User
		var key entity.AccessToken
		if err := d.Where("script_id = ?", item.ID).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := d.First(&owner, item.UserID).Error; err != nil {
			t.Fatal(err)
		}
		if err := d.First(&key, item.AccessTokenID).Error; err != nil {
			t.Fatal(err)
		}
		return row, owner, key
	}
	before, u, k := snapshot()
	for i := 0; i < 2; i++ {
		if id, err := ResolveLeasedDiagnosticOwner(d, "Bearer "+grant.Token, firstID, ip); err != nil || id != item.UserID {
			t.Fatal("valid lease rejected for its owner")
		}
	}
	for _, bad := range []struct{ token, identity, peer string }{{normal, firstID, ip}, {"diagnostic-second-ticket", firstID, ip}, {grant.Token, secondID, ip}, {grant.Token, firstID, "127.0.0.2"}} {
		if _, err = ResolveLeasedDiagnosticOwner(d, "Bearer "+bad.token, bad.identity, bad.peer); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
			t.Fatal("mismatched identity, ticket or peer accepted")
		}
	}
	after, u2, k2 := snapshot()
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(u, u2) || !reflect.DeepEqual(k, k2) {
		t.Fatal("log authorization modified a lease, quota, usage timestamp, balance or user")
	}
	// A stale heartbeat is never revived by logging.
	past := time.Now().Add(-2 * ScriptHeartbeatTimeout)
	if err = d.Model(&entity.ScriptRunSession{}).Where("id = ?", before.ID).Update("last_heartbeat_at", past).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveLeasedDiagnosticOwner(d, "Bearer "+grant.Token, firstID, ip); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatal("stale lease admitted")
	}
	if err = d.Model(&entity.ScriptRunSession{}).Where("id = ?", before.ID).Updates(map[string]any{"lease_slot": nil, "last_heartbeat_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveLeasedDiagnosticOwner(d, "Bearer "+grant.Token, firstID, ip); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatal("legacy unslotted session admitted as native exam")
	}
}
