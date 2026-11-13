package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	gl "gorm.io/gorm/logger"
)

// MariaDB 真库测试的开关与基建。
//
// 这套 helper 存在的唯一理由是 task_doc/122 决策 4：
// **「没设 DSN 所以跳过」与「全部通过」在 `exit 0` 上无法区分**。
// 真库作业（`mariadb-integration`）必须把 DSN 缺失当成配置错误判红，
// 否则哪天 workflow 少写一个 env，MySQL 覆盖会悄悄归零而 CI 依然全绿。
//
// 同时本地开发不能因为没装 MariaDB 就跑不了测试，所以本地必须能跳过。
// 两者靠 decideMySQLTestDSN 的三个输入区分：
//
//	① TIKU_TEST_MYSQL_DSN      —— DSN 本身
//	② TIKU_TEST_MYSQL_REQUIRED —— 该作业显式声明「必须有真库」
//	③ CI + !-short             —— CI 里跑非短路模式，等于在跑真库作业
//
// 为什么还要看 `-short`：`ci.yml` 的 backend 作业跑的是
// `go test -short -p 1 ./...`，那里**没有** MariaDB service。如果只看 CI 环境变量，
// backend 作业会因为找不到 DSN 直接判红，等于把整条流水线钉死。
// 真库作业刻意不带 `-short`（见 ci.yml 的 mariadb-integration），
// 于是「CI + 非 short」就是「这个作业承诺了有真库」的准确判据。
const (
	mysqlTestDSNEnv      = "TIKU_TEST_MYSQL_DSN"
	mysqlTestRequiredEnv = "TIKU_TEST_MYSQL_REQUIRED"
	mysqlTestCIEnv       = "CI"
	mysqlHarnessChildEnv = "TIKU_MYSQL_HARNESS_CHILD"
)

// mysqlTestDecision 是 mysqlTestDSN 的纯函数判定结果。
//
// 抽成结构体是为了能被**不依赖 t.Fatal 语义**的测试直接断言：
// `t.Fatalf` 与 `t.Skipf` 在测试进程里都表现为 Goexit，无法在同一个测试内
// 观察差别；但它们的「决策」是可以逐个分支断言的。
type mysqlTestDecision struct {
	// DSN 非空表示需要连真库。
	DSN string
	// Fatal 为真表示必须判红（配置错误，不是合法跳过）。
	Fatal bool
	// Reason 是人可读的判据说明，同时作为 skip/fatal 的文案。
	Reason string
}

// decideMySQLTestDSN 是唯一决定「跑 / 跳过 / 判红」的地方。
func decideMySQLTestDSN(dsn, ciEnv, required, short string) mysqlTestDecision {
	trimmed := strings.TrimSpace(dsn)
	if trimmed != "" {
		return mysqlTestDecision{DSN: trimmed, Reason: "已提供 " + mysqlTestDSNEnv}
	}
	if strings.TrimSpace(required) != "" {
		return mysqlTestDecision{
			Fatal:  true,
			Reason: "作业已声明 " + mysqlTestRequiredEnv + "，但 " + mysqlTestDSNEnv + " 为空（workflow 配置错误，不是合法跳过）",
		}
	}
	if strings.TrimSpace(ciEnv) != "" && strings.TrimSpace(short) == "" {
		return mysqlTestDecision{
			Fatal:  true,
			Reason: "CI 里以非 -short 模式运行 MariaDB 真库测试，但 " + mysqlTestDSNEnv + " 为空（workflow 配置错误，不是合法跳过）",
		}
	}
	return mysqlTestDecision{Reason: "未设置 " + mysqlTestDSNEnv + "，本地跳过 MariaDB 真库测试"}
}

func shortFlagValue() string {
	if testing.Short() {
		return "1"
	}
	return ""
}

// mysqlTestDSN 返回真库 DSN；缺失时按 decideMySQLTestDSN 的判定 skip 或 fatal。
func mysqlTestDSN(t *testing.T) string {
	t.Helper()
	decision := decideMySQLTestDSN(
		os.Getenv(mysqlTestDSNEnv),
		os.Getenv(mysqlTestCIEnv),
		os.Getenv(mysqlTestRequiredEnv),
		shortFlagValue(),
	)
	if decision.DSN != "" {
		return decision.DSN
	}
	if decision.Fatal {
		// 必须是 Fatal 而不是 Skip：Skip 会让「一条都没跑」以 exit 0 收场，
		// 也就是 121/122 反复点名的「skip 假绿」。
		t.Fatalf("MariaDB 真库测试无法执行：%s", decision.Reason)
	}
	t.Skip(decision.Reason)
	return ""
}

