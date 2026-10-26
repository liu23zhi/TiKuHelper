package service

import (
	"encoding/json"
	"strings"
	"testing"

	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"net/url"
)

func newBillingRecordTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:billing-record-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	return db
}

func seedBillingRecordUser(t *testing.T, db *gorm.DB, id int32, role string) entity.User {
	t.Helper()

	user := entity.User{
		ID:             id,
		Username:       "billing-user-" + role,
		Password:       "123456",
		Role:           role,
		Status:         "enabled",
		RegisterSource: "admin",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func TestMergeBillingSearchGroupRecoversIPFromSiblingCharge(t *testing.T) {
	got := mergeBillingSearchGroup(BillingRecordListItem{ClientIP: ""}, []BillingRecordListItem{
		{Amount: 1, ClientIP: ""}, {Amount: 2, ClientIP: " 8.8.8.8 "},
	})
	if got.ClientIP != "8.8.8.8" || got.Amount != 3 {
		t.Fatalf("expected merged request to preserve known source IP, got %+v", got)
	}
}

func TestDecorateBillingRecordExtractsFinalMultiplierFromStagedSnapshot(t *testing.T) {
	record := decorateBillingRecord(BillingRecordListItem{
		Type:     "normal_search",
		Provider: "ai",
		UnitPriceSnapshot: "provider=ai multiplier=2.0000 " +
			"stageBillingCall1=index%3D1%2CtotalTokens%3D3000%2CstageMultiplier%3D2.0000%2CfinalMultiplier%3D4.0000",
	})

	if record.FinalMultiplier != "4x" {
		t.Fatalf("expected final multiplier to use model price x stage multiplier, got %q", record.FinalMultiplier)
	}
}

func TestDecorateBillingRecordUsesPriceMultiplierAndNeutralFallback(t *testing.T) {
	priced := decorateBillingRecord(BillingRecordListItem{
		Type:              "normal_search",
		Provider:          "ai",
		UnitPriceSnapshot: "provider=ai multiplier=1.3000 stageBillingEnabled=false",
	})
	if priced.FinalMultiplier != "1.3x" {
		t.Fatalf("expected model price multiplier fallback, got %q", priced.FinalMultiplier)
	}

	ordinary := decorateBillingRecord(BillingRecordListItem{
		Type:     "normal_search",
		Provider: "icodef",
	})
	if ordinary.FinalMultiplier != "1x" {
		t.Fatalf("expected ordinary search neutral multiplier, got %q", ordinary.FinalMultiplier)
	}

	recharge := decorateBillingRecord(BillingRecordListItem{Type: "recharge"})
	if recharge.FinalMultiplier != "-" {
		t.Fatalf("expected recharge multiplier placeholder, got %q", recharge.FinalMultiplier)
	}

	unpriced := decorateBillingRecord(BillingRecordListItem{
		Type:              "normal_search",
		Provider:          "ai",
		UnitPriceSnapshot: "provider=ai pricing=unpriced_model",
	})
	if unpriced.FinalMultiplier != "-" {
		t.Fatalf("expected unpriced AI multiplier placeholder, got %q", unpriced.FinalMultiplier)
	}
}

func TestDecorateBillingRecordIncludesWebReadURL(t *testing.T) {
	const expectedURL = "https://example.com/article?chapter=1&lang=zh"
	record := decorateBillingRecord(BillingRecordListItem{
		Type:     normalSearchBillingRecordType,
		Provider: "web_read",
		UnitPriceSnapshot: "provider=web_read webReadURL=" + url.QueryEscape(expectedURL) +
			" webReadStatus=success webReadDurationMs=187",
	})

	if len(record.FeeDetails) != 1 {
		t.Fatalf("expected one web-read fee detail, got %+v", record.FeeDetails)
	}
	if record.FeeDetails[0].URL != expectedURL {
		t.Fatalf("expected structured web-read URL %q, got %q", expectedURL, record.FeeDetails[0].URL)
	}
	if !strings.Contains(record.FeeDetails[0].Detail, "网页："+expectedURL) {
		t.Fatalf("legacy web-read detail should retain the URL, got %q", record.FeeDetails[0].Detail)
	}
}

func TestMergeBillingSearchGroupCombinesDistinctFinalMultipliers(t *testing.T) {
	merged := mergeBillingSearchGroup(
		BillingRecordListItem{FinalMultiplier: "4x"},
		[]BillingRecordListItem{
			{Amount: 1, FinalMultiplier: "4x"},
			{Amount: 2, FinalMultiplier: "1.3x"},
			{Amount: 3, FinalMultiplier: "4x"},
		},
	)

	want := "4x" + string([]rune{0x3001}) + "1.3x"
	if merged.FinalMultiplier != want {
		t.Fatalf("expected merged final multipliers, got %q", merged.FinalMultiplier)
	}
}

func TestBillingEffectiveMultiplierUsesOnlyAIModelCharges(t *testing.T) {
	ai := decorateBillingRecord(BillingRecordListItem{
		Type:     normalSearchBillingRecordType,
		Provider: "ai",
		Amount:   9.2,
		UnitPriceSnapshot: "provider=ai pricing=model_price_table multiplier=2.0000 inputPrice=1.0000 cachedPrice=0 cacheWritePrice=0 outputPrice=0 " +
			"inputTokens=5000000 cachedInputTokens=0 cacheWriteTokens=0 outputTokens=0 totalTokens=5000000 " +
			"stageBillingCall1=" + url.QueryEscape("index=1,totalTokens=1000000,baseAmount=2.000000,amount=4.000000,stageMultiplier=2.0000,finalMultiplier=4.0000") + " " +
			"stageBillingCall2=" + url.QueryEscape("index=2,totalTokens=4000000,baseAmount=8.000000,amount=5.200000,stageMultiplier=0.6500,finalMultiplier=1.3000"),
	})
	ordinary := decorateBillingRecord(BillingRecordListItem{Type: normalSearchBillingRecordType, Provider: "icodef", Amount: 5})
	tool := decorateBillingRecord(BillingRecordListItem{Type: normalSearchBillingRecordType, Provider: "ai_search", Amount: 0.8})
	merged := mergeBillingSearchGroup(tool, []BillingRecordListItem{ordinary, ai, tool})
	if merged.EffectiveMultiplier != "1.84x" {
		t.Fatalf("model-only effective multiplier: got %q, want 1.84x", merged.EffectiveMultiplier)
	}
	if merged.Amount != 15 || merged.FinalMultiplier != "1x、4x、1.3x" {
		t.Fatalf("list calculation must leave existing amount and per-call multiplier labels intact: %+v", merged)
	}
	if len(merged.FeeDetails) != 4 || merged.FeeDetails[1].Amount != 4 || merged.FeeDetails[2].Amount != 5.2 {
		t.Fatalf("per-call fee details must remain intact: %+v", merged.FeeDetails)
	}

	second := decorateBillingRecord(BillingRecordListItem{
		Type: normalSearchBillingRecordType, Provider: "ai", Amount: 2,
		UnitPriceSnapshot: "provider=ai pricing=model_price_table multiplier=2 inputPrice=1 cachedPrice=0 cacheWritePrice=0 outputPrice=0 inputTokens=1000000 cachedInputTokens=0 cacheWriteTokens=0 outputTokens=0 totalTokens=1000000",
	})
	merged = mergeBillingSearchGroup(second, []BillingRecordListItem{ai, second})
	if merged.EffectiveMultiplier != "1.8667x" {
		t.Fatalf("multiple AI charges must use their combined price baselines, got %q", merged.EffectiveMultiplier)
	}
}

func TestBillingEffectiveMultiplierUsesAllPricedTokenCategories(t *testing.T) {
	record := decorateBillingRecord(BillingRecordListItem{
		Type: normalSearchBillingRecordType, Provider: "ai", Amount: 13,
		UnitPriceSnapshot: "provider=ai pricing=model_price_table multiplier=2 inputPrice=1 cachedPrice=0.5 cacheWritePrice=2 outputPrice=3 " +
			"inputTokens=1000000 cachedInputTokens=1000000 cacheWriteTokens=1000000 outputTokens=1000000 totalTokens=4000000",
	})
	if record.EffectiveMultiplier != "2x" {
		t.Fatalf("all four token prices must contribute to the 6.5 baseline, got %q", record.EffectiveMultiplier)
	}
}

func TestBillingEffectiveMultiplierHistoricalFallbacks(t *testing.T) {
	tests := []struct {
		name, provider, recordType, snapshot, want string
	}{
		{"single known historical multiplier", "ai", normalSearchBillingRecordType, "provider=ai multiplier=1.3000", "1.3x"},
		{"single staged historical multiplier", "ai", normalSearchBillingRecordType, "provider=ai stageBillingCall1=" + url.QueryEscape("finalMultiplier=4.0000"), "4x"},
		{"multiple historical multipliers", "ai", normalSearchBillingRecordType, "provider=ai stageBillingCall1=" + url.QueryEscape("finalMultiplier=4.0000") + " stageBillingCall2=" + url.QueryEscape("finalMultiplier=1.3000"), "-"},
		{"missing staged call", "ai", normalSearchBillingRecordType, "provider=ai stageBillingModelCallCount=2 stageBillingCall1=" + url.QueryEscape("finalMultiplier=4.0000"), "-"},
		{"invalid historical multiplier", "ai", normalSearchBillingRecordType, "provider=ai multiplier=NaN", "-"},
		{"missing baseline and multiplier", "ai", normalSearchBillingRecordType, "provider=ai", "-"},
		{"unpriced model", "ai", normalSearchBillingRecordType, "provider=ai pricing=unpriced_model multiplier=2", "-"},
		{"ordinary search has no AI multiplier", "icodef", normalSearchBillingRecordType, "provider=icodef", "-"},
		{"recharge", "epay", rechargeBillingRecordType, "", "-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := decorateBillingRecord(BillingRecordListItem{Type: tc.recordType, Provider: tc.provider, UnitPriceSnapshot: tc.snapshot})
			if record.EffectiveMultiplier != tc.want {
				t.Fatalf("effective multiplier = %q, want %q; finalMultiplier=%q", record.EffectiveMultiplier, tc.want, record.FinalMultiplier)
			}
		})
	}
}

func TestMergeBillingSearchGroupSortsFeeDetailsByUsageSequence(t *testing.T) {
	representative := BillingRecordListItem{
		Amount: 0.02,
		FeeDetails: []BillingRecordFeeDetail{
			{Provider: "ai", ProviderLabel: "AI 搜索", Category: "AI模型调用", CallIndex: 1, Amount: 0.01, Sequence: 1},
			{Provider: "ai", ProviderLabel: "AI 搜索", Category: "AI模型调用", CallIndex: 2, Amount: 0.01, Sequence: 5},
		},
	}
	searchCharge := BillingRecordListItem{
		Amount: 0,
		FeeDetails: []BillingRecordFeeDetail{
			{Provider: "ai_search", ProviderLabel: "联网搜索", Amount: 0, Sequence: 3},
		},
	}

	merged := mergeBillingSearchGroup(representative, []BillingRecordListItem{representative, searchCharge})
	if len(merged.FeeDetails) != 3 {
		t.Fatalf("expected three fee details, got %+v", merged.FeeDetails)
	}
	if merged.FeeDetails[0].Sequence != 1 || merged.FeeDetails[1].Sequence != 3 || merged.FeeDetails[2].Sequence != 5 {
		t.Fatalf("fee details must follow usage sequence, got %+v", merged.FeeDetails)
	}
}

func TestMergeBillingSearchGroupInterleavesLegacyAIToolDetails(t *testing.T) {
	representative := BillingRecordListItem{
		Amount: 0.02,
		FeeDetails: []BillingRecordFeeDetail{
			{Provider: "ai", ProviderLabel: "AI 搜索", Category: "AI模型调用", CallIndex: 1, Amount: 0.01},
			{Provider: "ai", ProviderLabel: "AI 搜索", Category: "AI模型调用", CallIndex: 2, Amount: 0.01},
		},
	}
	searchCharge := BillingRecordListItem{
		Amount: 0,
		FeeDetails: []BillingRecordFeeDetail{
			{Provider: "ai_search", ProviderLabel: "联网搜索", CallIndex: 1, Amount: 0},
		},
	}

	merged := mergeBillingSearchGroup(representative, []BillingRecordListItem{representative, searchCharge})
	if len(merged.FeeDetails) != 3 {
		t.Fatalf("expected three fee details, got %+v", merged.FeeDetails)
	}
	if merged.FeeDetails[0].Category != "AI模型调用" || merged.FeeDetails[0].CallIndex != 1 {
		t.Fatalf("expected first model call first, got %+v", merged.FeeDetails)
	}
	if merged.FeeDetails[1].Provider != "ai_search" {
		t.Fatalf("expected search tool between model calls, got %+v", merged.FeeDetails)
	}
	if merged.FeeDetails[2].Category != "AI模型调用" || merged.FeeDetails[2].CallIndex != 2 {
		t.Fatalf("expected second model call last, got %+v", merged.FeeDetails)
	}
}

func seedBillingRecord(t *testing.T, db *gorm.DB, userID int32, requestID string, amount float64) entity.BillingRecord {
	t.Helper()

	record := entity.BillingRecord{
		UserID:             userID,
		Type:               "normal_search",
		UsedDefaultAccount: true,
		Provider:           "icodef",
		Amount:             amount,
		RequestID:          requestID,
		Remark:             requestID,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create billing record: %v", err)
	}
	return record
}

func seedBillingAccessToken(t *testing.T, db *gorm.DB, userID int32, name string) entity.AccessToken {
	t.Helper()

	token := entity.AccessToken{
		UserID:      userID,
		Name:        name,
		TokenHash:   "hash-" + name,
		TokenPrefix: "tk-" + name,
		TokenValue:  "plain-" + name,
		Status:      "enabled",
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("create access token: %v", err)
	}
	return token
}

func TestListBillingRecordsWithDBScopesByRole(t *testing.T) {
	db := newBillingRecordTestDB(t)
	admin := seedBillingRecordUser(t, db, 1, "admin")
	owner := seedBillingRecordUser(t, db, 2, "user")
	other := seedBillingRecordUser(t, db, 3, "user")

	ownerRecord := seedBillingRecord(t, db, owner.ID, "req-owner", 1.25)
	otherRecord := seedBillingRecord(t, db, other.ID, "req-other", 2.5)

	ownerResult, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list owner billing records: %v", err)
	}
	if ownerResult.Total != 1 {
		t.Fatalf("expected owner total 1, got %d", ownerResult.Total)
	}
	if len(ownerResult.Data) != 1 || ownerResult.Data[0].ID != ownerRecord.ID {
		t.Fatalf("expected owner to see only own record, got %+v", ownerResult.Data)
	}
	if ownerResult.Page != 1 || ownerResult.PageSize != 10 {
		t.Fatalf("expected owner pagination 1/10, got %d/%d", ownerResult.Page, ownerResult.PageSize)
	}

	adminResult, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list admin billing records: %v", err)
	}
	if adminResult.Total != 2 {
		t.Fatalf("expected admin total 2, got %d", adminResult.Total)
	}
	if len(adminResult.Data) != 2 {
		t.Fatalf("expected admin to see two records, got %+v", adminResult.Data)
	}
	if adminResult.Data[0].ID != otherRecord.ID || adminResult.Data[1].ID != ownerRecord.ID {
		t.Fatalf("expected admin records ordered desc by id, got %+v", adminResult.Data)
	}
}

