package service

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
)

func expectedAISearchDefaultPrompt() string {
	return defaultAISearchPrompt()
}

// AI 搜索配置不承载模型 token 价格；工具调用单价仍通过系统设置集中管理。
func TestUpdateAISearchConfigWithDBIgnoresLegacyBillingFields(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("update ai config: %v", err)
	}

	if view.Model != "gpt-4o-mini" || !view.Enabled {
		t.Fatalf("unexpected config view: %+v", view)
	}

	// 历史列即使保留，也必须保持默认值，不能因为保存配置而被写入。
	var saved entity.AISearchConfig
	if err := db.Where("provider = ?", aiSearchAccountProvider).First(&saved).Error; err != nil {
		t.Fatalf("load ai config: %v", err)
	}
	if saved.InputTokenPrice != 0 || saved.OutputTokenPrice != 0 {
		t.Fatalf("expected legacy token prices to stay untouched, got %+v", saved)
	}
}

func TestAISearchConfigStagedBillingSettingsDefaultAndPartialUpdate(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	initial, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatalf("get default ai config: %v", err)
	}
	if initial.EnableAIStagedTokenBilling || initial.SmoothAIStagedTokenMultiplier {
		t.Fatalf("staged billing should default off, got %+v", initial)
	}

	enable := true
	updated, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		EnableAIStagedTokenBilling: &enable,
	})
	if err != nil {
		t.Fatalf("enable staged billing: %v", err)
	}
	if !updated.EnableAIStagedTokenBilling || updated.SmoothAIStagedTokenMultiplier {
		t.Fatalf("unexpected staged billing update: %+v", updated)
	}

	smooth := true
	updated, err = UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		SmoothAIStagedTokenMultiplier: &smooth,
	})
	if err != nil {
		t.Fatalf("enable smooth staged billing: %v", err)
	}
	if !updated.EnableAIStagedTokenBilling || !updated.SmoothAIStagedTokenMultiplier {
		t.Fatalf("partial update should preserve staged billing toggle: %+v", updated)
	}

	legacy, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Model: "legacy-client"})
	if err != nil {
		t.Fatalf("legacy config update: %v", err)
	}
	if !legacy.EnableAIStagedTokenBilling || !legacy.SmoothAIStagedTokenMultiplier {
		t.Fatalf("legacy update should preserve staged billing settings: %+v", legacy)
	}
}

func TestAISearchConfigStagedBillingPointsCanBeEditedAndPersisted(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	custom := []AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 4},
		{Tokens: 3000, Multiplier: 2.5},
		{Tokens: 10000, Multiplier: 1.2},
		{Tokens: 100000, Multiplier: 0.3},
	}
	enabled := true
	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		EnableAIStagedTokenBilling:  &enabled,
		StagedTokenMultiplierPoints: &custom,
	})
	if err != nil {
		t.Fatalf("save staged points: %v", err)
	}
	if !reflect.DeepEqual(view.StagedTokenMultiplierPoints, custom) {
		t.Fatalf("expected saved staged points %+v, got %+v", custom, view.StagedTokenMultiplierPoints)
	}

	settings := GetAIStagedTokenBillingSettingsWithDB(db)
	if !reflect.DeepEqual(settings.MultiplierPoints, custom) {
		t.Fatalf("expected billing settings %+v, got %+v", custom, settings.MultiplierPoints)
	}
}

func TestAISearchConfigStagedPointsHaveNoBusinessValueBounds(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	points := []AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 12.3456789},
		{Tokens: 2_000_000_000, Multiplier: 12.3456789},
		{Tokens: 9_000_000_000, Multiplier: -3.25},
	}
	want := []AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 12.345679},
		{Tokens: 2_000_000_000, Multiplier: 12.345679},
		{Tokens: 9_000_000_000, Multiplier: -3.25},
	}
	updated, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{StagedTokenMultiplierPoints: &points})
	if err != nil {
		t.Fatalf("finite monotonic multipliers and thresholds above the former limit should be accepted: %v", err)
	}
	if !reflect.DeepEqual(updated.StagedTokenMultiplierPoints, want) {
		t.Fatalf("expected unbounded staged values rounded to money precision %+v, got %+v", want, updated.StagedTokenMultiplierPoints)
	}
}

