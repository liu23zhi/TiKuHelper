package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gl "gorm.io/gorm/logger"
)

// SQLite → MariaDB 迁移端到端测试的**第 ① 步**：造源库 + 灌种子。
//
// task_doc/122 §5.2 决策 5 选的是「CI 里用 Go 现造」，而不是提交
// `run/data/tiku.db`（2.97 MB 且含真实日志/账单，被 .gitignore 排除）。
// 现造的两个好处：
//   - schema 由**同一套** ensure*Schema 生成，不会随 entity 演进静默失配；
//   - 顺带验证「ensure*Schema 建出来的 schema 能通过 migrate 工具的 4 类拒绝闸」——
//     将来某个迁移加了 CHECK 约束或外键，这里会直接变红。
//
// 分工（122 决策 6 = 选项 A）：
//
//	Go      → 只负责造源库（本文件）
//	python  → migrate_sqlite_to_mysql.py / verify_sqlite_mysql_migration.py（ci.yml 的 step）
//
// 为什么不把 python 塞进 Go：Go 侧一旦依赖 python 环境，本地就跑不了，
// 而本地跑不了的东西等于没有回归保护。这里的分工让**源库构造**在本地就能验证。

// e2eSourceDBEnv 指定源库输出路径；未设置时写到 t.TempDir()。
const e2eSourceDBEnv = "TIKU_E2E_SQLITE_OUT"

// e2eSeedTime 是种子数据的固定时间戳。
//
// 必须固定：verify 工具会按目标列的声明精度做时间归一后逐行比对，
// 用 time.Now() 会让「同一份种子两次运行产生不同字节」，失败时无法复现。
var e2eSeedTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// TestMariaDBMigrationE2EPrepare 造 SQLite 源库并灌种子。
//
// 注意它**不需要 MariaDB**：只写 SQLite 文件。因此本机（无 mysqld）也能真跑，
// 这是 CI 之外唯一能拿到「源库确实符合迁移工具前提」证据的地方。
func TestMariaDBMigrationE2EPrepare(t *testing.T) {
	sourcePath := os.Getenv(e2eSourceDBEnv)
	if strings.TrimSpace(sourcePath) == "" {
		sourcePath = filepath.Join(t.TempDir(), "e2e-source.db")
	} else {
		sourcePath = strings.TrimSpace(sourcePath)
		if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
			t.Fatalf("创建源库目录失败：%v", err)
		}
	}

	buildMariaDBSourceSQLite(t, sourcePath)

	// 把路径写进日志，供 workflow 的后续 step 与人工排查对照。
	t.Logf("E2E_SOURCE_SQLITE=%s", sourcePath)
	t.Logf("E2E_SOURCE_SIZE=%d", fileSize(t, sourcePath))
	t.Logf("E2E_SOURCE_SUMMARY=%s", mariaDBSourceSummary(t, sourcePath))
}

// buildMariaDBSourceSQLite 用生产同一套 ensure*Schema 建库并灌种子。
func buildMariaDBSourceSQLite(t *testing.T, path string) {
	t.Helper()

	// 与 db_test.go 的 openRegistryTestDB 同款前置：
	// development 模式不会要求 TIKU_ADMIN_PASSWORD 满足复杂度策略。
	t.Setenv("TIKU_APP_MODE", "development")
	t.Setenv("SQL_DSN", "")
	resetRegistryDBState(t)
	t.Cleanup(func() { resetRegistryDBState(t) })

	database, err := RegisterDBWithError(configs.Config{Database: configs.DatabaseConfig{Path: path}})
	if err != nil {
		t.Fatalf("构造 SQLite 源库失败：%v", err)
	}
	if database == nil {
		t.Fatal("RegisterDBWithError 返回了空句柄")
	}

	seedMariaDBSource(t, database)
	mustPassMigrateToolGates(t, database)

	// 显式 checkpoint 到主文件。
	//
	// ⚠️ 变异验证的实测结论（不要照抄「不 checkpoint 会读不到数据」这类直觉断言）：
	// 移除本行后 TestMariaDBMigrationE2ESourceIsReadable 与
	// TestMariaDBSourceSeedsCoverBoundaries **仍然 PASS**。原因是
	// glebarez/sqlite 在同一进程内重开同一路径时会读到 -wal 里的内容，
	// 所以那两个用例并不能证明 checkpoint 生效。
	//
	// 保留本行的理由与那两条用例无关：migrate 工具用 `?mode=ro` 打开，
	// 交付一个自洽的主文件能消除「读取视角依赖 -wal 是否可写」这个未知项。
	if err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		// checkpoint 失败不是致命错误（例如库不是 WAL 模式），但要留痕。
		t.Logf("wal_checkpoint(TRUNCATE) 未成功（不阻断）：%v", err)
	}
	if err := CloseDB(); err != nil {
		t.Fatalf("关闭源库失败（python 端需要独占读取）：%v", err)
	}
}

