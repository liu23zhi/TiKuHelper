package service

import (
	"math"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/util"
)

// nearEqual 比较两个 6 位金额（用 util.RoundMoney 归一再比，避免浮点尾差误判）。
func nearEqual(a, b float64) bool {
	return math.Abs(util.RoundMoney(a)-util.RoundMoney(b)) < 1e-9
}

// Test 1（43-*.md 算例 A）：赠金 2 + 本金 1，消耗 4 → 赠金 0、本金 −1。
//
// 这是双桶模型最核心的一条：**先扣赠金、扣光再扣本金、本金允许为负**。
func TestWalletConsumeExampleA(t *testing.T) {
	balance, gift := ConsumeWalletBuckets(1, 2, 4)
	if !nearEqual(gift, 0) {
		t.Fatalf("gift must drain to 0, got %v", gift)
	}
	if !nearEqual(balance, -1) {
		t.Fatalf("principal must go to -1 (debt carrier), got %v", balance)
	}
	// 合计 −1 ⇒ 准入拒绝。
	if WalletAvailableTotal(balance, gift) > 0 {
		t.Fatalf("total must be -1 after example A, got %v", WalletAvailableTotal(balance, gift))
	}
}

// Test 2（43-*.md 算例 B）：本金 −1、赠金 0，赠 2 → 本金 0、赠金 1。
//
// 规则 3 是本轮最容易漏的一条：直接 gift += 2 会得到 balance=−1 gift=2。
func TestWalletCreditGiftExampleB(t *testing.T) {
	balance, gift := CreditGiftBuckets(-1, 0, 2)
	if !nearEqual(balance, 0) {
		t.Fatalf("principal must be offset to 0, got %v", balance)
	}
	if !nearEqual(gift, 1) {
		t.Fatalf("only the remainder (1) may enter the gift bucket, got %v", gift)
	}
	// 合计 1 ⇒ 准入放行（旧判据 balance<=0 会误拒）。
	if WalletAvailableTotal(balance, gift) <= 0 {
		t.Fatalf("total must be 1 after example B, got %v", WalletAvailableTotal(balance, gift))
	}
}

// Test 3（规则 3 边界）：抵不完 / 刚好抵平 / 抵完剩。
func TestWalletCreditGiftDebtBoundaries(t *testing.T) {
	cases := []struct {
		name                  string
		balance, gift, amount float64
		wantBalance, wantGift float64
	}{
		{"抵不完", -5, 0, 3, -2, 0},
		{"刚好抵平", -5, 0, 5, 0, 0},
		{"抵完剩3", -5, 0, 8, 0, 3},
		{"无欠款全进赠金", 0, 0, 2, 0, 2},
		{"有本金也无欠款", 3, 1, 2, 3, 3},
		{"零金额不动", -5, 0, 0, -5, 0},
	}
	for _, tc := range cases {
		balance, gift := CreditGiftBuckets(tc.balance, tc.gift, tc.amount)
		if !nearEqual(balance, tc.wantBalance) || !nearEqual(gift, tc.wantGift) {
			t.Fatalf("%s: balance=%v gift=%v, want %v/%v",
				tc.name, balance, gift, tc.wantBalance, tc.wantGift)
		}
	}
}

// Test 4（规则 2 边界）：无赠金纯扣本金 / 刚好扣光赠金 / 赠金扣光后本金变负。
func TestWalletConsumeBoundaries(t *testing.T) {
	cases := []struct {
		name                  string
		balance, gift, amount float64
		wantBalance, wantGift float64
	}{
		{"无赠金纯扣本金", 5, 0, 3, 2, 0},
		{"刚好扣光赠金", 5, 3, 3, 5, 0},
		{"赠金扣光后本金变负", 5, 3, 10, -2, 0},
		{"赠金足够本金不动", 5, 10, 3, 5, 7},
		{"零金额不动", 5, 3, 0, 5, 3},
		{"负赠金脏数据按0处理", 5, -2, 3, 2, 0},
	}
	for _, tc := range cases {
		balance, gift := ConsumeWalletBuckets(tc.balance, tc.gift, tc.amount)
		if !nearEqual(balance, tc.wantBalance) || !nearEqual(gift, tc.wantGift) {
			t.Fatalf("%s: balance=%v gift=%v, want %v/%v",
				tc.name, balance, gift, tc.wantBalance, tc.wantGift)
		}
	}
}

