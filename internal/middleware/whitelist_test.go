package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newWhitelistMiddlewareTestDB 建一个隔离的测试库（含黑/白名单两表 + system_settings）。
func newWhitelistMiddlewareTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:whitelist-mw-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.BlacklistEntry{},
		&entity.BlacklistHit{}, &entity.WhitelistEntry{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func setWhitelistMiddlewareSwitch(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()
	value := "false"
	if enabled {
		value = "true"
	}
	if err := db.Where(map[string]any{"key": service.BlacklistEnabledSettingKey}).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear switch: %v", err)
	}
	if err := db.Create(&entity.SystemSetting{
		Key: service.BlacklistEnabledSettingKey, Value: value, Remark: "启用黑名单（默认关闭）",
	}).Error; err != nil {
		t.Fatalf("set switch: %v", err)
	}
}

// seedBlacklistRuleForWhitelistTest 落一条**启用**的 IP 黑名单规则。
func seedBlacklistRuleForWhitelistTest(t *testing.T, db *gorm.DB, pattern string, matchType string) entity.BlacklistEntry {
	t.Helper()
	if matchType == "" {
		matchType = service.BlacklistMatchExact
	}
	normalized, err := service.NormalizeBlacklistValue(service.BlacklistKindIP, pattern, matchType)
	if err != nil {
		t.Fatalf("normalize blacklist %q: %v", pattern, err)
	}
	entry := entity.BlacklistEntry{
		Kind: normalized.Kind, Pattern: normalized.Pattern, MatchType: normalized.MatchType,
		ValueHash: normalized.ValueHash, ValueMasked: normalized.ValueMasked,
		Reason: "中间件测试", Enabled: true,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create blacklist rule: %v", err)
	}
	return entry
}

// seedWhitelistRuleForMWTest 落一条**启用**的白名单规则。
//
// matchType 传空时由 NormalizeWhitelistValue 自动判定（含 "/" → cidr）。
// 注意：**不能像黑名单 helper 那样把空值兜成 exact**，否则 "203.0.113.0/24"
// 会被当成 exact 去 ParseIP 而报错（本文件踩过一次）。
func seedWhitelistRuleForMWTest(t *testing.T, db *gorm.DB, pattern string, matchType string) entity.WhitelistEntry {
	t.Helper()
	normalized, err := service.NormalizeWhitelistValue(service.WhitelistKindIP, pattern, matchType)
	if err != nil {
		t.Fatalf("normalize whitelist %q: %v", pattern, err)
	}
	entry := entity.WhitelistEntry{
		Kind: normalized.Kind, Pattern: normalized.Pattern, MatchType: normalized.MatchType,
		ValueHash: normalized.ValueHash, ValueMasked: normalized.ValueMasked,
		Reason: "中间件测试", Enabled: true,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create whitelist rule: %v", err)
	}
	return entry
}

func setWhitelistMiddlewareExemptIPs(t *testing.T, db *gorm.DB, value string) {
	t.Helper()
	if err := db.Where(map[string]any{"key": service.WhitelistExemptIPsSettingKey}).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear exempt ips: %v", err)
	}
	if err := db.Create(&entity.SystemSetting{
		Key: service.WhitelistExemptIPsSettingKey, Value: value, Remark: "按 IP 白名单",
	}).Error; err != nil {
		t.Fatalf("set exempt ips: %v", err)
	}
}

// newWhitelistTestRouter 建一个最小路由：中间件 + 一个永远返回 200 的 handler。
func newWhitelistTestRouter(mw gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(mw)
	router.Any("/*path", func(c *gin.Context) {
		source, _ := c.Get(WhitelistContextKey)
		c.JSON(http.StatusOK, gin.H{"message": "ok", "whitelistSource": source})
	})
	return router
}

func loadWhitelistCache(t *testing.T, db *gorm.DB) *service.WhitelistCache {
	t.Helper()
	cache := service.NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload whitelist cache: %v", err)
	}
	return cache
}

func loadBlacklistCacheForWhitelistTest(t *testing.T, db *gorm.DB) *service.BlacklistCache {
	t.Helper()
	cache := service.NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload blacklist cache: %v", err)
	}
	return cache
}

// whitelistTestStack 把「三个缓存 + 聚合器 + 路由」一次装好，避免每个用例重复六行。
type whitelistTestStack struct {
	db        *gorm.DB
	whitelist *service.WhitelistCache
	blacklist *service.BlacklistCache
	recorder  *service.BlacklistHitRecorder
	router    *gin.Engine
}

