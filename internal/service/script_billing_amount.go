package service

import (
	"math"
	"math/big"
	"strconv"
)

// ScriptChargeMicros applies a six-decimal administrator multiplier to a
// standard tariff, then rounds the resulting line once at the money boundary.
// The shortest decimal encoding preserves the normal price's representation
// without an intermediate binary product.
func ScriptChargeMicros(amount float64, multiplierMicros int64) (int64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount >= 1e12 ||
		multiplierMicros <= 0 || multiplierMicros > maxScriptEscrowMicros {
		return 0, ErrScriptEscrowInvalid
	}
	n, ok := new(big.Rat).SetString(strconv.FormatFloat(amount, 'g', -1, 64))
	if !ok {
		return 0, ErrScriptEscrowInvalid
	}
	n.Mul(n, big.NewRat(multiplierMicros, 1))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n.Num(), n.Denom(), rem)
	if rem.Lsh(rem, 1).Cmp(n.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Int64() > maxScriptEscrowMicros {
		return 0, ErrScriptEscrowInvalid
	}
	return q.Int64(), nil
}
