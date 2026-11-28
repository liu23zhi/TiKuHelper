package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
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

var ErrLedgerUncertain = errors.New("账务存在待核事实，不能修正累计金额")
var ledgerDecimal = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,6})?$`)

func ledgerMicros(v string) (int64, error) {
	if !ledgerDecimal.MatchString(v) {
		return 0, ErrLedgerInput
	}
	a, b, _ := strings.Cut(v, ".")
	n, err := strconv.ParseInt(a+b+strings.Repeat("0", 6-len(b)), 10, 64)
	if err != nil || n > maxEffectiveMicros {
		return 0, ErrLedgerRange
	}
	return n, nil
}

// Lifetime account totals use the same width as the derived DECIMAL(38,6)
// history snapshots; validation of each selected source remains DECIMAL(18,6).
var accountDecimal = regexp.MustCompile(`^[0-9]{1,32}(\.[0-9]{1,6})?$`)
var auditUnits = regexp.MustCompile(`^(0|[1-9][0-9]{0,37})$`)

func accountMicros(v string) (*big.Int, error) {
	if !accountDecimal.MatchString(v) {
		return nil, ErrLedgerInput
	}
	whole, frac, _ := strings.Cut(v, ".")
	n, ok := new(big.Int).SetString(whole+frac+strings.Repeat("0", 6-len(frac)), 10)
	if !ok {
		return nil, ErrLedgerInput
	}
	return n, nil
}
func exactAuditTotal(raw json.Number) (string, error) {
	if !auditUnits.MatchString(raw.String()) {
		return "", ErrLedgerRange
	}
	n, ok := new(big.Int).SetString(raw.String(), 10)
	if !ok {
		return "", ErrLedgerRange
	}
	return fixedBig(n, 6), nil
}

func (p *effectiveProjection) revision() int64 {
	if len(p.audits) == 0 {
		return 0
	}
	return p.audits[len(p.audits)-1].ID
}

// Lock only the identity, not wallet eligibility: disabled or archived owners
// may still legitimately have a pending payout finalized or released.
func lockEffectiveOwner(tx *gorm.DB, uid int32) error {
	var row struct{ ID int32 }
	return tx.Table(entity.TableNameUser).Select("id").Where("id = ?", uid).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
}
func checkEffectiveTarget(tx *gorm.DB, actor entity.User, uid int32, lock bool) error {
	if _, err := ticketRefundActor(tx, actor.ID, true, lock); err != nil {
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
	// Totals, audit and the selected bill page must come from one snapshot,
	// even when MariaDB's session default is READ COMMITTED.
	var opts *sql.TxOptions
	if db.Dialector.Name() == "mysql" {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
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
			a := p.audits[i]
			result.Audit = append(result.Audit, a)
		}
		if !tx.Migrator().HasTable(&entity.BillingRecord{}) {
			return nil
		}
		q := billProjectionQuery(tx, uid)
		if beforeBill > 0 {
			q = q.Where("id < ?", beforeBill)
		}
		var bs []effectiveBillRow
		const page = 30
		if err := q.Order("id DESC").Limit(page + 1).Find(&bs).Error; err != nil {
			return err
		}
		if len(bs) > page {
			bs = bs[:page]
			result.NextBill = bs[page-1].ID
		}
		for _, b := range bs {
			result.Sources = append(result.Sources, *p.billFromRow(b))
		}
		return nil
	}, opts)
	return result, err
}

func CorrectEffectiveLedger(db *gorm.DB, actor entity.User, uid int32, input EffectiveCorrection) (entity.EffectiveAdjustment, error) {
	var a entity.EffectiveAdjustment
	old, err := accountMicros(input.Expected)
	if err != nil {
		return a, err
	}
	next, err := accountMicros(input.New)
	if err != nil {
		return a, err
	}
	reason := strings.TrimSpace(input.Reason)
	if db == nil || uid <= 0 || input.SourceID <= 0 || old.Cmp(next) == 0 || reason == "" || utf8.RuneCountInString(reason) > 255 {
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
		if p.Totals.RequiresReview() {
			return ErrLedgerUncertain
		}
		if p.revision() != input.Revision {
			return ErrLedgerStale
		}
		src, err := p.source(tx, uid, input.SourceKind, input.SourceID)
		if err != nil {
			return err
		}
		current := p.Totals.Exact.Consumption
		if src.Kind == "order" {
			current = p.Totals.Exact.Recharge
			if p.unallocatedMicros.Sign() != 0 || src.Uncertain || p.Totals.Issues["refund_without_exact_order_allocation"] > 0 {
				return ErrLedgerInput
			}
		}
		if current != fixedBig(old, 6) {
			return ErrLedgerStale
		}
		// CAS the whole account but apply only its signed delta to this source.
		// No cast to int64 until the proposed contribution is within the source's net.
		contribution := new(big.Int).Sub(next, old)
		contribution.Add(contribution, big.NewInt(src.effectiveMicros))
		net := src.netMicros
		if net < 0 || net > maxEffectiveMicros || src.grossMicros > maxEffectiveMicros {
			return ErrLedgerRange
		}
		if contribution.Sign() < 0 || contribution.Cmp(big.NewInt(net)) > 0 {
			return ErrLedgerInput
		}
		a = entity.EffectiveAdjustment{UserID: uid, OperatorID: actor.ID, SourceKind: src.Kind, SourceID: src.ID, Reason: reason, OldMicros: json.Number(old.String()), NewMicros: json.Number(next.String()), OldExact: fixedBig(old, 6), NewExact: fixedBig(next, 6), DiscountMicros: net - contribution.Int64(), RefundIDs: linkedRefundIDs(src)}
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
		_, err = EvaluateUserLevelWithDB(tx, uid, entity.UserLevelTriggerManual)
		return err
	})
	return a, err
}
