package service

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type UserLevelTotals struct {
	Exact                      ledgerExactTotals `json:"exact"`
	ConfirmedUnallocatedRefund float64           `json:"confirmedUnallocatedRefund,omitempty"`
	RefundEstimateUpper        float64           `json:"refundEstimateUpper,omitempty"`
	RechargeTotal              float64           `json:"rechargeTotal"`
	ConsumptionTotal           float64           `json:"consumptionTotal"`
	HistoricalRecharge         float64           `json:"historicalRecharge"`
	HistoricalConsumption      float64           `json:"historicalConsumption"`
	// When nonempty, summaries may include only confirmed amounts or explicitly bounded estimates; entitlements stay unchanged.
	Issues map[string]int64 `json:"issues,omitempty"`
}

type effectiveSource struct {
	Excluded                                                                             bool              `json:"excluded,omitempty"` // bill was omitted from recognized consumption
	Exact                                                                                ledgerExactSource `json:"exact"`
	grossMicros, debitedMicros, refundMicros, estimateMicros, netMicros, effectiveMicros int64
	RefundEstimateUpper                                                                  float64  `json:"refundEstimateUpper,omitempty"`
	Debited                                                                              *float64 `json:"debited,omitempty"`
	Uncertain                                                                            bool     `json:"uncertain,omitempty"`
	Kind                                                                                 string   `json:"kind"`
	ID                                                                                   int32    `json:"id"`
	Gross                                                                                float64  `json:"gross"`
	Refunded                                                                             float64  `json:"refunded"`
	RefundIDs                                                                            []int64  `json:"refundIds,omitempty"`
	Net                                                                                  float64  `json:"net"`
	Effective                                                                            float64  `json:"effective"`
}
type ledgerKey struct {
	kind string
	id   int32
}
type effectiveProjection struct {
	grossMicros, historicalChargeOctos, confirmedChargeMicros          big.Int
	unallocatedMicros, estimatedUnallocatedMicros, estimateUpperMicros big.Int
	Totals                                                             UserLevelTotals
	orders                                                             map[int32]*effectiveSource
	discounts                                                          map[ledgerKey]int64
	audits                                                             []entity.EffectiveAdjustment
}

func (p *effectiveProjection) issue(k string, n int64) {
	if n > 0 {
		p.Totals.Issues[k] += n
	}
}
func (p *effectiveProjection) apply(s *effectiveSource) {
	s.netMicros = max(0, s.grossMicros-s.refundMicros-s.estimateMicros)
	if s.Kind == "bill" {
		s.netMicros = s.debitedMicros
	}
	s.effectiveMicros = max(0, s.netMicros-p.discounts[ledgerKey{s.Kind, s.ID}])
	s.Exact.Refunded = fixedInt(s.refundMicros, 6)
	s.Exact.RefundEstimateUpper = fixedInt(s.estimateMicros, 6)
	s.Exact.Net = fixedInt(s.netMicros, 6)
	s.Exact.Effective = fixedInt(s.effectiveMicros, 6)
	s.Gross = fixedFloat(s.Exact.Gross)
	s.Refunded = fixedFloat(s.Exact.Refunded)
	s.RefundEstimateUpper = fixedFloat(s.Exact.RefundEstimateUpper)
	s.Net = fixedFloat(s.Exact.Net)
	s.Effective = fixedFloat(s.Exact.Effective)

}

func replayEffectiveLedger(db *gorm.DB, uid int32) (effectiveProjection, error) {
	if uid <= 0 || db == nil {
		return effectiveProjection{}, ErrLedgerInput
	}
	batch, err := replayEffectiveBatch(db, []int32{uid})
	if err != nil {
		return effectiveProjection{}, err
	}
	return *batch[uid], nil
}

// Both interactive and batched readers use this projection. One SQL aggregate
// per source per page rather than scanning each user's entire bill history
// or issuing schema probes for each row of a large user export.
type ledgerBatch map[int32]*effectiveProjection

