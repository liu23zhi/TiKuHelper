package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 黑名单管理页控制器测试（BL-5）。
//
// 全部用真 sqlite 内存库（newUserControllerTestDB），不 mock service：
// 「新增后缓存立即生效」「停用后不再命中」这类断言只有在真实 GORM 写库 +
// 真实缓存重载链路上才有意义 —— 换成 mock 就退化成「我们调用了一个函数」。

// newBlacklistAdminTestDB 建控制器测试库（含黑名单两张表 + system_settings）。
func newBlacklistAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}, &entity.BlacklistHit{}); err != nil {
		t.Fatalf("migrate blacklist tables: %v", err)
	}
	return db
}

// setBlacklistAdminSwitch 写黑名单总开关并重载单例缓存。
//
// 必须先打开开关再重载：LoadBlacklistSnapshot 在开关为 false 时**只读一次开关就返回空快照**，
// 于是「新增后缓存里能查到」这类断言永远不可能通过 —— 这正是「开关默认关闭」的语义，
// 测试必须显式把它打开，否则测的是「关闭时什么都不做」。
func setBlacklistAdminSwitch(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()

	value := "false"
	if enabled {
		value = "true"
	}
	var existing entity.SystemSetting
	err := db.Where("`key` = ?", service.BlacklistEnabledSettingKey).Take(&existing).Error
	switch {
	case err == nil:
		if err := db.Model(&entity.SystemSetting{}).Where("`key` = ?", service.BlacklistEnabledSettingKey).
			Update("value", value).Error; err != nil {
			t.Fatalf("update blacklist switch: %v", err)
		}
	case err != nil && err != gorm.ErrRecordNotFound:
		t.Fatalf("read blacklist switch: %v", err)
	default:
		if err := db.Create(&entity.SystemSetting{Key: service.BlacklistEnabledSettingKey, Value: value}).Error; err != nil {
			t.Fatalf("seed blacklist switch: %v", err)
		}
	}
	if err := service.ReloadDefaultBlacklistCache(db); err != nil {
		t.Fatalf("reload blacklist cache: %v", err)
	}
}

// installBlacklistAdminTestDB 把控制器与缓存刷新的数据库取用点换成测试库。
func installBlacklistAdminTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()

	previousDB := blacklistAdminDB
	previousReload := reloadBlacklistCache
	blacklistAdminDB = func() *gorm.DB { return db }
	reloadBlacklistCache = func(target *gorm.DB) error {
		if target == nil {
			return service.ErrBlacklistCacheNoDatabase
		}
		return service.ReloadDefaultBlacklistCache(target)
	}
	t.Cleanup(func() {
		blacklistAdminDB = previousDB
		reloadBlacklistCache = previousReload
	})
}

// blacklistAdminRouter 组装与 router.go 完全一致的中间件顺序。
//
// 注意：这里**刻意不挂 middleware.BlacklistIP()**，与生产 router.go 一致 ——
// 那五条管理接口挂在一个不带 BlacklistIP 的独立 group 上（防管理员自锁），
// 如果测试里补上 BlacklistIP，测的就是另一套链路了。
func blacklistAdminRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/adapter-service/blacklist", auth, middleware.RequireAdmin, BlacklistEntryList)
	router.POST("/adapter-service/blacklist", auth, middleware.RequireAdmin, BlacklistEntryCreate)
	router.PUT("/adapter-service/blacklist/:id/enabled", auth, middleware.RequireAdmin, BlacklistEntryToggle)
	router.DELETE("/adapter-service/blacklist/:id", auth, middleware.RequireAdmin, BlacklistEntryDelete)
	router.GET("/adapter-service/blacklist/hits", auth, middleware.RequireAdmin, BlacklistHitList)
	return router
}

