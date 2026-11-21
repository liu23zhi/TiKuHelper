package api

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// 「我的设备」七条用户端路由的注册断言（137-设备体系设计 §8.1 / 批次 A2）。
//
// 单独成文件而不是追加进 router_test.go：A3 批次（管理员端设备路由）也会改
// 同一批事实，两个成员同时编辑同一个测试文件必然冲突。本文件只覆盖 A2 的六条。
//
// ## 为什么必须有一条**真实的 SetAPIRouter** 用例，而不是只测 handler
//
// gin 的路由树是「按方法 + 按段」构建的。用户端这六条与既有路由的形态极易撞车：
//
//	DELETE /user/devices/blocks/:id  vs  DELETE /user/:id         静态 vs 参数
//	POST   /user/devices/:id/remove  vs  POST   /user/:id/balance  段数不同
//	GET    /user/devices             vs  GET    /user             （静态同级）
//
// gin 在「同层静态段与参数段共存」时不是永远兼容的（本仓在
// `/admin/devices/ban` vs `/admin/devices/:id/unbind` 上踩过 panic）。
// 一旦冲突，`SetAPIRouter` 会在**启动时** panic —— 单测直接炸，但这只有在真的
// 走一遍 SetAPIRouter 时才会发生。只测 handler 的用例对此完全无感。
func TestSetAPIRouterRegistersUserDeviceRoutes(t *testing.T) {
	ginx := ginNewEngineForDeviceRoutes()
	SetAPIRouter(ginx)

	registered := map[string]bool{}
	for _, route := range ginx.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	want := []string{
		"GET /api/user/devices",
		"POST /api/user/devices/:id/remove",
		"DELETE /api/user/devices/:id",
		"PUT /api/user/devices/:id/label",
		"POST /api/user/devices/:id/block",
		"DELETE /api/user/devices/blocks/:id",
		"GET /api/user/device-limit",
		"PUT /api/user/device-limit",
		"GET /api/user/device-expiry",
		"PUT /api/user/device-expiry",
	}
	for _, route := range want {
		if !registered[route] {
			t.Errorf("缺少路由 %s（137 §8.1 的用户端接口 + 重命名）", route)
		}
	}

	// 反方向：这些**不能**出现在 /api 下。
	//
	// ⚠️ 2026-11-15 修订：本用例原先把 `GET /api/admin/devices` 与
	// `POST /api/admin/devices/ban` 也列在 forbidden 里，注释写「A3 负责」。
	// A3 落地后这两条由 A3 的 device_admin_router_test.go **正向**断言存在，
	// 若继续留在这里会与新事实冲突（实测：本用例报「不该注册的路由出现了」）。
	// ⇒ 管理员端路由的存在性交给 A3 的用例守；本用例只守「用户端不得越界」。
	// 真正的越权判据在 A3 那条：四条管理员路由必须同时挂 Auth + RequireAdmin。
	forbidden := []string{
		"PUT /api/user/devices/:id",     // 同上：设备行不可编辑
		"POST /api/user/device-limit",   // 只允许 PUT（读改写同一入口，避免两个写口径）
		"DELETE /api/user/device-limit", // 恢复默认走 PUT + 等级值，不设删除入口
	}
	for _, route := range forbidden {
		if registered[route] {
			t.Errorf("不该注册的路由出现了：%s", route)
		}
	}
}

// ginNewEngineForDeviceRoutes 造一个 TestMode 引擎。
//
// 用 gin.New() 而不是 gin.Default()：Default 会挂 Logger/Recovery，
// 后者会把路由 panic 转成 500 —— 那样「路由冲突」会被静默吞掉，
// 本用例就变成了永远绿的空断言。这里要的是**让 panic 冒出来**。
func ginNewEngineForDeviceRoutes() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}
