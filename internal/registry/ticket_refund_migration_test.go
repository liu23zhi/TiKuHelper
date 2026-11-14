package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 退款壳工单迁移（T-209）的测试。
//
// 判据全部围绕「存量退款能不能出现在工单列表里」——这是用户诉求
// 「退款工单也显示在工单列表」的数据层保证。

// legacyTicketRefund 是「加 ticket_id 列之前」的 ticket_refunds 表结构快照。
//
// 与 legacyTicket 同一条纪律：用老结构建表才叫真实模拟老库，
// 用 entity.TicketRefund 建表等于自证（列本来就存在）。
type legacyTicketRefund struct {
	ID             int64      `gorm:"primaryKey;autoIncrement"`
	UserID         int32      `gorm:"column:user_id;not null;index"`
	OrderID        *int32     `gorm:"column:order_id"`
	Kind           string     `gorm:"column:kind;type:varchar(16);not null"`
	Status         string     `gorm:"column:status;type:varchar(24);not null;index"`
	RequestedCents int64      `gorm:"column:requested_cents;not null"`
	ApprovedCents  int64      `gorm:"column:approved_cents;not null;default:0"`
	Reason         string     `gorm:"column:reason;type:text"`
	RefundNo       string     `gorm:"column:refund_no;type:varchar(64);not null"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
	PaidAt         *time.Time `gorm:"column:paid_at"`
}

func (*legacyTicketRefund) TableName() string { return "ticket_refunds" }

// newTicketRefundMigrationDB 建迁移测试库（老退款表 + 新工单表 + 设置表）。
func newTicketRefundMigrationDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.Ticket{}, &entity.TicketMessage{},
		&entity.User{}, &legacyTicketRefund{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// 结构 + 回填：存量退款被补出壳工单，并写回 ticket_id。
//
// 这是「历史退款也要出现在列表里」的唯一判据。变异方向：删掉回填那一步
// （只补列不回填）→ 本用例必红（存量退款的 ticket_id 仍为 NULL、列表里没有它）。
func TestTicketRefundMigrationBackfillsLegacyRefundsIntoTickets(t *testing.T) {
	db := newTicketRefundMigrationDB(t)

	// 两笔历史退款（ticket_id 列还不存在，所以它们天然是「未关联」）。
	legacy := []legacyTicketRefund{
		{UserID: 11, Kind: entity.TicketRefundKindManual, Status: entity.TicketRefundStatusPendingReview,
			RequestedCents: 800, Reason: "历史退款一", RefundNo: "tr-old-1",
			CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{UserID: 12, Kind: entity.TicketRefundKindManual, Status: entity.TicketRefundStatusSucceeded,
			RequestedCents: 1500, Reason: "历史退款二", RefundNo: "tr-old-2",
			CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	for index := range legacy {
		if err := db.Create(&legacy[index]).Error; err != nil {
			t.Fatalf("seed legacy refund: %v", err)
		}
	}

	// 跑迁移。
	if err := ensureTicketRefundTicketSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// ① 列存在。
	if !db.Migrator().HasColumn("ticket_refunds", "ticket_id") {
		t.Fatal("迁移必须补上 ticket_refunds.ticket_id 列")
	}

	// ② 每条退款都有了壳工单。
	var rows []entity.TicketRefund
	if err := db.Where("ticket_id IS NOT NULL").Find(&rows).Error; err != nil {
		t.Fatalf("read refunds: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("两笔历史退款都应被关联到壳工单，实际关联 %d 笔", len(rows))
	}

	// ③ 壳工单本身必须可读、归属正确、分类为 refund。
	for _, refund := range rows {
		var ticket entity.Ticket
		if err := db.Where("id = ?", *refund.TicketID).Take(&ticket).Error; err != nil {
			t.Fatalf("壳工单不存在（ticket_id=%d）：%v", *refund.TicketID, err)
		}
		if ticket.Category != entity.TicketCategoryRefund {
			t.Fatalf("壳工单 category 必须是 refund，got %q", ticket.Category)
		}
		if ticket.UserID != refund.UserID {
			t.Fatalf("壳工单必须归属退款申请人：want %d got %d", refund.UserID, ticket.UserID)
		}
		if ticket.Origin != entity.TicketOriginUser {
			t.Fatalf("壳工单 origin 必须是 user，got %q", ticket.Origin)
		}
		// 每张壳工单都要有一条说明来源的系统消息（否则详情页是空的）。
		var messageCount int64
		if err := db.Model(&entity.TicketMessage{}).Where("ticket_id = ?", ticket.ID).
			Count(&messageCount).Error; err != nil {
			t.Fatal(err)
		}
		if messageCount != 1 {
			t.Fatalf("壳工单应有 1 条系统消息，got %d", messageCount)
		}
	}

	// ④ 状态映射：pending_review → open，succeeded → closed。
	for _, refund := range rows {
		var ticket entity.Ticket
		if err := db.Where("id = ?", *refund.TicketID).Take(&ticket).Error; err != nil {
			t.Fatal(err)
		}
		want := entity.TicketStatusOpen
		if refund.Status == entity.TicketRefundStatusSucceeded {
			want = entity.TicketStatusClosed
		}
		if ticket.Status != want {
			t.Fatalf("退款 %s 的壳工单状态应为 %s，got %s", refund.Status, want, ticket.Status)
		}
	}
}

// 幂等：迁移跑两次不能造出第二套壳工单。
func TestTicketRefundMigrationIsIdempotent(t *testing.T) {
	db := newTicketRefundMigrationDB(t)
	legacy := legacyTicketRefund{
		UserID: 21, Kind: entity.TicketRefundKindManual, Status: entity.TicketRefundStatusPendingReview,
		RequestedCents: 500, Reason: "幂等测试", RefundNo: "tr-old-3",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ensureTicketRefundTicketSchema(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// 清掉回填标记，强制第二轮重跑回填（验证「即使标记丢了也幂等」）。
	if err := db.Where("`key` = ?", ticketRefundBackfilledKey).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if err := ensureTicketRefundTicketSchema(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var ticketCount int64
	if err := db.Model(&entity.Ticket{}).Where("category = ?", entity.TicketCategoryRefund).
		Count(&ticketCount).Error; err != nil {
		t.Fatal(err)
	}
	if ticketCount != 1 {
		t.Fatalf("迁移必须幂等，重复跑不应造出第 %d 张壳工单", ticketCount)
	}
}

// 空库/表不存在时不报错（迁移顺序由 db.go 保证，但要能安全早退）。
func TestTicketRefundMigrationSkipsMissingTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// 只建设置表，不建 tickets / ticket_refunds。
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTicketRefundTicketSchema(db); err != nil {
		t.Fatalf("缺表时必须安全早退，got %v", err)
	}
	if err := ensureTicketRefundTicketSchema(nil); err != nil {
		t.Fatalf("nil db 必须安全早退，got %v", err)
	}
}
