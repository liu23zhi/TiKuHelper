package controller

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func newScriptBillingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:script-billing-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}, &entity.UserScript{}, &entity.ScriptVersion{}, &entity.ScriptExchangeToken{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func resolveFixtureScriptTariff(db *gorm.DB, scriptID int32, req model.SearchRequest) ([]searchBillingCharge, string, error) {
	var script entity.UserScript
	if err := db.First(&script, scriptID).Error; err != nil {
		return nil, "", err
	}
	tariff, err := snapshotScriptAnswerTariff(db, script)
	if err != nil || !script.Enabled || !tariff.enabled {
		return nil, "", err
	}
	return []searchBillingCharge{tariff.charge(req)}, "", nil
}

func TestScriptAnswerBillingUsesIndependentPriceOrTikuPrice(t *testing.T) {
	db := newScriptBillingTestDB(t)
	if err := service.SetTikuSearchPricePerCallWithDB(db, 0.125); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.User{Username: "student", Role: "user", Balance: 100}).Error; err != nil {
		t.Fatal(err)
	}
	independent := entity.UserScript{UserID: 1, Enabled: true, BillingEnabled: true, UseTikuPrice: false, AnswerPrice: 0.375, BaseDomain: "a.com", ShortCode: "a"}
	if err := db.Create(&independent).Error; err != nil {
		t.Fatal(err)
	}
	charges, warning, err := resolveFixtureScriptTariff(db, independent.ID, model.SearchRequest{Question: "独立价格题"})
	if err != nil || warning != "" {
		t.Fatalf("independent price resolution failed: charges=%+v warning=%q err=%v", charges, warning, err)
	}
	if len(charges) != 1 || charges[0].Provider != "script_answer" || charges[0].Amount != 0.375 {
		t.Fatalf("expected independent script price, got %+v", charges)
	}

	free := entity.UserScript{UserID: 1, Enabled: true, BillingEnabled: true, UseTikuPrice: false, AnswerPriceMultiplier: 0, BaseDomain: "a.com", ShortCode: "free"}
	if err := db.Create(&free).Error; err != nil {
		t.Fatal(err)
	}
	charges, _, err = resolveFixtureScriptTariff(db, free.ID, model.SearchRequest{Question: "免费题"})
	if err != nil || len(charges) != 1 || charges[0].Amount != 0 {
		t.Fatalf("expected 0X script price, got charges=%+v err=%v", charges, err)
	}

	tiku := entity.UserScript{UserID: 1, Enabled: true, BillingEnabled: true, UseTikuPrice: true, AnswerPrice: 9.99, BaseDomain: "a.com", ShortCode: "b"}
	if err := db.Create(&tiku).Error; err != nil {
		t.Fatal(err)
	}
	charges, _, err = resolveFixtureScriptTariff(db, tiku.ID, model.SearchRequest{Question: "题库价格题"})
	if err != nil {
		t.Fatal(err)
	}
	if len(charges) != 1 || charges[0].Amount != 0.125 {
		t.Fatalf("expected tiku price, got %+v", charges)
	}
}

func TestScriptAnswerBillingSkipsDisabledScript(t *testing.T) {
	db := newScriptBillingTestDB(t)
	script := entity.UserScript{UserID: 1, Enabled: false, BillingEnabled: true, AnswerPrice: 1, BaseDomain: "a.com", ShortCode: "a"}
	if err := db.Create(&script).Error; err != nil {
		t.Fatal(err)
	}
	charges, _, err := resolveFixtureScriptTariff(db, script.ID, model.SearchRequest{Question: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	if len(charges) != 0 {
		t.Fatalf("disabled script must not be charged: %+v", charges)
	}
}

func TestScriptSettlementKeepsFrozenTariffAndExecutedToolUsage(t *testing.T) {
	db := newScriptBillingTestDB(t)
	if err := service.SetTikuSearchPricePerCallWithDB(db, 0.125001); err != nil {
		t.Fatal(err)
	}
	user := entity.User{Username: "script-settlement-fixture", Role: "user", Balance: 0.000001}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	script := entity.UserScript{UserID: user.ID, Enabled: true, BillingEnabled: true, UseTikuPrice: true, BaseDomain: "fixture.test", ShortCode: "a"}
	if err := db.Create(&script).Error; err != nil {
		t.Fatal(err)
	}
	token := entity.AccessToken{UserID: user.ID, ScriptID: script.ID, Status: "enabled", TotalQuotaLimit: 10}
	if err := db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	if err := checkSearchSpendingAdmission(user, token); err != nil {
		t.Fatal(err)
	}
	frozen, err := snapshotScriptAnswerTariff(db, script)
	if err != nil {
		t.Fatal(err)
	}
	// Change price and retire the script after the call was admitted.
	if err := service.SetTikuSearchPricePerCallWithDB(db, 7); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&script).Updates(map[string]any{"billing_enabled": false, "deleted_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&token).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	resolved := resolvedSearchRequestContext{
		UserID: user.ID, AccessTokenID: token.ID, ScriptID: script.ID, ScriptTariff: frozen,
		ScriptHadNormalAnswer: true, RequestID: "script-settlement", SearchMode: searchModeNormal,
	}
	usage := search.AISearchUsage{SearchCalls: []search.AISearchCallUsage{{}}}
	charges, _, err := resolveCompletedSearchBillingCharges(db, resolved, "", model.SearchRequest{Question: "fixture"}, usage, false, true)
	if err != nil || len(charges) != 2 || charges[0].Amount != 0.125001 || charges[0].Provider != "script_answer" || charges[1].Provider != "ai_search" {
		t.Fatalf("frozen normal tariff and actual tool row both required, charges=%+v err=%v", charges, err)
	}
	failed, _, err := resolveCompletedSearchBillingCharges(db, resolved, "", model.SearchRequest{Question: "fixture"}, usage, false, false)
	if err != nil || len(failed) != 1 || failed[0].Provider != "ai_search" {
		t.Fatalf("no answer must still preserve executed tool usage, charges=%+v err=%v", failed, err)
	}
	ctx := searchBillingContext{UserID: user.ID, AccessTokenID: token.ID, ScriptID: script.ID, RequestID: resolved.RequestID}
	for i := 0; i < 2; i++ {
		if err := persistSearchBillingRecordsWithDB(db, ctx, charges); err != nil {
			t.Fatalf("settlement/retry %d: %v", i, err)
		}
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	var bills int64
	if err := db.Model(&entity.BillingRecord{}).Where("request_id = ?", resolved.RequestID).Count(&bills).Error; err != nil {
		t.Fatal(err)
	}
	if bills != 2 || user.Balance >= 0 {
		t.Fatalf("executed work must settle once and may leave debt, count=%d balance=%.6f", bills, user.Balance)
	}
}
