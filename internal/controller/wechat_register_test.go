package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestPublicWechatRegisterStatusRejectsWhenWechatRegistrationIsDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousStore := wechatRegisterFlowStore
	previousSettings := loadWechatLoginSettings
	wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{}, nil
	}
	defer func() {
		wechatRegisterFlowStore = previousStore
		loadWechatLoginSettings = previousSettings
	}()
	flow, err := wechatRegisterFlowStore.CreateOrResume("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var router = gin.New()
	router.POST("/adapter-service/public/wechat-register/qrcode", PublicWechatRegisterQRCode)
	router.GET("/adapter-service/public/wechat-register/status", PublicWechatRegisterStatus)
	recorder := httptest.NewRecorder()
	qrcodeRequest := httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-register/qrcode", bytes.NewBufferString(`{"registrationFlowToken":"`+flow.Token+`"}`))
	qrcodeRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, qrcodeRequest)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected qrcode config error, got %d: %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	statusRequest := httptest.NewRequest(http.MethodGet, "/adapter-service/public/wechat-register/status?sessionId=unknown", nil)
	router.ServeHTTP(recorder, statusRequest)
	if recorder.Code == http.StatusOK {
		t.Fatalf("expected status config error, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicWechatRegisterSessionDoesNotAcceptJWTAsFlowToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := wechatRegisterFlowStore
	wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
	defer func() { wechatRegisterFlowStore = previous }()
	router := gin.New()
	router.POST("/adapter-service/public/wechat-register/session", PublicWechatRegisterSession)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-register/session", nil)
	request.Header.Set("Authorization", "Bearer login-jwt")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected session creation, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["wechatId"]; ok {
		t.Fatal("registration response must not expose wechatId")
	}
	if _, ok := payload["registrationFlowToken"]; !ok {
		t.Fatalf("expected opaque registration token in response: %s", recorder.Body.String())
	}
}

func TestPublicWechatRegisterRoutesRejectClientWechatIDAndExposeOpaqueStatusOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := wechatRegisterFlowStore
	wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
	defer func() { wechatRegisterFlowStore = previous }()
	router := gin.New()
	router.POST("/adapter-service/public/wechat-register/session", PublicWechatRegisterSession)
	router.POST("/adapter-service/public/wechat-register/code", PublicWechatRegisterCode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-register/code", bytes.NewBufferString(`{"registrationFlowToken":"bad","wechatId":"client-supplied"}`))
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid flow token rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("client-supplied")) {
		t.Fatal("client wechatId must not be echoed")
	}
}

func TestPublicWechatRegisterQRCodeReturnsDisplayDataAndSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousStore := wechatRegisterFlowStore
	previousSettings := loadWechatLoginSettings
	previousCreate := createWechatRegisterQRCode
	wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{WechatLoginEnabled: true, WechatScanLoginEnabled: true, WechatAPIBaseURL: "https://wechat.example.com", WechatAPIBearerToken: "token"}, nil
	}
	createWechatRegisterQRCode = func(service.WechatLoginClient) (service.WechatQRCodeResult, error) {
		return service.WechatQRCodeResult{LoginToken: "register-session", QRCodeImageData: "data:image/png;base64,abc", ExpiresAtUnix: time.Now().Add(time.Minute).UnixMilli()}, nil
	}
	defer func() {
		wechatRegisterFlowStore = previousStore
		loadWechatLoginSettings = previousSettings
		createWechatRegisterQRCode = previousCreate
	}()
	flow, err := wechatRegisterFlowStore.CreateOrResume("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/adapter-service/public/wechat-register/qrcode", PublicWechatRegisterQRCode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/adapter-service/public/wechat-register/qrcode", bytes.NewBufferString(`{"registrationFlowToken":"`+flow.Token+`"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte("register-session")) || !bytes.Contains(recorder.Body.Bytes(), []byte("data:image/png")) {
		t.Fatalf("expected public QR session/display data, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("wechatId")) {
		t.Fatal("registration QR response must not expose wechatId")
	}
}

func TestPublicWechatRegisterStatusRemovesExpiredUpstreamQRCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousStore := wechatRegisterFlowStore
	previousSettings := loadWechatLoginSettings
	previousPoll := pollWechatRegisterQRCode
	wechatRegisterFlowStore = service.NewWechatRegisterFlowStore()
	loadWechatLoginSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{WechatLoginEnabled: true, WechatScanLoginEnabled: true, WechatAPIBaseURL: "https://wechat.example.com", WechatAPIBearerToken: "token"}, nil
	}
	pollWechatRegisterQRCode = func(service.WechatLoginClient, string) (service.WechatRegisterQRCodeStatus, error) {
		return service.WechatRegisterQRCodeStatus{Status: service.WechatLoginStatusExpired}, nil
	}
	defer func() {
		wechatRegisterFlowStore = previousStore
		loadWechatLoginSettings = previousSettings
		pollWechatRegisterQRCode = previousPoll
	}()
	flow, err := wechatRegisterFlowStore.CreateOrResume("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wechatRegisterFlowStore.GetOrCreateQRCode(flow.Token, time.Now(), false, func() (service.WechatRegisterQRCode, error) {
		return service.WechatRegisterQRCode{SessionID: "expired-upstream-session", ExpiresAt: time.Now().Add(time.Minute)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/adapter-service/public/wechat-register/status", PublicWechatRegisterStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/adapter-service/public/wechat-register/status?sessionId=expired-upstream-session", nil))
	if recorder.Code != http.StatusGone {
		t.Fatalf("expected expired status, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := wechatRegisterFlowStore.StatusByQRCodeSession("expired-upstream-session", time.Now()); err == nil {
		t.Fatal("expected expired upstream session mapping to be removed")
	}
}
