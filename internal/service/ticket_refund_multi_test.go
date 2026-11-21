package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 多选提交：一笔订单一张退款单，且必须「全成或全败」。
//
// 2026-11-18 用户要求「退款订单选择框允许多选」。
// 关键数据约束：ticket_refunds.order_id 上有 uniqueIndex —— 一个订单只能被
// 一张退款单引用（防重复退款）。所以多选在服务端必然展开成多张退款单，
// 而不是一张单挂多个订单。

func TestCreateRefundsForMultipleOrders(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	// 两笔订单必须用**不同时刻**：seedTicketRefundOrder 用 paid_at 生成 order_no，
	// 同一时刻会撞 recharge_orders.order_no 的唯一约束（与本次改动无关的环境细节）。
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, time.Now().Add(-2*time.Hour), 50, true)
	seedTicketRefundOrder(t, db, 2, owner.ID, rechargeOrderStatusPaid, time.Now().Add(-3*time.Hour), 80, true)

	rows, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind:           entity.TicketRefundKindOriginal,
		OrderIDs:       []int32{1, 2},
		RequestedCents: 5000,
		Reason:         "多笔误充",
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("create multi refunds: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("两笔订单必须生成两张退款单，want 2 got %d", len(rows))
	}

	// 每张单必须各挂一个不同订单（uniqueIndex 的必然结果）。
	seen := map[int32]bool{}
	for _, row := range rows {
		if row.OrderID == nil {
			t.Fatalf("退款单必须挂到订单上，实际 %+v", row)
		}
		if seen[*row.OrderID] {
			t.Fatalf("订单 %d 被两张退款单引用（违反 order_id 唯一约束）", *row.OrderID)
		}
		seen[*row.OrderID] = true
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("订单 1 与 2 都必须各有一张退款单，实际 %v", seen)
	}
}

// 任一订单不可退时整批回滚 —— 部分成功会让用户以为全退了。
func TestCreateRefundsRollsBackWhenAnyOrderInvalid(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)
	// 订单 2 不存在 → 应让整批失败
	_, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind:           entity.TicketRefundKindOriginal,
		OrderIDs:       []int32{1, 999},
		RequestedCents: 5000,
		Reason:         "混合无效",
	}, testVerifyTicketRefundChannel)
	if err == nil {
		t.Fatal("含无效订单时必须整批失败，实际成功了")
	}

	// 关键：订单 1 不能留下退款单（否则就是「部分成功」）。
	var count int64
	if qerr := db.Model(&entity.TicketRefund{}).Count(&count).Error; qerr != nil {
		t.Fatalf("count: %v", qerr)
	}
	if count != 0 {
		t.Fatalf("整批回滚后不应有任何退款单，实际 %d 条", count)
	}
}

// 重复的 orderId 要去重，不能因为用户多选重复而整批撞唯一索引失败。
func TestCreateRefundsDeduplicatesOrderIDs(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)

	rows, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind:           entity.TicketRefundKindOriginal,
		OrderIDs:       []int32{1, 1, 1},
		RequestedCents: 5000,
		Reason:         "重复选择",
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("重复 orderId 应被去重而不是报错，实际 %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("去重后只应有一张退款单，want 1 got %d", len(rows))
	}
}

// 空列表直接拒绝。
func TestCreateRefundsRejectsEmptyOrderIDs(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	if _, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal, OrderIDs: nil, RequestedCents: 100, Reason: "x",
	}, testVerifyTicketRefundChannel); err == nil {
		t.Fatal("空 orderIds 必须被拒绝")
	}
}
