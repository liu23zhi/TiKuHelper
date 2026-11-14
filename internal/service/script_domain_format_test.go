package service

// 脚本域名新语法（2026-11-14 用户裁决）的行为钉子。
//
// 覆盖三件事，每一件都对应一个「实现写错就会静默放过」的接缝：
//
//  1. 解析：只认 `*.a.com` 与 `a.com/*`，旧格式必须**报错**而不是被跳过；
//  2. 匹配：`*.a.com` 不匹配 `a.com` 本身；`a.com/*` 匹配多段路径、但不匹配根路径；
//  3. 槽位拼装：`*.a.com` 的输入框在**前面**、`a.com/*` 的在**后面**，不能写反。

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// ───────────────────── 1) 新语法解析 ─────────────────────

func TestScriptDomainFormatParsesOnlyStarSyntax(t *testing.T) {
	cases := []struct {
		raw      string
		wantKind string
		wantHost string
	}{
		{"*.a.com", ScriptDomainFormatWildcard, "a.com"},
		{"*.A.COM", ScriptDomainFormatWildcard, "a.com"}, // 归一化为小写
		{"a.com/*", ScriptDomainFormatPath, "a.com"},
		{"a.com/*", ScriptDomainFormatPath, "a.com"},
		{"  *.a.zelly.cn  ", ScriptDomainFormatWildcard, "a.zelly.cn"},
		{"x.example.co.uk/*", ScriptDomainFormatPath, "x.example.co.uk"},
	}
	for _, c := range cases {
		format, ok, err := parseScriptDomainFormat(c.raw)
		if err != nil || !ok {
			t.Fatalf("parseScriptDomainFormat(%q) = ok=%v err=%v, want ok", c.raw, ok, err)
		}
		if format.Kind != c.wantKind || format.Host != c.wantHost {
			t.Fatalf("parseScriptDomainFormat(%q) = %+v, want kind=%s host=%s", c.raw, format, c.wantKind, c.wantHost)
		}
	}
}

func TestScriptDomainFormatRejectsLegacySyntaxWithExplicitError(t *testing.T) {
	// 旧格式必须是 **报错**，不能被当成「跳过」。静默失效会让管理员以为
	// 规则还在生效，这是本轮明确要避免的形态。
	legacy := []string{
		"a.com",           // 裸域名（上一版的主要写法）
		"a.com/a/*",       // 段级 glob 的路径写法
		"a.com/**",        // 多段 glob
		"https://a.com",   // 带 scheme
		"a.com/a/**/x",    // 中间通配
		"*.a.com/",        // 尾部斜杠
		"a.com/a.com",     // 无 `*` 的路径
		"regex:^a\\.com$", // 正则（从未支持，但管理员会试）
	}
	for _, raw := range legacy {
		if _, _, err := parseScriptDomainFormat(raw); !errors.Is(err, ErrScriptDomainFormatLegacy) {
			t.Fatalf("parseScriptDomainFormat(%q) err = %v, want ErrScriptDomainFormatLegacy", raw, err)
		}
	}
}

func TestScriptDomainFormatRejectsMalformedStarSyntax(t *testing.T) {
	// 有 `*` 但形态不对：必须报 ErrScriptDomainFormatInvalid（不是 Legacy）。
	invalid := []string{
		"a.*.a.com", // 中间通配
		"**.a.com",  // 双星
		"*a.com",    // 星不独立
		"a.com/*/x", // 路径里夹住
		"*.com",     // 顶级域只有一级标签? 实际是 host=com，单标签 → 非法
		"*.a",       // 顶级域单字符
		"*.a.1",     // 顶级域是数字
		"*.-a.com",  // 标签以 - 开头
		"*.a..com",  // 空标签
	}
	for _, raw := range invalid {
		if _, _, err := parseScriptDomainFormat(raw); !errors.Is(err, ErrScriptDomainFormatInvalid) {
			t.Fatalf("parseScriptDomainFormat(%q) err = %v, want ErrScriptDomainFormatInvalid", raw, err)
		}
	}
	// 空串是「没填」，不算错误（调用方决定跳过还是报 required）。
	if _, ok, err := parseScriptDomainFormat("   "); ok || err != nil {
		t.Fatal("blank entry should be ok=false, err=nil")
	}
}

