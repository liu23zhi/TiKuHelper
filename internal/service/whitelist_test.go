package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newWhitelistTestDB 建一个隔离的白名单测试库（含 system_settings）。
//
// 库名带 t.Name()：SQLite 的 file:...?mode=memory&cache=shared 在同一进程内同名库
// 是共享的，跨用例会互相污染。
func newWhitelistTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:whitelist-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.WhitelistEntry{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// seedWhitelistEntry 落一条**启用**的规则。
//
// ⚠️ 刻意忽略 entry.Enabled：`entity.WhitelistEntry{...}` 的 struct literal 不写
// Enabled 时是零值 false，而实体带 `default:true` 标签 —— 直接 Create 会把一条
// 「本来想启用」的规则静默写成启用（GORM 把等于默认值的字段从 INSERT 省略）。
// 也就是说「不写 Enabled」与「写 Enabled:false」在 Create 上无法区分，两者都会变成
// true。所以这里不玩隐式语义：本函数一律落成启用，要停用请用
// seedDisabledWhitelistEntry（显式两步写）。
func seedWhitelistEntry(t *testing.T, db *gorm.DB, entry entity.WhitelistEntry) entity.WhitelistEntry {
	t.Helper()
	if entry.Kind == "" {
		entry.Kind = WhitelistKindIP
	}
	if entry.MatchType == "" {
		entry.MatchType = WhitelistMatchExact
	}
	if entry.ValueHash == "" {
		entry.ValueHash = WhitelistPatternHashHex(entry.Pattern)
	}
	if entry.ValueMasked == "" {
		entry.ValueMasked = MaskWhitelistValue(entry.Kind, entry.Pattern, entry.MatchType)
	}
	if entry.Reason == "" {
		entry.Reason = "测试放行"
	}
	entry.Enabled = true
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create whitelist entry: %v", err)
	}
	return entry
}

// seedDisabledWhitelistEntry 落一条**停用**的规则（两步写，绕开 default:true 陷阱）。
func seedDisabledWhitelistEntry(t *testing.T, db *gorm.DB, entry entity.WhitelistEntry) entity.WhitelistEntry {
	t.Helper()
	created := seedWhitelistEntry(t, db, entry)
	if err := db.Model(&entity.WhitelistEntry{}).Where("id = ?", created.ID).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable whitelist entry: %v", err)
	}
	created.Enabled = false
	return created
}

func setWhitelistExemptIPs(t *testing.T, db *gorm.DB, value string) {
	t.Helper()
	if err := saveSystemSettingValue(db, WhitelistExemptIPsSettingKey, value,
		"按 IP 白名单（IP/CIDR，逗号或换行分隔；命中即完全不查黑名单）"); err != nil {
		t.Fatalf("set %s=%q: %v", WhitelistExemptIPsSettingKey, value, err)
	}
}

// ---------------------------------------------------------------------------
// 判据②：CIDR 与单 IP 都能解析，非法项拒绝保存
// ---------------------------------------------------------------------------