func TestListBillingRecordsWithDBIncludesUsername(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	seedBillingRecord(t, db, owner.ID, "req-username", 1.25)

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list billing records: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one billing record, got %+v", result.Data)
	}

	payload, err := json.Marshal(result.Data[0])
	if err != nil {
		t.Fatalf("marshal billing record: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal billing record: %v", err)
	}
	if decoded["username"] != owner.Username {
		t.Fatalf("expected username %q, got %#v", owner.Username, decoded["username"])
	}
}

func TestListBillingRecordsWithDBShapesRechargeRecordsAsLedgerRows(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	searchRecord := seedBillingRecord(t, db, owner.ID, "req-search", 1.25)
	rechargeRecord := entity.BillingRecord{
		UserID:         owner.ID,
		Type:           "recharge",
		Provider:       "epay",
		Amount:         10,
		RequestID:      "rcg_user_20260621002829_1000_ABC12345",
		RelatedOrderID: 28,
		Remark:         "账户充值",
	}
	if err := db.Create(&rechargeRecord).Error; err != nil {
		t.Fatalf("create recharge billing record: %v", err)
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list owner billing records: %v", err)
	}
	if result.Total != 2 || len(result.Data) != 2 {
		t.Fatalf("expected owner list to include search and recharge rows, got %+v", result)
	}
	rechargeRow := result.Data[0]
	if rechargeRow.ID != rechargeRecord.ID {
		t.Fatalf("expected newest recharge row first, got %+v", result.Data)
	}
	if rechargeRow.Type != "recharge" || rechargeRow.TypeLabel != "充值入账" || rechargeRow.Direction != "income" {
		t.Fatalf("unexpected recharge row classification: %+v", rechargeRow)
	}
	if rechargeRow.Subject != "账户充值" || rechargeRow.Detail != "账户充值" || rechargeRow.ReferenceNo != "rcg_user_20260621002829_1000_ABC12345" {
		t.Fatalf("unexpected recharge row display fields: %+v", rechargeRow)
	}
	if rechargeRow.ProviderLabel != "epay" || rechargeRow.AnswerSourceLabel != "epay" {
		t.Fatalf("expected recharge source/channel to remain epay, got %+v", rechargeRow)
	}
	if result.Data[1].ID != searchRecord.ID || result.Data[1].Direction != "expense" || result.Data[1].Subject == "" {
		t.Fatalf("expected search row to remain visible as expense, got %+v", result.Data[1])
	}
}

