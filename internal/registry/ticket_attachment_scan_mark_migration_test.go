package registry

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// ===================== scan_marked_by 列迁移的钉子 =====================
//
// 这一列的存在理由只有一条：额度耗尽时写下的 scan_state="sensitive" 与上游
// 真判违规**同值**，管理员无法分辨「该封」与「该补额度后重检」。因此本列
// 必须满足三个硬约束，每个都是独立用例：
//
//  1. 物理列是 NOT NULL DEFAULT 'upstream'（旧代码 INSERT 不带它不能失败）；
//  2. 存量行加上列后读出的是安全值，不 panic、不是 NULL；
//  3. 迁移是列检测型：幂等、不建表、不碰其它列。
//
// ⚠️ 所有期望值一律写**字面量**（"upstream" / varchar(32)），不引用
// entity.TicketAttachmentMarkedByUpstream：常量一旦漂移，用常量拼的期望值会
// 跟着漂移 → 断言恒真 → 假钉子（第 4 类假钉子形态）。

func newScanMarkTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:scan-mark-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// ticketAttachmentScanMarkColumnInfo 是 PRAGMA table_info 的一行。
//
// 每个字段都写显式 column tag：gorm 默认把字段名 `NotNull` 映射成 not_null，
// 而 PRAGMA 出的列名是 notnull。漏了 tag 时 Scan 不报错、只落零值 ——
// 断言「notnull=1」会变成一个永远失败的假红，容易被误判成「没写 NOT NULL」。
type ticketAttachmentScanMarkColumnInfo struct {
	Name       string  `gorm:"column:name"`
	Type       string  `gorm:"column:type"`
	NotNull    int     `gorm:"column:notnull"`
	DefaultVal *string `gorm:"column:dflt_value"`
}

// readScanMarkColumn 读 ticket_attachments.scan_marked_by 的物理定义；不存在返回 nil。
//
// 用 PRAGMA 而不是 HasColumn：本条要断言的是**列约束**（NOT NULL / DEFAULT），
// HasColumn 只能回答「在不在」，证不出「有默认值」这条硬要求。
func readScanMarkColumn(t *testing.T, db *gorm.DB) *ticketAttachmentScanMarkColumnInfo {
	t.Helper()
	var columns []ticketAttachmentScanMarkColumnInfo
	if err := db.Raw("PRAGMA table_info(" + entity.TableNameTicketAttachment + ")").Scan(&columns).Error; err != nil {
		t.Fatalf("pragma table_info(%s): %v", entity.TableNameTicketAttachment, err)
	}
	for i := range columns {
		if columns[i].Name == "scan_marked_by" {
			return &columns[i]
		}
	}
	return nil
}

// createLegacyTicketAttachmentTable 造一张**加列之前**的 ticket_attachments。
//
// 为什么用裸 DDL 而不是 AutoMigrate：entity.TicketAttachment 现在已经有
// ScanMarkedBy 字段，AutoMigrate 会顺手把列建出来 —— 那就永远测不到
// 「存量库缺列 → 迁移补列」这条真路径（这是本迁移唯一存在的理由）。
func createLegacyTicketAttachmentTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	statement := "CREATE TABLE `" + entity.TableNameTicketAttachment + "` (" +
		"`id` integer PRIMARY KEY AUTOINCREMENT," +
		"`ticket_id` integer NOT NULL," +
		"`message_id` integer," +
		"`user_id` integer NOT NULL," +
		"`media_type` varchar(32) NOT NULL," +
		"`byte_size` integer NOT NULL," +
		"`sha256` varchar(64) NOT NULL," +
		"`nonce` blob," +
		"`ciphertext` blob," +
		"`scan_state` varchar(16) NOT NULL," +
		"`nsfw_score` real," +
		"`risk_level` varchar(16)," +
		"`suggestion` varchar(16)," +
		"`label` varchar(32)," +
		"`check_error` text," +
		"`checked_at` datetime," +
		"`attempt_count` integer NOT NULL DEFAULT 0," +
		"`next_attempt_at` datetime," +
		"`blob_sha256` char(64) NOT NULL DEFAULT ''," +
		"`created_at` datetime," +
		"`updated_at` datetime" +
		")"
	if err := db.Exec(statement).Error; err != nil {
		t.Fatalf("create legacy ticket_attachments: %v", err)
	}
}

