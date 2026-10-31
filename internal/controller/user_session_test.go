package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func TestSMSLoginReplacesPasswordSessionAndLogoutRevokesIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	phone := "13800000000"
	user := entity.User{Username: "mixed-login", Password: "password", Phone: &phone, LoginSessionID: "password-session"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	previous := util.GenerateJwtForUserSession(user.ID, user.Password, user.LoginSessionID)
	originalConsume, originalVerify := consumeCaptchaTokenWithDB, verifySMSCodeWithDB
	t.Cleanup(func() { consumeCaptchaTokenWithDB, verifySMSCodeWithDB = originalConsume, originalVerify })
	consumeCaptchaTokenWithDB = func(string, string, string) error { return nil }
	verifySMSCodeWithDB = func(string, string, string) (service.SMSVerificationEvidence, error) {
		return service.SMSVerificationEvidence{}, nil
	}
	response := phoneControllerRequest(t, http.MethodPost, "/adapter-service/user/login/sms", `{"phone":"13800000000","smsCode":"123456","captchaToken":"captcha"}`, nil, UserSMSLogin)
	if response.Code != http.StatusOK {
		t.Fatalf("SMS login: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		JWT string `json:"jwt"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.JWT == "" {
		t.Fatalf("SMS token: %s", response.Body.String())
	}
	router := gin.New()
	router.GET("/session", Auth, UserSession)
	router.POST("/logout", UserLogout)
	request := func(method, path, token string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
		router.ServeHTTP(response, req)
		return response
	}
	if res := request(http.MethodGet, "/session", previous); res.Code != http.StatusUnauthorized {
		t.Fatalf("old password session remains valid: %d", res.Code)
	}
	if res := request(http.MethodGet, "/session", result.JWT); res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("current SMS session: %d headers=%v", res.Code, res.Header())
	}
	if res := request(http.MethodPost, "/logout", previous); res.Code != http.StatusOK {
		t.Fatalf("stale logout status: %d", res.Code)
	}
	if res := request(http.MethodGet, "/session", result.JWT); res.Code != http.StatusOK {
		t.Fatalf("stale logout revoked SMS session: %d", res.Code)
	}
	if res := request(http.MethodPost, "/logout", result.JWT); res.Code != http.StatusOK {
		t.Fatalf("logout status: %d", res.Code)
	}
	if res := request(http.MethodGet, "/session", result.JWT); res.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session remains valid: %d", res.Code)
	}
}

func TestUserSessionHeartbeatDoesNotWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	user := entity.User{Username: "heartbeat-user", Password: "password"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	writes := 0
	if err := db.Callback().Update().Before("gorm:update").Register("count_heartbeat_updates", func(*gorm.DB) { writes++ }); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/session", Auth, UserSession)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/session", nil)
		req.Header.Set("Authorization", util.GenerateJwtForUser(user.ID, user.Password))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("heartbeat: %d", response.Code)
		}
	}
	if writes != 0 {
		t.Fatalf("heartbeat performed %d updates", writes)
	}
}