// TestMariaDBTestDSNPolicyIsExplicit 是开关本身的钉子。
//
// 它不连库、永不跳过，因此在本机与 CI 都会执行。
// 变异验证：把 decideMySQLTestDSN 里任何一条 Fatal 改成 skip 语义，这里立刻变红。
func TestMariaDBTestDSNPolicyIsExplicit(t *testing.T) {
	cases := []struct {
		name     string
		dsn      string
		ci       string
		required string
		short    string
		wantDSN  string
		wantFail bool
	}{
		{name: "本地未设 DSN", wantFail: false, wantDSN: ""},
		{name: "本地已设 DSN", dsn: "tiku:tiku@tcp(127.0.0.1:3306)/tiku_ci", wantDSN: "tiku:tiku@tcp(127.0.0.1:3306)/tiku_ci"},
		{name: "真库作业已设 DSN", dsn: "  dsn-value  ", ci: "true", required: "1", wantDSN: "dsn-value"},
		{name: "真库作业缺 DSN", ci: "true", required: "1", wantFail: true},
		{name: "CI 非 short 缺 DSN", ci: "true", wantFail: true},
		{name: "CI 且 -short 缺 DSN（backend 作业）", ci: "true", short: "1", wantFail: false},
		{name: "本地 required 缺 DSN", required: "1", wantFail: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			decision := decideMySQLTestDSN(testCase.dsn, testCase.ci, testCase.required, testCase.short)
			if decision.Fatal != testCase.wantFail {
				t.Fatalf("Fatal = %v, want %v（reason=%s）", decision.Fatal, testCase.wantFail, decision.Reason)
			}
			if decision.DSN != testCase.wantDSN {
				t.Fatalf("DSN = %q, want %q", decision.DSN, testCase.wantDSN)
			}
			if decision.DSN == "" && decision.Reason == "" {
				t.Fatal("skip/fatal 必须带可读原因，否则日志里看不出为什么没跑")
			}
		})
	}
}

// TestMariaDBHarnessChildUsesMySQLTestDSN 是被父测试用 -test.run 单独拉起的子进程入口。
//
// 它存在的目的：`t.Fatal` 与 `t.Skip` 在**同一个**测试进程里都表现为 Goexit，
// 无法互相区分；只有把子进程的退出码作为观测量，才能证明
// 「CI 缺 DSN 时整个测试二进制真的失败」而不是「安静地跳过」。
func TestMariaDBHarnessChildUsesMySQLTestDSN(t *testing.T) {
	if strings.TrimSpace(os.Getenv(mysqlHarnessChildEnv)) == "" {
		t.Skip("仅作为父测试的子进程入口使用")
	}
	dsn := mysqlTestDSN(t)
	t.Logf("HARNESS_CHILD_DSN_LEN=%d", len(dsn))
}