// blacklistRequest 发一次请求并返回 recorder。
func blacklistRequest(router *gin.Engine, method string, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

// blacklistSeedAdmin 建一个管理员账户（RequireAdmin 依据 role + status）。
func blacklistSeedAdmin(t *testing.T, db *gorm.DB) entity.User {
	t.Helper()

	admin := entity.User{
		ID: 9101, Username: "SYNTHETIC_BLACKLIST_ADMIN", Password: "123456",
		Role: "admin", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return admin
}

// decodeBlacklistEntryList 解列表响应。
func decodeBlacklistEntryList(t *testing.T, recorder *httptest.ResponseRecorder) service.BlacklistEntryListResult {
	t.Helper()

	var result service.BlacklistEntryListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode entry list: %v (%s)", err, recorder.Body.String())
	}
	return result
}

func TestBlacklistAdminRejectsNonAdminOnEveryRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistAdminTestDB(t)
	installBlacklistAdminTestDB(t, db)

	// 非管理员 / 无身份两种都要 403，且**不产生任何写**。
	//
	// ⚠️ 这里**不测**「role=admin 但 status=disabled」：该场景由 controller.Auth
	// 在签发/校验 token 时拦下（停用账号拿不到有效会话），middleware.RequireAdmin
	// 只看 role（见 middleware/authz.go）。在同一条链上再断言一次会测到 Auth 的职责，
	// 而本测试的 router 没有 Auth —— 断言会变成对 RequireAdmin 的错误期待。
	users := []*entity.User{
		{ID: 9102, Username: "SYNTHETIC_PLAIN", Role: "user", Status: "enabled"},
		nil,
	}
	for index, actor := range users {
		router := blacklistAdminRouter(actor)
		cases := []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodGet, "/adapter-service/blacklist", ""},
			{http.MethodPost, "/adapter-service/blacklist", `{"kind":"ip","pattern":"203.0.113.9","reason":"x"}`},
			{http.MethodPut, "/adapter-service/blacklist/1/enabled", `{"enabled":false}`},
			{http.MethodDelete, "/adapter-service/blacklist/1", ""},
			{http.MethodGet, "/adapter-service/blacklist/hits", ""},
		}
		for _, item := range cases {
			recorder := blacklistRequest(router, item.method, item.path, item.body)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("actor %d %s %s: want 403, got %d (%s)",
					index, item.method, item.path, recorder.Code, recorder.Body.String())
			}
		}
	}

	var count int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if count != 0 {
		t.Fatalf("non-admin requests must not write entries, got %d", count)
	}
}

func TestBlacklistAdminListPaginatesAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistAdminTestDB(t)
	installBlacklistAdminTestDB(t, db)
	admin := blacklistSeedAdmin(t, db)
	setBlacklistAdminSwitch(t, db, true)
	router := blacklistAdminRouter(&admin)

	// 三条规则：IP 精确（生效）、IP CIDR（停用）、手机号（生效）。
	if recorder := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist",
		`{"kind":"ip","pattern":"203.0.113.10","reason":"SYNTHETIC_REASON_IP","enabled":true}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create ip exact: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist",
		`{"kind":"ip","pattern":"198.51.100.0/24","reason":"SYNTHETIC_REASON_CIDR","enabled":false}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create ip cidr: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist",
		`{"kind":"phone","pattern":"13800001111","reason":"SYNTHETIC_REASON_PHONE","enabled":true}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create phone: %d %s", recorder.Code, recorder.Body.String())
	}

	all := decodeBlacklistEntryList(t, blacklistRequest(router, http.MethodGet, "/adapter-service/blacklist", ""))
	if all.Total != 3 || len(all.Data) != 3 {
		t.Fatalf("want 3 entries, got total=%d len=%d", all.Total, len(all.Data))
	}
	// 排序 created_at desc, id desc：刚加的手机号必须在第一行。
	if all.Data[0].Kind != service.BlacklistKindPhone {
		t.Fatalf("want newest entry first, got kind=%q", all.Data[0].Kind)
	}
	// 新增时 enabled=false 必须真的落成 false（GORM default:true 零值陷阱的回归钉子）。
	if all.Data[1].Enabled {
		t.Fatalf("cidr entry created with enabled=false but stored enabled=true: %+v", all.Data[1])
	}

	// 分页：pageSize=2 → 第一页 2 条、total 仍是 3；第二页 1 条。
	firstPage := decodeBlacklistEntryList(t, blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist?page=1&pageSize=2", ""))
	if firstPage.Total != 3 || len(firstPage.Data) != 2 || firstPage.Page != 1 || firstPage.PageSize != 2 {
		t.Fatalf("page 1 of size 2 wrong: total=%d len=%d page=%d size=%d",
			firstPage.Total, len(firstPage.Data), firstPage.Page, firstPage.PageSize)
	}
	secondPage := decodeBlacklistEntryList(t, blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist?page=2&pageSize=2", ""))
	if len(secondPage.Data) != 1 {
		t.Fatalf("page 2 should hold the remaining row, got %d", len(secondPage.Data))
	}

	// kind 筛选。
	ips := decodeBlacklistEntryList(t, blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist?kind=ip", ""))
	if ips.Total != 2 {
		t.Fatalf("kind=ip should match 2, got %d", ips.Total)
	}
	// enabled 筛选：false 必须只返回停用那条（裸 bool 零值会退化成「不筛选」，这里钉住）。
	disabled := decodeBlacklistEntryList(t, blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist?enabled=false", ""))
	if disabled.Total != 1 || disabled.Data[0].MatchType != service.BlacklistMatchCIDR {
		t.Fatalf("enabled=false should return the cidr row only, got total=%d data=%+v", disabled.Total, disabled.Data)
	}
	// 关键词同时匹配 valueMasked 与 reason。
	byReason := decodeBlacklistEntryList(t, blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist?keyword=SYNTHETIC_REASON_PHONE", ""))
	if byReason.Total != 1 || byReason.Data[0].Kind != service.BlacklistKindPhone {
		t.Fatalf("keyword on reason should match the phone row, got total=%d", byReason.Total)
	}
	// 关键词匹配 valueMasked（脱敏值 203.0.113.*）。
	byMasked := decodeBlacklistEntryList(t, blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist?keyword=203.0.113", ""))
	if byMasked.Total != 1 {
		t.Fatalf("keyword on masked value should match 1, got %d", byMasked.Total)
	}
	// 非法 kind 筛选必须 400，而不是「查不到 → 空列表」（运营会以为规则丢了）。
	if recorder := blacklistRequest(router, http.MethodGet, "/adapter-service/blacklist?kind=synthetic", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind filter should be 400, got %d", recorder.Code)
	}
	// 非法 enabled 同样 400。
	if recorder := blacklistRequest(router, http.MethodGet, "/adapter-service/blacklist?enabled=Synthetic", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid enabled filter should be 400, got %d", recorder.Code)
	}
}

func TestBlacklistAdminCreateTakesEffectInCacheImmediately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistAdminTestDB(t)
	installBlacklistAdminTestDB(t, db)
	admin := blacklistSeedAdmin(t, db)
	setBlacklistAdminSwitch(t, db, true)
	router := blacklistAdminRouter(&admin)

	// 建之前：缓存里查不到，判定不命中。
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchIP("203.0.113.42"); hit {
		t.Fatalf("ip must not be blacklisted before creation")
	}

	recorder := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist",
		`{"kind":"ip","pattern":"203.0.113.42","reason":"SYNTHETIC_IMMEDIATE","enabled":true}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", recorder.Code, recorder.Body.String())
	}
	var created entity.BlacklistEntry
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created entry: %v", err)
	}
	if created.ValueMasked != "203.0.113.*" {
		t.Fatalf("server must fill valueMasked, got %q", created.ValueMasked)
	}
	if created.ValueHash != service.BlacklistPatternHashHex("203.0.113.42") {
		t.Fatalf("valueHash must be sha256(normalized pattern), got %q", created.ValueHash)
	}

	// 关键断言：**不手动重载**，直接看单例缓存 —— 写路径必须自己刷新。
	match, hit := service.DefaultBlacklistCache().Snapshot().MatchIP("203.0.113.42")
	if !hit {
		t.Fatalf("newly created rule is not effective in cache (write path did not reload)")
	}
	if match.EntryID != created.ID {
		t.Fatalf("cache points at entry %d, want %d", match.EntryID, created.ID)
	}

	// 手机号规则同理（归一化 + hash 路径）。
	phoneRecorder := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist",
		`{"kind":"phone","pattern":"138 0000 2222","reason":"SYNTHETIC_PHONE"}`)
	if phoneRecorder.Code != http.StatusCreated {
		t.Fatalf("create phone: %d %s", phoneRecorder.Code, phoneRecorder.Body.String())
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchPhone("13800002222"); !hit {
		t.Fatalf("phone rule with spaces was not normalized into cache")
	}

	// 过期时间：显式设成过去 → 建成功但**不进缓存**（46-*.md §7「到期自动失效」）。
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	expiredRecorder := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist",
		fmt.Sprintf(`{"kind":"ip","pattern":"203.0.113.43","reason":"SYNTHETIC_EXPIRED","expiresAt":%q}`, past))
	if expiredRecorder.Code != http.StatusCreated {
		t.Fatalf("create expired: %d %s", expiredRecorder.Code, expiredRecorder.Body.String())
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchIP("203.0.113.43"); hit {
		t.Fatalf("already expired rule must not enter the cache")
	}
}

