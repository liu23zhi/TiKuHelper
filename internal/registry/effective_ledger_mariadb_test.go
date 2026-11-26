package registry

import (
	"database/sql"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	gl "gorm.io/gorm/logger"
)

// An opt-in socket-only harness for an independently restored, disposable
// MariaDB clone. Never handles an archive, host DSN, host TCP address, external
// service, or prints an account, secret, row, or per-user balance.
func openT14Clone(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("T14_CLONE_ACK") != "isolated-disposable-copy" {
		t.Skip("requires explicit isolated clone")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("run inside standalone isolated clone container")
	}
	nics, err := net.Interfaces()
	if err != nil {
		t.Fatal("cannot inspect isolation")
	}
	for _, i := range nics {
		if i.Flags&net.FlagLoopback == 0 {
			t.Fatal("requires --network none")
		}
	}
	name, socket := os.Getenv("T14_CLONE_DB"), os.Getenv("T14_CLONE_SOCKET")
	if !filepath.IsAbs(socket) || name == "" {
		t.Fatal("set clone database and absolute clone socket explicitly")
	}
	cfg := driver.NewConfig()
	cfg.DBName = name
	cfg.Net = "unix"
	cfg.Addr = socket
	cfg.User = os.Getenv("T14_CLONE_USER")
	if cfg.User == "" {
		cfg.User = "root"
	}
	cfg.Passwd = os.Getenv("T14_CLONE_PASSWORD")
	if cfg.Passwd == "" {
		cfg.Passwd = os.Getenv("MARIADB_ROOT_PASSWORD")
	}
	if cfg.Passwd == "" {
		cfg.Passwd = os.Getenv("MYSQL_ROOT_PASSWORD")
	}
	cfg.ParseTime = true
	db, err := gorm.Open(openMySQLDialector(cfg.FormatDSN()), &gorm.Config{Logger: gl.Default.LogMode(gl.Silent)})
	if err != nil {
		t.Fatal("cannot open isolated socket (credentials not logged)")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal("cannot obtain clone handle")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var dbName, events string
	if err := db.Raw("SELECT DATABASE()").Scan(&dbName).Error; err != nil || dbName != name {
		t.Fatal("schema mismatch")
	}
	if err := db.Raw("SELECT @@event_scheduler").Scan(&events).Error; err != nil || (!strings.EqualFold(events, "OFF") && !strings.EqualFold(events, "DISABLED")) {
		t.Fatal("event scheduler must be disabled")
	}
	return db
}

type t14CloneSummary struct {
	Users                                   int
	RechargeMicros, ConsumptionMicros       int64
	WarningUsers, DebitUnknown, UnknownKind int64
}

func t14CloneProjection(t *testing.T, db *gorm.DB) t14CloneSummary {
	t.Helper()
	var out t14CloneSummary
	err := db.Transaction(func(tx *gorm.DB) error {
		var ids []int32
		if err := tx.Table("users").Order("id").Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			s, e := service.ComputeUserLevelTotalsWithDB(tx, id)
			if e != nil {
				return e
			}
			out.Users++
			if len(s.Issues) > 0 {
				out.WarningUsers++
			}
			out.RechargeMicros += int64(math.Round(s.RechargeTotal * 1e6))
			out.ConsumptionMicros += int64(math.Round(s.ConsumptionTotal * 1e6))
			out.DebitUnknown += s.Issues["charge_without_exact_debit"]
			for k, n := range s.Issues {
				if strings.HasPrefix(k, "unclassified_billing_type:") {
					out.UnknownKind += n
				}
			}
		}
		// Independent oracle for THIS 23-table snapshot: no ledger refunds,
		// no split/escrow purchases; all service charges are ordinary searches.
		var principal, debit int64
		if e := tx.Raw("SELECT COALESCE(SUM(ROUND(amount*1000000,0)),0) FROM recharge_orders WHERE status='paid' AND " + entity.RechargeCreditEvidenceSQL).Scan(&principal).Error; e != nil {
			return e
		}
		if e := tx.Raw("SELECT COALESCE(SUM(ROUND((balance_before-balance_after)*1000000,0)),0) FROM billing_records WHERE type='normal_search' AND amount>0 AND balance_before>balance_after AND ABS(ROUND(balance_before-balance_after,6)-amount)<=0.00000201 AND NOT (COALESCE(access_token_id,-1)=0 AND COALESCE(request_id,'') LIKE 'tool-test-%')").Scan(&debit).Error; e != nil {
			return e
		}
		if principal != out.RechargeMicros || debit != out.ConsumptionMicros {
			return errors.New("aggregate oracle mismatch")
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("aggregate query / independent-oracle check failed (%T); individual values suppressed", err)
	}
	return out
}
func TestEffectiveLedgerRestoredMariaDB(t *testing.T) {
	db := openT14Clone(t)
	var n int64
	for tab, want := range map[string]int64{"users": 941, "billing_records": 61034, "recharge_orders": 455} {
		if err := db.Table(tab).Count(&n).Error; err != nil || n != want {
			t.Fatal("not the authorized 2026-11-27 restored snapshot")
		}
	}
	// A parent row with a genuine payout or an existing correction changes the
	// oracle; never discard it or reset the database to make a comparison pass.
	for _, m := range []any{&entity.TicketRefund{}, &entity.EffectiveAdjustment{}} {
		if db.Migrator().HasTable(m) {
			if err := db.Model(m).Count(&n).Error; err != nil || n != 0 {
				t.Fatal("clone contains later financial events")
			}
		}
	}
	before := t14CloneProjection(t, db)
	t.Logf("BEFORE users=%d warning_users=%d missing_debit=%d unknown_billing_rows=%d aggregate_matches_independent_oracle=true", before.Users, before.WarningUsers, before.DebitUnknown, before.UnknownKind)
	if os.Getenv("T14_APPLY_SCHEMA") != "approved-clone-ddl" {
		return
	}
	// Only DDL on the opt-in copy. No app startup, default user import,
	// registration-level backfill, corrections or production credentials.
	if err := ensureBillingWalletSplitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.TicketRefund{}, &entity.TicketRefundOrderLine{}, &entity.TicketRefundLedger{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureEffectiveAuditSchema(db); err != nil {
		t.Fatal(err)
	}
	after := t14CloneProjection(t, db)
	if before.Users != after.Users || before.RechargeMicros != after.RechargeMicros || before.ConsumptionMicros != after.ConsumptionMicros || before.UnknownKind != after.UnknownKind || before.DebitUnknown != after.DebitUnknown {
		t.Fatal("structural migration changed a financial fact")
	}
	t.Logf("AFTER users=%d warning_users=%d missing_debit=%d unknown_billing_rows=%d aggregate_matches_independent_oracle=true amounts_unchanged=true", after.Users, after.WarningUsers, after.DebitUnknown, after.UnknownKind)
}
