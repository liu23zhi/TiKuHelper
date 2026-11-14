package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 脱敏序列端点（/api/service-status/series）的路由鉴权钉子。
//
// # 为什么是源码级断言而不是发真实请求
//
// 与 storage_status_router_test.go 同一组理由（gin engine.Routes() 不暴露中间件链；
// 走 SetAPIRouter 会因 GlobalAPIRateLimit 解引用 manager 而 nil panic）。
//
// 本文件精确命中两个真实缺陷形态：
//  1. 有人「顺手跟 /admin/service-status 保持一致」→ 加上 RequireAdmin
//     ⇒ 普通用户（以及只持 adapter key 的 PLUS）全拿 403，需求只兑现给管理员；
//  2. 有人改用 controller.Auth（只收 JWT/cookie）
//     ⇒ PLUS 持 adapter key 会拿 401「请重新登录」，表现为状态页恒显空曲线。

const seriesRouteChainPattern = `apiRouter\.GET\("/service-status/series",([^)\n]*)\)`

// TestServiceStatusSeriesRouteUsesAuthOrAccessToken 断言：路由存在、
// 鉴权是 AuthOrAccessToken、**不带** RequireAdmin。
func TestServiceStatusSeriesRouteUsesAuthOrAccessToken(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}

	matches := regexp.MustCompile(seriesRouteChainPattern).FindAllStringSubmatch(string(source), -1)
	if len(matches) != 1 {
		t.Fatalf("应当恰好有一条 GET /service-status/series 的注册，实际匹配到 %d 条", len(matches))
	}
	chain := matches[0][1]

	if !strings.Contains(chain, "controller.GetServiceStatusSeries") {
		t.Fatalf("路由必须指向 controller.GetServiceStatusSeries，实际链 = %q", chain)
	}
	// ★ 本文件的核心断言之一：必须是 AuthOrAccessToken。
	if !strings.Contains(chain, "controller.AuthOrAccessToken") {
		t.Fatalf("序列端点必须挂 controller.AuthOrAccessToken（PLUS 只持 adapter key，"+
			"走 Auth 会 401），实际链 = %q", chain)
	}
	// ★ 核心断言之二：绝不能带 RequireAdmin。
	if strings.Contains(chain, "middleware.RequireAdmin") {
		t.Fatalf("序列端点**不得**挂 RequireAdmin —— 用户已拍板「让普通用户也能看曲线」，"+
			"挂上后需求只兑现给管理员。实际链 = %q", chain)
	}
}

// TestServiceStatusSeriesRouteMirrorsSummaryAuth 反向对照：
// series 与 summary 必须用**同一条**鉴权链。
//
// 两条端点的消费方完全相同（普通用户 + PLUS adapter key），鉴权一旦分叉，
// 就会出现「摘要能看、曲线 403」这种半残状态。
func TestServiceStatusSeriesRouteMirrorsSummaryAuth(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	text := string(source)

	summaryChain := regexp.MustCompile(`apiRouter\.GET\("/service-status/summary",([^)\n]*)\)`).FindStringSubmatch(text)
	seriesChain := regexp.MustCompile(seriesRouteChainPattern).FindStringSubmatch(text)
	if summaryChain == nil || seriesChain == nil {
		t.Fatalf("找不到 summary(%v) 或 series(%v) 的路由注册", summaryChain, seriesChain)
	}

	// 去掉 handler 名后比较中间件部分。
	normalize := func(chain string) string {
		chain = strings.ReplaceAll(chain, "controller.GetServiceStatusSummary", "")
		chain = strings.ReplaceAll(chain, "controller.GetServiceStatusSeries", "")
		return strings.Trim(strings.TrimSpace(chain), ",")
	}
	if normalize(summaryChain[1]) != normalize(seriesChain[1]) {
		t.Fatalf("series 与 summary 的鉴权链必须一致：summary=%q series=%q",
			summaryChain[1], seriesChain[1])
	}
}

// TestServiceStatusSeriesRouteIsInsideAPIRouter 反向钉子：确认没被挪到公开段。
//
// 直接挂在全局 router 上会绕过 Auth 与黑名单豁免策略 —— 那等于把逐分钟
// 业务曲线公开给任何扫描器。
func TestServiceStatusSeriesRouteIsInsideAPIRouter(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	text := string(source)

	bareRoute := regexp.MustCompile(`\brouter\.(?:GET|Any|POST)\("/[^"]*service-status/series"`)
	if bareRoute.MatchString(text) {
		t.Fatal("序列端点不得直接挂在全局 router 上（会绕过 Auth 与黑名单豁免）")
	}
	if !regexp.MustCompile(seriesRouteChainPattern).MatchString(text) {
		t.Fatal("找不到 apiRouter 上的 /service-status/series 路由")
	}
}

// TestAdminServiceStatusRouteUnchanged 保护红线：admin 端点不得被本次改动动过。
//
// 它有别的消费方（管理端状态页）。本次任务只新增普通用户端点，
// 任何对 admin 链的修改（放宽或收紧）都应当被发现。
func TestAdminServiceStatusRouteUnchanged(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	chain := regexp.MustCompile(`apiRouter\.GET\("/admin/service-status",([^)\n]*)\)`).
		FindStringSubmatch(string(source))
	if chain == nil {
		t.Fatal("找不到 admin 服务状态路由 —— 它不该被删除")
	}
	// 必须仍然是 Auth + RequireAdmin + GetServiceStatus。
	for _, want := range []string{"controller.Auth", "middleware.RequireAdmin", "controller.GetServiceStatus"} {
		if !strings.Contains(chain[1], want) {
			t.Fatalf("admin 服务状态路由的链必须仍含 %q（本次改动不该动它），实际 = %q", want, chain[1])
		}
	}
	// 且不能变成 AuthOrAccessToken（那会放宽 admin 端点）。
	if strings.Contains(chain[1], "AuthOrAccessToken") {
		t.Fatalf("admin 端点不得改用 AuthOrAccessToken（会放宽鉴权），实际 = %q", chain[1])
	}
}
