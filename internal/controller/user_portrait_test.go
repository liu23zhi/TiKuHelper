package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 用户画像的四层权限（task_doc/98 §0）。
//
// 每层一条独立用例，**互不替代**（要求原文：「实施时必须逐层落实，不能只在路由挂中间件」）：
//
//	① 路由层：两条画像路由必须同时挂 controller.Auth + middleware.RequireAdmin（源码级断言）
//	② handler 层：每个 handler 进入时二次校验角色 → 普通用户 token 直调 handler 得 403
//	③ service 层：查询函数只接受目标用户 ID（见 service/user_portrait_test.go 的签名审查）
//	④ 前端：非管理员看不到菜单项，直接敲 URL 要跳转（见 web 侧 portraitSource 测试）
//
// 本文件负责 ① 和 ②；③ 在 service 包；④ 在 web/src/utils/portraitSource.test.mjs。

// newPortraitControllerTestDB 建控制器测试库（画像会读到的全部表）。
func newPortraitControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.Ticket{}, &entity.LocationCache{}, &entity.IPSegment{}); err != nil {
		t.Fatalf("migrate portrait tables: %v", err)
	}
	return db
}

// installPortraitTestDB 把画像的数据库取用点换成测试库。
func installPortraitTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()

	previous := portraitDatabase
	portraitDatabase = func() *gorm.DB { return db }
	t.Cleanup(func() { portraitDatabase = previous })
}

// portraitTestRouter 组装与 router.go 完全一致的中间件顺序。
func portraitTestRouter(actor *entity.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/api/admin/portrait/overview", auth, middleware.RequireAdmin, AdminUserPortraitOverview)
	router.GET("/api/admin/users/:id/portrait", auth, middleware.RequireAdmin, AdminUserPortrait)
	return router
}

func portraitRequest(router *gin.Engine, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	router.ServeHTTP(recorder, request)
	return recorder
}

func portraitAdmin(id int32, username string) *entity.User {
	return &entity.User{ID: id, Username: username, Role: "admin", Status: "enabled"}
}

func portraitNormalUser(id int32, username string) *entity.User {
	return &entity.User{ID: id, Username: username, Role: "user", Status: "enabled"}
}

// ---------------------------------------------------------------------------
// 第 ① 层：路由源码级断言
// ---------------------------------------------------------------------------

// TestPortraitRoutesBindBothMiddlewares 钉住「路由层双中间件」。
//
// 直接读 internal/api/router.go 的原文，断言两条画像路由同时出现
// `controller.Auth` 与 `middleware.RequireAdmin`。
//
// ⚠️ 为什么必须写源码断言而不只测行为：本项目的已知坑是「handler 都实现了，
// 但路由器里一条都没挂」（33 个 admin handler 处于这个状态）。行为测试在
// 「没挂路由」时根本走不到 handler —— 它会以 404 通过「不是 200」这种弱断言，
// 于是「漏挂」与「正确拒绝」看起来一模一样。源码断言是唯一能区分两者的判据。
//
// ⚠️ 本条用例当前是**预期的红**：本任务的可写范围明确排除 `internal/api/router.go`
// （captain 独占），所以接线由 captain 完成。失败信息里直接打印需要粘贴的两行原文，
// 避免「红了但不知道加什么」。加完后本用例自动转绿，无需再改测试。
func TestPortraitRoutesBindBothMiddlewares(t *testing.T) {
	source := readRouterSource(t)

	cases := []struct {
		label string
		// pattern 匹配整条路由注册行。
		pattern string
		// wiring 是 captain 需要粘进 router.go 的原文（失败时打印）。
		wiring string
	}{
		{
			label:   "整体画像",
			pattern: `apiRouter\.GET\("/admin/portrait/overview",\s*controller\.Auth,\s*middleware\.RequireAdmin,\s*controller\.AdminUserPortraitOverview\)`,
			wiring:  `apiRouter.GET("/admin/portrait/overview", controller.Auth, middleware.RequireAdmin, controller.AdminUserPortraitOverview)`,
		},
		{
			label:   "单用户画像",
			pattern: `apiRouter\.GET\("/admin/users/:id/portrait",\s*controller\.Auth,\s*middleware\.RequireAdmin,\s*controller\.AdminUserPortrait\)`,
			wiring:  `apiRouter.GET("/admin/users/:id/portrait", controller.Auth, middleware.RequireAdmin, controller.AdminUserPortrait)`,
		},
	}
	for _, testCase := range cases {
		matched, err := regexp.MatchString(testCase.pattern, source)
		if err != nil {
			t.Fatalf("%s: 正则非法: %v", testCase.label, err)
		}
		if !matched {
			t.Fatalf("%s 的路由尚未接线。router.go 里需要加（放在 /admin/users/:id/bindings 那一组旁边）：\n\n\t%s\n", testCase.label, testCase.wiring)
		}
	}

	// 反向断言：两条画像路由**不得**出现在不需要 Auth 的裸 router 上。
	if matched, _ := regexp.MatchString(`router\.GET\("/portrait`, source); matched {
		t.Fatalf("画像路由不得挂在无鉴权的裸 router 上")
	}
}

