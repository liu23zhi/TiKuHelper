package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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
			WechatLoginEnabled:   true,
			WechatAPIBaseURL:     "https://wechat.example.com",
			WechatAPIBearerToken: "token",
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
			WechatLoginEnabled:   true,
			WechatAPIBaseURL:     "https://wechat.example.com",
			WechatAPIBearerToken: "token",
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
