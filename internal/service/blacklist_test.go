package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newBlacklistTestDB 建一个隔离的黑名单测试库。
//
// 库名带 t.Name()：SQLite 的 file:...?mode=memory&cache=shared 在同一进程内
// 同名库是共享的，跨用例会互相污染（AGENTS.md 第 9 条）。
func newBlacklistTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:blacklist-"+name+"?mode=memory&cache=shared"), &gorm.Config{
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

func setBlacklistEnabled(t *testing.T, db *gorm.DB, value string) {
	t.Helper()
	if err := saveSystemSettingValue(db, BlacklistEnabledSettingKey, value, "启用黑名单（默认关闭）"); err != nil {
		t.Fatalf("set %s=%q: %v", BlacklistEnabledSettingKey, value, err)
	}
}

// seedBlacklistEntry 落一条规则，并**保证 Enabled 与入参一致**。
//
// 为什么是两步：GORM 对带 `default:true` 标签的字段会把零值从 INSERT 里省略
// （连 Select("*") 也绕不过，见 entity/blacklist.go 的注释），
// 于是「直接 Create 一条 Enabled=false 的规则」会被静默写成 true。
// 正确写法是 Create 之后 Update，与 BL-5 管理页必须遵守的写法一致。
func seedBlacklistEntry(t *testing.T, db *gorm.DB, entry entity.BlacklistEntry) entity.BlacklistEntry {
	t.Helper()
	if entry.Kind == "" {
		entry.Kind = BlacklistKindIP
	}
	if entry.MatchType == "" {
		entry.MatchType = BlacklistMatchExact
	}
	if entry.ValueHash == "" {
		entry.ValueHash = BlacklistPatternHashHex(entry.Pattern)
	}
	if entry.ValueMasked == "" {
		entry.ValueMasked = MaskBlacklistValue(entry.Kind, entry.Pattern, entry.MatchType)
	}
	if entry.Reason == "" {
		entry.Reason = "测试规则"
	}
	wantEnabled := entry.Enabled
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create blacklist entry: %v", err)
	}
	if !wantEnabled {
		if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entry.ID).
			Update("enabled", false).Error; err != nil {
			t.Fatalf("disable blacklist entry: %v", err)
		}
		entry.Enabled = false
	}
	return entry
}

// ---------------------------------------------------------------------------
// 归一化：各类型都要覆盖
// ---------------------------------------------------------------------------

func TestBlacklistNormalizeIPExact(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		matchType string
		want      string
	}{
		{"plain ipv4", "203.0.113.7", "", "203.0.113.7"},
		{"trimmed ipv4", "  203.0.113.7  ", BlacklistMatchExact, "203.0.113.7"},
		{"ipv4 mapped ipv6 collapses to ipv4", "::ffff:203.0.113.7", BlacklistMatchExact, "203.0.113.7"},
		{"ipv6 lowercased", "2001:DB8:0:0:0:0:0:1", BlacklistMatchExact, "2001:db8::1"},
		{"ipv6 loopback", "::1", BlacklistMatchExact, "::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeBlacklistValue(BlacklistKindIP, tc.input, tc.matchType)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if got.Pattern != tc.want {
				t.Fatalf("pattern = %q, want %q", got.Pattern, tc.want)
			}
			if got.MatchType != BlacklistMatchExact {
				t.Fatalf("matchType = %q, want exact", got.MatchType)
			}
			if got.ValueHash != BlacklistPatternHashHex(tc.want) {
				t.Fatalf("valueHash must be sha256 of the normalized pattern")
			}
			if len(got.ValueHash) != 64 {
				t.Fatalf("valueHash must be a 64-char hex digest, got %q", got.ValueHash)
			}
		})
	}
}

func TestBlacklistNormalizeIPCIDRAndPrefix(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"cidr ipv4", "203.0.113.0/24", "203.0.113.0/24"},
		{"cidr host bits cleared", "203.0.113.77/24", "203.0.113.0/24"},
		{"cidr ipv6", "2001:db8::/32", "2001:db8::/32"},
		{"prefix three octets", "203.0.113.", "203.0.113.0/24"},
		{"prefix two octets", "203.0.", "203.0.0.0/16"},
		{"prefix one octet", "203.", "203.0.0.0/8"},
		{"prefix without trailing dot", "203.0.113", "203.0.113.0/24"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeBlacklistValue(BlacklistKindIP, tc.input, "")
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if got.Pattern != tc.want {
				t.Fatalf("pattern = %q, want %q", got.Pattern, tc.want)
			}
			// prefix 只是输入侧写法：入库一律是 cidr，判定永远走 net.IPNet。
			if got.MatchType != BlacklistMatchCIDR {
				t.Fatalf("matchType = %q, want cidr", got.MatchType)
			}
		})
	}
}

