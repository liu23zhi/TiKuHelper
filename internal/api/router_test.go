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
		"POST /adapter-service/public/wechat-login/code":   false,
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

func TestSetAPIRouterRegistersCurrentUserBindingRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	expected := map[string]bool{
		"POST /adapter-service/user/phone/sms-code": false,
		"PUT /adapter-service/user/phone":           false,
		"POST /adapter-service/user/wechat/bind":    false,
		"DELETE /adapter-service/user/wechat":       false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := expected[key]; ok {
			expected[key] = true
		}
	}
	for key, found := range expected {
		if !found {
			t.Fatalf("expected current-user binding route %s to be registered", key)
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

func TestSetAPIRouterRegistersRechargeOrderListRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	for _, route := range router.Routes() {
		if route.Method == "GET" && route.Path == "/adapter-service/recharge-orders" {
			return
		}
	}

	t.Fatal("expected recharge order list route to be registered")
}

func TestSetAPIRouterRegistersPaymentNotifyForPostAndGet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	expected := map[string]bool{
		"POST /adapter-service/payment/notify": false,
		"GET /adapter-service/payment/notify":  false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := expected[key]; ok {
			expected[key] = true
		}
	}
	for key, found := range expected {
		if !found {
			t.Fatalf("expected payment notify route %s to be registered", key)
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
	request := httptest.NewRequest(http.MethodOptions, "http://adapter.example.com/adapter-service/search", nil)
	request.Header.Set("Origin", "http://adapter.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type,authorization")

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status 204 for preflight, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "http://adapter.example.com" {
		t.Fatalf("expected allow-origin header to echo request origin, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
	if recorder.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("expected allow-methods header on preflight response")
	}
	if recorder.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Fatal("expected allow-headers header on preflight response")
	}
}

func TestSetAPIRouterRejectsUnlistedCrossOriginPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://allowed.example.com")

	router := gin.New()
	SetAPIRouter(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "http://adapter.example.com/adapter-service/search", nil)
	request.Header.Set("Origin", "https://attacker.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 for unlisted origin, got %d", recorder.Code)
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("expected no allow-origin header, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestSetAPIRouterAllowsAnyOriginWhenCORSAllowlistIsUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CORS_ALLOWED_ORIGINS", "")

	router := gin.New()
	SetAPIRouter(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "https://adapter.example.com/adapter-service/search", nil)
	request.Header.Set("Origin", "https://any-origin.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type,authorization")

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status 204 when CORS allowlist is unset, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://any-origin.example.com" {
		t.Fatalf("expected allow-origin header to echo request origin, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestSetAPIRouterAcceptsWildcardCORSPatternForChaoxing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://*.chaoxing.com")

	router := gin.New()
	SetAPIRouter(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "https://adapter.example.com/adapter-service/search", nil)
	request.Header.Set("Origin", "https://mooc1.chaoxing.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type,authorization")

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status 204 for wildcard preflight, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://mooc1.chaoxing.com" {
		t.Fatalf("expected allow-origin header to echo request origin, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestSetAPIRouterRejectsWildcardLookalikeOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://*.chaoxing.com")

	router := gin.New()
	SetAPIRouter(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "https://adapter.example.com/adapter-service/search", nil)
	request.Header.Set("Origin", "https://chaoxing.com.attacker.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 for lookalike origin, got %d", recorder.Code)
	}
}
