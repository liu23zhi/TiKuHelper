package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// Ledger UI and accounting corrections expose six decimals. Within this
// supported range all integer micros (and sum intermediates in 1e-8 units)
// are exactly representable, even through SQLite's REAL aggregation.
const maxEffectiveYuan = 10_000_000
const maxEffectiveMicros int64 = maxEffectiveYuan * 1_000_000

var ErrLedgerRange = errors.New("有效账务支持 0 至 10000000 元、六位小数；超出范围不能精确核对或重评")

type UserLevelTotals struct {
	ConfirmedUnallocatedRefund float64 `json:"confirmedUnallocatedRefund,omitempty"`
	RefundEstimateUpper        float64 `json:"refundEstimateUpper,omitempty"`
	RechargeTotal              float64 `json:"rechargeTotal"`
	ConsumptionTotal           float64 `json:"consumptionTotal"`
	HistoricalRecharge         float64 `json:"historicalRecharge"`
	HistoricalConsumption      float64 `json:"historicalConsumption"`
	// When nonempty, summaries may include only confirmed amounts or explicitly bounded estimates; entitlements stay unchanged.
	Issues map[string]int64 `json:"issues,omitempty"`
}

type effectiveSource struct {
	RefundEstimateUpper float64  `json:"refundEstimateUpper,omitempty"`
	Debited             *float64 `json:"debited,omitempty"`
	Uncertain           bool     `json:"uncertain,omitempty"`
	Kind                string   `json:"kind"`
	ID                  int32    `json:"id"`
	Gross               float64  `json:"gross"`
	Refunded            float64  `json:"refunded"`
	RefundIDs           []int64  `json:"refundIds,omitempty"`
	Net                 float64  `json:"net"`
	Effective           float64  `json:"effective"`
}
type ledgerKey struct {
	kind string
	id   int32
}
type effectiveProjection struct {
	grossMicros, historicalChargeOctos, confirmedChargeMicros int64
	estimatedUnallocatedMicros                                int64
	unallocatedMicros                                         int64 // confirmed principal without a defensible per-order split
	Totals                                                    UserLevelTotals
	orders                                                    map[int32]*effectiveSource
	discounts                                                 map[ledgerKey]int64
	audits                                                    []entity.EffectiveAdjustment
}

