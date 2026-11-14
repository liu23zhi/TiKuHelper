package api

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// IP 段归属管理页（task_doc/71 §3.5）的路由注册。
//
// 断言两件事：
//  1. 五条路由真的挂在 /api/admin/ip-segments 族上（路径用**复数**，
//     与单 IP 的 ip-location-cache 不能混）；
//  2. 每条路由的末端 handler 是 IP 段那个函数，而不是被复制粘贴成别的管理接口。
//
// 中间件链**不在**这里断：gin v1.12 的 RouteInfo 只有 Method/Path/Handler/HandlerFunc，
// 不暴露每路由的完整链（没有 HandlersCount），所以「非管理员 403」只能由
// controller 侧的行为测试来钉（internal/controller/ip_segment_test.go）。
func TestSetAPIRouterRegistersIPSegmentAdminRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	want := map[string]string{
		"GET /api/admin/ip-segments":               "IPSegmentList",
		"GET /api/admin/ip-segments/lookup":        "IPSegmentLookup",
		"POST /api/admin/ip-segments/batch-delete": "IPSegmentBatchDelete",
		"DELETE /api/admin/ip-segments/:id":        "IPSegmentDelete",
		"POST /api/admin/ip-segments/:id/refresh":  "IPSegmentRefresh",
	}
	found := make(map[string]string, len(want))
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := want[key]; exists {
			found[key] = route.Handler
		}
	}

	for routeKey, handlerName := range want {
		actual, ok := found[routeKey]
		if !ok {
			t.Fatalf("missing IP segment admin route %s", routeKey)
		}
		if !strings.HasSuffix(actual, "."+handlerName) {
			t.Fatalf("%s must be wired to controller.%s, got handler %q", routeKey, handlerName, actual)
		}
	}

	// 反向钉子：不许出现单数路径（ip-segment）。复数不是风格问题 ——
	// 单数写法会和既有的 ip-location-cache 在侧边栏与日志排查时混淆，
	// 而且与 entity 表名 ip_segments 不对应。
	for _, route := range router.Routes() {
		if strings.Contains(route.Path, "/ip-segment") && !strings.Contains(route.Path, "/ip-segments") {
			t.Fatalf("IP segment routes must use the plural path, got %s %s", route.Method, route.Path)
		}
	}
}

// 判据①：**不存在**任何手动导入端点。
//
// # 用户口径（2026-11-13 明确改正）
//
// IP 段归属从「管理员手动点按钮批量导入」改成 **自动导入**：
//
//	上游查询成功 → service.PersistIPSegmentsFromCacheRows（实时沉淀）
//	缓存命中     → service.PersistMissingIPSegmentsFromCacheRows（读时补写）
//	进程启动     → registry.EnsureIPSegmentBackfill（存量回填）
//
// 三条路径全自动，没有任何按钮。因此一旦出现导入端点，就说明有人按**旧口径**
// 加回了手动入口 —— 那是功能性回退（管理员会以为必须手动点一次，而实际上
// 数据早已自动沉淀，重复导入还会造成「以为是新数据」的误解）。
//
// 这条是**反向断言**：它断言的是「某物不存在」。反向断言很容易写成永远为真
// （例如只 grep 一个不可能出现的字符串），所以这里同时：
//   - 断言路由表非空且确实含那 5 条 admin 路由（证明扫描对象是对的）；
//   - 用**多个**模式覆盖两种已知命名（import-from-location-cache / ip-segments/import）
//     以及更宽的 `import` 词根，避免有人换个命名绕过去。
//
// 变异验证：手工加一条 POST /api/admin/ip-segments/import 路由 →
// 本用例必须变红。
func TestIPSegmentRoutesExposeNoManualImportEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)
	routes := router.Routes()
	if len(routes) == 0 {
		t.Fatal("路由表为空，反向断言没有扫描对象（测试装置失效）")
	}

	// 前置：确认扫描对象里确实有 IP 段那批路由。
	// 没有这条，「路由表里没有 import」可能只是因为整批路由都没注册。
	var segmentAdminRoutes int
	for _, route := range routes {
		if strings.Contains(route.Path, "/admin/ip-segments") {
			segmentAdminRoutes++
		}
	}
	if segmentAdminRoutes != 5 {
		t.Fatalf("expected exactly the 5 existing IP segment admin handlers, got %d", segmentAdminRoutes)
	}

	// 用户口径明确点名的两种命名。
	forbidden := []string{
		"import-from-location-cache",
		"ip-segments/import",
		// 更宽的兜底：ip-segments 族下任何含 import 的路径都不允许
		// （换命名绕过前两条时仍会命中这里）。
		"ip-segment" + "s/" + "i" + "mport",
	}
	for _, route := range routes {
		lowered := strings.ToLower(route.Path)
		for _, pattern := range forbidden {
			if strings.Contains(lowered, strings.ToLower(pattern)) {
				t.Fatalf("用户口径是「自动导入」：路由表里不得存在手动导入端点，发现 %s %s",
					route.Method, route.Path)
			}
		}
		// 兜底：ip-segments 族里不许出现 import 字样（大小写不敏感）。
		if strings.Contains(lowered, "/admin/ip-segments") && strings.Contains(lowered, "import") {
			t.Fatalf("用户口径是「自动导入」：ip-segments 族下不得存在导入端点，发现 %s %s",
				route.Method, route.Path)
		}
	}
}
