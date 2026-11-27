package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestReferralAutoRulesOnlyAdminAtomicPriority(t *testing.T) {
	db := newReferralControllerTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	original := referralDB
	referralDB = func() *gorm.DB { return db }
	t.Cleanup(func() { referralDB = original })

	req := func(role, method, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(out)
		if role != "" {
			c.Set("user", &entity.User{ID: 1, Username: "test-tier-operator", Role: role, Status: "enabled"})
		}
		c.Request = httptest.NewRequest(method, "/api/admin/referral-rebate-rules", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		return out
	}
	read := func() service.ReferralAutoRules {
		t.Helper()
		r := req("admin", http.MethodGet, "", ReferralAutoRulesGet)
		var p service.ReferralAutoRules
		if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(r.Body.Bytes(), &p) != nil {
			t.Fatalf("read HTTP %d: %s", r.Code, r.Body)
		}
		return p
	}
	if p := read(); p.Enabled || p.Rules == nil || len(p.Rules) != 0 {
		t.Fatalf("default: %+v", p)
	}
	const input = `{"enabled":true,"rules":[{"enabled":true,"lowerCents":1000,"upperCents":2000,"rateUnits":0},{"enabled":true,"lowerCents":0,"upperCents":null,"rateUnits":3000000}]}`
	if r := req("user", http.MethodPut, input, ReferralAutoRulesPut); r.Code != http.StatusForbidden {
		t.Fatalf("ordinary user: HTTP %d", r.Code)
	}
	if r := req("admin", http.MethodPut, input, ReferralAutoRulesPut); r.Code != http.StatusOK {
		t.Fatalf("save HTTP %d: %s", r.Code, r.Body)
	}
	p := read()
	if !p.Enabled || len(p.Rules) != 2 || p.Rules[0].RateUnits != 0 || p.Rules[1].UpperCents != nil {
		t.Fatalf("lost explicit zero, no upper or priority: %+v", p)
	}
	const invalid = `{"enabled":true,"rules":[{"enabled":true,"lowerCents":10,"upperCents":10,"rateUnits":9}]}`
	if r := req("admin", http.MethodPut, invalid, ReferralAutoRulesPut); r.Code != http.StatusBadRequest {
		t.Fatalf("invalid range HTTP %d: %s", r.Code, r.Body)
	}
	var audit int64
	if err := db.Model(&entity.Log{}).Where("module = ? AND user_id = ?", "referral", 1).Count(&audit).Error; err != nil || audit != 1 {
		t.Fatalf("audit count=%d err=%v", audit, err)
	}
	if err := db.Migrator().DropTable(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	if r := req("admin", http.MethodPut, `{"enabled":false,"rules":[]}`, ReferralAutoRulesPut); r.Code != http.StatusInternalServerError {
		t.Fatalf("unaudited config was accepted HTTP %d", r.Code)
	}
	if p := read(); !p.Enabled || len(p.Rules) != 2 {
		t.Fatalf("invalid or unaudited config was stored: %+v", p)
	}
}