func TestBlacklistAdminToggleAndDeleteBothDropTheRuleFromCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistAdminTestDB(t)
	installBlacklistAdminTestDB(t, db)
	admin := blacklistSeedAdmin(t, db)
	setBlacklistAdminSwitch(t, db, true)
	router := blacklistAdminRouter(&admin)

	create := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist",
		`{"kind":"ip","pattern":"198.51.100.77","reason":"SYNTHETIC_TOGGLE"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var entry entity.BlacklistEntry
	if err := json.Unmarshal(create.Body.Bytes(), &entry); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchIP("198.51.100.77"); !hit {
		t.Fatalf("rule must be effective right after creation")
	}

	// 停用：缓存必须立刻不再命中。
	togglePath := "/adapter-service/blacklist/" + strconv.FormatInt(entry.ID, 10) + "/enabled"
	toggle := blacklistRequest(router, http.MethodPut, togglePath, `{"enabled":false}`)
	if toggle.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", toggle.Code, toggle.Body.String())
	}
	var toggled entity.BlacklistEntry
	if err := json.Unmarshal(toggle.Body.Bytes(), &toggled); err != nil {
		t.Fatalf("decode toggled entry: %v", err)
	}
	if toggled.Enabled {
		t.Fatalf("response says enabled=true after disabling")
	}
	// 库里的 enabled 必须是 false（GORM default:true 零值陷阱的回归钉子）。
	var reloaded entity.BlacklistEntry
	if err := db.Where("id = ?", entry.ID).Take(&reloaded).Error; err != nil {
		t.Fatalf("reload entry: %v", err)
	}
	if reloaded.Enabled {
		t.Fatalf("enabled column is still true after disable — Update didn't land")
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchIP("198.51.100.77"); hit {
		t.Fatalf("disabled rule still matches in cache (cache was not reloaded after write)")
	}

	// 重新启用：又命中。
	if recorder := blacklistRequest(router, http.MethodPut, togglePath, `{"enabled":true}`); recorder.Code != http.StatusOK {
		t.Fatalf("re-enable: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchIP("198.51.100.77"); !hit {
		t.Fatalf("re-enabled rule does not match in cache")
	}

	// 删除：缓存不再命中，且库里没有这行了；命中记录保留。
	deletePath := "/adapter-service/blacklist/" + strconv.FormatInt(entry.ID, 10)
	if recorder := blacklistRequest(router, http.MethodDelete, deletePath, ""); recorder.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, hit := service.DefaultBlacklistCache().Snapshot().MatchIP("198.51.100.77"); hit {
		t.Fatalf("deleted rule still matches in cache (cache was not reloaded after delete)")
	}
	var remaining int64
	if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entry.ID).Count(&remaining).Error; err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("entry %d still present after delete", entry.ID)
	}

	// 再次删除同一 id → 404（不是 200 也不是 500）。
	if recorder := blacklistRequest(router, http.MethodDelete, deletePath, ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("second delete should be 404, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	// 启停不存在的 id → 404。
	if recorder := blacklistRequest(router, http.MethodPut,
		"/adapter-service/blacklist/999999/enabled", `{"enabled":false}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("toggle missing entry should be 404, got %d", recorder.Code)
	}
}

