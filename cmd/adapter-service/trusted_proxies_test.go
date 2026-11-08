package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 多种写法必须都能解析出来。
func TestParseTrustedProxyEntriesFormats(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "one per line", raw: "172.26.0.3\n10.0.0.0/8\n", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "comma separated", raw: "172.26.0.3, 172.18.0.0/16", want: []string{"172.26.0.3", "172.18.0.0/16"}},
		{name: "semicolon separated", raw: "172.26.0.3;10.0.0.0/8;", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "crlf and bom", raw: "\ufeff172.26.0.3\r\n10.0.0.0/8\r\n", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "hash comment", raw: "# 反代\n172.26.0.3 # 说明\n", want: []string{"172.26.0.3"}},
		{name: "slash comment", raw: "// 反代\n172.26.0.3\n", want: []string{"172.26.0.3"}},
		{name: "yaml list", raw: "- 172.26.0.3\n- 10.0.0.0/8\n", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "json array", raw: `["172.26.0.3", "10.0.0.0/8"]`, want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "key value", raw: "trusted_proxies: 172.26.0.3, 10.0.0.0/8", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "yaml key with array", raw: "trustedProxies: [172.26.0.3, 10.0.0.0/8]", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "nginx directive", raw: "set_real_ip_from 172.26.0.3;\nallow 10.0.0.0/8;", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "entry with label", raw: "172.26.0.3 反向代理容器\n10.0.0.0/8 internal", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "quoted", raw: "\"172.26.0.3\"\n'10.0.0.0/8'\n", want: []string{"172.26.0.3", "10.0.0.0/8"}},
		{name: "ipv6", raw: "::1\nfd00::/8\n[2001:db8::1]\n", want: []string{"::1", "fd00::/8", "2001:db8::1"}},
		{name: "netmask form", raw: "10.0.0.0/255.0.0.0", want: []string{"10.0.0.0/8"}},
		{name: "range", raw: "172.26.0.10-172.26.0.12", want: []string{"172.26.0.10/31", "172.26.0.12/32"}},
		{name: "range aligned to network", raw: "172.26.0.0-172.26.0.255", want: []string{"172.26.0.0/24"}},
		{name: "dedup", raw: "172.26.0.3\n172.26.0.3\n", want: []string{"172.26.0.3"}},
		{name: "empty", raw: "  \n# 只有注释\n", want: []string{}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, invalid := parseTrustedProxyEntries(testCase.raw)
			if len(invalid) != 0 {
				t.Fatalf("unexpected invalid entries: %v", invalid)
			}
			if len(got) == 0 && len(testCase.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("parse = %v, want %v", got, testCase.want)
			}
		})
	}
}

// 信任所有来源的几种写法都要展开成全信任列表。
func TestParseTrustedProxyEntriesTrustAllKeywords(t *testing.T) {
	for _, raw := range []string{"*", "any", "all", "0.0.0.0/0", "::/0"} {
		got, invalid := parseTrustedProxyEntries(raw)
		if len(invalid) != 0 {
			t.Fatalf("%q: unexpected invalid entries: %v", raw, invalid)
		}
		if len(got) == 0 {
			t.Fatalf("%q: expected entries", raw)
		}
	}
	got, _ := parseTrustedProxyEntries("*")
	if !reflect.DeepEqual(got, []string{trustAllProxiesIPv4, trustAllProxiesIPv6}) {
		t.Fatalf("wildcard should expand to trust-all list, got %v", got)
	}
}

// 写错的条目必须被报出来，而不是被当成有效配置或悄悄丢掉。
func TestParseTrustedProxyEntriesReportsInvalid(t *testing.T) {
	got, invalid := parseTrustedProxyEntries("172.26.0.3\n172.26.0.300\n反代容器\n")
	if !reflect.DeepEqual(got, []string{"172.26.0.3"}) {
		t.Fatalf("unexpected entries: %v", got)
	}
	if !reflect.DeepEqual(invalid, []string{"172.26.0.300", "反代容器"}) {
		t.Fatalf("unexpected invalid: %v", invalid)
	}
}

func TestResolveTrustedProxies(t *testing.T) {
	trustAll := []string{trustAllProxiesIPv4, trustAllProxiesIPv6}
	tests := []struct {
		name       string
		env        string
		file       string
		want       []string // 非空表示精确匹配
		configured []string // want 为空时：断言这些条目在最前面且带局域网/本地默认值
	}{
		{name: "both empty means trust all", want: trustAll},
		{name: "blank env and commented file means trust all", env: " \t\n", file: "# 只有注释\n\n", want: trustAll},
		{name: "explicit trust all stays as-is", env: "0.0.0.0/0", want: []string{trustAllProxiesIPv4}},
		{
			name: "env list", env: "10.0.0.10,172.18.0.0/16",
			configured: []string{"10.0.0.10", "172.18.0.0/16"},
		},
		{
			name: "file list with comments and newlines", file: "# 反代\n10.0.0.10\n\n172.26.0.3\n",
			configured: []string{"10.0.0.10", "172.26.0.3"},
		},
		{
			name: "env wins over file", env: "10.0.0.11", file: "10.0.0.10",
			configured: []string{"10.0.0.11"},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := resolveTrustedProxies(testCase.env, testCase.file)
			if len(testCase.want) > 0 {
				if !reflect.DeepEqual(got, testCase.want) {
					t.Fatalf("resolveTrustedProxies() = %v, want %v", got, testCase.want)
				}
				return
			}
			assertConfiguredPlusLanDefaults(t, got, testCase.configured)
		})
	}
}

