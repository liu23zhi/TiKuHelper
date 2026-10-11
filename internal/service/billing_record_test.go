package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
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

	result, err := ListBillingRecordsWithDB(db, owner, BillingRecordListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list merged billing records: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("expected mixed search charges to merge into one row, got %+v", result)
	}

	merged := result.Data[0]
	if merged.ID != aiRecord.ID {
		t.Fatalf("expected latest search charge id %d to represent merged row, got %d", aiRecord.ID, merged.ID)
	}
	if merged.Amount != 2 {
		t.Fatalf("expected merged amount 2.00, got %.8f", merged.Amount)
	}
	if len(merged.FeeDetails) != 2 {
		t.Fatalf("expected two fee details, got %+v", merged.FeeDetails)
	}
	if merged.FeeDetails[0].ProviderLabel != "Icodef 题库" || merged.FeeDetails[0].Amount != 1.25 {
		t.Fatalf("unexpected normal fee detail: %+v", merged.FeeDetails[0])
	}
	if merged.FeeDetails[1].ProviderLabel != "AI 搜索" || merged.FeeDetails[1].Amount != 0.75 {
		t.Fatalf("unexpected ai fee detail: %+v", merged.FeeDetails[1])
	}
	if merged.QuestionText != "混合搜索题目" {
		t.Fatalf("expected question text from normal charge, got %q", merged.QuestionText)
	}
	if merged.Subject != "搜索答案，题目：混合搜索题目" {
		t.Fatalf("expected merged subject to identify a search answer and question, got %q", merged.Subject)
	}
	if merged.ProviderLabel != "Icodef 题库、AI 搜索" {
		t.Fatalf("expected merged provider label, got %q", merged.ProviderLabel)
	}
	if merged.AnswerSourceLabel != "Icodef 题库、AI 搜索" {
		t.Fatalf("expected merged answer source label, got %q", merged.AnswerSourceLabel)
	}
	if merged.ReferenceNo != "req-mixed-search" || merged.AccessTokenName != "混合搜索令牌" {
		t.Fatalf("expected merged identity fields to be preserved, got %+v", merged)
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
