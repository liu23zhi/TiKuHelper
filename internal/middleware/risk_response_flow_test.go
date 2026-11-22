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