func replayEffectiveBatch(db *gorm.DB, ids []int32) (ledgerBatch, error) {
	batch := ledgerBatch{}
	for _, id := range ids {
		batch[id] = &effectiveProjection{Totals: UserLevelTotals{Issues: map[string]int64{}}, orders: map[int32]*effectiveSource{}, discounts: map[ledgerKey]int64{}}
	}
	if len(ids) == 0 {
		return batch, nil
	}
	db = userLevelSession(db)
	if db.Migrator().HasTable(&entity.EffectiveAdjustment{}) {
		var all []entity.EffectiveAdjustment
		if err := db.Where("user_id IN ?", ids).Order("id asc").Find(&all).Error; err != nil {
			return nil, err
		}
		for _, a := range all {
			var err error
			a.OldExact, err = exactAuditTotal(a.OldMicros)
			if err != nil {
				return nil, err
			}
			a.NewExact, err = exactAuditTotal(a.NewMicros)
			if err != nil {
				return nil, err
			}
			if a.DiscountMicros < 0 || a.DiscountMicros > maxEffectiveMicros {
				return nil, ErrLedgerRange
			}
			p := batch[a.UserID]
			p.audits = append(p.audits, a)
			p.discounts[ledgerKey{a.SourceKind, a.SourceID}] = a.DiscountMicros
		}
	}

	bills := db.Migrator().HasTable(&entity.BillingRecord{})
	if db.Migrator().HasTable(&entity.RechargeOrder{}) {
		evidence := "1=0"
		if bills {
			evidence = entity.RechargeCreditEvidenceSQL
		}
		var rows []struct {
			ID, UserID int32
			Amount     microColumn
			Status     string
			Credited   bool
		}
		if err := db.Table("recharge_orders").Select("id,user_id,amount,status, ("+evidence+") AS credited").Where("user_id IN ? AND (status IN ? OR paid_at IS NOT NULL OR "+evidence+")", ids, []string{"paid", "refunded"}).Order("id").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			p := batch[r.UserID]
			if !r.Amount.known {
				p.issue("stored_recharge_precision_unknown", 1)
				continue
			}
			if !r.Credited || r.Amount.n <= 0 {
				p.issue("order_without_credit_proof", 1)
				continue
			}
			s := &effectiveSource{Kind: "order", ID: r.ID, grossMicros: r.Amount.n, Exact: ledgerExactSource{Gross: fixedInt(r.Amount.n, 6)}}
			p.orders[r.ID] = s
			p.grossMicros.Add(&p.grossMicros, big.NewInt(r.Amount.n))
			// Operator display status alone is neither a payment nor a payout receipt.
			if r.Status == "refunded" {
				p.issue("operator_refund_mark", 1)
			}
		}
		if bills {
			var orphan []struct {
				UserID int32
				N      int64
			}
			if err := db.Table("billing_records br").Select("br.user_id, COUNT(*) AS n").Where("br.user_id IN ? AND br.type = 'recharge' AND NOT EXISTS (SELECT 1 FROM recharge_orders o WHERE o.id=br.related_order_id AND o.user_id=br.user_id AND o.order_no=br.request_id AND o.amount=br.amount)", ids).Group("br.user_id").Scan(&orphan).Error; err != nil {
				return nil, err
			}
			for _, r := range orphan {
				batch[r.UserID].issue("unmatched_recharge_credit", r.N)
			}
		}
	} else {
		for _, p := range batch {
			p.issue("order_table_unavailable", 1)
		}
	}
	if err := batch.applyRefunds(db, ids); err != nil {
		return nil, err
	}
	if !bills {
		for _, p := range batch {
			p.issue("billing_table_unavailable", 1)
		}
	}
	if bills {
		if err := batch.readCharges(db, ids); err != nil {
			return nil, err
		}
	}
	for _, p := range batch {
		p.publish()
	}
	return batch, nil
}
func (batch ledgerBatch) applyRefunds(db *gorm.DB, ids []int32) error {
	if !db.Migrator().HasTable(&entity.TicketRefund{}) {
		for _, p := range batch {
			p.issue("refund_table_unavailable", 1)
		}
		return nil
	}

	var rs []entity.TicketRefund
	if err := db.Select("id,user_id,order_id,status,approved_cents").Where("user_id IN ?", ids).Order("id").Find(&rs).Error; err != nil {
		return err
	}
	if len(rs) == 0 {
		return nil
	}
	var ls []entity.TicketRefundOrderLine
	lines := map[int64][]entity.TicketRefundOrderLine{}
	if db.Migrator().HasTable(&entity.TicketRefundOrderLine{}) {
		if err := db.Model(&entity.TicketRefundOrderLine{}).Select("ticket_refund_order_lines.refund_id,ticket_refund_order_lines.order_id,ticket_refund_order_lines.cents,ticket_refund_order_lines.status").Joins("JOIN ticket_refunds r ON r.id=ticket_refund_order_lines.refund_id").Where("r.user_id IN ?", ids).Order("ticket_refund_order_lines.id").Scan(&ls).Error; err != nil {
			return err
		}
		for _, l := range ls {
			lines[l.RefundID] = append(lines[l.RefundID], l)
		}
	}
	consumed := map[int64]entity.TicketRefundLedger{}
	holds := map[int64]entity.TicketRefundLedger{}
	if db.Migrator().HasTable(&entity.TicketRefundLedger{}) {
		var rows []entity.TicketRefundLedger
		if err := db.Model(&entity.TicketRefundLedger{}).Select("ticket_refund_ledger.user_id,ticket_refund_ledger.refund_id,ticket_refund_ledger.amount_micros,ticket_refund_ledger.kind").Joins("JOIN ticket_refunds r ON r.id=ticket_refund_ledger.refund_id").Where("r.user_id IN ? AND ticket_refund_ledger.kind IN ?", ids, []string{ticketRefundLedgerConsume, ticketRefundLedgerFreeze}).Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			if r.Kind == ticketRefundLedgerConsume {
				consumed[r.RefundID] = r
			} else {
				holds[r.RefundID] = r
			}
		}
	}
	for _, r := range rs {
		if err := batch[r.UserID].projectRefund(r, lines[r.ID], consumed[r.ID], holds[r.ID]); err != nil {
			return err
		}
	}
	return nil
}

