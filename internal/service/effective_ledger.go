package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

type UserLevelTotals struct {
	RechargeTotal         float64 `json:"rechargeTotal"`
	ConsumptionTotal      float64 `json:"consumptionTotal"`
	HistoricalRecharge    float64 `json:"historicalRecharge"`
	HistoricalConsumption float64 `json:"historicalConsumption"`
	// When nonempty, amounts are confirmed subtotals, not guessed totals.
	Issues map[string]int64 `json:"issues,omitempty"`
}

type effectiveSource struct {
	Uncertain bool    `json:"uncertain,omitempty"`
	Kind      string  `json:"kind"`
	ID        int32   `json:"id"`
	Gross     float64 `json:"gross"`
	Refunded  float64 `json:"refunded"`
	RefundIDs []int64 `json:"refundIds,omitempty"`
	Net       float64 `json:"net"`
	Effective float64 `json:"effective"`
}
type ledgerKey struct {
	kind string
	id   int32
}
type effectiveProjection struct {
	unallocatedMicros int64 // confirmed principal without a defensible per-order split
	Totals            UserLevelTotals
	orders            map[int32]*effectiveSource
	discounts         map[ledgerKey]int64
	audits            []entity.EffectiveAdjustment
}

func (p *effectiveProjection) issue(k string, n int64) {
	if n > 0 {
		p.Totals.Issues[k] += n
	}
}
func (p *effectiveProjection) apply(s *effectiveSource) {
	s.Net = util.RoundMoney(max(0, s.Gross-s.Refunded))
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
			p.Totals.HistoricalRecharge += r.Amount
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
		p.Totals.RechargeTotal = float64(max(0, net-p.unallocatedMicros)) / 1e6
		p.Totals.HistoricalRecharge = util.RoundMoney(p.Totals.HistoricalRecharge)
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
	if db.Migrator().HasTable(&entity.TicketRefundLedger{}) {
		var rows []entity.TicketRefundLedger
		if err := db.Model(&entity.TicketRefundLedger{}).Select("ticket_refund_ledger.user_id,ticket_refund_ledger.refund_id,ticket_refund_ledger.amount_micros").Joins("JOIN ticket_refunds r ON r.id=ticket_refund_ledger.refund_id").Where("r.user_id IN ? AND ticket_refund_ledger.kind = ?", ids, ticketRefundLedgerConsume).Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			consumed[r.RefundID] = r
		}
	}
	for _, r := range rs {
		p := batch[r.UserID]
		terminal := r.Status == entity.TicketRefundStatusSucceeded || r.Status == entity.TicketRefundStatusPartial
		parts := map[int32]int64{}
		var planned int64
		for _, l := range lines[r.ID] {
			if l.Status == entity.TicketRefundStatusSucceeded && l.Cents > 0 {
				v := l.Cents * ticketRefundMicrosPerCent
				parts[l.OrderID] += v
				planned += v
			}
		}
		if len(parts) == 0 && r.Status == entity.TicketRefundStatusSucceeded && r.OrderID != nil {
			planned = r.ApprovedCents * ticketRefundMicrosPerCent
			parts[*r.OrderID] = planned
		}
		receipt, ok := consumed[r.ID]
		if !terminal {
			if r.Status != entity.TicketRefundStatusProcessing && r.Status != entity.TicketRefundStatusUnknown {
				if planned > 0 || ok {
					p.issue("refund_state_conflict", 1)
				}
				continue
			}
			if ok {
				p.issue("refund_state_conflict", 1)
			}
			if planned == 0 {
				continue
			}
		}
		if ok && receipt.UserID != r.UserID {
			p.issue("refund_wrong_owner_receipt", 1)
			continue
		}
		amount := receipt.AmountMicros
		if !ok || !terminal {
			amount = planned
			if terminal {
				p.issue("settled_refund_without_consume_receipt", 1)
			}
		}
		if amount <= 0 || planned <= 0 || amount > planned || planned-amount >= ticketRefundMicrosPerCent || (len(parts) > 1 && amount != planned) {
			if ok && terminal && amount > 0 {
				p.unallocatedMicros += amount
			}
			p.issue("refund_without_exact_order_allocation", 1)
			continue
		}
		ids := make([]int, 0, len(parts))
		for id := range parts {
			ids = append(ids, int(id))
		}
		sort.Ints(ids)
		for _, i := range ids {
			id := int32(i)
			v := min(parts[id], amount)
			amount -= v
			s := p.orders[id]
			if s == nil {
				p.issue("refund_without_credited_order", 1)
				continue
			}

			if terminal && !ok {
				s.Uncertain = true
			} // Approved cents bound the actual micro debit but cannot establish it.
			s.Refunded = util.RoundMoney(s.Refunded + float64(v)/1e6)
			s.RefundIDs = append(s.RefundIDs, r.ID)
			if s.Refunded > s.Gross {
				p.issue("refund_exceeds_order_principal", 1)
			}
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
func (batch ledgerBatch) readCharges(db *gorm.DB, ids []int32) error {
	var groups []struct {
		UserID     int32
		Kind       string
		Diagnostic int
		N          int64
		Amount     float64
	}
	if err := db.Model(&entity.BillingRecord{}).Select("user_id, TRIM(type) AS kind,"+chargeOriginSQL+" AS diagnostic, COUNT(*) AS n, SUM(amount) AS amount").Where("user_id IN ? AND amount > 0", ids).Group("user_id, TRIM(type)," + chargeOriginSQL).Scan(&groups).Error; err != nil {
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
			p.Totals.HistoricalConsumption += g.Amount
			if confirmedCharge(g.Kind, g.Diagnostic != 0) {
				p.Totals.ConsumptionTotal += g.Amount
			}
		case g.Kind == scriptReservationBillingRecordType:
			p.Totals.HistoricalConsumption += g.Amount
		case !known[g.Kind]:
			p.issue("unclassified_billing_type:"+g.Kind, g.N)
		}
	}
	for _, p := range batch {
		for k, d := range p.discounts {
			if k.kind == "bill" && d > 0 {
				p.Totals.ConsumptionTotal -= float64(d) / 1e6
			}
		}
		p.Totals.ConsumptionTotal = util.RoundMoney(max(0, p.Totals.ConsumptionTotal))
		p.Totals.HistoricalConsumption = util.RoundMoney(p.Totals.HistoricalConsumption)
	}
	return nil
}

func (p *effectiveProjection) billSource(db *gorm.DB, uid, id int32) (*effectiveSource, error) {
	var b entity.BillingRecord
	if err := db.Select("id,type,amount,access_token_id,request_id").Where("id = ? AND user_id = ?", id, uid).Take(&b).Error; err != nil {
		return nil, err
	}
	if b.Amount <= 0 || !confirmedCharge(strings.TrimSpace(b.Type), b.AccessTokenID == 0 && strings.HasPrefix(b.RequestID, "tool-test-")) {
		return nil, ErrLedgerInput
	}
	s := &effectiveSource{Kind: "bill", ID: b.ID, Gross: b.Amount}
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
func (t UserLevelTotals) RequiresReview() bool {
	for k := range t.Issues {
		switch k {
		case "order_table_unavailable", "refund_table_unavailable", "billing_table_unavailable":
			continue
		default:
			return true
		}
	}
	return false
}
