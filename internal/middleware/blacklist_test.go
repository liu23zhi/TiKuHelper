package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newBlacklistMiddlewareTestDB 建一个隔离的黑名单测试库（含 system_settings）。
func newBlacklistMiddlewareTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:blacklist-mw-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.BlacklistEntry{}, &entity.BlacklistHit{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func setBlacklistSwitch(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()
	value := "false"
	if enabled {
		value = "true"
	}
	setting := entity.SystemSetting{Key: service.BlacklistEnabledSettingKey, Value: value, Remark: "启用黑名单（默认关闭）"}
	if err := db.Where(map[string]any{"key": service.BlacklistEnabledSettingKey}).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear switch: %v", err)
	}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatalf("set switch: %v", err)
	}
}

// seedBlacklistIPRule 落一条 IP 规则，并保证 Enabled 与入参一致（两步写，见 entity 注释）。
func seedBlacklistIPRule(t *testing.T, db *gorm.DB, pattern string, matchType string, enabled bool) entity.BlacklistEntry {
	t.Helper()
	if matchType == "" {
		matchType = service.BlacklistMatchExact
	}
	normalized, err := service.NormalizeBlacklistValue(service.BlacklistKindIP, pattern, matchType)
	if err != nil {
		t.Fatalf("normalize %q: %v", pattern, err)
	}
	entry := entity.BlacklistEntry{
		Kind: normalized.Kind, Pattern: normalized.Pattern, MatchType: normalized.MatchType,
		ValueHash: normalized.ValueHash, ValueMasked: normalized.ValueMasked,
		Reason: "中间件测试", Enabled: true,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if !enabled {
		if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entry.ID).
			Update("enabled", false).Error; err != nil {
			t.Fatalf("disable rule: %v", err)
		}
		entry.Enabled = false
	}
	return entry
}

// newBlacklistTestRouter 建一个最小路由：中间件 + 一个永远返回 200 的 handler。
func newBlacklistTestRouter(mw gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(mw)
	router.Any("/*path", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})
	return router
}

func doBlacklistRequest(router *gin.Engine, method string, path string, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func loadBlacklistCache(t *testing.T, db *gorm.DB) *service.BlacklistCache {
	t.Helper()
	cache := service.NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload cache: %v", err)
	}
	return cache
}

func countBlacklistHits(t *testing.T, db *gorm.DB) (rows int64, total int64) {
	t.Helper()
	if err := db.Model(&entity.BlacklistHit{}).Count(&rows).Error; err != nil {
		t.Fatalf("count hits: %v", err)
	}
	var sum struct {
		Total int64 `gorm:"column:total"`
	}
	if err := db.Model(&entity.BlacklistHit{}).Select("COALESCE(SUM(hit_count),0) AS total").Scan(&sum).Error; err != nil {
		t.Fatalf("sum hit_count: %v", err)
	}
	return rows, sum.Total
}

// ---------------------------------------------------------------------------
// 开关：关闭时零行为（零查询、零写日志）
// ---------------------------------------------------------------------------

// 开关关闭（默认）时：即使库里已经有一条「能命中」的规则，也不拦、不写日志。
// 这是「关闭时全站行为与今天完全一致」的可测证据。
func TestBlacklistMiddlewareDisabledByDefaultIsZeroBehavior(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	// 刻意不写 blacklist_enabled（缺记录 = 默认 false）。
	seedBlacklistIPRule(t, db, "203.0.113.7", service.BlacklistMatchExact, true)

	cache := loadBlacklistCache(t, db)
	if cache.Enabled() {
		t.Fatal("missing blacklist_enabled must load as disabled")
	}
	recorder := service.NewBlacklistHitRecorder()
	router := newBlacklistTestRouter(BlacklistIPWith(cache, recorder, db))

	response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:12345")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (switch off must not block)", response.Code)
	}
	if rows, total := countBlacklistHits(t, db); rows != 0 || total != 0 {
		t.Fatalf("switch off must write zero hit rows, got rows=%d total=%d", rows, total)
	}
	if slots := recorder.SlotCount(); slots != 0 {
		t.Fatalf("switch off must not touch the aggregator, slots=%d", slots)
	}

	// 显式写 false 也一样。
	setBlacklistSwitch(t, db, false)
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:12345"); response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with blacklist_enabled=false", response.Code)
	}
	if rows, _ := countBlacklistHits(t, db); rows != 0 {
		t.Fatalf("explicit false must also write zero hit rows, got %d", rows)
	}
}

