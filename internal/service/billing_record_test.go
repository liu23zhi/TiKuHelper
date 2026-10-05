package service

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
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

	ownerResult, err := ListBillingRecordsWithDB(db, owner, 1, 10)
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

	adminResult, err := ListBillingRecordsWithDB(db, admin, 1, 10)
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

	result, err := ListBillingRecordsWithDB(db, owner, 1, 10)
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

	result, err := ListBillingRecordsWithDB(db, owner, 2, 1)
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

	result, err := ListBillingRecordsWithDB(db, owner, 1, 10)
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

	result, err := ListBillingRecordsWithDB(db, owner, 1, 10)
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

	result, err := ListBillingRecordsWithDB(db, owner, 1, 10)
	if err != nil {
		t.Fatalf("list billing records with question text: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one record, got %+v", result.Data)
	}
	if result.Data[0].QuestionText != "这是账单题目" {
		t.Fatalf("expected question text 这是账单题目, got %q", result.Data[0].QuestionText)
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

	result, err := ListBillingRecordsWithDB(db, owner, 1, 10)
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

	result, err := ListBillingRecordsWithDB(db, owner, 1, 10)
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
