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
		"GET /adapter-service/public/wechat-login/ws":      false,
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
		"POST /adapter-service/user/phone/sms-code":   false,
		"PUT /adapter-service/user/phone":             false,
		"POST /adapter-service/user/wechat/bind":      false,
		"POST /adapter-service/user/wechat/bind-code": false,
		"DELETE /adapter-service/user/wechat":         false,
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

// 模型绑定与旧的模型映射路由都必须保持移除状态。
func TestSetAPIRouterDoesNotRegisterRemovedModelBindingRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	removed := map[string]bool{
		"/adapter-service/ai-model-mappings":          false,
		"/adapter-service/ai-model-mappings/:id":      false,
		"/adapter-service/ai-model-architectures":     false,
		"/adapter-service/ai-model-architectures/:id": false,
	}
	for _, route := range router.Routes() {
		if _, tracked := removed[route.Path]; tracked {
			t.Fatalf("expected route %s to be removed, but found %s", route.Path, route.Method)
		}
	}
}

func TestSetAPIRouterRegistersAIFallbackTaskRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	expected := map[string]bool{
		"POST /adapter-service/ai-fallback/tasks": false,
		"GET /adapter-service/ai-fallback/status": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := expected[key]; ok {
			expected[key] = true
		}
	}
	for route, found := range expected {
		if !found {
			t.Fatalf("expected route %s to be registered", route)
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

func TestSetAPIRouterAllowsAnyCrossOriginPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "https://adapter.example.com/adapter-service/search", nil)
	request.Header.Set("Origin", "https://any-origin.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type,authorization")

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status 204 for arbitrary origin, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://any-origin.example.com" {
		t.Fatalf("expected allow-origin header to echo request origin, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
}
