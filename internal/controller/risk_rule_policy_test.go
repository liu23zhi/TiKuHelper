package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestRiskRuleIndependentPolicyAPI(t *testing.T) {
	withRiskControllerDB(t, newRiskControllerTestDB(t))
	admin := &entity.User{ID: 901, Role: "admin"}
	const base = `{"name":"独立策略夹具","type":"login_bruteforce","config":{},"severity":"high","enabled":true,"threshold":50,"throttleSeconds":3600`
	makeRule := func(body string) (int, string) {
		resp := performRiskRequest(t, admin, http.MethodPost, "/risk/rules", body, nil, CreateRiskRule)
		return resp.Code, resp.Body.String()
	}
	t.Run("new defaults to notify", func(t *testing.T) {
		code, body := makeRule(base + `}`)
		if code != http.StatusCreated || !strings.Contains(body, `"responsePolicy":{"mode":"notify"`) {
			t.Fatalf("code=%d body=%s", code, body)
		}
	})
	t.Run("permanent requires confirmation", func(t *testing.T) {
		code, body := makeRule(base + `,"responsePolicy":{"mode":"permanent","scope":"ip"}}`)
		if code != http.StatusBadRequest {
			t.Fatalf("code=%d body=%s", code, body)
		}
	})
	t.Run("confirmed segment belongs to this rule", func(t *testing.T) {
		code, body := makeRule(base + `,"responsePolicy":{"mode":"temporary","scope":"segment","durationMinutes":30,"confirmed":true}}`)
		if code != http.StatusCreated || !strings.Contains(body, `"responsePolicy":{"mode":"temporary","scope":"segment","durationMinutes":30`) {
			t.Fatalf("code=%d body=%s", code, body)
		}
	})
}
