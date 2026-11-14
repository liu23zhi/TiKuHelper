package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 白名单管理页控制器测试（T-04）。
//
// 与黑名单那组同口径：全部用真 sqlite 内存库，不 mock service。
// 「新增后缓存立即生效」「停用后不再放行」只有在真实 GORM 写库 + 真实缓存重载
// 链路上才有意义 —— 换成 mock 就退化成「我们调用了一个函数」。

func newWhitelistAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.WhitelistEntry{}); err != nil {
		t.Fatalf("migrate whitelist table: %v", err)
	}
	return db
}

// installWhitelistAdminTestDB 把控制器的数据库取用点与缓存刷新钩子换成测试库。
func installWhitelistAdminTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousDB := whitelistAdminDB
	previousReload := reloadWhitelistCache
	whitelistAdminDB = func() *gorm.DB { return db }
	reloadWhitelistCache = func(target *gorm.DB) error {
		if target == nil {
			return service.ErrWhitelistCacheNoDatabase
		}
		return service.ReloadDefaultWhitelistCache(target)
	}
	t.Cleanup(func() {
		whitelistAdminDB = previousDB
		reloadWhitelistCache = previousReload
	})
}

// whitelistAdminRouter 组装与 router.go 完全一致的中间件顺序。
//
// 与黑名单那组同理：**刻意不挂 middleware.WhitelistIP()** —— 五条管理接口
// 生产上挂在不带 IP 判定的独立 group 上（防管理员自锁）。
func whitelistAdminRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/api/whitelist", auth, middleware.RequireAdmin, WhitelistEntryList)
	router.POST("/api/whitelist", auth, middleware.RequireAdmin, WhitelistEntryCreate)
	router.PUT("/api/whitelist/:id/enabled", auth, middleware.RequireAdmin, WhitelistEntryToggle)
	router.DELETE("/api/whitelist/:id", auth, middleware.RequireAdmin, WhitelistEntryDelete)
	router.GET("/api/whitelist/exempt-ips", auth, middleware.RequireAdmin, WhitelistExemptIPsGet)
	router.PUT("/api/whitelist/exempt-ips", auth, middleware.RequireAdmin, WhitelistExemptIPsPut)
	return router
}

func whitelistRequest(router *gin.Engine, method string, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

// itoa 是 strconv.FormatInt 的薄封装（本文件多处拼路径，省一句 import 噪音）。
func itoa(value int64) string { return strconv.FormatInt(value, 10) }

// contains 是 strings.Contains 的薄封装（断言报文时用）。
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func whitelistSeedAdmin(t *testing.T, db *gorm.DB) entity.User {
	t.Helper()
	admin := entity.User{
		ID: 9201, Username: "SYNTHETIC_WHITELIST_ADMIN", Password: "123456",
		Role: "admin", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return admin
}

func decodeWhitelistEntryList(t *testing.T, recorder *httptest.ResponseRecorder) service.WhitelistEntryListResult {
	t.Helper()
	var result service.WhitelistEntryListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode entry list: %v (%s)", err, recorder.Body.String())
	}
	return result
}

// 非管理员在**每一条**路由上都必须 403，且零写。
func TestWhitelistAdminRejectsNonAdminOnEveryRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newWhitelistAdminTestDB(t)
	installWhitelistAdminTestDB(t, db)

	users := []*entity.User{
		{ID: 9202, Username: "SYNTHETIC_WHITELIST_PLAIN", Role: "user", Status: "enabled"},
		nil,
	}
	for index, actor := range users {
		router := whitelistAdminRouter(actor)
		cases := []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodGet, "/api/whitelist", ""},
			{http.MethodPost, "/api/whitelist", `{"kind":"ip","pattern":"203.0.113.9","reason":"x"}`},
			{http.MethodPut, "/api/whitelist/1/enabled", `{"enabled":false}`},
			{http.MethodDelete, "/api/whitelist/1", ""},
			{http.MethodGet, "/api/whitelist/exempt-ips", ""},
			{http.MethodPut, "/api/whitelist/exempt-ips", `{"value":"203.0.113.9"}`},
		}
		for _, item := range cases {
			recorder := whitelistRequest(router, item.method, item.path, item.body)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("actor %d %s %s: want 403, got %d (%s)",
					index, item.method, item.path, recorder.Code, recorder.Body.String())
			}
		}
	}

	var count int64
	if err := db.Model(&entity.WhitelistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if count != 0 {
		t.Fatalf("非管理员请求不得写规则，got %d 行", count)
	}
}