func newWhitelistTestStack(t *testing.T, blacklistEnabled bool) *whitelistTestStack {
	t.Helper()
	db := newWhitelistMiddlewareTestDB(t)
	if blacklistEnabled {
		setWhitelistMiddlewareSwitch(t, db, true)
	}
	whitelistCache := loadWhitelistCache(t, db)
	blacklistCache := loadBlacklistCacheForWhitelistTest(t, db)
	recorder := service.NewBlacklistHitRecorder()
	return &whitelistTestStack{
		db:        db,
		whitelist: whitelistCache,
		blacklist: blacklistCache,
		recorder:  recorder,
		router: newWhitelistTestRouter(
			WhitelistIPWith(whitelistCache, blacklistCache, recorder, db)),
	}
}

// reload 刷新两个缓存（写完规则后用）。
func (s *whitelistTestStack) reload(t *testing.T) {
	t.Helper()
	if err := s.whitelist.Reload(s.db); err != nil {
		t.Fatalf("reload whitelist: %v", err)
	}
	if err := s.blacklist.Reload(s.db); err != nil {
		t.Fatalf("reload blacklist: %v", err)
	}
}

func (s *whitelistTestStack) do(method, path, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	recorder := httptest.NewRecorder()
	s.router.ServeHTTP(recorder, req)
	return recorder
}

// countBlacklistHitRows 返回 blacklist_hits 的行数与 hit_count 合计。
func countBlacklistHitRows(t *testing.T, db *gorm.DB) (rows int64, total int64) {
	t.Helper()
	if err := db.Model(&entity.BlacklistHit{}).Count(&rows).Error; err != nil {
		t.Fatalf("count hits: %v", err)
	}
	var sum struct {
		Total int64 `gorm:"column:total"`
	}
	if err := db.Model(&entity.BlacklistHit{}).Select("COALESCE(SUM(hit_count),0) AS total").
		Scan(&sum).Error; err != nil {
		t.Fatalf("sum hit_count: %v", err)
	}
	return rows, sum.Total
}

// ---------------------------------------------------------------------------
// ★ 最关键的一条判据（本项存在的理由）
//
// 优先级必须是「白名单命中 → 直接放行，完全不查黑名单」，
// 不是「两条都查再取反」。
//
// 这组用例同时钉两件事：
//   1. 同一 IP 同时在黑白名单 → 放行（不是 403）；
//   2. 命中白名单时**一行 blacklist_hits 都不写**（判据①，不能只是不拦还要不留痕）。
//
// 变异靶点：把白名单判定移到黑名单之后（例如改成先跑 blacklist、未命中才判白名单），
// 用例 1 与 2 都必须变红。
// ---------------------------------------------------------------------------

func TestWhitelistMiddlewareSameIPInBothListsIsAllowed(t *testing.T) {
	stack := newWhitelistTestStack(t, true)
	// 同一个 IP 同时在两张表里 —— 这是唯一能区分「白优先」与「两条都查」的构造。
	seedBlacklistRuleForWhitelistTest(t, stack.db, "203.0.113.7", service.BlacklistMatchExact)
	seedWhitelistRuleForMWTest(t, stack.db, "203.0.113.7", service.WhitelistMatchExact)
	stack.reload(t)

	response := stack.do(http.MethodPost, "/api/search", "203.0.113.7:12345")

	// ⚠️ 顺序刻意如此：**先断「零命中行」，再断状态码**。
	//
	// 若先断状态码，一个「白名单命中但仍调用黑名单闭包」的坏实现会先被 403 抓住，
	// 于是判据①（不留痕）这条断言根本没机会执行 —— 看起来覆盖了，实际从没跑到。
	// 先断命中行，坏实现无论拦不拦都会在「写了行」这一步先红，
	// 判据① 才是独立成立的钉子（实测：把 blacklist 闭包塞进白名单分支，
	// 红的正是这一行而不是状态码那一行）。
	if rows, total := countBlacklistHitRows(t, stack.db); rows != 0 || total != 0 {
		t.Fatalf("白名单命中不得写 blacklist_hits，got rows=%d total=%d", rows, total)
	}
	// 聚合器也不能被触碰（否则「放行」仍产生副作用）。
	if slots := stack.recorder.SlotCount(); slots != 0 {
		t.Fatalf("白名单命中不得触碰命中聚合器，slots=%d", slots)
	}

	if response.Code != http.StatusOK {
		t.Fatalf("同一 IP 同时在黑白名单时必须放行，status = %d，want 200（body=%s）",
			response.Code, response.Body.String())
	}
	// 放行的来源标记必须是 entry（证明是白名单命中的，不是「两条都没查到」）。
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got := body["whitelistSource"]; got != service.WhitelistSourceEntry {
		t.Fatalf("whitelistSource = %v, want %q（必须是白名单命中，而不是两条都没查）",
			got, service.WhitelistSourceEntry)
	}
}

