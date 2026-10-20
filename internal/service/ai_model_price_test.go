package service

import (
	"regexp"
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
	// 全量默认表：Standard 短上下文档位 + 别名行。
	if got := len(defaultAIModelPrices); got != 54 {
		t.Fatalf("expected 54 default model prices, got %d", got)
	}

	// 抽样式核对各价位段与带缓存写入价的模型。
	// 键为**模型 ID**，第二项为**显示名称**，后四项为 Input / Cached / CacheWrite / Output。
	type expectation struct {
		id          string
		displayName string
		prices      [4]float64
	}
	expected := []expectation{
		// 旗舰
		{"gpt-6-astra", "GPT-6 Astra", [4]float64{10.00, 1.00, 12.50, 50.00}},
		{"gpt-6.1-sol", "GPT-6.1 Sol", [4]float64{2.00, 0.10, 2.50, 10.00}},
		{"gpt-6-sol", "GPT-6 Sol", [4]float64{2.00, 0.20, 2.50, 10.00}},
		{"gpt-6-luna", "GPT-6 Luna", [4]float64{0.10, 0.01, 0.125, 0.50}},
		{"gpt-5.6-sol", "GPT-5.6 Sol", [4]float64{4.00, 0.40, 5.00, 20.00}},
		{"gpt-5.6-terra", "GPT-5.6 Terra", [4]float64{2.00, 0.20, 2.50, 12.00}},
		{"gpt-5.6-luna", "GPT-5.6 Luna", [4]float64{0.20, 0.02, 0.25, 1.20}},
		{"gpt-5.5", "GPT-5.5", [4]float64{5.00, 0.50, 0, 30.00}},
		{"gpt-5.5-pro", "GPT-5.5 Pro", [4]float64{30.00, 0, 0, 180.00}},
		{"gpt-5.4", "GPT-5.4", [4]float64{2.50, 0.25, 0, 15.00}},
		{"gpt-5.4-mini", "GPT-5.4 Mini", [4]float64{0.75, 0.075, 0, 4.50}},
		{"gpt-5.4-nano", "GPT-5.4 Nano", [4]float64{0.20, 0.02, 0, 1.25}},
		{"gpt-5.4-pro", "GPT-5.4 Pro", [4]float64{30.00, 0, 0, 180.00}},
		{"gpt-5.2", "GPT-5.2", [4]float64{1.75, 0.175, 0, 14.00}},
		{"gpt-5.2-pro", "GPT-5.2 Pro", [4]float64{21.00, 0, 0, 168.00}},
		{"gpt-5.1", "GPT-5.1", [4]float64{1.25, 0.125, 0, 10.00}},
		{"gpt-5", "GPT-5", [4]float64{1.25, 0.125, 0, 10.00}},
		{"gpt-5-mini", "GPT-5 Mini", [4]float64{0.25, 0.025, 0, 2.00}},
		{"gpt-5-nano", "GPT-5 Nano", [4]float64{0.05, 0.005, 0, 0.40}},
		{"gpt-5-pro", "GPT-5 Pro", [4]float64{15.00, 0, 0, 120.00}},
		// 通用
		{"gpt-4.1", "GPT-4.1", [4]float64{2.00, 0.50, 0, 8.00}},
		{"gpt-4.1-mini", "GPT-4.1 Mini", [4]float64{0.40, 0.10, 0, 1.60}},
		{"gpt-4.1-nano", "GPT-4.1 Nano", [4]float64{0.10, 0.025, 0, 0.40}},
		{"gpt-4o", "GPT-4o", [4]float64{2.50, 1.25, 0, 10.00}},
		{"gpt-4o-2024-05-13", "GPT-4o 2024-05-13", [4]float64{5.00, 0, 0, 15.00}},
		{"gpt-4o-mini", "GPT-4o Mini", [4]float64{0.15, 0.075, 0, 0.60}},
		{"gpt-4-turbo-2024-04-09", "GPT-4 Turbo 2024-04-09", [4]float64{10.00, 0, 0, 30.00}},
		{"gpt-4-0613", "GPT-4 0613", [4]float64{30.00, 0, 0, 60.00}},
		{"gpt-3.5-turbo", "GPT-3.5 Turbo", [4]float64{0.50, 0, 0, 1.50}},
		{"gpt-3.5-turbo-0125", "GPT-3.5 Turbo 0125", [4]float64{0.50, 0, 0, 1.50}},
		{"gpt-3.5-turbo-1106", "GPT-3.5 Turbo 1106", [4]float64{1.00, 0, 0, 2.00}},
		{"gpt-3.5-turbo-instruct", "GPT-3.5 Turbo Instruct", [4]float64{1.50, 0, 0, 2.00}},
		{"davinci-002", "Davinci-002", [4]float64{2.00, 0, 0, 2.00}},
		{"babbage-002", "Babbage-002", [4]float64{0.40, 0, 0, 0.40}},
		// 推理
		{"o1", "o1", [4]float64{15.00, 7.50, 0, 60.00}},
		{"o1-pro", "o1 Pro", [4]float64{150.00, 0, 0, 600.00}},
		{"o3", "o3", [4]float64{2.00, 0.50, 0, 8.00}},
		{"o3-pro", "o3 Pro", [4]float64{20.00, 0, 0, 80.00}},
		{"o3-mini", "o3 Mini", [4]float64{1.10, 0.55, 0, 4.40}},
		{"o4-mini", "o4 Mini", [4]float64{1.10, 0.275, 0, 4.40}},
		// Cyber
		{"gpt-5.6-cyber", "GPT-5.6 Cyber", [4]float64{12.50, 1.25, 15.625, 75.00}},
		{"gpt-5.5-cyber", "GPT-5.5 Cyber", [4]float64{12.50, 1.25, 0, 75.00}},
		// 生命科学
		{"gpt-rosalind-research", "GPT-Rosalind-Research", [4]float64{5.00, 0.50, 0, 25.00}},
		{"gpt-rosalind-discovery", "GPT-Rosalind-Discovery", [4]float64{5.00, 0.50, 0, 25.00}},
		// 专用
		{"gpt-5.3-codex", "GPT-5.3-Codex", [4]float64{1.75, 0.175, 0, 14.00}},
		{"chat-latest", "Chat-Latest", [4]float64{5.00, 0.50, 0, 30.00}},
		{"gpt-5-search-api", "GPT-5 Search API", [4]float64{1.25, 0.125, 0, 10.00}},
		{"text-embedding-3-small", "Text-Embedding-3-Small", [4]float64{0.02, 0, 0, 0}},
		{"text-embedding-3-large", "Text-Embedding-3-Large", [4]float64{0.13, 0, 0, 0}},
		{"text-embedding-ada-002", "Text-Embedding-Ada-002", [4]float64{0.10, 0, 0, 0}},
		{"omni-moderation-latest", "Omni-Moderation-Latest", [4]float64{0, 0, 0, 0}},
		// 别名
		{"daybreak-blue", "Daybreak Blue（GPT-5.6 Sol）", [4]float64{4.00, 0.40, 5.00, 20.00}},
		{"daybreak-red", "Daybreak Red", [4]float64{12.50, 1.25, 15.625, 75.00}},
		{"gpt-6-astra-law", "GPT-6 Astra Law *", [4]float64{12.50, 1.25, 15.625, 62.50}},
	}

	if len(expected) != len(defaultAIModelPrices) {
		t.Fatalf("expected table covers %d models but defaults have %d", len(expected), len(defaultAIModelPrices))
	}

	wantByID := make(map[string]expectation, len(expected))
	for _, item := range expected {
		wantByID[item.id] = item
	}

	for _, item := range defaultAIModelPrices {
		want, ok := wantByID[item.ModelName]
		if !ok {
			t.Fatalf("unexpected seeded model %q", item.ModelName)
		}
		if item.DisplayName != want.displayName {
			t.Fatalf("unexpected display name for %q: got %q want %q", item.ModelName, item.DisplayName, want.displayName)
		}
		prices := want.prices
		if item.InputPrice != prices[0] || item.CachedPrice != prices[1] ||
			item.CacheWritePrice != prices[2] || item.OutputPrice != prices[3] {
			t.Fatalf("unexpected price for %q: got in=%v cached=%v cacheWrite=%v out=%v want %v/%v/%v/%v",
				item.ModelName, item.InputPrice, item.CachedPrice, item.CacheWritePrice, item.OutputPrice,
				prices[0], prices[1], prices[2], prices[3])
		}
	}
}