func TestAIStagedTokenMultiplierPointJSONPreservesInt64Thresholds(t *testing.T) {
	const threshold = int64(9_007_199_254_740_993)
	for _, payload := range []string{
		`{"tokens":"9007199254740993","multiplier":0.1}`,
		`{"tokens":9007199254740993,"multiplier":0.1}`,
	} {
		var point AIStagedTokenMultiplierPoint
		if err := json.Unmarshal([]byte(payload), &point); err != nil {
			t.Fatalf("unmarshal threshold payload %s: %v", payload, err)
		}
		if point.Tokens != threshold {
			t.Fatalf("expected exact threshold %d, got %d", threshold, point.Tokens)
		}
		encoded, err := json.Marshal(point)
		if err != nil {
			t.Fatalf("marshal staged point: %v", err)
		}
		if string(encoded) != `{"tokens":"9007199254740993","multiplier":0.1}` {
			t.Fatalf("expected canonical string threshold JSON, got %s", encoded)
		}
	}
}

func TestAISearchConfigRoundsStageMultipliersToMoneyPrecision(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	points := []AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 1.23456789},
		{Tokens: 2000, Multiplier: 0.12345678},
	}
	updated, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{StagedTokenMultiplierPoints: &points})
	if err != nil {
		t.Fatalf("save staged multipliers: %v", err)
	}
	if updated.StagedTokenMultiplierPoints[0].Multiplier != 1.234568 || updated.StagedTokenMultiplierPoints[1].Multiplier != 0.123457 {
		t.Fatalf("expected staged multipliers rounded to balance precision, got %+v", updated.StagedTokenMultiplierPoints)
	}
}

func TestAISearchConfigStagedPointsAllowSingleZeroTokenNode(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	points := []AIStagedTokenMultiplierPoint{{Tokens: 0, Multiplier: 2.75}}
	updated, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{StagedTokenMultiplierPoints: &points})
	if err != nil {
		t.Fatalf("expected a single zero-token point to represent a flat multiplier: %v", err)
	}
	if !reflect.DeepEqual(updated.StagedTokenMultiplierPoints, points) {
		t.Fatalf("expected single staged point %+v, got %+v", points, updated.StagedTokenMultiplierPoints)
	}
}

func TestAISearchConfigStagedPointsAllowMoreThanTwentyNodes(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	points := make([]AIStagedTokenMultiplierPoint, 25)
	for index := range points {
		points[index] = AIStagedTokenMultiplierPoint{Tokens: int64(index), Multiplier: float64(25 - index)}
	}
	updated, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{StagedTokenMultiplierPoints: &points})
	if err != nil {
		t.Fatalf("expected more than twenty valid staged points to be accepted: %v", err)
	}
	if len(updated.StagedTokenMultiplierPoints) != len(points) {
		t.Fatalf("expected %d staged points, got %d", len(points), len(updated.StagedTokenMultiplierPoints))
	}
}

func TestAISearchConfigAllowsFlexibleStageEndpoints(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	for _, testCase := range []struct {
		terminalTokens     int64
		secondStageTokens  int64
		terminalMultiplier float64
	}{
		{terminalTokens: 50000, secondStageTokens: 1000, terminalMultiplier: 0.8},
		{terminalTokens: 150000, secondStageTokens: 3000, terminalMultiplier: 0.3},
	} {
		points := []AIStagedTokenMultiplierPoint{
			{Tokens: 0, Multiplier: 4},
			{Tokens: testCase.secondStageTokens, Multiplier: 2},
			{Tokens: testCase.terminalTokens, Multiplier: testCase.terminalMultiplier},
		}
		if _, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{StagedTokenMultiplierPoints: &points}); err != nil {
			t.Fatalf("valid custom endpoints %+v should be accepted: %v", testCase, err)
		}
		settings := GetAIStagedTokenBillingSettingsWithDB(db)
		last := settings.MultiplierPoints[len(settings.MultiplierPoints)-1]
		if last.Tokens != testCase.terminalTokens || last.Multiplier != testCase.terminalMultiplier {
			t.Fatalf("custom terminal node was not preserved: got %+v, want tokens=%d multiplier=%v", last, testCase.terminalTokens, testCase.terminalMultiplier)
		}
	}
}

