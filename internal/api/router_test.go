package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSetAPIRouterRegistersResetPasswordRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	for _, route := range router.Routes() {
		if route.Method == "PUT" && route.Path == "/adapter-service/user/:id/reset-password" {
			return
		}
	}

	t.Fatal("expected reset password route to be registered")
}

func TestSetAPIRouterRegistersQuestionImportRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	for _, route := range router.Routes() {
		if route.Method == "POST" && route.Path == "/adapter-service/questions/import" {
			return
		}
	}

	t.Fatal("expected question import route to be registered")
}

func TestSetAPIRouterRegistersWechatLoginRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	expected := map[string]bool{
		"POST /adapter-service/public/wechat-login/qrcode": false,
		"GET /adapter-service/public/wechat-login/status":  false,
		"POST /adapter-service/public/wechat-login/bind":   false,
		"GET /adapter-service/public/wechat/user":          false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := expected[key]; ok {
			expected[key] = true
		}
	}
	for key, found := range expected {
		if !found {
			t.Fatalf("expected wechat login route %s to be registered", key)
		}
	}
}

func TestSetAPIRouterRegistersRemoteCaptchaProxyRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	expected := map[string]bool{
		"POST /adapter-service/public/captcha/remote-gen":   false,
		"GET /adapter-service/public/captcha/remote-gen":    false,
		"POST /adapter-service/public/captcha/remote-check": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := expected[key]; ok {
			expected[key] = true
		}
	}
	for key, found := range expected {
		if !found {
			t.Fatalf("expected remote captcha proxy route %s to be registered", key)
		}
	}
}

func TestSetAPIRouterDoesNotRegisterAIModelMappingRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	for _, route := range router.Routes() {
		if route.Path == "/adapter-service/ai-model-mappings" || route.Path == "/adapter-service/ai-model-mappings/:id" {
			t.Fatalf("expected AI model mapping routes to be removed, but found %s %s", route.Method, route.Path)
		}
	}
}

func TestSetAPIRouterHandlesSearchPreflightWithCORSHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/adapter-service/search", nil)
	request.Header.Set("Origin", "https://mooc1.chaoxing.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type,authorization")

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status 204 for preflight, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://mooc1.chaoxing.com" {
		t.Fatalf("expected allow-origin header to echo request origin, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
	if recorder.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("expected allow-methods header on preflight response")
	}
	if recorder.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Fatal("expected allow-headers header on preflight response")
	}
}
