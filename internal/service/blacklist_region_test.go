package service

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// BL-4 地区黑名单：四级匹配（46-*.md §2.4 + 45-*.md §0.4 Q-B3）
// ---------------------------------------------------------------------------

// seedBlacklistRegionRow 落一条 kind='region' 的规则（走真实库，不手工构造快照）。
//
// 用 CreateBlacklistEntryWithDB 而不是 db.Create：前者是**新建规则的唯一入口**，
// 归一化/哈希/唯一索引都由它保证。若哪天它又被改成拒收 region，这条用例会立刻红。
func seedBlacklistRegionRow(t *testing.T, db *gorm.DB, pattern string, reason string) entity.BlacklistEntry {
	t.Helper()
	entry, err := CreateBlacklistEntryWithDB(db, CreateBlacklistEntryInput{
		Kind:    BlacklistKindRegion,
		Pattern: pattern,
		Reason:  reason,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create region rule %q: %v", pattern, err)
	}
	return entry
}

// loadRegionSnapshot 走真实加载路径（LoadBlacklistSnapshot），返回区域规则就位的快照。
func loadRegionSnapshot(t *testing.T, db *gorm.DB) *BlacklistSnapshot {
	t.Helper()
	setBlacklistEnabled(t, db, "true")
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snapshot := cache.Snapshot()
	if snapshot == nil {
		t.Fatal("snapshot must not be nil after a successful reload")
	}
	return snapshot
}

// ---------------------------------------------------------------------------
// 1. 语法解析：规范写法与拒绝项
// ---------------------------------------------------------------------------

func TestParseBlacklistRegionPatternCanonical(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantGeo  string
		wantCity string
		wantOp   string
		wantText string
	}{
		{"country only", "美国", "美国", "", "", "美国"},
		{"province only", "云南", "云南", "", "", "云南"},
		{"province+city", "云南 玉溪", "云南", "玉溪", "", "云南 玉溪"},
		{"province+city+operator", "云南 玉溪 移动", "云南", "玉溪", "移动", "云南 玉溪 移动"},
		{"province+any city+operator", "云南 * 移动", "云南", "", "移动", "云南 * 移动"},
		{"operator only shorthand", "*移动", "", "", "移动", "*移动"},
		{"operator only via full form", "* * 移动", "", "", "移动", "*移动"},
		{"trailing wildcards trimmed", "云南 * *", "云南", "", "", "云南"},
		{"full-width space separated", "云南　玉溪　移动", "云南", "玉溪", "移动", "云南 玉溪 移动"},
		{"extra spaces collapsed", "  云南   玉溪  ", "云南", "玉溪", "", "云南 玉溪"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseBlacklistRegionPattern(tc.input)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.input, err)
			}
			if got.Geo != tc.wantGeo || got.City != tc.wantCity || got.Operator != tc.wantOp {
				t.Fatalf("parsed %q = %+v, want geo=%q city=%q op=%q",
					tc.input, got, tc.wantGeo, tc.wantCity, tc.wantOp)
			}
			if got.Canonical != tc.wantText {
				t.Fatalf("canonical = %q, want %q", got.Canonical, tc.wantText)
			}
			if got.IsWildcard() {
				t.Fatalf("parsed %q must not be a full wildcard", tc.input)
			}
		})
	}
}

func TestParseBlacklistRegionPatternRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"spaces only", "   "},
		{"in-segment wildcard geo", "云*南"},
		{"in-segment wildcard city", "云南 玉*溪"},
		{"in-segment wildcard operator", "云南 玉溪 移*动"},
		{"all wildcard single", "*"},
		{"all wildcard full", "* * *"},
		{"too many segments", "中国 云南 玉溪 移动"},
		{"overlong segment", strings.Repeat("云", blacklistRegionSegmentMaxRunes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseBlacklistRegionPattern(tc.input); err == nil {
				t.Fatalf("%q must be rejected", tc.input)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. 归属地解析：段数自适应 + 直辖市 + 占位值
// ---------------------------------------------------------------------------

func TestParseBlacklistRegionLocation(t *testing.T) {
	cases := []struct {
		name     string
		region   string
		llc      string
		isp      string
		want     BlacklistRegionLocation
		wantZero bool
	}{
		{
			name:   "three segments",
			region: "中国 广东 中山", llc: "移动",
			want: BlacklistRegionLocation{Country: "中国", Province: "广东", City: "中山", Operator: "移动"},
		},
		{
			name:   "two segments municipality city==province",
			region: "中国 北京",
			want:   BlacklistRegionLocation{Country: "中国", Province: "北京", City: "北京"},
		},
		{
			name:   "three segments municipality province==city",
			region: "中国 北京 北京",
			want:   BlacklistRegionLocation{Country: "中国", Province: "北京", City: "北京"},
		},
		{
			name:   "overseas single segment",
			region: "美国",
			want:   BlacklistRegionLocation{Country: "美国"},
		},
		{
			name:   "operator falls back to isp when llc is placeholder",
			region: "中国 广东 中山", llc: "N/A", isp: "中国移动",
			want: BlacklistRegionLocation{Country: "中国", Province: "广东", City: "中山", Operator: "中国移动"},
		},
		{
			name:   "operator empty when both placeholder",
			region: "本地地址", llc: "N/A", isp: "N/A",
			want: BlacklistRegionLocation{Country: "本地地址"},
		},
		{
			name:   "placeholder segments dropped before counting",
			region: "中国 N/A 中山", llc: "移动",
			// 剔除 N/A 后只剩 2 段 → 走直辖市分支（city=province=中山）。
			// 这条断言的是「占位值不参与段位推断」，不是「中山是直辖市」。
			want: BlacklistRegionLocation{Country: "中国", Province: "中山", City: "中山", Operator: "移动"},
		},
		{
			name:   "empty region and operator",
			region: "   ", llc: "N/A", isp: "",
			wantZero: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseBlacklistRegionLocation(tc.region, tc.llc, tc.isp)
			if tc.wantZero {
				if !got.IsZero() {
					t.Fatalf("expected zero location, got %+v", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("location = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. 四级命中 / 不命中（行为级：真实库 → 真实加载 → 真实判定）
// ---------------------------------------------------------------------------

func TestBlacklistRegionMatchFourLevels(t *testing.T) {
	db := newBlacklistTestDB(t)
	province := seedBlacklistRegionRow(t, db, "云南", "省级规则")
	city := seedBlacklistRegionRow(t, db, "广东 中山", "市级规则")
	cityOperator := seedBlacklistRegionRow(t, db, "广东 深圳 移动", "某市某运营商")
	operatorOnly := seedBlacklistRegionRow(t, db, "*联通", "跨省运营商")
	snapshot := loadRegionSnapshot(t, db)

	cases := []struct {
		name       string
		region     string
		llc        string
		isp        string
		wantHit    bool
		wantEntry  int64
		wantReason string
	}{
		// 省级规则：云南省内任意市、任意运营商都命中。
		{"province exact", "中国 云南 玉溪", "移动", "", true, province.ID, "省级规则"},
		{"province other city", "中国 云南 昆明", "电信", "", true, province.ID, "省级规则"},
		{"province as sole segment", "云南", "", "", true, province.ID, "省级规则"},
		// 不误伤同省... 反过来：别的省一律不命中省级规则。
		{"other province not matched", "中国 广西 南宁", "移动", "", false, 0, ""},

		// 市级规则：该市所有运营商命中，同省他市不命中。
		{"city all operators", "中国 广东 中山", "电信", "", true, city.ID, "市级规则"},
		{"city other operator", "中国 广东 中山", "移动", "", true, city.ID, "市级规则"},
		{"same province other city not matched", "中国 广东 佛山", "移动", "", false, 0, ""},

		// 某市的某运营商：换运营商或换城市都不命中。
		{"city+operator exact", "中国 广东 深圳", "移动", "", true, cityOperator.ID, "某市某运营商"},
		{"city+operator wrong operator", "中国 广东 深圳", "电信", "", false, 0, ""},
		{"city+operator wrong city", "中国 广东 珠海", "移动", "", false, 0, ""},
		{"city+operator other province same city name", "中国 广西 深圳", "移动", "", false, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match, hit := snapshot.MatchRegionLocation(tc.region, tc.llc, tc.isp)
			if hit != tc.wantHit {
				t.Fatalf("hit = %v, want %v (region=%q llc=%q isp=%q)", hit, tc.wantHit, tc.region, tc.llc, tc.isp)
			}
			if !tc.wantHit {
				return
			}
			if match.EntryID != tc.wantEntry {
				t.Fatalf("entryID = %d, want %d", match.EntryID, tc.wantEntry)
			}
			if match.Kind != BlacklistKindRegion {
				t.Fatalf("kind = %q, want region", match.Kind)
			}
			if match.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", match.Reason, tc.wantReason)
			}
		})
	}
	_ = operatorOnly
}

// 直辖市：`中国 北京`（2 段）与 `中国 北京 北京`（3 段）解析后必须等价，
// 且「北京」规则与「北京 北京」规则对两种写法都命中。
func TestBlacklistRegionMunicipality(t *testing.T) {
	db := newBlacklistTestDB(t)
	beijing := seedBlacklistRegionRow(t, db, "北京", "直辖市省级规则")
	snapshot := loadRegionSnapshot(t, db)

	writings := []struct {
		name   string
		region string
	}{
		{"two segments", "中国 北京"},
		{"three segments duplicate", "中国 北京 北京"},
	}
	for _, writing := range writings {
		t.Run(writing.name, func(t *testing.T) {
			match, hit := snapshot.MatchRegionLocation(writing.region, "移动", "")
			if !hit {
				t.Fatalf("北京 rule must match %q", writing.region)
			}
			if match.EntryID != beijing.ID {
				t.Fatalf("entryID = %d, want %d", match.EntryID, beijing.ID)
			}
		})
	}

	// 反向：写成 `北京 北京` 的规则对两种写法也都命中。
	// ⚠️ 必须换一个测试函数才有独立的库：newBlacklistTestDB 用 t.Name() 拼库名，
	// 同一个测试内两次调用会拿到**同一个**共享内存库
	// （`file:...?mode=memory&cache=shared` 在同进程内同名即共享，跨用例会互相污染）。
	if _, hit := snapshot.MatchRegionLocation("中国 河北 保定", "", ""); hit {
		t.Fatal("北京 rule must not match 河北 保定")
	}
}

// 直辖市的另一种规则写法：`北京 北京`（省=市）对 2 段与 3 段两种归属地都命中。
func TestBlacklistRegionMunicipalityCityRule(t *testing.T) {
	db := newBlacklistTestDB(t)
	beijingCity := seedBlacklistRegionRow(t, db, "北京 北京", "直辖市市级写法")
	snapshot := loadRegionSnapshot(t, db)

	for _, region := range []string{"中国 北京", "中国 北京 北京"} {
		t.Run(region, func(t *testing.T) {
			match, hit := snapshot.MatchRegionLocation(region, "", "")
			if !hit {
				t.Fatalf("北京 北京 rule must match %q", region)
			}
			if match.EntryID != beijingCity.ID {
				t.Fatalf("entryID = %d, want %d", match.EntryID, beijingCity.ID)
			}
		})
	}
}

// 运营商通配：`*联通` 跨地域命中；`云南 * 移动` 是「云南境内任意城市的移动」。
func TestBlacklistRegionOperatorWildcards(t *testing.T) {
	db := newBlacklistTestDB(t)
	unicom := seedBlacklistRegionRow(t, db, "*联通", "跨省联通")
	yunnanMobile := seedBlacklistRegionRow(t, db, "云南 * 移动", "云南境内移动")
	snapshot := loadRegionSnapshot(t, db)

	cases := []struct {
		name      string
		region    string
		llc       string
		isp       string
		wantHit   bool
		wantEntry int64
	}{
		{"unicom anywhere", "中国 广东 中山", "联通", "", true, unicom.ID},
		{"unicom overseas", "美国", "联通", "", true, unicom.ID},
		{"unicom via isp fallback", "中国 广东 中山", "N/A", "联通", true, unicom.ID},
		{"not unicom", "中国 广东 中山", "移动", "", false, 0},
		{"yunnan mobile any city", "中国 云南 昆明", "移动", "", true, yunnanMobile.ID},
		{"yunnan mobile other city", "中国 云南 玉溪", "移动", "", true, yunnanMobile.ID},
		{"yunnan telecom not matched", "中国 云南 昆明", "电信", "", false, 0},
		{"other province mobile not matched", "中国 广东 中山", "移动", "", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match, hit := snapshot.MatchRegionLocation(tc.region, tc.llc, tc.isp)
			if hit != tc.wantHit {
				t.Fatalf("hit = %v, want %v", hit, tc.wantHit)
			}
			if hit && match.EntryID != tc.wantEntry {
				t.Fatalf("entryID = %d, want %d", match.EntryID, tc.wantEntry)
			}
		})
	}
}

// 占位值 / 空归属地绝不命中任何规则 —— 否则 `*联通` 这类通配规则会在
// 「上游没查到归属地」时对所有人成立，变成全站 403。
func TestBlacklistRegionPlaceholderNeverMatches(t *testing.T) {
	db := newBlacklistTestDB(t)
	seedBlacklistRegionRow(t, db, "*联通", "跨省联通")
	seedBlacklistRegionRow(t, db, "美国", "国家级")
	seedBlacklistRegionRow(t, db, "云南", "省级")
	snapshot := loadRegionSnapshot(t, db)

	regions := []string{"", "   ", "N/A", "N/A N/A", "未知", "-", "本地地址", "私有地址"}
	for _, region := range regions {
		t.Run("region="+region, func(t *testing.T) {
			if match, hit := snapshot.MatchRegionLocation(region, "N/A", "N/A"); hit {
				t.Fatalf("placeholder region %q must not match anything, got %+v", region, match)
			}
		})
	}
	// 反过来：运营商是占位值时，只带运营商的规则不命中；但不限运营商的规则照常命中。
	if _, hit := snapshot.MatchRegionLocation("中国 云南 玉溪", "N/A", "N/A"); !hit {
		t.Fatal("a province rule must still match when the operator is unknown")
	}
	if _, hit := snapshot.MatchRegionLocation("中国 云南 玉溪", "", ""); !hit {
		t.Fatal("a province rule must match when the operator is empty")
	}
}

// MatchRegion(address) 便利入口：吃的是展示串 `{region} {运营商}`。
func TestBlacklistRegionMatchAddress(t *testing.T) {
	db := newBlacklistTestDB(t)
	seedBlacklistRegionRow(t, db, "云南", "省级规则")
	seedBlacklistRegionRow(t, db, "美国", "国家级")
	seedBlacklistRegionRow(t, db, "广东 深圳 移动", "某市某运营商")
	snapshot := loadRegionSnapshot(t, db)

	hits := []string{"中国 云南 玉溪 移动", "中国 云南 昆明 电信", "美国", "中国 广东 深圳 移动"}
	for _, address := range hits {
		if _, hit := snapshot.MatchRegion(address); !hit {
			t.Fatalf("address %q must hit", address)
		}
	}
	misses := []string{"", "  ", "中国 广东 中山 联通", "中国 广东 深圳 电信", "N/A", "私有地址 N/A"}
	for _, address := range misses {
		if match, hit := snapshot.MatchRegion(address); hit {
			t.Fatalf("address %q must not hit, got %+v", address, match)
		}
	}
}

// 同省他市不误伤：省级规则命中全省，而市级规则只命中本市的**所有**运营商，
// 且不因「城市名是省名的子串」而误命中（§2.4 明确否掉的字符串前缀匹配）。
func TestBlacklistRegionProvinceRuleDoesNotLeakToOtherProvinces(t *testing.T) {
	db := newBlacklistTestDB(t)
	seedBlacklistRegionRow(t, db, "云南", "省级规则")
	snapshot := loadRegionSnapshot(t, db)

	if _, hit := snapshot.MatchRegionLocation("中国 云南 玉溪", "", ""); !hit {
		t.Fatal("云南省内必须命中")
	}
	for _, region := range []string{"中国 云 玉溪", "中国 云南西 玉溪", "中国 广西 玉林", "中国 湖南 长沙"} {
		if match, hit := snapshot.MatchRegionLocation(region, "", ""); hit {
			t.Fatalf("region %q must not match the 云南 rule (prefix matching is explicitly forbidden by §2.4), got %+v",
				region, match)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. 接入：开关关闭零查询 / 快照不可用 fail-open / 命中 blocked
// ---------------------------------------------------------------------------

// regionGateFixture 组装一次「真实库 + 真实归属地缓存行 + 计数桩」的判定环境。
type regionGateFixture struct {
	db     *gorm.DB
	cache  *BlacklistCache
	lookup atomic.Int64
}

// newRegionGateFixture 建库、种规则、种归属地缓存行，并给出带计数的查表器。
//
// 归属地缓存行**直接写库**（模拟「管理员在归属地页面查过一次」），
// 这样判定路径无须任何上游调用 —— 这正是 §4 要求的「只读缓存、不主动查询」。
func newRegionGateFixture(t *testing.T, enabled bool, rules []string, cached map[string]entity.LocationCache) *regionGateFixture {
	t.Helper()
	db := newBlacklistTestDB(t)
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatalf("migrate location cache: %v", err)
	}
	if enabled {
		setBlacklistEnabled(t, db, "true")
	}
	for _, pattern := range rules {
		seedBlacklistRegionRow(t, db, pattern, "门禁规则")
	}
	for key, row := range cached {
		row.Kind = entity.LocationKindIP
		row.LookupKey = key
		if row.Address == "" && row.RawJSON == "" {
			t.Fatalf("cached row %q needs address or raw_json", key)
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed location row %q: %v", key, err)
		}
	}
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	return &regionGateFixture{db: db, cache: cache}
}

// lookup 是带计数的查表器包装：内部走真实的 NewBlacklistRegionCacheLooker。
func (f *regionGateFixture) lookupWithCounter() BlacklistRegionLooker {
	inner := NewBlacklistRegionCacheLooker(f.db)
	return func(clientIP string) (BlacklistRegionLocation, bool) {
		f.lookup.Add(1)
		return inner(clientIP)
	}
}

// buildIPLocationRawJSON 造一条与上游返回同构的 RawJSON。
func buildIPLocationRawJSON(t *testing.T, ip string, region string, llc string, isp string) string {
	t.Helper()
	payload := map[string]any{"ip": ip, "region": region, "llc": llc, "isp": isp}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal raw json: %v", err)
	}
	return string(raw)
}

// 开关关闭（默认）时：就算库里有地区规则、归属地缓存也已经存在，
// 也**一次归属地读都不发生**，请求照常放行。
//
// 这条是 §7 验收「开关默认 false → 全网行为与今天完全一致（零额外查询）」在
// 地区判定上的落点：只看「没返回 403」无法区分「没查」与「查了没命中」。
func TestEvaluateBlacklistRegionSwitchOffIssuesZeroLookups(t *testing.T) {
	fixture := newRegionGateFixture(t, false, []string{"云南"}, map[string]entity.LocationCache{
		"203.0.113.7": {Address: "云南玉溪 移动", RawJSON: buildIPLocationRawJSON(t, "203.0.113.7", "中国 云南 玉溪", "移动", "")},
	})
	if fixture.cache.Enabled() {
		t.Fatal("precondition: switch must be off (no blacklist_enabled row)")
	}
	looker := fixture.lookupWithCounter()

	for i := 0; i < 20; i++ {
		match, outcome := EvaluateBlacklistRegion(fixture.cache, looker, "203.0.113.7")
		if outcome != BlacklistRegionSkipped {
			t.Fatalf("outcome #%d = %v, want skipped", i, outcome)
		}
		if match.EntryID != 0 {
			t.Fatalf("switch off must not produce a match, got %+v", match)
		}
	}
	if got := fixture.lookup.Load(); got != 0 {
		t.Fatalf("switch off must issue zero location lookups, got %d", got)
	}
}

// 开关打开但**一条地区规则都没有**时同样零查询：
// 只开了 IP 黑名单的部署不该为每个请求多付一次归属地读。
func TestEvaluateBlacklistRegionWithoutRegionRulesIssuesZeroLookups(t *testing.T) {
	db := newBlacklistTestDB(t)
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatalf("migrate location cache: %v", err)
	}
	setBlacklistEnabled(t, db, "true")
	// 只种一条 **IP** 规则（不是地区规则）：这正是「只开 IP 黑名单」的部署形态。
	seedBlacklistEntry(t, db, entity.BlacklistEntry{Kind: BlacklistKindIP, Pattern: "203.0.113.7", Enabled: true})
	if err := db.Create(&entity.LocationCache{
		Kind: entity.LocationKindIP, LookupKey: "203.0.113.7",
		Address: "云南玉溪 移动", RawJSON: buildIPLocationRawJSON(t, "203.0.113.7", "中国 云南 玉溪", "移动", ""),
	}).Error; err != nil {
		t.Fatalf("seed location row: %v", err)
	}
	cache := NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !cache.Enabled() {
		t.Fatal("precondition: switch must be on")
	}
	fixture := &regionGateFixture{db: db, cache: cache}
	looker := fixture.lookupWithCounter()
	if _, outcome := EvaluateBlacklistRegion(fixture.cache, looker, "203.0.113.7"); outcome != BlacklistRegionSkipped {
		t.Fatalf("outcome = %v, want skipped (no region rules loaded)", outcome)
	}
	if got := fixture.lookup.Load(); got != 0 {
		t.Fatalf("no region rules must mean zero lookups, got %d", got)
	}
}

// 命中 → Hit + 命中日志的 EntryID/Reason 指向规则；未命中 → Miss。
func TestEvaluateBlacklistRegionHitAndMiss(t *testing.T) {
	fixture := newRegionGateFixture(t, true, []string{"云南"}, map[string]entity.LocationCache{
		"203.0.113.7":  {Address: "云南玉溪 移动", RawJSON: buildIPLocationRawJSON(t, "203.0.113.7", "中国 云南 玉溪", "移动", "")},
		"198.51.100.9": {Address: "美国", RawJSON: buildIPLocationRawJSON(t, "198.51.100.9", "美国", "N/A", "N/A")},
	})
	looker := fixture.lookupWithCounter()

	match, outcome := EvaluateBlacklistRegion(fixture.cache, looker, "203.0.113.7")
	if outcome != BlacklistRegionHit {
		t.Fatalf("outcome = %v, want hit", outcome)
	}
	if match.Kind != BlacklistKindRegion || match.Reason != "门禁规则" {
		t.Fatalf("match = %+v, want region kind with reason", match)
	}
	if match.EntryID == 0 {
		t.Fatal("hit must carry the rule entry id (hit log depends on it)")
	}
	if match.ValueMasked == "" {
		t.Fatal("hit must carry a masked value for the hit log")
	}

	// 没有归属地缓存的 IP：不判定、放行、且**不主动查询**（查表器只读库）。
	missMatch, missOutcome := EvaluateBlacklistRegion(fixture.cache, looker, "192.0.2.55")
	if missOutcome != BlacklistRegionMiss {
		t.Fatalf("outcome = %v, want miss", missOutcome)
	}
	if missMatch.EntryID != 0 {
		t.Fatalf("miss must not produce a match, got %+v", missMatch)
	}

	// 有缓存但不在云南省：Miss。
	if _, outcome := EvaluateBlacklistRegion(fixture.cache, looker, "198.51.100.9"); outcome != BlacklistRegionMiss {
		t.Fatalf("outcome = %v, want miss for a 美国 address", outcome)
	}

	// 三次判定 = 三次查表（每次请求一次点读，无缓存聚合也不重复读）。
	if got := fixture.lookup.Load(); got != 3 {
		t.Fatalf("lookups = %d, want 3 (one per evaluation)", got)
	}
}

// 开关为开但**快照不可用**：必须 fail-open（放行）+ 明确报出 outcome=fail-open，
// 中间件据此写 logged_only 留痕。
func TestEvaluateBlacklistRegionSnapshotMissingFailsOpen(t *testing.T) {
	cache := NewBlacklistCacheWithSwitch(true)
	if !cache.Enabled() {
		t.Fatal("precondition: the probe cache reports enabled")
	}
	if cache.Snapshot() != nil {
		t.Fatal("precondition: the probe cache has no snapshot")
	}
	var lookups atomic.Int64
	looker := func(string) (BlacklistRegionLocation, bool) {
		lookups.Add(1)
		return BlacklistRegionLocation{}, false
	}
	match, outcome := EvaluateBlacklistRegion(cache, looker, "203.0.113.7")
	if outcome != BlacklistRegionFailOpen {
		t.Fatalf("outcome = %v, want fail-open", outcome)
	}
	if match.EntryID != 0 {
		t.Fatalf("fail-open must not report a match, got %+v", match)
	}
	// 快照都没有，连归属地都不该去读。
	if got := lookups.Load(); got != 0 {
		t.Fatalf("fail-open must not read locations, got %d lookups", got)
	}
}

// BlacklistRegionGate 是生产入口：db + clientIP → (match, hit)。
func TestBlacklistRegionGateWithInjectedCache(t *testing.T) {
	fixture := newRegionGateFixture(t, true, []string{"广东 中山"}, map[string]entity.LocationCache{
		"203.0.113.7": {Address: "广东中山 移动", RawJSON: buildIPLocationRawJSON(t, "203.0.113.7", "中国 广东 中山", "移动", "")},
	})
	match, hit := BlacklistRegionGateWith(fixture.cache, NewBlacklistRegionCacheLooker(fixture.db), "203.0.113.7")
	if !hit {
		t.Fatal("广东 中山 rule must hit 中国 广东 中山")
	}
	if match.EntryID == 0 || match.Kind != BlacklistKindRegion {
		t.Fatalf("match = %+v", match)
	}
	// 没有缓存的 IP 与非法 IP 都放行。
	for _, candidate := range []string{"192.0.2.9", "", "not-an-ip"} {
		if _, hit := BlacklistRegionGateWith(fixture.cache, NewBlacklistRegionCacheLooker(fixture.db), candidate); hit {
			t.Fatalf("candidate %q must not hit", candidate)
		}
	}
}

// 只读默认来源：商业增强（commercial）的归属地行不参与地区判定，
// 否则「管理员为了提高展示质量开启商业来源」会顺带改变拦截口径。
func TestBlacklistRegionLookerIgnoresCommercialSource(t *testing.T) {
	db := newBlacklistTestDB(t)
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	setBlacklistEnabled(t, db, "true")
	seedBlacklistRegionRow(t, db, "云南", "省级规则")
	if err := db.Create(&entity.LocationCache{
		Kind: entity.LocationKindIP, LookupKey: "203.0.113.7", Source: "commercial",
		Address: "云南玉溪 移动", RawJSON: buildIPLocationRawJSON(t, "203.0.113.7", "中国 云南 玉溪", "移动", ""),
	}).Error; err != nil {
		t.Fatalf("seed commercial row: %v", err)
	}
	looker := NewBlacklistRegionCacheLooker(db)
	if _, ok := looker("203.0.113.7"); ok {
		t.Fatal("commercial-source location rows must not feed the region gate")
	}

	// 同一 IP 再补一条默认来源的行 → 立刻生效（证明上一条不是因为别的 bug 才不命中）。
	if err := db.Create(&entity.LocationCache{
		Kind: entity.LocationKindIP, LookupKey: "203.0.113.7", Source: "",
		Address: "云南玉溪 移动", RawJSON: buildIPLocationRawJSON(t, "203.0.113.7", "中国 云南 玉溪", "移动", ""),
	}).Error; err != nil {
		t.Fatalf("seed default row: %v", err)
	}
	if _, ok := looker("203.0.113.7"); !ok {
		t.Fatal("the default-source row must be visible to the region gate")
	}
}

// RawJSON 缺失（更早版本写入的行，只有 address 列）时要回退 address 解析，
// 否则历史缓存行的地区规则永远不生效 —— 而运营已经从展示上「看到」了归属地。
func TestBlacklistRegionLookerFallsBackToAddressColumn(t *testing.T) {
	db := newBlacklistTestDB(t)
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&entity.LocationCache{
		Kind: entity.LocationKindIP, LookupKey: "203.0.113.7", Address: "中国 广东 中山",
	}).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	looker := NewBlacklistRegionCacheLooker(db)
	location, ok := looker("203.0.113.7")
	if !ok {
		t.Fatal("a row with only the address column must still be usable")
	}
	if location.Province != "广东" || location.City != "中山" {
		t.Fatalf("location = %+v, want 广东/中山", location)
	}
}
