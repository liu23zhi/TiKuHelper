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

func TestCreateAIModelArchitectureWithDBTrimsFieldsAndPersists(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	created, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:             " GPT-4o ",
		InputTokenPrice:  0.12,
		OutputTokenPrice: 0.34,
		Enabled:          true,
		Sort:             20,
		Remark:           " 默认架构 ",
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	if created.Name != "GPT-4o" {
		t.Fatalf("expected trimmed name, got %q", created.Name)
	}
	if created.InputTokenPrice != 0.12 || created.OutputTokenPrice != 0.34 {
		t.Fatalf("unexpected created price: %+v", created)
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
	if stored.Name != "GPT-4o" || stored.InputTokenPrice != 0.12 || stored.OutputTokenPrice != 0.34 {
		t.Fatalf("unexpected stored architecture: %+v", stored)
	}
}

func TestListAIModelArchitecturesWithDBFiltersDisabledAndSortsAscending(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	items := []entity.AIModelArchitecture{
		{Name: "GPT-4o", InputTokenPrice: 0.2, OutputTokenPrice: 0.8, Enabled: true, Sort: 30},
		{Name: "DeepSeek-V3", ModelName: "deepseek-chat", InputTokenPrice: 0.1, OutputTokenPrice: 0.4, Enabled: false, Sort: 10},
		{Name: "Claude 3.5", ModelName: "claude-3-5-sonnet", InputTokenPrice: 0.15, OutputTokenPrice: 0.6, Enabled: true, Sort: 10},
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
		Enabled:          true,
		BaseURL:          "https://api.openai.com/v1",
		APIKey:           "sk-test",
		Model:            "gpt-4o-mini",
		InputTokenPrice:  0.88,
		OutputTokenPrice: 1.66,
	})
	if err != nil {
		t.Fatalf("seed legacy ai config: %v", err)
	}
	if legacyView.InputTokenPrice != 0.88 || legacyView.OutputTokenPrice != 1.66 {
		t.Fatalf("unexpected legacy view: %+v", legacyView)
	}

	architecture, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:             "GPT-4o",
		ModelName:        "gpt-4o",
		InputTokenPrice:  0.2,
		OutputTokenPrice: 0.4,
		Enabled:          true,
		Sort:             10,
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
	if resolved.InputTokenPrice != 0.2 || resolved.OutputTokenPrice != 0.4 {
		t.Fatalf("expected architecture prices, got %+v", resolved)
	}
	if resolved.Source != "architecture" {
		t.Fatalf("expected architecture source, got %+v", resolved)
	}

	resolvedLegacy, err := ResolveAIModelPriceWithDB(db, "claude-3.5-sonnet")
	if err != nil {
		t.Fatalf("resolve fallback to legacy: %v", err)
	}
	if resolvedLegacy.InputTokenPrice != 0.88 || resolvedLegacy.OutputTokenPrice != 1.66 {
		t.Fatalf("expected legacy fallback prices, got %+v", resolvedLegacy)
	}
	if resolvedLegacy.Source != "legacy" {
		t.Fatalf("expected legacy source, got %+v", resolvedLegacy)
	}
}

func TestUpdateAIModelArchitectureWithDBUpdatesModelBindingAndCanDisable(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	architecture, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:             "DeepSeek-V3",
		ModelName:        "deepseek-chat",
		InputTokenPrice:  0.11,
		OutputTokenPrice: 0.22,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	updated, err := UpdateAIModelArchitectureWithDB(db, architecture.ID, UpdateAIModelArchitectureInput{
		Name:             " DeepSeek-V3 ",
		ModelName:        " deepseek-reasoner ",
		InputTokenPrice:  0.31,
		OutputTokenPrice: 0.62,
		Enabled:          false,
		Sort:             7,
		Remark:           " 已停用 ",
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
	if resolved.InputTokenPrice != 0 || resolved.OutputTokenPrice != 0 {
		t.Fatalf("expected zero fallback prices without legacy config, got %+v", resolved)
	}
}

func TestCreateAIModelArchitectureWithDBRequiresName(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	if _, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{}); err == nil {
		t.Fatal("expected empty architecture name to fail")
	}
}
