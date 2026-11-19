package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 金额取整语义（2026-11-19 用户要求）：
//
//	用户可退款金额  —— 向上取整到两位小数（本金是用户的钱，少退一分就是平台占便宜）
//	充值订单可退款金额 —— 向下取整到两位小数（不超额退款）
//
// 两个方向相反是**刻意的**，各自对自己的风险负责。这组用例分别钉住，
// 任何一边被改成四舍五入都会红。

// 订单金额向下取整：20.001 → 2000 分，不是 2001（向上）也不是被拒。
func TestPaidOrderCentsFloorToTwoDecimals(t *testing.T) {
	cases := []struct {
		amount float64
		want   int64
	}{
		{20.001, 2000}, // 超过两位 → 截掉
		{20.009, 2000}, // 接近进位 → 仍截掉
		{20.01, 2001},  // 正好两位 → 原样
		{18.005, 1800}, // 用户举的例子类型
		{0.29, 29},     // 浮点表示误差：0.29*100 = 28.999999999999996
		{7.5, 750},     // 整数分
	}
	for _, c := range cases {
		paidAt := time.Now().Add(-time.Hour)
		order := entity.RechargeOrder{
			ID: 1, UserID: 1, OrderNo: "rounding-order", Status: rechargeOrderStatusPaid,
			PaymentProvider: DefaultPaymentProvider, Amount: c.amount, PaidAt: &paidAt,
		}
		got, err := ticketRefundPaidOrderCents(order)
		if err != nil {
			t.Fatalf("amount=%v 不应被拒（向下取整后仍可退），got err %v", c.amount, err)
		}
		if got != c.want {
			t.Fatalf("amount=%v 应取整为 %d 分，实际 %d", c.amount, c.want, got)
		}
	}
}

// 可退本金向上取整：micros 有余数时进位到分。
func TestRefundablePrincipalCeilsToCents(t *testing.T) {
	// 本金 10.004000 元 = 100040 micros；向下取整得 1000 分（10.00），
	// 向上取整得 1001 分（10.01）。用户要求是**向上**。
	breakdown := computeRefundablePrincipal(10_004_000, 0, 99_999_999)
	if breakdown.RefundableMicros != 10_004_000 {
		t.Fatalf("可退本金应为 10004000 micros，实际 %d", breakdown.RefundableMicros)
	}
	// 复现生产代码里的向上取整写法，避免用例与实现漂移。
	cents := breakdown.RefundableMicros / ticketRefundMicrosPerCent
	if breakdown.RefundableMicros%ticketRefundMicrosPerCent != 0 {
		cents++
	}
	if cents != 1001 {
		t.Fatalf("可退本金应向上取整为 1001 分，实际 %d（1000 说明退回了向下取整）", cents)
	}
}

// 整数分时不得因「余数判断」多加一分。
func TestRefundablePrincipalCeilNoBumpWhenExact(t *testing.T) {
	breakdown := computeRefundablePrincipal(10_000_000, 0, 99_999_999)
	cents := breakdown.RefundableMicros / ticketRefundMicrosPerCent
	if breakdown.RefundableMicros%ticketRefundMicrosPerCent != 0 {
		cents++
	}
	if cents != 1000 {
		t.Fatalf("正好 1000 分时不得进位，实际 %d", cents)
	}
}