func TestBlacklistAdminCreateRejectsInvalidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistAdminTestDB(t)
	installBlacklistAdminTestDB(t, db)
	admin := blacklistSeedAdmin(t, db)
	setBlacklistAdminSwitch(t, db, true)
	router := blacklistAdminRouter(&admin)

	cases := []struct {
		name string
		body string
		want int
	}{
		// kind 不在四类粒度内。
		{"unknown kind", `{"kind":"synthetic","pattern":"203.0.113.1","reason":"x"}`, http.StatusBadRequest},
		// region 本期不接入判定 → 明确 400，不静默存下一条永不生效的规则。
		{"region not implemented", `{"kind":"region","pattern":"云南","reason":"x"}`, http.StatusBadRequest},
		// 非法 IP。
		{"invalid ip", `{"kind":"ip","pattern":"203.0.113.999","reason":"x"}`, http.StatusBadRequest},
		// 非法 CIDR。
		{"invalid cidr", `{"kind":"ip","pattern":"203.0.113.0/99","reason":"x"}`, http.StatusBadRequest},
		// 非法手机号。
		{"invalid phone", `{"kind":"phone","pattern":"12345","reason":"x"}`, http.StatusBadRequest},
		// 空 reason（46-*.md §2.1 必填）。
		{"empty reason", `{"kind":"ip","pattern":"203.0.113.11","reason":"   "}`, http.StatusBadRequest},
		// 手机号不支持 cidr。
		{"phone with cidr", `{"kind":"phone","pattern":"13800003333","matchType":"cidr","reason":"x"}`, http.StatusBadRequest},
		// 无法解析的过期时间。
		{"bad expiresAt", `{"kind":"ip","pattern":"203.0.113.12","reason":"x","expiresAt":"tomorrow"}`, http.StatusBadRequest},
	}
	for _, item := range cases {
		recorder := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist", item.body)
		if recorder.Code != item.want {
			t.Fatalf("%s: want %d, got %d (%s)", item.name, item.want, recorder.Code, recorder.Body.String())
		}
	}

	// 启停缺 enabled 字段 → 400（不猜方向）。
	create := blacklistRequest(router, http.MethodPost, "/adapter-service/blacklist",
		`{"kind":"ip","pattern":"203.0.113.13","reason":"SYNTHETIC_KEEP"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("seed entry: %d %s", create.Code, create.Body.String())
	}
	var entry entity.BlacklistEntry
	if err := json.Unmarshal(create.Body.Bytes(), &entry); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if recorder := blacklistRequest(router, http.MethodPut,
		"/adapter-service/blacklist/"+strconv.FormatInt(entry.ID, 10)+"/enabled", `{}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("toggle without enabled should be 400, got %d (%s)", recorder.Code, recorder.Body.String())
	}

	// 非法输入一条都不能落库。
	var count int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if count != 1 {
		t.Fatalf("invalid input must not write entries; want 1 seeded row, got %d", count)
	}
}

