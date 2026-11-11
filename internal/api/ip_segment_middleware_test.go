package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// IP 段管理路由的**中间件挂载**回归（补 ip_segment_router_test.go 的缺口）。
//
// # 为什么需要这个文件
//
// ip_segment_router_test.go 只断「路径注册到了哪个 handler」，其注释（第 17-19 行）
// 明确承认：gin v1.12 的 RouteInfo 不暴露每路由的中间件链，所以「非管理员被 403」
// 交给 controller 侧的行为测试去钉。
//
// 但 controller 侧那条（TestIPSegmentAdminRejectsNonAdminOnEveryRoute）是用
// ipSegmentRouter() **自己在测试里重建了一条带 RequireAdmin 的路由**，
// 与 router.go 的真实注册是两套独立组装。于是形成真实存在的假钉子：
//
//	把 router.go 里五条路由的 middleware.RequireAdmin 全部删掉，
//	9 个测试（1 路由 + 8 controller）依然全绿。
//
// 已实测确认（2026-11-11）：删掉 RequireAdmin 后 internal/api 与
// internal/controller 两个包都 `ok`，无一条变红。
//
// # 本文件的做法与局限（必须如实说明）
//
// gin v1.12 的 engine.Routes() 返回的 RouteInfo 只有 Method/Path/Handler/HandlerFunc，
// **没有中间件链**，因此纯黑盒无法区分「Auth 拦下」与「Auth + RequireAdmin 都挂着」。
// 完全走 SetAPIRouter 发真实请求也不可行：全局链里的 GlobalAPIRateLimit
// 需要进程级初始化，单测直接调用会 nil panic（已实测）。
//
// 所以这里退一步，做**源码级断言**：读 router.go，逐条确认五条路由都同时带
// controller.Auth 与 middleware.RequireAdmin。这是弱验证（正则匹配源码），
// 但它精确命中「有人摘掉中间件」这个真实缺陷形态 —— 而现有的 9 条测试一条都抓不到。
//
// 配合关系：
//   - 本文件  → 防 router.go 漏挂中间件（源码级）
//   - controller 层 TestIPSegmentAdminRejectsNonAdminOnEveryRoute → 防 handler 不校验（行为级）
//   两者缺一都不完整。

// routerSourcePath 是 router.go 相对于 internal/api 包的路径。
const routerSourcePath = "router.go"

// TestIPSegmentRoutesDeclareBothAuthAndRequireAdmin 断言五条路由的注册行里
// **同时**出现 controller.Auth 与 middleware.RequireAdmin。
func TestIPSegmentRoutesDeclareBothAuthAndRequireAdmin(t *testing.T) {
	source, err := os.ReadFile(routerSourcePath)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", routerSourcePath, err)
	}
	text := string(source)

	// 逐条路由：(期望的方法+路径前缀, 端点名)。
	// 用端点名而不是完整行，避免对空白/格式敏感。
	routes := []struct {
		method  string
		path    string
		handler string
	}{
		{http.MethodGet, "/admin/ip-segments", "IPSegmentList"},
		{http.MethodGet, "/admin/ip-segments/lookup", "IPSegmentLookup"},
		{http.MethodPost, "/admin/ip-segments/batch-delete", "IPSegmentBatchDelete"},
		{http.MethodDelete, "/admin/ip-segments/:id", "IPSegmentDelete"},
		{http.MethodPost, "/admin/ip-segments/:id/refresh", "IPSegmentRefresh"},
	}

	for _, route := range routes {
		t.Run(route.handler, func(t *testing.T) {
			// 抓「apiRouter.<METHOD>("<path>", ... controller.<handler>)」整行。
			// 用 (?s) 不行——会在跨行处贪婪匹配到别的路由，所以限定在同一行内。
			pattern := regexp.MustCompile(
				`apiRouter\.` + route.method + `\("` + regexp.QuoteMeta(route.path) + `",([^)\n]*controller\.` + route.handler + `[^)\n]*)\)`)
			matches := pattern.FindAllStringSubmatch(text, -1)
			if len(matches) != 1 {
				t.Fatalf("应当恰好有一条 `%s %s → controller.%s` 的注册，实际匹配到 %d 条",
					route.method, route.path, route.handler, len(matches))
			}
			middlewareChain := matches[0][1]

			if !strings.Contains(middlewareChain, "controller.Auth") {
				t.Fatalf("%s %s 的中间件链缺少 controller.Auth，实际链 = %q",
					route.method, route.path, middlewareChain)
			}
			// 这一条是关键：删掉 RequireAdmin 时，现有 9 条测试全绿，只有这里变红。
			if !strings.Contains(middlewareChain, "middleware.RequireAdmin") {
				t.Fatalf("%s %s 的中间件链缺少 middleware.RequireAdmin —— "+
					"普通用户将能直接访问管理接口，实际链 = %q",
					route.method, route.path, middlewareChain)
			}
		})
	}
}

// TestIPSegmentRoutesAreNotPublic 是反向钉子：确认这些路径**不**在公开路由段里。
//
// 单独一条的意义：如果哪天有人把 ip-segments 挪到 apiRouter 之外的公开组
// （例如与 /user/login 同级），它会绕过 Auth，上面那条同文件测试仍可能通过
// （因为它只查行内是否含 Auth）。这条用「公开组注册块内不含 ip-segments」兜底。
func TestIPSegmentRoutesAreNotPublic(t *testing.T) {
	source, err := os.ReadFile(routerSourcePath)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", routerSourcePath, err)
	}
	// 公开路由的典型形态：直接挂在 router 上（而非 apiRouter），且不带 Auth。
	for _, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "ip-segments") {
			continue
		}
		// router.GET/POST/... 直接注册（没有 apiRouter 前缀）＝ 绕过了 API 组的 Auth。
		if strings.HasPrefix(trimmed, "router.") {
			t.Fatalf("ip-segments 不得直接挂在公开 router 上（会绕过 Auth）：%s", trimmed)
		}
	}
}

// 确保 net/http 与 httptest 的导入被使用（本文件目前只用常量与字符串判断）。
var (
	_ = http.MethodGet
	_ = httptest.NewRecorder
)