// readRouterSource 读 internal/api/router.go 原文。
func readRouterSource(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile("../api/router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败: %v", err)
	}
	return string(content)
}

// ---------------------------------------------------------------------------
// 第 ② 层：handler 内二次校验（不依赖路由中间件）
// ---------------------------------------------------------------------------

// TestPortraitHandlersRecheckRoleInsideHandler 钉住「handler 自己也会拒普通用户」。
//
// 关键点：这里**刻意不挂 middleware.RequireAdmin**，只挂一个「什么用户都能过」的
// 假 Auth。若 handler 只靠路由中间件，本用例会拿到 200 并泄露全站画像数据 → 必红。
//
// 变异方向：把 requirePortraitAdmin 里的角色判断删掉（只留 currentOperator）→ 本用例必红。
func TestPortraitHandlersRecheckRoleInsideHandler(t *testing.T) {
	db := newPortraitControllerTestDB(t)
	installPortraitTestDB(t, db)

	if err := db.Create(&entity.User{ID: 1, Username: "target", Password: "x", Role: "user", Status: "enabled"}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	gin.SetMode(gin.TestMode)
	ordinary := portraitNormalUser(2, "ordinary")
	router := gin.New()
	// 只挂假 Auth（不挂 RequireAdmin）：把「handler 是否自己校验」单独隔离出来。
	router.Use(func(c *gin.Context) { c.Set("user", ordinary) })
	router.GET("/api/admin/portrait/overview", AdminUserPortraitOverview)
	router.GET("/api/admin/users/:id/portrait", AdminUserPortrait)

	for _, path := range []string{
		"/api/admin/portrait/overview",
		"/api/admin/users/1/portrait",
	} {
		recorder := portraitRequest(router, path)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s 由普通用户直调必须 403（handler 必须自己校验角色，不能只靠路由中间件），实际 %d，body=%s",
				path, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "仅管理员可查看用户画像") {
			t.Fatalf("%s 的 403 文案应可辨认，实际 %s", path, recorder.Body.String())
		}
	}
}

// TestPortraitHandlersRejectAnonymous 钉住「没有登录态时是 401 而不是 403」。
//
// 401/403 混用会让前端把「该重新登录」显示成「你没权限」，用户会一直重试。
func TestPortraitHandlersRejectAnonymous(t *testing.T) {
	db := newPortraitControllerTestDB(t)
	installPortraitTestDB(t, db)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/admin/portrait/overview", AdminUserPortraitOverview)

	recorder := portraitRequest(router, "/api/admin/portrait/overview")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录时必须 401，实际 %d", recorder.Code)
	}
}

