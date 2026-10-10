package service

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newAIModelPriceTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:ai-model-price-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.AISearchConfig{}, &entity.AIModelPrice{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	return db
}

func TestDefaultAIModelPricesMatchAgreedPriceTable(t *testing.T) {
	if got := len(defaultAIModelPrices); got != 14 {
		t.Fatalf("expected 14 default model prices, got %d", got)
	}

	expected := map[string][3]float64{
		"GPT-6 Astra":                {10.00, 1.00, 50.00},
		"GPT-6.1 Sol":                {2.00, 0.10, 10.00},
		"GPT-6 Sol":                  {2.00, 0.20, 10.00},
		"GPT-6 Luna":                 {0.10, 0.01, 0.50},
		"GPT-5.6 Sol":                {4.00, 0.40, 20.00},
		"GPT-5.6 Terra":              {2.00, 0.20, 12.00},
		"GPT-5.6 Luna":               {0.20, 0.02, 1.20},
		"GPT-Rosalind-Research":      {5.00, 0.50, 25.00},
		"GPT-5.5":                    {5.00, 0.50, 30.00},
		"Daybreak Blue（GPT-5.6 Sol）": {4.00, 0.40, 20.00},
		"Daybreak Red":               {12.50, 1.25, 75.00},
		"GPT-5.3-Codex":              {1.75, 0.175, 14.00},
		"GPT-5.2":                    {1.75, 0.175, 14.00},
		"GPT-6 Astra Law *":          {12.50, 1.25, 62.50},
	}

	for _, item := range defaultAIModelPrices {
		want, ok := expected[item.ModelName]
		if !ok {
			t.Fatalf("unexpected seeded model %q", item.ModelName)
		}
		if item.InputPrice != want[0] || item.CachedPrice != want[1] || item.OutputPrice != want[2] {
			t.Fatalf("unexpected price for %q: got %v/%v/%v want %v/%v/%v",
				item.ModelName, item.InputPrice, item.CachedPrice, item.OutputPrice, want[0], want[1], want[2])
		}
	}
}

func TestEnsureDefaultAIModelPricesWithDBSeedsOnceAndKeepsAdminEdits(t *testing.T) {
	db := newAIModelPriceTestDB(t)

	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("re-seed defaults: %v", err)
	}

	var count int64
	if err := db.Model(&entity.AIModelPrice{}).Count(&count).Error; err != nil {
		t.Fatalf("count prices: %v", err)
	}
	if count != 14 {
		t.Fatalf("expected 14 rows after idempotent seeding, got %d", count)
	}

	var existing entity.AIModelPrice
	if err := db.Where("model_name = ?", "GPT-5.5").First(&existing).Error; err != nil {
		t.Fatalf("load seeded price: %v", err)
	}
	if _, err := UpdateAIModelPriceWithDB(db, existing.ID, UpdateAIModelPriceInput{
		ModelName:   "GPT-5.5",
		InputPrice:  9.99,
		CachedPrice: 0.99,
		OutputPrice: 99.99,
		Status:      AIModelPriceStatusEnabled,
	}); err != nil {
		t.Fatalf("update price: %v", err)
	}

	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("re-seed after edit: %v", err)
	}

	var updated entity.AIModelPrice
	if err := db.Where("model_name = ?", "GPT-5.5").First(&updated).Error; err != nil {
		t.Fatalf("reload price: %v", err)
	}
	if updated.InputPrice != 9.99 || updated.OutputPrice != 99.99 {
		t.Fatalf("expected admin edit to survive re-seeding, got %+v", updated)
	}
	if !updated.IsUserModified {
		t.Fatalf("expected admin edit to be flagged, got %+v", updated)
	}
}

func TestFindAIModelPriceMatchesNameIgnoringCaseAndSpaces(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	for _, candidate := range []string{"gpt-5.5", "  GPT-5.5  ", "GPT-5.5"} {
		resolved, found, err := ResolveAIModelTokenPriceWithDB(db, candidate)
		if err != nil {
			t.Fatalf("resolve %q: %v", candidate, err)
		}
		if !found {
			t.Fatalf("expected %q to match the price table", candidate)
		}
		if resolved.OutputPrice != 30 {
			t.Fatalf("unexpected output price for %q: %+v", candidate, resolved)
		}
	}

	resolved, found, err := ResolveAIModelTokenPriceWithDB(db, "GPT-5.6Sol")
	if err != nil {
		t.Fatalf("resolve normalized name: %v", err)
	}
	if !found || resolved.ModelName != "GPT-5.6 Sol" {
		t.Fatalf("expected whitespace-insensitive match, got found=%t %+v", found, resolved)
	}
}

func TestResolveAIModelTokenPriceWithDBUsesPriceTableValuesDirectly(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	if _, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://api.example.com/v1",
		APIKey:  "sk-test",
		Model:   "GPT-5.5",
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	resolved, found, err := ResolveAIModelTokenPriceWithDB(db, "GPT-5.5")
	if err != nil {
		t.Fatalf("resolve price: %v", err)
	}
	if !found {
		t.Fatal("expected GPT-5.5 to match the price table")
	}
	// 价格表即为唯一事实来源：不再经过任何倍率或汇率换算。
	if resolved.InputUnitPrice != 5 || resolved.CachedUnitPrice != 0.5 || resolved.OutputUnitPrice != 30 {
		t.Fatalf("unexpected unit prices: %+v", resolved)
	}
	if resolved.EffectiveUnitName != "元/百万token" {
		t.Fatalf("expected RMB unit label, got %q", resolved.EffectiveUnitName)
	}
}

