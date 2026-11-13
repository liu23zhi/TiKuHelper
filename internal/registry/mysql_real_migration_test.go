package registry

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 三个「假驱动」迁移测试的真库版（task_doc/122 决策 2：新增，不替换）。
//
// 假驱动（billing_record_indexes_mysql_test.go / payment_event_migration_mysql_test.go /
// blacklist_migration_test.go 的 MySQL 段）能断言「生成的 SQL 长什么样」，
// 但它们的 information_schema 回复是**手写**的，`clause.OnConflict` 也没实现 upsert。
// 所以下面这些用例只做假驱动**证明不了**的事：
//
//   - DDL 到底能不能被服务器接受（MySQL 的 CREATE INDEX 没有 IF NOT EXISTS）；
//   - information_schema 的真实语义（TABLE_SCHEMA=DATABASE() 是否命中、SEQ_IN_INDEX 顺序）；
//   - 唯一索引是否真的拦住重复插入；
//   - ON DUPLICATE KEY 的 RowsAffected 到底是 0 还是 1；
//   - 标记表读写是否真的在「key 是保留字」的前提下成立。

// ---------------------------------------------------------------------------
// 方言自证：先证明「连的是 MariaDB」，否则后面所有结论都失去前提
// ---------------------------------------------------------------------------

// TestMariaDBServerIsActuallyMariaDB 是 122 §3.3 坑 3 / §7 最后一步的 Go 版。
//
// 没有这一步，`int(11)` 之类的断言在任何 MySQL 兼容库上都能通过，
// 于是「真的在测 MariaDB」这件事无人验证 —— 一旦 DSN 指错库，
// 整条作业会以「全绿」的形态给出与目标库无关的结论。
func TestMariaDBServerIsActuallyMariaDB(t *testing.T) {
	database := mysqlTestFreshDB(t)

	version := mariaDBServerVersion(t, database)
	t.Logf("目标库版本：%s", version)
	if !strings.Contains(version, "MariaDB") {
		t.Fatalf("目标库版本 %q 不含 MariaDB：本作业的结论只对 MariaDB 成立，DSN 可能指向了别的库", version)
	}

	// 迁移工具的分支判据（tools/migrate_sqlite_to_mysql.py:472）依赖 11.4 >= 10.2.2。
	// 版本号解析失败时工具会走「旧版本」分支并因 767 字节索引上限误报，
	// 所以这里把「版本可解析且足够新」也钉住。
	var parsed struct {
		Major int `gorm:"column:major"`
		Minor int `gorm:"column:minor"`
		Patch int `gorm:"column:patch"`
	}
	if err := database.Raw(
		"SELECT CAST(SUBSTRING_INDEX(SUBSTRING_INDEX(SUBSTRING_INDEX(VERSION(), '-', 1), '.', 1), '.', 1) AS UNSIGNED) AS major, " +
			"CAST(SUBSTRING_INDEX(SUBSTRING_INDEX(SUBSTRING_INDEX(VERSION(), '-', 1), '.', 2), '.', -1) AS UNSIGNED) AS minor, " +
			"CAST(SUBSTRING_INDEX(SUBSTRING_INDEX(SUBSTRING_INDEX(VERSION(), '-', 1), '.', 3), '.', -1) AS UNSIGNED) AS patch",
	).Scan(&parsed).Error; err != nil {
		t.Fatalf("解析版本号失败：%v", err)
	}
	if parsed.Major < 10 || (parsed.Major == 10 && parsed.Minor < 2) || (parsed.Major == 10 && parsed.Minor == 2 && parsed.Patch < 2) {
		t.Fatalf("MariaDB 版本 %d.%d.%d 低于迁移工具门槛 10.2.2", parsed.Major, parsed.Minor, parsed.Patch)
	}
}

// TestMariaDBPreservesIntegerDisplayWidth 证明 MariaDB 保留 int(11) 显示宽度。
//
// 这是 task_doc/122 §6.1.2 第 1 条选 MariaDB 的实测依据（项目里 37 处
// `type:int(11)`，MySQL 8.0.19 起弃用显示宽度）。假驱动只能看到 GORM 拼出的
// `int(11)` 字面量，看不到服务器是否把它改写 —— 只有 information_schema 能回答。
func TestMariaDBPreservesIntegerDisplayWidth(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.Log{}, &entity.User{})

	cases := []struct {
		table  string
		column string
		want   string
	}{
		{table: entity.TableNameLog, column: "qid", want: "int(11)"},
		{table: entity.TableNameLog, column: "action", want: "tinyint(4)"},
		{table: entity.TableNameUser, column: "parent_id", want: "int(11)"},
		{table: entity.TableNameUser, column: "invited_by_user_id", want: "int(11)"},
	}
	for _, testCase := range cases {
		got := mariaDBColumnType(t, database, testCase.table, testCase.column)
		if got != testCase.want {
			t.Fatalf("%s.%s 的 COLUMN_TYPE = %q, want %q（MariaDB 应完整保留显示宽度）",
				testCase.table, testCase.column, got, testCase.want)
		}
	}
}