func TestAISearchConfigRejectsInvalidStagedBillingPoints(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	invalid := [][]AIStagedTokenMultiplierPoint{
		{{Tokens: 1, Multiplier: 4}, {Tokens: 100000, Multiplier: 0.3}},
		{{Tokens: 0, Multiplier: 4}, {Tokens: 3000, Multiplier: 2}, {Tokens: 2000, Multiplier: 1}, {Tokens: 100000, Multiplier: 0.3}},
		{{Tokens: 0, Multiplier: 4}, {Tokens: 3000, Multiplier: 2}, {Tokens: 100000, Multiplier: 2.1}},
		{{Tokens: 0, Multiplier: 4}, {Tokens: 3000, Multiplier: math.NaN()}},
		{{Tokens: 0, Multiplier: 4}, {Tokens: 3000, Multiplier: math.Inf(1)}},
	}
	for index, points := range invalid {
		if _, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{StagedTokenMultiplierPoints: &points}); err == nil {
			t.Fatalf("case %d: expected invalid staged points to fail", index)
		}
	}
}

func TestNormalizeAISearchBaseURLTrimsAndKeepsRawHost(t *testing.T) {
	normalized := normalizeAISearchBaseURL(" https://api.openai.com/ ")
	if normalized != "https://api.openai.com" {
		t.Fatalf("expected raw host to be preserved without forced /v1, got %s", normalized)
	}
}

func TestNormalizeAISearchBaseURLKeepsExistingV1(t *testing.T) {
	normalized := normalizeAISearchBaseURL("https://api.openai.com/v1/")
	if normalized != "https://api.openai.com/v1" {
		t.Fatalf("expected existing /v1 url to stay normalized, got %s", normalized)
	}
}

func TestNormalizeAISearchManualModelsDeduplicatesValues(t *testing.T) {
	models := normalizeAISearchManualModels([]string{" gpt-4o ", "", "gpt-4o", "deepseek-chat"})
	expected := []string{"gpt-4o", "deepseek-chat"}
	if !reflect.DeepEqual(models, expected) {
		t.Fatalf("expected normalized models %v, got %v", expected, models)
	}
}

func TestGetAISearchConfigWithDBReturnsDefaultPromptAndFixedSamplingParams(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:     true,
		BaseURL:     "https://api.openai.com/v1",
		APIKey:      "sk-test",
		Model:       "gpt-4o-mini",
		Prompt:      "   ",
		Temperature: 0.9,
		TopP:        0.6,
	})
	if err != nil {
		t.Fatalf("update ai config: %v", err)
	}

	if view.Prompt != expectedAISearchDefaultPrompt() {
		t.Fatalf("expected default prompt, got %q", view.Prompt)
	}
	if view.Temperature != 0 {
		t.Fatalf("expected temperature=0, got %v", view.Temperature)
	}
	if view.TopP != 0 {
		t.Fatalf("expected topP=0, got %v", view.TopP)
	}

	loaded, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatalf("get ai config: %v", err)
	}
	if loaded.Prompt != expectedAISearchDefaultPrompt() {
		t.Fatalf("expected loaded default prompt, got %q", loaded.Prompt)
	}
	if loaded.Temperature != 0 {
		t.Fatalf("expected loaded temperature=0, got %v", loaded.Temperature)
	}
	if loaded.TopP != 0 {
		t.Fatalf("expected loaded topP=0, got %v", loaded.TopP)
	}

	var saved entity.AISearchConfig
	if err := db.Where("provider = ?", aiSearchAccountProvider).First(&saved).Error; err != nil {
		t.Fatalf("load ai config: %v", err)
	}
	if saved.Prompt != "" {
		t.Fatalf("expected blank prompt to be stored as empty string, got %q", saved.Prompt)
	}
	if saved.Temperature != 0 {
		t.Fatalf("expected saved temperature=0, got %v", saved.Temperature)
	}
	if saved.TopP != 0 {
		t.Fatalf("expected saved topP=0, got %v", saved.TopP)
	}
}

