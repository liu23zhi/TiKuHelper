package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestWechatBindStatusPreservesExistingLoginSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	wechatID := "already-bound-wechat"
	user := entity.User{Username: "bound-user", Password: "password-hash", Status: "enabled", WechatID: &wechatID, LoginSessionID: "existing-session"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"status": "success", "wechat_user": map[string]any{"openid": wechatID}}})
	}))
	t.Cleanup(server.Close)
	previousSettings := loadWechatLoginSettings
	previousCheck := checkWechatLoginStatusWithDB
	previousStore := service.DefaultWechatLoginSessionStore
	t.Cleanup(func() {
		loadWechatLoginSettings = previousSettings
		checkWechatLoginStatusWithDB = previousCheck
		service.DefaultWechatLoginSessionStore = previousStore
	})
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{WechatLoginEnabled: true, WechatScanLoginEnabled: true, WechatAPIBaseURL: server.URL, WechatAPIBearerToken: "test-token"}, nil
	}
	checkWechatLoginStatusWithDB = func(input service.WechatLoginStatusInput) (service.WechatLoginStatusResult, error) {
		input.Client.Client = server.Client()
		return service.CheckWechatLoginStatusWithDB(db, input)
	}
	service.DefaultWechatLoginSessionStore = service.NewWechatLoginSessionStore()
	service.DefaultWechatLoginSessionStore.Create("bind-status-token", time.Now(), time.Now().Add(time.Minute))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/public/wechat-login/status?loginToken=bind-status-token&purpose=bind", nil)
	CheckWechatLoginStatus(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var stored entity.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LoginSessionID != user.LoginSessionID {
		t.Fatalf("binding status rotated existing session: got %q, want %q", stored.LoginSessionID, user.LoginSessionID)
	}
	if strings.Contains(recorder.Body.String(), `"jwt"`) || strings.Contains(recorder.Header().Get("Set-Cookie"), authCookieName) {
		t.Fatalf("binding status issued login credentials: %s %s", recorder.Body.String(), recorder.Header())
	}
}