// TestMariaDBStoresFourByteCharacters 证明 utf8mb4 真的能存 4 字节字符。
//
// 假驱动完全没有字符集概念。若目标库是 utf8(3 字节)，emoji 会被截断或报
// "Incorrect string value"，而这类错误在迁移工具里表现为「行数校验失败」，
// 排查时会误以为是复制逻辑坏了。
func TestMariaDBStoresFourByteCharacters(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.Tiku{})

	const emoji = "🧪🔬 emoji 题库"
	row := entity.Tiku{
		Question:   "题干含四字节字符：" + emoji,
		Type:       1,
		Options:    "[]",
		Answer:     "A",
		Hash:       strings.Repeat("a", 32),
		CourseName: "课程" + emoji,
		Extra:      "",
		SourceName: "real-mariadb",
	}
	if err := database.Create(&row).Error; err != nil {
		t.Fatalf("写入四字节字符失败（目标库字符集可能不是 utf8mb4）：%v", err)
	}
	var got entity.Tiku
	if err := database.Where("id = ?", row.ID).Take(&got).Error; err != nil {
		t.Fatalf("回读失败：%v", err)
	}
	if !strings.Contains(got.Question, emoji) || !strings.Contains(got.CourseName, emoji) {
		t.Fatalf("四字节字符往返损坏：question=%q course=%q", got.Question, got.CourseName)
	}
}

// ---------------------------------------------------------------------------
// 真库版 ①：黑名单迁移（blacklist_migration.go）
// ---------------------------------------------------------------------------

// TestMariaDBBlacklistMigrationCreatesRealIndexes 跑真库版黑名单迁移。
//
// 与假驱动的分工：
//   - 假驱动断言 blacklistIndexDDL("mysql") 的**字符串**（MySQL 不能带 IF NOT EXISTS）；
//   - 这里断言那条 DDL **真的被执行成功**，且索引的列顺序/唯一性在
//     information_schema 里正确 —— 这正是 122 §1.3 表格里
//     「`SELECT ... FROM INFORMATION_SCHEMA` 全部被假驱动手写返回值」那条缺口。
func TestMariaDBBlacklistMigrationCreatesRealIndexes(t *testing.T) {
	database := mysqlTestFreshDB(t)
	// system_settings 必须先存在：迁移结束时会写一次性标记。
	mustAutoMigrate(t, database, &entity.SystemSetting{})

	if err := ensureBlacklistSchema(database); err != nil {
		t.Fatalf("真实 MariaDB 上跑黑名单迁移失败：%v", err)
	}

	for _, table := range []string{entity.TableNameBlacklistEntry, entity.TableNameBlacklistHit} {
		if !database.Migrator().HasTable(table) {
			t.Fatalf("迁移后表 %s 不存在", table)
		}
	}

	// 唯一索引 (kind, value_hash, match_type)：列顺序与唯一性都要在真库里核对。
	gotColumns := mariaDBIndexColumns(t, database, entity.TableNameBlacklistEntry, "uk_blacklist_entry_pattern")
	wantColumns := []string{"kind", "value_hash", "match_type"}
	if strings.Join(gotColumns, ",") != strings.Join(wantColumns, ",") {
		t.Fatalf("uk_blacklist_entry_pattern 列 = %v, want %v", gotColumns, wantColumns)
	}
	if !mariaDBIndexIsUnique(t, database, entity.TableNameBlacklistEntry, "uk_blacklist_entry_pattern") {
		t.Fatal("uk_blacklist_entry_pattern 必须是唯一索引，否则同一模式会被重复拉黑")
	}

	// 非唯一筛选索引。
	if got := mariaDBIndexColumns(t, database, entity.TableNameBlacklistEntry, "idx_blacklist_entry_kind_enabled"); strings.Join(got, ",") != "kind,enabled" {
		t.Fatalf("idx_blacklist_entry_kind_enabled 列 = %v, want [kind enabled]", got)
	}
	if mariaDBIndexIsUnique(t, database, entity.TableNameBlacklistEntry, "idx_blacklist_entry_kind_enabled") {
		t.Fatal("idx_blacklist_entry_kind_enabled 不应是唯一索引")
	}
	if got := mariaDBIndexColumns(t, database, entity.TableNameBlacklistHit, "idx_blacklist_hit_entry"); strings.Join(got, ",") != "entry_id,created_at" {
		t.Fatalf("idx_blacklist_hit_entry 列 = %v, want [entry_id created_at]", got)
	}
	if got := mariaDBIndexColumns(t, database, entity.TableNameBlacklistHit, "idx_blacklist_hit_created"); strings.Join(got, ",") != "created_at" {
		t.Fatalf("idx_blacklist_hit_created 列 = %v, want [created_at]", got)
	}

	// 迁移标记必须真的写进去了（真库上 `key` 是保留字，写不进去就会每次启动重跑）。
	if !systemSettingFlagIsSet(database, blacklistSchemaReadyKey) {
		t.Fatal("黑名单迁移标记未写入：真库上 key 列若未正确加引号，标记会读不到也写不进")
	}
}

