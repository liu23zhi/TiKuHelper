package service

// script_glob.go 的独立测试（45-*.md §0.25.2 / §0.25.3，72-*.md §3.2）。
//
// 为什么必须单独存在：script_domain_test.go 只覆盖「域名设置读写 + ValidateScriptDomainWithDB」，
// 段级 glob（`*` = 一段、`**` = 多段）、请求路径规范化、保留前缀枚举与合并语义
// **一条都没有专门用例** —— 改坏实现时既有测试全绿。下面每一类都以「如果实现坏了
// 这条会不会红」为准绳，并配合三次变异验证（见交付报告）。
//
// 全部使用包内真实函数，不手搓 struct 自比；涉及全局保留前缀表的用例一律用
// t.Cleanup 还原，避免污染同包其它测试。

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// ─────────────────────── 1) 域名通配按带点后缀匹配（关键安全面） ───────────────────────

func TestScriptGlobDomainWildcardMatchesDottedSuffixOnly(t *testing.T) {
	// 正例：`*.a.zelly.cn` 覆盖 `x.a.zelly.cn`（以及更深层的子域）。
	for _, host := range []string{"x.a.zelly.cn", "deep.x.a.zelly.cn", "X.A.ZELLY.CN", "x.a.zelly.cn."} {
		if !scriptDomainHostMatch("*.a.zelly.cn", host) {
			t.Fatalf("wildcard should match %q", host)
		}
	}
	// 反例：**这是本测试最重要的一条**。
	// 若实现写成 strings.HasSuffix(host, "a.zelly.cn")（掉了一个点），
	// `evila.zelly.cn` 会命中 —— 一个外部注册者只要买下 evila.zelly.cn 就能冒充
	// `*.a.zelly.cn` 下的脚本公开入口。带点后缀（`.a.zelly.cn`）才能挡住它。
	for _, host := range []string{"evila.zelly.cn", "fooa.zelly.cn", "zelly.cn"} {
		if scriptDomainHostMatch("*.a.zelly.cn", host) {
			t.Fatalf("wildcard must NOT match %q: dotted-suffix boundary was bypassed", host)
		}
	}
	// 通配本身不覆盖「点前没有段」的裸域：`a.zelly.cn` 比后缀 `.a.zelly.cn` 还短。
	if scriptDomainHostMatch("*.a.zelly.cn", "a.zelly.cn") {
		t.Fatal("wildcard must not match its own suffix without a leading label")
	}
	// 精确域名只做全等（大小写不敏感），不得退化成后缀匹配。
	if !scriptDomainHostMatch("a.zelly.cn", "A.Zelly.CN") {
		t.Fatal("exact host compare should be case-insensitive")
	}
	if scriptDomainHostMatch("a.zelly.cn", "x.a.zelly.cn") {
		t.Fatal("exact host must not match a subdomain")
	}
	// 空值不匹配（防止空规则把一切放行）。
	if scriptDomainHostMatch("", "a.zelly.cn") || scriptDomainHostMatch("*.a.zelly.cn", "") {
		t.Fatal("empty pattern or host must not match")
	}
}

// ─────────────────────── 2) 域名 `*` 不允许在中间（录入期拒绝） ───────────────────────

