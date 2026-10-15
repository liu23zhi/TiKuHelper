package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
)

func expectedAISearchDefaultPrompt() string {
	return defaultAISearchPrompt()
}

// AI 搜索配置只负责模型/提示词等运行参数，不再承载任何计费字段。
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