// TestMariaDBBlacklistUniqueIndexReallyRejectsDuplicates 证明唯一索引**真的**生效。
//
// 假驱动完全没有约束语义：它连 INSERT 都不真正执行，所以「同一模式不重复拉黑」
// 这条业务保障在假驱动下**从未被验证过**。这条用例补的正是这个洞。
func TestMariaDBBlacklistUniqueIndexReallyRejectsDuplicates(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.SystemSetting{})
	if err := ensureBlacklistSchema(database); err != nil {
		t.Fatalf("黑名单迁移失败：%v", err)
	}

	pattern := "203.0.113.7"
	digest := sha256.Sum256([]byte(pattern))
	entry := entity.BlacklistEntry{
		Kind:        "ip",
		Pattern:     pattern,
		ValueHash:   hex.EncodeToString(digest[:]),
		ValueMasked: "203.0.113.*",
		MatchType:   "exact",
		Reason:      "real-mariadb 真库唯一索引用例",
	}
	if err := database.Create(&entry).Error; err != nil {
		t.Fatalf("首次写入黑名单规则失败：%v", err)
	}

	duplicate := entity.BlacklistEntry{
		Kind:        "ip",
		Pattern:     pattern,
		ValueHash:   hex.EncodeToString(digest[:]),
		ValueMasked: "203.0.113.*",
		MatchType:   "exact",
		Reason:      "重复模式必须被唯一索引挡住",
	}
	err := database.Create(&duplicate).Error
	if err == nil {
		t.Fatal("重复的 (kind, value_hash, match_type) 竟然写入成功：唯一索引在真库上没有生效")
	}
	if !isMariaDBDuplicateError(err) {
		t.Fatalf("失败原因不是唯一键冲突（可能是别的错误被误认）：%v", err)
	}

	// 反向对照：改 match_type 就应当成功 —— 证明拦住它的是索引而不是别的闸。
	duplicate.ID = 0
	duplicate.MatchType = "cidr"
	duplicate.Pattern = "203.0.113.0/24"
	if err := database.Create(&duplicate).Error; err != nil {
		t.Fatalf("不同 match_type 的规则应当允许写入，却被拒绝：%v", err)
	}
}

// TestMariaDBBlacklistMigrationIsIdempotent 证明真库上重跑迁移不报错、不改 schema。
//
// MySQL 没有 CREATE INDEX IF NOT EXISTS，所以「重跑」是这块代码最容易出错的地方：
// 只要 HasIndex 判断失效，第二次就会撞 1061 Duplicate key name。
func TestMariaDBBlacklistMigrationIsIdempotent(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.SystemSetting{})
	if err := ensureBlacklistSchema(database); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	beforeEntry := describeMariaDBTable(t, database, entity.TableNameBlacklistEntry)
	beforeHit := describeMariaDBTable(t, database, entity.TableNameBlacklistHit)

	for attempt := 2; attempt <= 3; attempt++ {
		if err := ensureBlacklistSchema(database); err != nil {
			t.Fatalf("第 %d 次迁移失败（重跑必须是幂等的）：%v", attempt, err)
		}
	}
	if after := describeMariaDBTable(t, database, entity.TableNameBlacklistEntry); after != beforeEntry {
		t.Fatalf("重跑后 blacklist_entries 索引元数据变了：\nbefore=%s\nafter=%s", beforeEntry, after)
	}
	if after := describeMariaDBTable(t, database, entity.TableNameBlacklistHit); after != beforeHit {
		t.Fatalf("重跑后 blacklist_hits 索引元数据变了：\nbefore=%s\nafter=%s", beforeHit, after)
	}
}