func TestBlacklistNormalizeIPRejectsInvalid(t *testing.T) {
	for _, input := range []string{
		"", "   ", "not-an-ip", "300.1.1.1", "203.0.113.0/33", "2001:db8::/129",
		"203.0.113.1.", "203.0.113.0/", "1.2.3.4.5",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := NormalizeBlacklistValue(BlacklistKindIP, input, ""); err == nil {
				t.Fatalf("expected %q to be rejected", input)
			}
		})
	}
}

func TestBlacklistNormalizePhoneUsesMainlandNormalizer(t *testing.T) {
	got, err := NormalizeBlacklistValue(BlacklistKindPhone, " 138-0000-1111 ", "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got.Pattern != "13800001111" {
		t.Fatalf("pattern = %q, want 13800001111", got.Pattern)
	}
	// +86 旧写法同样收敛成 11 位（与 NormalizeMainlandPhoneInput 一致）。
	legacy, err := NormalizeBlacklistValue(BlacklistKindPhone, "+86 13800001111", BlacklistMatchExact)
	if err != nil {
		t.Fatalf("normalize legacy: %v", err)
	}
	if legacy.Pattern != "13800001111" {
		t.Fatalf("legacy pattern = %q, want 13800001111", legacy.Pattern)
	}
	for _, bad := range []string{"", "1380000111", "23800001111", "+1 5550100", "电话"} {
		if _, err := NormalizeBlacklistValue(BlacklistKindPhone, bad, ""); err == nil {
			t.Fatalf("expected phone %q to be rejected", bad)
		}
	}
	// 手机号不支持 cidr / prefix。
	if _, err := NormalizeBlacklistValue(BlacklistKindPhone, "13800001111", BlacklistMatchCIDR); err == nil {
		t.Fatal("phone must reject cidr match type")
	}
}