// 开关关闭时**零行为**的硬证据：请求处理期间对黑名单两张表零查询、零写入。
//
// 用 GORM 的 Query/Row/Raw 回调计数（与 registry/billing_client_ip_migration_test.go
// 同款手法），而不是只看「没返回 403」—— 后者无法区分「没查」与「查了没命中」。
func TestBlacklistMiddlewareSwitchOffIssuesZeroBlacklistQueries(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	// 库里有一条能命中当前 IP 的规则；开关缺记录（= false）。
	seedBlacklistIPRule(t, db, "0.0.0.0/0", service.BlacklistMatchCIDR, true)

	cache := loadBlacklistCache(t, db) // 加载发生在计数开始之前
	if cache.Enabled() {
		t.Fatal("precondition: switch must be off")
	}

	var entriesQueries, hitsQueries, hitsWrites atomic.Int64
	countStatement := func(statement string, entries, hits *atomic.Int64) {
		lower := strings.ToLower(statement)
		switch {
		case strings.Contains(lower, entity.TableNameBlacklistEntry):
			entries.Add(1)
		case strings.Contains(lower, entity.TableNameBlacklistHit):
			hits.Add(1)
		}
	}
	if err := db.Callback().Query().After("gorm:query").Register("blacklist_test:count_query", func(tx *gorm.DB) {
		countStatement(tx.Statement.SQL.String(), &entriesQueries, &hitsQueries)
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}
	if err := db.Callback().Raw().After("gorm:raw").Register("blacklist_test:count_raw", func(tx *gorm.DB) {
		countStatement(tx.Statement.SQL.String(), &entriesQueries, &hitsQueries)
	}); err != nil {
		t.Fatalf("register raw callback: %v", err)
	}
	if err := db.Callback().Create().After("gorm:create").Register("blacklist_test:count_create", func(tx *gorm.DB) {
		if strings.Contains(strings.ToLower(tx.Statement.Table), entity.TableNameBlacklistHit) {
			hitsWrites.Add(1)
		}
	}); err != nil {
		t.Fatalf("register create callback: %v", err)
	}

	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))
	for i := 0; i < 20; i++ {
		if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234"); response.Code != http.StatusOK {
			t.Fatalf("request #%d status = %d, want 200 with the switch off", i, response.Code)
		}
	}

	if got := entriesQueries.Load(); got != 0 {
		t.Fatalf("switch off must issue zero blacklist_entries queries per request, got %d", got)
	}
	if got := hitsQueries.Load(); got != 0 {
		t.Fatalf("switch off must issue zero blacklist_hits queries, got %d", got)
	}
	if got := hitsWrites.Load(); got != 0 {
		t.Fatalf("switch off must issue zero blacklist_hits writes, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// 精确 IP / CIDR / IPv6 命中与未命中
// ---------------------------------------------------------------------------

func TestBlacklistMiddlewareExactIPHit(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	entry := seedBlacklistIPRule(t, db, "203.0.113.7", service.BlacklistMatchExact, true)

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))

	// 命中 → 403 + 严格响应体。
	response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:12345")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body=%q)", err, response.Body.String())
	}
	if got := body["code"]; got != float64(403) {
		t.Fatalf("body code = %v, want 403", got)
	}
	if got := body["message"]; got != "当前访问受限，如有疑问请联系管理员" {
		t.Fatalf("body message = %v", got)
	}
	// 不得泄漏规则细节：响应体里不能出现网段、规则 ID、kind。
	for _, leak := range []string{"203.0.113", "cidr", "exact", "ip", "blacklist", "entry"} {
		if strings.Contains(strings.ToLower(response.Body.String()), leak) {
			t.Fatalf("response body must not leak %q: %s", leak, response.Body.String())
		}
	}

	// 未命中 → 放行。
	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.8:12345"); response.Code != http.StatusOK {
		t.Fatalf("other IP status = %d, want 200", response.Code)
	}

	// 命中日志：一条 blocked 行，带脱敏值与路径。
	var hits []entity.BlacklistHit
	if err := db.Find(&hits).Error; err != nil {
		t.Fatalf("load hits: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hit rows = %d, want 1", len(hits))
	}
	hit := hits[0]
	if hit.EntryID != entry.ID || hit.ClientIP != "203.0.113.7" || hit.Path != "/adapter-service/search" ||
		hit.Action != service.BlacklistActionBlocked || hit.HitCount != 1 {
		t.Fatalf("hit row = %+v", hit)
	}
	// 记录里存的是脱敏值，不是规则原文。
	if hit.ValueMasked != "203.0.113.*" {
		t.Fatalf("hit valueMasked = %q, want 203.0.113.*", hit.ValueMasked)
	}
	if strings.Contains(hit.ValueMasked, "203.0.113.7") {
		t.Fatalf("hit row must not store the full IP: %q", hit.ValueMasked)
	}
}

