package registry

import (
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
			r, e := strconv.ParseInt(strings.Replace(s.Exact.Recharge, ".", "", 1), 10, 64)
			if e != nil {
				return e
			}
			c, e := strconv.ParseInt(strings.Replace(s.Exact.Consumption, ".", "", 1), 10, 64)
			if e != nil {
				return e
			}
			out.RechargeMicros += r
			out.ConsumptionMicros += c
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

// Two independent connections. A writer owns the account lock and its bill is
// uncommitted while the verifier reads its authenticated actor. Release the
// writer only when the verifier reaches the cohort lock: no scheduler sleeps.
func TestReviewRebuildMariaDBInterleaving(t *testing.T) {
	if os.Getenv("T14_SYNTHETIC_RACE") != "empty-schema-only" {
		t.Skip("requires empty synthetic DB, never a restore")
	}
	if !strings.HasPrefix(os.Getenv("T14_CLONE_DB"), "t14_synthetic_") {
		t.Fatal("not a synthetic schema")
	}
	db := openT14Clone(t)
	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&n).Error; err != nil || n != 0 {
		t.Fatal("requires an empty synthetic database")
	}
	verifyT14HistoryMigration(t, db)
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{}, &entity.UserLevelHistory{}, &entity.RechargeOrder{}, &entity.BillingRecord{}, &entity.TicketRefund{}, &entity.TicketRefundOrderLine{}, &entity.TicketRefundLedger{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureUserLevelIDColumn(db); err != nil {
		t.Fatal(err)
	}
	if err := ensureEffectiveAuditSchema(db); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{ID: 1, Username: "synthetic-admin", Role: "admin", Status: "enabled"}
	owner := entity.User{ID: 2, Username: "synthetic-owner", Role: "user", Status: "enabled"}
	if err := db.Create(&[]entity.User{admin, owner}).Error; err != nil {
		t.Fatal(err)
	}
	levels := []entity.UserLevel{{ID: 1, Name: "basic", Rank: 1, Enabled: true, DeviceLimit: 1}, {ID: 2, Name: "paid", Rank: 2, Enabled: true, MinConsumptionTotal: 5, DeviceLimit: 3}}
	if err := db.Create(&levels).Error; err != nil {
		t.Fatal(err)
	}
	in := service.EffectiveRebuildInput{AfterID: 1, Limit: 1}
	preview, err := service.RebuildEffectiveLevels(db, admin, in)
	if err != nil || preview.Changed != 1 {
		t.Fatalf("preview %+v %v", preview, err)
	}
	writer := db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	if e := writer.Exec("UPDATE users SET balance=balance+0 WHERE id=2").Error; e != nil {
		t.Fatal(e)
	}
	if e := writer.Create(&entity.BillingRecord{UserID: 2, Type: "normal_search", Amount: 6, GiftCharged: 6, RequestID: "synthetic-commit"}).Error; e != nil {
		t.Fatal(e)
	}
	barrier := make(chan struct{})
	var once sync.Once
	if e := db.Callback().Query().Before("gorm:query").Register("t14:cohort_lock", func(q *gorm.DB) {
		if q.Statement.Table == "users" && strings.Contains(strings.Join(q.Statement.Selects, ","), "level_id") {
			if _, ok := q.Statement.Clauses["FOR"]; ok {
				once.Do(func() { close(barrier) })
			}
		}
	}); e != nil {
		t.Fatal(e)
	}
	in.Reason = "合成重评"
	in.ApplyDigest = preview.Digest
	done := make(chan error, 1)
	go func() { _, e := service.RebuildEffectiveLevels(db, admin, in); done <- e }()
	select {
	case <-barrier:
	case e := <-done:
		t.Fatalf("ended before cohort lock: %v", e)
	case <-time.After(8 * time.Second):
		t.Fatal("cohort lock barrier timed out")
	}
	if e := writer.Commit().Error; e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if !errors.Is(e, service.ErrLedgerStale) {
			t.Fatalf("committed bill must invalidate preview, got %v", e)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("rebuild did not finish")
	}
	if got, e := service.CurrentUserLevelIDWithDB(db, 2); e != nil || got != 0 {
		t.Fatalf("stale preview wrote level %d %v", got, e)
	}
	for _, model := range []any{&entity.EffectiveRebuildAudit{}, &entity.UserLevelHistory{}} {
		q := db.Model(model)
		if _, isHistory := model.(*entity.UserLevelHistory); isHistory {
			q = q.Where("user_id=2")
		}
		if e := q.Count(&n).Error; e != nil || n != 0 {
			t.Fatalf("stale writes %d %v", n, e)
		}
	}
	in.ApplyDigest = ""
	fresh, e := service.RebuildEffectiveLevels(db, admin, in)
	if e != nil || fresh.Digest == preview.Digest {
		t.Fatalf("new snapshot %v", e)
	}
	in.ApplyDigest = fresh.Digest
	out, e := service.RebuildEffectiveLevels(db, admin, in)
	if e != nil || out.AuditID == 0 {
		t.Fatalf("fresh apply %+v %v", out, e)
	}
	if got, e := service.CurrentUserLevelIDWithDB(db, 2); e != nil || got != 2 {
		t.Fatalf("fresh level %d %v", got, e)
	}
	var history entity.UserLevelHistory
	if e := db.Where("user_id=2").Take(&history).Error; e != nil || history.ConsumptionTotal != 6 || history.FromLevelID != 0 || history.ToLevelID != 2 {
		t.Fatalf("wrong applied facts %+v %v", history, e)
	}
	t.Log("stale_rejected=true stale_level_writes=0 stale_audits=0 fresh_level=2 fresh_consumption=6")
	// Real MariaDB DECIMAL evidence in the adjacent-micro collision region.
	// Each underlying payment/credit fits both existing source column widths.
	hi := entity.User{ID: 3, Username: "synthetic-precision", Role: "user", Status: "enabled"}
	if err := db.Create(&hi).Error; err != nil {
		t.Fatal(err)
	}
	for i, v := range []string{"6000000000.000001", "5000000000.000001"} {
		id := int32(50 + i)
		no := "synthetic-hi-" + strconv.Itoa(i)
		if e := db.Table("recharge_orders").Create(map[string]any{"id": id, "user_id": 3, "order_no": no, "amount": v, "status": "paid", "payment_provider": "fixture"}).Error; e != nil {
			t.Fatal(e)
		}
		if e := db.Table("billing_records").Create(map[string]any{"user_id": 3, "type": "recharge", "amount": v, "request_id": no, "related_order_id": id}).Error; e != nil {
			t.Fatal(e)
		}
	}
	if err := db.Create(&entity.UserLevel{ID: 3, Name: "precise", Rank: 3, Enabled: true, DeviceLimit: 7}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("user_levels").Where("id = ?", 3).Update("min_recharge_total", "11000000000.000001").Error; err != nil {
		t.Fatal(err)
	}
	fix := service.EffectiveCorrection{SourceKind: "order", SourceID: 50, Expected: "11000000000.000002", New: "11000000000.000001", Reason: "核对一微元"}
	first, e := service.CorrectEffectiveLedger(db, admin, 3, fix)
	if e != nil || first.OldMicros-first.NewMicros != 1 {
		t.Fatalf("full-domain first audit %v", e)
	}
	if got, e := service.CurrentUserLevelIDWithDB(db, 3); got != 3 || e != nil {
		t.Fatalf("exact threshold: %d %v", got, e)
	}
	fix.Expected = fix.New
	fix.New = "11000000000.000000"
	fix.Revision = first.ID
	if _, e := service.CorrectEffectiveLedger(db, admin, 3, fix); e != nil {
		t.Fatal(e)
	}
	if got, e := service.CurrentUserLevelIDWithDB(db, 3); got != 1 || e != nil {
		t.Fatalf("one micro below threshold: %d %v", got, e)
	}
	var values []struct{ Numerical, Exact string }
	if e := db.Table("user_level_histories").Select("CAST(recharge_total AS CHAR) AS numerical,recharge_exact AS exact").Where("user_id=3").Order("id").Find(&values).Error; e != nil || len(values) != 2 || values[0].Numerical != fix.Expected || values[0].Exact != fix.Expected || values[1].Numerical != fix.New || values[1].Exact != fix.New {
		t.Fatalf("history did not preserve exact DECIMAL %v", e)
	}
	t.Log("source_18_6=true adjacent_micro_cas=true cutoff_levels=3,1 history_decimal_text_match=true")

}

// Exercise the actual migration with a legacy DECIMAL(18,6) row. Text fields
// have no defensible historical source, so they must stay empty, not backfilled.
type t14LegacyHistory struct {
	ID                              int32 `gorm:"primaryKey;autoIncrement"`
	UserID                          int32 `gorm:"not null;index"`
	FromLevelID, ToLevelID          int32
	Trigger                         string  `gorm:"type:varchar(16);not null;default:''"`
	EventKey                        string  `gorm:"type:varchar(128);not null;uniqueIndex:idx_user_level_history_event"`
	RechargeTotal, ConsumptionTotal float64 `gorm:"type:decimal(18,6);not null;default:0"`
	Remark                          string  `gorm:"type:varchar(255);not null;default:''"`
	CreatedAt                       time.Time
}

func (*t14LegacyHistory) TableName() string { return entity.TableNameUserLevelHistory }
func verifyT14HistoryMigration(t *testing.T, db *gorm.DB) {
	t.Helper()
	if e := db.AutoMigrate(&t14LegacyHistory{}); e != nil {
		t.Fatal(e)
	}
	if e := db.Table(entity.TableNameUserLevelHistory).Create(map[string]any{"user_id": 179, "event_key": "synthetic-old-history", "recharge_total": "123.456789", "consumption_total": "4.000001", "from_level_id": 0, "to_level_id": 0}).Error; e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e := ensureEffectiveAuditSchema(db); e != nil {
			t.Fatal(e)
		}
	}
	var old struct{ Recharge, Consumption, RechargeExact, ConsumptionExact string }
	if e := db.Table(entity.TableNameUserLevelHistory).Select("CAST(recharge_total AS CHAR) AS recharge,CAST(consumption_total AS CHAR) AS consumption,recharge_exact,consumption_exact").Where("user_id=179").Take(&old).Error; e != nil || old.Recharge != "123.456789" || old.Consumption != "4.000001" || old.RechargeExact != "" || old.ConsumptionExact != "" {
		t.Fatalf("old snapshot changed: %+v %v", old, e)
	}
	if db.Dialector.Name() == "mysql" {
		var count int
		if e := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='user_level_histories' AND column_name IN ('recharge_total','consumption_total') AND numeric_precision=38 AND numeric_scale=6").Scan(&count).Error; e != nil || count != 2 {
			t.Fatalf("MariaDB must widen both cumulative (not source) columns; matched %d %v", count, e)
		}
	}
	t.Log("history_widened_or_numeric_affinity=true original_numeric_unchanged=true old_exact_text_empty=true second_migration_idempotent=true")
}
func TestReviewEffectiveHistoryMigrationSQLite(t *testing.T) {
	verifyT14HistoryMigration(t, newA1RegistryDB(t))
}
