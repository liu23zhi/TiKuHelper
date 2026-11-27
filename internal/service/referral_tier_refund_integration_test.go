package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func t11ReferralFixture(t *testing.T) refundEPayFixture {
	t.Helper()
	f := newRefundEPayFixture(t)
	if err := f.db.AutoMigrate(&entity.ReferralRebate{}, &entity.RechargePromotionTierRule{}, &entity.RechargePromotionGrant{},
		&entity.Ticket{}, &entity.TicketMessage{}, &entity.TicketNotification{}, &entity.TicketRefund{},
		&entity.TicketRefundOrderLine{}, &entity.TicketRefundLedger{}); err != nil {
		t.Fatal(err)
	}
	return f
}

func t11ReferralPolicy(t *testing.T, db *gorm.DB, value string) {
	t.Helper()
	if err := saveSystemSettingValue(db, "referral_auto_rebate_rules", value, "T11 synthetic policy"); err != nil {
		t.Fatal(err)
	}
}

func t11ReferralPaid(t *testing.T, f refundEPayFixture, payer entity.User, name string, principal float64) entity.RechargeOrder {
	t.Helper()
	order := entity.RechargeOrder{UserID: payer.ID, OrderNo: "t11-" + name,
		PaymentProvider: DefaultPaymentProvider, PaymentMethodNameSnapshot: "synthetic",
		Status: rechargeOrderStatusPending, Amount: principal, RefundChannelFingerprint: f.cfg.fingerprint}
	if err := f.db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	values := url.Values{
		"pid": {f.cfg.partnerID}, "type": {"alipay"}, "out_trade_no": {order.OrderNo},
		"trade_no": {"trade-" + order.OrderNo}, "trade_status": {rechargeOrderTradeStatusSuccess},
		"money": {formatRechargeAmount(principal)},
	}
	signSyntheticRefundEPay(t, values, f.platformPrivate)
	for i := 0; i < 2; i++ {
		if ack, err := HandleRechargePaymentNotifyWithDB(f.db, values); err != nil || !ack {
			t.Fatalf("signed recharge %d: ack=%t err=%v", i+1, ack, err)
		}
	}
	if err := f.db.First(&order, order.ID).Error; err != nil || order.PaidAt == nil || order.Status != rechargeOrderStatusPaid {
		t.Fatalf("recharge did not settle: status=%s err=%v", order.Status, err)
	}
	var bills []entity.BillingRecord
	if err := f.db.Where("related_order_id = ? AND type = ?", order.ID, rechargeBillingRecordType).Find(&bills).Error; err != nil ||
		len(bills) != 1 || bills[0].UserID != payer.ID || bills[0].RequestID != order.OrderNo || bills[0].Amount != principal {
		t.Fatalf("signed callback must create exactly one matching principal credit: bills=%+v err=%v", bills, err)
	}
	return order
}

func t11ReferralAward(t *testing.T, db *gorm.DB, order entity.RechargeOrder) entity.ReferralRebate {
	t.Helper()
	var awards []entity.ReferralRebate
	if err := db.Where("recharge_order_id = ?", order.ID).Find(&awards).Error; err != nil || len(awards) != 1 {
		t.Fatalf("expected exactly one award for paid order %d: count=%d err=%v", order.ID, len(awards), err)
	}
	return awards[0]
}

func t11Gift(t *testing.T, db *gorm.DB, uid int32, amount, principal string) {
	t.Helper()
	var wallet entity.User
	if err := db.First(&wallet, uid).Error; err != nil {
		t.Fatal(err)
	}
	if gift, balance := fmt.Sprintf("%.6f", wallet.GiftBalance), fmt.Sprintf("%.6f", wallet.Balance); gift != amount || balance != principal {
		t.Errorf("wallet %d: gift=%s balance=%s want gift=%s balance=%s", uid, gift, balance, amount, principal)
	}
}

