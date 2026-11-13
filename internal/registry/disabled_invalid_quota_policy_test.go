package registry

import (
	"bytes"
	"context"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func dqRegistryRaw(t *testing.T, db *gorm.DB) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := db.Raw("SELECT quote(total_quota_limit) AS quota, typeof(total_quota_limit) AS storage, quote(status) AS status FROM access_tokens ORDER BY id").Scan(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

// dqColumnDef 从 `CREATE TABLE x (...)` 的 DDL 里按逗号切出「顶层」列定义。
//
// 不能用 strings.Split(ddl, ",") —— 列定义里可能有 `decimal(18,6)` 这种带逗号的
// 类型参数。这里按括号深度切分，只在深度为 0 的逗号处断开。
func dqColumnDef(schema string) []string {
	open := strings.Index(schema, "(")
	close := strings.LastIndex(schema, ")")
	if open < 0 || close <= open {
		return nil
	}
	body := schema[open+1 : close]

	var parts []string
	depth, start := 0, 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(body[start:i]))
				start = i + 1
			}
		}
	}
	if start < len(body) {
		parts = append(parts, strings.TrimSpace(body[start:]))
	}
	return parts
}

// dqColumnName 取列定义的首个标识符（去掉反引号/引号）。表级约束返回空串。
func dqColumnName(def string) string {
	trimmed := strings.TrimSpace(def)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"primary key", "unique", "constraint", "foreign key", "check", "index"} {
		if strings.HasPrefix(lower, prefix) {
			return ""
		}
	}
	name := trimmed
	if idx := strings.IndexAny(name, " \t"); idx >= 0 {
		name = name[:idx]
	}
	return strings.Trim(name, "`\"'")
}