func TestWhitelistNormalizeAcceptsSingleIPAndCIDR(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		matchType string
		wantPat   string
		wantMatch string
	}{
		{"single ipv4", "203.0.113.7", "", "203.0.113.7", WhitelistMatchExact},
		{"single ipv4 trimmed", "  203.0.113.7 \t", "", "203.0.113.7", WhitelistMatchExact},
		{"explicit exact", "203.0.113.7", WhitelistMatchExact, "203.0.113.7", WhitelistMatchExact},
		{"cidr v4", "198.51.100.0/24", "", "198.51.100.0/24", WhitelistMatchCIDR},
		// host 位必须清零：写 198.51.100.77/24 要收敛成网段，否则「规则看着是 /24、
		// 实际只放行一个 IP」——这是白名单最隐蔽的错配。
		{"cidr v4 host bits cleared", "198.51.100.77/24", "", "198.51.100.0/24", WhitelistMatchCIDR},
		{"cidr v6", "2001:db8::/32", "", "2001:db8::/32", WhitelistMatchCIDR},
		{"ipv4 prefix short", "203.", "", "203.0.0.0/8", WhitelistMatchCIDR},
		{"ipv4 prefix three octets", "203.0.113", "", "203.0.113.0/24", WhitelistMatchCIDR},
		{"single ipv6", "2001:db8::1", "", "2001:db8::1", WhitelistMatchExact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := NormalizeWhitelistValue(WhitelistKindIP, tc.input, tc.matchType)
			if err != nil {
				t.Fatalf("NormalizeWhitelistValue(%q, %q) error: %v", tc.input, tc.matchType, err)
			}
			if normalized.Pattern != tc.wantPat {
				t.Fatalf("pattern = %q, want %q", normalized.Pattern, tc.wantPat)
			}
			if normalized.MatchType != tc.wantMatch {
				t.Fatalf("matchType = %q, want %q", normalized.MatchType, tc.wantMatch)
			}
			if normalized.Kind != WhitelistKindIP {
				t.Fatalf("kind = %q, want %q", normalized.Kind, WhitelistKindIP)
			}
			// value_hash 必须是归一化后的 pattern 的 sha256，否则规则存进去永远命中不了。
			if want := WhitelistPatternHashHex(tc.wantPat); normalized.ValueHash != want {
				t.Fatalf("valueHash = %q, want %q", normalized.ValueHash, want)
			}
			if strings.TrimSpace(normalized.ValueMasked) == "" {
				t.Fatal("valueMasked 不能为空")
			}
		})
	}
}

