package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
)

func TestSetAPIRouterRegistersTicketRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)
	want := map[string]bool{
		"GET /adapter-service/user/session":                                     false,
		"GET /adapter-service/tickets":                                          false,
		"POST /adapter-service/tickets":                                         false,
		"GET /adapter-service/tickets/config":                                   false,
		"PUT /adapter-service/tickets/config":                                   false,
		"POST /adapter-service/tickets/config/test":                             false,
		"GET /adapter-service/tickets/:id":                                      false,
		"POST /adapter-service/tickets/:id/messages":                            false,
		"PUT /adapter-service/tickets/:id":                                      false,
		"POST /adapter-service/tickets/:id/notifications/:notificationId/retry": false,
		// 工单图片附件（上传 / 列表 / 用户端读图 / 管理端读图）。
		"POST /adapter-service/ticket-attachments":                    false,
		"GET /adapter-service/tickets/:id/attachments":                false,
		"GET /adapter-service/ticket-attachments/:attachmentId":       false,
		"GET /adapter-service/ticket-attachments/:attachmentId/admin": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Fatalf("missing route %s", route)
		}
	}
}

func TestSetAPIRouterRegistersSensitiveOriginalRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	// 原文接口是「管理员有按钮可以显示原文」的唯一入口，路由必须存在。
	const want = "GET /adapter-service/logs/:id/sensitive-original"
	found := false
	for _, route := range router.Routes() {
		if route.Method+" "+route.Path == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected route %s to be registered", want)
	}
}

func TestSensitiveOriginalRouteRejectsNonAdminBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 用真实中间件组装一条同构路由，只挂探针 handler：断言的是**权限链**，
	// 不牵扯数据库与业务逻辑（控制器内的二次校验另有 controller 包测试覆盖）。
	for _, testCase := range []struct {
		name       string
		user       *entity.User
		wantStatus int
		wantReach  bool
	}{
		{
			name:       "admin reaches handler",
			user:       &entity.User{ID: 1, Username: "admin-route", Role: "admin"},
			wantStatus: http.StatusOK,
			wantReach:  true,
		},
		{
			name:       "role case and surrounding spaces are normalized",
			user:       &entity.User{ID: 3, Username: "admin-spaced", Role: "  ADMIN  "},
			wantStatus: http.StatusOK,
			wantReach:  true,
		},
		{
			name:       "member is forbidden",
			user:       &entity.User{ID: 2, Username: "member-route", Role: "user"},
			wantStatus: http.StatusForbidden,
			wantReach:  false,
		},
		{
			name:       "anonymous is forbidden",
			user:       nil,
			wantStatus: http.StatusForbidden,
			wantReach:  false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reached := false
			router := gin.New()
			// 第一个中间件模拟 controller.Auth 已把 user 放进上下文（匿名时什么都不放）。
			router.GET("/adapter-service/logs/:id/sensitive-original",
				func(c *gin.Context) {
					if testCase.user != nil {
						c.Set("user", testCase.user)
					}
				},
				middleware.RequireAdmin,
				func(c *gin.Context) {
					reached = true
					c.Status(http.StatusOK)
				})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/adapter-service/logs/1/sensitive-original", nil))

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if reached != testCase.wantReach {
				t.Fatalf("handler reached=%t want=%t", reached, testCase.wantReach)
			}
		})
	}
}

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

func TestSetAPIRouterRegistersAsyncSearchRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)

	want := map[string]bool{
		"POST /adapter-service/search":       false,
		"POST /adapter-service/search/tasks": false,
		"GET /adapter-service/search/status": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Fatalf("expected route %s to be registered", route)
		}
	}
}

func TestSetAPIRouterRegistersExtensionRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)
	want := map[string]bool{
		"GET /adapter-service/extensions/:source/latest":   false,
		"GET /adapter-service/extensions/:source/download": false,
		"POST /adapter-service/extensions/:source/refresh": false,
		"GET /adapter-service/extensions/config":           false,
		"PUT /adapter-service/extensions/config":           false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Fatalf("expected route %s to be registered", route)
		}
	}
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

func TestSetAPIRouterRegistersWechatRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)
	want := map[string]string{
		"POST /adapter-service/public/wechat-register/session": "",
		"POST /adapter-service/public/wechat-register/qrcode":  "",
		"GET /adapter-service/public/wechat-register/status":   "",
		"POST /adapter-service/public/wechat-register/code":    "",
		"POST /adapter-service/public/wechat-register/release": "",
	}
	for _, route := range router.Routes() {
		delete(want, route.Method+" "+route.Path)
	}
	if len(want) != 0 {
		t.Fatalf("missing registration routes: %v", want)
	}
}

func TestSetAPIRouterRegistersCurrentUserBindingRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	expected := map[string]bool{
		"PUT /adapter-service/user/profile":           false,
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
	request.Header.Set("Access-Control-Request-Headers", "content-type,authorization,idempotency-key")

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status 204 for preflight, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("Token-only CORS must never enable browser Cookie credentials")
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("expected wildcard allow-origin for explicit Token CORS, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
	if recorder.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("expected allow-methods header on preflight response")
	}
	allowedHeaders := strings.Split(recorder.Header().Get("Access-Control-Allow-Headers"), ",")
	if len(allowedHeaders) == 0 || strings.TrimSpace(allowedHeaders[0]) == "" {
		t.Fatal("expected allow-headers header on preflight response")
	}
	for _, header := range allowedHeaders {
		if strings.EqualFold(strings.TrimSpace(header), "Idempotency-Key") {
			return
		}
	}
	t.Fatalf("expected Idempotency-Key to be allowed, got %q", recorder.Header().Get("Access-Control-Allow-Headers"))
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
	if recorder.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("Token-only CORS must never enable browser Cookie credentials")
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("expected wildcard allow-origin for explicit Token CORS, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
}
