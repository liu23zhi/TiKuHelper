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
		Name:      " GPT-4o ",
		ModelName: " gpt-4o ",
		Enabled:   true,
		Sort:      20,
		Remark:    " 默认架构 ",
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	if created.Name != "GPT-4o" || created.ModelName != "gpt-4o" {
		t.Fatalf("expected trimmed fields, got %+v", created)
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
	if stored.Name != "GPT-4o" || stored.ModelName != "gpt-4o" {
		t.Fatalf("unexpected stored architecture: %+v", stored)
	}
}

// 模型架构表只负责模型绑定，不应再承载任何计费字段。
func TestAIModelArchitectureNoLongerCarriesBillingFields(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	if err := db.Create(&entity.AIModelArchitecture{Name: "GPT-4o", ModelName: "gpt-4o", Enabled: true}).Error; err != nil {
		t.Fatalf("seed architecture: %v", err)
	}

	columns, err := db.Migrator().ColumnTypes(&entity.AIModelArchitecture{})
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, strings.ToLower(column.Name()))
	}

	for _, forbidden := range []string{"input_token_price", "output_token_price", "billing_multiplier"} {
		for _, name := range names {
			if name == forbidden {
				t.Fatalf("expected billing column %q to be gone, got columns %v", forbidden, names)
			}
		}
	}
}

func TestListAIModelArchitecturesWithDBFiltersDisabledAndSortsAscending(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	items := []entity.AIModelArchitecture{
		{Name: "GPT-4o", Enabled: true, Sort: 30},
		{Name: "DeepSeek-V3", ModelName: "deepseek-chat", Enabled: false, Sort: 10},
		{Name: "Claude 3.5", ModelName: "claude-3-5-sonnet", Enabled: true, Sort: 10},
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

func TestUpdateAIModelArchitectureWithDBUpdatesModelBindingAndCanDisable(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	architecture, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{
		Name:      "DeepSeek-V3",
		ModelName: "deepseek-chat",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create architecture: %v", err)
	}

	updated, err := UpdateAIModelArchitectureWithDB(db, architecture.ID, UpdateAIModelArchitectureInput{
		Name:      " DeepSeek-V3 ",
		ModelName: " deepseek-reasoner ",
		Enabled:   false,
		Sort:      7,
		Remark:    " 已停用 ",
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

	enabledList, err := ListAIModelArchitecturesWithDB(db, false)
	if err != nil {
		t.Fatalf("list enabled architectures: %v", err)
	}
	if len(enabledList) != 0 {
		t.Fatalf("expected disabled architecture to be filtered out, got %+v", enabledList)
	}
}

func TestUpdateAIModelArchitectureWithDBRejectsUnknownID(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	if _, err := UpdateAIModelArchitectureWithDB(db, 404, UpdateAIModelArchitectureInput{Name: "GPT-4o"}); err == nil {
		t.Fatal("expected unknown architecture id to fail")
	}
}

func TestCreateAIModelArchitectureWithDBRequiresName(t *testing.T) {
	db := newAIModelPricingTestDB(t)

	if _, err := CreateAIModelArchitectureWithDB(db, CreateAIModelArchitectureInput{}); err == nil {
		t.Fatal("expected empty architecture name to fail")
	}
}
