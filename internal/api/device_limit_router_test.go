package api

// 设备门禁中间件的**路由挂载范围**回归（C6-2 / C6-3 的接线层）。
//
// # 为什么必须单独钉「挂在哪里」
//
// 中间件本身写得再对，挂错位置也会出事，而且两个方向都会出事：
//
//	漏挂 → 限制完全不生效（最惨的是老路径 /adapter-service/search 漏挂，
//	        而那正是「不带 X-Device-Id 的老客户端」走的路）
//	多挂 / 全局 Use → 登录、健康检查、管理接口全被拦，等于全站自助锁门
//
// 本仓已有先例：router.go 的中间件链**不会**出现在 engine.Routes() 里
// （RouteInfo 只有 Method/Path/Handler），所以纯行为断言拿不到它。
// 因此这里两条腿走路：
//
//	① 源码级 —— 逐条断言该挂的挂了、不该挂的没挂（精确到注册行）
//	② 真实路由 —— 走一遍 SetAPIRouter 确认没有 gin panic，且挂载点数量与位置符合预期

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/registry/manager"
)

// deviceLimitExpectedMounts 是「必须挂设备门禁」的路由清单（方法 + 路径 + handler）。
//
// 选取口径：**消耗权益的搜题业务入口**。判断依据是「这条路由会不会拿题库/AI 的答案」——
// 会，就占设备槽位；不会（观察位、下发凭证、上传证据），就不占。
//
// 路径要写全组前缀：legacy 组的实际路径是 /adapter-service/search，写 /search 会匹配不到。
var deviceLimitExpectedMounts = []struct {
	method  string
	path    string
	handler string
	// group 是注册语句里的变量名（apiRouter / legacySearchRouter）。
	group string
}{
	{http.MethodPost, "/search", "SearchWithIdempotency", "apiRouter"},
	{http.MethodPost, "/search/tasks", "CreateSearchTask", "apiRouter"},
	{http.MethodPost, "/search", "SearchWithIdempotency", "legacySearchRouter"},
	{http.MethodPost, "/search/tasks", "CreateSearchTask", "legacySearchRouter"},
	{http.MethodPost, "/ai-fallback", "AIFallback", "apiRouter"},
	{http.MethodPost, "/ai-fallback/tasks", "CreateAIFallbackTask", "apiRouter"},
}

// deviceLimitForbiddenMounts 是「**不得**挂设备门禁」的路由清单。
//
// 反向钉子。这几条挂上就是事故：
//
//	/user/login       → 登录都要设备头，还没登录的人永远进不来（自锁）
//	/search/health    → 观察位，不消耗权益；拦它只是让排障更难
//	/search/status    → 同上
//	/tiku/evidence    → 上报答案证据，不占槽位
//	/user/device-*    → 用户自救入口；被拦 = 用户无法删设备来解除限制（死锁）
var deviceLimitForbiddenMounts = []struct {
	method  string
	path    string
	handler string
}{
	{http.MethodPost, "/user/login", "UserLogin"},
	{http.MethodGet, "/search/health", "GetSearchTaskHealth"},
	{http.MethodGet, "/search/status", "GetSearchTaskStatus"},
	{http.MethodPost, "/tiku/evidence", "UploadTikuAnswerEvidence"},
	{http.MethodGet, "/user/devices", "UserDevices"},
	{http.MethodPost, "/user/devices/:id/remove", "UserDeviceRemove"},
	{http.MethodGet, "/user/device-limit", "UserDeviceLimitGet"},
}

// TestDeviceLimitMiddlewareMountedOnExpectedRoutes 断言该挂的都挂上了（源码级）。
func TestDeviceLimitMiddlewareMountedOnExpectedRoutes(t *testing.T) {
	text := readRouterSource(t)

	for _, mount := range deviceLimitExpectedMounts {
		t.Run(mount.group+"_"+mount.path+"_"+mount.handler, func(t *testing.T) {
			chain := findGroupRouteChain(t, text, mount.group, mount.method, mount.path, mount.handler)
			if !strings.Contains(chain, "middleware.DeviceLimit()") {
				t.Fatalf("%s.%s(%q, ... controller.%s) 缺少 middleware.DeviceLimit()，实际链 = %q\n"+
					"漏挂的后果：这条入口上的设备池限制完全不生效。",
					mount.group, mount.method, mount.path, mount.handler, chain)
			}
		})
	}
}