// TestPortraitOverviewReturnsBothScopesFromSameShape 钉住「两个入口返回同一结构」。
//
// 用户要求的是「一个页面的两种视角」，因此两条响应除 scope/targetUserId/base 之外
// 结构必须完全一致。这里用 JSON key 集合比较：多一个/少一个字段都会被发现。
func TestPortraitOverviewReturnsBothScopesFromSameShape(t *testing.T) {
	db := newPortraitControllerTestDB(t)
	installPortraitTestDB(t, db)

	admin := portraitAdmin(9, "portrait-admin")
	if err := db.Create(&entity.User{ID: 5, Username: "subject", Password: "x", Role: "user", Status: "enabled"}).Error; err != nil {
		t.Fatalf("seed subject: %v", err)
	}
	at := time.Date(2026, 11, 13, 13, 0, 0, 0, portraitTestLocation(t))
	if err := db.Create(&entity.BillingRecord{UserID: 5, Type: "normal_search", Provider: "ai", Amount: 1.25, RequestID: "portrait-shape", CreatedAt: at}).Error; err != nil {
		t.Fatalf("seed billing: %v", err)
	}

	router := portraitTestRouter(admin)

	overviewRecorder := portraitRequest(router, "/api/admin/portrait/overview")
	if overviewRecorder.Code != http.StatusOK {
		t.Fatalf("整体画像应 200，实际 %d，body=%s", overviewRecorder.Code, overviewRecorder.Body.String())
	}
	singleRecorder := portraitRequest(router, "/api/admin/users/5/portrait")
	if singleRecorder.Code != http.StatusOK {
		t.Fatalf("单用户画像应 200，实际 %d，body=%s", singleRecorder.Code, singleRecorder.Body.String())
	}

	overview := decodePortrait(t, overviewRecorder.Body.Bytes())
	single := decodePortrait(t, singleRecorder.Body.Bytes())

	if overview.Scope != service.PortraitScopeOverall {
		t.Fatalf("整体画像 scope 应为 overall，实际 %q", overview.Scope)
	}
	if single.Scope != service.PortraitScopeUser || single.TargetUserID != 5 {
		t.Fatalf("单用户画像 scope/targetUserId 不对: %q / %d", single.Scope, single.TargetUserID)
	}
	if overview.Base != nil {
		t.Fatalf("整体画像 base 必须是 null")
	}
	if single.Base == nil || single.Base.Username != "subject" {
		t.Fatalf("单用户画像必须带目标用户 base，实际 %+v", single.Base)
	}

	overviewKeys := portraitJSONKeys(t, overviewRecorder.Body.Bytes())
	singleKeys := portraitJSONKeys(t, singleRecorder.Body.Bytes())
	if strings.Join(overviewKeys, ",") != strings.Join(singleKeys, ",") {
		t.Fatalf("两种视角的响应字段必须完全一致（一个页面的两种视角），整体 %v vs 单用户 %v", overviewKeys, singleKeys)
	}

	// 数据也要真的算出来：13 点桶、1.25 元。
	if len(single.Hours) != 1 || single.Hours[0].Bucket != 13 || single.Hours[0].Count != 1 {
		t.Fatalf("单用户 13 点应有 1 条，实际 %+v", single.Hours)
	}
	if single.Totals.Amount == nil || *single.Totals.Amount != 1.25 {
		t.Fatalf("单用户金额应为 1.25，实际 %v", single.Totals.Amount)
	}
}

// TestPortraitSingleUserReturns404ForMissingTarget 钉住「查错人」与「无行为」可区分。
func TestPortraitSingleUserReturns404ForMissingTarget(t *testing.T) {
	db := newPortraitControllerTestDB(t)
	installPortraitTestDB(t, db)
	admin := portraitAdmin(9, "portrait-admin")

	router := portraitTestRouter(admin)
	recorder := portraitRequest(router, "/api/admin/users/4242/portrait")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的目标用户必须 404（不能返回空画像），实际 %d，body=%s",
			recorder.Code, recorder.Body.String())
	}
}