func TestBlacklistMiddlewareCIDRHitAndMiss(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "198.51.100.0/24", service.BlacklistMatchCIDR, true)

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))

	for _, tc := range []struct {
		ip   string
		want int
	}{
		{"198.51.100.1:1111", http.StatusForbidden},
		{"198.51.100.254:1111", http.StatusForbidden},
		{"198.51.101.1:1111", http.StatusOK},
		{"203.0.113.1:1111", http.StatusOK},
	} {
		if response := doBlacklistRequest(router, http.MethodGet, "/adapter-service/user", tc.ip); response.Code != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.ip, response.Code, tc.want)
		}
	}
}

func TestBlacklistMiddlewareIPv6(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "2001:db8::/32", service.BlacklistMatchCIDR, true)
	seedBlacklistIPRule(t, db, "2001:db9::1", service.BlacklistMatchExact, true)

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))

	for _, tc := range []struct {
		remoteAddr string
		want       int
	}{
		{"[2001:db8::dead:beef]:443", http.StatusForbidden}, // CIDR
		{"[2001:db9::1]:443", http.StatusForbidden},         // 精确
		{"[2001:dba::1]:443", http.StatusOK},
	} {
		if response := doBlacklistRequest(router, http.MethodGet, "/adapter-service/user", tc.remoteAddr); response.Code != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.remoteAddr, response.Code, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 管理员豁免（选了「路径豁免」方案）
// ---------------------------------------------------------------------------

func TestBlacklistMiddlewareAdminAndLoginExemptPaths(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	// 拉黑 0.0.0.0/0：所有请求都在黑名单里，只有豁免路径能过。
	seedBlacklistIPRule(t, db, "0.0.0.0/0", service.BlacklistMatchCIDR, true)

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))

	// 豁免：管理接口 + **管理员专用登录入口** + 它依赖的人机验证端点。
	exempt := []string{
		"/adapter-service/admin/referral-rebates",
		"/adapter-service/admin",
		"/adapter-service/admin/userexam/inspect",
		"/adapter-service/admin-login",
		"/adapter-service/public/captcha/challenge",
		"/adapter-service/public/captcha/verify",
		"/adapter-service/public/captcha/remote-gen",
		"/adapter-service/public/captcha/remote-check",
		"/adapter-service/public/captcha/remote-token",
		// query / 结尾斜杠不能绕过豁免表。
		"/adapter-service/admin-login?next=/x",
		"/adapter-service/admin-login/",
	}
	for _, path := range exempt {
		if response := doBlacklistRequest(router, http.MethodPost, path, "203.0.113.7:1234"); response.Code != http.StatusOK {
			t.Fatalf("exempt path %s status = %d, want 200", path, response.Code)
		}
	}

	// **普通登录现在要拦**（45-*.md §0.1.1 Q63：「打开网页即显示拉黑原因，无法登录等」）。
	// 这是相对 PR #42 的行为变更：普通登录不再豁免，只留管理员专用入口。
	blocked := []string{
		"/adapter-service/user/login",
		"/adapter-service/user/login/sms",
		"/adapter-service/search",
		"/adapter-service/user/register",
		"/adapter-service/billing-records",
		"/adapter-service/user",
	}
	for _, path := range blocked {
		if response := doBlacklistRequest(router, http.MethodPost, path, "203.0.113.7:1234"); response.Code != http.StatusForbidden {
			t.Fatalf("path %s status = %d, want 403", path, response.Code)
		}
	}
}

