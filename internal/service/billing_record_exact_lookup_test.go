package service

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestListBillingRecordsExactRequestKeepsAdminDetailsAndScope(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	other := seedBillingRecordUser(t, db, 2, "user")
	const requestID = "request-exact"
	charges := []entity.BillingRecord{
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: requestID, Amount: 1,
			InputTokens: 1000, CachedInputTokens: 100, TotalTokens: 1000,
			UnitPriceSnapshot: "provider=ai stageBillingCall1=" + url.QueryEscape("index=1,model=model-a,inputTokens=1000,cachedInputTokens=100,totalTokens=1000,amount=1,finalMultiplier=2")},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: requestID, Amount: 2,
			InputTokens: 9000, CachedInputTokens: 4500, TotalTokens: 9000,
			UnitPriceSnapshot: "provider=ai stageBillingCall1=" + url.QueryEscape("index=2,model=model-b,inputTokens=9000,cachedInputTokens=4500,totalTokens=9000,amount=2,finalMultiplier=2")},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai_search", RequestID: requestID, Amount: 0.25},
		{UserID: other.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: requestID, Amount: 9, InputTokens: 10000},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "prefix-" + requestID, Amount: 99},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: requestID + "-suffix", Amount: 999},
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "unrelated", Remark: requestID, Amount: 9999},
		{UserID: admin.ID, Type: "recharge", Provider: "system", RequestID: requestID, Amount: 99999},
	}
	if err := db.Create(&charges).Error; err != nil {
		t.Fatal(err)
	}
	filter := BillingRecordListQuery{UserID: admin.ID, RequestID: requestID, Type: normalSearchBillingRecordType}
	result, err := ListBillingRecordsWithDB(db, admin, filter, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Data) != 1 || result.Summary.TotalAmount != 3.25 || result.Summary.CurrentPageAmount != 3.25 {
		t.Fatalf("exact owner/request lookup included unrelated billing rows: %+v", result)
	}
	row := result.Data[0]
	if row.UserID != admin.ID || row.ReferenceNo != requestID || row.Amount != 3.25 || row.CacheHitRate == nil || *row.CacheHitRate != 0.46 || len(row.FeeDetails) != 3 {
		t.Fatalf("exact lookup lost merged billing details: %+v", row)
	}
	rates := []float64{}
	for _, fee := range row.FeeDetails {
		if fee.CacheHitRate != nil {
			rates = append(rates, *fee.CacheHitRate)
		}
	}
	if len(rates) != 2 || rates[0] != 0.1 || rates[1] != 0.5 || result.Summary.TotalCacheHitRate == nil || *result.Summary.TotalCacheHitRate != 0.46 {
		t.Fatalf("exact lookup redacted admin metadata or changed weighted rates: rates=%v summary=%+v", rates, result.Summary)
	}
	personal, err := ListBillingRecordsWithDB(db, other, filter, 1, 1)
	if err != nil || personal.Total != 0 || len(personal.Data) != 0 {
		t.Fatalf("internal exact filters bypassed the caller's role scope: result=%+v err=%v", personal, err)
	}
}

func TestBillingExactRequestLookupUsesIndexWithUnrelatedRows(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	rows := make([]entity.BillingRecord, 10000)
	for i := range rows {
		rows[i] = entity.BillingRecord{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: fmt.Sprintf("noise-%05d", i)}
	}
	if err := db.CreateInBatches(&rows, 250).Error; err != nil {
		t.Fatal(err)
	}
	const requestID = "single-test-request"
	if err := db.Create(&entity.BillingRecord{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai_search", RequestID: requestID, Amount: 0.2}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE billing_records").Error; err != nil {
		t.Fatal(err)
	}
	filter := BillingRecordListQuery{UserID: admin.ID, RequestID: requestID, Type: normalSearchBillingRecordType}
	var count int64
	statement := applyBillingRecordFilters(buildBillingRecordDisplayQuery(db, admin), filter, true).
		Session(&gorm.Session{DryRun: true}).Count(&count).Statement
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw("EXPLAIN QUERY PLAN "+statement.SQL.String(), statement.Vars...).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	indexed := false
	for _, step := range plan {
		if strings.Contains(step.Detail, "SCAN billing_records") {
			t.Fatalf("known request lookup scanned the full billing table: %+v", plan)
		}
		if strings.Contains(step.Detail, "SEARCH billing_records USING INDEX") {
			indexed = true
		}
	}
	if !indexed {
		t.Fatalf("known request lookup did not use a billing index: %+v", plan)
	}
	started := time.Now()
	result, err := ListBillingRecordsWithDB(db, admin, filter, 1, 1)
	if err != nil || result.Total != 1 || len(result.Data) != 1 || result.Data[0].Amount != 0.2 {
		t.Fatalf("large fixture exact lookup failed: result=%+v err=%v", result, err)
	}
	t.Logf("exact request lookup among %d unrelated rows completed in %s; plan=%+v", len(rows), time.Since(started), plan)
}
