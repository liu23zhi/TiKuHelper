package service

import (
	"math/big"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type effectiveBillRow struct {
	ID, UserID                int32
	Kind                      string
	Amount, Gift, Principal   octoColumn
	WalletBefore, WalletAfter microColumn
	Diagnostic, Escrow        bool
}

// Read native numeric values (MySQL DECIMAL bytes or SQLite integer/real)
// directly. Do not let a DB SUM cast or an ORM float turn micro-units into
// binary approximations. No text, addresses or callback payloads leave SQL.
func chargeProjection(db *gorm.DB) *gorm.DB {
	gift, principal := "0", "0"
	if db.Migrator().HasColumn(&entity.BillingRecord{}, "gift_charged") && db.Migrator().HasColumn(&entity.BillingRecord{}, "principal_charged") {
		gift, principal = "gift_charged", "principal_charged"
	}
	return db.Model(&entity.BillingRecord{}).Select("id,user_id,TRIM(type) AS kind,amount," + gift + " AS gift," + principal + " AS principal,balance_before AS wallet_before,balance_after AS wallet_after," + chargeOriginSQL + " AS diagnostic,CASE WHEN access_token_id>0 AND INSTR(unit_price_snapshot,' script_mode=escrow script_multiplier=')>0 THEN 1 ELSE 0 END AS escrow").Where("amount>0")
}
func billProjectionQuery(db *gorm.DB, uid int32) *gorm.DB {
	return chargeProjection(db).Where("user_id = ? AND TRIM(type) IN ? AND ("+chargeOriginSQL+")=0", uid, []string{normalSearchBillingRecordType, ScriptActivationPurchaseBillingType})
}
func (b effectiveBillRow) debit() (int64, bool) {
	if !b.Amount.known || !b.Gift.known || !b.Principal.known || b.Gift.n < 0 || b.Principal.n < 0 {
		return 0, false
	}
	var d int64
	switch {
	case b.Escrow:
		d = (b.Amount.n + 50) / 100
	case b.Gift.n > 0 || b.Principal.n > 0:
		d = (b.Gift.n + b.Principal.n + 50) / 100
	case b.WalletBefore.known && b.WalletAfter.known && b.WalletBefore.n > b.WalletAfter.n:
		d = b.WalletBefore.n - b.WalletAfter.n
	default:
		return 0, false
	}
	if d > b.Amount.n/100+3 {
		return 0, false
	}
	delta := d*100 - b.Amount.n
	return d, delta >= -201 && delta <= 201
}
func (p *effectiveProjection) billFromRow(b effectiveBillRow) *effectiveSource {
	d, ok := b.debit()
	s := &effectiveSource{Kind: "bill", ID: b.ID, Uncertain: !ok, Excluded: !ok}
	if b.Amount.known {
		s.Exact.Gross = fixedInt(b.Amount.n, 8)
	}
	if ok {
		s.debitedMicros = d
		s.Exact.Debited = fixedInt(d, 6)
		v := fixedFloat(s.Exact.Debited)
		s.Debited = &v
	}
	p.apply(s)
	return s
}
func (p *effectiveProjection) billSource(db *gorm.DB, uid, id int32) (*effectiveSource, error) {
	var row effectiveBillRow
	if err := billProjectionQuery(db, uid).Where("id = ?", id).Take(&row).Error; err != nil {
		return nil, err
	}
	s := p.billFromRow(row)
	if s.Uncertain {
		return nil, ErrLedgerUncertain
	}
	return s, nil
}
func (batch ledgerBatch) readCharges(db *gorm.DB, ids []int32) error {
	if db.Dialector.Name() == "mysql" {
		return batch.readMariaCharges(db, ids)
	}
	q := chargeProjection(db).Where("user_id IN ?", ids)
	rows, e := q.Rows()
	if e != nil {
		return e
	}
	defer rows.Close()
	var n big.Int
	for rows.Next() {
		var b effectiveBillRow
		if err := rows.Scan(&b.ID, &b.UserID, &b.Kind, &b.Amount, &b.Gift, &b.Principal, &b.WalletBefore, &b.WalletAfter, &b.Diagnostic, &b.Escrow); err != nil {
			return err
		}
		p := batch[b.UserID]
		if !confirmedCharge(b.Kind, false) && b.Kind != scriptReservationBillingRecordType {
			if !knownNonCharge(b.Kind) {
				p.issue("unclassified_billing_type:"+b.Kind, 1)
			}
			continue
		}
		if !b.Amount.known {
			p.issue("stored_billing_precision_unknown", 1)
			continue
		}
		p.historicalChargeOctos.Add(&p.historicalChargeOctos, n.SetInt64(b.Amount.n))
		if confirmedCharge(b.Kind, b.Diagnostic) {
			if debit, ok := b.debit(); ok {
				p.confirmedChargeMicros.Add(&p.confirmedChargeMicros, n.SetInt64(debit))
			} else {
				p.issue("charge_without_exact_debit", 1)
			}
		}
	}
	return rows.Err()
}

// MariaDB has native fixed DECIMAL arithmetic: aggregate in SQL, bind integer
// unit sums as text, and parse with big.Int. Hundreds of thousands of bill
// details need not traverse the DB connection to render one summary. SQLite
// uses the streaming path because SUM(REAL) and SUM(INTEGER) can lose precision
// or overflow, and an old REAL may not retain its original granularity.
func (batch ledgerBatch) readMariaCharges(db *gorm.DB, ids []int32) error {
	split := ""
	if db.Migrator().HasColumn(&entity.BillingRecord{}, "gift_charged") && db.Migrator().HasColumn(&entity.BillingRecord{}, "principal_charged") {
		split = " WHEN gift_charged < 0 OR principal_charged < 0 THEN NULL WHEN gift_charged+principal_charged>0 THEN ROUND(gift_charged+principal_charged,6)"
	}
	d := "(CASE WHEN access_token_id>0 AND INSTR(unit_price_snapshot,' script_mode=escrow script_multiplier=')>0 THEN ROUND(amount,6)" + split + " WHEN balance_before>balance_after THEN ROUND(balance_before-balance_after,6) ELSE NULL END)"
	proven := "(CASE WHEN ABS(" + d + "-amount)<=0.00000201 THEN " + d + " ELSE NULL END)"
	var groups []struct {
		UserID                      int32
		Kind                        string
		Diagnostic                  bool
		N, Proven                   int64
		NominalOctos, ChargedMicros string
	}
	e := db.Model(&entity.BillingRecord{}).Select("user_id,TRIM(type) AS kind,"+chargeOriginSQL+" AS diagnostic,COUNT(*) AS n,COUNT("+proven+") AS proven,CAST(SUM(ROUND(amount*100000000,0)) AS CHAR) AS nominal_octos,CAST(COALESCE(SUM(ROUND("+proven+"*1000000,0)),0) AS CHAR) AS charged_micros").Where("user_id IN ? AND amount>0", ids).Group("user_id,TRIM(type)," + chargeOriginSQL).Scan(&groups).Error
	if e != nil {
		return e
	}
	for _, g := range groups {
		p := batch[g.UserID]
		if !confirmedCharge(g.Kind, false) && g.Kind != scriptReservationBillingRecordType {
			if !knownNonCharge(g.Kind) {
				p.issue("unclassified_billing_type:"+g.Kind, g.N)
			}
			continue
		}
		nominal, ok := new(big.Int).SetString(g.NominalOctos, 10)
		if !ok {
			return ErrLedgerPrecision
		}
		p.historicalChargeOctos.Add(&p.historicalChargeOctos, nominal)
		if confirmedCharge(g.Kind, g.Diagnostic) {
			debit, ok := new(big.Int).SetString(g.ChargedMicros, 10)
			if !ok {
				return ErrLedgerPrecision
			}
			p.confirmedChargeMicros.Add(&p.confirmedChargeMicros, debit)
			p.issue("charge_without_exact_debit", g.N-g.Proven)
		}
	}
	return nil
}
func knownNonCharge(k string) bool {
	if k == AdminGrantBillingRecordType || k == AdminGrantReversalBillingRecordType || k == "test" {
		return true
	}
	for _, t := range nonConsumptionBillingRecordTypes {
		if t == k {
			return true
		}
	}
	return false
}