// 对照组：同一 IP 只在黑名单里 → 必须照旧 403，且写一条 blocked 命中行。
//
// 没有这条，上面的用例无法排除「实现把黑名单整段弄坏了所以全都 200」。
func TestWhitelistMiddlewareWithoutWhitelistHitStillBlocks(t *testing.T) {
	stack := newWhitelistTestStack(t, true)
	entry := seedBlacklistRuleForWhitelistTest(t, stack.db, "203.0.113.7", service.BlacklistMatchExact)
	stack.reload(t)

	response := stack.do(http.MethodPost, "/api/search", "203.0.113.7:12345")
	if response.Code != http.StatusForbidden {
		t.Fatalf("未命中白名单时必须仍按黑名单拦截，status = %d，want 403", response.Code)
	}
	// 响应体仍须严格（白名单没有放松黑名单的泄漏约束）。
	for _, leak := range []string{"203.0.113", "cidr", "exact", "blacklist", "whitelist"} {
		if strings.Contains(strings.ToLower(response.Body.String()), leak) {
			t.Fatalf("403 响应体不得泄漏 %q：%s", leak, response.Body.String())
		}
	}
	var hits []entity.BlacklistHit
	if err := stack.db.Find(&hits).Error; err != nil {
		t.Fatalf("load hits: %v", err)
	}
	if len(hits) != 1 || hits[0].EntryID != entry.ID || hits[0].Action != service.BlacklistActionBlocked {
		t.Fatalf("hits = %+v, want 1 条 blocked 且 entryID=%d", hits, entry.ID)
	}
}

// 白名单命中时**一次黑名单查询都不发**（不是「查了没命中」）。
//
// 用 GORM 回调计数而非只看状态码：后者无法区分「没查」与「查了没命中」。
func TestWhitelistMiddlewareHitIssuesZeroBlacklistQueries(t *testing.T) {
	stack := newWhitelistTestStack(t, true)
	seedBlacklistRuleForWhitelistTest(t, stack.db, "203.0.113.0/24", service.BlacklistMatchCIDR)
	seedWhitelistRuleForMWTest(t, stack.db, "203.0.113.7", service.WhitelistMatchExact)
	stack.reload(t) // 计数开始前的加载不计入

	var blacklistQueries int64
	register := func() {
		if err := stack.db.Callback().Query().After("gorm:query").Register("whitelist_test:count", func(tx *gorm.DB) {
			lower := strings.ToLower(tx.Statement.SQL.String())
			if strings.Contains(lower, entity.TableNameBlacklistEntry) {
				blacklistQueries++
			}
		}); err != nil {
			t.Fatalf("register callback: %v", err)
		}
	}
	register()

	for i := 0; i < 10; i++ {
		if response := stack.do(http.MethodPost, "/api/search", "203.0.113.7:1234"); response.Code != http.StatusOK {
			t.Fatalf("request #%d status = %d, want 200", i, response.Code)
		}
	}
	if blacklistQueries != 0 {
		t.Fatalf("白名单命中时不得发起 blacklist_entries 查询，got %d", blacklistQueries)
	}
}