// seedMariaDBSource 灌入覆盖各类型边界值的种子。
//
// 选这些行的理由（每一条都对应 verify 工具的一类归一化）：
//   - 四字节 emoji        → utf8mb4 是否真的落地
//   - 含引号与反斜杠的文本 → 工具里 `sqlite_default_sql` 的转义路径
//   - 负数金额            → 「本金可为负」的业务口径（欠款）
//   - 6 位小数的金额       → DECIMAL 定标
//   - NULL 与空串并存      → 可空列语义
//   - 空表（blacklist_hits）→ 「0 行表也要建出来」
func seedMariaDBSource(t *testing.T, database *gorm.DB) {
	t.Helper()

	// user id=1 是 RegisterDBWithError 建库时由 applyAdminBootstrap 写入的，
	// 直接 Create 会撞 UNIQUE(users.id)。这里改成「补字段」而不是「再插一行」——
	// 顺带覆盖「已存在的行被就地更新」这条路径。
	if err := database.Model(&entity.User{}).Where("id = ?", 1).Updates(map[string]any{
		"nickname":      "管理员🧑‍🔬",
		"balance":       -1.5,
		"gift_balance":  2.25,
		"register_ip":   "203.0.113.9",
		"created_at":    e2eSeedTime,
		"last_login_ip": "",
	}).Error; err != nil {
		t.Fatalf("更新 admin 行失败：%v", err)
	}

	users := []entity.User{
		{
			ID: 2, Username: "e2e-user", Password: "plain-legacy-password",
			Nickname: "含引号的昵称 'x' 与反斜杠 \\ 和 emoji 🚀",
			Role:     "user", Status: "enabled", Balance: 0, GiftBalance: 0,
			RegisterSource: "legacy", CreatedAt: e2eSeedTime,
		},
	}
	if err := database.Create(&users).Error; err != nil {
		t.Fatalf("种入 users 失败：%v", err)
	}

	tikus := []entity.Tiku{
		{
			Question: "下列属于指示剂的有( )🧪", Type: 1, Options: `["A. 酚酞","B. 石蕊"]`,
			Answer: "AB", Hash: strings.Repeat("1", 32), CourseName: "化学", Extra: `{"tag":"e2e"}`, SourceName: "e2e",
		},
		{
			Question: "以下说法不正确的是( )", Type: 1, Options: `["A. 正确","B. 错误"]`,
			Answer: "B", Hash: strings.Repeat("2", 32), CourseName: "通用", Extra: "", SourceName: "e2e",
		},
	}
	if err := database.Create(&tikus).Error; err != nil {
		t.Fatalf("种入 tiku 失败：%v", err)
	}

	billing := []entity.BillingRecord{
		{
			ID: 1, UserID: 1, Type: "normal_search", Provider: "mock", ModelName: "m1",
			InputTokens: 12, OutputTokens: 3, TotalTokens: 15,
			Amount: 0.000062, BalanceBefore: -1.5, BalanceAfter: -1.500062,
			GiftCharged: 0, PrincipalCharged: 0.000062, RequestID: "E2E-REQ-1",
			ClientIP: "203.0.113.9", SearchMode: "normal", CreatedAt: e2eSeedTime,
		},
		{
			ID: 2, UserID: 2, Type: "deep_search", Provider: "", ModelName: "",
			Amount: 0, BalanceBefore: 0, BalanceAfter: 0,
			GiftCharged: 1.234567, PrincipalCharged: 0, RequestID: "E2E-REQ-2",
			QuestionText: "题干冗余副本 with 'quote'", CreatedAt: e2eSeedTime,
		},
	}
	if err := database.Create(&billing).Error; err != nil {
		t.Fatalf("种入 billing_records 失败：%v", err)
	}

	logs := []entity.Log{
		{
			Qid: 1, OldAnswer: "A", NewAnswer: "AB", UserID: 1, Username: "e2e-admin",
			Module: "tiku", Action: 1, Description: "e2e 修改答案🚀",
			CorrelationID: "e2e-corr-1", ClientIP: "203.0.113.9", CreateTime: e2eSeedTime,
		},
	}
	if err := database.Create(&logs).Error; err != nil {
		t.Fatalf("种入 logs 失败：%v", err)
	}

	recharge := []entity.RechargeOrder{
		{
			ID: 1, UserID: 1, OrderNo: "E2E-ORDER-1", PaymentProvider: "mock",
			PaymentMethodNameSnapshot: "余额", Amount: 12.5, Status: "paid",
			ThirdPartyOrderNo: "E2E-TRADE-1", PaidAt: &e2eSeedTime,
			CallbackPayload: `{"ok":true}`, CreatedAt: e2eSeedTime,
		},
	}
	if err := database.Create(&recharge).Error; err != nil {
		t.Fatalf("种入 recharge_orders 失败：%v", err)
	}

	// blacklist_hits 刻意留空：验证「0 行表也必须建出来并成功迁移」。
	if !database.Migrator().HasTable(entity.TableNameBlacklistHit) {
		t.Fatal("黑名单命中表不存在：ensureBlacklistSchema 未被启动流程调用")
	}
	var hitCount int64
	if err := database.Model(&entity.BlacklistHit{}).Count(&hitCount).Error; err != nil {
		t.Fatalf("统计 blacklist_hits 失败：%v", err)
	}
	if hitCount != 0 {
		t.Fatalf("blacklist_hits 应当为空表，实际 %d 行", hitCount)
	}

	entries := []entity.BlacklistEntry{
		{
			Kind: "ip", Pattern: "203.0.113.0/24", ValueHash: strings.Repeat("3", 64),
			ValueMasked: "203.0.113.*", MatchType: "cidr",
			Reason: "e2e 种子", CreatedAt: e2eSeedTime,
		},
	}
	if err := database.Create(&entries).Error; err != nil {
		t.Fatalf("种入 blacklist_entries 失败：%v", err)
	}
}

