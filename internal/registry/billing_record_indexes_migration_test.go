package registry

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const billingUsageIndexName = "idx_billing_records_usage"

func newBillingIndexesMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.BillingRecord{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestBillingUsageIndexMigratesLegacyIndexesWithoutChangingData(t *testing.T) {
	db := newBillingIndexesMigrationTestDB(t)
	for _, statement := range []string{
		"CREATE INDEX idx_billing_records_dedup ON billing_records (user_id, request_id, type, hidden_from_statement, id)",
		"CREATE INDEX idx_billing_records_aggregation ON billing_records (type, request_id, user_id, hidden_from_statement)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := setSystemSettingFlag(db, "billing_record_indexes_v1"); err != nil {
		t.Fatal(err)
	}
	stored := entity.BillingRecord{
		UserID: 7, RequestID: "legacy-request", Type: "normal_search", Provider: "ai",
		InputTokens: 10000, CachedInputTokens: 5640, Amount: 1.234567,
		QuestionText: "历史题目", UnitPriceSnapshot: "历史价格快照", HiddenFromStatement: true,
	}
	if err := db.Create(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureBillingRecordIndexes(db); err != nil {
		t.Fatal(err)
	}
	columns, err := billingRecordIndexColumns(db, billingUsageIndexName)
	if err != nil {
		t.Fatal(err)
	}
	want := "type,user_id,request_id,hidden_from_statement,provider,input_tokens,cached_input_tokens,amount"
	if strings.Join(columns, ",") != want {
		t.Fatalf("usage covering index columns = %v, want %s", columns, want)
	}
	for _, index := range []string{"idx_billing_records_dedup", "idx_billing_records_aggregation"} {
		if !db.Migrator().HasIndex(&entity.BillingRecord{}, index) {
			t.Fatalf("legacy index %s must be preserved", index)
		}
	}
	var actual entity.BillingRecord
	if err := db.First(&actual, stored.ID).Error; err != nil {
		t.Fatal(err)
	}
	if actual.ID != stored.ID || actual.RequestID != stored.RequestID || actual.InputTokens != stored.InputTokens || actual.CachedInputTokens != stored.CachedInputTokens || actual.Amount != stored.Amount || actual.QuestionText != stored.QuestionText || actual.UnitPriceSnapshot != stored.UnitPriceSnapshot || !actual.HiddenFromStatement {
		t.Fatalf("index migration changed historical billing data: %+v", actual)
	}
	if !systemSettingFlagIsSet(db, billingRecordIndexesMigrationKey) {
		t.Fatal("successful index migration must set its marker")
	}

	var output bytes.Buffer
	loggedDB := db.Session(&gorm.Session{Logger: gormlogger.New(log.New(&output, "", 0), gormlogger.Config{LogLevel: gormlogger.Info})})
	if err := ensureBillingRecordIndexes(loggedDB); err != nil {
		t.Fatal(err)
	}
	statements := strings.ToUpper(output.String())
	if strings.Contains(statements, "CREATE INDEX") || strings.Contains(statements, "DROP INDEX") || strings.Contains(statements, "ALTER TABLE") {
		t.Fatalf("repeat migration must not rebuild indexes: %s", output.String())
	}
}

func TestBillingUsageAggregationUsesCoveringIndexOnSQLite(t *testing.T) {
	db := newBillingIndexesMigrationTestDB(t)
	if err := ensureBillingRecordIndexes(db); err != nil {
		t.Fatal(err)
	}
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw(`EXPLAIN QUERY PLAN
		SELECT user_id, request_id, SUM(amount),
			SUM(CASE WHEN provider = 'ai' AND input_tokens > 0 THEN input_tokens ELSE 0 END),
			SUM(CASE WHEN provider = 'ai' AND input_tokens > 0 AND cached_input_tokens > 0
				THEN CASE WHEN cached_input_tokens > input_tokens THEN input_tokens ELSE cached_input_tokens END ELSE 0 END)
		FROM billing_records
		WHERE type = ? AND user_id = ? AND request_id IN (?, ?) AND hidden_from_statement = ?
		GROUP BY user_id, request_id`, "normal_search", 7, "one", "two", false).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range plan {
		if strings.Contains(step.Detail, "USING COVERING INDEX "+billingUsageIndexName) {
			return
		}
	}
	t.Fatalf("bounded request usage aggregation should use the covering index: %+v", plan)
}

func TestBillingUsageIndexCreationFailureCanRetryWithoutSuccessMarker(t *testing.T) {
	db := newBillingIndexesMigrationTestDB(t)
	if err := setSystemSettingFlag(db, "billing_record_indexes_v1"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("simulated index creation failure")
	const callback = "test:fail_billing_usage_index"
	if err := db.Callback().Raw().Before("gorm:raw").Register(callback, func(tx *gorm.DB) {
		statement := tx.Statement.SQL.String()
		if strings.Contains(statement, "CREATE INDEX") && strings.Contains(statement, billingUsageIndexName) {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureBillingRecordIndexes(db); !errors.Is(err, failure) {
		t.Fatalf("index creation error must be returned: %v", err)
	}
	if systemSettingFlagIsSet(db, billingRecordIndexesMigrationKey) {
		t.Fatal("failed index migration must not write its success marker")
	}
	if !systemSettingFlagIsSet(db, "billing_record_indexes_v1") {
		t.Fatal("upgrading must preserve the historical migration marker")
	}
	if err := db.Callback().Raw().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := ensureBillingRecordIndexes(db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	if !db.Migrator().HasIndex(&entity.BillingRecord{}, billingUsageIndexName) || !systemSettingFlagIsSet(db, billingRecordIndexesMigrationKey) {
		t.Fatal("successful retry must create the usage index and set its marker")
	}
}