// TestMariaDBBlacklistRebuildsMismatchedIndex 证明索引列不符时会被重建。
//
// 122 §1.3 指出假驱动的 information_schema 回复是手写的，所以
// 「读到真实列顺序后判定不符 → drop 再 create」这条路径在假驱动下从未真正执行。
func TestMariaDBBlacklistRebuildsMismatchedIndex(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.SystemSetting{}, &entity.BlacklistHit{})

	// 手工建一个「名字对、列不对」的索引，模拟历史遗留形态。
	mustExec(t, database, "CREATE TABLE `blacklist_entries` ("+
		"`id` bigint NOT NULL AUTO_INCREMENT, `kind` varchar(16) NOT NULL DEFAULT '', "+
		"`pattern` varchar(191) NOT NULL DEFAULT '', `value_hash` char(64) NOT NULL DEFAULT '', "+
		"`value_masked` varchar(191) NOT NULL DEFAULT '', `match_type` varchar(16) NOT NULL DEFAULT 'exact', "+
		"`reason` varchar(255) NOT NULL DEFAULT '', `enabled` tinyint(1) NOT NULL DEFAULT 1, "+
		"`expires_at` datetime(3) NULL, `created_by` int(11) NOT NULL DEFAULT 0, "+
		"`created_at` datetime(3) NULL, `updated_at` datetime(3) NULL, "+
		"PRIMARY KEY (`id`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
	mustExec(t, database, "CREATE UNIQUE INDEX `uk_blacklist_entry_pattern` ON `blacklist_entries` (`kind`)")

	if err := ensureBlacklistSchema(database); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	got := mariaDBIndexColumns(t, database, entity.TableNameBlacklistEntry, "uk_blacklist_entry_pattern")
	want := []string{"kind", "value_hash", "match_type"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("列不符的唯一索引没有被重建，实际列 = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// 真库版 ②：支付已入账事件表（payment_event_migration.go）
// ---------------------------------------------------------------------------

// TestMariaDBPaymentEventMigrationRunsForReal 真库上建表 + 回填。
//
// 假驱动（payment_event_migration_mysql_test.go）声明自己「does not claim to
// validate a MySQL server's storage engine」，这里补的就是那一层：
// AutoMigrate 的 DDL 是否被服务器接受、唯一索引是否真的建出来、
// 回填是否真的把 paid_at 非空的历史订单复制成事件、标记是否落库。
func TestMariaDBPaymentEventMigrationRunsForReal(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.SystemSetting{}, &entity.User{}, &entity.RechargeOrder{})

	paidAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	orders := []entity.RechargeOrder{
		{UserID: 11, OrderNo: "MARIA-PAID-1", PaymentProvider: "mock", Amount: 12.5, Status: "paid", ThirdPartyOrderNo: "T1", PaidAt: &paidAt},
		{UserID: 12, OrderNo: "MARIA-PAID-2", PaymentProvider: "mock", Amount: 33.25, Status: "refunded", ThirdPartyOrderNo: "T2", PaidAt: &paidAt},
		// paid_at 为空的订单**不能**回填：它是「从未入账」的准确集合成员。
		{UserID: 13, OrderNo: "MARIA-UNPAID-1", PaymentProvider: "mock", Amount: 99, Status: "pending"},
	}
	if err := database.Create(&orders).Error; err != nil {
		t.Fatalf("种入充值订单失败：%v", err)
	}

	if err := ensurePaymentEventSchema(database); err != nil {
		t.Fatalf("真实 MariaDB 上跑支付事件迁移失败：%v", err)
	}

	var events []entity.PaymentEvent
	if err := database.Order("order_no asc").Find(&events).Error; err != nil {
		t.Fatalf("读取 payment_events 失败：%v", err)
	}
	if len(events) != 2 {
		t.Fatalf("回填事件数 = %d, want 2（只有 paid_at 非空的订单应回填）：%+v", len(events), events)
	}
	for _, event := range events {
		if event.Source != paymentEventBackfillSource {
			t.Fatalf("事件 %s 的 source = %q, want %q", event.OrderNo, event.Source, paymentEventBackfillSource)
		}
	}
	if events[0].OrderNo != "MARIA-PAID-1" || events[1].OrderNo != "MARIA-PAID-2" {
		t.Fatalf("回填的订单号 = %q,%q", events[0].OrderNo, events[1].OrderNo)
	}
	// status=refunded 那条也要回填（判据是 paid_at 而不是 status）。
	if events[1].Amount != 33.25 {
		t.Fatalf("refunded 订单的事件金额 = %v, want 33.25", events[1].Amount)
	}

	// order_no 唯一索引必须真的存在，且真的拦住重复。
	if !mariaDBIndexIsUnique(t, database, entity.TableNamePaymentEvent, "idx_payment_events_order_no") {
		t.Fatalf("payment_events.order_no 唯一索引不存在或非唯一；实际索引清单：%s",
			describeMariaDBTable(t, database, entity.TableNamePaymentEvent))
	}
	duplicate := entity.PaymentEvent{OrderNo: "MARIA-PAID-1", RechargeOrderID: orders[0].ID, UserID: 11, Amount: 1, Source: "manual"}
	if err := database.Create(&duplicate).Error; err == nil {
		t.Fatal("重复 order_no 竟然写入成功：加款幂等的唯一锚点在真库上没有生效")
	} else if !isMariaDBDuplicateError(err) {
		t.Fatalf("失败原因不是唯一键冲突：%v", err)
	}

	if !systemSettingFlagIsSet(database, paymentEventSchemaReadyKey) {
		t.Fatalf("支付事件迁移标记未写入：下次启动会重复回填")
	}
}

// TestMariaDBPaymentEventOnConflictIsRealUpsert 证明 `clause.OnConflict{DoNothing}`
// 在真库上真的走 ON DUPLICATE KEY，且「首次 / 非首次」能否被 RowsAffected 区分。
//
// 这条用例钉的是**业务契约**（见 entity.PaymentEvent 注释与
// service/payment_event.go:80）：`RowsAffected == 1` 才算首次入账，重投必须
// 落到「非首次」。它不钉具体数值 0 —— 驱动换计数口径不该让用例误红。
//
// 真库实测（mariadb 11.4.13 + gorm.io/driver/mysql v1.6.0 + gorm v1.31.2）：
//
//	                                 首行 | 重投
//	裸 SQL: ON DUPLICATE KEY UPDATE     1  |  0
//	裸 SQL: 同上 + RETURNING id         0  |  0
//	gorm 默认（RETURNING 开启）          1  |  1     ← 重投返回 1，与首次无法区分
//	gorm DisableWithReturning=true       1  |  0
//
// 根因是 **RETURNING 结果集**，不是「ON DUPLICATE KEY 按被更新的行计数」：
// v1.6.0 的 mysql.go:128-181 在服务器版本 >= 10.5 时给 Create 回调追加
// RETURNING 子句；MariaDB 的 `ON DUPLICATE KEY UPDATE pk=pk RETURNING pk`
// 在撞键时**仍返回一行**（实测 `SELECT id` 能看到既有 id）。gorm 走
// QueryContext + gorm.Scan（callbacks/create.go:80-106），把「扫描到的行数」
// 记账成 RowsAffected，于是撞键也记 1；批量两行重投则记 2。
// 裸 SQL 的 Exec RowsAffected 在同样语句上是 0，可见差异全部来自驱动这条路径。
//
// 因此本用例在**当前配置下应当变红**：它是「二次加款」红线的探针，不是钉子写错。
func TestMariaDBPaymentEventOnConflictIsRealUpsert(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.PaymentEvent{})

	// 单行首次：真插入 = 1，这是 production 判「首次入账」的唯一取值。
	first := entity.PaymentEvent{OrderNo: "MARIA-UPSERT-1", RechargeOrderID: 1, UserID: 1, Amount: 8, Source: "notify"}
	result := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&first)
	if result.Error != nil {
		t.Fatalf("首次 upsert 失败：%v", result.Error)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("首次单行 upsert RowsAffected = %d, want 1（只有真插入才是首次入账）", result.RowsAffected)
	}

	// 单行重投：必须**不是** 1，否则网关重投会被判成首次入账并二次加款。
	replayed := entity.PaymentEvent{OrderNo: "MARIA-UPSERT-1", RechargeOrderID: 1, UserID: 1, Amount: 8, Source: "notify"}
	result = database.Clauses(clause.OnConflict{DoNothing: true}).Create(&replayed)
	if result.Error != nil {
		t.Fatalf("重复 upsert 失败（DoNothing 不应报错）：%v", result.Error)
	}
	t.Logf("单行重投 RowsAffected = %d（非 1 才算「已入过账」）", result.RowsAffected)
	if result.RowsAffected == 1 {
		t.Fatalf("单行重投 RowsAffected = 1，与首次插入无法区分：service/payment_event.go:80 的 " +
			"`RowsAffected == 1` 会把网关重投判成首次入账并二次加款。\n" +
			"实测根因是 MariaDB RETURNING 结果集被 gorm 记成 RowsAffected（见本函数注释的对照表）；" +
			"驱动侧规避手段是 mysql.New(mysql.Config{DSN: ..., DisableWithReturning: true})。\n" +
			"注：本用例**不是**把期望写成 want 2，而是要求重投与首次可区分。")
	}

	// 第二条独立断言：重投**不新增行**。它与 RowsAffected 的取值无关，
	// 所以即使驱动换了计数口径（1 / 2 / 0）这条也不会跟着漂。
	var countAfterFirst int64
	if err := database.Model(&entity.PaymentEvent{}).Count(&countAfterFirst).Error; err != nil {
		t.Fatalf("统计事件行数失败：%v", err)
	}
	second := entity.PaymentEvent{OrderNo: "MARIA-UPSERT-2", RechargeOrderID: 2, UserID: 2, Amount: 9, Source: "notify"}
	if err := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&second).Error; err != nil {
		t.Fatalf("插入第二条事件失败：%v", err)
	}
	replayedTwo := []entity.PaymentEvent{
		{OrderNo: "MARIA-UPSERT-1", RechargeOrderID: 1, UserID: 1, Amount: 8, Source: "notify"},
		{OrderNo: "MARIA-UPSERT-2", RechargeOrderID: 2, UserID: 2, Amount: 9, Source: "notify"},
	}
	result = database.Clauses(clause.OnConflict{DoNothing: true}).Create(&replayedTwo)
	if result.Error != nil {
		t.Fatalf("重复 upsert 两条失败（DoNothing 不应报错）：%v", result.Error)
	}
	var count int64
	if err := database.Model(&entity.PaymentEvent{}).Count(&count).Error; err != nil {
		t.Fatalf("统计事件行数失败：%v", err)
	}
	if count != 2 {
		t.Fatalf("事件行数 = %d, want 2（重投不应插入新行；首次后 %d 行）", count, countAfterFirst)
	}
}