// ───────────────────── 2) 匹配边界 ─────────────────────

func TestScriptDomainWildcardMatchesSubdomainsButNotTheBareDomain(t *testing.T) {
	wildcard := mustFormat(t, "*.a.com")
	// 正例：任意深度子域。
	for _, host := range []string{"x.a.com", "deep.x.a.com", "X.A.COM", "x.a.com."} {
		if !scriptDomainTemplateMatches(wildcard, host, "") {
			t.Fatalf("*.a.com should match %q", host)
		}
	}
	// 反例 1（本文件最重要的一条）：`*.a.com` **不匹配** `a.com` 本身。
	// 若实现写成 HasSuffix(host, "a.com")（漏点）或 len(host) >= len(suffix)，这条会红。
	if scriptDomainTemplateMatches(wildcard, "a.com", "") {
		t.Fatal("*.a.com must NOT match a.com itself")
	}
	// 反例 2：带点后缀边界 —— `evila.com` 不能冒充 `*.a.com`。
	for _, host := range []string{"evila.com", "fooa.com", "com", "a.com.evil.cn"} {
		if scriptDomainTemplateMatches(wildcard, host, "") {
			t.Fatalf("*.a.com must NOT match %q", host)
		}
	}
}

func TestScriptDomainPathMatchesAnyNonEmptyPathOnExactHost(t *testing.T) {
	pathRule := mustFormat(t, "a.com/*")
	// 正例：单段与多段都命中（用户明确给的例子 `a.com/*` + `p/q` → `a.com/p/q`）。
	for _, p := range []string{"/p", "/p/q", "/p/q/r", "/a/b/c/d"} {
		if !scriptDomainTemplateMatches(pathRule, "a.com", p) {
			t.Fatalf("a.com/* should match path %q", p)
		}
	}
	// 反例 1：根路径不命中（`/*` 要求至少一段）。
	if scriptDomainTemplateMatches(pathRule, "a.com", "") {
		t.Fatal("a.com/* must NOT match the root path")
	}
	// 反例 2：主机必须**全等**，子域不算。
	if scriptDomainTemplateMatches(pathRule, "x.a.com", "/p") {
		t.Fatal("a.com/* must NOT match a subdomain host")
	}
	// 反例 3：别的域不算。
	if scriptDomainTemplateMatches(pathRule, "b.com", "/p") {
		t.Fatal("a.com/* must NOT match another host")
	}
	// 大小写：域名不区分大小写。
	if !scriptDomainTemplateMatches(pathRule, "A.COM", "/p") {
		t.Fatal("host compare should be case-insensitive")
	}
}

func TestScriptDomainTemplateRejectsUnknownKind(t *testing.T) {
	// 未知 Kind 必须一律不匹配（失败方向安全：宁可拒绝也不放行）。
	if scriptDomainTemplateMatches(ScriptDomainFormat{Kind: "unknown", Host: "a.com"}, "a.com", "/p") {
		t.Fatal("unknown kind must never match")
	}
}

// ───────────────────── 3) 槽位拼装 ─────────────────────

func TestScriptDomainSlotAssemblyKeepsInputOnTheCorrectSide(t *testing.T) {
	// `*.a.com` + 输入 x → x.a.com（输入框在**前面**）
	wildcard := mustFormat(t, "*.a.com")
	got, err := BuildScriptDomainFromFormat(wildcard, "x")
	if err != nil || got != "x.a.com" {
		t.Fatalf("BuildScriptDomainFromFormat(*.a.com, x) = %q,%v; want x.a.com", got, err)
	}
	// `a.com/*` + 输入 p/q → a.com/p/q（输入框在**后面**）
	pathRule := mustFormat(t, "a.com/*")
	got, err = BuildScriptDomainFromFormat(pathRule, "p/q")
	if err != nil || got != "a.com/p/q" {
		t.Fatalf("BuildScriptDomainFromFormat(a.com/*, p/q) = %q,%v; want a.com/p/q", got, err)
	}
	// 多条格式并存时各自独立拼装，互不影响。
	multi := []ScriptDomainFormat{wildcard, pathRule}
	results := make([]string, 0, len(multi))
	for _, format := range multi {
		domain, err := BuildScriptDomainFromFormat(format, "y")
		if err != nil {
			t.Fatalf("format %q unexpected error: %v", format.Raw, err)
		}
		results = append(results, domain)
	}
	if len(results) != 2 || results[0] != "y.a.com" || results[1] != "a.com/y" {
		t.Fatalf("multiple formats produced %v, want [y.a.com a.com/y]", results)
	}
	// 多级子域允许（`*.a.com` 覆盖任意深度）。
	if got, err := BuildScriptDomainFromFormat(wildcard, "x.y"); err != nil || got != "x.y.a.com" {
		t.Fatalf("multi-level subdomain = %q,%v; want x.y.a.com", got, err)
	}
}