// TestMariaDBHarnessFailsWhenDSNMissingInCI 是「CI 缺 DSN 必须判红」的钉子。
//
// 四个状态都要验，否则会出现「拿到了正确的 fail 但原因不对」的假钉子：
//
//	A 本地无 CI 标记       → 子进程 skip，退出码 0（本地开发不能被钉死）
//	B CI 且声明需要真库     → 子进程必须失败，退出码 != 0
//	C CI 且非 short 模式    → 子进程必须失败（即使 REQUIRED 被人误删也兜住）
//	D CI 且 -short         → 子进程 skip（backend 作业没有 MariaDB，必须能过）
//
// 变异（Fatal→Skip）会让 B、C 变成退出码 0，本测试立刻变红。
//
// ⚠️ 子进程的 `-test.short` 必须由**每个用例显式给定**，不能从父进程继承：
// 父进程在 backend 作业里是 `-short` 模式，若继承就会让 C 失去意义；
// 反之控制组 A 在非 short 的父进程里又会因为「CI 非 short」被误判成必须失败。
func TestMariaDBHarnessFailsWhenDSNMissingInCI(t *testing.T) {
	cases := []struct {
		name       string
		ci         string
		required   string
		childShort bool
		wantFail   bool
	}{
		{name: "本地跳过", wantFail: false},
		{name: "CI 真库作业必须判红", ci: "true", required: "1", wantFail: true},
		{name: "CI 非 short 模式必须判红", ci: "true", wantFail: true},
		{name: "CI 且 -short 必须跳过（backend 作业）", ci: "true", childShort: true, wantFail: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			exitCode, output := runMariaDBHarnessChild(t, testCase.ci, testCase.required, testCase.childShort)
			failed := exitCode != 0
			if failed != testCase.wantFail {
				t.Fatalf("子进程退出码 = %d（failed=%v），want failed=%v\n--- 子进程输出 ---\n%s",
					exitCode, failed, testCase.wantFail, output)
			}
			if testCase.wantFail {
				// 失败原因必须是「缺 DSN」，不是崩溃、不是编译问题。
				if !strings.Contains(output, mysqlTestDSNEnv) {
					t.Fatalf("子进程失败原因不是缺 %s，输出：\n%s", mysqlTestDSNEnv, output)
				}
				if strings.Contains(output, "HARNESS_CHILD_DSN_LEN") {
					t.Fatalf("子进程竟然跑到了 DSN 之后的逻辑，输出：\n%s", output)
				}
			} else if !strings.Contains(output, "SKIP") {
				t.Fatalf("控制组应当是 SKIP，输出：\n%s", output)
			}
		})
	}
}

// runMariaDBHarnessChild 在干净环境里重新执行本测试二进制。
//
// 环境必须**剔除**父进程的 DSN/CI/required 三个变量再显式赋值，
// 否则在真库作业里跑这条测试会继承到真实 DSN，控制组全部失去意义。
func runMariaDBHarnessChild(t *testing.T, ci, required string, short bool) (int, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("无法定位测试二进制：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	args := []string{
		"-test.run=^TestMariaDBHarnessChildUsesMySQLTestDSN$",
		"-test.count=1",
		"-test.v",
	}
	if short {
		args = append(args, "-test.short")
	}
	cmd := exec.CommandContext(ctx, self, args...)
	env := make([]string, 0, len(os.Environ())+4)
	for _, item := range os.Environ() {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		switch key {
		case mysqlTestDSNEnv, mysqlTestRequiredEnv, mysqlTestCIEnv, mysqlHarnessChildEnv:
			continue
		}
		env = append(env, item)
	}
	env = append(env,
		mysqlHarnessChildEnv+"=1",
		mysqlTestDSNEnv+"=",
		mysqlTestCIEnv+"="+ci,
		mysqlTestRequiredEnv+"="+required,
	)
	cmd.Env = env
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("子进程超时：%v\n%s", ctx.Err(), combined.String())
	}
	if runErr == nil {
		return 0, combined.String()
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("子进程基础设施错误：%T %v\n%s", runErr, runErr, combined.String())
	}
	return exitErr.ExitCode(), combined.String()
}

// ---------------------------------------------------------------------------
// 真库连接与清库
// ---------------------------------------------------------------------------