func TestBlacklistExemptPathTable(t *testing.T) {
	cases := map[string]bool{
		// 管理接口。
		"/adapter-service/admin/referral-rebates":    true,
		"/adapter-service/admin/review-rebate/rules": true,
		"/adapter-service/admin":                     true,
		"/adapter-service/admin/userexam/tool":       true,
		// 管理员专用登录入口（Q63）：API 与前端路由都豁免。
		"/adapter-service/admin-login":     true,
		"/adapter-service/admin-login?x=1": true,
		"/adapter-service/admin-login/":    true,
		"/admin-login":                     true,
		"/admin-login/":                    true,
		// 人机验证端点必须可达，否则管理员卡在验证码上（比进不去更难排查）。
		"/adapter-service/public/captcha/challenge":    true,
		"/adapter-service/public/captcha/verify":       true,
		"/adapter-service/public/captcha/remote-gen":   true,
		"/adapter-service/public/captcha/remote-check": true,
		"/adapter-service/public/captcha/remote-token": true,

		// **普通登录不再豁免**（Q63 行为变更）。
		"/adapter-service/user/login":     false,
		"/adapter-service/user/login/sms": false,
		"/adapter-service/search":         false,
		"/adapter-service/user/register":  false,
		"/adapter-service/user/logout":    false,
		// 其他公开接口不豁免（只有验证码例外）。
		"/adapter-service/public/sms-code":        false,
		"/adapter-service/public/register-status": false,
		// 前缀陷阱：administrator / admin-login-extra 都不是豁免路径。
		"/adapter-service/administrator":     false,
		"/adapter-service/admin-login-extra": false,
		"/sqp/anything":                      false,
		"/":                                  false,
		"":                                   false,
		"  ":                                 false,
	}
	for path, want := range cases {
		if got := BlacklistExemptPath(path); got != want {
			t.Fatalf("BlacklistExemptPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// fail-open：缓存不可用时放行 + 留痕
// ---------------------------------------------------------------------------

// fail-open 的核心证据：开关为开、但规则快照拿不到（数据库抖动/尚未加载）
// → **放行**（不是拦住所有人），并且必须留痕：
//   - 打一条 SysWarn（fail-open 不能静默）
//   - 写一条 action='logged_only' 的命中记录（EntryID=0，表示「不知道命中哪条规则」）
func TestBlacklistMiddlewareFailOpenWhenRulesUnavailable(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	// 开关为开、规则表里有能命中的规则 —— 但缓存拿不到它（模拟刷新失败后的降级态）。
	seedBlacklistIPRule(t, db, "0.0.0.0/0", service.BlacklistMatchCIDR, true)
	cache := service.NewBlacklistCacheWithSwitch(true)
	if !cache.Enabled() {
		t.Fatal("precondition: fail-open probe cache must report enabled")
	}
	if cache.Snapshot() != nil {
		t.Fatal("precondition: fail-open probe cache must have no snapshot")
	}

	// 捕获 SysWarn 输出：fail-open 必须留痕，不能静默。
	var logBuf strings.Builder
	oldOut, oldErr := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &logBuf, &logBuf
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldOut, oldErr })

	recorder := service.NewBlacklistHitRecorder()
	router := newBlacklistTestRouter(BlacklistIPWith(cache, recorder, db))
	response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open must let the request through)", response.Code)
	}
	if !strings.Contains(logBuf.String(), "fail-open") {
		t.Fatalf("fail-open must log SysWarn, log=%q", logBuf.String())
	}

	var hits []entity.BlacklistHit
	if err := db.Find(&hits).Error; err != nil {
		t.Fatalf("load hits: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hit rows = %d, want 1 logged_only row", len(hits))
	}
	hit := hits[0]
	if hit.Action != service.BlacklistActionLoggedOnly {
		t.Fatalf("action = %q, want logged_only", hit.Action)
	}
	if hit.EntryID != 0 {
		t.Fatalf("entry id = %d, want 0 (unknown rule)", hit.EntryID)
	}
	if hit.ClientIP != "203.0.113.7" || hit.Path != "/adapter-service/search" {
		t.Fatalf("logged_only row lost context: %+v", hit)
	}
	// 留痕值同样脱敏。
	if hit.ValueMasked != "203.0.113.*" {
		t.Fatalf("logged_only valueMasked = %q, want 203.0.113.*", hit.ValueMasked)
	}
}

// 缓存刷新失败（DB 关闭）后仍无快照 → 开关读不到 → 零行为放行（不拦、不写日志）。
// 这与上一条的区别：这里连「开关为开」都不知道，因此按「关闭」处理（默认 false）。
func TestBlacklistMiddlewareStaysDisabledWhenReloadNeverSucceeded(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "0.0.0.0/0", service.BlacklistMatchCIDR, true)

	cache := service.NewBlacklistCache()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close sql db: %v", err)
	}
	if reloadErr := cache.Reload(db); reloadErr == nil {
		t.Fatal("expected reload to fail with a closed database")
	}
	if cache.Enabled() {
		t.Fatal("cache without a snapshot must report disabled (zero behavior)")
	}

	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))
	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234"); response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when the switch cannot be read", response.Code)
	}
}

// nil 缓存（未装配）→ 零行为放行。
func TestBlacklistMiddlewareNilCacheIsZeroBehavior(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "0.0.0.0/0", service.BlacklistMatchCIDR, true)

	router := newBlacklistTestRouter(BlacklistIPWith(nil, service.NewBlacklistHitRecorder(), db))
	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234"); response.Code != http.StatusOK {
		t.Fatalf("nil cache status = %d, want 200 (fail-open)", response.Code)
	}
	if rows, _ := countBlacklistHits(t, db); rows != 0 {
		t.Fatalf("nil cache must not write hit rows, got %d", rows)
	}
}