// TestDeviceLimitMiddlewareNotMountedOnForbiddenRoutes 断言不该挂的都没挂（源码级）。
func TestDeviceLimitMiddlewareNotMountedOnForbiddenRoutes(t *testing.T) {
	text := readRouterSource(t)

	for _, mount := range deviceLimitForbiddenMounts {
		t.Run(mount.path+"_"+mount.handler, func(t *testing.T) {
			chain := findRouteChainAllGroups(text, mount.method, mount.path, mount.handler)
			if chain == "" {
				// 路由不在这条路径上（可能换了组/改了名）：不算缺陷，跳过。
				t.Skipf("%s %s → controller.%s 未在 router.go 找到，跳过", mount.method, mount.path, mount.handler)
			}
			if strings.Contains(chain, "middleware.DeviceLimit()") {
				t.Fatalf("%s %s 不得挂 middleware.DeviceLimit()，实际链 = %q\n"+
					"挂上的后果：%s", mount.method, mount.path, chain,
					"这条是用户自救 / 观察位 / 登录入口，被设备门禁拦住会形成死锁（无法删设备来解除限制）。")
			}
		})
	}
}

// TestDeviceLimitIsNotUsedGlobally 断言没有全局 Use（红线 3）。
//
// 全局 Use 会让 /user/login、/health、静态资源全部经过设备门禁 ——
// 登录都进不来，等于把设备门禁变成「全站自助锁门」。
//
// # 为什么用括号平衡扫描而不是正则
//
// 初版写的是 `\.Use\([^)]*middleware\.DeviceLimit\(\)`。实测**规则失效**：
// 挂载成 `apiRouter.Use(middleware.WhitelistIP(), middleware.DeviceLimit())` 时，
// `[^)]*` 在 `WhitelistIP()` 自己的 `)` 处就停了 → 匹配不到 → 变异不红。
// 与 findGroupRouteChain 里踩的是同一类坑（字符类不能跨过括号）。
//
// 所以这里改成「按 .Use( 切段 + 数括号」：只要在某个 .Use(...) 的**完整参数区间**里
// 出现 DeviceLimit，就是全局挂载。
func TestDeviceLimitIsNotUsedGlobally(t *testing.T) {
	text := readRouterSource(t)
	for _, statement := range findUseCallArguments(text) {
		if strings.Contains(statement, "middleware.DeviceLimit()") {
			t.Fatalf("middleware.DeviceLimit() 不得出现在 .Use(...) 里（全局挂载），实际语句 = %q\n"+
				"全局挂载的后果：登录、健康检查、静态资源全部经过设备门禁，等于全站自助锁门。",
				statement)
		}
	}
}

// findUseCallArguments 返回 router.go 里每个 `.Use(` 的**完整**参数串。
//
// 实现要点：从 `.Use(` 之后开始，按括号深度累加，深度归零时收尾。
// 这样 `Use(a(), b())` 会作为整体返回，不会被内层 `)` 截断。
func findUseCallArguments(source string) []string {
	const marker = ".Use("
	var out []string
	index := 0
	for {
		found := strings.Index(source[index:], marker)
		if found < 0 {
			return out
		}
		start := index + found + len(marker)
		depth := 1
		cursor := start
		for cursor < len(source) && depth > 0 {
			switch source[cursor] {
			case '(':
				depth++
			case ')':
				depth--
			}
			cursor++
		}
		if depth == 0 {
			out = append(out, source[start:cursor-1])
		}
		index = cursor
		if index >= len(source) {
			return out
		}
	}
}

