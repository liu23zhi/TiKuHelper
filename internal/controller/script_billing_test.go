package controller

import (
	"testing"

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
	charges, warning, err := resolveScriptAnswerBillingCharge(db, independent.ID, model.SearchRequest{Question: "独立价格题"}, search.AISearchUsage{}, true)
	if err != nil || warning != "" {
		t.Fatalf("independent price resolution failed: charges=%+v warning=%q err=%v", charges, warning, err)
	}
	if len(charges) != 1 || charges[0].Provider != "script_answer" || charges[0].Amount != 0.375 {
		t.Fatalf("expected independent script price, got %+v", charges)
	}

	tiku := entity.UserScript{UserID: 1, Enabled: true, BillingEnabled: true, UseTikuPrice: true, AnswerPrice: 9.99, BaseDomain: "a.com", ShortCode: "b"}
	if err := db.Create(&tiku).Error; err != nil {
		t.Fatal(err)
	}
	charges, _, err = resolveScriptAnswerBillingCharge(db, tiku.ID, model.SearchRequest{Question: "题库价格题"}, search.AISearchUsage{}, true)
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
	charges, _, err := resolveScriptAnswerBillingCharge(db, script.ID, model.SearchRequest{Question: "disabled"}, search.AISearchUsage{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(charges) != 0 {
		t.Fatalf("disabled script must not be charged: %+v", charges)
	}
}