// Test 5（不变量 I1 / I3）：任意操作序列后 gift >= 0，且不可能出现 balance<0 且 gift>0。
//
// I3 是一条很强的自检：只要规则 2 或规则 3 写错，这个断言立刻会红。
func TestWalletBucketInvariants(t *testing.T) {
	type op struct {
		consume   bool
		amount    float64
		balanceIn float64
		giftIn    float64
	}
	ops := []op{
		{true, 4, 1, 2},
		{false, 2, -1, 0},
		{true, 3, 5, 0},
		{true, 3, 5, 3},
		{true, 10, 5, 3},
		{false, 3, -5, 0},
		{false, 5, -5, 0},
		{false, 8, -5, 0},
		{true, 0.000001, 0, 0},
		{false, 0.001, -0.5, 0},
		{true, 1.5, 0.25, 0.75},
	}
	balance, gift := 0.0, 0.0
	for i, step := range ops {
		before := step.balanceIn
		if step.consume {
			balance, gift = ConsumeWalletBuckets(step.balanceIn, step.giftIn, step.amount)
		} else {
			balance, gift = CreditGiftBuckets(step.balanceIn, step.giftIn, step.amount)
		}
		if gift < 0 {
			t.Fatalf("I1 violated at step %d: gift=%v", i, gift)
		}
		if balance < 0 && gift > 0 {
			t.Fatalf("I3 violated at step %d: balance=%v gift=%v (rules 2/3 are wrong)", i, balance, gift)
		}
		if !step.consume {
			// 赠送入账只能让本金**上升**（抵欠款），绝不会让它更负。
			if balance < before {
				t.Fatalf("I4 violated at step %d: gift credit lowered principal %v -> %v", i, before, balance)
			}
			// 抵完欠款后本金必然 >= 0（43-*.md 算例 B 与边界表）。
			if step.amount >= -before && balance < 0 {
				t.Fatalf("I4 violated at step %d: credit %v covers debt %v but balance stayed %v",
					i, step.amount, -before, balance)
			}
		}
	}
}

// Test 6：DeductGiftBucket 的 clamp 语义 —— 应扣 8、桶内 5 → 实扣 5、桶归 0。
func TestDeductGiftBucketClamps(t *testing.T) {
	cases := []struct {
		name                      string
		gift, amount              float64
		wantDeducted, wantNewGift float64
	}{
		{"足额", 10, 8, 8, 2},
		{"不足", 5, 8, 5, 0},
		{"恰好", 8, 8, 8, 0},
		{"桶已空", 0, 8, 0, 0},
		{"负脏数据", -3, 8, 0, 0},
		{"零金额", 5, 0, 0, 5},
	}
	for _, tc := range cases {
		deducted, newGift := DeductGiftBucket(tc.gift, tc.amount)
		if !nearEqual(deducted, tc.wantDeducted) || !nearEqual(newGift, tc.wantNewGift) {
			t.Fatalf("%s: deducted=%v gift=%v, want %v/%v",
				tc.name, deducted, newGift, tc.wantDeducted, tc.wantNewGift)
		}
		if newGift < 0 {
			t.Fatalf("%s: gift must never go negative, got %v", tc.name, newGift)
		}
	}
}

// Test 7：可用合计 = 本金 + 赠金（准入判据的唯一来源）。
func TestWalletAvailableTotal(t *testing.T) {
	cases := []struct {
		balance, gift, want float64
	}{
		{-1, 0, -1},
		{0, 1, 1},
		{5, 0, 5},
		{0, 0, 0},
		{0.000001, 0.000002, 0.000003},
		{-2.5, 0.5, -2},
	}
	for _, tc := range cases {
		if got := WalletAvailableTotal(tc.balance, tc.gift); !nearEqual(got, tc.want) {
			t.Fatalf("total(%v,%v) = %v, want %v", tc.balance, tc.gift, got, tc.want)
		}
	}
}