func TestDefaultAISearchPromptCoversAllSupportedQuestionTypes(t *testing.T) {
	prompt := defaultAISearchPrompt()
	for _, expected := range []string{
		"判断题：只返回 1 个判断结果",
		"填空题：按题干中空的出现顺序",
		"简答题、问答题、阅读理解、大作业：必须回答全部小问",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected default prompt %q to contain %q", prompt, expected)
		}
	}
}

func TestDefaultAISearchPromptReusesSearchLayerCopy(t *testing.T) {
	// 只有一份提示词副本，避免配置页展示的内容与真正发给模型的提示词漂移。
	if got, want := defaultAISearchPrompt(), internalsearch.DefaultAISearchPrompt(); got != want {
		t.Fatalf("expected service default prompt to reuse search layer prompt")
	}
}

func TestInspectAISearchModelPricingWithDBReportsUnpricedModels(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed default prices: %v", err)
	}

	if _, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:      true,
		BaseURL:      "https://api.example.com/v1",
		APIKey:       "sk-test",
		Model:        "GPT-5.5",
		ManualModels: []string{"GPT-5.5", "GPT-6 Luna", "my-private-model"},
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	status, err := InspectAISearchModelPricingWithDB(db)
	if err != nil {
		t.Fatalf("inspect pricing: %v", err)
	}
	if status.ConfiguredModel != "GPT-5.5" {
		t.Fatalf("expected configured model GPT-5.5, got %q", status.ConfiguredModel)
	}
	if !status.ConfiguredModelPriced {
		t.Fatalf("expected GPT-5.5 to be priced, got %+v", status)
	}
	if !status.HasUnpricedModel {
		t.Fatalf("expected unpriced models to be reported, got %+v", status)
	}
	if len(status.UnpricedModels) != 1 || status.UnpricedModels[0] != "my-private-model" {
		t.Fatalf("expected only my-private-model to be unpriced, got %+v", status.UnpricedModels)
	}
}

func TestInspectAISearchModelPricingWithDBFlagsUnpricedConfiguredModel(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("seed default prices: %v", err)
	}

	if _, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://api.example.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-model-without-price",
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	status, err := InspectAISearchModelPricingWithDB(db)
	if err != nil {
		t.Fatalf("inspect pricing: %v", err)
	}
	if status.ConfiguredModelPriced {
		t.Fatalf("expected configured model to be reported as unpriced, got %+v", status)
	}
	if !status.HasUnpricedModel || len(status.UnpricedModels) != 1 || status.UnpricedModels[0] != "gpt-model-without-price" {
		t.Fatalf("unexpected unpriced list: %+v", status)
	}
}

func TestInspectAISearchModelPricingWithDBWithoutConfigIsEmpty(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	status, err := InspectAISearchModelPricingWithDB(db)
	if err != nil {
		t.Fatalf("inspect pricing: %v", err)
	}
	if status.HasUnpricedModel || status.ConfiguredModelPriced || len(status.UnpricedModels) != 0 {
		t.Fatalf("expected empty status without config, got %+v", status)
	}
}

func TestBuildAISearchRequestConfigWithDBReturnsEffectivePromptAndFixedSamplingParams(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:     true,
		BaseURL:     "https://default.example.com/v1",
		APIKey:      "default-key",
		Model:       "default-model",
		Prompt:      "  自定义 Prompt  ",
		Temperature: 1,
		TopP:        1,
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatalf("build request config: %v", err)
	}

	if resolved.Config.Prompt != "自定义 Prompt" {
		t.Fatalf("expected trimmed prompt, got %q", resolved.Config.Prompt)
	}
	if resolved.Config.Temperature != 0 {
		t.Fatalf("expected temperature=0, got %v", resolved.Config.Temperature)
	}
	if resolved.Config.TopP != 0 {
		t.Fatalf("expected topP=0, got %v", resolved.Config.TopP)
	}
}

