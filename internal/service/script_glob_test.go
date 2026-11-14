package service

// script_glob.go 的测试（语法重写后）。
//
// 本文件只覆盖**与域名格式无关**的三块：
//  1. 段级路径 glob（`*` = 一段、`**` = 多段）—— 它是保留前缀与公开路径解析的底层；
//  2. path.Clean 与 `..` 拒绝；
//  3. 保留前缀（`/api` `/sqp` `/assets` …）优先于一切用户配置。
//
// 域名格式的解析/匹配/拼装全部在 script_domain_format_test.go，那边是主战场。
// 全部使用包内真实函数，不手搓 struct 自比；涉及全局保留前缀表的用例一律用
// t.Cleanup 还原，避免污染同包其它测试。

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// ─────────────────────── 1) 路径 `*` = 恰好一段 ───────────────────────

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

// ─────────────────────── 2) 路径 `**` = 任意多段 ───────────────────────

func TestScriptGlobPathDoubleStarMatchesMultipleSegments(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/a/**/c", "/a/b/c", true},
		{"/a/**/c", "/a/b/x/c", true}, // ** 吞多段
		{"/a/**/c", "/a/c", true},     // ** 也能吞零段
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
	// 直接打段级比较器（叶子），确保多段语义不是被上层规范化「凑」出来的。
	if !scriptPathSegmentsMatch([]string{"a", "**", "c"}, []string{"a", "b", "x", "c"}) {
		t.Fatal("scriptPathSegmentsMatch must let ** consume three segments")
	}
	if scriptPathSegmentsMatch([]string{"a", "*", "c"}, []string{"a", "b", "x", "c"}) {
		t.Fatal("scriptPathSegmentsMatch must not let * consume two segments")
	}
}

// ─────────────────────── 3) path.Clean 与 `..` 拒绝 ───────────────────────

func TestScriptGlobRejectsPathTraversalBeforeCleaning(t *testing.T) {
	// 关键用例：`/a/../api/x` 经 path.Clean 会变成 `/api/x`，
	// 看起来「更安全」，但那会让调用方无法区分「用户显式穿越」和「正常路径」，
	// 且某些规则组合下穿越能把请求送进保留前缀之外的分支。实现要求**报错**。
	if got, err := cleanScriptRequestPath("/a/../api/x"); !errors.Is(err, ErrScriptPathTraversal) {
		t.Fatalf("cleanScriptRequestPath(/a/../api/x) = %q,%v; want ErrScriptPathTraversal", got, err)
	}
	for _, raw := range []string{"/..", "/../x", "/a/b/..", "/a/../../etc/passwd", "/a/../b/../c"} {
		if _, err := cleanScriptRequestPath(raw); !errors.Is(err, ErrScriptPathTraversal) {
			t.Fatalf("cleanScriptRequestPath(%q) = %v, want ErrScriptPathTraversal", raw, err)
		}
	}
	// glob 匹配对穿越必须返回 **error**（不是 (false,nil)），否则调用方会当成
	// 「普通不匹配」继续试下一条规则。
	if ok, err := scriptPathGlobMatch("/a/**", "/a/../api/x"); !errors.Is(err, ErrScriptPathTraversal) {
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

// ─────────────────────── 4) 保留前缀优先于一切通配 ───────────────────────

func TestScriptGlobReservedPrefixOutranksWildcardRules(t *testing.T) {
	if !scriptReservedPrefix("adapter-service") {
		t.Fatal("adapter-service must be reserved")
	}
	for _, value := range []string{"ADAPTER-SERVICE", " adapter-service ", "/api/", "sqp", "assets"} {
		if !scriptReservedPrefix(value) {
			t.Fatalf("%q should normalize to a reserved prefix", value)
		}
	}
	// 多段值不当作前缀（调用方负责先取第一段）。这是有意的「宁可漏判非法输入」。
	if scriptReservedPrefix("adapter-service/x") || scriptReservedPrefix("") {
		t.Fatal("multi-segment or empty values must not be treated as a prefix")
	}

	// 运行期：即使管理员配了 `a.com/*`（它天然覆盖 `/api/...`），
	// 保留前缀也必须先挡住。这是「保留前缀优先于一切用户配置」的核心判据。
	db := newScriptTestDB(t)
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"a.com/*"}}); err != nil {
		t.Fatal(err)
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
	// 规则允许 /a/sc（`a.com/*` 覆盖多段），保留前缀则必须挡下 /api/sc。
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/a/sc"); err != nil {
		t.Fatalf("a.com/* rule should allow /a/sc, got %v", err)
	}
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/api/sc"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("reserved prefix must outrank the wildcard rule, got %v", err)
	}
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/a/../api/sc"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("traversal into a reserved prefix must be refused, got %v", err)
	}
	// 同一台 host 上，多段路径必须继续被 `a.com/*` 放行 ——
	// 证明放行来自规则而不是「配了规则就全通」。
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/b/x/sc"); err != nil {
		t.Fatalf("a.com/* should cover /b/x/sc too, got %v", err)
	}
}

func TestScriptGlobEmptyDomainListAllowsOnlySingleSegmentPaths(t *testing.T) {
	// 列表为空 = 不限制域名，但多段公开路径仍要求显式配置 —— 沿用上一版的安全取向。
	db := newScriptTestDB(t)
	script := entity.UserScript{
		UserID: 1, VersionID: 1, Name: "fixture", Instruction: "fixture", Parameters: "{}",
		BaseDomain: "a.com", Scheme: "https", UseWildcardDomain: false, Enabled: true,
		ShortCode: "sc", BillingEnabled: true,
	}
	if err := db.Create(&script).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/sc"); err != nil {
		t.Fatalf("single-segment path should be allowed with an empty list, got %v", err)
	}
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/a/sc"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("multi-segment path must need an explicit rule, got %v", err)
	}
}

// ─────────────────────── 5) 保留前缀枚举的完整性 ───────────────────────

// 说明（实测偏差，已在交付报告中标注）：任务书要求「枚举数 ≥ 20」，
// 但实测 buildReservedScriptPrefixes(nil) 只有 baseline 的 9 条，
// 而真实路由表（208 条路由）只贡献 2 个第一段（adapter-service / sqp），
// 合并后仍是 9 —— 因为「前缀」按第一段聚合，200 多条 /api/* 只算一个。
// 因此「前缀数 ≥ 20」本仓永远不可能成立，写成硬断言只会得到一个恒红的假钉子。
//
// ⚠️ 不要「修正」成 >= 20：数字 20 属于 internal/api 的 scriptReservedPrefixWarnBelow，
// 它比的是 **scannedRoutes（路由条数）**，不是前缀数。
// 该断言的真实意图是「枚举时机不对时能被发现」，本文件守住 service 侧能守的三件事：
//
//	a. baseline 清单本身完整且无重复（兜底不会被悄悄删条）；
//	b. 合并语义是 merge 而不是 replace —— 枚举不完整时兜底不会丢；
//	c. 快照与查询结果一致（诊断接口不会骗人）。
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

// ─────────────────────── 6) SetReservedScriptPrefixes 生效 ───────────────────────

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
	// 因此第二次调用会用新列表覆盖第一次的追加项。这个语义是有意的：
	// 本仓只在 SetAPIRouter 末尾调用一次（启动时快照），若改成累积，
	// 将来热重载路由会无限膨胀且无法撤销。这里把它钉住，防止被「顺手加固」改坏。
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