func TestListBillingRecordsWithDBAppliesPagination(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	older := seedBillingRecord(t, db, owner.ID, "req-older", 1)
	_ = seedBillingRecord(t, db, owner.ID, "req-newer", 2)

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 2, 1)
	if err != nil {
		t.Fatalf("list paginated billing records: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected total 2, got %d", result.Total)
	}
	if result.Page != 2 || result.PageSize != 1 {
		t.Fatalf("expected pagination 2/1, got %d/%d", result.Page, result.PageSize)
	}
	if len(result.Data) != 1 || result.Data[0].ID != older.ID {
		t.Fatalf("expected second page to contain older record, got %+v", result.Data)
	}
}

func TestListBillingRecordsWithDBMergesSearchChargesByRequestID(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	token := seedBillingAccessToken(t, db, owner.ID, "混合搜索令牌")

	normalRecord := entity.BillingRecord{
		UserID:             owner.ID,
		AccessTokenID:      token.ID,
		Type:               "normal_search",
		UsedDefaultAccount: true,
		Provider:           "icodef",
		Amount:             1.25,
		RequestID:          "req-mixed-search",
		UnitPriceSnapshot:  "provider=icodef flatPrice=1.25 question=%E6%B7%B7%E5%90%88%E6%90%9C%E7%B4%A2%E9%A2%98%E7%9B%AE",
		Remark:             "搜索答题消耗",
	}
	if err := db.Create(&normalRecord).Error; err != nil {
		t.Fatalf("create normal search billing record: %v", err)
	}

	aiRecord := entity.BillingRecord{
		UserID:             owner.ID,
		AccessTokenID:      token.ID,
		Type:               "normal_search",
		UsedDefaultAccount: true,
		Provider:           "ai",
		ModelName:          "gpt-test",
		InputTokens:        100,
		OutputTokens:       50,
		TotalTokens:        150,
		Amount:             0.75,
		RequestID:          "req-mixed-search",
		UnitPriceSnapshot:  "provider=ai model=gpt-test totalTokens=150 amount=0.75",
		Remark:             "搜索答题消耗",
	}
	if err := db.Create(&aiRecord).Error; err != nil {
		t.Fatalf("create ai search billing record: %v", err)
	}
	searchRecord := entity.BillingRecord{
		UserID:             owner.ID,
		AccessTokenID:      token.ID,
		Type:               "normal_search",
		UsedDefaultAccount: true,
		Provider:           "ai_search",
		Amount:             0.02,
		RequestID:          "req-mixed-search",
		UnitPriceSnapshot:  "provider=ai_search searchQuery=加法%20验算 searchAPI=https%3A%2F%2Fuapis.cn%2Fapi%2Fv1%2Fsearch%2Faggregate searchStatus=success searchResultCount=1",
		Remark:             "联网搜索调用",
	}
	if err := db.Create(&searchRecord).Error; err != nil {
		t.Fatalf("create AI search billing record: %v", err)
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list merged billing records: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("expected mixed search charges to merge into one row, got %+v", result)
	}

	merged := result.Data[0]
	if merged.ID != searchRecord.ID {
		t.Fatalf("expected latest search charge id %d to represent merged row, got %d", searchRecord.ID, merged.ID)
	}
	if merged.Amount != 2.02 {
		t.Fatalf("expected merged amount 2.02, got %.8f", merged.Amount)
	}
	if len(merged.FeeDetails) != 3 {
		t.Fatalf("expected three fee details, got %+v", merged.FeeDetails)
	}
	if merged.FeeDetails[0].ProviderLabel != "Icodef 题库" || merged.FeeDetails[0].Amount != 1.25 {
		t.Fatalf("unexpected normal fee detail: %+v", merged.FeeDetails[0])
	}
	if merged.FeeDetails[1].ProviderLabel != "AI 搜索" || merged.FeeDetails[1].Amount != 0.75 {
		t.Fatalf("unexpected ai fee detail: %+v", merged.FeeDetails[1])
	}
	if merged.FeeDetails[2].ProviderLabel != "联网搜索" || merged.FeeDetails[2].Amount != 0.02 || !strings.Contains(merged.FeeDetails[2].Detail, "加法 验算") {
		t.Fatalf("unexpected AI web search fee detail: %+v", merged.FeeDetails[2])
	}
	if merged.QuestionText != "混合搜索题目" {
		t.Fatalf("expected question text from normal charge, got %q", merged.QuestionText)
	}
	if merged.Subject != "搜索答案，题目：混合搜索题目" {
		t.Fatalf("expected merged subject to identify a search answer and question, got %q", merged.Subject)
	}
	if merged.ProviderLabel != "Icodef 题库、AI 搜索、联网搜索" {
		t.Fatalf("expected merged provider label, got %q", merged.ProviderLabel)
	}
	if merged.AnswerSourceLabel != "Icodef 题库、AI 搜索、联网搜索" {
		t.Fatalf("expected merged answer source label, got %q", merged.AnswerSourceLabel)
	}
	if merged.ReferenceNo != "req-mixed-search" || merged.AccessTokenName != "混合搜索令牌" {
		t.Fatalf("expected merged identity fields to be preserved, got %+v", merged)
	}

	filtered, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "icodef"}, 1, 10)
	if err != nil {
		t.Fatalf("list mixed search by a sibling provider: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Data) != 1 {
		t.Fatalf("expected sibling provider filter to retain the request group, got %+v", filtered)
	}
	filteredRow := filtered.Data[0]
	if filteredRow.Amount != 2.02 || filtered.Summary.TotalAmount != 2.02 || filtered.Summary.CurrentPageAmount != 2.02 {
		t.Fatalf("expected filtered row and summaries to use the full request amount, got row=%v summary=%+v", filteredRow.Amount, filtered.Summary)
	}
	feeTotal := 0.0
	for _, fee := range filteredRow.FeeDetails {
		feeTotal += fee.Amount
	}
	if util.RoundMoney(feeTotal) != filteredRow.Amount {
		t.Fatalf("expected fee details to equal the merged row amount, got details=%v row=%v", feeTotal, filteredRow.Amount)
	}
}