// TestMariaDBPaymentEventReplayDoesNotCreditTwice 把「二次加款」红线钉在**生产代码**上。
//
// 上一条用例只证明 clause 的 RowsAffected 语义，不经过 service 的分支判断。
// 而真正的资金风险在 service/payment_event.go:80 的 `result.RowsAffected == 1`。
//
// 这里走真实的 `HandleRechargePaymentNotifyWithDB`，并且**先把订单状态改成
// refunded** 才能绕开 status==paid 的快速通道、真正落到 claim 分支：
//
//	首次回调 → 加款 8；把订单改成 refunded；再次回调 → 余额必须仍是 8。
//
// 实测（mariadb 11.4.13）：本用例在**未做任何生产改动**时就会红，余额变成 16。
// 原因同 TestMariaDBPaymentEventOnConflictIsRealUpsert 的注释 —— 撞键的
// `... RETURNING id` 会返回一行，gorm 把它记成 RowsAffected=1，于是
// `claimRechargePaymentEventInTx` 在重投时返回 firstCredit=true。
// 也就是说「2 与 0 都落进非首次」这个前提在 MariaDB + RETURNING 下不成立：
// 单行重投返回的是 **1**，正好等于「首次」。
//
// 变异 `== 1` → `>= 1` 时本用例同样红（余额 16），说明它确实覆盖了这条分支。
func TestMariaDBPaymentEventReplayDoesNotCreditTwice(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database,
		&entity.SystemSetting{}, &entity.User{}, &entity.RechargeOrder{},
		&entity.BillingRecord{}, &entity.Log{}, &entity.PaymentEvent{})

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥失败：%v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("序列化公钥失败：%v", err)
	}
	// 直接写 system_settings 行而不是走 UpdateSystemSettingDetailWithDB：
	// 后者会顺带校验/归一化几十个无关配置项，任何一个失败都会让本用例
	// 因为与本缺陷无关的原因变红。
	for _, setting := range []entity.SystemSetting{
		{Key: service.EPayGatewayURLSettingKey, Value: "https://pay.example.com"},
		{Key: service.EPayPartnerIDSettingKey, Value: "10001"},
		{Key: service.EPayPlatformPublicKeySettingKey, Value: base64.StdEncoding.EncodeToString(publicDER)},
	} {
		if err := database.Create(&setting).Error; err != nil {
			t.Fatalf("种入 system_settings 失败：%v", err)
		}
	}

	user := entity.User{Username: "maria-credit-once", Password: "x", Role: "user", Status: "enabled", RegisterSource: "admin"}
	if err := database.Create(&user).Error; err != nil {
		t.Fatalf("种入用户失败：%v", err)
	}
	order := entity.RechargeOrder{
		UserID: user.ID, OrderNo: "MARIA-CREDIT-ONCE-1", PaymentProvider: "epay",
		Amount: 8, Status: "pending",
	}
	if err := database.Create(&order).Error; err != nil {
		t.Fatalf("种入充值订单失败：%v", err)
	}
	if err := ensurePaymentEventSchema(database); err != nil {
		t.Fatalf("迁移支付事件表失败：%v", err)
	}

	notify := func() {
		t.Helper()
		values := url.Values{}
		values.Set("pid", "10001")
		values.Set("type", "alipay")
		values.Set("out_trade_no", order.OrderNo)
		values.Set("trade_no", "maria-trade-1")
		values.Set("trade_status", "TRADE_SUCCESS")
		values.Set("money", "8.00")
		mariaDBSignNotifyValues(t, values, privateKey)
		if acknowledged, err := service.HandleRechargePaymentNotifyWithDB(database, values); err != nil || !acknowledged {
			t.Fatalf("处理充值回调失败：ack=%v err=%v", acknowledged, err)
		}
	}

	balance := func() float64 {
		t.Helper()
		var reloaded entity.User
		if err := database.Where("id = ?", user.ID).First(&reloaded).Error; err != nil {
			t.Fatalf("回读用户失败：%v", err)
		}
		return reloaded.Balance
	}

	notify()
	if got := balance(); got != 8 {
		t.Fatalf("首次回调后余额 = %v, want 8", got)
	}

	// 关键前置：绕开 status==paid 的快速通道，让第二次回调真的走到 claim 分支。
	if err := database.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).
		Update("status", "refunded").Error; err != nil {
		t.Fatalf("把订单状态改成 refunded 失败：%v", err)
	}
	notify()
	if got := balance(); got != 8 {
		t.Fatalf("订单改为 refunded 后重投回调，余额 = %v, want 8（真实加款两次 = 二次加款漏洞）", got)
	}

	var events int64
	if err := database.Model(&entity.PaymentEvent{}).Count(&events).Error; err != nil {
		t.Fatalf("统计事件行数失败：%v", err)
	}
	if events != 1 {
		t.Fatalf("事件行数 = %d, want 1（一笔网关订单最多一条入账事件）", events)
	}
}

