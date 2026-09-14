package service

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newAIModelPricingTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:ai-model-pricing-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.AISearchConfig{}, &entity.AIModelArchitecture{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	return db
}

func boolPtr(value bool) *bool {
	return &value
}

func TestCreateAIModelArchitectureWithDBTrimsFieldsAndPersists(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	created, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:              " GPT-4o ",
		BillingMultiplier: 1.25,
		Enabled:           true,
		Sort:              20,
		Remark:            " 默认架构 ",
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	if created.Name != "GPT-4o" {
		t.Fatalf("expected trimmed name, got %q", created.Name)
	}
	if created.BillingMultiplier != 1.25 {
		t.Fatalf("unexpected created multiplier: %+v", created)
	}
	if !created.Enabled {
		t.Fatal("expected created architecture enabled")
	}
	if created.Sort != 20 || created.Remark != "默认架构" {
		t.Fatalf("unexpected created architecture fields: %+v", created)
	}

	var stored entity.AIModelArchitecture
	if err := db.Where("id = ?", created.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored architecture: %v", err)
	}
	if stored.Name != "GPT-4o" || stored.BillingMultiplier != 1.25 {
		t.Fatalf("unexpected stored architecture: %+v", stored)
	}
}

func TestListAIModelArchitecturesWithDBFiltersDisabledAndSortsAscending(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	items := []entity.AIModelArchitecture{
		{Name: "GPT-4o", BillingMultiplier: 2, Enabled: true, Sort: 30},
		{Name: "DeepSeek-V3", ModelName: "deepseek-chat", BillingMultiplier: 3, Enabled: false, Sort: 10},
		{Name: "Claude 3.5", ModelName: "claude-3-5-sonnet", BillingMultiplier: 1.5, Enabled: true, Sort: 10},
	}
	for _, item := range items {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("seed architecture: %v", err)
		}
	}

	adminList, err := ListAIModelArchitecturesWithDB(db, true)
	if err != nil {
		t.Fatalf("list architectures: %v", err)
	}
	if len(adminList) != 3 {
		t.Fatalf("expected 3 architectures, got %d", len(adminList))
	}
	if adminList[0].Name != "DeepSeek-V3" || adminList[1].Name != "Claude 3.5" || adminList[2].Name != "GPT-4o" {
		t.Fatalf("unexpected admin order: %+v", adminList)
	}

	enabledList, err := ListAIModelArchitecturesWithDB(db, false)
	if err != nil {
		t.Fatalf("list enabled architectures: %v", err)
	}
	if len(enabledList) != 2 {
		t.Fatalf("expected 2 enabled architectures, got %d", len(enabledList))
	}
	if enabledList[0].Name != "Claude 3.5" || enabledList[1].Name != "GPT-4o" {
		t.Fatalf("unexpected enabled order: %+v", enabledList)
	}
}

func TestResolveAIModelPriceWithDBMatchesModelNameBeforeLegacyFallback(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	legacyView, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:                    true,
		BaseURL:                    "https://api.openai.com/v1",
		APIKey:                     "sk-test",
		Model:                      "gpt-4o-mini",
		InputTokenPrice:            0.88,
		OutputTokenPrice:           1.66,
		UseGlobalBillingMultiplier: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("seed legacy ai config: %v", err)
	}
	if legacyView.InputTokenPrice != 0.88 || legacyView.OutputTokenPrice != 1.66 {
		t.Fatalf("unexpected legacy view: %+v", legacyView)
	}

	architecture, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:              "GPT-4o",
		ModelName:         "gpt-4o",
		BillingMultiplier: 2.4,
		Enabled:           true,
		Sort:              10,
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	resolved, err := ResolveAIModelPriceWithDB(db, "gpt-4o")
	if err != nil {
		t.Fatalf("resolve architecture price: %v", err)
	}
	if resolved.ModelName != "gpt-4o" || resolved.ArchitectureID != architecture.ID {
		t.Fatalf("unexpected resolved architecture identity: %+v", resolved)
	}
	if resolved.BillingMultiplier != 2.4 {
		t.Fatalf("expected architecture multiplier, got %+v", resolved)
	}
	if resolved.Source != "architecture" {
		t.Fatalf("expected architecture source, got %+v", resolved)
	}

	resolvedLegacy, err := ResolveAIModelPriceWithDB(db, "claude-3.5-sonnet")
	if err != nil {
		t.Fatalf("resolve fallback to legacy: %v", err)
	}
	if resolvedLegacy.BillingMultiplier != 1.66 {
		t.Fatalf("expected legacy fallback multiplier from old prices, got %+v", resolvedLegacy)
	}
	if resolvedLegacy.Source != "legacy" {
		t.Fatalf("expected legacy source, got %+v", resolvedLegacy)
	}
}

