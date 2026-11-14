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

// ⚠️ 2026-11-14 域名语法重写（`*.a.com` / `a.com/*`，旧格式全部舍弃）。
//
// 原本这里有两个用例：
//   TestScriptGlobDomainWildcardMatchesDottedSuffixOnly      → 测 scriptDomainHostMatch
//   TestScriptGlobRejectsNonLeadingDomainWildcardAtCompileTime → 测 compileScriptDomainRule
// 那两个函数随旧语法一起被移除（通配符现在由 ScriptDomainFormat 表达，不再是「域名 + 布尔开关」）。
//
// **不是简单删掉**：「通配符只匹配子域、不匹配裸域」与「`*` 只允许最左侧」这两条语义仍然存在，
// 只是搬到了新实现里。它们现在由 script_domain_format_test.go 覆盖：
//   TestScriptDomainWildcardMatchesSubdomainsButNotTheBareDomain  ← 对应上面第一条
//   TestScriptDomainFormatRejectsMalformedStarSyntax              ← 对应上面第二条
// 这条留痕是为了让下一个人知道「覆盖没丢，只是换了文件」，而不是以为被谁删了。

// validateScriptBaseDomain 的语义仍然有效（脚本自身域名不能含通配符），单独钉住。
func TestValidateScriptBaseDomainRejectsWildcardOnScriptItself(t *testing.T) {
	for _, domain := range []string{"*.a.zelly.cn", "a.*.a.com", "*", "*.a.*.b.com"} {
		err := validateScriptBaseDomain(domain)
		if !errors.Is(err, ErrScriptDomainWildcardOnScript) && !errors.Is(err, ErrScriptDomainWildcardPlacement) {
			t.Fatalf("validateScriptBaseDomain(%q) = %v, want wildcard rejection", domain, err)
		}
	}
	// 具体主机名必须放行 —— 否则正常脚本建不出来。
	for _, domain := range []string{"a.zelly.cn", "x.y.zelly.cn", "localhost"} {
		if err := validateScriptBaseDomain(domain); err != nil {
			t.Fatalf("validateScriptBaseDomain(%q) = %v, want nil", domain, err)
		}
	}
}

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

// ─────────────────────── 6) 保留前缀优先于一切通配 ───────────────────────

func TestScriptGlobReservedPrefixOutranksDomainFormat(t *testing.T) {
	// ⚠️ 2026-11-14 域名语法重写后本用例被重写（原 TestScriptGlobReservedPrefixOutranksWildcardRules）。
	//
	// 旧版用 `a.com/**` 造「脏数据」并断言 PathPattern，而用户明确要求「旧格式全部舍弃，
	// 不做兼容」—— 那个前提已被推翻（新语法里 `/**` 根本不是合法形态，PathPattern 字段也删了）。
	//
	// 现在要守的语义**没变**：保留前缀必须优先于域名/路径规则。只是改用新语法表达。
	db := newScriptTestDB(t)
	seed := entity.SystemSetting{Key: ScriptDomainSettingsKey, Value: `{"domains":["a.com/*"]}`}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}
	formats, err := scriptDomainRulesWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(formats) != 1 || formats[0].Kind != ScriptDomainFormatPath || formats[0].Host != "a.com" {
		t.Fatalf("path format should parse to Kind=path Host=a.com, got %+v", formats)
	}
	script := entity.UserScript{
		UserID: 1, VersionID: 1, Name: "fixture", Instruction: "fixture", Parameters: "{}",
		BaseDomain: "a.com", Scheme: "https", UseWildcardDomain: false, Enabled: true,
		ShortCode: "sc", BillingEnabled: true,
	}
	if err := db.Create(&script).Error; err != nil {
		t.Fatal(err)
	}
	// 非保留路径必须放行 —— 证明保留前缀不是「一刀切全拒」。
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/sc"); err != nil {
		t.Fatalf("wildcard format should allow /sc, got %v", err)
	}
	// 保留前缀必须挡下。
	// 用 baseline 清单里确实存在的段（login 在生产由路由表动态注入，测试环境没有）。
	for _, reserved := range []string{"/api/sc", "/adapter-service/sc", "/static/sc"} {
		if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", reserved); !errors.Is(err, ErrScriptNotFound) {
			t.Fatalf("reserved prefix %q must be refused, got %v", reserved, err)
		}
	}
	// 路径穿越进保留前缀同样必须拒 —— 否则 `..` 能绕过前缀判定。
	if _, err := ResolvePublicScriptWithDB(db, "sc.a.com", "/x/../api/sc"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("traversal into a reserved prefix must be refused, got %v", err)
	}
}

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