// mariaDBSignNotifyValues 按 service.buildRechargeSignContent 的口径签名。
//
// 不复用测试内的同名 helper：签名串的构成（排序、跳过 sign/sign_type、跳过空值）
// 就是网关协议本身，写在这里才能让「签名口径被改动」在本用例里也能被发现。
func mariaDBSignNotifyValues(t *testing.T, values url.Values, privateKey *rsa.PrivateKey) {
	t.Helper()
	keys := make([]string, 0, len(values))
	for key, list := range values {
		if key == "sign" || key == "sign_type" {
			continue
		}
		if len(list) == 0 || strings.TrimSpace(list[0]) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "&")))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatalf("签名失败：%v", err)
	}
	values.Set("sign", base64.StdEncoding.EncodeToString(signature))
	values.Set("sign_type", "RSA")
}

// TestMariaDBPaymentEventMigrationIsNoOpOnSecondStartup 证明第二次启动是纯 no-op。
//
// 判据不能只看「没报错」：必须在**真库**上确认标记被读到
// （`key` 是保留字，读不到就会每次重跑）且事件行数不再增长。
func TestMariaDBPaymentEventMigrationIsNoOpOnSecondStartup(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.SystemSetting{}, &entity.User{}, &entity.RechargeOrder{})
	paidAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := database.Create(&entity.RechargeOrder{
		UserID: 21, OrderNo: "MARIA-NOOP-1", PaymentProvider: "mock", Amount: 5, Status: "paid", PaidAt: &paidAt,
	}).Error; err != nil {
		t.Fatalf("种入订单失败：%v", err)
	}
	if err := ensurePaymentEventSchema(database); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	var first int64
	if err := database.Model(&entity.PaymentEvent{}).Count(&first).Error; err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if first != 1 {
		t.Fatalf("首次迁移后事件数 = %d, want 1", first)
	}
	beforeSchema := describeMariaDBTable(t, database, entity.TableNamePaymentEvent)

	if err := ensurePaymentEventSchema(database); err != nil {
		t.Fatalf("第二次启动的迁移失败（应为纯 no-op）：%v", err)
	}
	var second int64
	if err := database.Model(&entity.PaymentEvent{}).Count(&second).Error; err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if second != first {
		t.Fatalf("第二次启动后事件数从 %d 变成 %d：标记没被读到，回填重跑了", first, second)
	}
	if after := describeMariaDBTable(t, database, entity.TableNamePaymentEvent); after != beforeSchema {
		t.Fatalf("第二次启动改动了表结构：\nbefore=%s\nafter=%s", beforeSchema, after)
	}
}

