package service

import (
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 收款码附件的回归钉子（2026-11-19 用户要求：
// 「即便可以原路退回也可以要求用户设置退款账号。即添加一个带记忆的图片上传框，
// 用于上传收款码，只能上传一张」）。
//
// 钉两件事：
//   · 收款码 ID 必须真的落到 ticket_refunds 上（否则管理员拿不到它）；
//   · 它必须被认领到壳工单的消息上（否则工单详情里看不到）。

func seedPayoutQrDraft(t *testing.T, db *gorm.DB, ownerID int32) entity.TicketAttachment {
	t.Helper()
	// newTicketRefundCoreTestDB 只迁退款相关表；附件表在这里单独补，
	// 避免为了一个测试去改动共享辅助函数（那会影响其它用例的迁移集合）。
	// 壳工单表也要迁：CreateTicketRefundShellInTx 在缺 tickets/ticket_messages
	// 时**有意跳过**建壳工单（退款是钱的动作，不能被展示层依赖否决）。
	// 那条早退会让本用例变成「什么都没绑也通过」，所以这里必须把表补齐，
	// 让绑定路径真的被执行到。
	if err := db.AutoMigrate(&entity.TicketAttachment{}, &entity.Ticket{}, &entity.TicketMessage{}); err != nil {
		t.Fatalf("migrate attachments/tickets: %v", err)
	}
	row := entity.TicketAttachment{
		UserID: ownerID, TicketID: 0, MessageID: nil,
		MediaType: "image/png",
		ByteSize:  128, SHA256: strings.Repeat("b", 64),
		ScanState: entity.TicketAttachmentScanPending,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed payout qr: %v", err)
	}
	return row
}

// 收款码 ID 必须落库，并认领到壳工单的消息上。
func TestCreateRefundPersistsAndBindsPayoutQr(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)
	qr := seedPayoutQrDraft(t, db, owner.ID)

	refund, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind:                 entity.TicketRefundKindOriginal,
		OrderID:              payoutPtrInt32(1),
		RequestedCents:       5000,
		Reason:               "带收款码",
		PayoutQrAttachmentID: &qr.ID,
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if refund.PayoutQrAttachmentID == nil || *refund.PayoutQrAttachmentID != qr.ID {
		t.Fatalf("收款码 ID 必须落在退款行上，实际 %v", refund.PayoutQrAttachmentID)
	}

	// 认领判据：附件的 ticket_id 必须从 0 变成壳工单 ID。
	var bound entity.TicketAttachment
	if err := db.Where("id = ?", qr.ID).Take(&bound).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if bound.TicketID == 0 {
		t.Fatal("收款码必须被认领到壳工单上，否则工单详情里看不到它")
	}
	if refund.TicketID == nil || bound.TicketID != *refund.TicketID {
		t.Fatalf("收款码应挂在壳工单 %v 上，实际 %d", refund.TicketID, bound.TicketID)
	}
}

// 不给收款码时也要能正常退款（它是可选项）。
func TestCreateRefundWithoutPayoutQrStillWorks(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)

	refund, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal, OrderID: payoutPtrInt32(1),
		RequestedCents: 5000, Reason: "不带收款码",
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("不带收款码必须能退款: %v", err)
	}
	if refund.PayoutQrAttachmentID != nil {
		t.Fatalf("没给收款码时该字段应为空，实际 %v", *refund.PayoutQrAttachmentID)
	}
}

// 非法 ID（<=0）必须被拒，而不是落一个指向不存在附件的裸 ID。
func TestCreateRefundRejectsNonPositivePayoutQr(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)

	bad := int64(0)
	if _, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal, OrderID: payoutPtrInt32(1),
		RequestedCents: 5000, Reason: "非法二维码", PayoutQrAttachmentID: &bad,
	}, testVerifyTicketRefundChannel); err == nil {
		t.Fatal("收款码 ID 为 0 必须被拒")
	}
}

func payoutPtrInt32(v int32) *int32 { return &v }
