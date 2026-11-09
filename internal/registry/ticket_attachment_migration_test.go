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

// TestEnsureTicketAttachmentSchemaPreservesExistingRows 断言迁移不会破坏既有工单数据。
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