func TestListBillingRecordsWithDBReportsEffectiveMultiplierWithoutChangingDetails(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	admin := seedBillingRecordUser(t, db, 1, "admin")
	for _, charge := range []entity.BillingRecord{
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "icodef", Amount: 5, RequestID: "req-effective", UnitPriceSnapshot: "provider=icodef flatPrice=5"},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", ModelName: "gpt-test", Amount: 9.2, RequestID: "req-effective", UnitPriceSnapshot: "provider=ai pricing=model_price_table multiplier=2 inputPrice=1 cachedPrice=0 cacheWritePrice=0 outputPrice=0 inputTokens=5000000 cachedInputTokens=0 cacheWriteTokens=0 outputTokens=0 totalTokens=5000000 stageBillingCall1=" + url.QueryEscape("index=1,totalTokens=1000000,amount=4,finalMultiplier=4") + " stageBillingCall2=" + url.QueryEscape("index=2,totalTokens=4000000,amount=5.2,finalMultiplier=1.3")},
		{UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai_search", Amount: 0.8, RequestID: "req-effective", UnitPriceSnapshot: "provider=ai_search"},
	} {
		if err := db.Create(&charge).Error; err != nil {
			t.Fatalf("create charge: %v", err)
		}
	}

	for _, filter := range []BillingRecordListQuery{{}, {Keyword: "gpt-test"}} {
		result, err := ListBillingRecordsWithDB(db, admin, filter, 1, 10)
		if err != nil {
			t.Fatalf("list billing records with filter %+v: %v", filter, err)
		}
		if result.Total != 1 || len(result.Data) != 1 {
			t.Fatalf("expected one merged record for filter %+v, got %+v", filter, result)
		}
		row := result.Data[0]
		if row.EffectiveMultiplier != "1.84x" || row.Amount != 15 || row.FinalMultiplier != "1x、4x、1.3x" {
			t.Fatalf("effective multiplier must exclude flat and tool fees without rewriting existing fields: %+v", row)
		}
		if len(row.FeeDetails) != 4 || row.FeeDetails[1].Amount != 4 || row.FeeDetails[2].Amount != 5.2 {
			t.Fatalf("per-call fee detail changed: %+v", row.FeeDetails)
		}
		payload, err := json.Marshal(row)
		if err != nil || !strings.Contains(string(payload), `"effectiveMultiplier":"1.84x"`) {
			t.Fatalf("list JSON must expose read-only effectiveMultiplier: %s, %v", payload, err)
		}
	}
}