func (p *effectiveProjection) issue(k string, n int64) {
	if n > 0 {
		p.Totals.Issues[k] += n
	}
}
func (p *effectiveProjection) apply(s *effectiveSource) {
	s.Net = util.RoundMoney(max(0, s.Gross-s.Refunded-s.RefundEstimateUpper))
	if s.Kind == "bill" {
		if s.Debited == nil {
			s.Net = 0
			s.Uncertain = true
		} else {
			s.Net = *s.Debited
		}
	}
	s.Effective = util.RoundMoney(max(0, s.Net-float64(p.discounts[ledgerKey{s.Kind, s.ID}])/1e6))
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
			if a.OldMicros < 0 || a.NewMicros < 0 || a.DiscountMicros < 0 || max(a.OldMicros, a.NewMicros, a.DiscountMicros) > maxEffectiveMicros {
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
			Amount     float64
			Status     string
			Credited   bool
		}
		if err := db.Table("recharge_orders").Select("id,user_id,amount,status, ("+evidence+") AS credited").Where("user_id IN ? AND (status IN ? OR paid_at IS NOT NULL OR "+evidence+")", ids, []string{"paid", "refunded"}).Order("id").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			p := batch[r.UserID]
			if !r.Credited || r.Amount <= 0 {
				p.issue("order_without_credit_proof", 1)
				continue
			}
			s := &effectiveSource{Kind: "order", ID: r.ID, Gross: r.Amount}
			p.orders[r.ID] = s
			if r.Amount > maxEffectiveYuan {
				return nil, ErrLedgerRange
			}
			p.grossMicros += yuanToMicros(r.Amount)
			if p.grossMicros > maxEffectiveMicros {
				return nil, ErrLedgerRange
			}
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
	for _, p := range batch {
		var net int64
		for _, s := range p.orders {
			p.apply(s)
			net += yuanToMicros(s.Effective)
		}
		p.Totals.ConfirmedUnallocatedRefund = float64(p.unallocatedMicros) / 1e6
		p.Totals.RechargeTotal = float64(max(0, net-p.unallocatedMicros-p.estimatedUnallocatedMicros)) / 1e6
		p.Totals.HistoricalRecharge = float64(p.grossMicros) / 1e6
		if !bills {
			p.issue("billing_table_unavailable", 1)
		}
	}
	if bills {
		if err := batch.readCharges(db, ids); err != nil {
			return nil, err
		}
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
				p.unallocatedMicros += receipt.AmountMicros
			}
		}
		return p.checkRefundBounds()
	}
	conflict := len(lines) > 0 && ((r.Status == entity.TicketRefundStatusSucceeded && (failures > 0 || other > 0)) || (r.Status == entity.TicketRefundStatusPartial && (planned == 0 || failures == 0 || other > 0)))
	if conflict || (!terminal && hasReceipt) {
		p.issue("refund_state_conflict", 1)
		markUnknown()
		if hasReceipt {
			p.unallocatedMicros += receipt.AmountMicros
		} else if terminal {
			p.estimatedUnallocatedMicros += approved
			p.Totals.RefundEstimateUpper += float64(approved) / 1e6
		}
		return p.checkRefundBounds()
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
			p.unallocatedMicros += amount
		} else if terminal {
			p.estimatedUnallocatedMicros += approved
			p.Totals.RefundEstimateUpper += float64(approved) / 1e6
		}
		return p.checkRefundBounds()
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
			s.Refunded = util.RoundMoney(s.Refunded + float64(v)/1e6)
		} else {
			s.Uncertain = true
			s.RefundEstimateUpper = util.RoundMoney(s.RefundEstimateUpper + float64(v)/1e6)
			p.Totals.RefundEstimateUpper = util.RoundMoney(p.Totals.RefundEstimateUpper + float64(v)/1e6)
		}
		if s.Refunded > s.Gross {
			p.issue("refund_exceeds_order_principal", 1)
		}
	}
	return p.checkRefundBounds()
}
func (p *effectiveProjection) checkRefundBounds() error {
	if p.unallocatedMicros > maxEffectiveMicros || p.estimatedUnallocatedMicros > maxEffectiveMicros || p.Totals.RefundEstimateUpper > maxEffectiveYuan {
		return ErrLedgerRange
	}
	return nil
}

// No arbitrary provider, remark, or signed amount can turn an unknown bill into
// a confirmed expense. Diagnostic requests originate in the no-token test path.
const chargeOriginSQL = "CASE WHEN access_token_id = 0 AND request_id LIKE 'tool-test-%' THEN 1 ELSE 0 END"

func confirmedCharge(kind string, diag bool) bool {
	return !diag && (kind == normalSearchBillingRecordType || kind == ScriptActivationPurchaseBillingType)
}

// Amount is the eight-decimal quote. Wallet bucket deltas are the six-decimal
// posted charge. In legacy wallets the principal before/after delta is usable
// if it corroborates the billed amount. Escrow settlements commit the priced
// per-line amount in the same transaction, even when no wallet bucket moves.
func effectiveDebitSQL(db *gorm.DB) string {
	split := ""
	if db.Migrator().HasColumn(&entity.BillingRecord{}, "gift_charged") && db.Migrator().HasColumn(&entity.BillingRecord{}, "principal_charged") {
		split = " WHEN gift_charged < 0 OR principal_charged < 0 THEN NULL WHEN gift_charged + principal_charged > 0 THEN ROUND(gift_charged + principal_charged,6)"
	}
	debit := "(CASE WHEN access_token_id > 0 AND INSTR(unit_price_snapshot,' script_mode=escrow script_multiplier=') > 0 THEN ROUND(amount,6)" + split + " WHEN balance_before > balance_after THEN ROUND(balance_before-balance_after,6) ELSE NULL END)"
	// Each of two buckets rounds its own before/after boundary. A larger
	// discrepancy is not attributable to rounding; flag it rather than guess.
	return "(CASE WHEN ABS(" + debit + " - amount) <= 0.00000201 THEN " + debit + " ELSE NULL END)"
}