// 写命中日志失败不能影响「命中就 403」的结论，也不能 panic。
func TestBlacklistMiddlewareHitStillBlocksWhenLogWriteFails(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "203.0.113.7", service.BlacklistMatchExact, true)

	cache := loadBlacklistCache(t, db)
	// 传一个没有 blacklist_hits 表的库给 recorder：写日志必然失败。
	loglessDB, err := gorm.Open(sqlite.Open("file:blacklist-mw-nolog-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open logless db: %v", err)
	}
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), loglessDB))
	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234"); response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 even when the hit log cannot be written", response.Code)
	}
}

// ---------------------------------------------------------------------------
// 命中日志聚合：刷 100 次只写 1 行
// ---------------------------------------------------------------------------

// 这是验收判据「被刷 1000 次 → blacklist_hits 行数 << 1000」的中间件级证据。
func TestBlacklistMiddlewareHitAggregationWritesOneRowForManyRequests(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "203.0.113.7", service.BlacklistMatchExact, true)

	cache := loadBlacklistCache(t, db)
	recorder := service.NewBlacklistHitRecorder()
	router := newBlacklistTestRouter(BlacklistIPWith(cache, recorder, db))

	const requests = 100
	for i := 0; i < requests; i++ {
		response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234")
		if response.Code != http.StatusForbidden {
			t.Fatalf("request #%d status = %d, want 403", i, response.Code)
		}
	}

	rows, total := countBlacklistHits(t, db)
	if rows != 1 {
		t.Fatalf("hit rows = %d, want 1 for %d blocked requests", rows, requests)
	}
	if total != requests {
		t.Fatalf("sum(hit_count) = %d, want %d", total, requests)
	}
	var hit entity.BlacklistHit
	if err := db.First(&hit).Error; err != nil {
		t.Fatalf("load hit: %v", err)
	}
	if hit.HitCount != requests {
		t.Fatalf("hit_count = %d, want %d", hit.HitCount, requests)
	}
}

// 不同 IP / 不同规则各自成行，聚合不会把无关命中并在一起。
func TestBlacklistMiddlewareAggregationKeepsDistinctIPsSeparate(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "198.51.100.0/24", service.BlacklistMatchCIDR, true)

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))

	for i := 0; i < 5; i++ {
		doBlacklistRequest(router, http.MethodGet, "/adapter-service/user", "198.51.100.1:1")
		doBlacklistRequest(router, http.MethodGet, "/adapter-service/user", "198.51.100.2:1")
	}
	rows, total := countBlacklistHits(t, db)
	if rows != 2 || total != 10 {
		t.Fatalf("rows=%d total=%d, want rows=2 total=10", rows, total)
	}
}

// 停用规则后立即不拦（缓存刷新）。
func TestBlacklistMiddlewareDisabledRuleStopsBlockingAfterReload(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	entry := seedBlacklistIPRule(t, db, "203.0.113.7", service.BlacklistMatchExact, true)

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))
	if response := doBlacklistRequest(router, http.MethodGet, "/adapter-service/user", "203.0.113.7:1"); response.Code != http.StatusForbidden {
		t.Fatalf("precondition: status = %d, want 403", response.Code)
	}

	if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entry.ID).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable rule: %v", err)
	}
	if err := cache.Invalidate(db); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if response := doBlacklistRequest(router, http.MethodGet, "/adapter-service/user", "203.0.113.7:1"); response.Code != http.StatusOK {
		t.Fatalf("disabled rule status = %d, want 200", response.Code)
	}
}

// 过期规则不拦（记录仍在）。
func TestBlacklistMiddlewareExpiredRuleDoesNotBlock(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	past := time.Now().Add(-time.Minute)
	entry := seedBlacklistIPRule(t, db, "203.0.113.7", service.BlacklistMatchExact, true)
	if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entry.ID).Update("expires_at", past).Error; err != nil {
		t.Fatalf("set expiry: %v", err)
	}

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))
	if response := doBlacklistRequest(router, http.MethodGet, "/adapter-service/user", "203.0.113.7:1"); response.Code != http.StatusOK {
		t.Fatalf("expired rule status = %d, want 200", response.Code)
	}
	// 记录保留：运营还能在列表里看到它。
	var count int64
	if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entry.ID).Count(&count).Error; err != nil {
		t.Fatalf("count rule: %v", err)
	}
	if count != 1 {
		t.Fatal("expired rule must stay in the table")
	}
}