// mustPassMigrateToolGates 断言源库能通过 migrate_sqlite_to_mysql.py 的结构性拒绝闸。
//
// 逐条对应 tools/migrate_sqlite_to_mysql.py 的 raise：
//
//	L590 视图/触发器；L595 CHECK 约束；L597 非 BINARY COLLATE；
//	L601 外键；L603 生成列/隐藏列。
//
// 为什么在 Go 侧复刻一遍：这些是**源库自身的属性**，与目标库无关，
// 所以本地（无 MariaDB）也能验；而 workflow 里的 python 步骤只能验「当时那份
// 库文件」。两边都留下判据，任一处的 schema 演进都会被立刻发现。
func mustPassMigrateToolGates(t *testing.T, database *gorm.DB) {
	t.Helper()

	// ① 视图 / 触发器
	var objects []struct {
		Type string
		Name string
	}
	if err := database.Raw(
		"SELECT type, name FROM sqlite_master WHERE type IN ('view','trigger') AND lower(substr(name, 1, 7)) <> 'sqlite_'",
	).Scan(&objects).Error; err != nil {
		t.Fatalf("读取 sqlite_master 失败：%v", err)
	}
	if len(objects) > 0 {
		t.Fatalf("源库含视图/触发器（迁移工具会拒绝）：%v", objects)
	}

	tables := mariaDBSourceTables(t, database)
	if len(tables) == 0 {
		t.Fatal("源库没有业务表：迁移工具会以「没有可迁移的业务表」失败")
	}

	checkPattern := regexp.MustCompile(`(?i)\bCHECK\s*\(`)
	for _, table := range tables {
		ddl := mariaDBSourceTableDDL(t, database, table)
		// ② CHECK 约束
		if checkPattern.MatchString(ddl) {
			t.Fatalf("源表 %s 含 CHECK 约束（迁移工具会拒绝）：%s", table, ddl)
		}
		// ③ 非 BINARY COLLATE
		if collation := firstNonBinaryCollation(ddl); collation != "" {
			t.Fatalf("源表 %s 含非 BINARY 排序规则 %q（迁移工具会拒绝）：%s", table, collation, ddl)
		}
		// ④ 外键
		if rows := rawRows(t, database, fmt.Sprintf("PRAGMA foreign_key_list(%s)", sqliteQuote(table))); len(rows) > 0 {
			t.Fatalf("源表 %s 含外键（迁移工具会拒绝）：%v", table, rows)
		}
		// ⑤ 生成列 / 隐藏列（table_xinfo 的第 7 列非 0）
		for _, row := range rawRows(t, database, fmt.Sprintf("PRAGMA table_xinfo(%s)", sqliteQuote(table))) {
			if len(row) > 6 && row[6] != "0" && row[6] != "" {
				t.Fatalf("源表 %s 含生成列或隐藏列（迁移工具会拒绝）：列=%v hidden=%s", table, row[1], row[6])
			}
		}
	}
}