func TestBlacklistNormalizeWechatKeepsCaseAndTrimsSpace(t *testing.T) {
	got, err := NormalizeBlacklistValue(BlacklistKindWechat, "  oABCdefGHIjkl  ", "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	// OpenID 大小写敏感：绝不能 ToLower（46-*.md §1.2）。
	if got.Pattern != "oABCdefGHIjkl" {
		t.Fatalf("pattern = %q, want case preserved", got.Pattern)
	}
	if got.MatchType != BlacklistMatchExact {
		t.Fatalf("matchType = %q, want exact", got.MatchType)
	}
	// 大小写不同的两个 OpenID 摘要必须不同（否则两条规则会并成一条）。
	if got.ValueHash == BlacklistPatternHashHex("oabcdefghijkl") {
		t.Fatal("wechat hash must be case sensitive")
	}
	if _, err := NormalizeBlacklistValue(BlacklistKindWechat, "   ", ""); err == nil {
		t.Fatal("empty wechat openid must be rejected")
	}
}

func TestBlacklistNormalizeRejectsRegionAndUnknownKind(t *testing.T) {
	// 地区规则本期不接入判定：明确报错，而不是静默存下来但不生效。
	if _, err := NormalizeBlacklistValue(BlacklistKindRegion, "云南", BlacklistMatchPrefix); !errors.Is(err, ErrBlacklistRegionNotImplemented) {
		t.Fatalf("region must return ErrBlacklistRegionNotImplemented, got %v", err)
	}
	if _, err := NormalizeBlacklistValue("email", "a@b.c", ""); !errors.Is(err, ErrInvalidBlacklistKind) {
		t.Fatalf("unknown kind must return ErrInvalidBlacklistKind, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 脱敏
// ---------------------------------------------------------------------------

func TestBlacklistMaskValue(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		pattern   string
		matchType string
		want      string
	}{
		{"ipv4 exact", BlacklistKindIP, "203.0.113.7", BlacklistMatchExact, "203.0.113.*"},
		{"ipv6 exact", BlacklistKindIP, "2001:db8::1", BlacklistMatchExact, "2001:db8:*"},
		{"cidr kept as-is", BlacklistKindIP, "203.0.113.0/24", BlacklistMatchCIDR, "203.0.113.0/24"},
		{"phone", BlacklistKindPhone, "13800001111", BlacklistMatchExact, "138****1111"},
		{"wechat", BlacklistKindWechat, "oABCdefGHIjkl", BlacklistMatchExact, "o***jkl"},
		{"short wechat", BlacklistKindWechat, "abcd", BlacklistMatchExact, "a***"},
		{"region", BlacklistKindRegion, "云南", BlacklistMatchPrefix, "云南*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaskBlacklistValue(tc.kind, tc.pattern, tc.matchType); got != tc.want {
				t.Fatalf("mask = %q, want %q", got, tc.want)
			}
		})
	}
	// 脱敏结果不得包含完整原文（否则「脱敏」没有意义）。
	if masked := MaskBlacklistValue(BlacklistKindPhone, "13800001111", BlacklistMatchExact); strings.Contains(masked, "13800001111") {
		t.Fatalf("masked phone must not contain the full number: %q", masked)
	}
}

// ---------------------------------------------------------------------------
// 缓存：加载 / 失效 / 过期过滤 / 加载失败保留旧快照 / 开关
// ---------------------------------------------------------------------------

func TestBlacklistCacheLoadsExactAndCIDRMatches(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.7", Enabled: true})
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "198.51.100.0/24", MatchType: BlacklistMatchCIDR, Enabled: true})
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "2001:db8::/32", MatchType: BlacklistMatchCIDR, Enabled: true})

	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !cache.Enabled() {
		t.Fatal("cache must report enabled after loading blacklist_enabled=true")
	}
	snapshot := cache.Snapshot()
	if snapshot == nil {
		t.Fatal("snapshot must exist after a successful reload")
	}
	if snapshot.RuleCount != 3 {
		t.Fatalf("rule count = %d, want 3", snapshot.RuleCount)
	}

	cases := []struct {
		ip   string
		want bool
	}{
		{"203.0.113.7", true},
		{"203.0.113.8", false},
		{"198.51.100.42", true},
		{"198.51.101.1", false},
		{"2001:db8::dead:beef", true},
		{"2001:db9::1", false},
		// IPv4-mapped 写法必须与 IPv4 精确规则对齐（规范化收敛）。
		{"::ffff:203.0.113.7", true},
		{"", false},
		{"不合法", false},
	}
	for _, tc := range cases {
		_, hit := snapshot.MatchIP(tc.ip)
		if hit != tc.want {
			t.Fatalf("MatchIP(%q) = %v, want %v", tc.ip, hit, tc.want)
		}
	}
}

func TestBlacklistCacheSkipsDisabledAndExpiredEntries(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.7", Enabled: true})
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.8", Enabled: false})

	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.9", Enabled: true, ExpiresAt: &past})
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.10", Enabled: true, ExpiresAt: &future})

	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snapshot := cache.Snapshot()
	if snapshot.RuleCount != 2 {
		t.Fatalf("rule count = %d, want 2 (disabled + expired must be excluded)", snapshot.RuleCount)
	}
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"203.0.113.7", true},
		{"203.0.113.8", false}, // Enabled=false
		{"203.0.113.9", false}, // 已过期
		{"203.0.113.10", true}, // 未过期
	} {
		_, hit := snapshot.MatchIP(tc.ip)
		if hit != tc.want {
			t.Fatalf("MatchIP(%q) = %v, want %v", tc.ip, hit, tc.want)
		}
	}
	// 过期规则**不删除记录**（只不生效）：运营还能在列表里看到它。
	var expired entity.BlacklistEntry
	if err := db.Where("pattern = ?", "203.0.113.9").First(&expired).Error; err != nil {
		t.Fatalf("expired rule must stay in the table: %v", err)
	}
}

func TestBlacklistCacheInvalidatePicksUpNewRuleAndDisable(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.7", Enabled: true})

	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.8"); hit {
		t.Fatal("203.0.113.8 must not match before it is added")
	}

	// 管理员新增 → 立即失效重载后生效。
	added := seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.8", Enabled: true})
	if err := cache.Invalidate(db); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	match, hit := cache.Snapshot().MatchIP("203.0.113.8")
	if !hit {
		t.Fatal("newly added rule must take effect right after Invalidate")
	}
	if match.EntryID != added.ID {
		t.Fatalf("match entry id = %d, want %d", match.EntryID, added.ID)
	}

	// 管理员停用单条 → 立即不生效（缓存已刷新）。
	if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", added.ID).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable rule: %v", err)
	}
	if err := cache.Invalidate(db); err != nil {
		t.Fatalf("invalidate after disable: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.8"); hit {
		t.Fatal("disabled rule must stop matching after refresh")
	}
}