// normalizeDDL 去掉 DDL 里的全部空白，用于「语义相同、排版不同」的比较。
//
// 为什么需要它：本条用例把 schemaAfter 剥掉新增列后拼回去，拼接用的分隔符
// 与 SQLite 原样回显的布局不可能逐字符一致（实测原表 `...,` 无空格、
// 剥列后是 `, `）。若直接比字符串，会因空白差异假红，看起来像「既有列被改」。
//
// 只去空白，不去、不改任何 token —— 类型、NOT NULL、DEFAULT 值、列顺序
// 有任何变化都仍然会红。
func normalizeDDL(ddl string) string {
	var b strings.Builder
	b.Grow(len(ddl))
	for _, r := range ddl {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stripAddedColumns 返回「schemaAfter 去掉新增列之后」的 DDL。
//
// 用途：本文件的两个用例要证明「启动不改写历史配额值」。
// 该判据原本写成「整表 DDL 前后完全相等」，但那会与「启动时显式新增列」的
// 合法迁移冲突（例如 EnsureClientKindSchema 给 access_tokens 加 client_kind）。
//
// 严格性没有放宽：
//   - 只保留 before 里出现过的列名；after 独有的列被剥掉
//   - 保留的是 **before 的定义原文**，所以任何既有列被改（类型/NOT NULL/默认值）
//     都会让结果 != before，仍然会红
//   - 表级约束（PRIMARY KEY 等）原样保留在 after 侧，若被改动也会红
func stripAddedColumns(schemaBefore, schemaAfter string) string {
	beforeDefs := dqColumnDef(schemaBefore)
	afterDefs := dqColumnDef(schemaAfter)
	if len(beforeDefs) == 0 || len(afterDefs) == 0 {
		return schemaAfter
	}

	known := make(map[string]bool, len(beforeDefs))
	for _, def := range beforeDefs {
		if name := dqColumnName(def); name != "" {
			known[name] = true
		}
	}

	kept := make([]string, 0, len(beforeDefs))
	for _, def := range afterDefs {
		name := dqColumnName(def)
		if name == "" {
			// 表级约束：保留（被改动会红）
			kept = append(kept, def)
			continue
		}
		if known[name] {
			kept = append(kept, def)
		}
	}

	open := strings.Index(schemaAfter, "(")
	close := strings.LastIndex(schemaAfter, ")")
	if open < 0 || close <= open {
		return schemaAfter
	}
	return schemaAfter[:open] + "(" + strings.Join(kept, ", ") + schemaAfter[close:]
}
func TestDisabledInvalidQuotaPolicyRegistryInspection(t *testing.T) {
	for _, c := range []struct {
		name, status string
		allow        bool
	}{{"disabled", "disabled", true}, {"disabled_case", " DISABLED ", true}, {"enabled", "enabled", false}, {"unknown", "paused", false}, {"blank", "", false}} {
		t.Run(c.name, func(t *testing.T) {
			db := newAccessTokenQuotaMigrationDB(t)
			if err := db.Create(&entity.AccessToken{UserID: 1, Name: "disabled_bad", TokenHash: fmt.Sprintf("%064x", 1), Status: "enabled", TotalQuotaLimit: -2}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&entity.AccessToken{}).Where("id = 1").Update("status", c.status).Error; err != nil {
				t.Fatal(err)
			}
			before := dqRegistryRaw(t, db)
			for i := 0; i < 2; i++ {
				err := ensureAccessTokenQuotaLimitData(db)
				if (err == nil) != c.allow {
					t.Errorf("startup exception allow=%t gotError=%t", c.allow, err != nil)
				}
			}
			if !reflect.DeepEqual(before, dqRegistryRaw(t, db)) {
				t.Error("inspection changed raw quota/status/storage")
			}
		})
	}
}
func TestDisabledInvalidQuotaPolicyStartup(t *testing.T) {
	if os.Getenv("TIKU_TEST_DQ_CHILD") == "1" {
		t.Fatal("parent called as child")
	}
	path := filepath.Join(t.TempDir(), "owned-disabled-quota.db")
	seed, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.AutoMigrate(&entity.AccessToken{}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Create(&entity.AccessToken{ID: 1, UserID: 1, Name: "disabled_bad", TokenHash: fmt.Sprintf("%064x", 1), TokenValue: "tk_synthetic_never_print", Status: "disabled", TotalQuotaLimit: 1e24}).Error; err != nil {
		t.Fatal(err)
	}
	before := dqRegistryRaw(t, seed)
	var schemaBefore string
	if err := seed.Raw("SELECT sql FROM sqlite_master WHERE name = 'access_tokens'").Scan(&schemaBefore).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := seed.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDisabledInvalidQuotaPolicyStartupChild$", "-test.v", "-test.timeout=18s")
	cmd.Env = append(os.Environ(), "TIKU_TEST_DQ_CHILD=1", "TIKU_TEST_DQ_DB="+path, "SQL_DSN=", "TIKU_APP_MODE=production", "TIKU_ADMIN_PASSWORD=OwnedOnlyQuotaPolicy!20261103")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	t.Logf("owned startup exitSuccess=%t\nDQ_CHILD_STDOUT:\n%s\nDQ_CHILD_STDERR:\n%s", runErr == nil, stdout.Bytes(), stderr.Bytes())
	if runErr != nil {
		t.Errorf("disabled historical key blocked other startup: %v", runErr)
	}
	if strings.Contains(stdout.String(), "tk_synthetic_never_print") {
		t.Error("startup leaked raw token")
	}
	check, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := check.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !reflect.DeepEqual(before, dqRegistryRaw(t, check)) {
		t.Error("real startup changed raw disabled value/type/status")
	}
	var schemaAfter string
	check.Raw("SELECT sql FROM sqlite_master WHERE name = 'access_tokens'").Scan(&schemaAfter)
	// 本用例保护的是「历史无效的 total_quota_limit 值不被启动流程改写」，
	// 所以判据是「原有列的定义一字不差」，而不是「整张表的 DDL 一字不差」。
	//
	// 2026-11-13 调整：EnsureClientKindSchema 会在启动时给 access_tokens **新增**
	// client_kind 列（107 Q11，列检测型迁移，NOT NULL DEFAULT 'unknown'）。
	// 那是设计内的显式迁移，不是「改写了历史配额」，不应该让本条用例变红。
	// 若继续用整表 DDL 相等来断言，任何新增列都会误判成「历史数据被改」。
	//
	// 判据改为：把 schemaAfter 里所有新增的列剥掉后，必须与 schemaBefore 等价。
	// 只剥「新增的列定义」，不剥、不改任何既有列 —— 既有列一旦被改，仍然会红。
	// 比较用规范化形式（去掉全部空白），因为 SQLite 回显的 DDL 空白布局不可控：
	// 本轮实测原表是 `...,` 无空格，剥列后拼回去会带 `, `，纯字符串比对会假红。
	if got, want := normalizeDDL(stripAddedColumns(schemaBefore, schemaAfter)), normalizeDDL(schemaBefore); got != want {
		t.Errorf("held token schema changed beyond adding new columns\nbefore: %s\nafter(剥新增列): %s", want, got)
	}
	var quick string
	check.Raw("PRAGMA quick_check").Scan(&quick)
	if quick != "ok" {
		t.Error("owned database quick_check failed")
	}
}
func TestDisabledInvalidQuotaPolicyStartupChild(t *testing.T) {
	if os.Getenv("TIKU_TEST_DQ_CHILD") != "1" {
		t.Skip("private owned startup child")
	}
	path := os.Getenv("TIKU_TEST_DQ_DB")
	if !filepath.IsAbs(path) {
		t.Fatal("owned absolute DB path required")
	}
	RegisterDB(configs.Config{Database: configs.DatabaseConfig{Path: path}})
	defer CloseDB()
	if !db.Migrator().HasTable(&entity.AIOCRCache{}) || !db.Migrator().HasTable(&entity.BillingRecord{}) {
		t.Error("other domain models did not start")
	}
	t.Log("DISABLED_INVALID_QUOTA_OWNED_STARTUP_COMPLETE")
}