func TestBlacklistAdminHitsListOrdersNewestFirstAndFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newBlacklistAdminTestDB(t)
	installBlacklistAdminTestDB(t, db)
	admin := blacklistSeedAdmin(t, db)
	setBlacklistAdminSwitch(t, db, true)
	router := blacklistAdminRouter(&admin)

	now := time.Now().UTC()
	hits := []entity.BlacklistHit{
		{EntryID: 11, Kind: service.BlacklistKindIP, ValueMasked: "203.0.113.*", ClientIP: "203.0.113.5",
			Path: "/adapter-service/search", Action: service.BlacklistActionBlocked, HitCount: 1,
			CreatedAt: now.Add(-2 * time.Hour)},
		{EntryID: 11, Kind: service.BlacklistKindIP, ValueMasked: "203.0.113.*", ClientIP: "203.0.113.5",
			Path: "/adapter-service/user/login", Action: service.BlacklistActionBlocked, HitCount: 7,
			CreatedAt: now.Add(-time.Minute)},
		{EntryID: 22, Kind: service.BlacklistKindPhone, ValueMasked: "138****2222", ClientIP: "198.51.100.9",
			Path: "/adapter-service/user/register", Action: service.BlacklistActionLoggedOnly, HitCount: 1,
			CreatedAt: now.Add(-time.Hour)},
	}
	for index := range hits {
		if err := db.Create(&hits[index]).Error; err != nil {
			t.Fatalf("seed hit %d: %v", index, err)
		}
	}

	all := blacklistRequest(router, http.MethodGet, "/adapter-service/blacklist/hits", "")
	if all.Code != http.StatusOK {
		t.Fatalf("hits list: %d %s", all.Code, all.Body.String())
	}
	var listResult service.BlacklistHitListResult
	if err := json.Unmarshal(all.Body.Bytes(), &listResult); err != nil {
		t.Fatalf("decode hits: %v", err)
	}
	if listResult.Total != 3 || len(listResult.Data) != 3 {
		t.Fatalf("want 3 hits, got total=%d len=%d", listResult.Total, len(listResult.Data))
	}
	// created_at desc：最新那条（1 分钟前）必须排第一。
	if listResult.Data[0].ClientIP != "203.0.113.5" || listResult.Data[0].HitCount != 7 {
		t.Fatalf("hits must be ordered newest first, got %+v", listResult.Data[0])
	}
	if !listResult.Data[1].CreatedAt.After(listResult.Data[2].CreatedAt) {
		t.Fatalf("hits order is not strictly descending: %v then %v", listResult.Data[1].CreatedAt, listResult.Data[2].CreatedAt)
	}

	// entryId 筛选。
	byEntry := blacklistRequest(router, http.MethodGet, "/adapter-service/blacklist/hits?entryId=11", "")
	var entryResult service.BlacklistHitListResult
	if err := json.Unmarshal(byEntry.Body.Bytes(), &entryResult); err != nil {
		t.Fatalf("decode entry hits: %v", err)
	}
	if entryResult.Total != 2 {
		t.Fatalf("entryId=11 should match 2, got %d", entryResult.Total)
	}
	// action 筛选。
	byAction := blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist/hits?action=logged_only", "")
	var actionResult service.BlacklistHitListResult
	if err := json.Unmarshal(byAction.Body.Bytes(), &actionResult); err != nil {
		t.Fatalf("decode action hits: %v", err)
	}
	if actionResult.Total != 1 || actionResult.Data[0].EntryID != 22 {
		t.Fatalf("action=logged_only should match the phone row, got total=%d", actionResult.Total)
	}
	// clientIp 精确匹配。
	byIP := blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist/hits?clientIp=198.51.100.9", "")
	var ipResult service.BlacklistHitListResult
	if err := json.Unmarshal(byIP.Body.Bytes(), &ipResult); err != nil {
		t.Fatalf("decode ip hits: %v", err)
	}
	if ipResult.Total != 1 {
		t.Fatalf("clientIp filter should match 1, got %d", ipResult.Total)
	}
	// 分页。
	paged := blacklistRequest(router, http.MethodGet, "/adapter-service/blacklist/hits?page=2&pageSize=2", "")
	var pagedResult service.BlacklistHitListResult
	if err := json.Unmarshal(paged.Body.Bytes(), &pagedResult); err != nil {
		t.Fatalf("decode paged hits: %v", err)
	}
	if pagedResult.Total != 3 || len(pagedResult.Data) != 1 {
		t.Fatalf("page 2 of size 2: want total=3 len=1, got total=%d len=%d", pagedResult.Total, len(pagedResult.Data))
	}
	// 非法 action / kind → 400。
	if recorder := blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist/hits?action=synthetic", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid action should be 400, got %d", recorder.Code)
	}
	if recorder := blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist/hits?kind=synthetic", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind should be 400, got %d", recorder.Code)
	}
	if recorder := blacklistRequest(router, http.MethodGet,
		"/adapter-service/blacklist/hits?entryId=synthetic", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid entryId should be 400, got %d", recorder.Code)
	}
}
