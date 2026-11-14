package api

// GET /api/user/device-quota 的路由钉子（141 §6.1 / C1-3）。
//
// ## 为什么必须有源码级断言（而不只是「路由存在」）
//
// `engine.Routes()` 只返回 method + path + handler 名字，**不暴露中间件链**。
// 也就是说「路由挂的是 Auth 还是 AuthOrAccessToken」在 Routes() 里完全看不出，
// 而这两者的差别正是本端点存在的唯一理由：
//
//	Auth            → 只收 JWT / 登录 cookie ⇒ exe（只持 adapter key）恒 401
//	AuthOrAccessToken → 先试 access token，失败再落 Auth ⇒ exe 可用
//
// 回退成 Auth 的表现是「exe 首页的可用设备数一直是空的」——不报错、不告警，
// 属于最难自查的一类。所以这里直接读 router.go 的注册行并断言。
//
// 与 service_status_series_route_test.go 同一套手法（同一组理由）。

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const deviceQuotaRoutePattern = `apiRouter\.GET\("/user/device-quota",([^)\n]*)\)`

// TestDeviceQuotaRouteUsesAuthOrAccessToken 断言：路由存在、鉴权是
// AuthOrAccessToken、指向 DeviceQuota、且**不带** RequireAdmin。
func TestDeviceQuotaRouteUsesAuthOrAccessToken(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	matches := regexp.MustCompile(deviceQuotaRoutePattern).FindAllStringSubmatch(string(source), -1)
	if len(matches) != 1 {
		t.Fatalf("应当恰好有一条 GET /user/device-quota 的注册，实际匹配到 %d 条", len(matches))
	}
	chain := matches[0][1]

	if !strings.Contains(chain, "controller.DeviceQuota") {
		t.Fatalf("路由必须指向 controller.DeviceQuota，实际链 = %q", chain)
	}
	// ★ 核心断言：必须是 AuthOrAccessToken。
	if !strings.Contains(chain, "controller.AuthOrAccessToken") {
		t.Fatalf("配额端点必须挂 controller.AuthOrAccessToken —— "+
			"exe 只持 adapter key（config.json 的 common.settings.tiku-adapter.key），"+
			"挂 controller.Auth 会让它恒 401，表现只是「exe 首页可用设备数一直是空的」。实际链 = %q", chain)
	}
	// 反向断言①：不能同时挂 Auth（那会让 Auth 先跑并 401）。
	if strings.Contains(chain, "controller.Auth,") || strings.Contains(chain, "controller.Auth)") {
		t.Fatalf("配额端点不得再挂 controller.Auth（会先拦掉 adapter key），实际链 = %q", chain)
	}
	// 反向断言②：不得带 RequireAdmin（exe 用的是普通用户的 key）。
	if strings.Contains(chain, "middleware.RequireAdmin") {
		t.Fatalf("配额端点不得挂 RequireAdmin —— 它是给普通用户/exe 用的。实际链 = %q", chain)
	}
}

// TestDeviceQuotaRouteRejectsPutPostDelete 反向断言：配额端点**只**有 GET。
//
// 它必须只读（不登记、不改 session_count、不刷 last_seen_at）：
// 一旦出现 POST/PUT，就意味着「读配额」被顺手做成了「登记槽位」，
// 而 GET 上的写副作用会让 exe 每次轮询首页都占一个槽位。
func TestDeviceQuotaRouteRejectsWriteMethods(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	text := string(source)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		pattern := `apiRouter\.` + method + `\("/user/device-quota[^)]*\)`
		if regexp.MustCompile(pattern).MatchString(text) {
			t.Fatalf("配额端点不得注册 %s：它必须只读（GET）。"+
				"写成写入口会让 exe 每次轮询首页都产生副作用", method)
		}
	}
}

// TestSetAPIRouterRegistersDeviceQuotaRoute 真实走一遍 SetAPIRouter。
//
// 作用与源码级断言不同：它证明这条路由真的能被 gin 注册（不与既有
// `/user/*` 路由冲突而 panic）。gin 在「同层静态段与参数段共存」时不是永远
// 兼容的，而冲突只在真的注册时才会暴露 —— 只读源码对此完全无感。
func TestSetAPIRouterRegistersDeviceQuotaRoute(t *testing.T) {
	ginx := ginNewEngineForDeviceRoutes()
	SetAPIRouter(ginx)

	registered := map[string]bool{}
	for _, route := range ginx.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	if !registered["GET /api/user/device-quota"] {
		t.Fatalf("缺少路由 GET /api/user/device-quota（141 §6.1）。已注册的 /user 路由：%v",
			userRoutesForDebug(ginx))
	}
	// 既有六条不得被改动（红线 4）。
	for _, route := range []string{
		"GET /api/user/devices",
		"POST /api/user/devices/:id/remove",
		"POST /api/user/devices/:id/block",
		"DELETE /api/user/devices/blocks/:id",
		"GET /api/user/device-limit",
		"PUT /api/user/device-limit",
	} {
		if !registered[route] {
			t.Errorf("既有路由 %s 必须保留（本批次只追加配额端点，不改既有 6 条）", route)
		}
	}
}

// userRoutesForDebug 列出 /user 前缀的路由，便于失败时看全貌。
func userRoutesForDebug(ginx *gin.Engine) []string {
	paths := []string{}
	for _, route := range ginx.Routes() {
		if strings.HasPrefix(route.Path, "/api/user") {
			paths = append(paths, route.Method+" "+route.Path)
		}
	}
	return paths
}

// ⚠️ 这里**刻意没有**「匿名请求必须 401」的行为级用例。
//
// 理由与 storage_status_router_test.go / service_status_series_route_test.go 相同：
// 对真实 SetAPIRouter 引擎发起请求会先走 GlobalAPIRateLimit，而它在单测里
// `manager` 为 nil —— 实测直接 `panic: invalid memory address`（rate_limit.go:52），
// 栈里挂在 ServeHTTP 上，与「鉴权对不对」毫无关系。
//
// 鉴权是否生效由上面两条源码级断言守（AuthOrAccessToken 在链里、不带 Auth/RequireAdmin），
// 而那正是「exe 能不能调」的**唯一**决定性事实 —— handler 内部的 currentOperator
// 对匿名请求也会 401，所以一个行为级的 401 用例在本议题上是恒真的（无区分力）。
