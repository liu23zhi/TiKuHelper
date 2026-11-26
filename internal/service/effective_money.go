package service

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// The original DECIMAL(18,6) correction/audit domain is unchanged. BIGINT
// fits its 18 digits. Lifetime sums use big.Int and may exceed a single row.
const maxEffectiveMicros int64 = 999999999999999999

var ErrLedgerRange = errors.New("金额超出原账务字段 DECIMAL(18,6) 的范围")
var ErrLedgerPrecision = errors.New("历史金额的存储精度不足，无法精确重评")

// MariaDB returns DECIMAL as bytes, SQLite NUMERIC affinity may yield INTEGER
// or REAL. Preserve exact integers and decimal strings. A previously rounded
// SQLite REAL cannot attest a decimal unit smaller than its own spacing.
type storedUnits struct {
	n     int64
	known bool
}
type microColumn struct{ storedUnits }
type octoColumn struct{ storedUnits }

func (c *microColumn) Scan(v any) error            { return c.storedUnits.scan(v, 6) }
func (c *octoColumn) Scan(v any) error             { return c.storedUnits.scan(v, 8) }
func (c microColumn) Value() (driver.Value, error) { return fixedInt(c.n, 6), nil }
func (c octoColumn) Value() (driver.Value, error)  { return fixedInt(c.n, 8), nil }
func (c *storedUnits) scan(v any, places int) error {
	*c = storedUnits{}
	if v == nil {
		return nil
	}
	var s string
	switch x := v.(type) {
	case []byte:
		s = string(x)
	case string:
		s = x
	case int64:
		s = strconv.FormatInt(x, 10)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(math.Nextafter(x, math.Inf(1))-x) >= 1/math.Pow10(places) {
			return nil
		}
		s = strconv.FormatFloat(x, 'f', places, 64)
	default:
		return fmt.Errorf("unsupported decimal storage %T", v)
	}
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > places {
		if strings.Trim(frac[places:], "0") != "" {
			return nil
		}
		frac = frac[:places]
	}
	n, err := strconv.ParseInt(whole+frac+strings.Repeat("0", places-len(frac)), 10, 64)
	if err != nil {
		return ErrLedgerRange
	}
	c.n = n
	c.known = true
	return nil
}
func fixedInt(n int64, places int) string { return fixedBig(big.NewInt(n), places) }
func fixedBig(n *big.Int, places int) string {
	sign := ""
	s := n.String()
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	if len(s) <= places {
		s = strings.Repeat("0", places+1-len(s)) + s
	}
	i := len(s) - places
	return sign + s[:i] + "." + s[i:]
}
func fixedFloat(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

type ledgerExactTotals struct {
	Recharge                   string `json:"recharge"`
	Consumption                string `json:"consumption"`
	HistoricalRecharge         string `json:"historicalRecharge"`
	HistoricalConsumption      string `json:"historicalConsumption"`
	ConfirmedUnallocatedRefund string `json:"confirmedUnallocatedRefund"`
	RefundEstimateUpper        string `json:"refundEstimateUpper"`
}
type ledgerExactSource struct {
	Gross               string `json:"gross"`
	Debited             string `json:"debited,omitempty"`
	Refunded            string `json:"refunded"`
	RefundEstimateUpper string `json:"refundEstimateUpper"`
	Net                 string `json:"net"`
	Effective           string `json:"effective"`
}

func (p *effectiveProjection) publish() {
	net := new(big.Int)
	for _, s := range p.orders {
		p.apply(s)
		net.Add(net, big.NewInt(s.effectiveMicros))
	}
	net.Sub(net, &p.unallocatedMicros)
	net.Sub(net, &p.estimatedUnallocatedMicros)
	if net.Sign() < 0 {
		net.SetInt64(0)
	}
	charges := new(big.Int).Set(&p.confirmedChargeMicros)
	for k, d := range p.discounts {
		if k.kind == "bill" {
			charges.Sub(charges, big.NewInt(d))
		}
	}
	if charges.Sign() < 0 {
		charges.SetInt64(0)
	}
	hist := new(big.Int).Add(&p.historicalChargeOctos, big.NewInt(50))
	hist.Quo(hist, big.NewInt(100))
	x := ledgerExactTotals{Recharge: fixedBig(net, 6), Consumption: fixedBig(charges, 6), HistoricalRecharge: fixedBig(&p.grossMicros, 6), HistoricalConsumption: fixedBig(hist, 6), ConfirmedUnallocatedRefund: fixedBig(&p.unallocatedMicros, 6), RefundEstimateUpper: fixedBig(&p.estimateUpperMicros, 6)}
	p.Totals.Exact = x
	p.Totals.RechargeTotal = fixedFloat(x.Recharge)
	p.Totals.ConsumptionTotal = fixedFloat(x.Consumption)
	p.Totals.HistoricalRecharge = fixedFloat(x.HistoricalRecharge)
	p.Totals.HistoricalConsumption = fixedFloat(x.HistoricalConsumption)
	p.Totals.ConfirmedUnallocatedRefund = fixedFloat(x.ConfirmedUnallocatedRefund)
	p.Totals.RefundEstimateUpper = fixedFloat(x.RefundEstimateUpper)
}