// ---------------------------------------------------------------------------
// 真库版 ③：账单复合索引（billing_record_indexes_migration.go）
// ---------------------------------------------------------------------------

// TestMariaDBBillingIndexesMigrationRunsForReal 真库上建复合索引。
//
// 122 §1.3 的核心缺口：`SELECT SEQ_IN_INDEX, COLUMN_NAME FROM
// INFORMATION_SCHEMA.STATISTICS` 在假驱动里是手写返回值，所以
// 「真实元数据的列顺序与期望一致」这件事从未被验证。索引列顺序错了
// 覆盖索引就不成立（查询照样能跑，只是回表），属于静默性能劣化。
func TestMariaDBBillingIndexesMigrationRunsForReal(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.SystemSetting{}, &entity.BillingRecord{})

	if err := ensureBillingRecordIndexes(database); err != nil {
		t.Fatalf("真实 MariaDB 上跑账单索引迁移失败：%v", err)
	}

	cases := []struct {
		index   string
		columns []string
		unique  bool
	}{
		{index: "idx_billing_records_dedup", columns: []string{"user_id", "request_id", "type", "hidden_from_statement", "id"}},
		{index: "idx_billing_records_aggregation", columns: []string{"type", "request_id", "user_id", "hidden_from_statement"}},
		{index: "idx_billing_records_usage", columns: []string{"type", "user_id", "request_id", "hidden_from_statement", "provider", "input_tokens", "cached_input_tokens", "amount"}},
	}
	for _, testCase := range cases {
		got := mariaDBIndexColumns(t, database, entity.TableNameBillingRecord, testCase.index)
		if strings.Join(got, ",") != strings.Join(testCase.columns, ",") {
			t.Fatalf("%s 的真实列顺序 = %v, want %v", testCase.index, got, testCase.columns)
		}
		if unique := mariaDBIndexIsUnique(t, database, entity.TableNameBillingRecord, testCase.index); unique != testCase.unique {
			t.Fatalf("%s 唯一性 = %v, want %v", testCase.index, unique, testCase.unique)
		}
	}
	if !systemSettingFlagIsSet(database, billingRecordIndexesMigrationKey) {
		t.Fatalf("账单索引迁移标记未写入")
	}
}