func TestUpdateAISearchConfigWithDBNormalizesAndPersistsValues(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:      true,
		BaseURL:      " https://api.openai.com/ ",
		APIKey:       "sk-test",
		Model:        "gpt-4o-mini",
		ManualModels: []string{" gpt-4o ", "", "deepseek-chat", "gpt-4o"},
		Remark:       "AI 默认搜索",
		Temperature:  0.6,
		TopP:         0.8,
	})
	if err != nil {
		t.Fatalf("update ai config: %v", err)
	}

	if view.BaseURL != "https://api.openai.com" {
		t.Fatalf("expected trimmed base url, got %s", view.BaseURL)
	}

	if !reflect.DeepEqual(view.ManualModels, []string{"gpt-4o", "deepseek-chat"}) {
		t.Fatalf("unexpected manual models: %v", view.ManualModels)
	}

	var saved entity.AISearchConfig
	if err := db.Where("provider = ?", aiSearchAccountProvider).First(&saved).Error; err != nil {
		t.Fatalf("load ai config: %v", err)
	}

	if saved.BaseURL != "https://api.openai.com" {
		t.Fatalf("expected saved trimmed base url, got %s", saved.BaseURL)
	}

	if saved.ManualModels != `["gpt-4o","deepseek-chat"]` {
		t.Fatalf("expected saved manual models json, got %s", saved.ManualModels)
	}
}

// 汇率与全局倍率已彻底废弃：保存配置不再写入，读取也不再暴露。
func TestAISearchConfigNoLongerExposesExchangeRateOrMultiplier(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	if _, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-4o-mini",
	}); err != nil {
		t.Fatalf("update ai config: %v", err)
	}

	loaded, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatalf("load ai config: %v", err)
	}
	if loaded.Model != "gpt-4o-mini" {
		t.Fatalf("unexpected loaded config: %+v", loaded)
	}

	var saved entity.AISearchConfig
	if err := db.Where("provider = ?", aiSearchAccountProvider).First(&saved).Error; err != nil {
		t.Fatalf("load ai config row: %v", err)
	}
	if saved.ExchangeRate != 1 || saved.GlobalBillingMultiplier != 1 || !saved.UseGlobalBillingMultiplier {
		t.Fatalf("expected legacy billing columns to keep defaults, got %+v", saved)
	}
}

func TestGetAISearchConfigWithDBReturnsParsedManualModels(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		BaseURL:      "https://api.openai.com",
		ManualModels: []string{"gpt-4o", "deepseek-chat"},
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	view, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatalf("get ai config: %v", err)
	}

	expected := []string{"gpt-4o", "deepseek-chat"}
	if !reflect.DeepEqual(view.ManualModels, expected) {
		t.Fatalf("expected parsed manual models %v, got %v", expected, view.ManualModels)
	}
}

