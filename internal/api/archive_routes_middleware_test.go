package api

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// 已归档账号 / 注销申请路由的**中间件挂载**回归。
//
// # 为什么需要这个文件
//
// controller 层的 archived_user_test.go / closure_test.go 是行为级测试，
// 但它们要么直接调 handler、要么在测试里**自行重建**路由，
// 与 router.go 的真实注册是两套独立组装。
//
// 实测确认（2026-11-11）：把 router.go 里三条 /admin/archived-users 的
// middleware.RequireAdmin 全部删掉后，controller 包 `TestArchived|TestClosure`
// 依然 `ok`，无一条变红 —— 即「管理员专属接口漏挂 RequireAdmin」这个真实
// 缺陷形态，现有测试一条都抓不到。
//
// # 为什么是源码级断言
//
// gin v1.12 的 engine.Routes() 返回的 RouteInfo 只有 Method/Path/Handler/HandlerFunc，
// 不暴露每路由的中间件链；而完全走 SetAPIRouter 发真实请求不可行
// （全局链的 GlobalAPIRateLimit 需进程级初始化，单测调用会 nil panic，已实测）。
//
// 因此这里退一步读 router.go 源码做正则断言。它是弱验证，但精确命中
// 「有人摘掉中间件」这一形态，填补现有 9+ 条行为测试的空白。
//
// 配合关系：
//   - 本文件 → 防 router.go 漏挂（源码级）
//   - controller 层 → 防 handler 不校验（行为级）

// routerSourcePath 与 readRouterSource 定义在 ip_segment_middleware_test.go（同包共享）：
// 两处各定义一个同名 const 会编译失败（redeclared in this block）。

// TestArchivedUserRoutesRequireAdmin 断言三条管理员接口都带 Auth + RequireAdmin。
func TestArchivedUserRoutesRequireAdmin(t *testing.T) {
	text := readRouterSource(t)

	routes := []struct {
		method  string
		path    string
		handler string
	}{
		{http.MethodGet, "/admin/archived-users", "ArchivedUserList"},
		{http.MethodGet, "/admin/archived-users/:id", "ArchivedUserDetail"},
		{http.MethodPost, "/admin/archived-users/:id/restore", "ArchivedUserRestore"},
	}

	for _, route := range routes {
		t.Run(route.handler, func(t *testing.T) {
			chain := findRouteMiddlewareChain(t, text, route.method, route.path, route.handler)
			if !strings.Contains(chain, "controller.Auth") {
				t.Fatalf("%s %s 缺少 controller.Auth，实际链 = %q", route.method, route.path, chain)
			}
			if !strings.Contains(chain, "middleware.RequireAdmin") {
				t.Fatalf("%s %s 缺少 middleware.RequireAdmin —— "+
					"普通用户将能读取/恢复他人已归档账号，实际链 = %q",
					route.method, route.path, chain)
			}
		})
	}
}

// TestClosureRoutesAreUserScopedNotAdmin 断言注销申请三条**只**带 Auth、不带 RequireAdmin。
//
// 反向钉子：这三条是「用户本人」接口（目标账号取当前登录用户）。
// 若有人给它们加 RequireAdmin，普通用户就再也申请不了注销 —— 功能直接失效。
// 与上一条相反方向，同样重要。
func TestClosureRoutesAreUserScopedNotAdmin(t *testing.T) {
	text := readRouterSource(t)

	routes := []struct {
		method  string
		path    string
		handler string
	}{
		{http.MethodPost, "/user/closure-request", "ClosureRequest"},
		{http.MethodPost, "/user/closure-cancel", "ClosureCancel"},
		{http.MethodGet, "/user/closure-status", "ClosureStatus"},
	}

	for _, route := range routes {
		t.Run(route.handler, func(t *testing.T) {
			chain := findRouteMiddlewareChain(t, text, route.method, route.path, route.handler)
			if !strings.Contains(chain, "controller.Auth") {
				t.Fatalf("%s %s 缺少 controller.Auth（必须是登录用户接口），实际链 = %q",
					route.method, route.path, chain)
			}
			if strings.Contains(chain, "middleware.RequireAdmin") {
				t.Fatalf("%s %s 不得带 RequireAdmin —— 这是用户本人接口，"+
					"加了普通用户就无法申请注销，实际链 = %q", route.method, route.path, chain)
			}
		})
	}
}

// TestArchivedRoutesAreNotPublic 反向钉子：确认这些路径不直接挂在公开 router 上。
func TestArchivedRoutesAreNotPublic(t *testing.T) {
	text := readRouterSource(t)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "archived-users") && !strings.Contains(trimmed, "closure-") {
			continue
		}
		if strings.HasPrefix(trimmed, "router.") {
			t.Fatalf("管理/注销路由不得直接挂在公开 router 上（会绕过 Auth）：%s", trimmed)
		}
	}
}

func readRouterSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(routerSourcePath)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", routerSourcePath, err)
	}
	return string(source)
}

// findRouteMiddlewareChain 返回某条路由注册行里「路径之后、handler 之前」的中间件链片段。
func findRouteMiddlewareChain(t *testing.T, text, method, path, handler string) string {
	t.Helper()
	pattern := regexp.MustCompile(
		`apiRouter\.` + method + `\("` + regexp.QuoteMeta(path) + `",([^)\n]*controller\.` + handler + `[^)\n]*)\)`)
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		t.Fatalf("应当恰好有一条 `%s %s → controller.%s` 的注册，实际匹配到 %d 条",
			method, path, handler, len(matches))
	}
	return matches[0][1]
}