func TestScriptGlobRejectsNonLeadingDomainWildcardAtCompileTime(t *testing.T) {
	// `a.*.a.com` 必须在**规则编译阶段**被拒绝，而不是「匹配时匹配不上」——
	// 后者会让管理员以为规则保存成功、实际永远不生效。
	if _, err := compileScriptDomainRule("a.*.a.com"); !errors.Is(err, ErrScriptDomainWildcardPlacement) {
		t.Fatalf("compileScriptDomainRule(a.*.a.com) = %v, want ErrScriptDomainWildcardPlacement", err)
	}
	for _, host := range []string{"a.*.a.com", "a.*.zelly.cn", "*.a.*.com", "*a.zelly.cn", "a.zelly.*.cn", "*."} {
		if err := validateScriptDomainWildcardPlacement(host); !errors.Is(err, ErrScriptDomainWildcardPlacement) {
			t.Fatalf("validateScriptDomainWildcardPlacement(%q) = %v, want ErrScriptDomainWildcardPlacement", host, err)
		}
	}
	// 合法的最左形态必须放行，否则上面那些断言只是在验证「什么都拒绝」。
	for _, host := range []string{"a.zelly.cn", "*.zelly.cn", "*.a.zelly.cn"} {
		if err := validateScriptDomainWildcardPlacement(host); err != nil {
			t.Fatalf("validateScriptDomainWildcardPlacement(%q) unexpected error: %v", host, err)
		}
	}
	// 脚本自身的域名不能是通配（通配属于管理员的「允许域名列表」）。
	if err := validateScriptBaseDomain("*.a.zelly.cn"); !errors.Is(err, ErrScriptDomainWildcardOnScript) {
		t.Fatalf("validateScriptBaseDomain(wildcard) = %v, want ErrScriptDomainWildcardOnScript", err)
	}
	// 保存配置时（validateScriptDomainEntry → compileScriptDomainRule）也要当场报错。
	if err := validateScriptDomainEntry("a.*.a.com"); !errors.Is(err, ErrScriptDomainWildcardPlacement) {
		t.Fatalf("validateScriptDomainEntry(a.*.a.com) = %v, want ErrScriptDomainWildcardPlacement", err)
	}
	// 真正走一次落库入口，确认拒绝发生在写库之前。
	db := newScriptTestDB(t)
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"a.*.a.com"}, AllowWildcard: true}); !errors.Is(err, ErrScriptDomainWildcardPlacement) {
		t.Fatalf("UpdateScriptDomainSettingsWithDB accepted a mid-domain wildcard: %v", err)
	}
}

// ─────────────────────── 3) 路径 `*` = 恰好一段 ───────────────────────

