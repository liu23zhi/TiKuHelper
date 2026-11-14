package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newScriptLeaseFixture(t *testing.T) (*gorm.DB, ScriptListItem) {
	t.Helper()
	db := newScriptTestDB(t)
	owner := entity.User{Username: "lease-owner", Role: "user", Balance: 200}
	admin := entity.User{Username: "lease-admin", Role: "admin"}
	bindSyntheticScriptOwner(&owner)
	for _, user := range []*entity.User{&owner, &admin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	version, err := UploadScriptVersionWithDB(db, admin, "native-lease", "fixture.exe", []byte("MZsynthetic"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "lease", VersionID: version.ID, BaseDomain: "lease.example"})
	if err != nil {
		t.Fatal(err)
	}
	return db, item
}

func TestScriptLeaseConcurrentActivationKeepsSingleSlot(t *testing.T) {
	db, item := newScriptLeaseFixture(t)
	for i := 0; i < 6; i++ {
		insertLeaseTicket(t, db, item, fmt.Sprintf("synthetic-contender-%d", i))
	}
	start := make(chan struct{})
	results := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode,
				fmt.Sprintf("synthetic-contender-%d", i), fmt.Sprintf("%064x", i+1), "127.0.0.1", 0)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		}
	}
	var leases, consumed int64
	if err := db.Model(&entity.ScriptRunSession{}).Where("lease_slot = ?", item.ID).Count(&leases).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.ScriptExchangeToken{}).Where("script_id = ? AND used_at IS NOT NULL", item.ID).Count(&consumed).Error; err != nil {
		t.Fatal(err)
	}
	if successes != 1 || leases != 1 || consumed != 1 {
		t.Fatalf("expected one committed lease and one consumed ticket: %d/%d/%d", successes, leases, consumed)
	}
}

func insertLeaseTicket(t *testing.T, db *gorm.DB, item ScriptListItem, ticket string) {
	t.Helper()
	if err := db.Create(&entity.ScriptExchangeToken{ScriptID: item.ID, VersionID: item.VersionID,
		Purpose: "activation", TokenHash: hashScriptSecret(ticket), ExpiresAt: time.Now().Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestScriptLeaseSingleOwnerHeartbeatAndReplay(t *testing.T) {
	db, item := newScriptLeaseFixture(t)
	a, b := strings.Repeat("12", 32), strings.Repeat("34", 32)
	const ip = "127.0.0.1"
	insertLeaseTicket(t, db, item, "synthetic-a")
	insertLeaseTicket(t, db, item, "synthetic-b")
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-a", "old-runtime-id", ip, 0); !errors.Is(err, ErrScriptRunIdentityRequired) {
		t.Fatalf("legacy identity gate: %v", err)
	}
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-a", a, ip, 151); !errors.Is(err, ErrScriptDurationInvalid) {
		t.Fatalf("duration gate: %v", err)
	}
	start := time.Now()
	first, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-a", a, ip, 120)
	if err != nil || first.HeartbeatURL != fmt.Sprintf("/api/script-command/%s/heartbeat", item.ShortCode) ||
		first.ExpiresAt.Before(start.Add(119*time.Minute)) || first.ExpiresAt.After(start.Add(121*time.Minute)) {
		t.Fatalf("activation did not issue a bounded lease: %v", err)
	}
	replay, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-a", a, ip, 0)
	if err != nil || replay.Token != first.Token || !replay.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("lost-response retry did not retrieve the same lease: %v", err)
	}
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-a", b, ip, 0); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("same ticket with different proof must fail: %v", err)
	}
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-b", b, ip, 0); !errors.Is(err, ErrScriptDeviceBusy) {
		t.Fatalf("active lease must block a second device: %v", err)
	}
	auth := "Bearer " + first.Token
	if _, _, err := ResolveScriptRunSessionWithDB(db, auth, a, "127.0.0.2"); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("changed local source must fail: %v", err)
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, auth, a); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("missing source must fail: %v", err)
	}
	if _, err := HeartbeatScriptRunSessionWithDB(db, "different-script", auth, a, ip); !errors.Is(err, ErrScriptTokenInvalid) {
		t.Fatalf("heartbeat scoped to wrong script: %v", err)
	}
	expires, err := HeartbeatScriptRunSessionWithDB(db, item.ShortCode, auth, a, ip)
	if err != nil || !expires.Equal(first.ExpiresAt) {
		t.Fatalf("heartbeat failed or extended hard expiry: %v", err)
	}
	if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", item.ID).
		Update("last_heartbeat_at", time.Now().Add(-ScriptHeartbeatTimeout)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := HeartbeatScriptRunSessionWithDB(db, item.ShortCode, auth, a, ip); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("stale heartbeat cannot revive lease: %v", err)
	}
	second, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-b", b, ip, 0)
	if err != nil || second.Token == first.Token {
		t.Fatalf("unused ticket did not acquire retired slot: %v", err)
	}
	var live, revoked int64
	if err := db.Model(&entity.ScriptRunSession{}).Where("lease_slot = ?", item.ID).Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ? AND revoked_at IS NOT NULL AND lease_slot IS NULL", item.ID).Count(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if live != 1 || revoked != 1 {
		t.Fatalf("expected exactly one active lease and one retained revoked record: %d/%d", live, revoked)
	}
}

func TestScriptLeaseSourceIsServerCacheOnly(t *testing.T) {
	db, item := newScriptLeaseFixture(t)
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("12", 32)
	insertLeaseTicket(t, db, item, "synthetic-geo")
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-geo", identity, "203.0.113.1", 0); !errors.Is(err, ErrScriptSourceUnavailable) {
		t.Fatalf("unknown location was admitted: %v", err)
	}
	for ip, region := range map[string]string{"203.0.113.1": "Fixture East", "203.0.113.2": "Fixture East", "203.0.113.3": "Fixture West"} {
		row := entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: ip, Address: region,
			RawJSON: fmt.Sprintf(`{"ip":%q,"region":%q}`, ip, region), FetchedAt: time.Now()}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	grant, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-geo", identity, "203.0.113.1", 0)
	if err != nil {
		t.Fatalf("cached server geolocation should admit: %v", err)
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, grant.Token, identity, "203.0.113.2"); err != nil {
		t.Fatalf("same validated region should be compatible: %v", err)
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, grant.Token, identity, "203.0.113.3"); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("different region must not be admitted: %v", err)
	}
}
