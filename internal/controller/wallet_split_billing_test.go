package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// newWalletSplitBillingFixture 建一个能直接驱动扣费落账的最小库。
//
// 与 priceHistory / runtime 的 fixture 同构：只迁移本用例真正会用到的表，
// 库名按 t.Name() 隔离（AGENTS.md 第 9 条：内存库同名会跨用例污染）。
func newWalletSplitBillingFixture(t *testing.T, balance, gift float64) (*gorm.DB, entity.User, entity.AccessToken) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:wallet-split-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	owner := entity.User{ID: 1, Username: "wallet-split-owner", Role: "user", Status: "enabled",
		Balance: balance, GiftBalance: gift}
	token := entity.AccessToken{ID: 1, UserID: owner.ID, Name: "key", TokenHash: "hash",
		TokenPrefix: "tk-", Status: "enabled"}
	for _, item := range []any{&owner, &token} {
		if err := db.Create(item).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return db, owner, token
}

// chargeOnce 用真实落账路径扣一笔钱，返回落账后的钱包与账单行。
func chargeOnce(t *testing.T, db *gorm.DB, owner entity.User, token entity.AccessToken, requestID string, amount float64) (entity.User, entity.BillingRecord) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	billingCtx := searchBillingContext{
		UserID: owner.ID, AccessTokenID: token.ID, RequestID: requestID,
		UsedDefaultAccount: true, Billable: true, ClientIP: "192.0.2.9",
	}
	charges := []searchBillingCharge{{Provider: "icodef", Amount: amount, Snapshot: "provider=icodef"}}
	if err := persistSearchBillingRecordsWithDB(db.WithContext(ctx), billingCtx, charges); err != nil {
		t.Fatalf("persist billing: %v", err)
	}
	var fresh entity.User
	if err := db.First(&fresh, owner.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	var row entity.BillingRecord
	if err := db.Where("request_id = ?", requestID).First(&row).Error; err != nil {
		t.Fatalf("load billing row: %v", err)
	}
	return fresh, row
}

// 双桶扣款 + 分桶记录：赠金够时全额从赠金扣，本金一分不动。
//
// 这两列是「可退金额」的唯一可靠来源 —— 退款可退额 = 充值实付本金 −
// 已消耗本金；没有它们就只能靠猜。
func TestWalletSplitBillingChargesGiftFirst(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 10, 5)

	wallet, row := chargeOnce(t, db, owner, token, "split-gift-only", 3)

	if wallet.GiftBalance != 2 || wallet.Balance != 10 {
		t.Fatalf("gift must be charged first: gift=%v balance=%v", wallet.GiftBalance, wallet.Balance)
	}
	if row.GiftCharged != 3 || row.PrincipalCharged != 0 {
		t.Fatalf("bucket columns wrong: gift=%v principal=%v", row.GiftCharged, row.PrincipalCharged)
	}
	// 本金未动 → 账单里的 balance 前后值应当相等。
	if row.BalanceBefore != 10 || row.BalanceAfter != 10 {
		t.Fatalf("principal snapshot must be unchanged: %v -> %v", row.BalanceBefore, row.BalanceAfter)
	}
}

// 赠金不够时跨桶扣款：赠金扣光，差额从本金扣，两列分别记实际扣减额。
func TestWalletSplitBillingCrossesBuckets(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 10, 2)

	wallet, row := chargeOnce(t, db, owner, token, "split-cross", 5)

	if wallet.GiftBalance != 0 || wallet.Balance != 7 {
		t.Fatalf("cross-bucket charge wrong: gift=%v balance=%v", wallet.GiftBalance, wallet.Balance)
	}
	if row.GiftCharged != 2 || row.PrincipalCharged != 3 {
		t.Fatalf("bucket columns wrong: gift=%v principal=%v", row.GiftCharged, row.PrincipalCharged)
	}
	// 两列之和必须等于这笔账单金额（守恒）。
	if sum := row.GiftCharged + row.PrincipalCharged; sum != row.Amount {
		t.Fatalf("split must add up to the amount: %v != %v", sum, row.Amount)
	}
	if row.BalanceBefore != 10 || row.BalanceAfter != 7 {
		t.Fatalf("principal snapshot wrong: %v -> %v", row.BalanceBefore, row.BalanceAfter)
	}
}