// 新库：AutoMigrate 建出的列必须是 varchar(32) / NOT NULL / DEFAULT 'upstream'。
func TestTicketAttachmentScanMarkedByColumnHasSafeNotNullDefault(t *testing.T) {
	db := newScanMarkTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachment{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	column := readScanMarkColumn(t, db)
	if column == nil {
		t.Fatal("ticket_attachments.scan_marked_by must exist after AutoMigrate")
	}
	if !strings.EqualFold(column.Type, "varchar(32)") {
		t.Fatalf("scan_marked_by type = %q, want varchar(32)", column.Type)
	}
	if column.NotNull != 1 {
		t.Fatalf("scan_marked_by must be NOT NULL (got notnull=%d)", column.NotNull)
	}
	// 字面量 "upstream"：迁移前写入的存量行若已是 sensitive，只能按「上游判定」对待；
	// 反过来默认成 quota 等于把历史真违规一律降级成「未检测」（证据丢失）。
	if column.DefaultVal == nil {
		t.Fatal("scan_marked_by must carry a DEFAULT (old code INSERTs omit this column)")
	}
	if got := strings.Trim(*column.DefaultVal, "'\""); got != "upstream" {
		t.Fatalf("scan_marked_by default = %q, want \"upstream\"", got)
	}
}

// 存量库：已有 sensitive 行在补列后读出安全值，且旧式 INSERT 仍能成功。
//
// 这是「NOT NULL DEFAULT」的判别性用例 —— 把默认值去掉后存量行会变 NULL、
// 旧式 INSERT 直接失败，本用例立刻变红。
func TestTicketAttachmentScanMarkedByBackfillsLegacyRowsWithoutPanic(t *testing.T) {
	db := newScanMarkTestDB(t)
	createLegacyTicketAttachmentTable(t, db)
	if readScanMarkColumn(t, db) != nil {
		t.Fatal("test setup invalid: legacy table must not have scan_marked_by yet")
	}
	// 加列之前的存量行：scan_state 已经是 sensitive（这是迁移前最常见的历史形态）。
	if err := db.Exec("INSERT INTO `" + entity.TableNameTicketAttachment + "` " +
		"(ticket_id, user_id, media_type, byte_size, sha256, scan_state, created_at, updated_at) " +
		"VALUES (5, 7, 'image/png', 128, '" + strings.Repeat("a", 64) + "', 'sensitive', " +
		"'2026-01-02 03:04:05', '2026-01-02 03:04:05')").Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	if err := EnsureTicketAttachmentScanMarkedBySchema(db); err != nil {
		t.Fatalf("ensure scan marked by schema: %v", err)
	}
	if column := readScanMarkColumn(t, db); column == nil {
		t.Fatal("scan_marked_by must be added to the legacy table")
	}

	// 存量行读出来必须是安全值（不是 NULL、不是空串）。
	var marks []string
	if err := db.Table(entity.TableNameTicketAttachment).Select("scan_marked_by").Scan(&marks).Error; err != nil {
		t.Fatalf("read legacy scan_marked_by: %v", err)
	}
	if len(marks) != 1 || marks[0] != "upstream" {
		t.Fatalf("legacy row scan_marked_by = %v, want [\"upstream\"]", marks)
	}

	// 旧代码继续按原样 INSERT（不带这一列）也必须成功并落默认值。
	if err := db.Exec("INSERT INTO `" + entity.TableNameTicketAttachment + "` " +
		"(ticket_id, user_id, media_type, byte_size, sha256, scan_state) " +
		"VALUES (6, 7, 'image/png', 64, '" + strings.Repeat("b", 64) + "', 'optimistic')").Error; err != nil {
		t.Fatalf("insert without scan_marked_by must still succeed: %v", err)
	}
	var newest string
	if err := db.Table(entity.TableNameTicketAttachment).
		Where("sha256 = ?", strings.Repeat("b", 64)).
		Select("scan_marked_by").Scan(&newest).Error; err != nil {
		t.Fatalf("read new row: %v", err)
	}
	if newest != "upstream" {
		t.Fatalf("row inserted without scan_marked_by = %q, want \"upstream\"", newest)
	}

	// 实体读取路径也不能 panic，且能读回真实值。
	var row entity.TicketAttachment
	if err := db.Where("sha256 = ?", strings.Repeat("a", 64)).First(&row).Error; err != nil {
		t.Fatalf("gorm read of legacy row: %v", err)
	}
	if row.ScanMarkedBy != "upstream" {
		t.Fatalf("entity ScanMarkedBy = %q, want \"upstream\"", row.ScanMarkedBy)
	}
}

// 列检测型：三次执行幂等、且只多出一列（不碰其它列）。
func TestTicketAttachmentScanMarkedBySchemaIsIdempotentAndAddsOneColumn(t *testing.T) {
	db := newScanMarkTestDB(t)
	createLegacyTicketAttachmentTable(t, db)

	before := 0
	var legacyColumns []ticketAttachmentScanMarkColumnInfo
	if err := db.Raw("PRAGMA table_info(" + entity.TableNameTicketAttachment + ")").Scan(&legacyColumns).Error; err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	before = len(legacyColumns)

	for i := 0; i < 3; i++ {
		if err := EnsureTicketAttachmentScanMarkedBySchema(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	var columns []ticketAttachmentScanMarkColumnInfo
	if err := db.Raw("PRAGMA table_info(" + entity.TableNameTicketAttachment + ")").Scan(&columns).Error; err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	if len(columns) != before+1 {
		t.Fatalf("column count = %d, want %d (exactly one added)", len(columns), before+1)
	}
	count := 0
	for _, column := range columns {
		if column.Name == "scan_marked_by" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("has %d scan_marked_by columns, want exactly 1", count)
	}
}

// 空库首启：表不存在时不得报错、不得凭空建表；表后来出现则必须补上列。
func TestTicketAttachmentScanMarkedBySchemaSkipsMissingTableButRecovers(t *testing.T) {
	db := newScanMarkTestDB(t)

	if err := EnsureTicketAttachmentScanMarkedBySchema(db); err != nil {
		t.Fatalf("migration without the table must not fail: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameTicketAttachment) {
		t.Fatal("migration must not create the table")
	}
	if err := EnsureTicketAttachmentScanMarkedBySchema(nil); err != nil {
		t.Fatalf("nil db must be a no-op: %v", err)
	}

	createLegacyTicketAttachmentTable(t, db)
	if err := EnsureTicketAttachmentScanMarkedBySchema(db); err != nil {
		t.Fatalf("migration after the table appeared: %v", err)
	}
	if readScanMarkColumn(t, db) == nil {
		t.Fatal("scan_marked_by must be added on the later run")
	}
}

// MySQL 分支必须把列建成 varchar(32) NOT NULL DEFAULT 'upstream'。
//
// 断言点选在 gorm 真正拼进 `ALTER TABLE ? ADD ? ?` 的列定义（FullDataTypeOf），
// 而不是截 DryRun 的 SQL 串：DryRun 下 RawExec 直接 return，
// 既没 Trace 也没回调，截不到语句只能证明「测试没跑起来」。
//
// 这条用例的意义：SQLite 分支是手写裸 DDL，MySQL 分支靠实体 tag ——
// 两者必须一致，否则只在 MySQL 上丢掉默认值（121 决策 5 警告的故障）。
func TestTicketAttachmentScanMarkedByMySQLDefinitionKeepsNotNullDefault(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:password@tcp(127.0.0.1:3306)/test",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run mysql: %v", err)
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&entity.TicketAttachment{}); err != nil {
		t.Fatalf("parse TicketAttachment: %v", err)
	}
	if stmt.Table != entity.TableNameTicketAttachment {
		t.Fatalf("entity resolved to table %q, want %q", stmt.Table, entity.TableNameTicketAttachment)
	}
	field := stmt.Schema.LookUpField("ScanMarkedBy")
	if field == nil {
		t.Fatal("entity.TicketAttachment has no ScanMarkedBy field (MySQL AddColumn would fail)")
	}
	if field.DBName != "scan_marked_by" {
		t.Fatalf("field DBName = %q, want scan_marked_by", field.DBName)
	}
	definition := strings.ToLower(db.Migrator().FullDataTypeOf(field).SQL)
	if !strings.Contains(definition, "varchar(32)") {
		t.Fatalf("column type must be varchar(32), got %q", definition)
	}
	if !strings.Contains(definition, "not null") {
		t.Fatalf("column must be NOT NULL, got %q", definition)
	}
	// 期望值写死字面量 "default 'upstream'"：同时钉「字面值」与「带引号」两件事。
	// gorm 对 `default:upstream` 走 reflect.String 分支并拼 `DEFAULT 'upstream'`，
	// 用常量拼期望串会同时失去这两条判别力。
	if !strings.Contains(definition, "default 'upstream'") {
		t.Fatalf("column must default to 'upstream' (quoted literal), got %q", definition)
	}
}

// 常量不得为别的值（两处字面量必须真能区分，且都不是空串）。
func TestTicketAttachmentScanMarkedByConstantsAreDistinct(t *testing.T) {
	if entity.TicketAttachmentMarkedByUpstream != "upstream" {
		t.Fatalf("upstream constant = %q, want upstream", entity.TicketAttachmentMarkedByUpstream)
	}
	if entity.TicketAttachmentMarkedByQuota != "quota" {
		t.Fatalf("quota constant = %q, want quota", entity.TicketAttachmentMarkedByQuota)
	}
	if entity.TicketAttachmentMarkedByUpstream == entity.TicketAttachmentMarkedByQuota {
		t.Fatal("the two marks must differ, otherwise they cannot distinguish anything")
	}
}