func TestScriptGlobPathStarMatchesExactlyOneSegment(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/a/*/c", "/a/b/c", true},
		{"/a/*/c", "/a/b/x/c", false}, // * 不是多段
		{"/a/*", "/a/b", true},
		{"/a/*", "/a", false},
		{"/a/*", "/A/b", false}, // 段级比较区分大小写（路径不是域名）
		{"/*", "/a", true},
		{"/*", "/a/b", false},
		{"/*/c", "/a/c", true},
	}
	for _, c := range cases {
		got, err := scriptPathGlobMatch(c.pattern, c.path)
		if err != nil {
			t.Fatalf("scriptPathGlobMatch(%q,%q) unexpected error: %v", c.pattern, c.path, err)
		}
		if got != c.want {
			t.Fatalf("scriptPathGlobMatch(%q,%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
	// `*` 必须作为**独立一段**；段内混写（`a*b`）语义模糊，模式本身非法。
	if _, err := scriptPathPatternSegments("/a/a*b/c"); !errors.Is(err, ErrScriptPathPatternInvalid) {
		t.Fatalf("mixed-segment wildcard should be rejected, got %v", err)
	}
}

// ─────────────────────── 4) 路径 `**` = 任意多段 ───────────────────────

func TestScriptGlobPathDoubleStarMatchesMultipleSegments(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/a/**/c", "/a/b/c", true},
		{"/a/**/c", "/a/b/x/c", true}, // ** 吞多段
		{"/a/**/c", "/a/c", true},      // ** 也能吞零段
		{"/a/**", "/a", true},
		{"/a/**", "/a/b/x/y", true},
		{"/**/c", "/c", true},
		{"/a/**/c", "/a/b/x", false}, // 尾部 c 不能省
		{"/a/**/c", "/b/b/c", false}, // 首段 a 不能变
	}
	for _, c := range cases {
		got, err := scriptPathGlobMatch(c.pattern, c.path)
		if err != nil {
			t.Fatalf("scriptPathGlobMatch(%q,%q) unexpected error: %v", c.pattern, c.path, err)
		}
		if got != c.want {
			t.Fatalf("scriptPathGlobMatch(%q,%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
	// 直接打段级比较器（scriptPathGlobMatch 的叶子），确保多段语义不是被
	// 上层规范化「凑」出来的。
	if !scriptPathSegmentsMatch([]string{"a", "**", "c"}, []string{"a", "b", "x", "c"}) {
		t.Fatal("scriptPathSegmentsMatch must let ** consume three segments")
	}
	if scriptPathSegmentsMatch([]string{"a", "*", "c"}, []string{"a", "b", "x", "c"}) {
		t.Fatal("scriptPathSegmentsMatch must not let * consume two segments")
	}
}

// ─────────────────────── 5) path.Clean 与 `..` 拒绝 ───────────────────────

func TestScriptGlobRejectsPathTraversalBeforeCleaning(t *testing.T) {
	// 关键用例：`/a/../adapter-service/x` 经 path.Clean 会变成 `/adapter-service/x`，
	// 看起来「更安全」，但那会让调用方无法区分「用户显式穿越」和「正常路径」，
	// 且某些规则组合下穿越能把请求送进保留前缀之外的分支。实现要求**报错**。
	if got, err := cleanScriptRequestPath("/a/../adapter-service/x"); !errors.Is(err, ErrScriptPathTraversal) {
		t.Fatalf("cleanScriptRequestPath(/a/../adapter-service/x) = %q,%v; want ErrScriptPathTraversal", got, err)
	}
	for _, raw := range []string{"/..", "/../x", "/a/b/..", "/a/../../etc/passwd", "/a/../b/../c"} {
		if _, err := cleanScriptRequestPath(raw); !errors.Is(err, ErrScriptPathTraversal) {
			t.Fatalf("cleanScriptRequestPath(%q) = %v, want ErrScriptPathTraversal", raw, err)
		}
	}
	// glob 匹配对穿越必须返回 **error**（不是 (false,nil)），否则调用方会当成
	// 「普通不匹配」继续试下一条规则。
	if ok, err := scriptPathGlobMatch("/a/**", "/a/../adapter-service/x"); !errors.Is(err, ErrScriptPathTraversal) {
		t.Fatalf("scriptPathGlobMatch with traversal = %v,%v; want ErrScriptPathTraversal", ok, err)
	}
	// 正常路径仍要按 path.Clean 规范化：尾斜杠、重复斜杠、`.` 段。
	cleans := []struct{ raw, want string }{
		{"", ""},
		{"/", ""},
		{"  /a/b/  ", "/a/b"},
		{"//a//b", "/a/b"},
		{"/a/./b", "/a/b"},
		{"/a/b", "/a/b"},
	}
	for _, c := range cleans {
		got, err := cleanScriptRequestPath(c.raw)
		if err != nil || got != c.want {
			t.Fatalf("cleanScriptRequestPath(%q) = %q,%v; want %q,nil", c.raw, got, err, c.want)
		}
	}
	// 非绝对路径与含反斜杠/CRLF/NUL 的输入是非法而不是穿越。
	for _, raw := range []string{"a/b", "/a\\b", "/a\r\nb", "/a\x00b"} {
		if _, err := cleanScriptRequestPath(raw); !errors.Is(err, ErrScriptPathPatternInvalid) {
			t.Fatalf("cleanScriptRequestPath(%q) = %v, want ErrScriptPathPatternInvalid", raw, err)
		}
	}
	// 模式侧的 `.` / `..` 同样非法。
	if _, err := scriptPathPatternSegments("/a/../b"); !errors.Is(err, ErrScriptPathPatternInvalid) {
		t.Fatal("pattern with .. should be rejected")
	}
	if _, err := scriptPathPatternSegments("a/b"); !errors.Is(err, ErrScriptPathPatternInvalid) {
		t.Fatal("pattern without a leading slash should be rejected")
	}
}

// ─────────────────────── 6) 保留前缀优先于一切通配 ───────────────────────

func TestScriptGlobReservedPrefixOutranksWildcardRules(t *testing.T) {
	if !scriptReservedPrefix("adapter-service") {
		t.Fatal("adapter-service must be reserved")
	}
	for _, value := range []string{"ADAPTER-SERVICE", " adapter-service ", "/adapter-service/", "sqp", "assets"} {
		if !scriptReservedPrefix(value) {
			t.Fatalf("%q should normalize to a reserved prefix", value)
		}
	}
	// 多段值不当作前缀（调用方负责先取第一段）。这是有意的「宁可漏判非法输入」。
	if scriptReservedPrefix("adapter-service/x") || scriptReservedPrefix("") {
		t.Fatal("multi-segment or empty values must not be treated as a prefix")
	}

	// 配置期：首段是 `*`/`**` 或直接命中保留前缀的规则一律拒绝，
	// 否则 `a.com/**` 会吞掉整站 API。
	for _, entry := range []string{"a.com/**", "a.com/*/*", "a.com/adapter-service/*", "a.com/sqp/**"} {
		if err := validateScriptDomainEntry(entry); !errors.Is(err, ErrScriptReservedPrefix) {
			t.Fatalf("validateScriptDomainEntry(%q) = %v, want ErrScriptReservedPrefix", entry, err)
		}
	}
	// 合法规则不能被误杀。
	for _, entry := range []string{"a.com", "a.com/a/*", "a.com/a/**"} {
		if err := validateScriptDomainEntry(entry); err != nil {
			t.Fatalf("validateScriptDomainEntry(%q) unexpected error: %v", entry, err)
		}
	}

	// 运行期：即使库里有「存量脏数据」`a.com/**`（compileScriptDomainRule 不拦保留前缀，
	// validateScriptDomainEntry 才拦），保留前缀也必须先挡住。
	db := newScriptTestDB(t)
	seed := entity.SystemSetting{Key: ScriptDomainSettingsKey, Value: `{"domains":["a.com/**"],"allowWildcard":true}`}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}
	rules, err := scriptDomainRulesWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].PathPattern != "/**" {
		t.Fatalf("dirty wildcard rule should compile, got %+v", rules)
	}
	// 非根路径的 shortCode 取**路径最后一段**（见 ResolvePublicScriptWithDB 第 3 步），
	// 所以 fixture 的 short_code 必须等于请求路径末段，否则测的就不是规则这一层了。
	script := entity.UserScript{
		UserID: 1, VersionID: 1, Name: "fixture", Instruction: "fixture", Parameters: "{}",
		BaseDomain: "a.com", Scheme: "https", UseWildcardDomain: false, Enabled: true,
		ShortCode: "sc", BillingEnabled: true,
	}
	if err := db.Create(&script).Error; err != nil {
		t.Fatal(err)
	}
	// 规则允许 /a/sc（`**` 吞两段），保留前缀则必须挡下 /adapter-service/x。
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/a/sc"); err != nil {
		t.Fatalf("wildcard rule should allow /a/sc, got %v", err)
	}
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/adapter-service/sc"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("reserved prefix must outrank ** rule, got %v", err)
	}
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/a/../adapter-service/sc"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("traversal into a reserved prefix must be refused, got %v", err)
	}
	// 同一台 host 上，规则没覆盖的路径必须被拒 —— 证明放行来自 glob 而不是「配了规则就全通」。
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/b/sc"); err != nil {
		t.Fatalf("`/**` should cover /b/sc too, got %v", err)
	}
}