func TestAISearchConfigPersistsAndResolvesSearchBudgets(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}

	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:               true,
		BaseURL:               "https://api.example.com/v1",
		APIKey:                "sk-test",
		Model:                 "gpt-test",
		MaxAISearchModelCalls: 6,
		MaxAISearches:         5,
		MaxAISearchRetries:    4,
	})
	if err != nil {
		t.Fatalf("update AI search budgets: %v", err)
	}
	if view.MaxAISearchModelCalls != 6 || view.MaxAISearches != 5 || view.MaxAISearchRetries != 4 {
		t.Fatalf("expected config view budgets 6/5/4, got %d/%d/%d", view.MaxAISearchModelCalls, view.MaxAISearches, view.MaxAISearchRetries)
	}

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatalf("resolve AI search config: %v", err)
	}
	if resolved.Config.MaxAISearchModelCalls != 6 || resolved.Config.MaxAISearches != 5 || resolved.Config.MaxAISearchRetries != 4 {
		t.Fatalf("expected request budgets 6/5/4, got %d/%d/%d", resolved.Config.MaxAISearchModelCalls, resolved.Config.MaxAISearches, resolved.Config.MaxAISearchRetries)
	}

	legacyView, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://api.example.com/v1",
		APIKey:  "********",
		Model:   "gpt-test",
	})
	if err != nil {
		t.Fatalf("update legacy AI config: %v", err)
	}
	if legacyView.MaxAISearchModelCalls != 6 || legacyView.MaxAISearches != 5 || legacyView.MaxAISearchRetries != 4 {
		t.Fatalf("legacy payload must preserve budgets 6/5/4, got %d/%d/%d", legacyView.MaxAISearchModelCalls, legacyView.MaxAISearches, legacyView.MaxAISearchRetries)
	}
}

func TestAISearchConfigPreservesLargeBudgetsAcrossEveryView(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		MaxAISearchModelCalls: 1000, MaxAISearches: 700, MaxAISearchRetries: 80, MaxAIWebReads: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertView := func(view AISearchConfigView) {
		t.Helper()
		if view.MaxAISearchModelCalls != 1000 || view.MaxAISearches != 700 || view.MaxAISearchRetries != 80 || view.MaxAIWebReads != 600 {
			t.Fatalf("large AI settings were changed: %+v", view)
		}
	}
	assertView(view)
	reloaded, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	assertView(reloaded)
	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Config.MaxAISearchModelCalls != 1000 || resolved.Config.MaxAISearches != 700 || resolved.Config.MaxAISearchRetries != 80 || resolved.Config.MaxAIWebReads != 600 {
		t.Fatalf("request settings changed large budgets: %+v", resolved.Config)
	}
	legacy, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Model: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	assertView(legacy)
}

func TestAISearchConfigPersistsSearchPricePerCallWithLegacyPayloadCompatibility(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}

	initial, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatalf("get default AI search config: %v", err)
	}
	if initial.SearchPricePerCall != 0 {
		t.Fatalf("search price should default to zero, got %v", initial.SearchPricePerCall)
	}

	price := 0.125
	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{SearchPricePerCall: &price})
	if err != nil {
		t.Fatalf("set search price: %v", err)
	}
	if view.SearchPricePerCall != price {
		t.Fatalf("expected configured search price %v, got %v", price, view.SearchPricePerCall)
	}
	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatalf("resolve AI search config: %v", err)
	}
	if resolved.Config.SearchPricePerCall != price {
		t.Fatalf("expected resolved search price %v, got %v", price, resolved.Config.SearchPricePerCall)
	}

	legacyView, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Model: "legacy-client"})
	if err != nil {
		t.Fatalf("update using legacy payload: %v", err)
	}
	if legacyView.SearchPricePerCall != price {
		t.Fatalf("legacy payload must preserve search price %v, got %v", price, legacyView.SearchPricePerCall)
	}

	zero := 0.0
	cleared, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{SearchPricePerCall: &zero})
	if err != nil {
		t.Fatalf("clear search price: %v", err)
	}
	if cleared.SearchPricePerCall != 0 {
		t.Fatalf("explicit zero should disable search billing, got %v", cleared.SearchPricePerCall)
	}
}