func TestResolveAIModelPriceWithDBUsesDefaultGlobalMultiplier(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:                 true,
		BaseURL:                 "https://api.openai.com/v1",
		APIKey:                  "sk-test",
		Model:                   "gpt-global",
		ExchangeRate:            7.2,
		GlobalBillingMultiplier: 1.5,
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}
	_, err = CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:              "GPT Global",
		ModelName:         "gpt-global",
		BillingMultiplier: 2.4,
		Enabled:           true,
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	resolved, err := ResolveAIModelPriceWithDB(db, "gpt-global")
	if err != nil {
		t.Fatalf("resolve global price: %v", err)
	}

	if resolved.BillingMultiplier != 10.8 {
		t.Fatalf("expected exchange rate multiplied by global multiplier, got %+v", resolved)
	}
	if resolved.Source != "global" {
		t.Fatalf("expected global source, got %+v", resolved)
	}
}

func TestResolveAIModelPriceWithDBCanDisableGlobalMultiplier(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:                    true,
		BaseURL:                    "https://api.openai.com/v1",
		APIKey:                     "sk-test",
		Model:                      "gpt-per-model",
		ExchangeRate:               7.2,
		UseGlobalBillingMultiplier: boolPtr(false),
		GlobalBillingMultiplier:    1.5,
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}
	architecture, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:              "GPT Per Model",
		ModelName:         "gpt-per-model",
		BillingMultiplier: 2.4,
		Enabled:           true,
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	resolved, err := ResolveAIModelPriceWithDB(db, "gpt-per-model")
	if err != nil {
		t.Fatalf("resolve per model price: %v", err)
	}

	if resolved.ArchitectureID != architecture.ID {
		t.Fatalf("expected architecture identity, got %+v", resolved)
	}
	if resolved.BillingMultiplier != 17.28 {
		t.Fatalf("expected exchange rate multiplied by model multiplier, got %+v", resolved)
	}
	if resolved.Source != "architecture" {
		t.Fatalf("expected architecture source, got %+v", resolved)
	}
}

func TestUpdateAIModelArchitectureWithDBUpdatesModelBindingAndCanDisable(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	architecture, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:              "DeepSeek-V3",
		ModelName:         "deepseek-chat",
		BillingMultiplier: 1.1,
		Enabled:           true,
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	updated, err := UpdateAIModelArchitectureWithDB(db, architecture.ID, UpdateAIModelArchitectureInput{
		Name:              " DeepSeek-V3 ",
		ModelName:         " deepseek-reasoner ",
		BillingMultiplier: 3.1,
		Enabled:           false,
		Sort:              7,
		Remark:            " 已停用 ",
	})
	if err != nil {
		t.Fatalf("update architecture: %v", err)
	}

	if updated.Name != "DeepSeek-V3" || updated.ModelName != "deepseek-reasoner" {
		t.Fatalf("unexpected updated architecture fields: %+v", updated)
	}
	if updated.Enabled {
		t.Fatalf("expected architecture to be disabled, got %+v", updated)
	}
	if updated.Remark != "已停用" || updated.Sort != 7 {
		t.Fatalf("unexpected updated metadata: %+v", updated)
	}

	resolved, err := ResolveAIModelPriceWithDB(db, "deepseek-reasoner")
	if err != nil {
		t.Fatalf("resolve disabled architecture: %v", err)
	}
	if resolved.Source != "legacy" {
		t.Fatalf("expected legacy fallback after disable, got %+v", resolved)
	}
	if resolved.BillingMultiplier != 1 {
		t.Fatalf("expected default multiplier fallback without legacy config, got %+v", resolved)
	}
}

func TestCreateAIModelArchitectureWithDBRequiresName(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	if _, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{}); err == nil {
		t.Fatal("expected empty architecture name to fail")
	}
}