// 本金不足时允许透支：赠金先扣光，本金变负（欠款载体），两列仍守恒。
func TestWalletSplitBillingAllowsPrincipalDebt(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 1, 2)

	wallet, row := chargeOnce(t, db, owner, token, "split-debt", 5)

	if wallet.GiftBalance != 0 {
		t.Fatalf("gift must be drained to 0, got %v", wallet.GiftBalance)
	}
	if wallet.Balance != -2 {
		t.Fatalf("principal must go negative (debt carrier), got %v", wallet.Balance)
	}
	if row.GiftCharged != 2 || row.PrincipalCharged != 3 {
		t.Fatalf("bucket columns wrong: gift=%v principal=%v", row.GiftCharged, row.PrincipalCharged)
	}
	// 不变量 I1：赠金永不为负。
	if wallet.GiftBalance < 0 {
		t.Fatalf("gift_balance must never be negative, got %v", wallet.GiftBalance)
	}
}

// 连续多笔消费后，把账单里的 principal_charged 求和 == 本金实际减少额。
//
// 这条钉住的是「可退金额能从账单重建」这个前提：若两列与实际钱包变动对不上，
// 任何基于账单的可退额计算都是错的。
func TestWalletSplitBillingColumnsReconstructPrincipalSpend(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 10, 4)

	amounts := []float64{1, 2, 3, 5}
	for i, amount := range amounts {
		chargeOnce(t, db, owner, token, fmt.Sprintf("split-seq-%d", i), amount)
	}

	var fresh entity.User
	if err := db.First(&fresh, owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	// 合计消费 11：赠金 4 全花掉，本金花掉 7 → 10-7 = 3。
	if fresh.GiftBalance != 0 || fresh.Balance != 3 {
		t.Fatalf("sequential spend wrong: gift=%v balance=%v", fresh.GiftBalance, fresh.Balance)
	}

	var sums struct {
		Gift      float64 `gorm:"column:gift"`
		Principal float64 `gorm:"column:principal"`
		Amount    float64 `gorm:"column:amount"`
	}
	if err := db.Model(&entity.BillingRecord{}).
		Select("COALESCE(SUM(gift_charged),0) AS gift, COALESCE(SUM(principal_charged),0) AS principal, COALESCE(SUM(amount),0) AS amount").
		Scan(&sums).Error; err != nil {
		t.Fatal(err)
	}
	if sums.Amount != 11 {
		t.Fatalf("total amount = %v, want 11", sums.Amount)
	}
	if sums.Gift+sums.Principal != sums.Amount {
		t.Fatalf("split sum must equal total amount: %v + %v != %v", sums.Gift, sums.Principal, sums.Amount)
	}
	// 已消耗本金 == 本金实际减少额（10 → 3）。
	if sums.Principal != 7 {
		t.Fatalf("principal_charged sum = %v, want 7 (matches actual principal drop)", sums.Principal)
	}
	if sums.Gift != 4 {
		t.Fatalf("gift_charged sum = %v, want 4 (all gift spent)", sums.Gift)
	}
}

