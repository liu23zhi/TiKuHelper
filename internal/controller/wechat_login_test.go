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
	"github.com/itihey/tikuAdapter/internal/entity"
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
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/public/wechat-login/qrcode", nil)

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
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/public/wechat-login/status?loginToken=login-token-1", nil)

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
	checkWechatLoginStatusWithDB = func(input service.WechatLoginStatusInput) (service.WechatLoginStatusResult, error) {
		if !input.BindOnly {
			t.Fatal("binding status must use the non-login service path")
		}
		return service.WechatLoginStatusResult{Status: service.WechatLoginStatusSuccess, JWT: "jwt-other-user"}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/public/wechat-login/status?loginToken=login-token-1&purpose=bind", nil)

	CheckWechatLoginStatus(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if cookie := recorder.Header().Get("Set-Cookie"); strings.Contains(cookie, authCookieName) {
		t.Fatalf("binding status must not set auth cookie, got %q", cookie)
	}
	if strings.Contains(recorder.Body.String(), "jwt-other-user") {
		t.Fatalf("binding status must not return login credentials: %s", recorder.Body.String())
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
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/public/wechat-login/code", bytes.NewBufferString(`{"code":"104014"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	LoginWechatByCode(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "need_bind") || !strings.Contains(body, "bind-token-code") {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestResolveAdminWechatIDByScanReturnsWechatID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings := loadWechatLoginSettings
	originalResolve := resolveAdminWechatScanID
	defer func() {
		loadWechatLoginSettings = originalSettings
		resolveAdminWechatScanID = originalResolve
	}()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatScanLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	resolveAdminWechatScanID = func(client service.WechatLoginClient, loginToken string) (service.WechatScanIDResult, error) {
		if client.BaseURL != "https://wechat.example.com" || client.Token != "token" || loginToken != "login-token-1" {
			t.Fatalf("unexpected scan input client=%+v loginToken=%s", client, loginToken)
		}
		return service.WechatScanIDResult{Status: service.WechatLoginStatusSuccess, WechatID: "openid-scan"}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/wechat/scan-id?loginToken=login-token-1", nil)

	ResolveAdminWechatIDByScan(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "openid-scan") || strings.Contains(body, "jwt") {
		t.Fatalf("unexpected body %s", body)
	}
}

func TestResolveAdminWechatIDByCodeReturnsWechatID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings := loadWechatLoginSettings
	originalFetch := fetchWechatUserIDByCode
	defer func() {
		loadWechatLoginSettings = originalSettings
		fetchWechatUserIDByCode = originalFetch
	}()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			WechatLoginEnabled:     true,
			WechatCodeLoginEnabled: true,
			WechatAPIBaseURL:       "https://wechat.example.com",
			WechatAPIBearerToken:   "token",
		}, nil
	}
	fetchWechatUserIDByCode = func(client service.WechatLoginClient, code string) (string, error) {
		if client.BaseURL != "https://wechat.example.com" || client.Token != "token" || code != "104014" {
			t.Fatalf("unexpected code input client=%+v code=%s", client, code)
		}
		return "openid-code", nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/wechat/code-id", bytes.NewBufferString(`{"code":"104014"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	ResolveAdminWechatIDByCode(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "openid-code") || strings.Contains(body, "jwt") {
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
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/public/wechat-login/bind", bytes.NewBufferString(`{"bindToken":"bind-token-1","username":"existing","password":"Abc12345!"}`))
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

func TestWechatLoginWebSocketDeliversTerminalStatusBeforeClose(t *testing.T) {
	for _, status := range []string{service.WechatLoginStatusSuccess, service.WechatLoginStatusNeedBind, service.WechatLoginStatusExpired} {
		for _, alreadyScanned := range []bool{true, false} {
			t.Run(status+map[bool]string{true: "/initial", false: "/after-poll"}[alreadyScanned], func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				previousSettings, previousProbe := loadWechatLoginSettings, probeWechatScanStatusWithDB
				previousStore := service.DefaultWechatLoginSessionStore
				t.Cleanup(func() {
					loadWechatLoginSettings, probeWechatScanStatusWithDB = previousSettings, previousProbe
					service.DefaultWechatLoginSessionStore = previousStore
				})
				loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
					return service.SystemSettingDetail{WechatLoginEnabled: true, WechatScanLoginEnabled: true, WechatAPIBaseURL: "https://wechat.example.com", WechatAPIBearerToken: "token"}, nil
				}
				calls := 0
				probeWechatScanStatusWithDB = func(service.WechatScanProbeInput) (service.WechatScanProbeResult, error) {
					calls++
					if !alreadyScanned && calls == 1 {
						return service.WechatScanProbeResult{Status: service.WechatLoginStatusPending}, nil
					}
					return service.WechatScanProbeResult{Status: status}, nil
				}
				service.DefaultWechatLoginSessionStore = service.NewWechatLoginSessionStore()
				service.DefaultWechatLoginSessionStore.Create("terminal-session", time.Now(), time.Now().Add(time.Minute))
				engine := gin.New()
				engine.GET("/ws", WechatLoginWebSocket)
				server := httptest.NewServer(engine)
				defer server.Close()
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?loginToken=terminal-session", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
				for _, want := range []string{service.WechatLoginStatusPending, status} {
					var message map[string]any
					if err := conn.ReadJSON(&message); err != nil {
						t.Fatalf("read %s before close: %v", want, err)
					}
					if message["type"] != "status" || message["status"] != want {
						t.Fatalf("message=%+v want=%s", message, want)
					}
					if message["jwt"] != nil || message["bindToken"] != nil {
						t.Fatal("status transport must not issue authentication credentials")
					}
				}
			})
		}
	}
}