// ─────────────────────── 7) 保留前缀枚举的完整性 ───────────────────────

// 说明（实测偏差，已在交付报告中标注）：任务书要求「枚举数 ≥ 20」，
// 但实测 buildReservedScriptPrefixes(nil) 只有 baseline 的 9 条，
// 而真实路由表（208 条路由）只贡献 2 个第一段（adapter-service / sqp），
// 合并后仍是 9 —— 因为「前缀」按第一段聚合，200 多条 /adapter-service/* 只算一个。
// 因此「前缀数 ≥ 20」本仓永远不可能成立，写成硬断言只会得到一个恒红的假钉子。
//
// 该断言的真实意图是「枚举时机不对时能被发现」。仓里真正用于这件事的信号是
// internal/api/router.go 的 scannedRoutes（阈值 scriptReservedPrefixWarnBelow=20），
// 它在 internal/api 包里，service 不能反向 import（会形成循环）。
// 于是这里守住三件 service 侧能守的事：
//   a. baseline 清单本身完整且无重复（兜底不会被悄悄删条）；
//   b. 合并语义是 merge 而不是 replace —— 枚举不完整时兜底不会丢；
//   c. 快照与查询结果一致（诊断接口不会骗人）。
func TestScriptGlobReservedPrefixEnumerationStaysComplete(t *testing.T) {
	t.Cleanup(func() { SetReservedScriptPrefixes(nil) })

	baseline := buildReservedScriptPrefixes(nil)
	if len(baseline) < len(baselineReservedScriptPrefixes) {
		t.Fatalf("buildReservedScriptPrefixes dropped entries: %d < %d", len(baseline), len(baselineReservedScriptPrefixes))
	}
	for _, prefix := range baselineReservedScriptPrefixes {
		if !baseline[prefix] {
			t.Fatalf("baseline prefix %q missing from the built set", prefix)
		}
	}
	// 兜底清单必须覆盖四类固定入口：API、静态资源、gin 自带探针、本仓入口。
	for _, required := range []string{"adapter-service", "sqp", "assets", "static", "health", "metrics"} {
		if !scriptReservedPrefix(required) {
			t.Fatalf("required reserved prefix %q is not protected", required)
		}
	}
	// 每个条目都必须是单段且已归一化（多段/空值会被 normalize 丢掉，等于没保护）。
	seen := map[string]bool{}
	for _, prefix := range baselineReservedScriptPrefixes {
		if prefix == "" || strings.Contains(prefix, "/") || prefix != strings.ToLower(strings.TrimSpace(prefix)) {
			t.Fatalf("baseline prefix %q is not a normalized single segment", prefix)
		}
		if seen[prefix] {
			t.Fatalf("duplicate baseline prefix %q", prefix)
		}
		seen[prefix] = true
	}

	// 合并语义：路由枚举只是「启动快照」，绝不能替换掉兜底清单。
	SetReservedScriptPrefixes([]string{"route-one", "route-two"})
	if !scriptReservedPrefix("route-one") || !scriptReservedPrefix("route-two") {
		t.Fatal("enumerated route prefixes must be added")
	}
	for _, prefix := range baselineReservedScriptPrefixes {
		if !scriptReservedPrefix(prefix) {
			t.Fatalf("merge dropped baseline prefix %q — a partial route scan would silently unprotect it", prefix)
		}
	}
	snapshot := ScriptReservedPrefixes()
	if len(snapshot) != len(baselineReservedScriptPrefixes)+2 {
		t.Fatalf("snapshot length = %d, want %d", len(snapshot), len(baselineReservedScriptPrefixes)+2)
	}
	if !sort.StringsAreSorted(snapshot) {
		t.Fatalf("snapshot must be sorted, got %v", snapshot)
	}
	if len(snapshot) != len(buildReservedScriptPrefixes(snapshot)) {
		t.Fatalf("snapshot contains entries normalize would drop: %v", snapshot)
	}
}