func TestAISearchConfigPersistsWebReadBudgetAndPrice(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}

	initial, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatalf("get default AI search config: %v", err)
	}
	if initial.MaxAIWebReads != DefaultMaxAIWebReads || initial.WebReadPricePerCall != DefaultWebReadPricePerCall {
		t.Fatalf("web read defaults should be %d and %v, got %d and %v", DefaultMaxAIWebReads, DefaultWebReadPricePerCall, initial.MaxAIWebReads, initial.WebReadPricePerCall)
	}

	price := 0.375
	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{MaxAIWebReads: 7, WebReadPricePerCall: &price})
	if err != nil {
		t.Fatalf("set web read config: %v", err)
	}
	if view.MaxAIWebReads != 7 || view.WebReadPricePerCall != price {
		t.Fatalf("expected web read config 7/%v, got %d/%v", price, view.MaxAIWebReads, view.WebReadPricePerCall)
	}
	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatalf("resolve web read config: %v", err)
	}
	if resolved.Config.MaxAIWebReads != 7 || resolved.Config.WebReadPricePerCall != price {
		t.Fatalf("resolved web read config mismatch: %d/%v", resolved.Config.MaxAIWebReads, resolved.Config.WebReadPricePerCall)
	}

	zero := 0.0
	cleared, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{WebReadPricePerCall: &zero})
	if err != nil {
		t.Fatalf("clear web read price: %v", err)
	}
	if cleared.WebReadPricePerCall != 0 {
		t.Fatalf("explicit zero should disable web read billing, got %v", cleared.WebReadPricePerCall)
	}
}

func TestAISearchConfigPersistsImageOCRPrice(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	initial, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatalf("get default AI search config: %v", err)
	}
	if initial.ImageOCRPricePerCall != DefaultImageOCRPricePerCall {
		t.Fatalf("OCR price should default to zero, got %v", initial.ImageOCRPricePerCall)
	}

	price := 0.015
	view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{ImageOCRPricePerCall: &price})
	if err != nil {
		t.Fatalf("set OCR price: %v", err)
	}
	if view.ImageOCRPricePerCall != price {
		t.Fatalf("expected OCR price %v, got %v", price, view.ImageOCRPricePerCall)
	}
	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatalf("resolve OCR price: %v", err)
	}
	if resolved.Config.ImageOCRPricePerCall != price {
		t.Fatalf("resolved OCR price mismatch: %v", resolved.Config.ImageOCRPricePerCall)
	}

	zero := 0.0
	cleared, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{ImageOCRPricePerCall: &zero})
	if err != nil {
		t.Fatalf("clear OCR price: %v", err)
	}
	if cleared.ImageOCRPricePerCall != 0 {
		t.Fatalf("explicit zero should disable OCR billing, got %v", cleared.ImageOCRPricePerCall)
	}
}

func TestAISearchConfigViewJSONOmitsLegacyRemoteModelsFlag(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:      true,
		BaseURL:      "https://api.openai.com/v1",
		APIKey:       "sk-test",
		Model:        "gpt-4o-mini",
		ManualModels: []string{"gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	view, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatalf("get ai config: %v", err)
	}

	payload, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal ai config view: %v", err)
	}

	if strings.Contains(string(payload), "remoteModelsEnabled") {
		t.Fatalf("expected legacy remoteModelsEnabled to be omitted, got %s", string(payload))
	}
}

