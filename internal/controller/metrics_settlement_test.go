package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/diagnostics"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestMetricsSettlementCountsBoundariesWithoutChangingMoney(t *testing.T) {
	for _, kind := range []string{"two-record-success", "second-record-error", "key-limit", "panic", "empty"} {
		t.Run(kind, func(t *testing.T) {
			limit := 0.0
			if kind == "key-limit" {
				limit = .1
			}
			db, u, k := integrationQuotaFixture(t, limit, 0, 1)
			c := diagnostics.NewCollector(nil)
			old := diagnostics.Default
			diagnostics.Default = c
			t.Cleanup(func() { diagnostics.Default = old })
			charges := []searchBillingCharge{{Provider: "fixture", Amount: .1, Snapshot: "question=SYNTHETIC_PRIVATE_QUESTION"}, {Provider: "fixture", Amount: .2}}
			if kind == "empty" {
				charges = nil
			}
			creates := 0
			if kind == "second-record-error" || kind == "panic" {
				if err := db.Callback().Create().Before("gorm:create").Register("b04_fixture", func(tx *gorm.DB) {
					if tx.Statement.Table != "billing_records" {
						return
					}
					creates++
					if kind == "panic" {
						panic("SYNTHETIC_SETTLEMENT_PANIC")
					}
					if creates == 2 {
						tx.AddError(errors.New("synthetic second row failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			panicked := false
			func() {
				defer func() {
					if v := recover(); v != nil {
						if v != "SYNTHETIC_SETTLEMENT_PANIC" {
							panic(v)
						}
						panicked = true
					}
				}()
				err = persistSearchBillingRecordsWithDB(db, searchBillingContext{UserID: u.ID, AccessTokenID: k.ID, RequestID: "SYNTHETIC_DIAGNOSTICS_REQUEST", Billable: true}, charges)
			}()
			if kind == "two-record-success" {
				if err != nil {
					t.Fatal(err)
				}
				walletSettlementAssertState(t, db, u.ID, k.ID, 2, .7, .3)
			} else {
				integrationNoFinancialChange(t, db, u, k)
				if (kind == "second-record-error" || kind == "key-limit") && err == nil {
					t.Fatal("financial failure swallowed")
				}
			}
			if panicked != (kind == "panic") {
				t.Fatalf("panic propagation changed: %t", panicked)
			}
			s := c.Snapshot().Billing
			wantAttempts, wantSuccess, wantFailure := uint64(1), uint64(0), uint64(1)
			if kind == "two-record-success" {
				wantSuccess, wantFailure = 1, 0
			}
			if kind == "empty" {
				wantAttempts, wantSuccess, wantFailure = 0, 0, 0
			}
			if s.Attempts != wantAttempts || s.Successes != wantSuccess || s.Failures != wantFailure {
				t.Fatalf("BILLING_EVENT_NOT_LEDGER_ROW_COUNT: kind=%s %+v", kind, s)
			}
			var rows int64
			db.Model(&entity.BillingRecord{}).Count(&rows)
			if strings.Contains(c.Snapshot().StartedAt, "SYNTHETIC") {
				t.Fatal("private value retained")
			}
			t.Logf("B04_BILLING kind=%s attempts=%d successes=%d failures=%d rows=%d", kind, s.Attempts, s.Successes, s.Failures, rows)
		})
	}
}