// TestPortraitSingleUserRejectsInvalidID 钉住路径参数校验。
func TestPortraitSingleUserRejectsInvalidID(t *testing.T) {
	db := newPortraitControllerTestDB(t)
	installPortraitTestDB(t, db)
	admin := portraitAdmin(9, "portrait-admin")
	router := portraitTestRouter(admin)

	for _, path := range []string{
		"/api/admin/users/0/portrait",
		"/api/admin/users/abc/portrait",
		"/api/admin/users/-1/portrait",
	} {
		recorder := portraitRequest(router, path)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s 必须 400，实际 %d", path, recorder.Code)
		}
	}
}

// TestPortraitAdminCanReadOtherUserNotOnlySelf 钉住「画像查的是别人」这件事真的成立。
//
// 98 号 §0 特别提醒：普通用户本来就能看自己的基础资料，画像的价值在看**他人**。
// 本用例让管理员查另一个用户，断言拿到的是**对方**的数据 —— 若实现被误改成
// 「查当前登录用户」，这里会拿到空或管理员自己，必红。
func TestPortraitAdminCanReadOtherUserNotOnlySelf(t *testing.T) {
	db := newPortraitControllerTestDB(t)
	installPortraitTestDB(t, db)

	admin := portraitAdmin(7, "portrait-admin-self")
	if err := db.Create(&entity.User{ID: 7, Username: "portrait-admin-self", Password: "x", Role: "admin", Status: "enabled"}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if err := db.Create(&entity.User{ID: 8, Username: "other-subject", Password: "x", Role: "user", Status: "enabled", RegisterSource: "public"}).Error; err != nil {
		t.Fatalf("seed other: %v", err)
	}

	router := portraitTestRouter(admin)
	recorder := portraitRequest(router, "/api/admin/users/8/portrait")
	if recorder.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d，body=%s", recorder.Code, recorder.Body.String())
	}
	portrait := decodePortrait(t, recorder.Body.Bytes())
	if portrait.Base == nil {
		t.Fatalf("必须返回目标用户的 base")
	}
	if portrait.Base.UserID != 8 || portrait.Base.Username != "other-subject" {
		t.Fatalf("必须返回**目标用户**（id=8）的画像，而不是当前登录用户（id=7），实际 id=%d username=%q",
			portrait.Base.UserID, portrait.Base.Username)
	}
}

// decodePortrait 解析画像响应。
func decodePortrait(t *testing.T, body []byte) service.UserPortrait {
	t.Helper()

	var portrait service.UserPortrait
	if err := json.Unmarshal(body, &portrait); err != nil {
		t.Fatalf("解析画像响应失败: %v，body=%s", err, string(body))
	}
	return portrait
}

// portraitJSONKeys 返回响应体顶层字段名（已排序）。
//
// 用 map + sort 而不是手工扫描：两边「字段集合完全一致」是待测点，
// 手写扫描器自己就成了一个待测的解析器（写错了会给出假的绿色）。
func portraitJSONKeys(t *testing.T, body []byte) []string {
	t.Helper()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("解析响应失败: %v，body=%s", err, string(body))
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		t.Fatalf("响应没有任何顶层字段，body=%s", string(body))
	}
	return keys
}

// portraitTestLocation 返回画像测试专用的**显式时区**（上海）。
//
// ⚠️ 为什么不能用 time.Local（这是 CI 上抓出来的真实缺陷）：
//
// 画像的时段/星期桶按 `datetime(created_at,'+8 hours')` 换算到上海时区。
// 旧测试用 `time.Date(..., time.Local)` 起种子 —— GORM 按**进程本地时区**序列化：
//
//	开发机 CST：种出 `2026-11-13 14:00:00+08:00` → +8 后仍是 14 点 → 断言过
//	CI   UTC ：种出 `2026-11-13 14:00:00+00:00` → +8 后变成 22 点 → 断言红
//
// 也就是说这些用例的**期望值会随运行环境漂移**，本地永远绿、CI 才会露出来。
// 显式指定上海时区后，种子与断言在两种环境里都一致。
func portraitTestLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Asia/Shanghai: %v", err)
	}
	return loc
}
