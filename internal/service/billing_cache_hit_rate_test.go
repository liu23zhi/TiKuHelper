package service

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func assertBillingCacheRate(t *testing.T, value any, field string, want any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	got, exists := fields[field]
	if !exists {
		t.Fatalf("missing %s: %s", field, payload)
	}
	if want == nil {
		if got != nil {
			t.Fatalf("%s = %v, want null", field, got)
		}
		return
	}
	rate, ok := got.(float64)
	if !ok || math.Abs(rate-want.(float64)) > 1e-12 {
		t.Fatalf("%s = %v, want %v", field, got, want)
	}
}

func TestBillingCacheHitRateUsesCompleteRequestUsageBeforePagination(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	owner := seedBillingRecordUser(t, db, 2, "user")
	charges := []entity.BillingRecord{
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "weighted", InputTokens: 1000, CachedInputTokens: 100, Amount: 1},
		// Zero-price and unpriced model usage still participates; snapshot input is
		// deliberately incompatible with the authoritative entity input total.
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "weighted", InputTokens: 9000, CachedInputTokens: 4500, UnitPriceSnapshot: "pricing=unpriced_model inputTokens=4500"},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "icodef", RequestID: "weighted", InputTokens: 50000, CachedInputTokens: 50000, Amount: 2},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "web_read", RequestID: "weighted", Amount: 3},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "uncached", InputTokens: 1000},
		// A different user with the same request ID must remain a separate request.
		{UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "weighted", InputTokens: 1000, CachedInputTokens: 1000},
	}
	for i := range charges {
		if err := db.Create(&charges[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	for page, want := range []float64{1, 0, 0.46} {
		result, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, page+1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 3 || len(result.Data) != 1 {
			t.Fatalf("unexpected grouped page: %+v", result)
		}
		assertBillingCacheRate(t, result.Data[0], "cacheHitRate", want)
		assertBillingCacheRate(t, result.Summary, "currentPageCacheHitRate", want)
		assertBillingCacheRate(t, result.Summary, "totalCacheHitRate", 5600.0/12000)
		if page == 2 && (result.Data[0].Provider != "web_read" || result.Data[0].Amount != 6) {
			t.Fatalf("tool representative must retain complete usage and amount: %+v", result.Data[0])
		}
	}
	filtered, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "icodef", Type: normalSearchBillingRecordType}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 {
		t.Fatalf("keyword on a sibling must retain the request: %+v", filtered)
	}
	assertBillingCacheRate(t, filtered.Data[0], "cacheHitRate", 0.46)
	assertBillingCacheRate(t, filtered.Summary, "totalCacheHitRate", 0.46)
	emptyPage, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "icodef"}, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertBillingCacheRate(t, emptyPage.Summary, "currentPageCacheHitRate", nil)
	assertBillingCacheRate(t, emptyPage.Summary, "totalCacheHitRate", 0.46)
}