func TestWhitelistNormalizeRejectsInvalid(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		input     string
		matchType string
		wantErr   error
	}{
		{"empty pattern", WhitelistKindIP, "   ", "", ErrInvalidWhitelistPattern},
		{"garbage", WhitelistKindIP, "not-an-ip", "", ErrInvalidWhitelistPattern},
		{"bad cidr mask", WhitelistKindIP, "203.0.113.0/33", "", ErrInvalidWhitelistPattern},
		{"cidr without address", WhitelistKindIP, "/24", "", ErrInvalidWhitelistPattern},
		{"ipv6 prefix shorthand rejected", WhitelistKindIP, "2001:db8:", "", ErrInvalidWhitelistPattern},
		{"phone kind rejected", "phone", "13800001111", "", ErrInvalidWhitelistKind},
		{"wechat kind rejected", "wechat", "oABC", "", ErrInvalidWhitelistKind},
		{"region kind rejected", "region", "云南", "", ErrInvalidWhitelistKind},
		{"bad match type", WhitelistKindIP, "203.0.113.7", "regex", ErrInvalidWhitelistMatchType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeWhitelistValue(tc.kind, tc.input, tc.matchType); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// 保存路径必须拒绝非法项（不是「跳过坏项」）：白名单写错的表现是
// 「该放行的仍被拦」，运营从现象完全反推不到是格式问题。
func TestWhitelistCreateRejectsInvalidPattern(t *testing.T) {
	db := newWhitelistTestDB(t)
	for _, bad := range []string{"not-an-ip", "203.0.113.0/33", "1.2.3.4/99"} {
		_, err := CreateWhitelistEntryWithDB(db, CreateWhitelistEntryInput{
			Kind: WhitelistKindIP, Pattern: bad, Reason: "测试",
		})
		if !errors.Is(err, ErrInvalidWhitelistPattern) {
			t.Fatalf("CreateWhitelistEntryWithDB(%q) err = %v, want ErrInvalidWhitelistPattern", bad, err)
		}
	}
	var count int64
	if err := db.Model(&entity.WhitelistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("非法输入必须一行都不落库，got %d 行", count)
	}

	// reason 必填（空串 / 纯空白都拒绝）。
	if _, err := CreateWhitelistEntryWithDB(db, CreateWhitelistEntryInput{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", Reason: "   ",
	}); !errors.Is(err, ErrInvalidWhitelistPattern) {
		t.Fatalf("empty reason err = %v, want ErrInvalidWhitelistPattern", err)
	}
}

// blacklist_exempt_ips 里的非法项也必须拒绝保存（复用 login_rate_limit 的校验）。
func TestWhitelistExemptIPsValidationRejectsInvalidItems(t *testing.T) {
	valid := "203.0.113.7, 198.51.100.0/24\n2001:db8::/32;  10.0.0.1"
	if err := ValidateWhitelistExemptIPs(valid); err != nil {
		t.Fatalf("合法文本被拒: %v", err)
	}
	for _, bad := range []string{
		"203.0.113.7, not-an-ip",
		"198.51.100.0/33",
		"1.2.3.4/99",
		"10.0.0.1 ::gggg",
	} {
		if err := ValidateWhitelistExemptIPs(bad); !errors.Is(err, ErrInvalidLoginRateLimitExemptIPs) {
			t.Fatalf("ValidateWhitelistExemptIPs(%q) err = %v, want ErrInvalidLoginRateLimitExemptIPs", bad, err)
		}
	}
	// 保存路径同样拒绝，且不落库。
	db := newWhitelistTestDB(t)
	if err := SaveWhitelistExemptIPsWithDB(db, "203.0.113.7, not-an-ip"); !errors.Is(err, ErrInvalidLoginRateLimitExemptIPs) {
		t.Fatalf("Save err = %v, want ErrInvalidLoginRateLimitExemptIPs", err)
	}
	if got := GetWhitelistExemptIPsWithDB(db); got != "" {
		t.Fatalf("非法输入必须不落库，got %q", got)
	}
}

// T-05：解析必须复用 login_rate_limit 那套（不新造解析器）。
// 判据：CIDR 与单 IP 都解析，坏项跳过（读取侧宽容）。
func TestWhitelistExemptIPsParsingReusesLoginRateLimitParser(t *testing.T) {
	if got := ParseWhitelistExemptIPs("   "); got != nil {
		t.Fatalf("空白文本应返回 nil，got %v", got)
	}
	networks := ParseWhitelistExemptIPs("203.0.113.7, 198.51.100.0/24;10.0.0.0/8\n2001:db8::/32")
	if len(networks) != 4 {
		t.Fatalf("解析出 %d 个网段，want 4", len(networks))
	}
	// 单个 IP 按 /32 处理，所以要能 Contains 自身。
	if !networks[0].Contains(parseWhitelistClientIP("203.0.113.7")) {
		t.Fatalf("单 IP 项应按 /32 处理：%v", networks[0])
	}
	// 坏项跳过，好项保留（读取侧宽容）。
	withBad := ParseWhitelistExemptIPs("203.0.113.7, not-an-ip, 198.51.100.0/24, 1.2.3.4/99")
	if len(withBad) != 2 {
		t.Fatalf("坏项应跳过、好项保留，got %d 个", len(withBad))
	}
}

// ---------------------------------------------------------------------------
// 判据③：v4-mapped 归一（::ffff:203.0.113.7 == 203.0.113.7）
// ---------------------------------------------------------------------------

func TestWhitelistNormalizeCollapsesV4Mapped(t *testing.T) {
	normalized, err := NormalizeWhitelistValue(WhitelistKindIP, "::ffff:203.0.113.7", "")
	if err != nil {
		t.Fatalf("Normalize err: %v", err)
	}
	if normalized.Pattern != "203.0.113.7" {
		t.Fatalf("pattern = %q, want 203.0.113.7（v4-mapped 必须收敛）", normalized.Pattern)
	}
}

func TestWhitelistSnapshotMatchesV4MappedBothDirections(t *testing.T) {
	db := newWhitelistTestDB(t)
	seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", MatchType: WhitelistMatchExact,
	})
	cache := NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snapshot := cache.Snapshot()
	for _, ip := range []string{"203.0.113.7", "::ffff:203.0.113.7", "  203.0.113.7  "} {
		if _, hit := snapshot.MatchIP(ip); !hit {
			t.Fatalf("MatchIP(%q) 应命中（v4-mapped 归一）", ip)
		}
	}
	if _, hit := snapshot.MatchIP("203.0.113.8"); hit {
		t.Fatal("MatchIP(203.0.113.8) 不应命中")
	}
	// 反向：库里存 v4-mapped 写法、请求用 IPv4 文本，也必须命中
	// （归一化在两条路径上都要做，只有一侧做等于只对一半）。
	db2 := newWhitelistTestDB(t)
	seedWhitelistEntry(t, db2, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "::ffff:198.51.100.9", MatchType: WhitelistMatchExact,
	})
	cache2 := NewWhitelistCache()
	if err := cache2.Reload(db2); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, hit := cache2.Snapshot().MatchIP("198.51.100.9"); !hit {
		t.Fatal("库里存 v4-mapped 写法时，IPv4 文本请求也应命中")
	}
}