func TestListAISearchModelsWithDBMergesManualAndRemoteModelsWithV1Fallback(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	var (
		mu    sync.Mutex
		paths []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/models":
			http.NotFound(w, r)
		case "/v1/models":
			if got := r.Header.Get("Authorization"); got != "Bearer sk-default" {
				t.Fatalf("unexpected authorization header: %s", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat"},{"id":"gpt-4.1-mini"}]}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:      true,
		BaseURL:      server.URL,
		APIKey:       "sk-default",
		ManualModels: []string{"gpt-4o-mini", "deepseek-chat"},
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	models, err := ListAISearchModelsWithDB(db, url.Values{})
	if err != nil {
		t.Fatalf("list ai search models: %v", err)
	}

	expected := []string{"gpt-4o-mini", "deepseek-chat", "gpt-4.1-mini"}
	if !reflect.DeepEqual(models, expected) {
		t.Fatalf("expected merged models %v, got %v", expected, models)
	}

	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(paths, []string{"/models", "/v1/models"}) {
		t.Fatalf("expected fallback request sequence, got %v", paths)
	}
}

func TestListAISearchModelsWithDBUsesExplicitQueryValuesAndFallsBackToManualModels(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	var calls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer sk-request" {
			t.Fatalf("unexpected authorization header: %s", got)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled:      true,
		BaseURL:      "https://default.example.com/v1",
		APIKey:       "sk-default",
		ManualModels: []string{"gpt-4o-mini", "deepseek-chat"},
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	models, err := ListAISearchModelsWithDB(db, url.Values{
		"url": []string{server.URL + "/v1"},
		"key": []string{"sk-request"},
	})
	if err != nil {
		t.Fatalf("list ai search models: %v", err)
	}

	expected := []string{"gpt-4o-mini", "deepseek-chat"}
	if !reflect.DeepEqual(models, expected) {
		t.Fatalf("expected manual models fallback %v, got %v", expected, models)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected three model-list attempts, got %d", got)
	}
}

func TestRequestRemoteAISearchModelsRetriesTransientStatus(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-after-retry"}]}`))
	}))
	defer server.Close()

	models, status, err := requestRemoteAISearchModels(server.URL+"/v1/models", "sk-test")
	if err != nil || status != http.StatusOK || !reflect.DeepEqual(models, []string{"model-after-retry"}) {
		t.Fatalf("unexpected model-list result: models=%v status=%d err=%v", models, status, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected two model-list attempts, got %d", got)
	}
}

func TestBuildAISearchRequestConfigWithDBUsesExplicitQueryValuesFirst(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{
		"url":   []string{"https://request.example.com"},
		"key":   []string{"request-key"},
		"model": []string{"request-model"},
	}, true)
	if err != nil {
		t.Fatalf("build request config: %v", err)
	}

	if resolved.Config.BaseURL != "https://request.example.com" || resolved.Config.APIKey != "request-key" || resolved.Config.Model != "request-model" {
		t.Fatalf("expected explicit query values to win, got %+v", resolved.Config)
	}
	if resolved.UsedDefaultAccount {
		t.Fatal("expected explicit ai config not to count as default account usage")
	}
}

func TestBuildAISearchRequestConfigWithDBFallsBackToEnabledDefault(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatalf("build request config: %v", err)
	}

	if resolved.Config.BaseURL != "https://default.example.com/v1" || resolved.Config.APIKey != "default-key" || resolved.Config.Model != "default-model" {
		t.Fatalf("expected enabled default config, got %+v", resolved.Config)
	}
	if !resolved.UsedDefaultAccount {
		t.Fatal("expected ai default fallback to be recorded")
	}
}

func TestBuildAISearchRequestConfigWithDBRejectsDefaultFallbackForAnonymousRequests(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, false)
	if err != nil {
		t.Fatalf("build request config: %v", err)
	}

	if resolved.Config.BaseURL != "" || resolved.Config.APIKey != "" || resolved.Config.Model != "" {
		t.Fatalf("expected anonymous request to skip default ai config, got %+v", resolved.Config)
	}
	if resolved.UsedDefaultAccount {
		t.Fatal("expected anonymous request not to use default ai config")
	}
}

func TestBuildAISearchRequestConfigWithDBIgnoresExplicitQueryForAnonymousRequests(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{
		"url":   []string{"http://127.0.0.1:8080"},
		"key":   []string{"request-key"},
		"model": []string{"request-model"},
	}, false, false)
	if err != nil {
		t.Fatalf("build request config: %v", err)
	}
	if resolved.Config.BaseURL != "" || resolved.Config.APIKey != "" || resolved.Config.Model != "" {
		t.Fatalf("expected anonymous explicit AI config to be ignored, got %+v", resolved.Config)
	}
}

func TestBuildAISearchRequestConfigWithDBSkipsDisabledDefault(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true,
		BaseURL: "https://default.example.com/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	_, err = UpdateSearchAccountConfigWithDB(db, "ai", UpdateSearchAccountConfigInput{
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("disable ai config: %v", err)
	}

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatalf("build request config: %v", err)
	}

	if resolved.Config.BaseURL != "" || resolved.Config.APIKey != "" || resolved.Config.Model != "" {
		t.Fatalf("expected disabled default config to be ignored, got %+v", resolved.Config)
	}
}
