package controller

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReferralManagementOnlyAdminAndPreservesUnrelatedSettings(t *testing.T) {
	db := newReferralControllerTestDB(t)
	previous := referralDB
	t.Cleanup(func() { referralDB = previous })
	referralDB = func() *gorm.DB { return db }
	db.Create(&entity.SystemSetting{Key: "site_name", Value: "preserve"})
	request := func(role, method, path, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(out)
		c.Set("user", &entity.User{ID: 1, Role: role, Status: "enabled"})
		c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		return out
	}
	if out := request("user", http.MethodPut, "/api/admin/referral-settings", `{"enabled":true,"rate":0.1}`, ReferralSettingsPut); out.Code != 403 {
		t.Fatalf("nonadmin settings: %d", out.Code)
	}
	if out := request("user", http.MethodGet, "/api/admin/referral-invites", "", ReferralInviteAdminList); out.Code != 403 {
		t.Fatalf("nonadmin code inventory: %d", out.Code)
	}
	if out := request("admin", http.MethodPut, "/api/admin/referral-settings", `{"enabled":true,"rate":0.1}`, ReferralSettingsPut); out.Code != 200 {
		t.Fatalf("admin settings: %d %s", out.Code, out.Body.String())
	}
	var unrelated entity.SystemSetting
	db.Where("key = ?", "site_name").First(&unrelated)
	if unrelated.Value != "preserve" {
		t.Fatal("referral-only update overwrote unrelated setting")
	}
	if got := service.GetReferralSettingsWithDB(db); !got.Enabled || got.Rate != 0.1 {
		t.Fatalf("settings=%+v", got)
	}
	if out := request("admin", http.MethodPut, "/api/admin/referral-settings", `{}`, ReferralSettingsPut); out.Code != 400 {
		t.Fatal("missing fields must not silently disable rewards")
	}
}
