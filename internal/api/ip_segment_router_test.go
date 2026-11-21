package api

import (
	"github.com/gin-gonic/gin"
	"os"
	"strings"
	"testing"
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

// 判据①：**不存在**独立的「手动导入」实现。
//
// # 口径演变（两次都是用户明确要求，不要只看其中一条）
//
// 2026-11-13：从「管理员手动点按钮批量导入」改成 **自动导入**：
//
//	上游查询成功 → service.PersistIPSegmentsFromCacheRows（实时沉淀）
//	缓存命中     → service.PersistMissingIPSegmentsFromCacheRows（读时补写）
//	进程启动     → registry.EnsureIPSegmentBackfill（存量回填）
//
// 2026-11-15：在上面三条全自动的基础上，**加一个「立即同步」按钮**。
// 理由是自动通路都不由用户控制时机（刚上线段表要等重启，或等某个 IP 恰好被查一次）。
// 该端点的语义是「现在就跑一次自动沉淀」，**不新增任何导入规则**：
// 它必须调用 service.PersistMissingIPSegmentsFromCacheRows，与缓存命中路径同一个函数。
//
// ⇒ 因此本用例的判据从「任何含 import 的端点都不许有」收窄为：
//
//	**不许存在绕过自动沉淀的独立导入逻辑**。
//	判据落点改成两条：
//	  ① ip-segments 族下不得出现 import 字样（仍防止按旧口径加回 import 端点）；
//	  ② 「立即同步」端点必须存在，且其 handler 必须调用同一个沉淀函数
//	     （见 TestIPSegmentSyncEndpointReusesAutoSediment）。
//
// 为什么仍要保留 import 禁令：sync 与 import 的语义差别不是命名游戏 ——
// import 意味着「从外部灌一份数据进来」，而 sync 只做「把已缓存的行补写成段」。
// 一旦有人加回 import，就意味着出现了绕过缓存的第二条数据来源。
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
	// 5 条原有 + 1 条「立即同步」（2026-11-15 方案 B 新增）。
	if segmentAdminRoutes != 6 {
		t.Fatalf("expected the 5 existing IP segment admin handlers plus the sync endpoint (6 total), got %d", segmentAdminRoutes)
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

// 判据②：「立即同步」端点必须复用自动沉淀的同一个函数，不允许自己写一遍 SQL。
//
// 这是 2026-11-15 方案 B 的核心约束。那个按钮之所以安全，唯一理由是
// 「它和自动通路做的是同一件事」；一旦有人在 handler 里重写一份 upsert，
// 就会出现两套唯一键口径 —— 表现为「自动沉淀判为已存在、手动同步判为缺失」
// 或反之，而这种不一致在页面上看起来只是「数字对不上」，极难定位。
//
// 判据走源码断言（不是行为断言），因为要钉的正是「调用的是哪个函数」这件事：
// 行为断言在两个实现都正确时都会绿，抓不到「实现了第二套」。
func TestIPSegmentSyncEndpointReusesAutoSediment(t *testing.T) {
	source, err := os.ReadFile("../controller/ip_segment.go")
	if err != nil {
		t.Fatalf("read controller source: %v", err)
	}
	text := string(source)

	// 定位 handler 函数体（从 func 声明到下一个顶层 func 之前）。
	start := strings.Index(text, "func IPSegmentSyncFromCache(")
	if start < 0 {
		t.Fatal("IPSegmentSyncFromCache 必须存在 —— 方案 B 的「立即同步」按钮依赖它")
	}
	rest := text[start+len("func IPSegmentSyncFromCache("):]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		end = len(rest)
	}
	body := rest[:end]

	// 必须调用自动沉淀函数。
	if !strings.Contains(body, "service.PersistMissingIPSegmentsFromCacheRows") {
		t.Fatal("同步端点必须调用 service.PersistMissingIPSegmentsFromCacheRows —— " +
			"它是缓存命中路径与启动回填共用的沉淀函数；自己写一份会出现两套唯一键口径")
	}

	// 反向：不得自己写 upsert / INSERT。
	for _, forbidden := range []string{"UpsertIPSegmentsWithDB", "INSERT INTO", "clause.OnConflict"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("同步端点不得自己实现写入（发现 %s）—— 必须走共享沉淀函数", forbidden)
		}
	}

	// 必须仍有管理员鉴权：这是能改全站段表的写操作。
	if !strings.Contains(body, "requireAdmin(c)") {
		t.Fatal("同步端点必须 requireAdmin —— 它会写入全站 IP 段表")
	}
}
