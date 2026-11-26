package service

import (
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func readEffectiveLevelDefinitions(q *gorm.DB) ([]entity.UserLevel, error) {
	var rows []struct {
		entity.UserLevel
		RechargeThreshold    microColumn
		ConsumptionThreshold microColumn
	}
	if e := q.Select("user_levels.*,min_recharge_total AS recharge_threshold,min_consumption_total AS consumption_threshold").Find(&rows).Error; e != nil {
		return nil, e
	}
	levels := make([]entity.UserLevel, 0, len(rows))
	for _, r := range rows {
		if !r.RechargeThreshold.known || !r.ConsumptionThreshold.known {
			return nil, ErrLedgerPrecision
		}
		r.MinRechargeExact = fixedInt(r.RechargeThreshold.n, 6)
		r.MinConsumptionExact = fixedInt(r.ConsumptionThreshold.n, 6)
		levels = append(levels, r.UserLevel)
	}
	SortUserLevelsByRank(levels)
	return levels, nil
}

// All inputs are nonnegative canonical fixed-six decimals; length then text
// compares even lifetime totals larger than the original per-source DECIMAL.
func atLeastFixed(a, b string) bool {
	if strings.HasPrefix(b, "-") {
		return true
	}
	return len(a) > len(b) || (len(a) == len(b) && a >= b)
}
func resolveEffectiveLevel(levels []entity.UserLevel, totals UserLevelTotals) (entity.UserLevel, bool) {
	var best entity.UserLevel
	ok := false
	for _, l := range levels {
		if l.Enabled && l.Rank >= userLevelMinRank && atLeastFixed(totals.Exact.Recharge, l.MinRechargeExact) && atLeastFixed(totals.Exact.Consumption, l.MinConsumptionExact) && (!ok || l.Rank > best.Rank) {
			best = l
			ok = true
		}
	}
	return best, ok
}

// Decimal strings are bound directly into MariaDB's original DECIMAL fields.
// Text companions preserve the exact new decision on SQLite, whose NUMERIC
// affinity may otherwise store it as REAL. Legacy rows stay unasserted.
type effectiveHistoryWrite struct {
	ID                                                                                          int32 `gorm:"primaryKey"`
	UserID, FromLevelID, ToLevelID                                                              int32
	Trigger, EventKey, RechargeTotal, ConsumptionTotal, RechargeExact, ConsumptionExact, Remark string
	CreatedAt                                                                                   time.Time
}