func TestScriptDomainSlotValidationRejectsUnsafeInput(t *testing.T) {
	wildcard := mustFormat(t, "*.a.com")
	pathRule := mustFormat(t, "a.com/*")
	badSubdomain := []string{"", " ", "a..b", ".x", "-x", "x-", "x/y", "x y", "x*", "x?y", "x#y", "x\ty", "X_OK"}
	for _, slot := range badSubdomain {
		if _, err := BuildScriptDomainFromFormat(wildcard, slot); !errors.Is(err, ErrScriptDomainSlotInvalid) {
			t.Fatalf("subdomain slot %q should be rejected, got %v", slot, err)
		}
	}
	badPath := []string{"", " ", "/", "p//q", "../etc", "p/./q", "p/../q", "p*q", "p q", "p\\q", "p?x", "p#y"}
	for _, slot := range badPath {
		if _, err := BuildScriptDomainFromFormat(pathRule, slot); !errors.Is(err, ErrScriptDomainSlotInvalid) {
			t.Fatalf("path slot %q should be rejected, got %v", slot, err)
		}
	}
	// 合法的路径字符必须放行，否则上面的断言只是「什么都拒绝」。
	for _, slot := range []string{"p", "p/q", "/p/q/", "a-b_c.d", "A/B"} {
		if _, err := BuildScriptDomainFromFormat(pathRule, slot); err != nil {
			t.Fatalf("path slot %q should be accepted, got %v", slot, err)
		}
	}
}

func TestScriptDomainSplitRoundTripsThroughAssembly(t *testing.T) {
	// UI 编辑态回填：库里存具体域名，界面要显示占位框里的内容。
	cases := []struct {
		raw, domain, wantSlot string
	}{
		{"*.a.com", "x.a.com", "x"},
		{"*.a.com", "x.y.a.com", "x.y"},
		{"a.com/*", "a.com/p", "p"},
		{"a.com/*", "a.com/p/q", "p/q"},
	}
	for _, c := range cases {
		format := mustFormat(t, c.raw)
		slot, ok := SplitScriptDomainForFormat(format, c.domain)
		if !ok || slot != c.wantSlot {
			t.Fatalf("Split(%q, %q) = %q,%v; want %q,true", c.raw, c.domain, slot, ok, c.wantSlot)
		}
		// 往返：拼回去必须等于原域名。
		rebuilt, err := BuildScriptDomainFromFormat(format, slot)
		if err != nil || rebuilt != c.domain {
			t.Fatalf("roundtrip %q → %q, err=%v", c.domain, rebuilt, err)
		}
	}
	// 不匹配的域名必须 ok=false（不能返回一个看似合法的槽位）。
	wildcard := mustFormat(t, "*.a.com")
	for _, domain := range []string{"a.com", "b.com", "x.a.com.evil.cn", ""} {
		if _, ok := SplitScriptDomainForFormat(wildcard, domain); ok {
			t.Fatalf("Split(*.a.com, %q) should not match", domain)
		}
	}
}

// ───────────────────── 4) 设置读写与脚本校验 ─────────────────────