// 加载失败**必须保留旧快照**：DB 抖动不能让已生效的规则整批放开。
func TestBlacklistCacheKeepsPreviousSnapshotOnReloadFailure(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.7", Enabled: true})

	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	before := cache.Snapshot()

	// 模拟「刷新时数据库不可用」：关掉底层连接池，后续查询全部失败。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close sql db: %v", err)
	}

	reloadErr := cache.Reload(db)
	if reloadErr == nil {
		t.Fatal("expected reload to fail after the database was closed")
	}
	after := cache.Snapshot()
	if after != before {
		t.Fatal("reload failure must keep the previous snapshot pointer")
	}
	if !cache.Available() {
		t.Fatal("cache must stay available after a failed refresh")
	}
	// 旧快照仍然拦得住：fail-open 只适用于「从未加载成功」。
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); !hit {
		t.Fatal("previous snapshot must keep blocking after a failed refresh")
	}
	_, failures, lastErr := cache.Stats()
	if failures != 1 || lastErr == nil {
		t.Fatalf("stats = (failures=%d, lastErr=%v), want 1 failure with error", failures, lastErr)
	}
}

// 从未成功加载过 → Enabled()=false（中间件据此零查询零日志），且不算「可用」。
func TestBlacklistCacheDefaultsToDisabledWithoutSnapshot(t *testing.T) {
	cache := NewBlacklistCache()
	if cache.Enabled() {
		t.Fatal("empty cache must report disabled")
	}
	if cache.Available() {
		t.Fatal("empty cache must not be available")
	}
	if cache.Snapshot() != nil {
		t.Fatal("empty cache must have nil snapshot")
	}
	var nilCache *BlacklistCache
	if nilCache.Enabled() || nilCache.Available() {
		t.Fatal("nil cache must report disabled and unavailable")
	}
	if _, _, err := nilCache.Stats(); !errors.Is(err, ErrBlacklistCacheNoDatabase) {
		t.Fatalf("nil cache stats err = %v, want ErrBlacklistCacheNoDatabase", err)
	}
}

// 缺表（迁移没跑/回滚到旧版本）按「零规则 + 开关 false」处理，不报错。
func TestBlacklistCacheWithoutTablesIsEmptyAndDisabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:blacklist-notables-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload without tables must not fail: %v", err)
	}
	if cache.Enabled() {
		t.Fatal("missing tables must be treated as disabled")
	}
	if snapshot := cache.Snapshot(); snapshot == nil || snapshot.RuleCount != 0 {
		t.Fatalf("snapshot = %+v, want an empty snapshot", snapshot)
	}
}