func TestListBillingRecordsWithDBRedactsPersonalAIUsageMetadata(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	admin := seedBillingRecordUser(t, db, 1, "admin")
	const requestID = "req-personal-redaction"
	const expectedURL = "https://example.com/article?chapter=1&lang=zh"

	charges := []entity.BillingRecord{
		{
			UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai", Amount: 9.2, RequestID: requestID,
			UnitPriceSnapshot: "provider=ai pricing=model_price_table multiplier=2 inputPrice=1 cachedPrice=0 cacheWritePrice=0 outputPrice=0 inputTokens=5000000 cachedInputTokens=0 cacheWriteTokens=0 outputTokens=0 totalTokens=5000000 stageBillingCall1=" + url.QueryEscape("index=1,model=gpt-test,totalTokens=5000000,amount=9.2,stageMultiplier=2,finalMultiplier=2"),
		},
		{
			UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "ai_search", Amount: 0.8, RequestID: requestID,
			UnitPriceSnapshot: "provider=ai_search searchQuery=" + url.QueryEscape("保留搜索关键词") + " searchStatus=success",
		},
		{
			UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "web_read", Amount: 0.1, RequestID: requestID,
			UnitPriceSnapshot: "provider=web_read webReadURL=" + url.QueryEscape(expectedURL) + " webReadStatus=success webReadDurationMs=187",
		},
	}
	for index := range charges {
		if err := db.Create(&charges[index]).Error; err != nil {
			t.Fatalf("create charge: %v", err)
		}
	}

	personal, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list personal billing records: %v", err)
	}
	if len(personal.Data) != 1 {
		t.Fatalf("expected one personal merged record, got %+v", personal.Data)
	}
	personalRow := personal.Data[0]
	if personalRow.FinalMultiplier != "" || personalRow.EffectiveMultiplier != "" {
		t.Fatalf("personal billing row must not expose multipliers: %+v", personalRow)
	}
	var foundSearchKeyword, foundWebURL, foundAI bool
	for _, detail := range personalRow.FeeDetails {
		switch detail.Provider {
		case "ai":
			foundAI = true
			if detail.Detail != "" || strings.Contains(detail.ProviderLabel, "gpt-test") {
				t.Fatalf("personal AI fee detail must hide model/token/multiplier metadata: %+v", detail)
			}
		case "ai_search":
			foundSearchKeyword = strings.Contains(detail.Detail, "搜索词：保留搜索关键词")
		case "web_read":
			foundWebURL = detail.URL == expectedURL
		}
	}
	if !foundAI || !foundSearchKeyword || !foundWebURL {
		t.Fatalf("personal details must retain AI search keyword and web-read URL: %+v", personalRow.FeeDetails)
	}
	personalPayload, err := json.Marshal(personal)
	if err != nil {
		t.Fatalf("marshal personal billing response: %v", err)
	}
	for _, forbidden := range []string{`"effectiveMultiplier"`, `"finalMultiplier"`, "gpt-test", "totalTokens=", "stageMultiplier="} {
		if strings.Contains(string(personalPayload), forbidden) {
			t.Fatalf("personal billing response leaked %q: %s", forbidden, personalPayload)
		}
	}

	adminResult, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list admin billing records: %v", err)
	}
	if len(adminResult.Data) != 1 {
		t.Fatalf("expected one admin merged record, got %+v", adminResult.Data)
	}
	adminRow := adminResult.Data[0]
	if adminRow.FinalMultiplier == "" || adminRow.EffectiveMultiplier == "" {
		t.Fatalf("admin billing row must retain multipliers: %+v", adminRow)
	}
	adminPayload, err := json.Marshal(adminResult)
	if err != nil {
		t.Fatalf("marshal admin billing response: %v", err)
	}
	for _, expected := range []string{`"effectiveMultiplier"`, `"finalMultiplier"`, "gpt-test", "totalTokens=", "stageMultiplier="} {
		if !strings.Contains(string(adminPayload), expected) {
			t.Fatalf("admin billing response lost %q: %s", expected, adminPayload)
		}
	}
}

func TestListBillingRecordsWithDBDoesNotMergeHiddenSiblingForPersonal(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	admin := seedBillingRecordUser(t, db, 1, "admin")
	const requestID = "req-hidden-sibling"

	charges := []entity.BillingRecord{
		{
			UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "icodef", Amount: 1.25, RequestID: requestID,
			UnitPriceSnapshot: "provider=icodef question=可见题目",
		},
		{
			UserID: owner.ID, Type: normalSearchBillingRecordType, Provider: "web_read", Amount: 0.75, RequestID: requestID,
			HiddenFromStatement: true,
			UnitPriceSnapshot:   "provider=web_read webReadURL=" + url.QueryEscape("https://hidden.example/article"),
		},
	}
	for index := range charges {
		if err := db.Create(&charges[index]).Error; err != nil {
			t.Fatalf("create charge: %v", err)
		}
	}

	personal, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list personal billing records: %v", err)
	}
	if len(personal.Data) != 1 {
		t.Fatalf("expected one personal record, got %+v", personal.Data)
	}
	personalRow := personal.Data[0]
	if personalRow.Amount != 1.25 {
		t.Fatalf("personal amount must exclude hidden sibling, got %v", personalRow.Amount)
	}
	for _, detail := range personalRow.FeeDetails {
		if detail.Provider == "web_read" || strings.Contains(detail.Detail, "hidden.example") {
			t.Fatalf("personal row leaked hidden sibling: %+v", personalRow.FeeDetails)
		}
	}

	adminResult, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list admin billing records: %v", err)
	}
	if len(adminResult.Data) != 1 {
		t.Fatalf("expected one admin record, got %+v", adminResult.Data)
	}
	adminRow := adminResult.Data[0]
	if adminRow.Amount != 2 || len(adminRow.FeeDetails) != 2 {
		t.Fatalf("admin row must retain hidden sibling details, got %+v", adminRow)
	}
}

func TestListBillingRecordsWithDBIncludesProviderLabelAndAccessTokenName(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	token := seedBillingAccessToken(t, db, owner.ID, "默认令牌")

	record := entity.BillingRecord{
		UserID:             owner.ID,
		AccessTokenID:      token.ID,
		Type:               "normal_search",
		UsedDefaultAccount: true,
		Provider:           "icodef",
		Amount:             1.25,
		RequestID:          "req-labeled",
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create billing record with token: %v", err)
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list billing records with labels: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one record, got %+v", result.Data)
	}
	if result.Data[0].ProviderLabel != "Icodef 题库" {
		t.Fatalf("expected provider label Icodef 题库, got %q", result.Data[0].ProviderLabel)
	}
	if result.Data[0].AccessTokenName != "默认令牌" {
		t.Fatalf("expected access token name 默认令牌, got %q", result.Data[0].AccessTokenName)
	}
}

func TestBillingSearchModeLabelsIncludeNativeAndMCPRoutes(t *testing.T) {
	if got := buildBillingSearchModeLabel("normal"); got != "普通搜索" {
		t.Fatalf("unexpected normal label: %q", got)
	}
	if got := buildBillingSearchModeLabel("deep_native"); got != "深度思考 原生工具" {
		t.Fatalf("unexpected native label: %q", got)
	}
	if got := buildBillingSearchModeLabel("deep_mcp"); got != "深度思考 MCP编排" {
		t.Fatalf("unexpected MCP label: %q", got)
	}
	if got := buildBillingSearchModeLabel("deep"); got != "深度思考" {
		t.Fatalf("unexpected legacy deep label: %q", got)
	}
}

