package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ---------------------------------------------------------------------------
// BL-4：地区黑名单接入中间件（IP 判定之后、同一条 fail-open 骨架）
// ---------------------------------------------------------------------------

// newBlacklistRegionTestDB 建一个含归属地缓存的隔离库。
//
// 与 newBlacklistMiddlewareTestDB 分开命名（库名前缀不同）：
// SQLite 的 `file:...?mode=memory&cache=shared` 在同一进程内同名库是共享的，
// 跨用例会互相污染；而这些用例需要多一张 location_cache_entries 表。
func newBlacklistRegionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:blacklist-mw-region-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.BlacklistEntry{}, &entity.BlacklistHit{}, &entity.LocationCache{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// seedBlacklistRegionRule 落一条地区规则（走唯一入口 CreateBlacklistEntryWithDB）。
func seedBlacklistRegionRule(t *testing.T, db *gorm.DB, pattern string, reason string) entity.BlacklistEntry {
	t.Helper()
	entry, err := service.CreateBlacklistEntryWithDB(db, service.CreateBlacklistEntryInput{
		Kind: service.BlacklistKindRegion, Pattern: pattern, Reason: reason, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create region rule %q: %v", pattern, err)
	}
	return entry
}

// seedIPLocationRow 落一条**已缓存**的 IP 归属地（模拟管理员在归属地页面查过一次）。
//
// 判定路径只读这张表，因此种下它之后整条链路零上游调用 ——
// 这正是 46-*.md §4/§7「地区规则命中时不产生 UApiPro 计费（只读缓存，不主动查询）」。
func seedIPLocationRow(t *testing.T, db *gorm.DB, ip string, region string, llc string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"ip": ip, "region": region, "llc": llc, "isp": llc})
	if err != nil {
		t.Fatalf("marshal raw json: %v", err)
	}
	if err := db.Create(&entity.LocationCache{
		Kind: entity.LocationKindIP, LookupKey: strings.ToLower(ip), Source: "",
		Address: strings.TrimSpace(region + " " + llc), RawJSON: string(raw),
	}).Error; err != nil {
		t.Fatalf("seed location row %q: %v", ip, err)
	}
}

// countingRegionLooker 包一层计数：用来证明「开关关闭 / 无地区规则」时一次归属地读都没有。
func countingRegionLooker(db *gorm.DB, counter *atomic.Int64) service.BlacklistRegionLooker {
	inner := service.NewBlacklistRegionCacheLooker(db)
	return func(clientIP string) (service.BlacklistRegionLocation, bool) {
		counter.Add(1)
		return inner(clientIP)
	}
}

func loadRegionMiddlewareCache(t *testing.T, db *gorm.DB) *service.BlacklistCache {
	t.Helper()
	cache := service.NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload cache: %v", err)
	}
	return cache
}

// 地区规则命中 → 403 JSON（固定文案）+ 一条 action='blocked' 的命中记录，
// 且请求被拦在业务 handler 之前。
func TestBlacklistMiddlewareRegionHitBlocksRequest(t *testing.T) {
	db := newBlacklistRegionTestDB(t)
	setBlacklistSwitch(t, db, true)
	rule := seedBlacklistRegionRule(t, db, "云南", "云南地区维护")
	seedIPLocationRow(t, db, "203.0.113.7", "中国 云南 玉溪", "移动")

	cache := loadRegionMiddlewareCache(t, db)
	recorder := service.NewBlacklistHitRecorder()
	router := newBlacklistTestRouter(BlacklistIPWith(cache, recorder, db))

	response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234")
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
	// 响应体不得泄漏规则细节（46-*.md §3.2）：不能出现省名、reason、entry id。
	body := response.Body.String()
	for _, leaked := range []string{"云南", "维护", rule.ValueMasked, "region"} {
		if leaked != "" && strings.Contains(body, leaked) {
			t.Fatalf("response body must not leak %q, body=%q", leaked, body)
		}
	}

	var hits []entity.BlacklistHit
	if err := db.Find(&hits).Error; err != nil {
		t.Fatalf("load hits: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hit rows = %d, want 1", len(hits))
	}
	hit := hits[0]
	if hit.Action != service.BlacklistActionBlocked {
		t.Fatalf("action = %q, want blocked", hit.Action)
	}
	if hit.Kind != service.BlacklistKindRegion {
		t.Fatalf("kind = %q, want region", hit.Kind)
	}
	if hit.EntryID != rule.ID {
		t.Fatalf("entry id = %d, want %d (the region rule)", hit.EntryID, rule.ID)
	}
	if hit.ClientIP != "203.0.113.7" || hit.Path != "/adapter-service/search" {
		t.Fatalf("hit row lost context: %+v", hit)
	}
	if hit.ValueMasked == "" {
		t.Fatal("hit row must carry the masked region value")
	}
}