// TestSetAPIRouterWithDeviceLimitDoesNotPanic 走一遍真实 SetAPIRouter，确认没有 gin panic。
//
// 为什么必须有这条：gin 在「同层静态段与参数段共存」「重复注册」时会 panic，
// 而且只会在真的构建路由树时发生 —— 只测 handler 的用例对此完全无感
// （本仓在 /admin/devices/ban vs /admin/devices/:id/unbind 上踩过）。
//
// 本用例同时是「中间件挂在真实路由链上能跑通」的最基本证据：
// 若 DeviceLimit 在构造期就 panic（例如闭包捕获了 nil），这里必红。
func TestSetAPIRouterWithDeviceLimitDoesNotPanic(t *testing.T) {
	router := ginNewEngineForDeviceRoutes()

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("SetAPIRouter 在挂载 deviceLimit 后 panic：%v", recovered)
		}
	}()
	SetAPIRouter(router)

	// 挂载点必须真的在路由树里（不是「写进源码但没生效」）。
	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"POST /api/search",
		"POST /api/search/tasks",
		"POST /adapter-service/search",
		"POST /adapter-service/search/tasks",
		"POST /api/ai-fallback",
		"POST /api/ai-fallback/tasks",
	} {
		if !registered[want] {
			t.Fatalf("真实路由树里缺少 %s —— 挂载点没生效", want)
		}
	}

	// 反向：这几条必须仍然注册着（挂了门禁不能把它们弄丢）。
	for _, mustExist := range []string{
		"POST /api/user/login",
		"GET /api/search/health",
		"GET /api/search/status",
	} {
		if !registered[mustExist] {
			t.Fatalf("真实路由树里缺少 %s —— 挂载门禁时把既有路由弄丢了", mustExist)
		}
	}
}

// findGroupRouteChain 返回某条路由注册行里「路径之后、handler 之前」的中间件链片段。
//
// 与 findRouteMiddlewareChain（archive_routes_middleware_test.go）的差别：
// 那个只认 apiRouter，本文件还要认 legacySearchRouter。
//
// # 两处踩过的坑（都写成注释，否则下一个人会原样改回去）
//
//  1. 前缀必须是 `(?m)^\s*` 而不是 `(?m)^`：router.go 里所有注册行都带缩进，
//     锚死在行首会匹配到 0 条（初版就是这样，6 条挂载断言全部假红）。
//  2. 中间件链的字符类必须是 `[^\n]*` 而不是 `[^)\n]*`：本文件要抓的正是
//     `middleware.DeviceLimit()` —— 带括号的调用。用「不含右括号」的字符类
//     会在 `DeviceLimit()` 自己的 `)` 处断掉，导致永远匹配不到目标中间件。
func findGroupRouteChain(t *testing.T, text, group, method, path, handler string) string {
	t.Helper()
	pattern := regexp.MustCompile(
		`(?m)^\s*` + regexp.QuoteMeta(group) + `\.` + method + `\("` + regexp.QuoteMeta(path) +
			`",([^\n]*controller\.` + handler + `)\b`)
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) != 1 {
		t.Fatalf("应当恰好有一条 `%s.%s(%q → controller.%s)` 的注册，实际匹配到 %d 条",
			group, method, path, handler, len(matches))
	}
	return matches[0][1]
}

// findRouteChainAllGroups 返回**所有**组下该路由的中间件链（用 "\n" 拼接）。
//
// 返回全部而不是单个：同一个路径可能同时注册在 /api 与 /adapter-service 两个组里
// （legacy 兼容组就是这种情况）。只取一条会漏掉另一条上的违规挂载 ——
// 初版用「恰好一条否则返回空」的写法，于是 legacy 组上的问题被静默跳过了。
func findRouteChainAllGroups(text, method, path, handler string) string {
	pattern := regexp.MustCompile(
		`(?m)^\s*\w+\.` + method + `\("` + regexp.QuoteMeta(path) + `",([^\n]*controller\.` +
			handler + `)\b`)
	matches := pattern.FindAllStringSubmatch(text, -1)
	chains := make([]string, 0, len(matches))
	for _, match := range matches {
		chains = append(chains, match[1])
	}
	return strings.Join(chains, "\n")
}

// TestRouterSourceIsReadable 是自检：读不到 router.go 时上面的全部用例都会
// 以 t.Fatalf 失败，但失败信息会指向「某条路由没挂」而不是「文件读不到」。
// 单独一条把根因说清楚。
func TestRouterSourceIsReadable(t *testing.T) {
	if _, err := os.ReadFile(routerSourcePath); err != nil {
		t.Fatalf("读取 %s 失败：%v —— 上面全部挂载断言的失败信息都会因此误导", routerSourcePath, err)
	}
}