func TestDecorateBillingRecordSplitsAIModelStageFees(t *testing.T) {
	record := decorateBillingRecord(BillingRecordListItem{
		Type:              normalSearchBillingRecordType,
		Provider:          "ai",
		ProviderLabel:     "AI 搜索",
		Amount:            0.3,
		UnitPriceSnapshot: "provider=ai stageBillingCall1=index%3D1%2Cmodel%3Dgpt-6-sol%2Cround%3D1%2Cattempt%3D1%2CtotalTokens%3D10%2Camount%3D0.1 stageBillingCall2=index%3D2%2Cmodel%3Dgpt-6-sol%2Cround%3D2%2Cattempt%3D1%2CtotalTokens%3D20%2Camount%3D0.2",
	})
	if len(record.FeeDetails) != 2 {
		t.Fatalf("expected two model fee details, got %+v", record.FeeDetails)
	}
	if record.FeeDetails[0].Category != "AI模型调用" || record.FeeDetails[0].CallIndex != 1 || record.FeeDetails[0].Amount != 0.1 {
		t.Fatalf("unexpected first model fee detail: %+v", record.FeeDetails[0])
	}
	if record.FeeDetails[1].CallIndex != 2 || record.FeeDetails[1].Amount != 0.2 {
		t.Fatalf("unexpected second model fee detail: %+v", record.FeeDetails[1])
	}
}

// 账单的「模式」列：deep→深度思考，normal→普通搜索，空→占位（非搜题/历史行）。
func TestListBillingRecordsWithDBIncludesSearchModeLabel(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	records := []entity.BillingRecord{
		{UserID: owner.ID, Type: "normal_search", Provider: "ai", Amount: 0.5, RequestID: "req-deep", SearchMode: "deep"},
		{UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 0.25, RequestID: "req-normal", SearchMode: "normal"},
		{UserID: owner.ID, Type: "recharge", Provider: "easy_pay", Amount: 10, RequestID: "req-recharge"},
	}
	for index := range records {
		if err := db.Create(&records[index]).Error; err != nil {
			t.Fatalf("seed billing record: %v", err)
		}
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list billing records: %v", err)
	}
	labels := make(map[string][2]string, len(result.Data))
	for _, row := range result.Data {
		labels[row.RequestID] = [2]string{row.SearchMode, row.SearchModeLabel}
	}
	if got := labels["req-deep"]; got != [2]string{"deep", "深度思考"} {
		t.Fatalf("unexpected deep record mode: %v", got)
	}
	if got := labels["req-normal"]; got != [2]string{"normal", "普通搜索"} {
		t.Fatalf("unexpected normal record mode: %v", got)
	}
	if got := labels["req-recharge"]; got != [2]string{"", ""} {
		t.Fatalf("expected recharge rows to stay empty, got %v", got)
	}
}

func TestListBillingRecordsWithDBIncludesQuestionTextFromSnapshot(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")
	token := seedBillingAccessToken(t, db, owner.ID, "题目令牌")

	record := entity.BillingRecord{
		UserID:             owner.ID,
		AccessTokenID:      token.ID,
		Type:               "normal_search",
		UsedDefaultAccount: true,
		Provider:           "icodef",
		Amount:             1.25,
		RequestID:          "req-question-text",
		UnitPriceSnapshot:  "provider=icodef flatPrice=1.25 question=这是账单题目",
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create billing record with question snapshot: %v", err)
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list billing records with question text: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one record, got %+v", result.Data)
	}
	if result.Data[0].QuestionText != "这是账单题目" {
		t.Fatalf("expected question text 这是账单题目, got %q", result.Data[0].QuestionText)
	}
	if result.Data[0].Subject != "搜索答案，题目：这是账单题目" {
		t.Fatalf("expected single search subject to align with merged search format, got %q", result.Data[0].Subject)
	}
}

func TestListBillingRecordsWithDBSearchesByQuestionText(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	records := []entity.BillingRecord{
		{UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 1, RequestID: "req-newton", QuestionText: "牛顿第二定律的表达式"},
		{UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 1, RequestID: "req-photosynthesis", QuestionText: "光合作用的产物"},
	}
	for i := range records {
		if err := db.Create(&records[i]).Error; err != nil {
			t.Fatalf("create billing record: %v", err)
		}
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "牛顿"}, 1, 10)
	if err != nil {
		t.Fatalf("search billing records by question: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("expected exactly one match, got total=%d rows=%d", result.Total, len(result.Data))
	}
	if result.Data[0].QuestionText != "牛顿第二定律的表达式" {
		t.Fatalf("unexpected matched record: %+v", result.Data[0])
	}
}

// 历史账单没有 question_text 时，仍要能按题目命中（回退到快照解析）。
func TestListBillingRecordsWithDBSearchesQuestionTextFallsBackToSnapshot(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	record := entity.BillingRecord{
		UserID:            owner.ID,
		Type:              "normal_search",
		Provider:          "icodef",
		Amount:            1,
		RequestID:         "req-legacy-question",
		UnitPriceSnapshot: "provider=icodef question=" + url.QueryEscape("历史上的题目内容"),
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create legacy billing record: %v", err)
	}

	// 直接按关键词搜不到（列里没有内容），但装饰阶段会从快照回填。
	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list billing records: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one record, got %+v", result.Data)
	}
	if result.Data[0].QuestionText != "历史上的题目内容" {
		t.Fatalf("expected question text from snapshot fallback, got %q", result.Data[0].QuestionText)
	}

	// 回溯填充写入新列后，关键词搜索即可命中。
	if err := db.Model(&entity.BillingRecord{}).Where("id = ?", record.ID).Update("question_text", "历史上的题目内容").Error; err != nil {
		t.Fatalf("backfill question text: %v", err)
	}
	result, err = ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "历史"}, 1, 10)
	if err != nil {
		t.Fatalf("search after backfill: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected the backfilled row to be searchable, got total=%d", result.Total)
	}
}