// openMariaDBTestDB 打开真库连接。
//
// DSN 复用生产同一套 normalizeMySQLDSN：缺 parseTime 时时间列扫描会失败，
// 而那种失败看起来像「迁移写坏了」，实际是 DSN 少了参数。
// 连接池固定 1 条：本文件里的用例会 DROP TABLE / 改 schema，
// 多连接会让「清库」与「建表」互相干扰。
//
// ⚠️ **必须用生产同一个 dialector 构造函数**（openMySQLDialector），不能用 mysql.Open：
//
// 用 mysql.Open 时真库上的 `clause.OnConflict{DoNothing:true}` 会因 MariaDB 的
// `... ON DUPLICATE KEY UPDATE id=id RETURNING id` 撞键也返回一行，被 gorm 记成
// RowsAffected=1。于是本文件里那些「二次加款」探针（TestMariaDBPaymentEventOnConflictIsRealUpsert
// / TestMariaDBPaymentEventReplayDoesNotCreditTwice）测的是**修复前的形态**，
// 不管生产改没改都恒定红 —— 那不是钉子，是永远红死的探针。
//
// 用生产构造函数之后，这批探针的含义变成：「生产 dialector 在真库上行为正确」。
// 想验证「退回 mysql.Open 会坏」的变异，靠的是 TestMariaDBProductionDialectorEmitsNoReturning
// 里对 DSN/构造函数的断言（那条不经过本 helper）。
func openMariaDBTestDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	normalized, fixes := normalizeMySQLDSN(dsn)
	if len(fixes) > 0 {
		t.Logf("测试 DSN 已补齐参数：%s", strings.Join(fixes, ", "))
	}
	database, err := gorm.Open(openMySQLDialector(normalized), &gorm.Config{
		Logger: gl.Default.LogMode(gl.Silent),
	})
	if err != nil {
		t.Fatalf("打开 MariaDB 失败（DSN 指向的库是否就绪？）：%v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("取 sql.DB 失败：%v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("MariaDB ping 失败：%v", err)
	}
	return database
}

// mysqlTestFreshDB 返回一个**已清空**的真库，供每个用例从零开始建表。
func mysqlTestFreshDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := mysqlTestDSN(t)
	database := openMariaDBTestDB(t, dsn)
	resetMariaDBTestSchema(t, database)
	return database
}

// resetMariaDBTestSchema 删除当前库里所有对象。
//
// 为什么不用 tools/mysql_clear.py：那个脚本是给运维用的（带结构备份等副作用），
// 测试里只需要「确定性地回到空库」。清完必须**复核确实为空**，
// 否则后续 migrate 会以「目标数据库不是空库」失败，而那种失败信息指向错误方向。
func resetMariaDBTestSchema(t *testing.T, database *gorm.DB) {
	t.Helper()
	type object struct {
		Name string
		Type string
	}
	var objects []object
	if err := database.Raw(
		"SELECT TABLE_NAME AS name, TABLE_TYPE AS type FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()",
	).Scan(&objects).Error; err != nil {
		t.Fatalf("读取目标库对象失败：%v", err)
	}
	if len(objects) > 0 {
		if err := database.Exec("SET FOREIGN_KEY_CHECKS = 0").Error; err != nil {
			t.Fatalf("关闭外键检查失败：%v", err)
		}
		for _, item := range objects {
			verb := "DROP TABLE IF EXISTS "
			if strings.EqualFold(item.Type, "VIEW") {
				verb = "DROP VIEW IF EXISTS "
			}
			if err := database.Exec(verb + mysqlTestQuoteIdentifier(item.Name)).Error; err != nil {
				t.Fatalf("删除 %s %s 失败：%v", item.Type, item.Name, err)
			}
		}
		if err := database.Exec("SET FOREIGN_KEY_CHECKS = 1").Error; err != nil {
			t.Fatalf("恢复外键检查失败：%v", err)
		}
	}
	var remaining int64
	if err := database.Raw(
		"SELECT COUNT(*) AS count FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()",
	).Scan(&remaining).Error; err != nil {
		t.Fatalf("复核空库失败：%v", err)
	}
	if remaining != 0 {
		t.Fatalf("清库后仍有 %d 张表，后续迁移会被「目标数据库不是空库」拒绝", remaining)
	}
}

func mysqlTestQuoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

// ---------------------------------------------------------------------------
// 元数据读取
// ---------------------------------------------------------------------------

// mariaDBServerVersion 返回 `SELECT VERSION()` 的原始值。
//
// 这是区分「真的在测 MariaDB」与「连到了别的东西」的唯一硬证据：
// 若 DSN 指向 MySQL 8.0，int(11) 显示宽度会变成 warning 甚至被改写，
// 整个作业的结论就不再适用于本项目的目标库。
func mariaDBServerVersion(t *testing.T, database *gorm.DB) string {
	t.Helper()
	var version string
	if err := database.Raw("SELECT VERSION() AS version").Scan(&version).Error; err != nil {
		t.Fatalf("读取数据库版本失败：%v", err)
	}
	return version
}