// Only a settled consume receipt establishes the terminal principal. Before
// terminal settlement, a successful line and a full-size immutable hold can
// establish its exact share. Missing or disagreeing facts are never reported
// as an observed per-order debit (nor made editable).
func (p *effectiveProjection) projectRefund(r entity.TicketRefund, lines []entity.TicketRefundOrderLine, receipt, hold entity.TicketRefundLedger) error {
	terminal := r.Status == entity.TicketRefundStatusSucceeded || r.Status == entity.TicketRefundStatusPartial
	if r.ApprovedCents < 0 || r.ApprovedCents > maxEffectiveMicros/ticketRefundMicrosPerCent || receipt.AmountMicros > maxEffectiveMicros {
		return ErrLedgerRange
	}
	approved := r.ApprovedCents * ticketRefundMicrosPerCent
	linked := map[int32]bool{}
	if r.OrderID != nil {
		linked[*r.OrderID] = true
	}
	parts := map[int32]int64{}
	var planned int64
	failures := 0
	other := 0
	for _, l := range lines {
		linked[l.OrderID] = true
		if l.Cents < 0 || l.Cents > maxEffectiveMicros/ticketRefundMicrosPerCent {
			return ErrLedgerRange
		}
		switch l.Status {
		case entity.TicketRefundStatusSucceeded:
			v := l.Cents * ticketRefundMicrosPerCent
			if v > 0 {
				parts[l.OrderID] += v
				planned += v
				if planned > maxEffectiveMicros {
					return ErrLedgerRange
				}
			}
		case entity.TicketRefundStatusFailed:
			failures++
		default:
			other++
		}
	}
	if len(lines) == 0 && r.Status == entity.TicketRefundStatusSucceeded && r.OrderID != nil {
		planned = approved
		parts[*r.OrderID] = planned
	}
	markUnknown := func() {
		for id := range linked {
			if s := p.orders[id]; s != nil {
				s.Uncertain = true
				s.RefundIDs = append(s.RefundIDs, r.ID)
			}
		}
	}
	hasReceipt := receipt.RefundID != 0
	if hasReceipt && (receipt.UserID != r.UserID || receipt.AmountMicros <= 0) {
		p.issue("refund_wrong_owner_or_amount", 1)
		markUnknown()
		return nil
	}
	if !terminal && r.Status != entity.TicketRefundStatusProcessing && r.Status != entity.TicketRefundStatusUnknown {
		if planned > 0 || hasReceipt {
			p.issue("refund_state_conflict", 1)
			markUnknown()
			if hasReceipt {
				p.unallocatedMicros.Add(&p.unallocatedMicros, big.NewInt(receipt.AmountMicros))
			}
		}
		return nil
	}
	conflict := len(lines) > 0 && ((r.Status == entity.TicketRefundStatusSucceeded && (failures > 0 || other > 0)) || (r.Status == entity.TicketRefundStatusPartial && (planned == 0 || failures == 0 || other > 0)))
	if conflict || (!terminal && hasReceipt) {
		p.issue("refund_state_conflict", 1)
		markUnknown()
		if hasReceipt {
			p.unallocatedMicros.Add(&p.unallocatedMicros, big.NewInt(receipt.AmountMicros))
		} else if terminal {
			p.estimatedUnallocatedMicros.Add(&p.estimatedUnallocatedMicros, big.NewInt(approved))
			p.estimateUpperMicros.Add(&p.estimateUpperMicros, big.NewInt(approved))
		}
		return nil
	}
	if !terminal && planned == 0 {
		return nil
	}
	exact := hasReceipt && terminal || !terminal && planned > 0 && hold.UserID == r.UserID && hold.AmountMicros == approved
	amount := planned
	if hasReceipt && terminal {
		amount = receipt.AmountMicros
	}
	if !exact {
		p.issue("refund_principal_without_exact_receipt", 1)
	}
	if planned <= 0 || amount > planned || planned-amount >= ticketRefundMicrosPerCent || (len(parts) > 1 && amount != planned) {
		p.issue("refund_without_exact_order_allocation", 1)
		markUnknown()
		if hasReceipt && terminal {
			p.unallocatedMicros.Add(&p.unallocatedMicros, big.NewInt(amount))
		} else if terminal {
			p.estimatedUnallocatedMicros.Add(&p.estimatedUnallocatedMicros, big.NewInt(approved))
			p.estimateUpperMicros.Add(&p.estimateUpperMicros, big.NewInt(approved))
		}
		return nil
	}
	for id, part := range parts {
		v := min(part, amount)
		s := p.orders[id]
		if s == nil {
			p.issue("refund_without_credited_order", 1)
			continue
		}
		s.RefundIDs = append(s.RefundIDs, r.ID)
		if exact {
			if s.refundMicros > maxEffectiveMicros-v {
				return ErrLedgerRange
			}
			s.refundMicros += v
		} else {
			s.Uncertain = true
			if s.estimateMicros > maxEffectiveMicros-v {
				return ErrLedgerRange
			}
			s.estimateMicros += v
			p.estimateUpperMicros.Add(&p.estimateUpperMicros, big.NewInt(v))
		}
		if s.refundMicros > s.grossMicros {
			p.issue("refund_exceeds_order_principal", 1)
		}
	}
	return nil
}