// mariaDBSourceTables 复制迁移工具的取表规则（排除 sqlite_ 前缀）。
func mariaDBSourceTables(t *testing.T, database *gorm.DB) []string {
	t.Helper()
	var names []string
	if err := database.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND lower(substr(name, 1, 7)) <> 'sqlite_' ORDER BY name",
	).Scan(&names).Error; err != nil {
		t.Fatalf("读取业务表清单失败：%v", err)
	}
	return names
}

func mariaDBSourceTableDDL(t *testing.T, database *gorm.DB, table string) string {
	t.Helper()
	var ddl string
	if err := database.Raw("SELECT COALESCE(sql, '') FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddl).Error; err != nil {
		t.Fatalf("读取 %s 的 DDL 失败：%v", table, err)
	}
	return ddl
}

// firstNonBinaryCollation 复刻迁移工具的正则语义：
//
//	\bCOLLATE\s+(?!BINARY\b)[A-Za-z_]+
//
// Go 的 regexp 不支持负向前瞻，所以手工扫描 COLLATE 之后那个标识符。
func firstNonBinaryCollation(ddl string) string {
	upper := strings.ToUpper(ddl)
	for offset := 0; ; {
		index := strings.Index(upper[offset:], "COLLATE")
		if index < 0 {
			return ""
		}
		position := offset + index
		// 词边界：前一个字符必须是分隔符，否则 "XCOLLATE" 也会命中。
		if position > 0 && isIdentifierByte(ddl[position-1]) {
			offset = position + len("COLLATE")
			continue
		}
		rest := ddl[position+len("COLLATE"):]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		end := 0
		for end < len(trimmed) && isIdentifierByte(trimmed[end]) {
			end++
		}
		word := trimmed[:end]
		if word != "" && !strings.EqualFold(word, "BINARY") {
			return word
		}
		offset = position + len("COLLATE")
	}
}

func isIdentifierByte(value byte) bool {
	return value == '_' ||
		(value >= '0' && value <= '9') ||
		(value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z')
}

// rawRows 把任意 PRAGMA 结果读成字符串二维表。
//
// 用 Rows() 而不是 Scan 到结构体：PRAGMA 的列集合随 SQLite 版本变化
// （`table_xinfo` 比 `table_info` 多两列），结构体绑定会静默丢列。
func rawRows(t *testing.T, database *gorm.DB, query string) [][]string {
	t.Helper()
	rows, err := database.Raw(query).Rows()
	if err != nil {
		t.Fatalf("执行 %s 失败：%v", query, err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("读取 %s 列名失败：%v", query, err)
	}
	var out [][]string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("扫描 %s 行失败：%v", query, err)
		}
		row := make([]string, len(values))
		for i, value := range values {
			switch typed := value.(type) {
			case nil:
				row[i] = ""
			case []byte:
				row[i] = string(typed)
			default:
				row[i] = fmt.Sprint(typed)
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 %s 失败：%v", query, err)
	}
	return out
}

func sqliteQuote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取源库大小失败：%v", err)
	}
	return info.Size()
}