func TestBlacklistCacheSwitchReadsDefaultFalseAndToleratesGarbage(t *testing.T) {
	db := newBlacklistTestDB(t)
	// 缺记录 → false。
	if GetBlacklistEnabledWithDB(db) {
		t.Fatal("missing blacklist_enabled must default to false")
	}
	// 值非法 → false。
	setBlacklistEnabled(t, db, "not-a-bool")
	if GetBlacklistEnabledWithDB(db) {
		t.Fatal("unparsable blacklist_enabled must fall back to false")
	}
	// 空值 → false。
	setBlacklistEnabled(t, db, "")
	if GetBlacklistEnabledWithDB(db) {
		t.Fatal("blank blacklist_enabled must fall back to false")
	}
	setBlacklistEnabled(t, db, "true")
	if !GetBlacklistEnabledWithDB(db) {
		t.Fatal("blacklist_enabled=true must read back as true")
	}
	// 没有 system_settings 表时也是 false（不能因为缺表而打开）。
	empty, err := gorm.Open(sqlite.Open("file:blacklist-nosettings-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if GetBlacklistEnabledWithDB(empty) {
		t.Fatal("missing system_settings table must default to false")
	}
	if GetBlacklistEnabledWithDB(nil) {
		t.Fatal("nil db must default to false")
	}
}

func TestBlacklistDefaultSwitchValueIsDocumented(t *testing.T) {
	// 红线：默认值必须是 false。这条断言防的是「有人把常量改成 true」。
	if DefaultBlacklistEnabled {
		t.Fatal("DefaultBlacklistEnabled must stay false")
	}
	if BlacklistEnabledSettingKey != "blacklist_enabled" {
		t.Fatalf("setting key = %q, want blacklist_enabled", BlacklistEnabledSettingKey)
	}
	if BlacklistHitAggregateWindow != 60*time.Second {
		t.Fatalf("aggregate window = %v, want 60s", BlacklistHitAggregateWindow)
	}
}

// 单条坏行只跳过它自己，不能让整张表的规则失效。
func TestBlacklistCacheSkipsMalformedRowsButKeepsOthers(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.7", Enabled: true})
	// 手工插入一条畸形 CIDR（绕过归一化，模拟运营直接改库）。
	if err := db.Select("*").Create(&entity.BlacklistEntry{
		Kind: BlacklistKindIP, Pattern: "203.0.113.0/99", MatchType: BlacklistMatchCIDR,
		ValueHash: BlacklistPatternHashHex("203.0.113.0/99"), ValueMasked: "203.0.113.0/99",
		Reason: "坏行", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("insert malformed row: %v", err)
	}
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snapshot := cache.Snapshot()
	if snapshot.SkippedCount != 1 {
		t.Fatalf("skipped count = %d, want 1", snapshot.SkippedCount)
	}
	if snapshot.RuleCount != 1 {
		t.Fatalf("rule count = %d, want 1", snapshot.RuleCount)
	}
	if _, hit := snapshot.MatchIP("203.0.113.7"); !hit {
		t.Fatal("good rule must survive a malformed sibling row")
	}
}

func TestBlacklistSnapshotMatchesPhoneAndWechat(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")
	phone := seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindPhone, Pattern: "13800001111", Enabled: true,
	})
	wechat := seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind: BlacklistKindWechat, Pattern: "oABCdefGHIjkl", Enabled: true,
	})
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snapshot := cache.Snapshot()

	if match, hit := snapshot.MatchPhone("+86 13800001111"); !hit || match.EntryID != phone.ID {
		t.Fatalf("phone match = (%+v, %v), want entry %d", match, hit, phone.ID)
	}
	if _, hit := snapshot.MatchPhone("13900002222"); hit {
		t.Fatal("other phone must not match")
	}
	if match, hit := snapshot.MatchWechat("  oABCdefGHIjkl "); !hit || match.EntryID != wechat.ID {
		t.Fatalf("wechat match = (%+v, %v), want entry %d", match, hit, wechat.ID)
	}
	// 大小写敏感：小写变体不得命中。
	if _, hit := snapshot.MatchWechat("oabcdefghijkl"); hit {
		t.Fatal("wechat match must be case sensitive")
	}
}

// ---------------------------------------------------------------------------
// 新建规则的唯一入口：CreateBlacklistEntryWithDB
// ---------------------------------------------------------------------------

// 这是「GORM default:true 零值陷阱」的**强制约束**测试。
//
// 陷阱本身：enabled 列带 default:true 标签时，GORM 会把等于默认值的字段从 INSERT
// 省略，`db.Create(&entry{Enabled:false})` 被静默写成 true（连 Select("*") 也绕不过）。
// 后果是「管理页显示已停用，但实际仍在拦人」——静默失败类缺陷。
//
// 本测试断言封装函数对 Enabled=false 的入参**最终落库确实是 false**，
// 而不是只看函数返回值（返回值可以被写成与库不一致）。
func TestCreateBlacklistEntryWithDBHonorsDisabledEnabled(t *testing.T) {
	db := newBlacklistTestDB(t)

	entry, err := CreateBlacklistEntryWithDB(db, CreateBlacklistEntryInput{
		Kind: BlacklistKindIP, Pattern: "203.0.113.7", Reason: "建单即停用",
		Enabled: false, CreatedBy: 1,
	})
	if err != nil {
		t.Fatalf("create disabled entry: %v", err)
	}
	if entry.Enabled {
		t.Fatal("returned entry must report enabled=false")
	}
	// 关键断言：**从库里读回来**也是 false（这才是管理页看到的东西）。
	var stored entity.BlacklistEntry
	if err := db.Where("id = ?", entry.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload entry: %v", err)
	}
	if stored.Enabled {
		t.Fatal("stored enabled must be false — GORM's default:true zero-value trap must be bypassed")
	}
	// 停用的规则不进缓存，因此不会命中。
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); hit {
		t.Fatal("a rule created as disabled must not match")
	}
}

