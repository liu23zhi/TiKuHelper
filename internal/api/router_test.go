package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
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
		"GET /api/user/session":                                     false,
		"GET /api/tickets":                                          false,
		"POST /api/tickets":                                         false,
		"GET /api/tickets/config":                                   false,
		"PUT /api/tickets/config":                                   false,
		"POST /api/tickets/config/test":                             false,
		"GET /api/tickets/:id":                                      false,
		"POST /api/tickets/:id/messages":                            false,
		"PUT /api/tickets/:id":                                      false,
		"POST /api/tickets/:id/notifications/:notificationId/retry": false,
		// 工单图片附件（上传 / 列表 / 用户端读图 / 管理端读图）。
		"POST /api/ticket-attachments":                    false,
		"GET /api/tickets/:id/attachments":                false,
		"GET /api/ticket-attachments/:attachmentId":       false,
		"GET /api/ticket-attachments/:attachmentId/admin": false,
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
	const want = "GET /api/logs/:id/sensitive-original"
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
			router.GET("/api/logs/:id/sensitive-original",
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
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/logs/1/sensitive-original", nil))

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
		if route.Method == "PUT" && route.Path == "/api/user/:id/reset-password" {
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
		if route.Method == "POST" && route.Path == "/api/questions/import" {
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
		"POST /api/search":       false,
		"POST /api/search/tasks": false,
		"GET /api/search/status": false,
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
		"GET /api/extensions/:source/latest":   false,
		"GET /api/extensions/:source/download": false,
		"POST /api/extensions/:source/refresh": false,
		"GET /api/extensions/config":           false,
		"PUT /api/extensions/config":           false,
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
		"POST /api/public/wechat-login/qrcode": false,
		"GET /api/public/wechat-login/status":  false,
		"GET /api/public/wechat-login/ws":      false,
		"POST /api/public/wechat-login/code":   false,
		"POST /api/public/wechat-login/bind":   false,
		"GET /api/public/wechat/user":          false,
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
		"POST /api/public/wechat-register/session": "",
		"POST /api/public/wechat-register/qrcode":  "",
		"GET /api/public/wechat-register/status":   "",
		"POST /api/public/wechat-register/code":    "",
		"POST /api/public/wechat-register/release": "",
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
		"PUT /api/user/profile":           false,
		"POST /api/user/phone/sms-code":   false,
		"PUT /api/user/phone":             false,
		"POST /api/user/wechat/bind":      false,
		"POST /api/user/wechat/bind-code": false,
		"DELETE /api/user/wechat":         false,
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
		"POST /api/public/captcha/remote-gen":   false,
		"GET /api/public/captcha/remote-gen":    false,
		"POST /api/public/captcha/remote-check": false,
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
		if route.Method == "GET" && route.Path == "/api/recharge-orders" {
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
		"POST /api/payment/notify": false,
		"GET /api/payment/notify":  false,
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
		"/api/ai-model-mappings":          false,
		"/api/ai-model-mappings/:id":      false,
		"/api/ai-model-architectures":     false,
		"/api/ai-model-architectures/:id": false,
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
		"POST /api/ai-fallback/tasks": false,
		"GET /api/ai-fallback/status": false,
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
	request := httptest.NewRequest(http.MethodOptions, "http://adapter.example.com/api/search", nil)
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
	request := httptest.NewRequest(http.MethodOptions, "https://adapter.example.com/api/search", nil)
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

// TestAPIPrefixMigrationKeepsOnlySearchCompat 钉住 2026-11-14 的路径改名结果。
//
// # 背景
//
// API 前缀由 `/adapter-service` 改为 `/api`。已安装的浏览器扩展与原生宿主里
// 写死了四个搜索路径，它们升级前改不了 URL，所以**只保留这四个**做兼容：
//
//	POST /adapter-service/search
//	POST /adapter-service/search/tasks
//	GET  /adapter-service/search/health
//	GET  /adapter-service/search/status
//
// # 为什么要钉「只有这四个」
//
// 最容易发生的退化是「顺手把整个旧前缀都挂回去」——那样旧路径会长期存活，
// 两条路径行为一旦漂移就无人发现，而且黑名单/限流等中间件容易只挂一边。
// 本用例同时钉住两个方向：
//   - 四个搜索路径在旧前缀下**必须**存在（否则老扩展升级前直接失效）
//   - 其它路径在旧前缀下**必须**不存在（否则兼容层悄悄扩大）
func TestAPIPrefixMigrationKeepsOnlySearchCompat(t *testing.T) {
	ginx := gin.New()
	SetAPIRouter(ginx)

	registered := map[string]bool{}
	for _, route := range ginx.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	// 四个兼容路径必须同时存在于**新旧两个前缀**下。
	compat := []string{
		"POST /search",
		"POST /search/tasks",
		"GET /search/health",
		"GET /search/status",
	}
	for _, suffix := range compat {
		if !registered["POST /api"+strings.TrimPrefix(suffix, "POST ")] &&
			!registered["GET /api"+strings.TrimPrefix(suffix, "GET ")] {
			t.Errorf("新前缀缺少 %s 路由", suffix)
		}
		legacy := replaceMethod(suffix, "/adapter-service")
		if !registered[legacy] {
			t.Errorf("旧扩展兼容缺失：%s 应当保留（已安装扩展升级前改不了 URL）", legacy)
		}
	}

	// 旧前缀下除这四个之外**不应**再有业务路由。
	allowed := map[string]bool{}
	for _, suffix := range compat {
		allowed[replaceMethod(suffix, "/adapter-service")] = true
	}
	var leaked []string
	for route := range registered {
		if !strings.Contains(route, "/adapter-service/") {
			continue
		}
		if !allowed[route] {
			leaked = append(leaked, route)
		}
	}
	sort.Strings(leaked)
	if len(leaked) > 0 {
		t.Errorf("旧前缀下出现了兼容层之外的 %d 条路由（兼容面必须最小）：\n- %s",
			len(leaked), strings.Join(leaked, "\n- "))
	}
}

// replaceMethod 把 "POST /search" 形态的 suffix 换成指定前缀下的完整路由串。
func replaceMethod(suffix, prefix string) string {
	parts := strings.SplitN(suffix, " ", 2)
	if len(parts) != 2 {
		return suffix
	}
	return parts[0] + " " + prefix + parts[1]
}