// exempt_ips 文本域同样要归一（否则「同一地址两种写法」在第二层白名单上复现）。
func TestWhitelistExemptIPsMatchIsV4MappedAware(t *testing.T) {
	db := newWhitelistTestDB(t)
	setWhitelistExemptIPs(t, db, "203.0.113.7")
	if !IsWhitelistExemptIPWithDB(db, "203.0.113.7") {
		t.Fatal("203.0.113.7 应命中 exempt_ips")
	}
	if !IsWhitelistExemptIPWithDB(db, "::ffff:203.0.113.7") {
		t.Fatal("::ffff:203.0.113.7 应命中 exempt_ips（v4-mapped 归一）")
	}
	if IsWhitelistExemptIPWithDB(db, "203.0.113.8") {
		t.Fatal("203.0.113.8 不应命中")
	}
	if IsWhitelistExemptIPWithDB(db, "") || IsWhitelistExemptIPWithDB(db, "not-an-ip") {
		t.Fatal("空串 / 非法串都不应命中")
	}

	// CIDR 形态。
	setWhitelistExemptIPs(t, db, "198.51.100.0/24")
	if !IsWhitelistExemptIPWithDB(db, "198.51.100.7") {
		t.Fatal("CIDR 内的 IP 应命中")
	}
	if IsWhitelistExemptIPWithDB(db, "198.51.101.7") {
		t.Fatal("CIDR 外的 IP 不应命中")
	}
}

// ---------------------------------------------------------------------------
// 缓存：加载 / 过滤 / 保留旧快照
// ---------------------------------------------------------------------------

func TestWhitelistCacheMatchesExactAndCIDR(t *testing.T) {
	db := newWhitelistTestDB(t)
	exact := seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", MatchType: WhitelistMatchExact,
	})
	cidr := seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "198.51.100.0/24", MatchType: WhitelistMatchCIDR,
	})
	setWhitelistExemptIPs(t, db, "192.0.2.10")

	cache := NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snapshot := cache.Snapshot()
	if got := snapshot.Count(); got != 2 {
		t.Fatalf("RuleCount = %d, want 2", got)
	}
	if got := snapshot.ExemptCount; got != 1 {
		t.Fatalf("ExemptCount = %d, want 1", got)
	}

	match, hit := snapshot.MatchIP("203.0.113.7")
	if !hit || match.EntryID != exact.ID || match.Source != WhitelistSourceEntry {
		t.Fatalf("精确命中 = %+v hit=%v, want entryID=%d source=entry", match, hit, exact.ID)
	}
	match, hit = snapshot.MatchIP("198.51.100.42")
	if !hit || match.EntryID != cidr.ID || match.Source != WhitelistSourceEntry {
		t.Fatalf("CIDR 命中 = %+v hit=%v, want entryID=%d", match, hit, cidr.ID)
	}
	match, hit = snapshot.MatchIP("192.0.2.10")
	if !hit || match.Source != WhitelistSourceExemptIPs {
		t.Fatalf("exempt_ips 命中 = %+v hit=%v, want source=%s", match, hit, WhitelistSourceExemptIPs)
	}
	if _, hit := snapshot.MatchIP("203.0.113.8"); hit {
		t.Fatal("203.0.113.8 不应命中")
	}
	if _, hit := snapshot.MatchIP(""); hit {
		t.Fatal("空串不应命中")
	}
}

