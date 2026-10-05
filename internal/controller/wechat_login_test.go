package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestCreateWechatLoginQRCodeControllerReturnsQRCodePayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings := loadWechatLoginSettings
	originalCreate := createWechatLoginQRCodeWithDB
	defer func() {
		loadWechatLoginSettings = originalSettings
		createWechatLoginQRCodeWithDB = originalCreate
	}()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatScanLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	createWechatLoginQRCodeWithDB = func(client service.WechatLoginClient) (service.WechatQRCodeResult, error) {
		if client.BaseURL != "https://wechat.example.com" || client.Token != "token" {
			t.Fatalf("unexpected client: %+v", client)
		}
		return service.WechatQRCodeResult{
			SceneID:         "scene-1",
			LoginToken:      "login-token-1",
			QRCodeImageData: "data:image/png;base64,QUJD",
			ExpireSeconds:   180,
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-login/qrcode", nil)

	CreateWechatLoginQRCode(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "login-token-1") || !strings.Contains(body, "qrcodeImageData") {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestCheckWechatLoginStatusControllerReturnsNeedBind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings := loadWechatLoginSettings
	originalCheck := checkWechatLoginStatusWithDB
	defer func() {
		loadWechatLoginSettings = originalSettings
		checkWechatLoginStatusWithDB = originalCheck
	}()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatScanLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	checkWechatLoginStatusWithDB = func(input service.WechatLoginStatusInput) (service.WechatLoginStatusResult, error) {
		if input.LoginToken != "login-token-1" {
			t.Fatalf("unexpected login token %s", input.LoginToken)
		}
		return service.WechatLoginStatusResult{Status: service.WechatLoginStatusNeedBind, BindToken: "bind-token-1"}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/public/wechat-login/status?loginToken=login-token-1", nil)

	CheckWechatLoginStatus(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "need_bind") || !strings.Contains(body, "bind-token-1") {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestCheckWechatLoginStatusControllerBindPurposeDoesNotSetAuthCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings := loadWechatLoginSettings
	originalCheck := checkWechatLoginStatusWithDB
	defer func() {
		loadWechatLoginSettings = originalSettings
		checkWechatLoginStatusWithDB = originalCheck
	}()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatScanLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	checkWechatLoginStatusWithDB = func(service.WechatLoginStatusInput) (service.WechatLoginStatusResult, error) {
		return service.WechatLoginStatusResult{Status: service.WechatLoginStatusSuccess, JWT: "jwt-other-user"}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/public/wechat-login/status?loginToken=login-token-1&purpose=bind", nil)

	CheckWechatLoginStatus(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if cookie := recorder.Header().Get("Set-Cookie"); strings.Contains(cookie, authCookieName) {
		t.Fatalf("binding status must not set auth cookie, got %q", cookie)
	}
}

func TestLoginWechatByCodeControllerReturnsNeedBind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings := loadWechatLoginSettings
	originalLoginByCode := loginWechatByCodeWithDB
	defer func() {
		loadWechatLoginSettings = originalSettings
		loginWechatByCodeWithDB = originalLoginByCode
	}()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatCodeLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	loginWechatByCodeWithDB = func(input service.WechatCodeLoginInput) (service.WechatLoginStatusResult, error) {
		if input.Code != "104014" {
			t.Fatalf("unexpected wechat code %s", input.Code)
		}
		if input.Client.BaseURL != "https://wechat.example.com" || input.Client.Token != "token" {
			t.Fatalf("unexpected client: %+v", input.Client)
		}
		return service.WechatLoginStatusResult{Status: service.WechatLoginStatusNeedBind, BindToken: "bind-token-code"}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-login/code", bytes.NewBufferString(`{"code":"104014"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	LoginWechatByCode(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "need_bind") || !strings.Contains(body, "bind-token-code") {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestBindWechatLoginControllerReturnsJWT(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalBind := bindWechatLoginWithDB
	defer func() {
		bindWechatLoginWithDB = originalBind
	}()
	bindWechatLoginWithDB = func(input service.WechatBindInput) (service.WechatBindResult, error) {
		if input.BindToken != "bind-token-1" || input.Username != "existing" || input.Password != "Abc12345!" {
			t.Fatalf("unexpected bind input: %+v", input)
		}
		return service.WechatBindResult{JWT: "jwt-1"}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-login/bind", bytes.NewBufferString(`{"bindToken":"bind-token-1","username":"existing","password":"Abc12345!"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	BindWechatLogin(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "jwt-1") {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestWechatLoginWebSocketSendsStatusAndPong(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings := loadWechatLoginSettings
	originalProbe := probeWechatScanStatusWithDB
	originalStore := service.DefaultWechatLoginSessionStore
	defer func() {
		loadWechatLoginSettings = originalSettings
		probeWechatScanStatusWithDB = originalProbe
		service.DefaultWechatLoginSessionStore = originalStore
	}()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatScanLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	probeWechatScanStatusWithDB = func(service.WechatScanProbeInput) (service.WechatScanProbeResult, error) {
		return service.WechatScanProbeResult{Status: service.WechatLoginStatusPending}, nil
	}
	store := service.NewWechatLoginSessionStore()
	store.Create("login-token-ws", time.Now(), time.Now().Add(time.Minute))
	service.DefaultWechatLoginSessionStore = store

	engine := gin.New()
	engine.GET("/ws", WechatLoginWebSocket)
	server := httptest.NewServer(engine)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?loginToken=login-token-ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	var initial map[string]any
	if err := conn.ReadJSON(&initial); err != nil {
		t.Fatalf("read initial status: %v", err)
	}
	if initial["type"] != "status" || initial["status"] != "pending" {
		t.Fatalf("unexpected initial message: %+v", initial)
	}
	if err := conn.WriteJSON(map[string]any{"type": "ping", "timestamp": 1}); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	var pong map[string]any
	if err := conn.ReadJSON(&pong); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if pong["type"] != "pong" {
		t.Fatalf("unexpected pong: %+v", pong)
	}
}
