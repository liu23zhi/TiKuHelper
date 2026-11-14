package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 设备管理四条路由的注册与鉴权（137 §8.2 / §8.3）。
//
// # 三件事必须同时成立
//
//  1. 四条路由真的注册在 `/api/admin/*` 上，且**封禁/解封用独立静态前缀**
//     `/admin/device-bans`（137 §8.3 / 任务书红线 5）。路径写错成
//     `/admin/devices/ban` 时，与 `POST /admin/devices/:id/unbind` 的静态/参数同层
//     组合在旧版 gin 上会 panic；新版虽允许，但语义仍不清楚（ban 的对象是
//     device_id，unbind 的对象是绑定行 id，不是同一类资源）。
//  2. 每条都挂了 `controller.Auth` + `middleware.RequireAdmin`。
//     这一条**必须读注册语句**：handler 内部还有 requireAdmin 二次校验（纵深防御），
//     把中间件摘掉后非管理员照样 403，纯行为断言会继续绿 —— 本仓已踩过这个坑
//     （t60 / binding_history_router_test.go 的注释里记着）。
//  3. 这一组**不挂 middleware.WhitelistIP()/BlacklistIP()**：理由与黑名单/封禁组
//     逐字相同（被误封 IP 的管理员必须能打开设备页自救）。这一条也读源码，
//     因为中间件链不会出现在 engine.Routes() 里。
func TestSetAPIRouterRegistersDeviceAdminRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)

	want := map[string]bool{
		"GET /api/admin/devices":                  false,
		"POST /api/admin/devices/:id/unbind":      false,
		"POST /api/admin/device-bans":             false,
		"DELETE /api/admin/device-bans/:deviceId": false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			var registered []string
			for _, candidate := range router.Routes() {
				if strings.Contains(candidate.Path, "device") {
					registered = append(registered, candidate.Method+" "+candidate.Path)
				}
			}
			t.Fatalf("missing device admin route %s；已注册的 device 路由：%v", route, registered)
		}
	}

	// 反向钉子：封禁不得挂在 `/admin/devices/ban`（137 §8.3 的写法）。
	for _, route := range router.Routes() {
		if route.Path == "/api/admin/devices/ban" || route.Path == "/api/admin/devices/unban" {
			t.Fatalf("%s %s 不得存在：封禁/解封必须用 /admin/device-bans 独立前缀",
				route.Method, route.Path)
		}
	}
}

// TestDeviceAdminRouterSourceMountsAuthAndRequireAdmin 读 router.go 断言四条注册语句。
func TestDeviceAdminRouterSourceMountsAuthAndRequireAdmin(t *testing.T) {
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
		{`"/admin/devices"`, "controller.AdminDeviceList", "设备列表"},
		{`"/admin/devices/:id/unbind"`, "controller.AdminDeviceUnbind", "强制解绑"},
		{`"/admin/device-bans"`, "controller.AdminDeviceBan", "封禁设备"},
		{`"/admin/device-bans/:deviceId"`, "controller.AdminDeviceUnban", "解封设备"},
	}

	for _, tc := range cases {
		var registration string
		hits := 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if !strings.Contains(trimmed, tc.pathMarker) || !strings.Contains(trimmed, tc.handler) {
				continue
			}
			registration = trimmed
			hits++
		}
		if hits != 1 {
			t.Fatalf("%s：期望恰好 1 条注册语句，实际 %d 条（查找 marker=%s handler=%s）",
				tc.label, hits, tc.pathMarker, tc.handler)
		}
		if !strings.Contains(registration, "controller.Auth") {
			t.Fatalf("%s 必须先经过 controller.Auth: %s", tc.label, registration)
		}
		guard := strings.Index(registration, "middleware.RequireAdmin")
		handlerAt := strings.Index(registration, tc.handler)
		if guard < 0 {
			t.Fatalf("%s 必须挂 middleware.RequireAdmin（handler 内二次校验不能替代路由级鉴权）: %s",
				tc.label, registration)
		}
		if handlerAt < 0 || guard > handlerAt {
			t.Fatalf("%s 的 RequireAdmin 必须位于 handler 之前（gin 按注册顺序执行）: %s", tc.label, registration)
		}
		// 这一组必须挂在不带 IP 判定的管理员组上（防管理员自锁）。
		if !strings.HasPrefix(registration, "blacklistAdminRouter.") {
			t.Fatalf("%s 必须挂在 blacklistAdminRouter（不挂 WhitelistIP/BlacklistIP 的管理组）上，"+
				"否则被误封 IP 的管理员连设备页都打不开、无法自救: %s", tc.label, registration)
		}
	}
}