func TestWhitelistCacheSkipsDisabledAndExpired(t *testing.T) {
	db := newWhitelistTestDB(t)
	seedDisabledWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", MatchType: WhitelistMatchExact,
	})
	past := time.Now().Add(-time.Hour)
	seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.8", MatchType: WhitelistMatchExact, ExpiresAt: &past,
	})
	seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.9", MatchType: WhitelistMatchExact,
	})

	cache := NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snapshot := cache.Snapshot()
	if got := snapshot.Count(); got != 1 {
		t.Fatalf("RuleCount = %d, want 1（停用与过期都不进缓存）", got)
	}
	if _, hit := snapshot.MatchIP("203.0.113.7"); hit {
		t.Fatal("停用规则不应命中")
	}
	if _, hit := snapshot.MatchIP("203.0.113.8"); hit {
		t.Fatal("过期规则不应命中")
	}
	if _, hit := snapshot.MatchIP("203.0.113.9"); !hit {
		t.Fatal("有效期内的规则应命中")
	}
}

func TestWhitelistCacheKeepsPreviousSnapshotOnReloadFailure(t *testing.T) {
	db := newWhitelistTestDB(t)
	seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", MatchType: WhitelistMatchExact,
	})
	cache := NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	previous := cache.Snapshot()
	if _, hit := previous.MatchIP("203.0.113.7"); !hit {
		t.Fatal("前置条件：快照应命中 203.0.113.7")
	}
	// 用 nil DB 触发刷新失败（模拟 DB 抖动）：旧快照必须继续生效。
	if err := cache.Reload(nil); err == nil {
		t.Fatal("nil DB 的 Reload 应返回错误")
	}
	if cache.Snapshot() != previous {
		t.Fatal("刷新失败必须保留旧快照（白名单被清空会让已放行的 IP 重新被拦）")
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); !hit {
		t.Fatal("刷新失败后旧快照仍应命中")
	}
	loads, failures, lastErr := cache.Stats()
	if loads != 1 || failures != 1 || lastErr == nil {
		t.Fatalf("stats = (%d,%d,%v), want (1,1,non-nil)", loads, failures, lastErr)
	}
}

// 没有快照时**不放行任何人**（与黑名单 fail-open 相反）。
func TestWhitelistCacheWithoutSnapshotDoesNotBypass(t *testing.T) {
	cache := NewWhitelistCache()
	if cache.Available() {
		t.Fatal("新建缓存不应 Available")
	}
	if cache.Snapshot() != nil {
		t.Fatal("新建缓存的 Snapshot 应为 nil")
	}
	var nilSnapshot *WhitelistSnapshot
	if _, hit := nilSnapshot.MatchIP("203.0.113.7"); hit {
		t.Fatal("nil 快照不应命中任何 IP")
	}
	if got := nilSnapshot.Count(); got != 0 {
		t.Fatalf("nil 快照 Count = %d, want 0", got)
	}
}

func TestWhitelistCacheWithoutTableIsEmptyNotError(t *testing.T) {
	db := newWhitelistTestDB(t)
	// 删掉表模拟「迁移没跑 / 回滚到旧版本」。
	if err := db.Migrator().DropTable(&entity.WhitelistEntry{}); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	cache := NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("缺表不应报错（白名单是增强功能）：%v", err)
	}
	if got := cache.Snapshot().Count(); got != 0 {
		t.Fatalf("缺表时 RuleCount = %d, want 0", got)
	}
}

