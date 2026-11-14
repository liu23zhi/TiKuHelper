package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 存储状态路由的注册与中间件链钉子（task_doc/108:297、109:474/486）。
//
// # 为什么需要源码级断言
//
// gin v1.12 的 engine.Routes() 返回的 RouteInfo **不含中间件链**，纯黑盒无法区分
// 「挂了 Auth」与「挂了 Auth + RequireAdmin」。而完全走 SetAPIRouter 发真实请求
// 也不可行：全局链里的 GlobalAPIRateLimit 直接解引用 manager.GetManager()，
// 单测无 manager 会 nil panic（本仓 ip_segment_middleware_test.go:34-35 已记录）。
//
// 所以这里用源码级断言，精确命中两个真实缺陷形态：
//  1. 有人「顺手补一个管理员校验」→ 普通用户看不到警告（功能失效）；
//  2. 有人把它挪出 apiRouter → 失去 Auth，未登录也能探测存储类型（指纹泄露）。

const storageStatusRoutePattern = `apiRouter\.GET\("/(?:system/)?storage-status"`

// TestStorageStatusRouteIsRegisteredWithoutRequireAdmin 断言：路由存在、带 Auth、
// **不带** RequireAdmin。
//
// 关键的一条是「不带」—— 与之相邻的 system-metrics / system-settings 都是
// Auth + RequireAdmin，很容易被顺手改成一致。但 108:297 的警告对象是所有人，
// 挂 RequireAdmin 会让普通用户永远看不到那条悬浮警告。
func TestStorageStatusRouteIsRegisteredWithoutRequireAdmin(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}

	pattern := regexp.MustCompile(`apiRouter\.GET\("/system/storage-status",([^)\n]*)\)`)
	matches := pattern.FindAllStringSubmatch(string(source), -1)
	if len(matches) != 1 {
		t.Fatalf("应当恰好有一条 GET /system/storage-status 的注册，实际匹配到 %d 条", len(matches))
	}
	chain := matches[0][1]

	if !strings.Contains(chain, "controller.Auth") {
		t.Fatalf("存储状态路由必须挂 controller.Auth（未登录不该探测存储类型），实际链 = %q", chain)
	}
	if !strings.Contains(chain, "controller.StorageStatus") {
		t.Fatalf("路由必须指向 controller.StorageStatus，实际链 = %q", chain)
	}
	// ← 本条是本文件的核心断言。
	if strings.Contains(chain, "middleware.RequireAdmin") {
		t.Fatalf("存储状态路由**不得**挂 RequireAdmin —— 挂上后普通用户永远看不到 SQLite 警告，"+
			"需求只兑现给管理员。实际链 = %q", chain)
	}
}

// TestStorageStatusRouteIsInsideAPIRouter 反向钉子：确认它没被挪到公开段。
//
// 「公开段」在本文件里的形态是直接挂在 router 上（而非 apiRouter），
// 例如 /sqp/*。若被挪过去，上面那条测试仍可能通过（它只查行内不含 RequireAdmin），
// 但实际已失去 Auth。
func TestStorageStatusRouteIsInsideAPIRouter(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	bareRoute := regexp.MustCompile(`\brouter\.(?:GET|Any|POST)\("/[^"]*storage-status"`)
	if bareRoute.MatchString(string(source)) {
		t.Fatal("存储状态路由不得直接挂在全局 router 上（会绕过 Auth 与黑名单豁免策略）")
	}
	if !regexp.MustCompile(storageStatusRoutePattern).MatchString(string(source)) {
		t.Fatal("找不到 apiRouter 上的存储状态路由")
	}
}