// 白名单 CIDR / v4-mapped / exempt_ips 三条命中的入口都要能放行。
func TestWhitelistMiddlewareHitSources(t *testing.T) {
	cases := []struct {
		name       string
		whitelist  []string // 白名单表规则
		exemptIPs  string
		remoteAddr string
		wantSource string
	}{
		{"exact", []string{"203.0.113.7"}, "", "203.0.113.7:1234", service.WhitelistSourceEntry},
		// v4-mapped 请求：IPv6 字面量必须用方括号包住，否则 net.SplitHostPort 无法
		// 区分「最后一段是端口还是地址的一部分」，gin 会拿到空 host。
		{"exact v4-mapped request", []string{"203.0.113.7"}, "", "[::ffff:203.0.113.7]:1234", service.WhitelistSourceEntry},
		{"cidr", []string{"203.0.113.0/24"}, "", "203.0.113.99:1234", service.WhitelistSourceEntry},
		{"exempt ips single", nil, "203.0.113.7", "203.0.113.7:1234", service.WhitelistSourceExemptIPs},
		{"exempt ips cidr", nil, "203.0.113.0/24", "203.0.113.99:1234", service.WhitelistSourceExemptIPs},
		{"exempt ips v4-mapped", nil, "203.0.113.7", "[::ffff:203.0.113.7]:1234", service.WhitelistSourceExemptIPs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stack := newWhitelistTestStack(t, true)
			// 黑名单故意覆盖「全部地址」：任何一条放过都只能是白名单造成的。
			seedBlacklistRuleForWhitelistTest(t, stack.db, "0.0.0.0/0", service.BlacklistMatchCIDR)
			for _, pattern := range tc.whitelist {
				seedWhitelistRuleForMWTest(t, stack.db, pattern, "")
			}
			if tc.exemptIPs != "" {
				setWhitelistMiddlewareExemptIPs(t, stack.db, tc.exemptIPs)
			}
			stack.reload(t)

			response := stack.do(http.MethodPost, "/api/search", tc.remoteAddr)
			if response.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200（body=%s）", tc.name, response.Code, response.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if got := body["whitelistSource"]; got != tc.wantSource {
				t.Fatalf("whitelistSource = %v, want %q", got, tc.wantSource)
			}
			if rows, _ := countBlacklistHitRows(t, stack.db); rows != 0 {
				t.Fatalf("白名单命中不得写命中行，got %d", rows)
			}
		})
	}
}

// 未命中白名单的 IP 照旧被 0.0.0.0/0 拦下（对照组的另一种形态）。
func TestWhitelistMiddlewareMissFallsThroughToBlacklist(t *testing.T) {
	stack := newWhitelistTestStack(t, true)
	seedBlacklistRuleForWhitelistTest(t, stack.db, "0.0.0.0/0", service.BlacklistMatchCIDR)
	seedWhitelistRuleForMWTest(t, stack.db, "203.0.113.7", service.WhitelistMatchExact)
	stack.reload(t)

	if response := stack.do(http.MethodPost, "/api/search", "198.51.100.1:1234"); response.Code != http.StatusForbidden {
		t.Fatalf("白名单未命中的 IP status = %d, want 403", response.Code)
	}
}

// ---------------------------------------------------------------------------
// 「配置读不出来 → 全站行为与未引入白名单时一致」的两条：黑名单照旧工作
// ---------------------------------------------------------------------------

// 黑名单开关关闭时，白名单未命中的请求仍然 200 且零写日志（黑名单原有行为不变）。
func TestWhitelistMiddlewareKeepsBlacklistSwitchOffZeroBehavior(t *testing.T) {
	stack := newWhitelistTestStack(t, false) // blacklist_enabled 缺记录 = false
	seedBlacklistRuleForWhitelistTest(t, stack.db, "203.0.113.7", service.BlacklistMatchExact)
	seedWhitelistRuleForMWTest(t, stack.db, "198.51.100.9", service.WhitelistMatchExact)
	stack.reload(t)

	if response := stack.do(http.MethodPost, "/api/search", "203.0.113.7:1234"); response.Code != http.StatusOK {
		t.Fatalf("黑名单开关关闭时 status = %d, want 200", response.Code)
	}
	if rows, total := countBlacklistHitRows(t, stack.db); rows != 0 || total != 0 {
		t.Fatalf("黑名单开关关闭时必须零写日志，got rows=%d total=%d", rows, total)
	}
	if slots := stack.recorder.SlotCount(); slots != 0 {
		t.Fatalf("黑名单开关关闭时不得触碰聚合器，slots=%d", slots)
	}
}

// 黑名单的路径豁免照旧生效（白名单中间件不得破坏它）。
func TestWhitelistMiddlewareKeepsBlacklistExemptPaths(t *testing.T) {
	stack := newWhitelistTestStack(t, true)
	seedBlacklistRuleForWhitelistTest(t, stack.db, "0.0.0.0/0", service.BlacklistMatchCIDR)
	stack.reload(t)

	for _, path := range []string{
		"/api/admin/users",
		"/api/admin-login",
		"/api/public/captcha/challenge",
	} {
		if response := stack.do(http.MethodPost, path, "203.0.113.7:1234"); response.Code != http.StatusOK {
			t.Fatalf("豁免路径 %s status = %d, want 200", path, response.Code)
		}
	}
	if rows, _ := countBlacklistHitRows(t, stack.db); rows != 0 {
		t.Fatalf("路径豁免不得写命中行，got %d", rows)
	}
}

