package service

import (
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestBillingPageDoesNotLoadAllHistoricalDetails(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	if err := db.Exec(`WITH RECURSIVE numbers(id) AS (
		SELECT 1 UNION ALL SELECT id + 1 FROM numbers WHERE id < 30000
	) INSERT INTO billing_records (user_id, type, provider, request_id, input_tokens, cached_input_tokens, amount)
	SELECT ?, 'normal_search', 'ai', 'history-' || id, 1000, 564, 0.01 FROM numbers`, admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE INDEX idx_billing_records_dedup ON billing_records (user_id, request_id, type, hidden_from_statement, id)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE INDEX idx_billing_records_usage ON billing_records (type, user_id, request_id, hidden_from_statement, provider, input_tokens, cached_input_tokens, amount)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE billing_records").Error; err != nil {
		t.Fatal(err)
	}
	var detailRows int64
	var summarySQL string
	var summaryArgs []interface{}
	if err := db.Callback().Query().After("gorm:query").Register("test:bounded_billing_details", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "billing_records.unit_price_snapshot") {
			detailRows += tx.RowsAffected
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().After("gorm:row").Register("test:billing_summary_plan", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "AS billing_totals") {
			summarySQL = tx.Statement.SQL.String()
			summaryArgs = append([]interface{}(nil), tx.Statement.Vars...)
		}
	}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("30000 billing rows, page size 20: %s, detail rows read=%d", time.Since(started), detailRows)
	if result.Total != 30000 || len(result.Data) != 20 || result.Summary.TotalAmount != 300 || result.Summary.CurrentPageAmount != 0.2 {
		t.Fatalf("large billing fixture changed totals: %+v", result)
	}
	assertBillingCacheRate(t, result.Summary, "currentPageCacheHitRate", 0.564)
	assertBillingCacheRate(t, result.Summary, "totalCacheHitRate", 0.564)
	if detailRows > 40 {
		t.Fatalf("page size 20 loaded %d historical detail rows; only page rows and their siblings may be loaded", detailRows)
	}
	if summarySQL == "" {
		t.Fatal("expected database-side summary query")
	}
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw("EXPLAIN QUERY PLAN "+summarySQL, summaryArgs...).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	for _, step := range plan {
		if strings.Contains(step.Detail, "USING COVERING INDEX idx_billing_records_usage") {
			return
		}
	}
	t.Fatalf("actual summary query should read the covering usage index: %+v", plan)
}

func TestBillingSummaryPreservesGroupedRoundingAndPositiveExpenses(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	rows := []entity.BillingRecord{
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "round-a", Amount: 0.0000003},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "round-a", Amount: 0.0000003},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "round-b", Amount: 0.0000004},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "negative", Amount: -1},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "offset", Amount: 2},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "offset", Amount: -1},
		{UserID: admin.ID, Type: rechargeBillingRecordType, Provider: "ai", Amount: 10},
		{UserID: admin.ID, Type: balanceAdjustmentBillingRecordType, Provider: "system", Amount: 20},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	result, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.TotalAmount != 1.000001 || result.Summary.CurrentPageAmount != 1.000001 {
		t.Fatalf("summary must round per request and exclude nonpositive/income/adjustment amounts: %+v", result.Summary)
	}
}

func TestBillingPagePreservesLegacyTypesWithoutSiblingCharges(t *testing.T) {
	for _, recordType := range []string{"", " normal_search "} {
		t.Run("type="+recordType, func(t *testing.T) {
			db := newBillingRecordTestDB(t)
			admin := seedBillingRecordUser(t, db, 1, "admin")
			row := entity.BillingRecord{UserID: admin.ID, Type: recordType, Provider: "ai", RequestID: "legacy", Amount: 1.5, InputTokens: 1000, CachedInputTokens: 564}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			result, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Data) != 1 || result.Data[0].Amount != 1.5 || result.Summary.TotalAmount != 1.5 || result.Summary.CurrentPageAmount != 1.5 {
				t.Fatalf("legacy type must retain its original charge when no canonical siblings exist: %+v", result)
			}
			assertBillingCacheRate(t, result.Data[0], "cacheHitRate", 0.564)
			assertBillingCacheRate(t, result.Summary, "currentPageCacheHitRate", 0.564)
			assertBillingCacheRate(t, result.Summary, "totalCacheHitRate", 0.564)
		})
	}
}

func TestBillingWhitespaceRequestIDsRemainIndependent(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	rows := []entity.BillingRecord{
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: " ", Amount: 1, InputTokens: 1000, CachedInputTokens: 564, Remark: "first standalone"},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: " ", Amount: 2, InputTokens: 1000, CachedInputTokens: 564, Remark: "second standalone"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	result, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Data) != 2 || result.Summary.TotalAmount != 3 || result.Summary.CurrentPageAmount != 3 {
		t.Fatalf("blank request IDs must not merge unrelated charges: %+v", result)
	}
	filtered, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{Keyword: "first standalone"}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || len(filtered.Data) != 1 || filtered.Summary.TotalAmount != 1 || filtered.Summary.CurrentPageAmount != 1 {
		t.Fatalf("blank request ID must not include unrelated siblings through a keyword filter: %+v", filtered)
	}
}

func TestBillingPageLoadsOnlyExactUserRequestPairs(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	other := seedBillingRecordUser(t, db, 2, "user")
	rows := []entity.BillingRecord{
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "other-request", Amount: 100},
		{UserID: other.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "admin-request", Amount: 200},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "admin-request", Amount: 1},
		{UserID: other.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "other-request", Amount: 2},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var siblingRows int64
	if err := db.Callback().Query().After("gorm:query").Register("test:exact_billing_pairs", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "billing_records.unit_price_snapshot") && strings.Contains(tx.Statement.SQL.String(), "ORDER BY billing_records.id asc") {
			siblingRows += tx.RowsAffected
		}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 4 || len(result.Data) != 2 || result.Summary.TotalAmount != 303 || result.Summary.CurrentPageAmount != 3 || siblingRows != 2 {
		t.Fatalf("page must load only selected user/request pairs; sibling rows=%d, result=%+v", siblingRows, result)
	}
}
