package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestRiskResponseHandlersRequireAdminAndRejectClientTargets(t *testing.T) {
	db := newRiskControllerTestDB(t)
	withRiskControllerDB(t, db)
	for _, handler := range []gin.HandlerFunc{RiskResponsePolicyGet, RiskResponsePolicyPut, RiskAlertResponseGet, RiskAlertResponseApply} {
		if got := performRiskRequestNoUser(t, "/risk", handler); got != http.StatusUnauthorized {
			t.Fatalf("anonymous=%d", got)
		}
		user := &entity.User{ID: 9, Role: "user"}
		if got := performRiskRequest(t, user, "GET", "/risk", "", nil, handler); got.Code != http.StatusForbidden {
			t.Fatalf("ordinary=%d", got.Code)
		}
	}
	admin := &entity.User{ID: 87, Role: "admin"}
	params := gin.Params{{Key: "detector", Value: "login_bruteforce"}}
	policy := performRiskRequest(t, admin, "PUT", "/risk/policies", `{"mode":"temp_ip","durationMinutes":10}`, params, RiskResponsePolicyPut)
	if policy.Code != 200 || !strings.Contains(policy.Body.String(), `"blacklistEnabled":false`) {
		t.Fatalf("PUT=%d %s", policy.Code, policy.Body.String())
	}
	policy = performRiskRequest(t, admin, "GET", "/risk/policies", "", params, RiskResponsePolicyGet)
	if policy.Code != 200 || !strings.Contains(policy.Body.String(), `"durationMinutes":10`) {
		t.Fatalf("GET=%d %s", policy.Code, policy.Body.String())
	}
	params = gin.Params{{Key: "id", Value: "1"}}
	for _, body := range []string{`{"action":"ip","confirmed":true,"reason":"reviewed"}`, `{"action":"ip","ip":"203.0.113.2","durationMinutes":10,"reason":"reviewed"}`, `{"action":"ip","durationMinutes":10,"reason":"reviewed"} {}`} {
		got := performRiskRequest(t, admin, "POST", "/risk/alerts/1/response", body, params, RiskAlertResponseApply)
		if got.Code != http.StatusBadRequest {
			t.Fatalf("invalid target/term %s accepted: %d", body, got.Code)
		}
	}
	if got := performRiskRequest(t, admin, "GET", "/risk/alerts/1/response", "", params, RiskAlertResponseGet); got.Code != http.StatusNotFound {
		t.Fatalf("absent warning: %d", got.Code)
	}
}