func TestListBillingRecordsWithDBSearchesUsernameProviderAndRequestID(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "searchable-owner")
	token := seedBillingAccessToken(t, db, owner.ID, "我的令牌")

	records := []entity.BillingRecord{
		{UserID: owner.ID, AccessTokenID: token.ID, Type: "normal_search", Provider: "icodef", ModelName: "gpt-6-astra", Amount: 1, RequestID: "req-aaa-111", QuestionText: "题目一"},
		{UserID: owner.ID, Type: "normal_search", Provider: "zhihuishu", ModelName: "gpt-6-luna", Amount: 1, RequestID: "req-bbb-222", QuestionText: "题目二"},
	}
	for i := range records {
		if err := db.Create(&records[i]).Error; err != nil {
			t.Fatalf("create billing record: %v", err)
		}
	}

	// 两条记录属于同一个 owner，因此按用户名搜索命中 2 条；其余条件各命中 1 条。
	cases := []struct {
		name     string
		keyword  string
		wantRows int64
	}{
		{name: "username", keyword: "searchable-owner", wantRows: 2},
		{name: "provider", keyword: "zhihuishu", wantRows: 1},
		{name: "model id", keyword: "gpt-6-astra", wantRows: 1},
		{name: "request id fragment", keyword: "bbb", wantRows: 1},
		{name: "access token name", keyword: "我的令牌", wantRows: 1},
		{name: "no match", keyword: "完全不存在", wantRows: 0},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: testCase.keyword}, 1, 10)
			if err != nil {
				t.Fatalf("search billing records: %v", err)
			}
			if result.Total != testCase.wantRows {
				t.Fatalf("expected %d match(es) for %q, got total=%d rows=%d", testCase.wantRows, testCase.keyword, result.Total, len(result.Data))
			}
			if int64(len(result.Data)) != result.Total {
				t.Fatalf("expected returned rows to match total, got total=%d rows=%d", result.Total, len(result.Data))
			}
		})
	}
}

func TestListBillingRecordsWithDBFiltersByType(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	records := []entity.BillingRecord{
		{UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 1, RequestID: "req-type-search"},
		{UserID: owner.ID, Type: "recharge", Provider: "epay", Amount: 50},
		{UserID: owner.ID, Type: "registration_bonus", Amount: 5},
	}
	for i := range records {
		if err := db.Create(&records[i]).Error; err != nil {
			t.Fatalf("create billing record: %v", err)
		}
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Type: "recharge"}, 1, 10)
	if err != nil {
		t.Fatalf("filter billing records by type: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("expected one recharge row, got total=%d rows=%d", result.Total, len(result.Data))
	}
	if result.Data[0].Type != "recharge" {
		t.Fatalf("unexpected row type: %q", result.Data[0].Type)
	}

	// 类型与关键词可以叠加。
	result, err = ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Type: "normal_search", Keyword: "req-type-search"}, 1, 10)
	if err != nil {
		t.Fatalf("filter billing records by type and keyword: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected combined filter to return 1 row, got %d", result.Total)
	}

	// total 必须与筛选口径一致：不带筛选时应看到全部 3 条。
	all, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list all billing records: %v", err)
	}
	if all.Total != 3 {
		t.Fatalf("expected 3 rows without filters, got %d", all.Total)
	}
}

// 关键词里的 LIKE 通配符必须按字面量处理。
func TestListBillingRecordsWithDBKeywordTreatsLikeWildcardsLiterally(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	records := []entity.BillingRecord{
		{UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 1, RequestID: "req-plain", QuestionText: "普通题目"},
		{UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 1, RequestID: "req-under_score", QuestionText: "含_下划线题目"},
	}
	for i := range records {
		if err := db.Create(&records[i]).Error; err != nil {
			t.Fatalf("create billing record: %v", err)
		}
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "%"}, 1, 10)
	if err != nil {
		t.Fatalf("search with percent keyword: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("expected %% to match nothing, got %d", result.Total)
	}

	result, err = ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "_"}, 1, 10)
	if err != nil {
		t.Fatalf("search with underscore keyword: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected only the underscore record, got %d", result.Total)
	}
	if result.Data[0].QuestionText != "含_下划线题目" {
		t.Fatalf("unexpected matched row: %+v", result.Data[0])
	}
}

// 「累计消费金额」必须覆盖全部筛选结果，而不是只统计当前页；
// 且同一笔请求的多渠道明细只算一次（与列表展示口径一致）。
func TestListBillingRecordsWithDBSummaryCoversAllPagesWithoutDoubleCounting(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	// 同一笔请求命中两个渠道：两条记录合计 2.00，但只能计一次 2.00。
	merged := []entity.BillingRecord{
		{UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 1.25, RequestID: "req-merged"},
		{UserID: owner.ID, Type: "normal_search", Provider: "ai", Amount: 0.75, RequestID: "req-merged"},
	}
	for i := range merged {
		if err := db.Create(&merged[i]).Error; err != nil {
			t.Fatalf("create merged record: %v", err)
		}
	}

	// 另外 25 条独立请求，每条 1 元，用于跨页统计。
	for i := 0; i < 25; i++ {
		record := entity.BillingRecord{
			UserID:    owner.ID,
			Type:      "normal_search",
			Provider:  "icodef",
			Amount:    1,
			RequestID: fmt.Sprintf("req-single-%02d", i),
		}
		if err := db.Create(&record).Error; err != nil {
			t.Fatalf("create single record: %v", err)
		}
	}

	// 充值入账属于收入，不计入消费金额。
	if err := db.Create(&entity.BillingRecord{
		UserID: owner.ID, Type: "recharge", Provider: "epay", Amount: 100,
	}).Error; err != nil {
		t.Fatalf("create recharge record: %v", err)
	}

	firstPage, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}

	// 全部页合计：合并请求 2.00 + 25 条独立请求 25.00 = 27.00（充值 100 不计）。
	if firstPage.Summary.TotalAmount != 27 {
		t.Fatalf("expected all-page total 27, got %v", firstPage.Summary.TotalAmount)
	}
	if firstPage.Summary.CurrentPageAmount <= 0 {
		t.Fatalf("expected a positive current page amount, got %v", firstPage.Summary.CurrentPageAmount)
	}
	if firstPage.Summary.CurrentPageAmount > firstPage.Summary.TotalAmount {
		t.Fatalf("current page amount %v must not exceed the all-page total %v",
			firstPage.Summary.CurrentPageAmount, firstPage.Summary.TotalAmount)
	}

	// 第二页的「累计」必须保持不变，只有「当前页」变化。
	secondPage, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 2, 10)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if secondPage.Summary.TotalAmount != firstPage.Summary.TotalAmount {
		t.Fatalf("expected the all-page total to be page independent, got %v then %v",
			firstPage.Summary.TotalAmount, secondPage.Summary.TotalAmount)
	}
	if secondPage.Summary.CurrentPageAmount == firstPage.Summary.CurrentPageAmount {
		t.Fatalf("expected different current page amounts on page 2, both %v", secondPage.Summary.CurrentPageAmount)
	}

	// 筛选后「累计」应随筛选结果收窄。
	filtered, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "req-single"}, 1, 10)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if filtered.Summary.TotalAmount != 25 {
		t.Fatalf("expected filtered total 25, got %v", filtered.Summary.TotalAmount)
	}
	if filtered.Summary.TotalAmount >= firstPage.Summary.TotalAmount {
		t.Fatalf("expected the filtered total to be smaller, got %v vs %v",
			filtered.Summary.TotalAmount, firstPage.Summary.TotalAmount)
	}
}