// 开关关闭（默认）时：库里有地区规则 + 归属地缓存已经存在，仍然
// **一次归属地读都没有**、请求放行、零命中日志。
//
// 这是 §7「开关默认 false → 全站行为与今天完全一致（零额外查询）」在地区链路上的落点。
func TestBlacklistMiddlewareRegionSwitchOffIssuesZeroLookups(t *testing.T) {
	db := newBlacklistRegionTestDB(t)
	// 刻意不写 blacklist_enabled（缺记录 = 默认 false）。
	seedBlacklistRegionRule(t, db, "云南", "地区维护")
	seedIPLocationRow(t, db, "203.0.113.7", "中国 云南 玉溪", "移动")

	cache := loadRegionMiddlewareCache(t, db)
	if cache.Enabled() {
		t.Fatal("precondition: missing blacklist_enabled must load as disabled")
	}
	var lookups atomic.Int64
	router := newBlacklistTestRouter(BlacklistIPWithRegionLooker(
		cache, service.NewBlacklistHitRecorder(), db, countingRegionLooker(db, &lookups)))

	for i := 0; i < 10; i++ {
		if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234"); response.Code != http.StatusOK {
			t.Fatalf("request #%d status = %d, want 200 with the switch off", i, response.Code)
		}
	}
	if got := lookups.Load(); got != 0 {
		t.Fatalf("switch off must issue zero location lookups, got %d", got)
	}
	if rows, total := countBlacklistHits(t, db); rows != 0 || total != 0 {
		t.Fatalf("switch off must write zero hit rows, got rows=%d total=%d", rows, total)
	}
}

// 开关打开但**没有地区规则**时同样零归属地读：
// 只开 IP 黑名单的部署不该为每个请求多付一次 location_cache_entries 读。
func TestBlacklistMiddlewareRegionWithoutRulesIssuesZeroLookups(t *testing.T) {
	db := newBlacklistRegionTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "203.0.113.7", service.BlacklistMatchExact, true)
	seedIPLocationRow(t, db, "198.51.100.9", "中国 云南 玉溪", "移动")

	cache := loadRegionMiddlewareCache(t, db)
	var lookups atomic.Int64
	router := newBlacklistTestRouter(BlacklistIPWithRegionLooker(
		cache, service.NewBlacklistHitRecorder(), db, countingRegionLooker(db, &lookups)))

	// 这条 IP 不在 IP 规则里、但**有**归属地缓存；没有地区规则 → 不该去读。
	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "198.51.100.9:1234"); response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := lookups.Load(); got != 0 {
		t.Fatalf("no region rules must mean zero location lookups, got %d", got)
	}
}

// IP 规则先判：IP 已命中时不再读归属地（省掉一次白读）。
func TestBlacklistMiddlewareIPHitShortCircuitsRegionLookup(t *testing.T) {
	db := newBlacklistRegionTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "203.0.113.7", service.BlacklistMatchExact, true)
	seedBlacklistRegionRule(t, db, "云南", "地区维护")
	seedIPLocationRow(t, db, "203.0.113.7", "中国 云南 玉溪", "移动")

	cache := loadRegionMiddlewareCache(t, db)
	var lookups atomic.Int64
	router := newBlacklistTestRouter(BlacklistIPWithRegionLooker(
		cache, service.NewBlacklistHitRecorder(), db, countingRegionLooker(db, &lookups)))

	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234"); response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.Code)
	}
	if got := lookups.Load(); got != 0 {
		t.Fatalf("an IP hit must short-circuit the region lookup, got %d lookups", got)
	}
}

