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
	// 全量默认表：Standard 短上下文档位 + 兼容别名行。
	if got := len(defaultAIModelPrices); got != 54 {
		t.Fatalf("expected 54 default model prices, got %d", got)
	}

	// 抽样式核对各价位段与带缓存写入价的模型（Input / Cached / CacheWrite / Output）。
	expected := map[string][4]float64{
		// 旗舰
		"GPT-6 Astra":   {10.00, 1.00, 12.50, 50.00},
		"GPT-6.1 Sol":   {2.00, 0.10, 2.50, 10.00},
		"GPT-6 Sol":     {2.00, 0.20, 2.50, 10.00},
		"GPT-6 Luna":    {0.10, 0.01, 0.125, 0.50},
		"GPT-5.6 Sol":   {4.00, 0.40, 5.00, 20.00},
		"GPT-5.6 Terra": {2.00, 0.20, 2.50, 12.00},
		"GPT-5.6 Luna":  {0.20, 0.02, 0.25, 1.20},
		"GPT-5.5":       {5.00, 0.50, 0, 30.00},
		"GPT-5.5 Pro":   {30.00, 0, 0, 180.00},
		"GPT-5.4":       {2.50, 0.25, 0, 15.00},
		"GPT-5.4 Mini":  {0.75, 0.075, 0, 4.50},
		"GPT-5.4 Nano":  {0.20, 0.02, 0, 1.25},
		"GPT-5.4 Pro":   {30.00, 0, 0, 180.00},
		"GPT-5.2":       {1.75, 0.175, 0, 14.00},
		"GPT-5.2 Pro":   {21.00, 0, 0, 168.00},
		"GPT-5.1":       {1.25, 0.125, 0, 10.00},
		"GPT-5":         {1.25, 0.125, 0, 10.00},
		"GPT-5 Mini":    {0.25, 0.025, 0, 2.00},
		"GPT-5 Nano":    {0.05, 0.005, 0, 0.40},
		"GPT-5 Pro":     {15.00, 0, 0, 120.00},
		// 通用
		"GPT-4.1":                {2.00, 0.50, 0, 8.00},
		"GPT-4.1 Mini":           {0.40, 0.10, 0, 1.60},
		"GPT-4.1 Nano":           {0.10, 0.025, 0, 0.40},
		"GPT-4o":                 {2.50, 1.25, 0, 10.00},
		"GPT-4o 2024-05-13":      {5.00, 0, 0, 15.00},
		"GPT-4o Mini":            {0.15, 0.075, 0, 0.60},
		"GPT-4 Turbo 2024-04-09": {10.00, 0, 0, 30.00},
		"GPT-4 0613":             {30.00, 0, 0, 60.00},
		"GPT-3.5 Turbo":          {0.50, 0, 0, 1.50},
		"GPT-3.5 Turbo 0125":     {0.50, 0, 0, 1.50},
		"GPT-3.5 Turbo 1106":     {1.00, 0, 0, 2.00},
		"GPT-3.5 Turbo Instruct": {1.50, 0, 0, 2.00},
		"Davinci-002":            {2.00, 0, 0, 2.00},
		"Babbage-002":            {0.40, 0, 0, 0.40},
		// 推理
		"o1":      {15.00, 7.50, 0, 60.00},
		"o1 Pro":  {150.00, 0, 0, 600.00},
		"o3":      {2.00, 0.50, 0, 8.00},
		"o3 Pro":  {20.00, 0, 0, 80.00},
		"o3 Mini": {1.10, 0.55, 0, 4.40},
		"o4 Mini": {1.10, 0.275, 0, 4.40},
		// Cyber
		"GPT-5.6 Cyber": {12.50, 1.25, 15.625, 75.00},
		"GPT-5.5 Cyber": {12.50, 1.25, 0, 75.00},
		// 生命科学
		"GPT-Rosalind-Research":  {5.00, 0.50, 0, 25.00},
		"GPT-Rosalind-Discovery": {5.00, 0.50, 0, 25.00},
		// 专用
		"GPT-5.3-Codex":          {1.75, 0.175, 0, 14.00},
		"Chat-Latest":            {5.00, 0.50, 0, 30.00},
		"GPT-5 Search API":       {1.25, 0.125, 0, 10.00},
		"Text-Embedding-3-Small": {0.02, 0, 0, 0},
		"Text-Embedding-3-Large": {0.13, 0, 0, 0},
		"Text-Embedding-Ada-002": {0.10, 0, 0, 0},
		"Omni-Moderation-Latest": {0, 0, 0, 0},
		// 兼容别名
		"Daybreak Blue（GPT-5.6 Sol）": {4.00, 0.40, 5.00, 20.00},
		"Daybreak Red":               {12.50, 1.25, 15.625, 75.00},
		"GPT-6 Astra Law *":          {12.50, 1.25, 15.625, 62.50},
	}

	if len(expected) != len(defaultAIModelPrices) {
		t.Fatalf("expected map covers %d models but defaults have %d", len(expected), len(defaultAIModelPrices))
	}

	for _, item := range defaultAIModelPrices {
		want, ok := expected[item.ModelName]
		if !ok {
			t.Fatalf("unexpected seeded model %q", item.ModelName)
		}
		if item.InputPrice != want[0] || item.CachedPrice != want[1] ||
			item.CacheWritePrice != want[2] || item.OutputPrice != want[3] {
			t.Fatalf("unexpected price for %q: got in=%v cached=%v cacheWrite=%v out=%v want %v/%v/%v/%v",
				item.ModelName, item.InputPrice, item.CachedPrice, item.CacheWritePrice, item.OutputPrice,
				want[0], want[1], want[2], want[3])
		}
	}
}

// 倍率默认必须是 1（倍率为 0 会让命中价格却按 0 计费）。
func TestDefaultAIModelPricesLeaveMultiplierAtOne(t *testing.T) {
	for _, item := range defaultAIModelPrices {
		if item.Multiplier != 0 {
			t.Fatalf("expected default table to omit multiplier for %q, got %v", item.ModelName, item.Multiplier)
		}
	}

	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	var zeroMultiplier int64
	if err := db.Model(&entity.AIModelPrice{}).Where("multiplier <= 0").Count(&zeroMultiplier).Error; err != nil {
		t.Fatalf("count zero multipliers: %v", err)
	}
	if zeroMultiplier != 0 {
		t.Fatalf("expected every seeded row to carry a positive multiplier, got %d zero/negative rows", zeroMultiplier)
	}

	items, err := ListAIModelPricesWithDB(db, false)
	if err != nil {
		t.Fatalf("list prices: %v", err)
	}
	for _, item := range items {
		if item.Multiplier != 1 {
			t.Fatalf("expected multiplier 1 for %q, got %v", item.ModelName, item.Multiplier)
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
	if count != int64(len(defaultAIModelPrices)) {
		t.Fatalf("expected %d rows after idempotent seeding, got %d", len(defaultAIModelPrices), count)
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
	if count != int64(len(defaultAIModelPrices)) {
		t.Fatalf("expected reset to restore %d defaults, got %d", len(defaultAIModelPrices), count)
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
	if count != int64(len(defaultAIModelPrices)) {
		t.Fatalf("expected %d seeded rows after auto-create, got %d", len(defaultAIModelPrices), count)
	}
}