func TestResolveAIModelTokenPriceWithDBReportsMissForUnknownModel(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	resolved, found, err := ResolveAIModelTokenPriceWithDB(db, "not-a-real-model")
	if err != nil {
		t.Fatalf("resolve unknown model: %v", err)
	}
	if found {
		t.Fatalf("expected unknown model to miss the price table, got %+v", resolved)
	}
}

func TestResolveAIModelTokenPriceWithDBIgnoresDisabledPrices(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	var seeded entity.AIModelPrice
	if err := db.Where("model_name = ?", "Daybreak Red").First(&seeded).Error; err != nil {
		t.Fatalf("load seeded price: %v", err)
	}
	if _, err := UpdateAIModelPriceWithDB(db, seeded.ID, UpdateAIModelPriceInput{
		ModelName:   seeded.ModelName,
		InputPrice:  seeded.InputPrice,
		CachedPrice: seeded.CachedPrice,
		OutputPrice: seeded.OutputPrice,
		Status:      AIModelPriceStatusDisabled,
	}); err != nil {
		t.Fatalf("disable price: %v", err)
	}

	if _, found, err := ResolveAIModelTokenPriceWithDB(db, "Daybreak Red"); err != nil {
		t.Fatalf("resolve disabled price: %v", err)
	} else if found {
		t.Fatal("expected disabled price to be skipped")
	}
}

func TestEnsureDefaultAIModelPricesDoesNotResurrectDeletedDefaults(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	// 管理员明确删掉某个默认模型后，不应该在下一次请求被自动补回。
	if err := db.Where("model_name = ?", "GPT-6 Astra").Delete(&entity.AIModelPrice{}).Error; err != nil {
		t.Fatalf("delete default price: %v", err)
	}
	aimodelPriceSeeded.Delete(aimodelPriceCacheKey(db))

	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("re-seed after delete: %v", err)
	}

	var count int64
	if err := db.Model(&entity.AIModelPrice{}).Where("model_name = ?", "GPT-6 Astra").Count(&count).Error; err != nil {
		t.Fatalf("count deleted price: %v", err)
	}
	if count != 0 {
		t.Fatal("expected deleted default price to stay deleted")
	}
}

func TestResetAIModelPricesWithDBRestoresDefaults(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	if err := db.Where("model_name = ?", "GPT-5.2").Delete(&entity.AIModelPrice{}).Error; err != nil {
		t.Fatalf("delete price: %v", err)
	}
	if _, err := CreateAIModelPriceWithDB(db, CreateAIModelPriceInput{ModelName: "Custom-Model", InputPrice: 1}); err != nil {
		t.Fatalf("create custom price: %v", err)
	}

	if err := ResetAIModelPricesWithDB(db); err != nil {
		t.Fatalf("reset prices: %v", err)
	}

	var count int64
	if err := db.Model(&entity.AIModelPrice{}).Count(&count).Error; err != nil {
		t.Fatalf("count prices: %v", err)
	}
	if count != 14 {
		t.Fatalf("expected reset to restore 14 defaults, got %d", count)
	}
	if _, found, err := ResolveAIModelTokenPriceWithDB(db, "Custom-Model"); err != nil {
		t.Fatalf("resolve custom model: %v", err)
	} else if found {
		t.Fatal("expected reset to drop custom model")
	}
}

func TestNormalizeAIModelPriceInputRejectsNegativeAndBlankValues(t *testing.T) {
	if _, err := CreateAIModelPriceWithDB(newAIModelPriceTestDB(t), CreateAIModelPriceInput{ModelName: "   "}); err == nil {
		t.Fatal("expected blank model name to be rejected")
	}

	db := newAIModelPriceTestDB(t)
	created, err := CreateAIModelPriceWithDB(db, CreateAIModelPriceInput{
		ModelName:   " Negative-Model ",
		InputPrice:  -1,
		CachedPrice: -2,
		OutputPrice: -3,
		Status:      "unknown-status",
	})
	if err != nil {
		t.Fatalf("create price: %v", err)
	}
	if created.ModelName != "Negative-Model" {
		t.Fatalf("expected trimmed model name, got %q", created.ModelName)
	}
	if created.InputPrice != 0 || created.CachedPrice != 0 || created.OutputPrice != 0 {
		t.Fatalf("expected negative prices to clamp to zero, got %+v", created)
	}
	if created.Status != AIModelPriceStatusEnabled {
		t.Fatalf("expected unknown status to fall back to enabled, got %q", created.Status)
	}
}

func TestEnsureDefaultAIModelPricesWithDBCreatesTableWhenMissing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ai-model-price-missing-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	// 老库升级场景：价格表还不存在时应该自建并写入默认价格，而不是让计费整体报错。
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("expected missing table to be created, got %v", err)
	}
	if !db.Migrator().HasTable(&entity.AIModelPrice{}) {
		t.Fatal("expected ai_model_prices table to be created")
	}

	var count int64
	if err := db.Model(&entity.AIModelPrice{}).Count(&count).Error; err != nil {
		t.Fatalf("count prices: %v", err)
	}
	if count != 14 {
		t.Fatalf("expected 14 seeded rows after auto-create, got %d", count)
	}
}
