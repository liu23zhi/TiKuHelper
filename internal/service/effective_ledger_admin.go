package service

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type EffectiveCorrection struct {
	SourceKind string `json:"sourceKind"`
	SourceID   int32  `json:"sourceId"`
	Expected   string `json:"expected"`
	New        string `json:"new"`
	Revision   int64  `json:"revision"`
	Reason     string `json:"reason"`
}
type EffectiveReview struct {
	Totals   UserLevelTotals              `json:"totals"`
	Sources  []effectiveSource            `json:"sources"`
	Audit    []entity.EffectiveAdjustment `json:"audit"`
	Revision int64                        `json:"revision"`
	NextBill int32                        `json:"nextBill"`
}

var ledgerDecimal = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,6})?$`)

func ledgerMicros(v string) (int64, error) {
	if !ledgerDecimal.MatchString(v) {
		return 0, ErrLedgerInput
	}
	a, b, _ := strings.Cut(v, ".")
	return strconv.ParseInt(a+b+strings.Repeat("0", 6-len(b)), 10, 64)
}
func (p *effectiveProjection) revision() int64 {
	if len(p.audits) == 0 {
		return 0
	}
	return p.audits[len(p.audits)-1].ID
}
func checkEffectiveTarget(tx *gorm.DB, actor entity.User, uid int32, lock bool) error {
	if _, err := ticketRefundActor(tx, actor.ID, true, false); err != nil {
		return err
	}
	q := tx.Model(&entity.User{}).Select("id").Where("id = ?", uid)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var target entity.User
	return q.Take(&target).Error
}
func ReviewEffectiveLedger(db *gorm.DB, actor entity.User, uid, beforeBill int32) (EffectiveReview, error) {
	var result EffectiveReview
	if db == nil || uid <= 0 || beforeBill < 0 {
		return result, ErrLedgerInput
	}
	err := userLevelSession(db).Transaction(func(tx *gorm.DB) error {
		if err := checkEffectiveTarget(tx, actor, uid, false); err != nil {
			return err
		}
		p, err := replayEffectiveLedger(tx, uid)
		if err != nil {
			return err
		}
		result.Totals = p.Totals
		result.Revision = p.revision()
		for _, s := range p.orders {
			result.Sources = append(result.Sources, *s)
		}
		sort.Slice(result.Sources, func(i, j int) bool { return result.Sources[i].ID > result.Sources[j].ID })
		for i := len(p.audits) - 1; i >= 0; i-- {
			result.Audit = append(result.Audit, p.audits[i])
		}
		if !tx.Migrator().HasTable(&entity.BillingRecord{}) {
			return nil
		}
		q := tx.Model(&entity.BillingRecord{}).Select("id,amount").Where("user_id = ? AND amount > 0 AND TRIM(type) IN ? AND ("+chargeOriginSQL+")=0", uid, []string{normalSearchBillingRecordType, ScriptActivationPurchaseBillingType})
		if beforeBill > 0 {
			q = q.Where("id < ?", beforeBill)
		}
		var bs []entity.BillingRecord
		const page = 30
		if err := q.Order("id DESC").Limit(page + 1).Find(&bs).Error; err != nil {
			return err
		}
		if len(bs) > page {
			bs = bs[:page]
			result.NextBill = bs[page-1].ID
		}
		for _, b := range bs {
			s := effectiveSource{Kind: "bill", ID: b.ID, Gross: b.Amount}
			p.apply(&s)
			result.Sources = append(result.Sources, s)
		}
		return nil
	})
	return result, err
}

func CorrectEffectiveLedger(db *gorm.DB, actor entity.User, uid int32, input EffectiveCorrection) (entity.EffectiveAdjustment, error) {
	var a entity.EffectiveAdjustment
	old, err := ledgerMicros(input.Expected)
	if err != nil {
		return a, ErrLedgerInput
	}
	next, err := ledgerMicros(input.New)
	if err != nil {
		return a, ErrLedgerInput
	}
	reason := strings.TrimSpace(input.Reason)
	if db == nil || uid <= 0 || input.SourceID <= 0 || old == next || reason == "" || utf8.RuneCountInString(reason) > 255 {
		return a, ErrLedgerInput
	}
	err = ticketRefundTransaction(db, func(tx *gorm.DB) error {
		if err := checkEffectiveTarget(tx, actor, uid, true); err != nil {
			return err
		}
		p, err := replayEffectiveLedger(tx, uid)
		if err != nil {
			return err
		}
		if p.revision() != input.Revision {
			return ErrLedgerStale
		}
		src, err := p.source(tx, uid, input.SourceKind, input.SourceID)
		if err != nil {
			return err
		}
		current := p.Totals.ConsumptionTotal
		if src.Kind == "order" {
			current = p.Totals.RechargeTotal
			if p.unallocatedMicros != 0 || src.Uncertain || p.Totals.Issues["refund_without_exact_order_allocation"] > 0 {
				return ErrLedgerInput
			}
		}
		if yuanToMicros(current) != old {
			return ErrLedgerStale
		}
		contribution := yuanToMicros(src.Effective) + next - old
		net := yuanToMicros(src.Net)
		if contribution < 0 || contribution > net {
			return ErrLedgerInput
		}
		a = entity.EffectiveAdjustment{UserID: uid, OperatorID: actor.ID, SourceKind: src.Kind, SourceID: src.ID, Reason: reason, OldMicros: old, NewMicros: next, DiscountMicros: net - contribution, RefundIDs: linkedRefundIDs(src)}
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
		_, err = EvaluateUserLevelWithDB(tx, uid, entity.UserLevelTriggerManual)
		return err
	})
	return a, err
}