func TestCreateBlacklistEntryWithDBHonorsEnabled(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")

	entry, err := CreateBlacklistEntryWithDB(db, CreateBlacklistEntryInput{
		Kind: BlacklistKindIP, Pattern: "203.0.113.7", Reason: "建单即启用", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create enabled entry: %v", err)
	}
	var stored entity.BlacklistEntry
	if err := db.Where("id = ?", entry.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload entry: %v", err)
	}
	if !stored.Enabled {
		t.Fatal("stored enabled must be true")
	}
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); !hit {
		t.Fatal("an enabled rule must match")
	}
}

// 封装负责归一化：调用方传原始输入，落库的是规范形式，且 value_hash 与 pattern 一致。
func TestCreateBlacklistEntryWithDBNormalizesInput(t *testing.T) {
	db := newBlacklistTestDB(t)
	entry, err := CreateBlacklistEntryWithDB(db, CreateBlacklistEntryInput{
		Kind: BlacklistKindIP, Pattern: "  203.0.113.77/24  ", Reason: "  归一化  ", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if entry.Pattern != "203.0.113.0/24" {
		t.Fatalf("pattern = %q, want 203.0.113.0/24 (host bits cleared)", entry.Pattern)
	}
	if entry.MatchType != BlacklistMatchCIDR {
		t.Fatalf("matchType = %q, want cidr", entry.MatchType)
	}
	if entry.ValueHash != BlacklistPatternHashHex(entry.Pattern) {
		t.Fatal("value_hash must be sha256 of the normalized pattern")
	}
	if entry.ValueMasked != "203.0.113.0/24" {
		t.Fatalf("valueMasked = %q", entry.ValueMasked)
	}
	if entry.Reason != "归一化" {
		t.Fatalf("reason = %q, want trimmed", entry.Reason)
	}
}

// 封装拒绝非法输入与 region：运营填错不会变成一条永不生效的脏规则。
func TestCreateBlacklistEntryWithDBRejectsInvalidInput(t *testing.T) {
	db := newBlacklistTestDB(t)
	cases := []struct {
		name  string
		input CreateBlacklistEntryInput
		want  error
	}{
		{"bad ip", CreateBlacklistEntryInput{Kind: BlacklistKindIP, Pattern: "300.1.1.1", Reason: "x", Enabled: true}, ErrInvalidBlacklistPattern},
		{"bad cidr", CreateBlacklistEntryInput{Kind: BlacklistKindIP, Pattern: "203.0.113.0/99", Reason: "x", Enabled: true}, ErrInvalidBlacklistPattern},
		{"bad phone", CreateBlacklistEntryInput{Kind: BlacklistKindPhone, Pattern: "1380000", Reason: "x", Enabled: true}, ErrInvalidBlacklistPattern},
		{"empty wechat", CreateBlacklistEntryInput{Kind: BlacklistKindWechat, Pattern: "   ", Reason: "x", Enabled: true}, ErrInvalidBlacklistPattern},
		{"unknown kind", CreateBlacklistEntryInput{Kind: "email", Pattern: "a@b.c", Reason: "x", Enabled: true}, ErrInvalidBlacklistKind},
		{"region rejected", CreateBlacklistEntryInput{Kind: BlacklistKindRegion, Pattern: "云南", Reason: "x", Enabled: true}, ErrBlacklistRegionNotImplemented},
		// 46-*.md §2.1：拉黑原因必填。
		{"empty reason", CreateBlacklistEntryInput{Kind: BlacklistKindIP, Pattern: "203.0.113.7", Reason: "   ", Enabled: true}, ErrInvalidBlacklistPattern},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CreateBlacklistEntryWithDB(db, tc.input); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// 一条都不该落库。
	var count int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected inputs must not be persisted, got %d rows", count)
	}
	// nil db 也要明确报错，不能 panic。
	if _, err := CreateBlacklistEntryWithDB(nil, CreateBlacklistEntryInput{Kind: BlacklistKindIP, Pattern: "203.0.113.7", Reason: "x"}); !errors.Is(err, ErrBlacklistCacheNoDatabase) {
		t.Fatalf("nil db err = %v, want ErrBlacklistCacheNoDatabase", err)
	}
}

// 唯一索引冲突要作为错误返回，而不是静默建出两条。
func TestCreateBlacklistEntryWithDBRejectsDuplicate(t *testing.T) {
	db := newBlacklistTestDB(t)
	if _, err := CreateBlacklistEntryWithDB(db, CreateBlacklistEntryInput{
		Kind: BlacklistKindIP, Pattern: "203.0.113.7", Reason: "第一条", Enabled: true,
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := CreateBlacklistEntryWithDB(db, CreateBlacklistEntryInput{
		Kind: BlacklistKindIP, Pattern: "203.0.113.7", Reason: "重复", Enabled: true,
	}); err == nil {
		t.Fatal("duplicate (kind, value_hash, match_type) must be rejected")
	}
}

// 手工插入一条 kind='region' 的脏行（绕过 service 层）：
//   - 必须被**跳过 + 告警**，不能静默接受（否则运营以为规则生效了）
//   - 更不能让整次加载失败（一条脏行 = 全站放开是最坏结果）
func TestBlacklistCacheSkipsHandInsertedRegionRow(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")
	good := seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.7", Enabled: true})
	// 直接写库，模拟运营手工 INSERT。
	if err := db.Create(&entity.BlacklistEntry{
		Kind: BlacklistKindRegion, Pattern: "云南", MatchType: BlacklistMatchPrefix,
		ValueHash: BlacklistPatternHashHex("云南"), ValueMasked: "云南*",
		Reason: "手工插入", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("insert region row: %v", err)
	}

	var logBuf strings.Builder
	oldOut, oldErr := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &logBuf, &logBuf
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldOut, oldErr })

	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("a dirty region row must not fail the whole load: %v", err)
	}
	snapshot := cache.Snapshot()
	if snapshot.SkippedCount != 1 {
		t.Fatalf("skipped count = %d, want 1", snapshot.SkippedCount)
	}
	if snapshot.RuleCount != 1 {
		t.Fatalf("rule count = %d, want 1 (only the good IP rule)", snapshot.RuleCount)
	}
	// 好的规则仍然生效 —— 一条脏行不能让整个黑名单瘫痪。
	if match, hit := snapshot.MatchIP("203.0.113.7"); !hit || match.EntryID != good.ID {
		t.Fatalf("good rule must still work: match=%+v hit=%v", match, hit)
	}
	if !strings.Contains(logBuf.String(), "region") {
		t.Fatalf("skipping a region row must warn, log=%q", logBuf.String())
	}
}

// ---------------------------------------------------------------------------
// 命中日志聚合
// ---------------------------------------------------------------------------

// 被刷 100 次 → 只写 1 行，hit_count=100。
func TestBlacklistHitRecorderAggregatesWithinWindow(t *testing.T) {
	db := newBlacklistTestDB(t)
	recorder := NewBlacklistHitRecorder()
	// 固定时钟：整个 100 次都在同一个 60 秒窗口内。
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	recorder.now = func() time.Time { return now.Add(500 * time.Millisecond) }

	for i := 0; i < 100; i++ {
		// 时间在窗口内缓慢推进（每次 +500ms，100 次共 50s < 60s 窗口）。
		offset := time.Duration(i) * 500 * time.Millisecond
		recorder.now = func() time.Time { return now.Add(offset) }
		if err := recorder.Record(db, entity.BlacklistHit{
			EntryID: 7, Kind: BlacklistKindIP, ValueMasked: "203.0.113.*",
			ClientIP: "203.0.113.7", Path: "/adapter-service/search",
			Action: BlacklistActionBlocked,
		}); err != nil {
			t.Fatalf("record #%d: %v", i, err)
		}
	}

	var rows []entity.BlacklistHit
	if err := db.Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("load hits: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("hit rows = %d, want 1 (100 blocked requests must aggregate into 1 row)", len(rows))
	}
	if rows[0].HitCount != 100 {
		t.Fatalf("hit_count = %d, want 100", rows[0].HitCount)
	}
	if rows[0].EntryID != 7 || rows[0].ClientIP != "203.0.113.7" ||
		rows[0].ValueMasked != "203.0.113.*" || rows[0].Path != "/adapter-service/search" ||
		rows[0].Action != BlacklistActionBlocked {
		t.Fatalf("aggregated row lost fields: %+v", rows[0])
	}
	// 只保留一个槽位。
	if slots := recorder.SlotCount(); slots != 1 {
		t.Fatalf("slots = %d, want 1", slots)
	}
}

// 不同 entry / 不同 IP / 不同 action 各自成行（聚合不能把无关命中并在一起）。
func TestBlacklistHitRecorderKeepsDistinctKeysSeparate(t *testing.T) {
	db := newBlacklistTestDB(t)
	recorder := NewBlacklistHitRecorder()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	recorder.now = func() time.Time { return now }

	hits := []entity.BlacklistHit{
		{EntryID: 1, ClientIP: "203.0.113.7", Action: BlacklistActionBlocked, Path: "/a"},
		{EntryID: 1, ClientIP: "203.0.113.7", Action: BlacklistActionBlocked, Path: "/a"},
		{EntryID: 2, ClientIP: "203.0.113.7", Action: BlacklistActionBlocked, Path: "/a"},
		{EntryID: 1, ClientIP: "203.0.113.8", Action: BlacklistActionBlocked, Path: "/a"},
		{EntryID: 1, ClientIP: "203.0.113.7", Action: BlacklistActionLoggedOnly, Path: "/a"},
	}
	for i, hit := range hits {
		hit.Kind = BlacklistKindIP
		if err := recorder.Record(db, hit); err != nil {
			t.Fatalf("record #%d: %v", i, err)
		}
	}
	var count int64
	if err := db.Model(&entity.BlacklistHit{}).Count(&count).Error; err != nil {
		t.Fatalf("count hits: %v", err)
	}
	if count != 4 {
		t.Fatalf("hit rows = %d, want 4 distinct (entry,ip,action) keys", count)
	}
}

// 窗口过期后重新插一行（不是无限累加同一行）。
func TestBlacklistHitRecorderStartsNewRowAfterWindow(t *testing.T) {
	db := newBlacklistTestDB(t)
	recorder := NewBlacklistHitRecorder()
	base := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	current := base
	recorder.now = func() time.Time { return current }

	hit := entity.BlacklistHit{EntryID: 1, Kind: BlacklistKindIP, ClientIP: "203.0.113.7", Action: BlacklistActionBlocked, Path: "/a"}
	if err := recorder.Record(db, hit); err != nil {
		t.Fatalf("record first: %v", err)
	}
	current = base.Add(BlacklistHitAggregateWindow + time.Second)
	if err := recorder.Record(db, hit); err != nil {
		t.Fatalf("record after window: %v", err)
	}
	var count int64
	if err := db.Model(&entity.BlacklistHit{}).Count(&count).Error; err != nil {
		t.Fatalf("count hits: %v", err)
	}
	if count != 2 {
		t.Fatalf("hit rows = %d, want 2 (a new window must open a new row)", count)
	}
}

// 长路径按**字符**截断到 varchar(255) 的量级，不能因为中文 3 字节而越界。
func TestBlacklistHitRecorderTruncatesLongPathByRunes(t *testing.T) {
	db := newBlacklistTestDB(t)
	recorder := NewBlacklistHitRecorder()
	longPath := "/adapter-service/" + strings.Repeat("题", 400)
	if err := recorder.Record(db, entity.BlacklistHit{
		EntryID: 1, Kind: BlacklistKindIP, ClientIP: "203.0.113.7",
		Action: BlacklistActionBlocked, Path: longPath,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	var row entity.BlacklistHit
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("load hit: %v", err)
	}
	if got := len([]rune(row.Path)); got != 255 {
		t.Fatalf("stored path runes = %d, want 255", got)
	}
}

// 数据库不可用时写日志失败要返回错误（由中间件降级为只记日志，不影响请求）。
func TestBlacklistHitRecorderReturnsErrorWithoutDatabase(t *testing.T) {
	recorder := NewBlacklistHitRecorder()
	if err := recorder.Record(nil, entity.BlacklistHit{EntryID: 1}); !errors.Is(err, ErrBlacklistCacheNoDatabase) {
		t.Fatalf("record without db err = %v, want ErrBlacklistCacheNoDatabase", err)
	}
	var nilRecorder *BlacklistHitRecorder
	if err := nilRecorder.Record(nil, entity.BlacklistHit{}); err == nil {
		t.Fatal("nil recorder must return an error")
	}
	if nilRecorder.SlotCount() != 0 {
		t.Fatal("nil recorder slot count must be 0")
	}
}

// 后台周期重载：停止函数必须幂等且能等到 goroutine 退出。
func TestStartBlacklistCacheRefreshStopsCleanly(t *testing.T) {
	db := newBlacklistTestDB(t)
	setBlacklistEnabled(t, db, "true")
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Pattern: "203.0.113.7", Enabled: true})

	cache := NewBlacklistCache()
	stop := StartBlacklistCacheRefresh(cache, db, 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cache.Available() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cache.Available() {
		stop()
		t.Fatal("background refresh must eventually load a snapshot")
	}
	stop()
	stop() // 幂等：重复调用不 panic
	loads, _, _ := cache.Stats()
	if loads < 1 {
		t.Fatalf("loads = %d, want at least 1", loads)
	}
}