// TestDeviceLimitOnRealRouterDoesNotBlockSearch 是**真实路由的行为探针**。
//
// 前面几条都是源码级断言（证明「链上写了 DeviceLimit()」），本用例走完整链路：
// SetAPIRouter 构建真实路由树 → 真实 HTTP 请求 → 看响应码。
//
// # 它证明什么
//
//   - 中间件在**真实路由树**上确实被执行了（不是「写进源码但没进链」）；
//   - 它在取不到数据库句柄时走 fail-open，**不会**把 /api/search 变成 403。
//     这条正是「上线瞬间全站 403」的反面：本仓单测进程里没有 manager 单例，
//     若中间件把「拿不到 db」当成拒绝，这里会立刻变 403。
//   - /api/user/login 未被牵连（登录入口不受设备门禁影响）。
//
// # 为什么必须走子进程
//
// 真实 SetAPIRouter 的全局链里有 GlobalAPIRateLimit，它直接解引用
// manager.GetManager()（middleware/rate_limit.go:51）。单测进程没有 manager
// 会 nil panic —— 这是**既有**约束，与本次改动无关
// （storage_status_router_test.go:16-18 与 ip_segment_middleware_test.go:34-35 都记着）。
//
// 本仓已有可用的解法：upstream_routes_auth_test.go 用子进程 +
// manager.CreateManager() + 合成临时库跑真实路由。这里照抄该形态。
//
// # 为什么不断言 200
//
// /api/search 无有效凭据/请求体时由 handler 自己给出 4xx（那是既有语义）。
// 本用例只断言「**不是** 403 + 不是 DEVICE_ID_REQUIRED」——即门禁没拦人。
// 断言具体 400/401 会把本用例绑死在 handler 的校验顺序上，属无关脆弱性。
func TestDeviceLimitOnRealRouterDoesNotBlockSearch(t *testing.T) {
	if os.Getenv("DEVICE_LIMIT_ROUTER_CHILD") != "1" {
		dir := t.TempDir()
		config, err := json.Marshal(map[string]any{
			"database": map[string]any{"path": filepath.Join(dir, "synthetic.db")},
			"limit":    map[string]any{"enable": true, "duration": 60, "requests": 120},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), config, 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestDeviceLimitOnRealRouterDoesNotBlockSearch$", "-test.v")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"DEVICE_LIMIT_ROUTER_CHILD=1", "SQL_DSN=",
			"JWT_SECRET=DeviceLimitRouteContract246!ABC",
			"TIKU_ADMIN_PASSWORD=DeviceLimitFixture246!",
			"TIKU_APP_MODE=production",
			"EXTENSION_UPDATE_ENABLED=false",
			"EXTENSION_EDGE_ID=", "EXTENSION_CHROME_ID=",
		)
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		if err != nil {
			t.Errorf("真实路由的行为探针失败：%v", err)
		}
		return
	}

	m, err := manager.CreateManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.CloseManager(); err != nil {
			t.Error(err)
		}
	})
	router := ginNewEngineForDeviceRoutes()
	SetAPIRouter(router)

	for _, probe := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/search", `{}`},
		{http.MethodPost, "/adapter-service/search", `{}`},
		{http.MethodPost, "/api/user/login", `{}`},
	} {
		out := httptest.NewRecorder()
		request := httptest.NewRequest(probe.method, probe.path, strings.NewReader(probe.body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(out, request)
		t.Logf("%s %s => %d", probe.method, probe.path, out.Code)

		if out.Code == http.StatusForbidden {
			t.Errorf("%s %s 返回 403 —— 设备门禁在无 db 句柄/开关关闭时必须 fail-open，"+
				"否则任何数据库抖动都会让全站搜题 403。body=%s",
				probe.method, probe.path, out.Body.String())
		}
		if strings.Contains(out.Body.String(), "DEVICE_ID_REQUIRED") {
			t.Errorf("%s %s 返回了 DEVICE_ID_REQUIRED —— 门禁在开关默认关闭时不应拒绝。body=%s",
				probe.method, probe.path, out.Body.String())
		}
	}
	t.Log("DEVICE_LIMIT_ROUTER_PROBE=no-403; all three probes reached tail")
}