// 开关为开、快照不可用 → fail-open：放行 + SysWarn + logged_only 留痕。
//
// 与 IP 那条 fail-open 用例守同一性质，但走的是地区分支 —— 两个分支各有一次
// blacklistFailOpen 调用，必须都被覆盖（否则「地区分支漏留痕」测不出来）。
func TestBlacklistMiddlewareRegionFailOpenWhenSnapshotMissing(t *testing.T) {
	db := newBlacklistRegionTestDB(t)
	cache := service.NewBlacklistCacheWithSwitch(true)
	if !cache.Enabled() || cache.Snapshot() != nil {
		t.Fatal("precondition: fail-open probe cache must report enabled without a snapshot")
	}
	var lookups atomic.Int64
	var logBuf strings.Builder
	oldOut, oldErr := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &logBuf, &logBuf
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldOut, oldErr })

	router := newBlacklistTestRouter(BlacklistIPWithRegionLooker(
		cache, service.NewBlacklistHitRecorder(), db, countingRegionLooker(db, &lookups)))

	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234"); response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open must let the request through)", response.Code)
	}
	if !strings.Contains(logBuf.String(), "fail-open") {
		t.Fatalf("fail-open must log SysWarn, log=%q", logBuf.String())
	}
	if got := lookups.Load(); got != 0 {
		t.Fatalf("fail-open must not read locations, got %d lookups", got)
	}

	var hits []entity.BlacklistHit
	if err := db.Find(&hits).Error; err != nil {
		t.Fatalf("load hits: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hit rows = %d, want 1 logged_only row", len(hits))
	}
	if hits[0].Action != service.BlacklistActionLoggedOnly || hits[0].EntryID != 0 {
		t.Fatalf("fail-open row = %+v, want logged_only with entry id 0", hits[0])
	}
	if hits[0].Path != "/adapter-service/search" || hits[0].ClientIP != "203.0.113.7" {
		t.Fatalf("fail-open row lost context: %+v", hits[0])
	}
}

// 管理员入口豁免优先于地区判定：即使该 IP 在云南、规则也拉黑了云南，
// /adapter-service/admin-login 仍必须可达（否则管理员自锁，46-*.md §3.3）。
func TestBlacklistMiddlewareRegionExemptsAdminLoginPath(t *testing.T) {
	db := newBlacklistRegionTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistRegionRule(t, db, "云南", "地区维护")
	seedIPLocationRow(t, db, "203.0.113.7", "中国 云南 玉溪", "移动")

	cache := loadRegionMiddlewareCache(t, db)
	var lookups atomic.Int64
	router := newBlacklistTestRouter(BlacklistIPWithRegionLooker(
		cache, service.NewBlacklistHitRecorder(), db, countingRegionLooker(db, &lookups)))

	for _, path := range []string{"/adapter-service/admin-login", "/adapter-service/admin/blacklist/entries", "/admin-login"} {
		if response := doBlacklistRequest(router, http.MethodPost, path, "203.0.113.7:1234"); response.Code != http.StatusOK {
			t.Fatalf("path %q status = %d, want 200 (admin paths must be exempt from the region rule)", path, response.Code)
		}
	}
	if got := lookups.Load(); got != 0 {
		t.Fatalf("exempt paths must not read locations, got %d lookups", got)
	}
	// 非豁免路径仍被拦 —— 否则上一条断言只是「什么都没生效」。
	if response := doBlacklistRequest(router, http.MethodPost, "/adapter-service/search", "203.0.113.7:1234"); response.Code != http.StatusForbidden {
		t.Fatalf("non-exempt path status = %d, want 403", response.Code)
	}
}

// 浏览器打开网页时命中地区规则 → 拉黑**页面**（含 reason + 管理员登录入口），
// 与 IP 命中的分叉口径完全一致。
func TestBlacklistMiddlewareRegionHitRendersHTMLPage(t *testing.T) {
	db := newBlacklistRegionTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistRegionRule(t, db, "云南", "云南地区维护")
	seedIPLocationRow(t, db, "203.0.113.7", "中国 云南 玉溪", "移动")

	cache := loadRegionMiddlewareCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "云南地区维护") {
		t.Fatalf("the blocked page must show the rule reason, body=%q", body)
	}
	if !strings.Contains(body, AdminLoginPagePath) {
		t.Fatalf("the blocked page must offer the admin login entry, body=%q", body)
	}
}