// 同一批落账内的跨笔精度：一次搜题命中多个渠道时，`charges` 里有多笔，
// 它们共享同一个 `exactBalance`（跨笔不取整、只在每笔边界取整）。
//
// 这条钉住的是一个真实缺陷：若分桶列记「未取整的份额」再各自 RoundMoney，
// 每笔的舍入误差会**在同一批内累积**。实测 7 笔各 0.0000007：
//
//	钱包实际只降 0.000005，而分桶列加起来是 0.000007（偏差 0.000002）。
//
// 可退额 = 实付本金 − Σ(principal_charged)，偏差会直接变成退款算错。
//
// 注意必须走**一次** persistSearchBillingRecordsWithDB（多笔 charges），
// 分多次调用测不出来：每次调用都从已取整的 user.Balance 重新开始，不会累积。
func TestWalletSplitBillingColumnsHaveNoCrossChargeDrift(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 10, 0)

	// 一批 7 笔各 7e-7：单笔都被 6 位取整抹掉，但合起来 4.9e-6 会真实体现。
	charges := make([]searchBillingCharge, 7)
	for i := range charges {
		charges[i] = searchBillingCharge{Provider: "icodef", Amount: 0.0000007, Snapshot: "provider=icodef"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	billingCtx := searchBillingContext{
		UserID: owner.ID, AccessTokenID: token.ID, RequestID: "drift-batch",
		UsedDefaultAccount: true, Billable: true, ClientIP: "192.0.2.9",
	}
	if err := persistSearchBillingRecordsWithDB(db.WithContext(ctx), billingCtx, charges); err != nil {
		t.Fatalf("persist batch: %v", err)
	}

	var fresh entity.User
	if err := db.First(&fresh, owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	actualDrop := 10.0 - fresh.Balance
	if actualDrop < 4.9e-6 || actualDrop > 5.1e-6 {
		t.Fatalf("sanity: the real principal drop should be ~5e-6, got %v", actualDrop)
	}

	var sums struct {
		Principal float64 `gorm:"column:principal"`
	}
	if err := db.Model(&entity.BillingRecord{}).
		Select("COALESCE(SUM(principal_charged),0) AS principal").
		Scan(&sums).Error; err != nil {
		t.Fatal(err)
	}
	// 容差 1e-12：float64 表示误差在 1e-16 量级，而真实缺陷造成的偏差是 2e-6，
	// 两者差 6 个数量级，这个容差既能放过表示误差又能抓住真缺陷。
	if diff := sums.Principal - actualDrop; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("Σ principal_charged (%v) must equal the actual principal drop (%v): "+
			"any drift breaks the refundable-amount formula", sums.Principal, actualDrop)
	}
}

// 赠金与本金混合、跨笔累积时两列之和仍必须等于账单金额之和。
func TestWalletSplitBillingColumnsSumToAmountAcrossCharges(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 10, 1)

	amounts := []float64{0.0000007, 0.4, 0.6, 0.0000003, 2}
	for i, amount := range amounts {
		chargeOnce(t, db, owner, token, fmt.Sprintf("mixed-%d", i), amount)
	}

	var sums struct {
		Gift      float64 `gorm:"column:gift"`
		Principal float64 `gorm:"column:principal"`
		Amount    float64 `gorm:"column:amount"`
	}
	if err := db.Model(&entity.BillingRecord{}).
		Select("COALESCE(SUM(gift_charged),0) AS gift, COALESCE(SUM(principal_charged),0) AS principal, COALESCE(SUM(amount),0) AS amount").
		Scan(&sums).Error; err != nil {
		t.Fatal(err)
	}
	// 两列之和必须等于账单金额之和（守恒），允许 1e-9 浮点容差。
	if diff := (sums.Gift + sums.Principal) - sums.Amount; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("split sum must equal amount sum: gift=%v principal=%v amount=%v diff=%v",
			sums.Gift, sums.Principal, sums.Amount, diff)
	}
	// 且必须与两个桶的实际变动一致。
	var fresh entity.User
	if err := db.First(&fresh, owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if giftDrop := 1.0 - fresh.GiftBalance; giftDrop != sums.Gift {
		t.Fatalf("gift_charged sum (%v) must equal the actual gift drop (%v)", sums.Gift, giftDrop)
	}
	if principalDrop := 10.0 - fresh.Balance; principalDrop != sums.Principal {
		t.Fatalf("principal_charged sum (%v) must equal the actual principal drop (%v)", sums.Principal, principalDrop)
	}
}

// 历史行（加列之前落账的）两列必须是 0，且读侧要把它当「未知」而不是「没扣本金」。
//
// 这条钉住的是迁移的「不回填」决定：旧模型 balance 是总可用、账单里没有分桶信息，
// 任何推断都会写错钱包口径。所以历史行留在 0，可退额计算必须显式识别这种情况
// （例如「该用户存在未知分桶的历史消费 → 保守按全额已消耗处理」），
// 而不是把 0 当成「这一笔没花本金」。
func TestWalletSplitBillingLeavesHistoricalRowsAtZero(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 10, 5)
	// 模拟加列之前的历史行：两列默认 0。
	historical := entity.BillingRecord{
		UserID: owner.ID, AccessTokenID: token.ID, Type: searchBillingRecordTypeNormal,
		Provider: "icodef", Amount: 1, BalanceBefore: 11, BalanceAfter: 10,
		RequestID: "legacy-row", Remark: "搜索答题消耗",
	}
	if err := db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	var stored entity.BillingRecord
	if err := db.Where("request_id = ?", "legacy-row").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.GiftCharged != 0 || stored.PrincipalCharged != 0 {
		t.Fatalf("historical rows must stay at 0 (unknown, not 'no principal'), got %+v",
			[]float64{stored.GiftCharged, stored.PrincipalCharged})
	}
	// 读侧必须能把 0 识别为「未知」而不是「没扣本金」。
	if service.WalletAvailableTotal(stored.GiftCharged, stored.PrincipalCharged) != 0 {
		t.Fatal("sanity: zero split columns represent unknown, not zero spend")
	}
}