// 回归：新用户赠送账单的 provider 是 "system"（界面标签「系统赠送」），
// 早期实现用 `provider <> 'system'` 过滤，会把它从「每笔请求消费总额」里误排除，
// 造成「逐行金额相加」与「累计消费金额」对不上。
func TestListBillingRecordsWithDBSummaryIgnoresSystemProviderBonusRows(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	// 真实数据形态：赠送行 provider=system、type=registration_bonus、request_id 非空。
	if err := db.Create(&entity.BillingRecord{
		UserID:    owner.ID,
		Type:      "registration_bonus",
		Provider:  "system",
		Amount:    50,
		RequestID: fmt.Sprintf("registration_bonus_%d", owner.ID),
		Remark:    "新用户赠送",
	}).Error; err != nil {
		t.Fatalf("create bonus record: %v", err)
	}
	// 充值行也是收入。
	if err := db.Create(&entity.BillingRecord{
		UserID: owner.ID, Type: "recharge", Provider: "epay", Amount: 100,
	}).Error; err != nil {
		t.Fatalf("create recharge record: %v", err)
	}
	// 一笔真实消费。
	if err := db.Create(&entity.BillingRecord{
		UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 1.5, RequestID: "req-real",
	}).Error; err != nil {
		t.Fatalf("create search record: %v", err)
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list billing records: %v", err)
	}

	// 只有题目消费计入：1.5；赠送 50 与充值 100 都不算。
	if result.Summary.TotalAmount != 1.5 {
		t.Fatalf("expected only the 1.5 search charge, got %v", result.Summary.TotalAmount)
	}
	if result.Summary.CurrentPageAmount != 1.5 {
		t.Fatalf("expected current page amount 1.5, got %v", result.Summary.CurrentPageAmount)
	}

	// 逐行金额相加必须等于「累计」：收入行按方向排除，消费行纳入。
	pageSum := 0.0
	for _, row := range result.Data {
		if row.Direction == "expense" && row.Amount > 0 {
			pageSum += row.Amount
		}
	}
	if pageSum != result.Summary.CurrentPageAmount {
		t.Fatalf("expected row amounts to sum to the page total: rows=%v summary=%v", pageSum, result.Summary.CurrentPageAmount)
	}
}

// 回归：关键词筛选必须同时作用于「逐行金额」与「累计消费金额」。
//
// 早期实现里，每笔请求的消费总额来自不带筛选条件的全量聚合，
// 于是按关键词筛掉某个渠道后，代表行的金额仍会变回整笔合计，
// 造成「列表只显示 1 行、金额却是整笔」的口径不一致。
func TestListBillingRecordsWithDBSummaryFollowsKeywordFilter(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	// 同一笔请求命中两个渠道：icodef 100 + ai 0.5
	merged := []entity.BillingRecord{
		{UserID: owner.ID, Type: "normal_search", Provider: "icodef", Amount: 100, RequestID: "req-merged"},
		{UserID: owner.ID, Type: "normal_search", Provider: "ai", Amount: 0.5, RequestID: "req-merged"},
	}
	for i := range merged {
		if err := db.Create(&merged[i]).Error; err != nil {
			t.Fatalf("create merged record: %v", err)
		}
	}
	// 另一笔独立请求，用于验证筛选确实会收窄结果。
	if err := db.Create(&entity.BillingRecord{
		UserID: owner.ID, Type: "normal_search", Provider: "zhihuishu", Amount: 7, RequestID: "req-other",
	}).Error; err != nil {
		t.Fatalf("create other record: %v", err)
	}

	all, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	// 全量：合并请求 100.5 + 独立请求 7 = 107.5
	if all.Summary.TotalAmount != 107.5 {
		t.Fatalf("expected all-page total 107.5, got %v", all.Summary.TotalAmount)
	}

	// 按渠道关键词筛到请求组内的 ai 明细时，代表行仍呈现完整请求金额与详情，
	// 独立请求被排除；同一请求不能只显示部分金额却挂载全部费用详情。
	filtered, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{Keyword: "ai"}, 1, 10)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if filtered.Summary.TotalAmount != 100.5 {
		t.Fatalf("expected filtered total 100.5 (the full matched request group), got %v", filtered.Summary.TotalAmount)
	}
	if filtered.Summary.TotalAmount == all.Summary.TotalAmount {
		t.Fatal("expected the filter to narrow the all-page total")
	}

	// 逐行金额相加必须等于当前页小计。
	pageSum := 0.0
	for _, row := range filtered.Data {
		if row.Direction == "expense" && row.Amount > 0 {
			pageSum += row.Amount
		}
	}
	if pageSum != filtered.Summary.CurrentPageAmount {
		t.Fatalf("expected row amounts to sum to the page total: rows=%v summary=%v", pageSum, filtered.Summary.CurrentPageAmount)
	}
	if pageSum != filtered.Summary.TotalAmount {
		t.Fatalf("expected the single-page filtered total to equal the page sum: %v vs %v", pageSum, filtered.Summary.TotalAmount)
	}
}

func TestListBillingRecordsWithDBUsesCachedSourceLabelForLocalSearch(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	record := entity.BillingRecord{
		UserID:             owner.ID,
		Type:               "normal_search",
		UsedDefaultAccount: true,
		Provider:           "local",
		Amount:             1.25,
		RequestID:          "req-local-cache",
		UnitPriceSnapshot:  "provider=local flatPrice=1.25 sourceName=万能题库",
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create cached local billing record: %v", err)
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list cached local billing records: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one record, got %+v", result.Data)
	}
	if result.Data[0].ProviderLabel != "万能题库（缓存）" {
		t.Fatalf("expected cached provider label 万能题库（缓存）, got %q", result.Data[0].ProviderLabel)
	}
	if result.Data[0].AnswerSourceLabel != "万能题库（缓存）" {
		t.Fatalf("expected cached answer source label 万能题库（缓存）, got %q", result.Data[0].AnswerSourceLabel)
	}
}

func TestListBillingRecordsWithDBUsesProviderLabelAsAnswerSourceForRemoteSearch(t *testing.T) {
	db := newBillingRecordTestDB(t)
	owner := seedBillingRecordUser(t, db, 2, "user")

	record := entity.BillingRecord{
		UserID:             owner.ID,
		Type:               "normal_search",
		UsedDefaultAccount: true,
		Provider:           "icodef",
		Amount:             2.5,
		RequestID:          "req-answer-source-remote",
		UnitPriceSnapshot:  "provider=icodef flatPrice=2.50 question=%E8%BF%9C%E7%A8%8B%E9%A2%98%E7%9B%AE",
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create remote billing record: %v", err)
	}

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list remote billing records: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one record, got %+v", result.Data)
	}
	if result.Data[0].AnswerSourceLabel != "Icodef 题库" {
		t.Fatalf("expected remote answer source label Icodef 题库, got %q", result.Data[0].AnswerSourceLabel)
	}
}