// 模型 ID 必须是发送给上游的小写 ID 形态，且每行都要有可读的显示名称。
func TestDefaultAIModelPricesUseLowercaseModelIDsAndReadableDisplayNames(t *testing.T) {
	modelIDPattern := regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]*$`)

	seen := make(map[string]struct{}, len(defaultAIModelPrices))
	for _, item := range defaultAIModelPrices {
		if !modelIDPattern.MatchString(item.ModelName) {
			t.Fatalf("expected %q to be a lowercase upstream model id", item.ModelName)
		}
		if _, exists := seen[item.ModelName]; exists {
			t.Fatalf("duplicated model id %q in default table", item.ModelName)
		}
		seen[item.ModelName] = struct{}{}

		if strings.TrimSpace(item.DisplayName) == "" {
			t.Fatalf("expected a display name for %q", item.ModelName)
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

	items, err := ListAIModelPricesWithDB(db)
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
	if err := db.Where("model_name = ?", "gpt-5.5").First(&existing).Error; err != nil {
		t.Fatalf("load seeded price: %v", err)
	}
	if _, err := UpdateAIModelPriceWithDB(db, existing.ID, UpdateAIModelPriceInput{
		ModelName:   "gpt-5.5",
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
	if err := db.Where("model_name = ?", "gpt-5.5").First(&updated).Error; err != nil {
		t.Fatalf("reload price: %v", err)
	}
	if updated.InputPrice != 9.99 || updated.OutputPrice != 99.99 {
		t.Fatalf("expected admin edit to survive re-seeding, got %+v", updated)
	}
	if !updated.IsUserModified {
		t.Fatalf("expected admin edit to be flagged, got %+v", updated)
	}
}

// 内置表从「可读名当模型 ID」改为「上游模型 ID + 显示名称」时，
// 旧行必须被原地认领并重命名，而不是插出一条重复行。
func TestModelPriceSeedRenamesLegacyRowsInPlace(t *testing.T) {
	db := newAIModelPriceTestDB(t)

	// 模拟旧版部署：模型 ID 与显示名称都是可读名。
	legacy := entity.AIModelPrice{
		ModelName:   "GPT-6 Astra",
		DisplayName: "GPT-6 Astra",
		InputPrice:  10,
		CachedPrice: 1,
		OutputPrice: 50,
		Status:      AIModelPriceStatusEnabled,
		IsSeed:      true,
		Sort:        10,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("sync defaults: %v", err)
	}

	var count int64
	if err := db.Model(&entity.AIModelPrice{}).Count(&count).Error; err != nil {
		t.Fatalf("count prices: %v", err)
	}
	if count != int64(len(defaultAIModelPrices)) {
		t.Fatalf("expected %d rows (no duplicate), got %d", len(defaultAIModelPrices), count)
	}

	var renamed entity.AIModelPrice
	if err := db.Where("id = ?", legacy.ID).First(&renamed).Error; err != nil {
		t.Fatalf("reload legacy row: %v", err)
	}
	if renamed.ModelName != "gpt-6-astra" {
		t.Fatalf("expected legacy model id to be renamed to gpt-6-astra, got %q", renamed.ModelName)
	}
	if renamed.DisplayName != "GPT-6 Astra" {
		t.Fatalf("expected display name to stay readable, got %q", renamed.DisplayName)
	}
	if renamed.CacheWritePrice != 12.5 {
		t.Fatalf("expected cache write price to be filled from defaults, got %v", renamed.CacheWritePrice)
	}
}

// 管理员改过价格的旧行：仍然重命名，但价格保持不动。
func TestModelPriceSeedKeepsAdminEditedRowDuringRename(t *testing.T) {
	db := newAIModelPriceTestDB(t)

	legacy := entity.AIModelPrice{
		ModelName:      "GPT-5.5",
		DisplayName:    "GPT-5.5",
		InputPrice:     7.77,
		OutputPrice:    77.77,
		Status:         AIModelPriceStatusEnabled,
		IsSeed:         true,
		IsUserModified: true,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("sync defaults: %v", err)
	}

	var renamed entity.AIModelPrice
	if err := db.Where("id = ?", legacy.ID).First(&renamed).Error; err != nil {
		t.Fatalf("reload legacy row: %v", err)
	}
	if renamed.ModelName != "gpt-5.5" {
		t.Fatalf("expected rename to gpt-5.5, got %q", renamed.ModelName)
	}
	if renamed.InputPrice != 7.77 || renamed.OutputPrice != 77.77 {
		t.Fatalf("expected admin prices to survive the rename, got %+v", renamed)
	}
}

// 模型 ID 与显示名称都能命中同一行。
func TestFindAIModelPriceMatchesModelIDAndDisplayName(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	byID, found, err := ResolveAIModelTokenPriceWithDB(db, "gpt-6-astra")
	if err != nil {
		t.Fatalf("resolve by model id: %v", err)
	}
	if !found || byID.MatchedBy != "model_name" {
		t.Fatalf("expected model id match, got found=%t matchedBy=%q", found, byID.MatchedBy)
	}

	byDisplayName, found, err := ResolveAIModelTokenPriceWithDB(db, "GPT-6 Astra")
	if err != nil {
		t.Fatalf("resolve by display name: %v", err)
	}
	if !found || byDisplayName.MatchedBy != "display_name" {
		t.Fatalf("expected display name match, got found=%t matchedBy=%q", found, byDisplayName.MatchedBy)
	}

	if byID.PriceID != byDisplayName.PriceID {
		t.Fatalf("expected both lookups to hit the same row, got %d and %d", byID.PriceID, byDisplayName.PriceID)
	}
	if byID.OutputPrice != 50 || byID.CacheWritePrice != 12.5 {
		t.Fatalf("unexpected prices resolved: %+v", byID)
	}
}

func TestFindAIModelPriceMatchesNameIgnoringCaseAndSpaces(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	// 模型 ID 与显示名称都应可用，且忽略大小写与首尾空格。
	for _, candidate := range []string{"gpt-5.5", "GPT-5.5", "  GPT-5.5  ", "  gpt-5.5  "} {
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

	// 归一化兜底：忽略空格差异，例如「GPT-5.6Sol」。
	resolved, found, err := ResolveAIModelTokenPriceWithDB(db, "GPT-5.6Sol")
	if err != nil {
		t.Fatalf("resolve normalized name: %v", err)
	}
	if !found || resolved.ModelName != "gpt-5.6-sol" {
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
		Model:   "gpt-5.5",
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	resolved, found, err := ResolveAIModelTokenPriceWithDB(db, "gpt-5.5")
	if err != nil {
		t.Fatalf("resolve price: %v", err)
	}
	if !found {
		t.Fatal("expected gpt-5.5 to match the price table")
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

// 价格表不再有「状态」概念：写入请求里带任何状态值都会被固定为启用，行始终参与计费。
func TestAIModelPriceIgnoresDisabledStatusAndStaysBillable(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	var seeded entity.AIModelPrice
	if err := db.Where("model_name = ?", "daybreak-red").First(&seeded).Error; err != nil {
		t.Fatalf("load seeded price: %v", err)
	}
	if _, err := UpdateAIModelPriceWithDB(db, seeded.ID, UpdateAIModelPriceInput{
		ModelName:       seeded.ModelName,
		InputPrice:      seeded.InputPrice,
		CachedPrice:     seeded.CachedPrice,
		CacheWritePrice: seeded.CacheWritePrice,
		OutputPrice:     seeded.OutputPrice,
		Multiplier:      1,
		Status:          "disabled",
	}); err != nil {
		t.Fatalf("update price: %v", err)
	}

	var updated entity.AIModelPrice
	if err := db.Where("id = ?", seeded.ID).First(&updated).Error; err != nil {
		t.Fatalf("reload price: %v", err)
	}
	if updated.Status != AIModelPriceStatusEnabled {
		t.Fatalf("expected status to be forced to enabled, got %q", updated.Status)
	}

	resolved, found, err := ResolveAIModelTokenPriceWithDB(db, "daybreak-red")
	if err != nil {
		t.Fatalf("resolve price: %v", err)
	}
	if !found {
		t.Fatal("expected the price row to stay billable without a status concept")
	}
	if resolved.OutputPrice != 75 {
		t.Fatalf("unexpected resolved price: %+v", resolved)
	}
}

func TestEnsureDefaultAIModelPricesDoesNotResurrectDeletedDefaults(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	// 管理员明确删掉某个默认模型后，不应该在下一次请求被自动补回。
	if err := db.Where("model_name = ?", "gpt-6-astra").Delete(&entity.AIModelPrice{}).Error; err != nil {
		t.Fatalf("delete default price: %v", err)
	}
	aimodelPriceSeeded.Delete(aimodelPriceCacheKey(db))

	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("re-seed after delete: %v", err)
	}

	var count int64
	if err := db.Model(&entity.AIModelPrice{}).Where("model_name = ?", "gpt-6-astra").Count(&count).Error; err != nil {
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

	if err := db.Where("model_name = ?", "gpt-5.2").Delete(&entity.AIModelPrice{}).Error; err != nil {
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

func TestAIModelPricePersistsPositiveFractionalMultiplier(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	created, err := CreateAIModelPriceWithDB(db, CreateAIModelPriceInput{
		ModelName:  "discounted-model",
		InputPrice: 2,
		Multiplier: 0.1,
	})
	if err != nil {
		t.Fatalf("create model price with a 0.1x multiplier: %v", err)
	}
	if created.Multiplier != 0.1 {
		t.Fatalf("expected 0.1x multiplier, got %v", created.Multiplier)
	}
	resolved, found, err := ResolveAIModelTokenPriceWithDB(db, "discounted-model")
	if err != nil || !found {
		t.Fatalf("resolve discounted model: found=%t err=%v", found, err)
	}
	if resolved.Multiplier != 0.1 || resolved.InputUnitPrice != 0.2 {
		t.Fatalf("expected resolved 0.1x price multiplier, got %+v", resolved)
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