// 白名单缓存没有快照时**不放行任何人**（与黑名单 fail-open 方向相反）。
func TestWhitelistMiddlewareWithoutSnapshotDoesNotBypass(t *testing.T) {
	db := newWhitelistMiddlewareTestDB(t)
	setWhitelistMiddlewareSwitch(t, db, true)
	seedBlacklistRuleForWhitelistTest(t, db, "203.0.113.7", service.BlacklistMatchExact)
	blacklistCache := loadBlacklistCacheForWhitelistTest(t, db)

	// 白名单缓存刻意不 Reload：Snapshot() 为 nil。
	emptyWhitelist := service.NewWhitelistCache()
	if emptyWhitelist.Snapshot() != nil {
		t.Fatal("前置条件：新建白名单缓存不应有快照")
	}
	router := newWhitelistTestRouter(
		WhitelistIPWith(emptyWhitelist, blacklistCache, service.NewBlacklistHitRecorder(), db))
	req := httptest.NewRequest(http.MethodPost, "/api/search", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("白名单无快照时不得凭空放行，status = %d，want 403", recorder.Code)
	}
}

// 白名单缓存为 nil（装配缺失）时也按「零白名单」处理，不能 panic。
func TestWhitelistMiddlewareNilCacheIsZeroBehavior(t *testing.T) {
	db := newWhitelistMiddlewareTestDB(t)
	setWhitelistMiddlewareSwitch(t, db, true)
	seedBlacklistRuleForWhitelistTest(t, db, "203.0.113.7", service.BlacklistMatchExact)
	blacklistCache := loadBlacklistCacheForWhitelistTest(t, db)

	router := newWhitelistTestRouter(
		WhitelistIPWith(nil, blacklistCache, service.NewBlacklistHitRecorder(), db))
	req := httptest.NewRequest(http.MethodPost, "/api/search", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("nil 白名单缓存 status = %d, want 403（黑名单照旧生效）", recorder.Code)
	}
}

// 停用 / 过期的白名单规则不放行（刷新后立刻失效）。
func TestWhitelistMiddlewareDisabledOrExpiredRuleDoesNotBypass(t *testing.T) {
	stack := newWhitelistTestStack(t, true)
	seedBlacklistRuleForWhitelistTest(t, stack.db, "0.0.0.0/0", service.BlacklistMatchCIDR)

	disabled := seedWhitelistRuleForMWTest(t, stack.db, "203.0.113.7", service.WhitelistMatchExact)
	if err := stack.db.Model(&entity.WhitelistEntry{}).Where("id = ?", disabled.ID).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	expired := seedWhitelistRuleForMWTest(t, stack.db, "203.0.113.8", service.WhitelistMatchExact)
	if err := stack.db.Model(&entity.WhitelistEntry{}).Where("id = ?", expired.ID).
		Update("expires_at", past).Error; err != nil {
		t.Fatalf("expire: %v", err)
	}
	seedWhitelistRuleForMWTest(t, stack.db, "203.0.113.9", service.WhitelistMatchExact)
	stack.reload(t)

	for _, tc := range []struct {
		ip   string
		want int
	}{
		{"203.0.113.7", http.StatusForbidden}, // 停用 → 不放行
		{"203.0.113.8", http.StatusForbidden}, // 过期 → 不放行
		{"203.0.113.9", http.StatusOK},        // 有效 → 放行
	} {
		if response := stack.do(http.MethodPost, "/api/search", tc.ip+":1234"); response.Code != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.ip, response.Code, tc.want)
		}
	}
}

// 白名单删除后立刻不再放行（管理页写路径的刷新语义）。
func TestWhitelistMiddlewareDeletedRuleStopsBypassing(t *testing.T) {
	stack := newWhitelistTestStack(t, true)
	seedBlacklistRuleForWhitelistTest(t, stack.db, "0.0.0.0/0", service.BlacklistMatchCIDR)
	entry := seedWhitelistRuleForMWTest(t, stack.db, "203.0.113.7", service.WhitelistMatchExact)
	stack.reload(t)

	if response := stack.do(http.MethodPost, "/api/search", "203.0.113.7:1234"); response.Code != http.StatusOK {
		t.Fatalf("删除前应放行，status = %d", response.Code)
	}
	if err := stack.db.Delete(&entity.WhitelistEntry{}, entry.ID).Error; err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	stack.reload(t)
	if response := stack.do(http.MethodPost, "/api/search", "203.0.113.7:1234"); response.Code != http.StatusForbidden {
		t.Fatalf("删除后不应再放行，status = %d，want 403", response.Code)
	}
}
