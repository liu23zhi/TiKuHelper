package registry

import (
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestDSNQueryParams(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want []string
	}{
		{"no params", "u:p@tcp(127.0.0.1:3306)/tiku", nil},
		{"single", "u:p@tcp(h:3306)/tiku?parseTime=true", []string{"parsetime"}},
		{"mixed case and values", "u:p@tcp(h:3306)/tiku?ParseTime=TRUE&loc=UTC", []string{"parsetime", "loc"}},
		{"value with equals", "u:p@tcp(h:3306)/tiku?collation=utf8mb4_general_ci&x=a=b", []string{"collation", "x"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			params := dsnQueryParams(test.dsn)
			if len(params) != len(test.want) {
				t.Fatalf("params = %v, want %v", params, test.want)
			}
			for _, name := range test.want {
				if !params[name] {
					t.Fatalf("params = %v, want to contain %q", params, name)
				}
			}
		})
	}
}

func TestNormalizeMySQLDSNFillsRequiredParameters(t *testing.T) {
	const raw = "tiku:secret@tcp(guangzhou.example:3306)/TikuHelper"
	normalized, fixes := normalizeMySQLDSN(raw)
	if len(fixes) != 3 {
		t.Fatalf("fixes = %v, want parseTime/loc/charset", fixes)
	}
	config := mustParseDSN(t, normalized)
	if !config.ParseTime {
		t.Fatal("parseTime must be enabled, otherwise time columns cannot be scanned")
	}
	if config.Loc == nil || config.Loc.String() != time.Local.String() {
		t.Fatalf("loc = %v, want process local %v", config.Loc, time.Local)
	}
	// ⚠️ 断言 FormatDSN() 的输出，不要断言 config.Params["charset"]。
	//
	// go-sql-driver/mysql 1.10 起把 charset 从公开的 Params map 挪进了私有字段
	// （`charsets []string`），于是 `config.Params["charset"]` 恒为空串 ——
	// 断言会红，但**服务端实际收到的 DSN 完全没变**（实测 FormatDSN 仍输出
	// `charset=utf8mb4`）。这是「测试耦合了依赖的内部数据结构」，不是功能回归。
	//
	// 判据用 FormatDSN 的输出串：那才是真正交给驱动的东西，换驱动版本也不会误报。
	if !strings.Contains(config.FormatDSN(), "charset=utf8mb4") {
		t.Fatalf("FormatDSN = %q, want it to contain charset=utf8mb4",
			config.FormatDSN())
	}
	if config.DBName != "TikuHelper" {
		t.Fatalf("db = %q, want TikuHelper", config.DBName)
	}
}

func TestNormalizeMySQLDSNLeavesCompleteDSNAlone(t *testing.T) {
	const raw = "u:p@tcp(127.0.0.1:3306)/tiku?charset=utf8mb4&parseTime=true&loc=Asia%2FShanghai"
	normalized, fixes := normalizeMySQLDSN(raw)
	if len(fixes) != 0 {
		t.Fatalf("fixes = %v, want none", fixes)
	}
	if normalized != raw {
		t.Fatalf("normalized = %q, want unchanged %q", normalized, raw)
	}
}

func TestNormalizeMySQLDSNForcesParseTimeEvenWhenDisabled(t *testing.T) {
	normalized, fixes := normalizeMySQLDSN("u:p@tcp(h:3306)/tiku?parseTime=false")
	if !containsFix(fixes, "parseTime=true") {
		t.Fatalf("fixes = %v, want parseTime fix", fixes)
	}
	if !mustParseDSN(t, normalized).ParseTime {
		t.Fatal("parseTime=false must be corrected: timestamps break the whole startup path")
	}
}

func TestNormalizeMySQLDSNRespectsExplicitLoc(t *testing.T) {
	normalized, fixes := normalizeMySQLDSN("u:p@tcp(h:3306)/tiku?loc=UTC")
	config := mustParseDSN(t, normalized)
	if config.Loc.String() != "UTC" {
		t.Fatalf("loc = %v, want explicit UTC kept", config.Loc)
	}
	// 同上：用 FormatDSN 的输出判断 charset，不读驱动的内部 Params。
	if !config.ParseTime || !strings.Contains(config.FormatDSN(), "charset=utf8mb4") {
		t.Fatalf("missing parameters must still be filled, got %s", config.FormatDSN())
	}
	if containsFix(fixes, "loc="+time.Local.String()) {
		t.Fatalf("fixes = %v, should not overwrite explicit loc", fixes)
	}
}

func TestNormalizeMySQLDSNKeepsCollationInsteadOfCharset(t *testing.T) {
	normalized, _ := normalizeMySQLDSN("u:p@tcp(h:3306)/tiku?collation=utf8mb4_bin")
	config := mustParseDSN(t, normalized)
	if config.Collation != "utf8mb4_bin" {
		t.Fatalf("collation = %q, want utf8mb4_bin", config.Collation)
	}
	if _, ok := config.Params["charset"]; ok {
		t.Fatal("charset must not be added when collation is explicit")
	}
}

func TestNormalizeMySQLDSNPreservesEscapedCredentials(t *testing.T) {
	const raw = "user:p%40ss%3Aword@tcp(127.0.0.1:3306)/db"
	before := mustParseDSN(t, raw)
	normalized, _ := normalizeMySQLDSN(raw)
	after := mustParseDSN(t, normalized)
	if after.User != before.User || after.Passwd != before.Passwd || after.DBName != before.DBName {
		t.Fatalf("credentials changed: %+v -> %+v", before, after)
	}
}

func TestNormalizeMySQLDSNKeepsUnparsableDSN(t *testing.T) {
	const raw = "this is not a dsn"
	normalized, fixes := normalizeMySQLDSN(raw)
	if normalized != raw {
		t.Fatalf("normalized = %q, want original", normalized)
	}
	if len(fixes) == 0 || !strings.Contains(fixes[0], "解析失败") {
		t.Fatalf("fixes = %v, want a parse failure note", fixes)
	}
}

func mustParseDSN(t *testing.T, dsn string) *mysqldriver.Config {
	t.Helper()
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	return config
}

func containsFix(fixes []string, want string) bool {
	for _, fix := range fixes {
		if fix == want {
			return true
		}
	}
	return false
}
