package util

import "math"

// MoneyPrecision 是余额与账务金额统一保留的小数位数。
//
// 之所以用 6 位而不是 2 位：AI 计费按 token 单价折算后单次金额常常远小于 1 分
// （例如 GPT-6 Luna 一次调用约 0.000062 元），2 位精度会把它抹成 0。
const MoneyPrecision = 6

// RechargePrecision 是充值金额允许的小数位数。
// 第三方支付渠道只支持「分」，因此充值这一环节限制为 2 位小数；
// 余额与账单仍然使用 MoneyPrecision（6 位）。
const RechargePrecision = 2

const moneyScale = 1e6

// RoundMoney 把金额按 MoneyPrecision 四舍五入（负数按远离零方向进位）。
// float64 本身没有十进制精度，所有金额在入账前都必须过一遍这个函数，
// 否则多次累加会出现 0.30000000000000004 这类尾差。
func RoundMoney(amount float64) float64 {
	return RoundMoneyToDecimals(amount, MoneyPrecision)
}

// RoundMoneyToDecimals 把金额按指定小数位四舍五入。
// decimals 超出 [0, 8] 时按边界处理，避免 1e9 级别的放大把值弄成 Inf。
func RoundMoneyToDecimals(amount float64, decimals int) float64 {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0
	}
	if decimals <= 0 {
		return math.Round(amount)
	}
	if decimals > 8 {
		decimals = 8
	}
	scale := math.Pow(10, float64(decimals))
	return math.Round(amount*scale) / scale
}

// HasMoreDecimalsThan 判断金额是否超出了指定小数位数（用于拒绝非法输入）。
// 采用 1e-9 容差，避免 float64 表示误差把合法的 12.34 误判为超精度。
func HasMoreDecimalsThan(amount float64, decimals int) bool {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return true
	}
	if decimals < 0 {
		decimals = 0
	}
	scale := math.Pow(10, float64(decimals))
	return math.Abs(amount*scale-math.Round(amount*scale)) > 1e-9
}