func TestScriptDomainSettingsPersistOnlyNewSyntax(t *testing.T) {
	db := newScriptTestDB(t)
	settings, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{
		Domains: []string{" *.A.COM ", "a.com/*", "*.a.com", "  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 归一化去重后只剩两条，且小写、已排序。
	want := []string{"*.a.com", "a.com/*"}
	if len(settings.Domains) != len(want) || settings.Domains[0] != want[0] || settings.Domains[1] != want[1] {
		t.Fatalf("normalized = %v, want %v", settings.Domains, want)
	}
}

func TestScriptDomainSettingsRejectLegacyEntriesOnWrite(t *testing.T) {
	db := newScriptTestDB(t)
	// 混一条旧格式进去，**整批**必须被拒（不能只跳过坏的）。
	_, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{
		Domains: []string{"*.a.com", "b.com"},
	})
	if !errors.Is(err, ErrScriptDomainFormatLegacy) {
		t.Fatalf("legacy entry on write = %v, want ErrScriptDomainFormatLegacy", err)
	}
	// 拒绝之后库里不能留下半成品。
	stored, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Domains) != 0 {
		t.Fatalf("rejected write must not persist anything, got %v", stored.Domains)
	}
}

func TestValidateScriptDomainUsesNewSyntaxBoundaries(t *testing.T) {
	db := newScriptTestDB(t)
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{
		Domains: []string{"*.a.com", "b.com/*"},
	}); err != nil {
		t.Fatal(err)
	}
	// `*.a.com` 覆盖子域。
	for _, domain := range []string{"x.a.com", "deep.x.a.com"} {
		if err := ValidateScriptDomainWithDB(db, domain); err != nil {
			t.Fatalf("%q should be allowed by *.a.com, got %v", domain, err)
		}
	}
	// **不覆盖**裸域。这条与 TestScriptDomainWildcardMatchesSubdomainsButNotTheBareDomain 同源，
	// 但在「走真实库」的层面上再钉一次（两处实现若漂移，必有一处红）。
	if err := ValidateScriptDomainWithDB(db, "a.com"); !errors.Is(err, ErrScriptDomainUnavailable) {
		t.Fatalf("bare a.com must be rejected by *.a.com, got %v", err)
	}
	// `b.com/*` 只在主机维度校验 ⇒ b.com 本身放行（路径部分由脚本绑定决定）。
	if err := ValidateScriptDomainWithDB(db, "b.com"); err != nil {
		t.Fatalf("b.com should be allowed by b.com/*, got %v", err)
	}
	if err := ValidateScriptDomainWithDB(db, "x.b.com"); !errors.Is(err, ErrScriptDomainUnavailable) {
		t.Fatalf("subdomain of a path rule must be rejected, got %v", err)
	}
	// 未列出的域一律拒绝。
	if err := ValidateScriptDomainWithDB(db, "other.com"); !errors.Is(err, ErrScriptDomainUnavailable) {
		t.Fatalf("unlisted domain must be rejected, got %v", err)
	}
}

func TestValidateScriptDomainAllowsEverythingWhenListIsEmpty(t *testing.T) {
	db := newScriptTestDB(t)
	// 空列表 = 不限制（既有语义），不能因为语法重写就改成「空列表 = 全拒」——
	// 那会让所有存量部署在升级瞬间无法创建脚本。
	if err := ValidateScriptDomainWithDB(db, "anything.example"); err != nil {
		t.Fatalf("empty list should not restrict, got %v", err)
	}
}

func TestScriptDomainSettingsGetToleratesLegacyRows(t *testing.T) {
	db := newScriptTestDB(t)
	// GET 是纯读接口：存量旧格式不能让管理员连「看到问题并改掉」的入口都进不去。
	seed := entity.SystemSetting{Key: ScriptDomainSettingsKey, Value: `{"domains":["a.com","*.b.com"],"allowWildcard":true}`}
	if err := db.Create(&seed).Error; err != nil {
		t.Fatal(err)
	}
	settings, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		t.Fatalf("GET must tolerate legacy rows, got error %v", err)
	}
	// 旧条目被读不出来（只剩合法的新语法条目）；`*.b.com` 应当存活。
	if len(settings.Domains) != 1 || settings.Domains[0] != "*.b.com" {
		t.Fatalf("legacy entries should be dropped on read, got %v", settings.Domains)
	}
	formats, err := ScriptDomainFormatsWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(formats) != 1 || formats[0].Raw != "*.b.com" {
		t.Fatalf("formats = %+v, want only *.b.com", formats)
	}
}

func mustFormat(t *testing.T, raw string) ScriptDomainFormat {
	t.Helper()
	format, ok, err := parseScriptDomainFormat(raw)
	if err != nil || !ok {
		t.Fatalf("parseScriptDomainFormat(%q) = ok=%v err=%v", raw, ok, err)
	}
	return format
}