// TestMariaDBBillingIndexesMigrationIsIdempotent 真库上重跑只读元数据、不发 DDL。
//
// 判据是「索引元数据前后逐字节一致」，而不是「没报错」：
// drop-then-create 也能「没报错」，但那会在每次启动时重建索引，
// 大表上等于每次启动都锁表一次。
func TestMariaDBBillingIndexesMigrationIsIdempotent(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.SystemSetting{}, &entity.BillingRecord{})
	if err := ensureBillingRecordIndexes(database); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	before := describeMariaDBTable(t, database, entity.TableNameBillingRecord)
	for attempt := 2; attempt <= 3; attempt++ {
		if err := ensureBillingRecordIndexes(database); err != nil {
			t.Fatalf("第 %d 次迁移失败：%v", attempt, err)
		}
	}
	if after := describeMariaDBTable(t, database, entity.TableNameBillingRecord); after != before {
		t.Fatalf("重跑后索引元数据变了（说明走了 drop/create 而不是复用）：\nbefore=%s\nafter=%s", before, after)
	}
}

// TestMariaDBBillingIndexesRebuildsMismatchedIndex 真库上重建列不符的索引。
//
// 这是假驱动唯一能「假装」但无法「验证」的分支：它靠手写返回值判断列，
// 而这里用真实的 CREATE INDEX 造出一个列不符的索引，看迁移是否真的：
//
//	① 读出真实列顺序 → ② DropIndex 成功 → ③ 重新 CREATE INDEX 成功。
func TestMariaDBBillingIndexesRebuildsMismatchedIndex(t *testing.T) {
	database := mysqlTestFreshDB(t)
	mustAutoMigrate(t, database, &entity.SystemSetting{}, &entity.BillingRecord{})

	// 先建一个缩写版索引（列不对，但名字是目标名字）。
	mustExec(t, database, "CREATE INDEX `idx_billing_records_usage` ON `billing_records` (`type`)")
	if got := mariaDBIndexColumns(t, database, entity.TableNameBillingRecord, "idx_billing_records_usage"); strings.Join(got, ",") != "type" {
		t.Fatalf("前置条件不成立：缩写索引列 = %v", got)
	}

	if err := ensureBillingRecordIndexes(database); err != nil {
		t.Fatalf("重建索引失败（MySQL 上 drop+create 是这条路径唯一可行的实现）：%v", err)
	}
	want := []string{"type", "user_id", "request_id", "hidden_from_statement", "provider", "input_tokens", "cached_input_tokens", "amount"}
	if got := mariaDBIndexColumns(t, database, entity.TableNameBillingRecord, "idx_billing_records_usage"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("列不符的索引没有被重建为完整覆盖索引，实际 = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func mustExec(t *testing.T, database *gorm.DB, statement string) {
	t.Helper()
	if err := database.Exec(statement).Error; err != nil {
		t.Fatalf("执行 SQL 失败：%v\nSQL: %s", err, statement)
	}
}

// isMariaDBDuplicateError 判定错误是否真的是唯一键冲突。
//
// 不能只看 err != nil：连接断开、语法错误、严格模式拒绝都会返回错误，
// 那些情况会伪装成「唯一索引生效」，让用例变成永远绿的假钉子。
func isMariaDBDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"error 1062", "duplicate entry", "duplicate key name", "already exists"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// mariaDBSeedSummary 仅用于日志：把种子表的行数写成一行可核对的事实。
func mariaDBSeedSummary(t *testing.T, database *gorm.DB, tables ...string) string {
	t.Helper()
	parts := make([]string, 0, len(tables))
	for _, table := range tables {
		var count int64
		if err := database.Raw("SELECT COUNT(*) AS count FROM " + mysqlTestQuoteIdentifier(table)).Scan(&count).Error; err != nil {
			t.Fatalf("统计 %s 行数失败：%v", table, err)
		}
		parts = append(parts, fmt.Sprintf("%s=%d", table, count))
	}
	return strings.Join(parts, " ")
}