// 坏行只跳过它自己：一条畸形 CIDR 不能把整张白名单搞失效。
func TestWhitelistCacheSkipsMalformedRowButKeepsOthers(t *testing.T) {
	db := newWhitelistTestDB(t)
	// 绕过归一化直接插脏数据（模拟历史脏行 / 手工改库）。
	if err := db.Create(&entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.0/99", MatchType: WhitelistMatchCIDR,
		ValueHash: WhitelistPatternHashHex("203.0.113.0/99"), ValueMasked: "203.0.113.0/99",
		Reason: "脏行", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("insert dirty row: %v", err)
	}
	seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", MatchType: WhitelistMatchExact,
	})
	cache := NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := cache.Snapshot().SkippedCount; got != 1 {
		t.Fatalf("SkippedCount = %d, want 1", got)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); !hit {
		t.Fatal("脏行不应影响其它规则")
	}
}

func TestWhitelistCacheInvalidatePicksUpChanges(t *testing.T) {
	db := newWhitelistTestDB(t)
	cache := NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); hit {
		t.Fatal("前置条件：空表不应命中")
	}
	entry := seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", MatchType: WhitelistMatchExact,
	})
	if err := cache.Invalidate(db); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); !hit {
		t.Fatal("Invalidate 后应立刻命中新规则")
	}
	// 停用后必须立刻不命中。
	if _, err := UpdateWhitelistEntryEnabledWithDB(db, entry.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := cache.Invalidate(db); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); hit {
		t.Fatal("停用后不应命中")
	}
	// 改 exempt_ips 文本域也要靠 Invalidate 收敛。
	setWhitelistExemptIPs(t, db, "192.0.2.10")
	if err := cache.Invalidate(db); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("192.0.2.10"); !hit {
		t.Fatal("Invalidate 后 exempt_ips 也应立刻生效")
	}
}