func TestBillingCacheHitRateEmptyRequestIDsAndNonModelUsage(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	charges := []entity.BillingRecord{
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", InputTokens: 1000, CachedInputTokens: 100},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", InputTokens: 9000, CachedInputTokens: 4500},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", InputTokens: 0, CachedInputTokens: 9000},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "local", InputTokens: 50000, CachedInputTokens: 50000},
		{UserID: owner.ID, Type: rechargeBillingRecordType, Provider: "ai", InputTokens: 50000, CachedInputTokens: 50000},
		{UserID: owner.ID, Type: registrationBonusBillingRecordType, Provider: "ai", InputTokens: 50000, CachedInputTokens: 50000},
	}
	for i := range charges {
		if err := db.Create(&charges[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 6 {
		t.Fatalf("empty request IDs must not be merged: %+v", result.Data)
	}
	for i, row := range result.Data {
		var want any
		if i == 4 {
			want = 0.5
		} else if i == 5 {
			want = 0.1
		}
		assertBillingCacheRate(t, row, "cacheHitRate", want)
	}
	assertBillingCacheRate(t, result.Summary, "currentPageCacheHitRate", 0.46)
	assertBillingCacheRate(t, result.Summary, "totalCacheHitRate", 0.46)
	income, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Type: rechargeBillingRecordType}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertBillingCacheRate(t, income.Summary, "totalCacheHitRate", nil)
}

func TestBillingCacheHitRateRespectsHiddenSiblingVisibility(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	admin := seedBillingRecordUser(t, db, 1, "admin")
	charges := []entity.BillingRecord{
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "hidden-group", InputTokens: 1000, CachedInputTokens: 100, Amount: 1},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "hidden-group", InputTokens: 9000, CachedInputTokens: 4500, Amount: 9, ModelName: "secret-model", HiddenFromStatement: true},
	}
	for i := range charges {
		if err := db.Create(&charges[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	personal, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertBillingCacheRate(t, personal.Data[0], "cacheHitRate", 0.1)
	assertBillingCacheRate(t, personal.Summary, "totalCacheHitRate", 0.1)
	personal, err = ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "secret-model"}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if personal.Total != 0 || len(personal.Data) != 0 || personal.Summary.TotalAmount != 0 {
		t.Fatalf("hidden sibling must not make a personal request match: %+v", personal)
	}
	assertBillingCacheRate(t, personal.Summary, "totalCacheHitRate", nil)
	full, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{Keyword: "secret-model"}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if full.Total != 1 || full.Data[0].Amount != 10 {
		t.Fatalf("admin must retain hidden sibling: %+v", full)
	}
	assertBillingCacheRate(t, full.Summary, "totalCacheHitRate", 0.46)
}

func TestBillingCacheHitRatePerCallMetadataIsAdminOnly(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	admin := seedBillingRecordUser(t, db, 1, "admin")
	snapshot := "provider=ai stageBillingCall1=" + url.QueryEscape("index=1,model=gpt-6.1-sol,totalTokens=5420,inputTokens=1000,cachedInputTokens=100,amount=1,finalMultiplier=2.7") +
		" stageBillingCall2=" + url.QueryEscape("index=2,model=gpt-6.1-sol,totalTokens=9000,inputTokens=9000,cachedInputTokens=4500,amount=2") +
		" stageBillingCall3=" + url.QueryEscape("index=3,totalTokens=10,amount=0") +
		" stageBillingCall4=" + url.QueryEscape("index=4,inputTokens=200,cachedInputTokens=0,amount=0") +
		" stageBillingCall5=" + url.QueryEscape("index=5,inputTokens=200,amount=0")
	if err := db.Create(&entity.BillingRecord{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", InputTokens: 10200, CachedInputTokens: 4600, UnitPriceSnapshot: snapshot}).Error; err != nil {
		t.Fatal(err)
	}
	full, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	fees := full.Data[0].FeeDetails
	if len(fees) != 5 {
		t.Fatalf("expected individual calls, got %+v", fees)
	}
	assertBillingCacheRate(t, fees[0], "cacheHitRate", 0.1)
	assertBillingCacheRate(t, fees[1], "cacheHitRate", 0.5)
	assertBillingCacheRate(t, fees[3], "cacheHitRate", 0.0)
	for _, index := range []int{2, 4} {
		payload, _ := json.Marshal(fees[index])
		var fields map[string]any
		_ = json.Unmarshal(payload, &fields)
		if fields["cacheHitRate"] != nil {
			t.Fatalf("historical missing usage must remain unknown: %s", payload)
		}
	}
	personal, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, fee := range personal.Data[0].FeeDetails {
		payload, _ := json.Marshal(fee)
		var fields map[string]any
		_ = json.Unmarshal(payload, &fields)
		if _, exists := fields["cacheHitRate"]; exists {
			t.Fatalf("personal per-call cache rate must be omitted: %s", payload)
		}
	}
}

func TestBillingCacheHitRateIncludesUsageBeyondSnapshotLimit(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	snapshot := []string{"provider=ai", "stageBillingCallsOmitted=2"}
	for index := 1; index <= 20; index++ {
		snapshot = append(snapshot, fmt.Sprintf("stageBillingCall%d=%s", index,
			url.QueryEscape(fmt.Sprintf("index=%d,inputTokens=100,cachedInputTokens=0,totalTokens=100,amount=0", index))))
	}
	if err := db.Create(&entity.BillingRecord{
		UserID: admin.ID, Type: normalSearchBillingRecordType, Provider: "ai", RequestID: "many-calls",
		InputTokens: 10000, CachedInputTokens: 4600, UnitPriceSnapshot: strings.Join(snapshot, " "),
	}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || len(result.Data[0].FeeDetails) != 20 {
		t.Fatalf("expected 20 call snapshots: %+v", result.Data)
	}
	for _, fee := range result.Data[0].FeeDetails {
		assertBillingCacheRate(t, fee, "cacheHitRate", 0.0)
	}
	assertBillingCacheRate(t, result.Data[0], "cacheHitRate", 0.46)
	assertBillingCacheRate(t, result.Summary, "currentPageCacheHitRate", 0.46)
	assertBillingCacheRate(t, result.Summary, "totalCacheHitRate", 0.46)
}
