package registry

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newTicketAttachmentMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	return db
}

// TestEnsureTicketAttachmentSchemaIsIdempotentAndMarked 验证迁移三条纪律：
// 幂等、写一次性标记、不建任何 DROP。
func TestEnsureTicketAttachmentSchemaIsIdempotentAndMarked(t *testing.T) {
	db := newTicketAttachmentMigrationDB(t)
	if err := ensureTicketAttachmentSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameTicketAttachment) {
		t.Fatal("ticket_attachments must be created")
	}
	if !systemSettingFlagIsSet(db, ticketAttachmentSchemaReadyKey) {
		t.Fatal("migration marker must be written")
	}
	// 需要的关键列都在（缺列会静默变成「永远 pending」）。
	for _, column := range []string{
		"ticket_id", "message_id", "user_id", "media_type", "byte_size", "sha256",
		"nonce", "ciphertext", "scan_state", "nsfw_score", "risk_level", "suggestion",
		"label", "check_error", "checked_at", "attempt_count", "next_attempt_at",
		"created_at", "updated_at",
	} {
		if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, column) {
			t.Fatalf("missing column %q", column)
		}
	}
	// 第二次运行必须成功且不改变结构（幂等）。
	if err := ensureTicketAttachmentSchema(db); err != nil {
		t.Fatalf("second run must be a no-op, got %v", err)
	}
	// 标记只应有一行（重复写入要幂等）。
	var count int64
	if err := db.Model(&entity.SystemSetting{}).Where(map[string]any{"key": ticketAttachmentSchemaReadyKey}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one marker row, got %d", count)
	}
}

// TestEnsureTicketAttachmentRetentionSchemaAddsPurgedAt 验证保留策略的加列迁移。
//
// 三条断言，各自对应一个真会坏掉的实现点：
//   - 列真的被补上（缺列 = 清理任务每轮静默跳过，功能上线却一张图都不清）；
//   - 标记真的被写（缺标记会每次启动重跑，虽然幂等但会刷日志）；
//   - 对**已有行**加列不报错（老库 ALTER 的典型失败点：NOT NULL 无默认值）。
func TestEnsureTicketAttachmentRetentionSchemaAddsPurgedAt(t *testing.T) {
	db := newTicketAttachmentMigrationDB(t)
	// 先建出「老库」形态的附件表：用实体直接 AutoMigrate 会带上 purged_at，
	// 所以这里先建表再删列，模拟「本迁移之前就已存在的库」。
	if err := db.AutoMigrate(&entity.TicketAttachment{}); err != nil {
		t.Fatalf("migrate attachment: %v", err)
	}
	if err := db.Exec("ALTER TABLE " + entity.TableNameTicketAttachment + " DROP COLUMN `purged_at`").Error; err != nil {
		t.Skipf("当前 SQLite 不支持 DROP COLUMN，跳过老库形态用例: %v", err)
	}
	if db.Migrator().HasColumn(entity.TableNameTicketAttachment, "purged_at") {
		t.Fatal("前提失败：purged_at 应当已不存在")
	}
	// 造一行既有数据：给已有行加列时若有 NOT NULL 而无默认值，ALTER 会失败。
	if err := db.Exec("INSERT INTO " + entity.TableNameTicketAttachment +
		" (ticket_id, user_id, media_type, byte_size, sha256, scan_state, attempt_count, created_at, updated_at)" +
		" VALUES (1, 1, 'image/png', 100, 'deadbeef', 'approved', 0, datetime('now'), datetime('now'))").Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	if err := EnsureTicketAttachmentRetentionSchema(db); err != nil {
		t.Fatalf("migration must succeed on a table with existing rows: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameTicketAttachment, "purged_at") {
		t.Fatal("purged_at 列必须被补上")
	}
	if !systemSettingFlagIsSet(db, ticketAttachmentRetentionColumnReadyKey) {
		t.Fatal("迁移标记必须被写入")
	}
	// 既有行的 purged_at 必须是 NULL（三态语义里「未清理」的权威初值，
	// 用零值/空串当哨兵会和「残缺行」混在一起）。
	var purged int64
	if err := db.Model(&entity.TicketAttachment{}).Where("purged_at IS NOT NULL").Count(&purged).Error; err != nil {
		t.Fatalf("count purged: %v", err)
	}
	if purged != 0 {
		t.Fatalf("存量行不得被误标为已清理，got %d 行", purged)
	}
	// 幂等：再跑一次不报错、标记仍只有一行。
	if err := EnsureTicketAttachmentRetentionSchema(db); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
	var markers int64
	if err := db.Model(&entity.SystemSetting{}).
		Where(map[string]any{"key": ticketAttachmentRetentionColumnReadyKey}).Count(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if markers != 1 {
		t.Fatalf("标记只应有一行，got %d", markers)
	}
}

// TestEnsureTicketAttachmentRetentionSchemaSkipsMissingTable 断言附件表还不存在时
// 迁移什么都不做也不报错（空库首次启动的正常路径）。
func TestEnsureTicketAttachmentRetentionSchemaSkipsMissingTable(t *testing.T) {
	db := newTicketAttachmentMigrationDB(t)
	if err := EnsureTicketAttachmentRetentionSchema(db); err != nil {
		t.Fatalf("missing table must be a silent no-op: %v", err)
	}
	if err := EnsureTicketAttachmentRetentionSchema(nil); err != nil {
		t.Fatalf("nil db must be a silent no-op: %v", err)
	}
}

func TestEnsureTicketAttachmentSchemaPreservesExistingRows(t *testing.T) {
	db := newTicketAttachmentMigrationDB(t)
	if err := db.AutoMigrate(&entity.Ticket{}, &entity.TicketMessage{}); err != nil {
		t.Fatal(err)
	}
	ticket := entity.Ticket{UserID: 1, UserName: "alice", Subject: "旧工单", Status: entity.TicketStatusOpen, Priority: entity.TicketPriorityNormal}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.TicketMessage{TicketID: ticket.ID, UserID: 1, AuthorName: "alice", Body: "历史内容", AuthorRole: "user"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureTicketAttachmentSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	var reloaded entity.Ticket
	if err := db.Where("id = ?", ticket.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("old ticket must survive: %v", err)
	}
	if reloaded.Subject != "旧工单" {
		t.Fatalf("old ticket mutated: %+v", reloaded)
	}
	var messages int64
	if err := db.Model(&entity.TicketMessage{}).Where("ticket_id = ?", ticket.ID).Count(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if messages != 1 {
		t.Fatalf("old message must survive, got %d", messages)
	}
}
