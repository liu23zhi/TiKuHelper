package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// exe 会话上报端点的路由注册断言（141 §6.2）。
//
// # 为什么单独成文件
//
// device_user_router_test.go 守「我的设备」六条（A2 批次），device_admin_router_test.go
// 守管理员四条（A3 批次）。本条属于 C2 批次，且它的**鉴权要求与那两批都不同**
// （AuthOrAccessToken != Auth），混进任何一个文件都会让那个文件的主语变模糊。
//
// # 两件事必须同时成立
//
//  1. `POST /api/user/device-sessions` 真的注册在 /api 下 —— 走一遍真实 SetAPIRouter，
//     因为 gin 按「方法 + 段」建树，本路径与既有 `/user/devices`（静态段）、
//     `/user/device-limit`（静态段）同层，一旦将来有人加 `POST /user/:id` 这类
//     参数段就会 panic（本仓在 /admin/devices 上踩过）。
//  2. 鉴权是 AuthOrAccessToken 而不是 Auth —— 这一条**只能读注册语句**：
//     engine.Routes() 不暴露中间件链，而 handler 内部的 currentOperator 对两种
//     鉴权方式都工作（身份由中间件塞进 context），纯行为断言恒绿。
//     真实后果：exe 只持 adapter key，挂 Auth 会让它恒 401 → 会话数从不上报 →
//     R5/R6/R7 的槽位语义静默失效（不报任何错）。
func TestSetAPIRouterRegistersDeviceSessionReportRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// 用 gin.New() 而不是 gin.Default()：Default 的 Recovery 会把路由 panic
	// 转成 500，那样「路由冲突」会被静默吞掉（同 device_user_router_test.go 的理由）。
	router := gin.New()
	SetAPIRouter(router)

	const want = "POST /api/user/device-sessions"
	found := false
	for _, route := range router.Routes() {
		if route.Method+" "+route.Path == want {
			found = true
			break
		}
	}
	if !found {
		var related []string
		for _, candidate := range router.Routes() {
			if strings.Contains(candidate.Path, "device-session") ||
				strings.Contains(candidate.Path, "/user/device") {
				related = append(related, candidate.Method+" "+candidate.Path)
			}
		}
		t.Fatalf("缺少路由 %s；已注册的 device 相关路由：%v", want, related)
	}

	// 反向钉子：这些形态都**不该**存在。
	//   - GET  /api/user/device-sessions：读会话数没有独立入口（配额端点读它，141 §6.1）；
	//   - PUT  /api/user/device-sessions：上报不是资源替换，PUT 会让人以为可以「设置」；
	//   - DELETE：会话数是心跳产生的，删除入口没有语义。
	//
	// 存在任一条都说明「上报」被误实现成了可增删的 CRUD 资源。
	for _, forbidden := range []string{
		"GET /api/user/device-sessions",
		"PUT /api/user/device-sessions",
		"DELETE /api/user/device-sessions",
	} {
		for _, route := range router.Routes() {
			if route.Method+" "+route.Path == forbidden {
				t.Fatalf("不该注册的路由出现了：%s（上报只允许 POST）", forbidden)
			}
		}
	}
}

// TestDeviceSessionReportRouteSourceUsesAuthOrAccessToken 读 router.go 断言鉴权中间件。
//
// 与 device_admin_router_test.go 的源码断言同一手法：中间件链不在 engine.Routes() 里，
// 而 handler 层无法区分「谁鉴定了他」，所以只能读注册语句。
func TestDeviceSessionReportRouteSourceUsesAuthOrAccessToken(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "api", "router.go"))
	if err != nil {
		// 相对路径在不同工作目录下会失败，退回到另一条已知可用的路径。
		raw, err = os.ReadFile(filepath.Join("..", "..", "internal", "api", "router.go"))
		if err != nil {
			t.Fatalf("read router.go: %v", err)
		}
	}

	var registration string
	hits := 0
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if !strings.Contains(trimmed, `"/user/device-sessions"`) ||
			!strings.Contains(trimmed, "controller.ReportDeviceSessions") {
			continue
		}
		registration = trimmed
		hits++
	}
	if hits != 1 {
		t.Fatalf("期望恰好 1 条 /user/device-sessions 注册语句，实际 %d 条", hits)
	}
	if !strings.Contains(registration, "controller.AuthOrAccessToken") {
		t.Fatalf("exe 只持 adapter key，必须挂 controller.AuthOrAccessToken（挂 Auth = 真实客户端恒 401，"+
			"会话数永不上报且无任何报错）：%s", registration)
	}
	if !strings.HasPrefix(registration, "apiRouter.POST(") {
		t.Fatalf("必须是 POST：%s", registration)
	}
}