// 新增 → 立刻进缓存（放行）；列表能查到。
func TestWhitelistAdminCreateTakesEffectInCacheImmediately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newWhitelistAdminTestDB(t)
	installWhitelistAdminTestDB(t, db)
	admin := whitelistSeedAdmin(t, db)
	router := whitelistAdminRouter(&admin)

	// 前置：空表 → 缓存不命中。
	if err := service.ReloadDefaultWhitelistCache(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, hit := service.DefaultWhitelistCache().Snapshot().MatchIP("203.0.113.7"); hit {
		t.Fatal("前置条件：空表不应命中")
	}

	recorder := whitelistRequest(router, http.MethodPost, "/api/whitelist",
		`{"kind":"ip","pattern":"203.0.113.7","reason":"办公出口"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", recorder.Code, recorder.Body.String())
	}
	var created entity.WhitelistEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v (%s)", err, recorder.Body.String())
	}
	if created.ID <= 0 || !created.Enabled || created.Pattern != "203.0.113.7" {
		t.Fatalf("created = %+v", created)
	}
	// 响应里必须有脱敏值（管理页据此确认规则写对没有）。
	if created.ValueMasked != "203.0.113.*" {
		t.Fatalf("valueMasked = %q, want 203.0.113.*", created.ValueMasked)
	}

	// 写后刷新：缓存里立刻能命中。
	snapshot := service.DefaultWhitelistCache().Snapshot()
	if snapshot == nil {
		t.Fatal("写后必须已加载缓存")
	}
	if _, hit := snapshot.MatchIP("203.0.113.7"); !hit {
		t.Fatal("新增规则必须立刻进缓存（否则界面说加了、实际没生效）")
	}

	// 列表能查到。
	list := decodeWhitelistEntryList(t, whitelistRequest(router, http.MethodGet, "/api/whitelist", ""))
	if list.Total != 1 || len(list.Data) != 1 || list.Data[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}
}

// 启停与删除都必须让规则立刻从缓存里消失 / 重新出现。
func TestWhitelistAdminToggleAndDeleteBothAffectCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newWhitelistAdminTestDB(t)
	installWhitelistAdminTestDB(t, db)
	admin := whitelistSeedAdmin(t, db)
	router := whitelistAdminRouter(&admin)

	recorder := whitelistRequest(router, http.MethodPost, "/api/whitelist",
		`{"kind":"ip","pattern":"203.0.113.7","reason":"办公出口"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	var created entity.WhitelistEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, hit := service.DefaultWhitelistCache().Snapshot().MatchIP("203.0.113.7"); !hit {
		t.Fatal("前置条件：新增后应命中")
	}

	// 停用 → 不再放行。
	recorder = whitelistRequest(router, http.MethodPut,
		"/api/whitelist/"+itoa(created.ID)+"/enabled", `{"enabled":false}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("toggle status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if _, hit := service.DefaultWhitelistCache().Snapshot().MatchIP("203.0.113.7"); hit {
		t.Fatal("停用后必须立刻不再放行")
	}

	// 重新启用 → 又放行。
	recorder = whitelistRequest(router, http.MethodPut,
		"/api/whitelist/"+itoa(created.ID)+"/enabled", `{"enabled":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("toggle back status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if _, hit := service.DefaultWhitelistCache().Snapshot().MatchIP("203.0.113.7"); !hit {
		t.Fatal("重新启用后应再次放行")
	}

	// 删除（?reason= 必填）→ 不再放行。
	recorder = whitelistRequest(router, http.MethodDelete,
		"/api/whitelist/"+itoa(created.ID), "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺少 reason 的删除 status = %d, want 400", recorder.Code)
	}
	recorder = whitelistRequest(router, http.MethodDelete,
		"/api/whitelist/"+itoa(created.ID)+"?reason=测试删除", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if _, hit := service.DefaultWhitelistCache().Snapshot().MatchIP("203.0.113.7"); hit {
		t.Fatal("删除后必须立刻不再放行")
	}
	// 删除必须留快照审计（rule_audit.go 的 archiveRuleDeleteInTx）。
	//
	// ⚠️ 不能只按 module='whitelist' 过滤：recordAccountAudit 也往 logs 写
	// module='whitelist' 的操作审计行（创建/启停/删除各一条），
	// 只按 module 数会得到 5 条并把「快照有没有写」这件事完全淹没。
	// 快照行的判别特征是 action=RuleActionDelete 且 old_answer 非空（JSON 快照）。
	var audits []entity.Log
	if err := db.Where("module = ? AND action = ?", "whitelist", RuleActionDelete).
		Find(&audits).Error; err != nil {
		t.Fatalf("load audit: %v", err)
	}
	if len(audits) != 1 {
		t.Fatalf("删除快照日志 = %d 条, want 1", len(audits))
	}
	if audits[0].OldAnswer == "" || !contains(audits[0].OldAnswer, "203.0.113.7") {
		t.Fatalf("快照必须含规则原文以便重建：%q", audits[0].OldAnswer)
	}
	if audits[0].NewAnswer != "" {
		t.Fatalf("删除行不该有「修改后」的值：%q", audits[0].NewAnswer)
	}
}

// 非法输入必须 400（不是静默 clamp、也不是存下一条永不生效的脏规则）。
func TestWhitelistAdminCreateRejectsInvalidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newWhitelistAdminTestDB(t)
	installWhitelistAdminTestDB(t, db)
	admin := whitelistSeedAdmin(t, db)
	router := whitelistAdminRouter(&admin)

	cases := []struct {
		name string
		body string
	}{
		{"非法 IP", `{"kind":"ip","pattern":"not-an-ip","reason":"x"}`},
		{"非法 CIDR", `{"kind":"ip","pattern":"203.0.113.0/33","reason":"x"}`},
		{"不支持的粒度", `{"kind":"phone","pattern":"13800001111","reason":"x"}`},
		{"缺 reason", `{"kind":"ip","pattern":"203.0.113.7"}`},
		{"空白 reason", `{"kind":"ip","pattern":"203.0.113.7","reason":"   "}`},
		{"非法过期时间", `{"kind":"ip","pattern":"203.0.113.7","reason":"x","expiresAt":"not-a-date"}`},
	}
	for _, tc := range cases {
		recorder := whitelistRequest(router, http.MethodPost, "/api/whitelist", tc.body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (%s)", tc.name, recorder.Code, recorder.Body.String())
		}
	}
	var count int64
	if err := db.Model(&entity.WhitelistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("非法输入必须零写入，got %d 行", count)
	}
	// 不存在的 id：404（不是 500）。
	if recorder := whitelistRequest(router, http.MethodPut,
		"/api/whitelist/999999/enabled", `{"enabled":false}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("不存在的 id status = %d, want 404 (%s)", recorder.Code, recorder.Body.String())
	}
	// 缺 enabled 字段：400（不猜方向）。
	if recorder := whitelistRequest(router, http.MethodPut,
		"/api/whitelist/1/enabled", `{}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺 enabled status = %d, want 400", recorder.Code)
	}
}

// T-05：blacklist_exempt_ips 的读 / 写 / 校验 / 立刻生效。
func TestWhitelistAdminExemptIPsGetPutAndTakeEffect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newWhitelistAdminTestDB(t)
	installWhitelistAdminTestDB(t, db)
	admin := whitelistSeedAdmin(t, db)
	router := whitelistAdminRouter(&admin)

	// 初始为空。
	recorder := whitelistRequest(router, http.MethodGet, "/api/whitelist/exempt-ips", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("get status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	var got struct {
		Value string   `json:"value"`
		Count int      `json:"count"`
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Value != "" || got.Count != 0 || len(got.Items) != 0 {
		t.Fatalf("初始值应为空，got %+v", got)
	}

	// 写入：单 IP + CIDR + 换行分隔。
	recorder = whitelistRequest(router, http.MethodPut, "/api/whitelist/exempt-ips",
		`{"value":"203.0.113.7\n198.51.100.0/24"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("put status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	// 必须立刻生效（保存后重载缓存）。
	snapshot := service.DefaultWhitelistCache().Snapshot()
	if snapshot == nil {
		t.Fatal("保存后必须已加载缓存")
	}
	if match, hit := snapshot.MatchIP("203.0.113.7"); !hit || match.Source != service.WhitelistSourceExemptIPs {
		t.Fatalf("exempt_ips 单 IP 应立刻生效：match=%+v hit=%v", match, hit)
	}
	if _, hit := snapshot.MatchIP("198.51.100.99"); !hit {
		t.Fatal("exempt_ips CIDR 应立刻生效")
	}

	// 读回：count / items 必须反映实际解析结果。
	recorder = whitelistRequest(router, http.MethodGet, "/api/whitelist/exempt-ips", "")
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Count != 2 || len(got.Items) != 2 {
		t.Fatalf("解析结果 = %+v, want 2 项", got)
	}

	// 非法项必须 400，且旧值不被覆盖。
	recorder = whitelistRequest(router, http.MethodPut, "/api/whitelist/exempt-ips",
		`{"value":"203.0.113.7, not-an-ip"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法项 status = %d, want 400 (%s)", recorder.Code, recorder.Body.String())
	}
	if persisted := service.GetWhitelistExemptIPsWithDB(db); persisted != "203.0.113.7\n198.51.100.0/24" {
		t.Fatalf("非法输入不得覆盖旧值，got %q", persisted)
	}

	// 缺 value 字段：400。
	if recorder := whitelistRequest(router, http.MethodPut,
		"/api/whitelist/exempt-ips", `{}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺 value status = %d, want 400", recorder.Code)
	}
}

// 关键词只匹配脱敏值与原因，不匹配 pattern 原文（与黑名单同约定）。
func TestWhitelistAdminListKeywordDoesNotMatchPattern(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newWhitelistAdminTestDB(t)
	installWhitelistAdminTestDB(t, db)
	admin := whitelistSeedAdmin(t, db)
	router := whitelistAdminRouter(&admin)

	if recorder := whitelistRequest(router, http.MethodPost, "/api/whitelist",
		`{"kind":"ip","pattern":"203.0.113.7","reason":"办公出口"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	// 脱敏值是 203.0.113.*，原文 .7 的前缀检索仍会命中脱敏值，所以这里用
	// 「只存在于原文、不在脱敏值里」的片段来区分：'113.7'。
	list := decodeWhitelistEntryList(t, whitelistRequest(router,
		http.MethodGet, "/api/whitelist?keyword=113.7", ""))
	if list.Total != 0 {
		t.Fatalf("关键词不得匹配 pattern 原文，got total=%d", list.Total)
	}
	// 原因可搜到。
	list = decodeWhitelistEntryList(t, whitelistRequest(router,
		http.MethodGet, "/api/whitelist?keyword=办公", ""))
	if list.Total != 1 {
		t.Fatalf("关键词应匹配 reason，got total=%d", list.Total)
	}
}