func t11ReversalBills(t *testing.T, db *gorm.DB, uid, orderID int32, amounts ...string) {
	t.Helper()
	var bills []entity.BillingRecord
	if err := db.Where("user_id = ? AND related_order_id = ? AND type = ?", uid, orderID, ReferralRebateReversalBillingRecordType).
		Order("id").Find(&bills).Error; err != nil {
		t.Fatal(err)
	}
	if len(bills) != len(amounts) {
		t.Errorf("order %d reversal bills: count=%d want=%d", orderID, len(bills), len(amounts))
		return
	}
	for i, bill := range bills {
		if got := fmt.Sprintf("%.6f", bill.Amount); got != amounts[i] || bill.BalanceBefore != bill.BalanceAfter {
			t.Errorf("order %d reversal bill %d: amount=%s want=%s principal-before=%v principal-after=%v",
				orderID, i, got, amounts[i], bill.BalanceBefore, bill.BalanceAfter)
		}
	}
}

func TestT11TierVerifiedRechargeExcludesPromotionAndLegacySwitch(t *testing.T) {
	f := t11ReferralFixture(t)
	inviter := seedReferralUser(t, f.db, 71, "t11-inviter", 0)
	payer := seedReferralUser(t, f.db, 72, "t11-payer", inviter.ID)
	admin := seedRechargeOrderUser(t, f.db, 73, "admin")
	setReferralSettings(t, f.db, false, 0.75)
	t11ReferralPolicy(t, f.db, `{"enabled":true,"rules":[{"enabled":true,"lowerCents":100000,"upperCents":200000,"rateUnits":10000000}]}`)
	if err := f.db.Create(&entity.RechargePromotionTierRule{Threshold: 1688, Rate: 2, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	order := t11ReferralPaid(t, f, payer, "promotion", 1688)
	t11Gift(t, f.db, payer.ID, "3376.000000", "1688.000000")
	award := t11ReferralAward(t, f.db, order)
	if award.Source != entity.ReferralRebateSourceAuto || award.Status != entity.ReferralRebateStatusPending ||
		award.InviterUserID != inviter.ID || award.InvitedUserID != payer.ID || award.OrderNo != order.OrderNo ||
		award.BaseAmount != 1688 || award.Rate != 0.1 || award.Amount != 168.8 {
		t.Fatalf("pending award must come from this paid principal and matched tier only: %+v", award)
	}
	t11Gift(t, f.db, inviter.ID, "0.000000", "0.000000")
	for i := 0; i < 2; i++ {
		if result, err := SettleReferralRebateWithDB(f.db, admin, award.ID); err != nil || result.Status != entity.ReferralRebateStatusSettled {
			t.Fatalf("administrator settlement %d: status=%q err=%v", i+1, result.Status, err)
		}
	}
	t11Gift(t, f.db, inviter.ID, "168.800000", "0.000000")
	var credits []entity.BillingRecord
	if err := f.db.Where("user_id = ? AND related_order_id = ? AND type = ?", inviter.ID, order.ID, ReferralRebateBillingRecordType).
		Find(&credits).Error; err != nil || len(credits) != 1 || credits[0].Amount != 168.8 {
		t.Fatalf("associated settlement credit must happen once: credits=%+v err=%v", credits, err)
	}
}

func TestT11TierUnmatchedAndEmptyPolicyDoNotAward(t *testing.T) {
	f := t11ReferralFixture(t)
	inviter := seedReferralUser(t, f.db, 71, "t11-inviter", 0)
	payer := seedReferralUser(t, f.db, 72, "t11-payer", inviter.ID)
	setReferralSettings(t, f.db, true, 0.75)
	for _, tc := range []struct{ name, policy string }{
		{"unmatched", `{"enabled":true,"rules":[{"enabled":true,"lowerCents":10001,"upperCents":null,"rateUnits":10000000}]}`},
		{"empty", `{"enabled":true,"rules":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t11ReferralPolicy(t, f.db, tc.policy)
			order := t11ReferralPaid(t, f, payer, tc.name, 100)
			var count int64
			if err := f.db.Model(&entity.ReferralRebate{}).Where("recharge_order_id = ?", order.ID).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("policy %s cannot fall back to the old scalar: count=%d err=%v", tc.name, count, err)
			}
		})
	}
	t11Gift(t, f.db, payer.ID, "0.000000", "200.000000")
	t11Gift(t, f.db, inviter.ID, "0.000000", "0.000000")
}

func t11SignedRefundReply(t *testing.T, f refundEPayFixture, refund entity.TicketRefund, order entity.RechargeOrder, status, money string) {
	t.Helper()
	installRefundEPayTransport(t, func(req *http.Request) (*http.Response, error) {
		want := map[string]string{"out_refund_no": refund.RefundNo}
		path := "/api/pay/refundquery"
		if req.URL.Path == "/api/pay/refund" {
			path = "/api/pay/refund"
			want["money"] = fmt.Sprintf("%d.%02d", refund.ApprovedCents/100, refund.ApprovedCents%100)
			want["trade_no"] = order.RefundOriginalTradeNo
		}
		f.assertRequest(t, req, path, want)
		fields := url.Values{
			"code": {"0"}, "status": {status}, "pid": {f.cfg.partnerID}, "money": {money},
			"trade_no": {order.RefundOriginalTradeNo}, "out_trade_no": {order.OrderNo},
			"out_refund_no": {refund.RefundNo}, "refund_no": {"t11-provider-" + refund.RefundNo},
			"timestamp": {strconv.FormatInt(time.Now().Unix(), 10)},
		}
		signSyntheticRefundEPay(t, fields, f.platformPrivate)
		return refundEPayResponse(http.StatusOK, refundEPayJSON(t, fields)), nil
	})
}

func TestT11TierSignedPartialRefundKeepsRemainingAward(t *testing.T) {
	for _, settleBefore := range []bool{true, false} {
		name := "pending"
		if settleBefore {
			name = "settled"
		}
		t.Run(name, func(t *testing.T) {
			f := t11ReferralFixture(t)
			inviter := seedReferralUser(t, f.db, 71, "t11-inviter", 0)
			payer := seedReferralUser(t, f.db, 72, "t11-payer", inviter.ID)
			admin := seedRechargeOrderUser(t, f.db, 73, "admin")
			// The same rate in the legacy setting isolates the refund regression on the old implementation.
			setReferralSettings(t, f.db, true, 0.03333333)
			t11ReferralPolicy(t, f.db, `{"enabled":true,"rules":[{"enabled":true,"lowerCents":0,"upperCents":null,"rateUnits":3333333}]}`)
			order := t11ReferralPaid(t, f, payer, name, 50.25)
			award := t11ReferralAward(t, f.db, order)
			if award.Amount != 1.675 {
				t.Fatalf("pre-refund award: amount=%v want=1.675", award.Amount)
			}
			before := "0.000000"
			if settleBefore {
				if _, err := SettleReferralRebateWithDB(f.db, admin, award.ID); err != nil {
					t.Fatal(err)
				}
				before = "1.675000"
			}
			refund, err := CreateTicketRefundWithDB(f.db, payer, CreateTicketRefundInput{
				Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 3000,
			}, f.cfg.VerifyOriginalOrder)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReviewTicketRefundWithDB(f.db, admin, refund.ID, true, 1501, f.cfg.VerifyOriginalOrder); err != nil {
				t.Fatal(err)
			}
			staged, err := BeginTicketRefundWithDB(f.db, admin, refund.ID, f.cfg.VerifyOriginalOrder)
			if err != nil {
				t.Fatal(err)
			}

			// A terminal-looking submit acknowledgement is not settlement proof.
			t11SignedRefundReply(t, f, staged, order, "1", "15.01")
			if err := SubmitTicketRefundEPay(context.Background(), f.cfg, staged, order); err != nil {
				t.Fatal(err)
			}
			t11Gift(t, f.db, inviter.ID, before, "0.000000")
			t11ReversalBills(t, f.db, inviter.ID, order.ID)
			t11SignedRefundReply(t, f, staged, order, "1", "15.02")
			if _, _, err := QueryTicketRefundEPay(context.Background(), f.cfg, staged, order); !errors.Is(err, ErrTicketRefundChannelUnknown) {
				t.Fatalf("signed but wrong payout amount must be rejected: %v", err)
			}
			t11Gift(t, f.db, inviter.ID, before, "0.000000")

			t11SignedRefundReply(t, f, staged, order, "1", "15.01")
			outcome, ref, err := QueryTicketRefundEPay(context.Background(), f.cfg, staged, order)
			if err != nil || outcome != TicketRefundStatusSucceeded {
				t.Fatalf("signed exact payout proof: outcome=%q err=%v", outcome, err)
			}
			for i := 0; i < 2; i++ {
				if result, err := ReconcileTicketRefundWithDB(f.db, admin, refund.ID, outcome, ref); err != nil || result.Status != outcome {
					t.Fatalf("apply verified payout %d: status=%q err=%v", i+1, result.Status, err)
				}
			}
			if settleBefore {
				t11ReversalBills(t, f.db, inviter.ID, order.ID, "0.500333")
			} else {
				if result, err := SettleReferralRebateWithDB(f.db, admin, award.ID); err != nil || result.Status != entity.ReferralRebateStatusSettled {
					t.Errorf("partial pending award must remain available for net settlement: status=%q err=%v", result.Status, err)
				}
				t11ReversalBills(t, f.db, inviter.ID, order.ID)
			}
			t11Gift(t, f.db, inviter.ID, "1.174667", "0.000000")
			requireTicketRefundWallet(t, f.db, payer.ID, 35_240_000)
		})
	}
}

func TestT11TierSignedGroupedRefundClawsBackOnlySucceededOrder(t *testing.T) {
	f := t11ReferralFixture(t)
	inviter := seedReferralUser(t, f.db, 71, "t11-inviter", 0)
	payer := seedReferralUser(t, f.db, 72, "t11-payer", inviter.ID)
	admin := seedRechargeOrderUser(t, f.db, 73, "admin")
	setReferralSettings(t, f.db, true, 0.1)
	t11ReferralPolicy(t, f.db, `{"enabled":true,"rules":[{"enabled":true,"lowerCents":0,"upperCents":null,"rateUnits":10000000}]}`)
	partial := t11ReferralPaid(t, f, payer, "group-partial", 20)
	full := t11ReferralPaid(t, f, payer, "group-full", 20)
	failed := t11ReferralPaid(t, f, payer, "group-failed", 20)
	for _, order := range []entity.RechargeOrder{partial, full, failed} {
		award := t11ReferralAward(t, f.db, order)
		if _, err := SettleReferralRebateWithDB(f.db, admin, award.ID); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := CreateTicketForUserWithDB(f.db, &payer, CreateTicketInput{TicketType: TicketTypeRefund,
		RefundMethod: TicketRefundKindOriginal, RefundRequestedCents: 3500, RefundReason: "synthetic settled payouts",
		RefundOrderAllocations: []TicketRefundOrderAllocation{
			{OrderID: partial.ID, Cents: 1000}, {OrderID: full.ID, Cents: 2000}, {OrderID: failed.ID, Cents: 500},
		},
	}, nil)
	if err != nil || detail.Refund == nil {
		t.Fatalf("create grouped refund: %v", err)
	}
	refundID := detail.Refund.RefundID
	if _, err := ReviewTicketRefundDecisionWithDB(f.db, admin, refundID, true, nil, f.cfg.VerifyOriginalOrder); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		order                                      entity.RechargeOrder
		status, gatewayStatus, money, parentStatus string
		gift, awardStatus                          string
		reversal                                   []string
	}{
		{partial, TicketRefundStatusSucceeded, "1", "10.00", TicketRefundStatusProcessing, "5.000000", entity.ReferralRebateStatusSettled, []string{"1.000000"}},
		{full, TicketRefundStatusSucceeded, "1", "20.00", TicketRefundStatusProcessing, "3.000000", entity.ReferralRebateStatusReversed, []string{"2.000000"}},
		{failed, TicketRefundStatusFailed, "0", "5.00", TicketRefundStatusPartial, "3.000000", entity.ReferralRebateStatusSettled, nil},
	} {
		_, staged, order, err := PrepareTicketRefundLineWithDB(f.db, admin, refundID, tc.order.ID, true, f.cfg.VerifyOriginalOrder)
		if err != nil {
			t.Fatal(err)
		}
		t11SignedRefundReply(t, f, staged, order, tc.gatewayStatus, tc.money)
		outcome, ref, err := QueryTicketRefundEPay(context.Background(), f.cfg, staged, order)
		if err != nil || outcome != tc.status {
			t.Fatalf("signed grouped proof for order %d: outcome=%q err=%v", order.ID, outcome, err)
		}
		for i := 0; i < 2; i++ {
			if result, err := ReconcileTicketRefundLineWithDB(f.db, admin, refundID, order.ID, outcome, ref); err != nil ||
				result.Status != tc.parentStatus {
				t.Fatalf("apply verified line %d #%d: parent=%q want=%q err=%v", order.ID, i+1, result.Status, tc.parentStatus, err)
			}
		}
		t11Gift(t, f.db, inviter.ID, tc.gift, "0.000000")
		t11ReversalBills(t, f.db, inviter.ID, order.ID, tc.reversal...)
		if a := t11ReferralAward(t, f.db, order); a.Status != tc.awardStatus {
			t.Errorf("grouped order %d award status=%q want=%q", order.ID, a.Status, tc.awardStatus)
		}
	}
	requireTicketRefundWallet(t, f.db, payer.ID, 30_000_000)
}

func TestT11TierSignedRefundSurvivesArchivedInviter(t *testing.T) {
	for _, status := range []string{entity.ReferralRebateStatusPending, entity.ReferralRebateStatusSettled} {
		t.Run(status, func(t *testing.T) {
			f := t11ReferralFixture(t)
			inviter := seedReferralUser(t, f.db, 71, "t11-inviter", 0)
			payer := seedReferralUser(t, f.db, 72, "t11-payer", inviter.ID)
			admin := seedRechargeOrderUser(t, f.db, 73, "admin")
			t11ReferralPolicy(t, f.db, `{"enabled":true,"rules":[{"enabled":true,"lowerCents":0,"upperCents":null,"rateUnits":10000000}]}`)
			order := t11ReferralPaid(t, f, payer, "archive-"+status, 100)
			award := t11ReferralAward(t, f.db, order)
			if status == entity.ReferralRebateStatusSettled {
				if _, err := SettleReferralRebateWithDB(f.db, admin, award.ID); err != nil {
					t.Fatal(err)
				}
			}

			refund, err := CreateTicketRefundWithDB(f.db, payer, CreateTicketRefundInput{
				Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 4000,
			}, f.cfg.VerifyOriginalOrder)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReviewTicketRefundWithDB(f.db, admin, refund.ID, true, 3000, f.cfg.VerifyOriginalOrder); err != nil {
				t.Fatal(err)
			}
			staged, err := BeginTicketRefundWithDB(f.db, admin, refund.ID, f.cfg.VerifyOriginalOrder)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.db.Delete(&inviter).Error; err != nil {
				t.Fatal(err)
			}
			var before entity.User
			if err := f.db.Unscoped().First(&before, inviter.ID).Error; err != nil || !before.DeletedAt.Valid {
				t.Fatalf("archive fixture not persisted: %v", err)
			}

			t11SignedRefundReply(t, f, staged, order, "1", "30.00")
			outcome, ref, err := QueryTicketRefundEPay(context.Background(), f.cfg, staged, order)
			if err != nil || outcome != TicketRefundStatusSucceeded {
				t.Fatalf("signed proof: outcome=%q err=%v", outcome, err)
			}
			for i := 0; i < 2; i++ {
				got, err := ReconcileTicketRefundWithDB(f.db, admin, refund.ID, outcome, ref)
				if err != nil || got.Status != TicketRefundStatusSucceeded {
					t.Fatalf("verified refund %d must settle despite archived inviter: status=%q err=%v", i+1, got.Status, err)
				}
			}
			if got := t11ReferralAward(t, f.db, order); got.Status != status || got.Amount != 10 ||
				got.ReversedAmount != 3 || got.RefundedCents != 3000 {
				t.Errorf("archival cannot lose or repeat the rebate reduction: status=%s award=%v reversed=%v refunded_cents=%v",
					got.Status, got.Amount, got.ReversedAmount, got.RefundedCents)
			}
			var after entity.User
			if err := f.db.Unscoped().First(&after, inviter.ID).Error; err != nil || !after.DeletedAt.Valid ||
				after.Balance != before.Balance || after.GiftBalance != before.GiftBalance {
				t.Errorf("archived inviter wallet changed: before=(%v,%v) after=(%v,%v) err=%v",
					before.Balance, before.GiftBalance, after.Balance, after.GiftBalance, err)
			}
			var count int64
			if err := f.db.Model(&entity.TicketRefundLedger{}).Where("refund_id = ? AND kind = ?", refund.ID, ticketRefundLedgerConsume).
				Count(&count).Error; err != nil || count != 1 {
				t.Errorf("expected one refund consume receipt, got %d err=%v", count, err)
			}
			requireTicketRefundWallet(t, f.db, payer.ID, 70_000_000)
		})
	}
}
