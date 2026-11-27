package middleware

import (
	"net/http"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

// Exercise the hand-off: a decision made from an alert must be consumed by the
// regular HTTP gate, without a restart, and must not affect a neighboring IP.
func TestRiskResponseProtectsRegistrationAndLogin(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	if err := db.AutoMigrate(&entity.WhitelistEntry{}, &entity.RiskWarning{}, &entity.RiskWarningResponse{}); err != nil {
		t.Fatal(err)
	}
	setBlacklistSwitch(t, db, true)
	allow := service.NewWhitelistCache()
	if err := allow.Reload(db); err != nil {
		t.Fatal(err)
	}
	cache := service.NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatal(err)
	}
	router := newBlacklistTestRouter(WhitelistIPWith(allow, cache, service.NewBlacklistHitRecorder(), db))
	w := entity.RiskWarning{RuleID: entity.RiskRuleIDLoginBruteforce, IP: "192.0.2.37", SubjectKey: "test-incident", Detail: `{"dimension":"ip","ip":"192.0.2.37","passwordFailureQualified":true}`, CreatedAt: time.Now()}
	if err := db.Create(&w).Error; err != nil {
		t.Fatal(err)
	}
	check := func(ip string, want int) {
		t.Helper()
		for _, route := range []string{"/api/user/register", "/api/user/login"} {
			got := doBlacklistRequest(router, http.MethodPost, route, ip+":45000")
			if got.Code != want {
				t.Fatalf("%s %s: got %d, want %d", route, ip, got.Code, want)
			}
		}
	}
	check(w.IP, http.StatusOK)
	result, err := service.ApplyRiskAlertResponse(db, w.ID, 7, service.RiskResponseInput{Action: "ip", DurationMinutes: 30, Reason: "isolated fixture"})
	if err != nil || result.Result == nil || result.Result.ExpiresAt == nil {
		t.Fatalf("temporary decision: %v / %+v", err, result)
	}
	// Use precisely the snapshot reloaded by the decision, not a second test-triggered reload.
	router = newBlacklistTestRouter(WhitelistIPWith(allow, service.DefaultBlacklistCache(), service.NewBlacklistHitRecorder(), db))
	check(w.IP, http.StatusForbidden)
	check("192.0.2.38", http.StatusOK)
}

// Real evaluator → automatic blacklist refresh → ordinary registration/login
// gate. Use only an in-process router and documentation-range fixture addresses.
func TestRiskRuleAutomaticSegmentHonorsWhitelistAndThrottle(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	for _, meta := range []any{&entity.WhitelistEntry{}, &entity.RiskRule{}, &entity.RiskRuleAudit{}, &entity.RiskWarning{}, &entity.RiskWarningResponse{}, &entity.LoginFailureEvent{}, &entity.LocationCache{}} {
		if err := db.AutoMigrate(meta); err != nil {
			t.Fatal(err)
		}
	}
	const login = entity.RiskRuleIDLoginBruteforce
	for _, def := range entity.DefaultRiskRules() {
		r := entity.RiskRule{RuleID: def.RuleID, Type: def.RuleID, Name: def.Name, Threshold: def.Threshold, ThrottleSeconds: def.ThrottleSeconds, Enabled: true}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&entity.RiskRule{}).Where("rule_id <> ?", login).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	const hit = "192.0.2.41"
	const permitted = "192.0.2.42"
	if _, err := service.CreateWhitelistEntryWithDB(db, service.CreateWhitelistEntryInput{Kind: "ip", Pattern: permitted, MatchType: "exact", Reason: "isolated", Enabled: true, CreatedBy: 7}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.LocationCache{Kind: entity.LocationKindIP, Source: "standard", LookupKey: hit, RawJSON: `{"beginip":"192.0.2.37","endip":"192.0.2.52"}`}).Error; err != nil {
		t.Fatal(err)
	}
	setBlacklistSwitch(t, db, true)
	clock := time.Now()
	seed := func(ip string) {
		t.Helper()
		for i := 0; i < 10; i++ {
			if err := db.Create(&entity.LoginFailureEvent{ClientIP: ip, Scene: entity.LoginFailureScenePassword, Reason: entity.LoginFailureReasonBadPassword, CreatedAt: clock.Add(-time.Second)}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	allow := service.NewWhitelistCache()
	if err := allow.Reload(db); err != nil {
		t.Fatal(err)
	}
	bl := service.DefaultBlacklistCache()
	if err := bl.Reload(db); err != nil {
		t.Fatal(err)
	}
	router := newBlacklistTestRouter(WhitelistIPWith(allow, bl, service.NewBlacklistHitRecorder(), db))
	assertStatus := func(ip string, want int) {
		t.Helper()
		for _, path := range []string{"/api/user/register", "/api/user/login"} {
			res := doBlacklistRequest(router, http.MethodPost, path, ip+":45100")
			if res.Code != want {
				t.Fatalf("ip=%s path=%s status=%d want=%d", ip, path, res.Code, want)
			}
		}
	}
	evaluate := func() {
		t.Helper()
		if _, err := service.EvaluateDebtAlerts(db, clock); err != nil {
			t.Fatal(err)
		}
	}
	seed(hit)
	evaluate()
	assertStatus(hit, http.StatusOK)
	if _, err := service.UpdateRiskRule(db, login, service.RiskRuleInput{Name: "synthetic rule", Severity: "low", Threshold: 10, ThrottleSeconds: 600, ResponsePolicy: &service.RiskRuleResponseInput{RiskRuleResponsePolicy: service.RiskRuleResponsePolicy{Mode: "permanent", Scope: "segment"}, Confirmed: true}}, 7); err != nil {
		t.Fatal(err)
	}
	evaluate() // previously recorded alert: do not retrofit newly enabled policy
	assertStatus(hit, http.StatusOK)
	clock = clock.Add(11 * time.Minute)
	seed(hit)
	evaluate()
	for _, ip := range []string{"192.0.2.37", hit, "192.0.2.52"} {
		assertStatus(ip, http.StatusForbidden)
	}
	for _, ip := range []string{"192.0.2.36", permitted, "192.0.2.53"} {
		assertStatus(ip, http.StatusOK)
	}
	var d entity.RiskWarningResponse
	if err := db.Where("status = ?", "applied").Take(&d).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeAutomaticRiskResponse(db, d.WarningID, d.ID, 7); err != nil {
		t.Fatal(err)
	}
	evaluate()
	assertStatus(hit, http.StatusOK)
	t.Log("new strategy did not touch historical warning; new cycle blocked exact range on both endpoints; interior whitelist and neighbors passed; revoke propagated, same warning did not reapply")
}