func TestStartWhitelistCacheRefreshStopsCleanly(t *testing.T) {
	db := newWhitelistTestDB(t)
	cache := NewWhitelistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	stop := StartWhitelistCacheRefresh(cache, db, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	stop()
	stop() // 幂等：重复 stop 不能 panic / 不能死锁
	loads, _, _ := cache.Stats()
	if loads < 2 {
		t.Fatalf("后台重载应至少成功加载 2 次，got %d", loads)
	}
}

// ---------------------------------------------------------------------------
// 管理面：创建 / 启停 / 删除 / 列表
// ---------------------------------------------------------------------------

func TestWhitelistCreateHonorsEnabledFlag(t *testing.T) {
	db := newWhitelistTestDB(t)

	// Enabled=true：直接生效。
	enabled, err := CreateWhitelistEntryWithDB(db, CreateWhitelistEntryInput{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", Reason: "办公出口", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create enabled: %v", err)
	}
	if !enabled.Enabled {
		t.Fatal("enabled=true 的规则必须启用")
	}

	// Enabled=false：必须真落成 false（default:true 零值陷阱）。
	disabled, err := CreateWhitelistEntryWithDB(db, CreateWhitelistEntryInput{
		Kind: WhitelistKindIP, Pattern: "203.0.113.8", Reason: "待启用", Enabled: false,
	})
	if err != nil {
		t.Fatalf("create disabled: %v", err)
	}
	if disabled.Enabled {
		t.Fatal("enabled=false 的规则不能落成 true（GORM default:true 零值陷阱）")
	}
	var reloaded entity.WhitelistEntry
	if err := db.Where("id = ?", disabled.ID).Take(&reloaded).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Enabled {
		t.Fatal("库里必须真的存 false，而不只是返回值")
	}
}

func TestWhitelistCreateNormalizesInput(t *testing.T) {
	db := newWhitelistTestDB(t)
	entry, err := CreateWhitelistEntryWithDB(db, CreateWhitelistEntryInput{
		Kind: "  IP ", Pattern: " 198.51.100.77/24 ", MatchType: " CIDR ", Reason: "  网段  ", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if entry.Kind != "ip" {
		t.Fatalf("kind = %q, want ip", entry.Kind)
	}
	if entry.Pattern != "198.51.100.0/24" {
		t.Fatalf("pattern = %q, want 198.51.100.0/24", entry.Pattern)
	}
	if entry.MatchType != WhitelistMatchCIDR {
		t.Fatalf("matchType = %q, want cidr", entry.MatchType)
	}
	if entry.Reason != "网段" {
		t.Fatalf("reason = %q, want 网段（应 TrimSpace）", entry.Reason)
	}
	if entry.ValueHash != WhitelistPatternHashHex("198.51.100.0/24") {
		t.Fatal("valueHash 必须是归一化 pattern 的 sha256")
	}
}

func TestWhitelistCreateRejectsDuplicatePattern(t *testing.T) {
	db := newWhitelistTestDB(t)
	input := CreateWhitelistEntryInput{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", Reason: "重复", Enabled: true,
	}
	if _, err := CreateWhitelistEntryWithDB(db, input); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// 同一模式换一种写法也要被唯一索引挡住（归一化后 value_hash 相同）。
	input.Pattern = "::ffff:203.0.113.7"
	if _, err := CreateWhitelistEntryWithDB(db, input); err == nil {
		t.Fatal("同一模式（v4-mapped 写法）不应重复入库")
	}
}

func TestWhitelistToggleAndDelete(t *testing.T) {
	db := newWhitelistTestDB(t)
	entry := seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", MatchType: WhitelistMatchExact,
	})

	updated, err := UpdateWhitelistEntryEnabledWithDB(db, entry.ID, false)
	if err != nil {
		t.Fatalf("toggle off: %v", err)
	}
	if updated.Enabled {
		t.Fatal("toggle off 后 Enabled 应为 false")
	}
	var reloaded entity.WhitelistEntry
	if err := db.Where("id = ?", entry.ID).Take(&reloaded).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Enabled {
		t.Fatal("库里必须真的存 false（单列 Update，不走 Save）")
	}
	// 幂等：重复设同一状态不报错。
	if _, err := UpdateWhitelistEntryEnabledWithDB(db, entry.ID, false); err != nil {
		t.Fatalf("idempotent toggle: %v", err)
	}
	if _, err := UpdateWhitelistEntryEnabledWithDB(db, 999999, false); !errors.Is(err, ErrWhitelistEntryNotFound) {
		t.Fatalf("不存在的 id err = %v, want ErrWhitelistEntryNotFound", err)
	}

	deleted, err := DeleteWhitelistEntryWithDB(db, entry.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted.ID != entry.ID {
		t.Fatalf("deleted.ID = %d, want %d", deleted.ID, entry.ID)
	}
	if _, err := DeleteWhitelistEntryWithDB(db, entry.ID); !errors.Is(err, ErrWhitelistEntryNotFound) {
		t.Fatalf("重复删除 err = %v, want ErrWhitelistEntryNotFound", err)
	}
}

func TestWhitelistListFiltersAndPaginates(t *testing.T) {
	db := newWhitelistTestDB(t)
	seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "203.0.113.7", MatchType: WhitelistMatchExact, Reason: "办公出口",
	})
	seedWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "198.51.100.0/24", MatchType: WhitelistMatchCIDR, Reason: "机房",
	})
	seedDisabledWhitelistEntry(t, db, entity.WhitelistEntry{
		Kind: WhitelistKindIP, Pattern: "192.0.2.10", MatchType: WhitelistMatchExact, Reason: "合作方",
	})

	all, err := ListWhitelistEntriesWithDB(db, WhitelistEntryListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if all.Total != 3 || len(all.Data) != 3 {
		t.Fatalf("total=%d len=%d, want 3/3", all.Total, len(all.Data))
	}
	// 排序：created_at desc, id desc —— 最后插入的排第一。
	if all.Data[0].Pattern != "192.0.2.10" {
		t.Fatalf("首行 = %q, want 192.0.2.10（最新在前）", all.Data[0].Pattern)
	}

	enabled := true
	onlyEnabled, err := ListWhitelistEntriesWithDB(db, WhitelistEntryListQuery{Enabled: &enabled}, 1, 20)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if onlyEnabled.Total != 2 {
		t.Fatalf("只看启用 total = %d, want 2", onlyEnabled.Total)
	}
	disabled := false
	onlyDisabled, err := ListWhitelistEntriesWithDB(db, WhitelistEntryListQuery{Enabled: &disabled}, 1, 20)
	if err != nil {
		t.Fatalf("list disabled: %v", err)
	}
	if onlyDisabled.Total != 1 {
		t.Fatalf("只看停用 total = %d, want 1", onlyDisabled.Total)
	}

	// 关键词匹配 reason 与脱敏值（不匹配 pattern 原文）。
	byReason, err := ListWhitelistEntriesWithDB(db, WhitelistEntryListQuery{Keyword: "机房"}, 1, 20)
	if err != nil {
		t.Fatalf("list keyword reason: %v", err)
	}
	if byReason.Total != 1 || byReason.Data[0].Reason != "机房" {
		t.Fatalf("按 reason 搜 = %+v", byReason)
	}
	byMasked, err := ListWhitelistEntriesWithDB(db, WhitelistEntryListQuery{Keyword: "203.0.113.*"}, 1, 20)
	if err != nil {
		t.Fatalf("list keyword masked: %v", err)
	}
	if byMasked.Total != 1 {
		t.Fatalf("按脱敏值搜 total = %d, want 1", byMasked.Total)
	}

	// 分页：pageSize=2 → 第 1 页 2 行、第 2 页 1 行，total 恒为 3。
	page1, err := ListWhitelistEntriesWithDB(db, WhitelistEntryListQuery{}, 1, 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Data) != 2 || page1.Total != 3 || page1.Page != 1 || page1.PageSize != 2 {
		t.Fatalf("page1 = %+v", page1)
	}
	page2, err := ListWhitelistEntriesWithDB(db, WhitelistEntryListQuery{}, 2, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Data) != 1 || page2.Total != 3 {
		t.Fatalf("page2 = %+v", page2)
	}

	// 非法 kind 筛选必须报错（而不是「查不到就返回空」）。
	if _, err := ListWhitelistEntriesWithDB(db, WhitelistEntryListQuery{Kind: "phone"}, 1, 20); !errors.Is(err, ErrInvalidWhitelistKind) {
		t.Fatalf("非法 kind err = %v, want ErrInvalidWhitelistKind", err)
	}
}

