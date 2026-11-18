package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 运营改状态的回归钉子（2026-11-19 用户要求「充值记录添加状态修改功能」）。
//
// 最重要的不是「能改成功」，而是**改状态绝不能碰钱包**：
// 状态是运营字段，入账只能由 payment_events 的幂等回调或退款流程完成。
// 一旦这里顺带加款，同一笔充值就会被记两次。

func seedStatusTestOrder(t *testing.T, db *gorm.DB, id int32, status string, paidAt *time.Time) entity.RechargeOrder {
	t.Helper()
	row := entity.RechargeOrder{
		ID: id, UserID: 10, OrderNo: "status-order-" + status + "-" + itoa32(id),
		PaymentProvider: DefaultPaymentProvider, Status: status, Amount: 50, PaidAt: paidAt,
		PaymentMethodNameSnapshot: "支付宝",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	return row
}

func seedStatusTestActors(t *testing.T, db *gorm.DB) (entity.User, entity.User) {
	t.Helper()
	admin := entity.User{ID: 1, Username: "admin", Password: "x", Role: "admin", Status: "enabled", Balance: 12.34}
	normal := entity.User{ID: 10, Username: "normal", Password: "x", Role: "user", Status: "enabled", Balance: 12.34}
	for _, u := range []entity.User{admin, normal} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	return admin, normal
}

// pending → paid 必须成功，且**补 paid_at**（否则该订单永远进不了可退列表）。
func TestUpdateRechargeStatusPendingToPaidSetsPaidAt(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	admin, _ := seedStatusTestActors(t, db)
	order := seedStatusTestOrder(t, db, 1, rechargeOrderStatusPending, nil)

	got, err := UpdateRechargeOrderStatusWithDB(db, admin, order.ID, "paid")
	if err != nil {
		t.Fatalf("pending→paid 应成功: %v", err)
	}
	if got.Status != rechargeOrderStatusPaid {
		t.Fatalf("状态应为 paid，实际 %q", got.Status)
	}
	if got.PaidAt == nil {
		t.Fatal("改成 paid 时必须补 paid_at，否则该订单永远进不了可退列表")
	}
}

// 已有 paid_at 的订单改状态时不得覆盖原支付时间。
func TestUpdateRechargeStatusKeepsExistingPaidAt(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	admin, _ := seedStatusTestActors(t, db)
	original := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	order := seedStatusTestOrder(t, db, 1, rechargeOrderStatusPending, &original)

	got, err := UpdateRechargeOrderStatusWithDB(db, admin, order.ID, "paid")
	if err != nil {
		t.Fatalf("应成功: %v", err)
	}
	if got.PaidAt == nil || !got.PaidAt.Equal(original) {
		t.Fatalf("不得覆盖已有 paid_at（那是渠道写的真实支付时刻），实际 %v", got.PaidAt)
	}
}

// 核心安全断言：改状态不得改动任何钱包列。
func TestUpdateRechargeStatusNeverTouchesWallet(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	admin, _ := seedStatusTestActors(t, db)
	order := seedStatusTestOrder(t, db, 1, rechargeOrderStatusPending, nil)

	var before entity.User
	db.First(&before, 10)

	if _, err := UpdateRechargeOrderStatusWithDB(db, admin, order.ID, "paid"); err != nil {
		t.Fatalf("应成功: %v", err)
	}

	var after entity.User
	db.First(&after, 10)
	if after.Balance != before.Balance || after.GiftBalance != before.GiftBalance {
		t.Fatalf("改状态不得碰钱包：balance %v→%v gift %v→%v",
			before.Balance, after.Balance, before.GiftBalance, after.GiftBalance)
	}
	// 也不得产生账单流水 —— 那等于凭空记了一笔入账。
	var billingCount int64
	db.Model(&entity.BillingRecord{}).Count(&billingCount)
	if billingCount != 0 {
		t.Fatalf("改状态不得生成账单流水，实际 %d 条", billingCount)
	}
}

// 非管理员必须被拒。
func TestUpdateRechargeStatusRequiresAdmin(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	_, normal := seedStatusTestActors(t, db)
	order := seedStatusTestOrder(t, db, 1, rechargeOrderStatusPending, nil)

	_, err := UpdateRechargeOrderStatusWithDB(db, normal, order.ID, "paid")
	if !errors.Is(err, ErrRechargeOrderStatusForbidden) {
		t.Fatalf("普通用户改状态必须被拒（403 语义），实际 %v", err)
	}
	var after entity.RechargeOrder
	db.First(&after, order.ID)
	if after.Status != rechargeOrderStatusPending {
		t.Fatalf("被拒绝后状态不得变化，实际 %q", after.Status)
	}
}

// refunded → paid 必须被拒：钱已经退出去了，重新标记成已支付是真实事故入口。
func TestUpdateRechargeStatusRejectsRefundedToPaid(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	admin, _ := seedStatusTestActors(t, db)
	paid := time.Now()
	order := seedStatusTestOrder(t, db, 1, rechargeOrderStatusRefunded, &paid)

	_, err := UpdateRechargeOrderStatusWithDB(db, admin, order.ID, "paid")
	if !errors.Is(err, ErrRechargeOrderStatusInvalid) {
		t.Fatalf("refunded→paid 必须被拒，实际 %v", err)
	}
}

// 白名单外的状态值必须被拒（例如 failed / 拼写错误）。
func TestUpdateRechargeStatusRejectsUnknownStatus(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	admin, _ := seedStatusTestActors(t, db)
	order := seedStatusTestOrder(t, db, 1, rechargeOrderStatusPending, nil)

	for _, bad := range []string{"failed", "", "PAIDX", "refund"} {
		if _, err := UpdateRechargeOrderStatusWithDB(db, admin, order.ID, bad); !errors.Is(err, ErrRechargeOrderStatusInvalid) {
			t.Fatalf("状态 %q 必须被拒，实际 %v", bad, err)
		}
	}
}

// 目标状态与当前一致 → 幂等，返回 ErrRechargeOrderStatusUnchanged（不是失败）。
func TestUpdateRechargeStatusIsIdempotent(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	admin, _ := seedStatusTestActors(t, db)
	paid := time.Now()
	order := seedStatusTestOrder(t, db, 1, rechargeOrderStatusPaid, &paid)

	_, err := UpdateRechargeOrderStatusWithDB(db, admin, order.ID, "paid")
	if !errors.Is(err, ErrRechargeOrderStatusUnchanged) {
		t.Fatalf("同状态重复设置应返回 Unchanged，实际 %v", err)
	}
}