// openE2ESourceDB 重新打开源库文件，模拟 python 侧 `?mode=ro` 的读取视角。
//
// 用 glebarez/sqlite 的默认 DSN（不加 -wal 相关参数）：`PRAGMA wal_checkpoint(TRUNCATE)`
// 之后主文件已自洽，能读到完整数据就证明 checkpoint 生效。
func openE2ESourceDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gl.Default.LogMode(gl.Silent)})
	if err != nil {
		t.Fatalf("重新打开源库失败：%v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("取 sql.DB 失败：%v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return database
}

// mariaDBSourceSummary 重新只读打开源库并汇总行数，作为「源库确实有数据」的证据。
func mariaDBSourceSummary(t *testing.T, path string) string {
	t.Helper()
	database := openE2ESourceDB(t, path)

	wanted := []string{
		entity.TableNameUser, entity.TableNameTiku, entity.TableNameBillingRecord,
		entity.TableNameLog, entity.TableNameRechargeOrder, entity.TableNamePaymentEvent,
		entity.TableNameBlacklistEntry, entity.TableNameBlacklistHit, entity.TableNameSystemSetting,
	}
	return mariaDBSeedSummary(t, database, wanted...)
}

// TestMariaDBMigrationE2ESourceIsReadable 断言源库在 Close 之后仍能被重新打开并读全。
//
// 它守的是「prepare 交出文件后 python 能读到东西」这条接缝。判据是
// 「重开后行数等于种子行数」，而不是「没报错」—— 后者在「读到空快照」时也成立。
//
// ⚠️ 变异验证的边界（实测）：把 buildMariaDBSourceSQLite 里的
// `PRAGMA wal_checkpoint(TRUNCATE)` 删掉，本用例**仍然 PASS** ——
// glebarez/sqlite 在同一进程内重开同一路径时会读到 -wal 里的内容，
// 所以它证明的是「Close 之后数据没丢」，**不是**「checkpoint 生效」。
// 不要在没有新证据的情况下把它描述成后者。
func TestMariaDBMigrationE2ESourceIsReadable(t *testing.T) {
	t.Setenv("TIKU_APP_MODE", "development")
	path := filepath.Join(t.TempDir(), "e2e-source-readback.db")
	buildMariaDBSourceSQLite(t, path)

	database := openE2ESourceDB(t, path)
	var tikuCount int64
	if err := database.Raw("SELECT COUNT(*) FROM " + sqliteQuote(entity.TableNameTiku)).Scan(&tikuCount).Error; err != nil {
		t.Fatalf("重新读取 tiku 失败：%v", err)
	}
	if tikuCount != 2 {
		t.Fatalf("只读读到的 tiku 行数 = %d, want 2（WAL 未 checkpoint 会读到残缺快照）", tikuCount)
	}
}

// TestMariaDBSourceSeedsCoverBoundaries 断言种子真的覆盖了边界值。
//
// 为什么需要它：种子一旦退化成「全是 ASCII、全是 0」，e2e 就变成
// 「能跑通但什么都验不到」——对应记忆里「跑了但没测到」那一类假绿。
func TestMariaDBSourceSeedsCoverBoundaries(t *testing.T) {
	t.Setenv("TIKU_APP_MODE", "development")
	path := filepath.Join(t.TempDir(), "e2e-source-boundaries.db")
	buildMariaDBSourceSQLite(t, path)

	database := openE2ESourceDB(t, path)

	var emojiRows int64
	if err := database.Raw(
		"SELECT COUNT(*) FROM " + sqliteQuote(entity.TableNameUser) + " WHERE nickname LIKE '%🧪%' OR nickname LIKE '%🚀%'",
	).Scan(&emojiRows).Error; err != nil {
		t.Fatalf("统计 emoji 行失败：%v", err)
	}
	if emojiRows == 0 {
		t.Fatal("种子里没有四字节 emoji：utf8mb4 落地这条判据会失效")
	}

	var negativeBalance int64
	if err := database.Raw(
		"SELECT COUNT(*) FROM " + sqliteQuote(entity.TableNameUser) + " WHERE balance < 0",
	).Scan(&negativeBalance).Error; err != nil {
		t.Fatalf("统计负余额失败：%v", err)
	}
	if negativeBalance == 0 {
		t.Fatal("种子里没有负 balance：双桶模型的「本金可为负」这条判据会失效")
	}

	var fractionalAmount int64
	if err := database.Raw(
		"SELECT COUNT(*) FROM " + sqliteQuote(entity.TableNameBillingRecord) + " WHERE gift_charged = 1.234567",
	).Scan(&fractionalAmount).Error; err != nil {
		t.Fatalf("统计小数金额失败：%v", err)
	}
	if fractionalAmount != 1 {
		t.Fatalf("6 位小数金额的行数 = %d, want 1（DECIMAL 定标判据需要它）", fractionalAmount)
	}

	var quotedRows int64
	if err := database.Raw(
		"SELECT COUNT(*) FROM " + sqliteQuote(entity.TableNameBillingRecord) + " WHERE question_text LIKE '%''quote''%'",
	).Scan(&quotedRows).Error; err != nil {
		t.Fatalf("统计带引号文本失败：%v", err)
	}
	if quotedRows != 1 {
		t.Fatalf("含单引号的文本行数 = %d, want 1（转义路径判据需要它）", quotedRows)
	}
}
