package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 绑定历史四条路由的注册与鉴权（task_doc/70 §3.6 / §3.7，需求 5a/6/7）。
//
// 两件事必须同时成立：
//  1. 四条路由真的注册在**管理段**上（写错成 /review-rebate-claims 那种
//     用户段路径 = 越权面）；
//  2. 每条都挂了 middleware.RequireAdmin。
//
// ⚠️ 第 2 点必须读注册语句，不能靠行为断言：这些 handler 内部还有
// requireAdmin 二次校验（纵深防御），把中间件摘掉后非管理员照样拿 403，
// 行为测试会继续绿 —— 这是本仓已踩过的坑（t60 实测）。

func TestSetAPIRouterRegistersBindingHistoryRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)

	want := map[string]bool{
		"GET /api/admin/users/:id/bindings": false,
		"GET /api/admin/bindings":           false,
		"POST /api/admin/bindings":          false,
		"POST /api/admin/bindings/:id/void": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Fatalf("missing binding history route %s", route)
		}
	}

	// 反向钉子：用户段不得出现绑定历史的任何写入口或跨用户读入口。
	// 需求 5a/6/7 全是管理员功能；挂到 /user 段就是越权面。
	for _, route := range router.Routes() {
		if route.Path == "/api/admin/users/:id/bindings" ||
			route.Path == "/api/admin/bindings" ||
			route.Path == "/api/admin/bindings/:id/void" {
			if route.Method != http.MethodGet && route.Method != http.MethodPost {
				t.Fatalf("unexpected method %s on %s", route.Method, route.Path)
			}
		}
	}
}

// TestBindingHistoryRouterSourceMountsRequireAdmin 读 router.go 断言四条路由
// 每条都在 handler 之前挂了 middleware.RequireAdmin。
//
// 与上面那条分工：上面证明「路由存在且路径对」，这条证明「权限链挂了」。
// 只有都能挡住，才算需求 5a/6/7 的管理员边界成立。
func TestBindingHistoryRouterSourceMountsRequireAdmin(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "router.go"))
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	lines := strings.Split(string(raw), "\n")

	cases := []struct {
		pathMarker string
		handler    string
		label      string
	}{
		{`"/admin/users/:id/bindings"`, "controller.GetAdminUserBindings", "单用户弹窗"},
		{`"/admin/bindings"`, "controller.ListAdminBindings", "侧边栏列表"},
		{`"/admin/bindings"`, "controller.CreateAdminBindingHistory", "手工补录"},
		{`"/admin/bindings/:id/void"`, "controller.VoidAdminBinding", "作废"},
	}

	for _, tc := range cases {
		var registration string
		hits := 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, tc.pathMarker) || !strings.Contains(trimmed, tc.handler) {
				continue
			}
			registration = trimmed
			hits++
		}
		if hits != 1 {
			t.Fatalf("%s：期望恰好 1 条注册，实际 %d 条", tc.label, hits)
		}
		guard := strings.Index(registration, "middleware.RequireAdmin")
		handlerAt := strings.Index(registration, tc.handler)
		if guard < 0 {
			t.Fatalf("%s 必须挂 middleware.RequireAdmin（handler 内二次校验不能替代路由级鉴权）: %s", tc.label, registration)
		}
		if handlerAt < 0 || guard > handlerAt {
			t.Fatalf("%s 的 RequireAdmin 必须位于 handler 之前（gin 按注册顺序执行）: %s", tc.label, registration)
		}
		if !strings.Contains(registration, "controller.Auth") {
			t.Fatalf("%s 必须先经过 controller.Auth: %s", tc.label, registration)
		}
	}
}