func TestMaskWhitelistValue(t *testing.T) {
	cases := []struct {
		pattern   string
		matchType string
		want      string
	}{
		{"203.0.113.7", WhitelistMatchExact, "203.0.113.*"},
		{"198.51.100.0/24", WhitelistMatchCIDR, "198.51.100.0/24"},
		{"2001:db8::1", WhitelistMatchExact, "2001:db8:*"},
		{"2001:db8::/32", WhitelistMatchCIDR, "2001:db8::/32"},
	}
	for _, tc := range cases {
		if got := MaskWhitelistValue(WhitelistKindIP, tc.pattern, tc.matchType); got != tc.want {
			t.Fatalf("MaskWhitelistValue(%q,%q) = %q, want %q", tc.pattern, tc.matchType, got, tc.want)
		}
	}
	// 非 ip 粒度兜底。
	if got := MaskWhitelistValue("phone", "13800001111", WhitelistMatchExact); got != "***" {
		t.Fatalf("非 ip 粒度 mask = %q, want ***", got)
	}
	// 非法 IP 兜底（不能返回半个脱敏值）。
	if got := MaskWhitelistValue(WhitelistKindIP, "not-an-ip", WhitelistMatchExact); got != "***" {
		t.Fatalf("非法 IP mask = %q, want ***", got)
	}
}