type effectiveBillRow struct {
	ID      int32
	Amount  float64
	Debited *float64
}

func billProjectionQuery(db *gorm.DB, uid int32) *gorm.DB {
	return db.Model(&entity.BillingRecord{}).Select("id,amount,"+effectiveDebitSQL(db)+" AS debited").Where("user_id = ? AND amount > 0 AND TRIM(type) IN ? AND ("+chargeOriginSQL+")=0", uid, []string{normalSearchBillingRecordType, ScriptActivationPurchaseBillingType})
}

func (batch ledgerBatch) readCharges(db *gorm.DB, ids []int32) error {
	var groups []struct {
		UserID        int32
		Kind          string
		Diagnostic    int
		N             int64
		Octos, Micros float64
		Proven        int64
	}
	debit := effectiveDebitSQL(db)
	if err := db.Model(&entity.BillingRecord{}).Select("user_id, TRIM(type) AS kind,"+chargeOriginSQL+" AS diagnostic, COUNT(*) AS n, SUM(ROUND(amount*100000000,0)) AS octos, COALESCE(SUM(ROUND(("+debit+")*1000000,0)),0) AS micros,COUNT("+debit+") AS proven").Where("user_id IN ? AND amount > 0", ids).Group("user_id, TRIM(type)," + chargeOriginSQL).Scan(&groups).Error; err != nil {
		return err
	}
	known := map[string]bool{AdminGrantBillingRecordType: true, AdminGrantReversalBillingRecordType: true, "test": true}
	for _, t := range nonConsumptionBillingRecordTypes {
		known[t] = true
	}
	for _, g := range groups {
		p := batch[g.UserID]
		switch {
		case confirmedCharge(g.Kind, false):
			if g.Octos > maxEffectiveYuan*1e8 {
				return ErrLedgerRange
			}
			p.historicalChargeOctos += int64(g.Octos)
			if confirmedCharge(g.Kind, g.Diagnostic != 0) {
				if g.Micros > float64(maxEffectiveMicros) {
					return ErrLedgerRange
				}
				p.confirmedChargeMicros += int64(g.Micros)
				p.issue("charge_without_exact_debit", g.N-g.Proven)
			}
		case g.Kind == scriptReservationBillingRecordType:
			if g.Octos > maxEffectiveYuan*1e8 {
				return ErrLedgerRange
			}
			p.historicalChargeOctos += int64(g.Octos)
		case !known[g.Kind]:
			p.issue("unclassified_billing_type:"+g.Kind, g.N)
		}
	}
	for _, p := range batch {
		for k, d := range p.discounts {
			if k.kind == "bill" && d > 0 {
				p.confirmedChargeMicros -= d
			}
		}
		if p.historicalChargeOctos > maxEffectiveYuan*100000000 || p.confirmedChargeMicros > maxEffectiveMicros {
			return ErrLedgerRange
		}
		p.Totals.ConsumptionTotal = float64(max(int64(0), p.confirmedChargeMicros)) / 1e6
		p.Totals.HistoricalConsumption = float64((p.historicalChargeOctos+50)/100) / 1e6
	}
	return nil
}

func (p *effectiveProjection) billSource(db *gorm.DB, uid, id int32) (*effectiveSource, error) {
	var b effectiveBillRow
	if err := billProjectionQuery(db, uid).Where("id = ?", id).Take(&b).Error; err != nil {
		return nil, err
	}
	if b.Debited == nil {
		return nil, ErrLedgerInput
	}
	s := &effectiveSource{Kind: "bill", ID: b.ID, Gross: b.Amount, Debited: b.Debited}
	p.apply(s)
	return s, nil
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