// 配置了可信代理时，局域网/本地地址必须作为默认值补上（用户条目在前）。
func TestComposeTrustedProxiesAddsLanAndLocalDefaults(t *testing.T) {
	assertConfiguredPlusLanDefaults(t, composeTrustedProxies([]string{"172.26.0.3"}), []string{"172.26.0.3"})

	// 去重：用户已经写过某个默认段时不重复追加。
	got := composeTrustedProxies([]string{"10.0.0.0/8", "10.0.0.0/8"})
	if countEntry(got, "10.0.0.0/8") != 1 {
		t.Fatalf("expected no duplicate entries, got %v", got)
	}
	assertConfiguredPlusLanDefaults(t, got, []string{"10.0.0.0/8"})

	// 显式全信任时不再补默认值。
	if got := composeTrustedProxies([]string{"0.0.0.0/0"}); !reflect.DeepEqual(got, []string{trustAllProxiesIPv4}) {
		t.Fatalf("expected trust-all to stay as-is, got %v", got)
	}
}

func TestLoadTrustedProxiesReadsFileAndStripsBOM(t *testing.T) {
	t.Setenv("TIKU_APP_MODE", "development")
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted_proxies.txt")
	// 模拟 Windows 记事本写入的 UTF-8 BOM，并混用 YAML 列表与说明文字。
	content := "\ufeff# 反向代理\n- 172.26.0.3  # 反代容器\n- 172.18.0.0/16\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Setenv(trustedProxiesEnv, "")
	t.Setenv(trustedProxiesFileEnv, path)

	config, err := loadTrustedProxies()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertConfiguredPlusLanDefaults(t, config.List, []string{"172.26.0.3", "172.18.0.0/16"})
	if config.Source != path {
		t.Fatalf("expected source %q, got %q", path, config.Source)
	}
	if config.ConfiguredCount != 2 {
		t.Fatalf("expected 2 configured entries, got %d", config.ConfiguredCount)
	}
	if len(config.Invalid) != 0 {
		t.Fatalf("unexpected invalid entries: %v", config.Invalid)
	}
}

func TestLoadTrustedProxiesFallsBackToTrustAll(t *testing.T) {
	t.Setenv("TIKU_APP_MODE", "development")
	t.Setenv(trustedProxiesEnv, "")
	t.Setenv(trustedProxiesFileEnv, filepath.Join(t.TempDir(), "missing.txt"))

	config, err := loadTrustedProxies()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(config.List, []string{trustAllProxiesIPv4, trustAllProxiesIPv6}) {
		t.Fatalf("expected trust-all fallback, got %v", config.List)
	}
	if config.Source != "" {
		t.Fatalf("expected no source, got %q", config.Source)
	}
}

// 文件里全是无法解析的内容时必须报错，不能静默降级成「信任所有来源」。
func TestLoadTrustedProxiesRejectsUnparsableFile(t *testing.T) {
	t.Setenv("TIKU_APP_MODE", "development")
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted_proxies.txt")
	if err := os.WriteFile(path, []byte("反代容器\n172.26.0.300\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Setenv(trustedProxiesEnv, "")
	t.Setenv(trustedProxiesFileEnv, path)

	if _, err := loadTrustedProxies(); err == nil {
		t.Fatal("expected an error for an unparsable trusted proxy file")
	} else if !strings.Contains(err.Error(), "172.26.0.300") {
		t.Fatalf("error should mention the offending entry, got %v", err)
	}
}

// 部分条目写错时：有效的照常用（再补默认值），写错的报 warning（不阻断启动）。
func TestLoadTrustedProxiesKeepsValidEntriesAndReportsInvalid(t *testing.T) {
	t.Setenv("TIKU_APP_MODE", "development")
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted_proxies.txt")
	if err := os.WriteFile(path, []byte("172.26.0.3\n172.26.0.300\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Setenv(trustedProxiesEnv, "")
	t.Setenv(trustedProxiesFileEnv, path)

	config, err := loadTrustedProxies()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertConfiguredPlusLanDefaults(t, config.List, []string{"172.26.0.3"})
	if !reflect.DeepEqual(config.Invalid, []string{"172.26.0.300"}) {
		t.Fatalf("unexpected invalid: %v", config.Invalid)
	}
}

// 环境变量里全是无效内容时报错，且不能悄悄改用文件里的配置。
func TestLoadTrustedProxiesRejectsUnparsableEnv(t *testing.T) {
	t.Setenv("TIKU_APP_MODE", "development")
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted_proxies.txt")
	if err := os.WriteFile(path, []byte("172.26.0.3\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Setenv(trustedProxiesEnv, "反代容器")
	t.Setenv(trustedProxiesFileEnv, path)

	if _, err := loadTrustedProxies(); err == nil {
		t.Fatal("expected an error when TRUSTED_PROXIES has no parsable entry")
	}
}

// assertConfiguredPlusLanDefaults 断言「用户配置在前，随后带局域网/本地默认值」。
func assertConfiguredPlusLanDefaults(t *testing.T, got, configured []string) {
	t.Helper()
	if len(got) < len(configured) || !reflect.DeepEqual(got[:len(configured)], configured) {
		t.Fatalf("expected configured entries first %v, got %v", configured, got)
	}
	for _, entry := range defaultTrustedProxies {
		if countEntry(got, entry) == 0 {
			t.Fatalf("expected LAN/local default %s in %v", entry, got)
		}
	}
}

func countEntry(list []string, target string) int {
	count := 0
	for _, entry := range list {
		if entry == target {
			count++
		}
	}
	return count
}