// No arbitrary provider, remark, or signed amount can turn an unknown bill into
// a confirmed expense. Diagnostic requests originate in the no-token test path.
const chargeOriginSQL = "CASE WHEN access_token_id = 0 AND request_id LIKE 'tool-test-%' THEN 1 ELSE 0 END"

func confirmedCharge(kind string, diag bool) bool {
	return !diag && (kind == normalSearchBillingRecordType || kind == ScriptActivationPurchaseBillingType)
}

var ErrLedgerInput = errors.New("金额、原因或来源不符合要求")
var ErrLedgerStale = errors.New("有效金额已变化，请重新读取后修正")

func (p *effectiveProjection) source(db *gorm.DB, uid int32, kind string, id int32) (*effectiveSource, error) {
	if kind == "order" {
		if s := p.orders[id]; s != nil {
			return s, nil
		}
	}
	if kind == "bill" {
		return p.billSource(db, uid, id)
	}
	return nil, ErrLedgerInput
}
func linkedRefundIDs(s *effectiveSource) string {
	xs := make([]string, 0, len(s.RefundIDs))
	for _, id := range s.RefundIDs {
		xs = append(xs, fmt.Sprint(id))
	}
	return strings.Join(xs, ",")
}

// RequiresReview identifies conflicts in existing facts, not a legitimate zero.
// A confirmed but unallocated manual payout has an exact account total, though
// it cannot be corrected per order until attribution is resolved.
func (t UserLevelTotals) RequiresReview() bool { return len(t.Issues) > 0 }
