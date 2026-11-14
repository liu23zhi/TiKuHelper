package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func TestReleaseAIFallbackScriptAdmissionAndSettlement(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:release-ai-fallback-script?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.ScriptRunSession{}, &entity.UserScript{},
		&entity.SystemSetting{}, &entity.BillingRecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	user := entity.User{ID: 1, Username: "fixture-owner", Status: "enabled", Balance: 0.000001}
	key := entity.AccessToken{ID: 2, UserID: user.ID, ScriptID: 3, Name: "fixture-key", TokenHash: strings.Repeat("3", 64), Status: "enabled", TotalQuotaLimit: 1}
	script := entity.UserScript{ID: key.ScriptID, UserID: user.ID, AccessTokenID: key.ID, ShortCode: "fixture-code",
		Enabled: true, BaseDomain: "example.com"}
	session := entity.ScriptRunSession{ID: 4, UserID: user.ID, ScriptID: script.ID, AccessTokenID: key.ID,
		TokenHash: strings.Repeat("1", 64), IdentityHash: strings.Repeat("2", 64), ExpiresAt: now.Add(time.Hour)}
	for _, row := range []any{&user, &key, &script, &session} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadAIFallbackBillingContextWithDB(db, user.ID, key.ID, entity.ScriptRunSession{}); !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("raw dedicated Key must not bypass script session: %v", err)
	}
	mismatch := session
	mismatch.AccessTokenID++
	if _, err := loadAIFallbackBillingContextWithDB(db, user.ID, key.ID, mismatch); !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("session attached to wrong Key was not rejected: %v", err)
	}
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, service.ScriptDomainSettings{Domains: []string{"other.example.com/*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAIFallbackBillingContextWithDB(db, user.ID, key.ID, session); !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("session with newly blocked domain admitted: %v", err)
	}
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, service.ScriptDomainSettings{Domains: []string{"example.com/*"}}); err != nil {
		t.Fatal(err)
	}
	admitted, err := loadAIFallbackBillingContextWithDB(db, user.ID, key.ID, session)
	if err != nil || admitted.ScriptID != script.ID || admitted.AccessTokenID != key.ID {
		t.Fatalf("valid script Key identity not carried to settlement: ctx=%+v err=%v", admitted, err)
	}
	if err := db.Model(&session).Update("revoked_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := loadAIFallbackBillingContextWithDB(db, user.ID, key.ID, session); !errors.Is(err, service.ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("revoked session still admitted to new execution: %v", err)
	}
	if err := db.Model(&key).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	// The actual request was already admitted and executed. Revocation/disable
	// prevents new calls but must not erase an actual usage charge.
	admitted.RequestID = "synthetic-executed-ai"
	charges := []searchBillingCharge{{Provider: "ai", Amount: 0.012345, Snapshot: "provider=ai"}}
	for i := 0; i < 2; i++ {
		if err := persistSearchBillingRecordsWithDB(db, admitted, charges); err != nil {
			t.Fatalf("settlement attempt %d failed: %v", i+1, err)
		}
	}
	var charged entity.User
	var used entity.AccessToken
	var bills int64
	if err := db.First(&charged, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&used, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.BillingRecord{}).Where("request_id = ?", admitted.RequestID).Count(&bills).Error; err != nil {
		t.Fatal(err)
	}
	if charged.Balance != -0.012344 || used.TotalAmountUsed != 0.012345 || bills != 1 {
		t.Fatalf("must retain exact negative settlement once: balance=%.6f key=%.6f bills=%d", charged.Balance, used.TotalAmountUsed, bills)
	}
}

func TestReleaseAIFallbackSeparatesScriptActorWork(t *testing.T) {
	const userID = 7
	const firstScope = "script:11:13:fixture-identity-one"
	const secondScope = "script:12:14:fixture-identity-two"
	store := newAIFallbackTaskStore(time.Minute, time.Now)
	run := func(string) service.AIFallbackResult {
		return service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Answer: "synthetic answer"}}
	}
	firstID, reused, err := store.createIdempotent(userID, "same-key", "same-body", run, firstScope)
	if err != nil || reused {
		t.Fatalf("first actor did not start: %v", err)
	}
	secondID, reused, err := store.createIdempotent(userID, "same-key", "same-body", run, secondScope)
	if err != nil || reused || secondID == firstID {
		t.Fatalf("same-user script actors shared idempotency: %v", err)
	}
	for _, id := range []string{firstID, secondID} {
		eventuallyAIFallbackTaskStatus(t, store, id, userID, aiFallbackTaskStatusSucceeded)
	}
	found, ok, err := store.findByIdempotency(userID, "same-key", "same-body", firstScope)
	if err != nil || !ok || found.ID != firstID {
		t.Fatalf("same actor could not retrieve its paid idempotent work: %v", err)
	}
	if _, err := store.get(firstID, userID, secondScope); !errors.Is(err, errAIFallbackTaskForbidden) {
		t.Fatalf("unrelated script read owner's paid task: %v", err)
	}
	if _, err := store.get(firstID, userID); err != nil {
		t.Fatalf("ordinary owner could not retrieve paid task: %v", err)
	}

	req := model.SearchRequest{Question: "release-synthetic-script-cache", Type: 4}
	storeCachedAIFallbackResult(userID, req, run("fixture"), firstScope)
	if _, ok := loadCachedAIFallbackResult(userID, req, secondScope); ok {
		t.Fatal("result cache crossed script actor boundary")
	}
	if _, ok := loadCachedAIFallbackResult(userID, req, firstScope); !ok {
		t.Fatal("same script actor lost its result cache")
	}
	if buildFallbackFlightKey(userID, req, firstScope) == buildFallbackFlightKey(userID, req, secondScope) {
		t.Fatal("different script actors can join one in-flight charge")
	}
}