// mariaDBIndexColumns 按 SEQ_IN_INDEX 顺序返回某索引的列名。
func mariaDBIndexColumns(t *testing.T, database *gorm.DB, table, index string) []string {
	t.Helper()
	var rows []struct {
		Seq       int    `gorm:"column:SEQ_IN_INDEX"`
		Column    string `gorm:"column:COLUMN_NAME"`
		NonUnique int    `gorm:"column:NON_UNIQUE"`
	}
	if err := database.Raw(
		"SELECT SEQ_IN_INDEX, COLUMN_NAME, NON_UNIQUE FROM information_schema.STATISTICS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ? ORDER BY SEQ_IN_INDEX",
		table, index,
	).Scan(&rows).Error; err != nil {
		t.Fatalf("读取索引 %s(%s) 元数据失败：%v", table, index, err)
	}
	columns := make([]string, 0, len(rows))
	seen := map[int]bool{}
	for _, row := range rows {
		if row.Seq <= 0 {
			continue
		}
		seen[row.Seq] = true
		columns = append(columns, row.Column)
	}
	for position := 1; position <= len(rows); position++ {
		if !seen[position] {
			t.Fatalf("索引 %s(%s) 的 SEQ_IN_INDEX 不连续：%v", table, index, rows)
		}
	}
	return columns
}

// mariaDBIndexIsUnique 读取索引唯一性。
func mariaDBIndexIsUnique(t *testing.T, database *gorm.DB, table, index string) bool {
	t.Helper()
	var rows []struct {
		NonUnique int `gorm:"column:NON_UNIQUE"`
	}
	if err := database.Raw(
		"SELECT DISTINCT NON_UNIQUE FROM information_schema.STATISTICS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?",
		table, index,
	).Scan(&rows).Error; err != nil {
		t.Fatalf("读取索引唯一性 %s(%s) 失败：%v", table, index, err)
	}
	if len(rows) == 0 {
		t.Fatalf("索引 %s(%s) 不存在", table, index)
	}
	if len(rows) > 1 {
		t.Fatalf("索引 %s(%s) 的唯一性元数据不唯一：%v", table, index, rows)
	}
	return rows[0].NonUnique == 0
}

// mariaDBColumnType 返回 information_schema 里的 COLUMN_TYPE（含显示宽度）。
func mariaDBColumnType(t *testing.T, database *gorm.DB, table, column string) string {
	t.Helper()
	var rows []struct {
		ColumnType string `gorm:"column:COLUMN_TYPE"`
	}
	if err := database.Raw(
		"SELECT COLUMN_TYPE FROM information_schema.COLUMNS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?",
		table, column,
	).Scan(&rows).Error; err != nil {
		t.Fatalf("读取列类型 %s.%s 失败：%v", table, column, err)
	}
	if len(rows) != 1 {
		t.Fatalf("列 %s.%s 的元数据条数 = %d，want 1", table, column, len(rows))
	}
	return rows[0].ColumnType
}

// mustAutoMigrate 是 t.Fatalf 版本，避免在用例里重复写 if err != nil。
func mustAutoMigrate(t *testing.T, database *gorm.DB, models ...any) {
	t.Helper()
	if err := database.AutoMigrate(models...); err != nil {
		t.Fatalf("AutoMigrate 失败：%v", err)
	}
}

// describeMariaDBTable 给出表上的索引清单，用于「重跑后元数据未变化」的断言。
func describeMariaDBTable(t *testing.T, database *gorm.DB, table string) string {
	t.Helper()
	var rows []struct {
		Index    string `gorm:"column:INDEX_NAME"`
		Seq      int    `gorm:"column:SEQ_IN_INDEX"`
		Column   string `gorm:"column:COLUMN_NAME"`
		NonUniq  int    `gorm:"column:NON_UNIQUE"`
		Nullable string `gorm:"column:NULLABLE"`
	}
	if err := database.Raw(
		"SELECT INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, NON_UNIQUE, NULLABLE FROM information_schema.STATISTICS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX",
		table,
	).Scan(&rows).Error; err != nil {
		t.Fatalf("读取 %s 索引清单失败：%v", table, err)
	}
	var builder strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&builder, "%s/%d/%s/%d/%s;", row.Index, row.Seq, row.Column, row.NonUniq, row.Nullable)
	}
	return builder.String()
}