// ─────────────────────── 8) SetReservedScriptPrefixes 生效 ───────────────────────

func TestScriptGlobSetReservedScriptPrefixesTakesEffect(t *testing.T) {
	t.Cleanup(func() { SetReservedScriptPrefixes(nil) })

	if scriptReservedPrefix("foo") {
		t.Fatal("foo must not be reserved before the call")
	}
	SetReservedScriptPrefixes([]string{"foo", "bar"})
	if !scriptReservedPrefix("foo") || !scriptReservedPrefix("bar") {
		t.Fatal("SetReservedScriptPrefixes did not take effect")
	}
	// 归一化：大小写/空白/斜杠都收敛到同一前缀。
	for _, value := range []string{"FOO", " foo ", "/foo/"} {
		if !scriptReservedPrefix(value) {
			t.Fatalf("%q should normalize to the reserved prefix foo", value)
		}
	}
	// 非法条目（多段 / 空）被忽略，且不会因为「有一个非法条目」导致整批失效。
	SetReservedScriptPrefixes([]string{"baz", "a/b", "  "})
	if !scriptReservedPrefix("baz") {
		t.Fatal("a valid entry must survive next to an invalid one")
	}
	if scriptReservedPrefix("a/b") || scriptReservedPrefix("a") {
		t.Fatal("multi-segment input must be dropped, not split into prefixes")
	}
	// 「合并」指的是**与兜底清单合并**，不是与上一次调用累积：
	// SetReservedScriptPrefixes 每次都用 buildReservedScriptPrefixes(prefixes) 重建，
	// 因此第二次调用会用新列表覆盖第一次的追加项。
	// 这个语义是有意的：本仓只在 SetAPIRouter 末尾调用一次（启动时快照），
	// 若改成累积，将来热重载路由会无限膨胀且无法撤销。这里把它钉住，防止被「顺手加固」改坏。
	SetReservedScriptPrefixes([]string{"qux"})
	if !scriptReservedPrefix("qux") {
		t.Fatal("the latest call must take effect")
	}
	if scriptReservedPrefix("baz") {
		t.Fatal("extras from a previous call are replaced, not accumulated — that is the documented contract")
	}
	for _, prefix := range baselineReservedScriptPrefixes {
		if !scriptReservedPrefix(prefix) {
			t.Fatalf("baseline prefix %q must survive every merge", prefix)
		}
	}
	// 还原后必须回到「只有兜底清单」的状态，否则会污染同包其它测试
	// （CreateUserScriptWithDB 会调 ensureScriptDomainRulePrefixAvailable）。
	SetReservedScriptPrefixes(nil)
	if scriptReservedPrefix("foo") || scriptReservedPrefix("baz") || scriptReservedPrefix("qux") {
		t.Fatal("restore did not clear the temporary prefixes")
	}
	if !scriptReservedPrefix("adapter-service") {
		t.Fatal("restore must keep the baseline reserved prefixes")
	}
}
