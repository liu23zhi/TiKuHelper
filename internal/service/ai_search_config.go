package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const aiSearchUAPIToolTimeout = 30 * time.Second
const aiImageOCRUAPITimeout = 60 * time.Second

// BuildUAPISearchToolWithDB constructs the optional web-search/web-read tool for AI.
// The UAPI key stays server-side; invalid or missing keys disable the tool
// rather than turning a normal AI answer into an avoidable configuration error.
func BuildUAPISearchToolWithDB(db *gorm.DB) internalsearch.AISearchTool {
	key := strings.TrimSpace(GetUAPIAPIKeyWithDB(db))
	if !strings.HasPrefix(key, "uapi-") || len(key) <= len("uapi-") {
		return nil
	}
	return uapi.NewClient(key).
		SetTimeout(aiSearchUAPIToolTimeout).
		SetMaxRetries(0)
}

func BuildAITikuSearchToolWithDB(db *gorm.DB) internalsearch.AITikuSearchTool {
	if db == nil || !db.Migrator().HasTable(&entity.Tiku{}) {
		return nil
	}
	return internalsearch.NewDBTikuSearchTool(db)
}

// BuildUAPIImageOCRProviderWithDB creates the optional auxiliary OCR provider.
// It is enabled when a valid UAPI key exists. The configured unit price only
// controls the billing amount; a zero price still produces a visible 0-yuan
// OCR billing detail for auditability.
func BuildUAPIImageOCRProviderWithDB(db *gorm.DB) internalsearch.AIImageOCRProvider {
	key := strings.TrimSpace(GetUAPIAPIKeyWithDB(db))
	if !strings.HasPrefix(key, "uapi-") || len(key) <= len("uapi-") {
		return nil
	}
	client := uapi.NewClient(key).SetTimeout(aiImageOCRUAPITimeout).SetMaxRetries(0)
	return func(ctx context.Context, _, imageName, contentType string, image []byte) (string, error) {
		if strings.TrimSpace(imageName) == "" {
			imageName = imageNameForContentType(contentType)
		}
		result, err := client.ImageOCR(ctx, uapi.ImageOCRRequest{
			Image:        image,
			ImageName:    imageName,
			NeedLocation: false,
			// OCR is only used to supplement the model prompt; avoid paying for
			// coordinate and Markdown payloads that are not consumed here.
			ReturnMarkdown: false,
			EnableCLS:      false,
		})
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(result.PlainText), nil
	}
}

// BuildCachedUAPIImageOCRProviderWithDB adds persistent URL/image fingerprint
// caching around the regular UAPI OCR provider.
func BuildCachedUAPIImageOCRProviderWithDB(db *gorm.DB) internalsearch.AIImageOCRCacheProvider {
	return cacheAIImageOCRProviderWithDB(db, BuildUAPIImageOCRProviderWithDB(db))
}

func imageNameForContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg":
		return "image.jpg"
	case "image/gif":
		return "image.gif"
	case "image/webp":
		return "image.webp"
	case "image/bmp":
		return "image.bmp"
	default:
		return "image.png"
	}
}

type AISearchConfigView struct {
	Enabled                          bool                           `json:"enabled"`
	BaseURL                          string                         `json:"baseUrl"`
	APIKey                           string                         `json:"apiKey"`
	Model                            string                         `json:"model"`
	BackupModel                      string                         `json:"backupModel"`
	ManualModels                     []string                       `json:"manualModels"`
	Prompt                           string                         `json:"prompt"`
	Remark                           string                         `json:"remark"`
	Temperature                      float64                        `json:"temperature"`
	TopP                             float64                        `json:"topP"`
	MaxAISearchModelCalls            int                            `json:"maxAISearchModelCalls"`
	MaxAISearches                    int                            `json:"maxAISearches"`
	MaxAISearchRetries               int                            `json:"maxAISearchRetries"`
	SearchPricePerCall               float64                        `json:"searchPricePerCall"`
	MaxAIWebReads                    int                            `json:"maxAIWebReads"`
	WebReadPricePerCall              float64                        `json:"webReadPricePerCall"`
	ImageOCRPricePerCall             float64                        `json:"imageOCRPricePerCall"`
	TikuSearchPricePerCall           float64                        `json:"tiku_search_price_per_call"`
	EnableAIStagedTokenBilling       bool                           `json:"enableAIStagedTokenBilling"`
	SmoothAIStagedTokenMultiplier    bool                           `json:"smoothAIStagedTokenMultiplier"`
	StagedTokenMultiplierPoints      []AIStagedTokenMultiplierPoint `json:"stagedTokenMultiplierPoints"`
	ContextCompressionEnabled        bool                           `json:"contextCompressionEnabled"`
	UseRemoteContextCompression      bool                           `json:"useRemoteContextCompression"`
	ContextCompressionTriggerPercent int                            `json:"contextCompressionTriggerPercent"`
	ContextCompressionTargetPercent  int                            `json:"contextCompressionTargetPercent"`
	ContextCompressionMaxAttempts    int                            `json:"contextCompressionMaxAttempts"`
	ContextCompressionOrder          []string                       `json:"contextCompressionOrder"`
}

type UpdateAISearchConfigInput struct {
	Enabled                          bool                            `json:"enabled"`
	BaseURL                          string                          `json:"baseUrl"`
	APIKey                           string                          `json:"apiKey"`
	Model                            string                          `json:"model"`
	BackupModel                      *string                         `json:"backupModel,omitempty"`
	ManualModels                     []string                        `json:"manualModels"`
	Prompt                           string                          `json:"prompt"`
	Remark                           string                          `json:"remark"`
	Temperature                      float64                         `json:"temperature"`
	TopP                             float64                         `json:"topP"`
	MaxAISearchModelCalls            int                             `json:"maxAISearchModelCalls"`
	MaxAISearches                    int                             `json:"maxAISearches"`
	MaxAISearchRetries               int                             `json:"maxAISearchRetries"`
	SearchPricePerCall               *float64                        `json:"searchPricePerCall,omitempty"`
	MaxAIWebReads                    int                             `json:"maxAIWebReads"`
	WebReadPricePerCall              *float64                        `json:"webReadPricePerCall,omitempty"`
	ImageOCRPricePerCall             *float64                        `json:"imageOCRPricePerCall,omitempty"`
	TikuSearchPricePerCall           *float64                        `json:"tiku_search_price_per_call,omitempty"`
	EnableAIStagedTokenBilling       *bool                           `json:"enableAIStagedTokenBilling,omitempty"`
	SmoothAIStagedTokenMultiplier    *bool                           `json:"smoothAIStagedTokenMultiplier,omitempty"`
	StagedTokenMultiplierPoints      *[]AIStagedTokenMultiplierPoint `json:"stagedTokenMultiplierPoints,omitempty"`
	ContextCompressionEnabled        *bool                           `json:"contextCompressionEnabled,omitempty"`
	UseRemoteContextCompression      *bool                           `json:"useRemoteContextCompression,omitempty"`
	ContextCompressionTriggerPercent *int                            `json:"contextCompressionTriggerPercent,omitempty"`
	ContextCompressionTargetPercent  *int                            `json:"contextCompressionTargetPercent,omitempty"`
	ContextCompressionMaxAttempts    *int                            `json:"contextCompressionMaxAttempts,omitempty"`
	ContextCompressionOrder          *[]string                       `json:"contextCompressionOrder,omitempty"`
}

type AISearchRequestConfig struct {
	BaseURL                          string
	APIKey                           string
	Model                            string
	BackupModel                      string
	Prompt                           string
	Temperature                      float64
	TopP                             float64
	MaxAISearchModelCalls            int
	MaxAISearches                    int
	MaxAISearchRetries               int
	SearchPricePerCall               float64
	MaxAIWebReads                    int
	WebReadPricePerCall              float64
	ImageOCRPricePerCall             float64
	TikuSearchPricePerCall           float64
	ContextCompressionEnabled        bool
	UseRemoteContextCompression      bool
	ContextCompressionTriggerPercent int
	ContextCompressionTargetPercent  int
	ContextCompressionMaxAttempts    int
	ContextCompressionOrder          []string
}

type ResolvedAISearchConfig struct {
	Config             AISearchRequestConfig `json:"config"`
	UsedDefaultAccount bool                  `json:"usedDefaultAccount"`
}

// AISearchModelPricingStatus 描述当前 AI 配置所用模型的定价情况。
// 未配置价格的模型会按 0 计费，因此需要显式提示管理员去模型价格表补价。
type AISearchModelPricingStatus struct {
	// ConfiguredModel 是当前生效的默认模型名。
	ConfiguredModel string `json:"configuredModel"`
	// ConfiguredModelPriced 表示 ConfiguredModel 是否已在价格表中配置。
	ConfiguredModelPriced bool `json:"configuredModelPriced"`
	// UnpricedModels 是所有已配置但缺少价格的模型。
	UnpricedModels []string `json:"unpricedModels"`
	// HasUnpricedModel 便于前端直接判断是否需要展示提示。
	HasUnpricedModel bool `json:"hasUnpricedModel"`
}

type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// defaultAISearchPrompt 统一复用搜索客户端的默认提示词，
// 避免 service 与 search 两份副本内容漂移，导致配置页显示与实际生效的提示词不一致。
func defaultAISearchPrompt() string {
	return internalsearch.DefaultAISearchPrompt()
}

func effectiveAISearchPrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return defaultAISearchPrompt()
	}
	return trimmed
}

// InspectAISearchModelPricingWithDB 检查「默认模型 + 模型列表」里哪些模型没有配置价格。
// 结果用于管理页提示，以及调用方在账单里看到 0 元时的排查依据。
func InspectAISearchModelPricingWithDB(db *gorm.DB) (AISearchModelPricingStatus, error) {
	status := AISearchModelPricingStatus{UnpricedModels: []string{}}

	config, exists, err := getAISearchConfigRecord(db)
	if err != nil {
		return status, err
	}
	if !exists {
		return status, nil
	}

	configuredModel := strings.TrimSpace(config.Model)
	status.ConfiguredModel = configuredModel

	candidates := make([]string, 0, 8)
	if configuredModel != "" {
		candidates = append(candidates, configuredModel)
	}
	if backupModel := strings.TrimSpace(config.BackupModel); backupModel != "" {
		candidates = append(candidates, backupModel)
	}
	manualModels, err := unmarshalAISearchManualModels(config.ManualModels)
	if err != nil {
		return status, err
	}
	candidates = append(candidates, manualModels...)
	candidates = normalizeAISearchManualModels(candidates)

	seen := make(map[string]struct{}, len(candidates))
	for _, modelName := range candidates {
		_, found, resolveErr := ResolveAIModelTokenPriceWithDB(db, modelName)
		if resolveErr != nil {
			return status, resolveErr
		}
		if found {
			if modelName == configuredModel {
				status.ConfiguredModelPriced = true
			}
			continue
		}
		key := strings.ToLower(modelName)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		status.UnpricedModels = append(status.UnpricedModels, modelName)
	}

	status.HasUnpricedModel = len(status.UnpricedModels) > 0
	return status, nil
}

func GetAISearchConfigWithDB(db *gorm.DB) (AISearchConfigView, error) {
	config, exists, err := getAISearchConfigRecord(db)
	if err != nil {
		return AISearchConfigView{}, err
	}
	if !exists {
		maxModelCalls, maxSearches := GetAISearchBudgetsWithDB(db)
		stagedBilling := GetAIStagedTokenBillingSettingsWithDB(db)
		compression := GetAIContextCompressionSettingsWithDB(db)
		return AISearchConfigView{
			Prompt:                           defaultAISearchPrompt(),
			Temperature:                      0,
			TopP:                             0,
			MaxAISearchModelCalls:            maxModelCalls,
			MaxAISearches:                    maxSearches,
			MaxAISearchRetries:               GetAISearchRetriesWithDB(db),
			SearchPricePerCall:               GetAISearchPricePerCallWithDB(db),
			MaxAIWebReads:                    getPositiveIntSystemSetting(db, MaxAIWebReadsSettingKey, DefaultMaxAIWebReads),
			WebReadPricePerCall:              GetWebReadPricePerCallWithDB(db),
			ImageOCRPricePerCall:             GetImageOCRPricePerCallWithDB(db),
			TikuSearchPricePerCall:           GetTikuSearchPricePerCallWithDB(db),
			EnableAIStagedTokenBilling:       stagedBilling.Enabled,
			SmoothAIStagedTokenMultiplier:    stagedBilling.SmoothFactor,
			StagedTokenMultiplierPoints:      cloneAIStagedTokenMultiplierPoints(stagedBilling.MultiplierPoints),
			ContextCompressionEnabled:        compression.Enabled,
			UseRemoteContextCompression:      compression.UseRemote,
			ContextCompressionTriggerPercent: compression.TriggerPercent,
			ContextCompressionTargetPercent:  compression.TargetPercent,
			ContextCompressionMaxAttempts:    compression.MaxAttempts,
			ContextCompressionOrder:          append([]string(nil), compression.Order...),
		}, nil
	}
	return buildAISearchConfigView(db, config)
}

func UpdateAISearchConfigWithDB(db *gorm.DB, input UpdateAISearchConfigInput) (AISearchConfigView, error) {
	if err := validateAISearchCountInputs(input.MaxAISearchModelCalls, input.MaxAISearches, input.MaxAISearchRetries, input.MaxAIWebReads); err != nil {
		return AISearchConfigView{}, err
	}
	if _, err := mergeAIContextCompressionSettings(db, input); err != nil {
		return AISearchConfigView{}, err
	}
	if input.SearchPricePerCall != nil {
		price := *input.SearchPricePerCall
		if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			return AISearchConfigView{}, errors.New("联网搜索单价必须是大于或等于 0 的有效金额")
		}
	}
	if input.WebReadPricePerCall != nil {
		if err := SetWebReadPricePerCallWithDB(db, *input.WebReadPricePerCall); err != nil {
			return AISearchConfigView{}, err
		}
	}
	if input.ImageOCRPricePerCall != nil {
		if err := SetImageOCRPricePerCallWithDB(db, *input.ImageOCRPricePerCall); err != nil {
			return AISearchConfigView{}, err
		}
	}
	if input.TikuSearchPricePerCall != nil {
		if err := SetTikuSearchPricePerCallWithDB(db, *input.TikuSearchPricePerCall); err != nil {
			return AISearchConfigView{}, err
		}
	}
	if err := SetAIStagedTokenBillingSettingsWithDB(db, input.EnableAIStagedTokenBilling, input.SmoothAIStagedTokenMultiplier, input.StagedTokenMultiplierPoints); err != nil {
		return AISearchConfigView{}, err
	}
	config, _, err := getAISearchConfigRecord(db)
	if err != nil {
		return AISearchConfigView{}, err
	}
	if config.Provider == "" {
		config.Provider = aiSearchAccountProvider
	}

	manualModels := normalizeAISearchManualModels(input.ManualModels)
	manualModelsJSON, err := marshalAISearchManualModels(manualModels)
	if err != nil {
		return AISearchConfigView{}, err
	}

	config.Enabled = input.Enabled
	config.BaseURL = normalizeAISearchBaseURL(input.BaseURL)
	config.APIKey = mergeSecretInput(input.APIKey, config.APIKey)
	config.Model = strings.TrimSpace(input.Model)
	if input.BackupModel != nil {
		config.BackupModel = strings.TrimSpace(*input.BackupModel)
		if config.BackupModel == config.Model {
			config.BackupModel = ""
		}
	}
	config.ManualModels = manualModelsJSON
	config.Prompt = strings.TrimSpace(input.Prompt)
	config.Remark = strings.TrimSpace(input.Remark)
	config.Temperature = 0
	config.TopP = 0

	if err := db.Save(&config).Error; err != nil {
		return AISearchConfigView{}, err
	}
	if input.SearchPricePerCall != nil {
		if err := SetAISearchPricePerCallWithDB(db, *input.SearchPricePerCall); err != nil {
			return AISearchConfigView{}, err
		}
	}
	if input.MaxAISearchModelCalls > 0 || input.MaxAISearches > 0 {
		if err := SetAISearchBudgetsWithDB(db, input.MaxAISearchModelCalls, input.MaxAISearches); err != nil {
			return AISearchConfigView{}, err
		}
	}
	if input.MaxAISearchRetries > 0 {
		if err := SetAISearchRetriesWithDB(db, input.MaxAISearchRetries); err != nil {
			return AISearchConfigView{}, err
		}
	}
	if input.MaxAIWebReads > 0 {
		if err := saveSystemSettingValue(db, MaxAIWebReadsSettingKey, strconv.Itoa(input.MaxAIWebReads), "AI 搜索网页读取最大次数"); err != nil {
			return AISearchConfigView{}, err
		}
	}

	if err := saveAIContextCompressionSettingsWithDB(db, input); err != nil {
		return AISearchConfigView{}, err
	}
	return buildAISearchConfigView(db, config)
}

func BuildAISearchRequestConfigWithDB(db *gorm.DB, query url.Values, allowDefaultAccount bool, explicitConfigAllowed ...bool) (ResolvedAISearchConfig, error) {
	maxModelCalls, maxSearches := GetAISearchBudgetsWithDB(db)
	compression := GetAIContextCompressionSettingsWithDB(db)
	resolved := ResolvedAISearchConfig{
		Config: AISearchRequestConfig{
			Prompt:                           defaultAISearchPrompt(),
			Temperature:                      0,
			TopP:                             0,
			MaxAISearchModelCalls:            maxModelCalls,
			MaxAISearches:                    maxSearches,
			MaxAISearchRetries:               GetAISearchRetriesWithDB(db),
			SearchPricePerCall:               GetAISearchPricePerCallWithDB(db),
			MaxAIWebReads:                    getPositiveIntSystemSetting(db, MaxAIWebReadsSettingKey, DefaultMaxAIWebReads),
			WebReadPricePerCall:              GetWebReadPricePerCallWithDB(db),
			ImageOCRPricePerCall:             GetImageOCRPricePerCallWithDB(db),
			TikuSearchPricePerCall:           GetTikuSearchPricePerCallWithDB(db),
			ContextCompressionEnabled:        compression.Enabled,
			UseRemoteContextCompression:      compression.UseRemote,
			ContextCompressionTriggerPercent: compression.TriggerPercent,
			ContextCompressionTargetPercent:  compression.TargetPercent,
			ContextCompressionMaxAttempts:    compression.MaxAttempts,
			ContextCompressionOrder:          append([]string(nil), compression.Order...),
		},
	}
	allowExplicitConfig := allowDefaultAccount
	if len(explicitConfigAllowed) > 0 {
		allowExplicitConfig = explicitConfigAllowed[0]
	}
	if allowExplicitConfig {
		resolved.Config.BaseURL = normalizeAISearchBaseURL(query.Get("url"))
		resolved.Config.APIKey = normalizeAISearchSecretInput(query.Get("key"))
		resolved.Config.Model = strings.TrimSpace(query.Get("model"))
	}

	defaultConfig, exists, err := getAISearchConfigRecord(db)
	if err != nil {
		return resolved, err
	}
	if !exists || !defaultConfig.Enabled {
		return resolved, nil
	}
	if !allowDefaultAccount {
		return resolved, nil
	}

	usedDefault := false
	if resolved.Config.BaseURL == "" {
		resolved.Config.BaseURL = normalizeAISearchBaseURL(defaultConfig.BaseURL)
		if resolved.Config.BaseURL != "" {
			usedDefault = true
		}
	}
	if resolved.Config.APIKey == "" {
		resolved.Config.APIKey = strings.TrimSpace(defaultConfig.APIKey)
		if resolved.Config.APIKey != "" {
			usedDefault = true
		}
	}
	if resolved.Config.Model == "" {
		resolved.Config.Model = strings.TrimSpace(defaultConfig.Model)
		if resolved.Config.Model != "" {
			usedDefault = true
		}
	}
	// A backup model belongs to the saved account. Explicit endpoint/key
	// overrides must not silently inherit it from another provider account.
	storedAccount := normalizeAISearchBaseURL(resolved.Config.BaseURL) == normalizeAISearchBaseURL(defaultConfig.BaseURL) &&
		strings.TrimSpace(resolved.Config.APIKey) == strings.TrimSpace(defaultConfig.APIKey) &&
		strings.TrimSpace(resolved.Config.Model) == strings.TrimSpace(defaultConfig.Model)
	if storedAccount {
		resolved.Config.BackupModel = strings.TrimSpace(defaultConfig.BackupModel)
		if resolved.Config.BackupModel == resolved.Config.Model {
			resolved.Config.BackupModel = ""
		}
	}
	resolved.Config.Prompt = effectiveAISearchPrompt(defaultConfig.Prompt)
	resolved.Config.Temperature = 0
	resolved.Config.TopP = 0
	resolved.UsedDefaultAccount = usedDefault

	return resolved, nil
}

func normalizeAISearchSecretInput(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == SecretMask {
		return ""
	}
	return trimmed
}

func IsAISearchConfigUsable(config AISearchRequestConfig) bool {
	return strings.TrimSpace(config.BaseURL) != "" && strings.TrimSpace(config.APIKey) != "" && strings.TrimSpace(config.Model) != ""
}

func ListAISearchModelsWithDB(db *gorm.DB, query url.Values) ([]string, error) {
	resolved, err := BuildAISearchRequestConfigWithDB(db, query, true)
	if err != nil {
		return nil, err
	}
	config := resolved.Config

	manualModels := []string{}
	storedConfig, exists, err := getAISearchConfigRecord(db)
	if err != nil {
		return nil, err
	}
	if exists {
		manualModels, err = unmarshalAISearchManualModels(storedConfig.ManualModels)
		if err != nil {
			return nil, err
		}
	}

	if config.BaseURL == "" || config.APIKey == "" {
		return manualModels, nil
	}

	remoteModels, err := fetchRemoteAISearchModels(config.BaseURL, config.APIKey)
	if err != nil {
		return manualModels, nil
	}

	return mergeAISearchModelLists(manualModels, remoteModels), nil
}

func buildAISearchConfigView(db *gorm.DB, config entity.AISearchConfig) (AISearchConfigView, error) {
	manualModels, err := unmarshalAISearchManualModels(config.ManualModels)
	if err != nil {
		return AISearchConfigView{}, err
	}
	maxModelCalls, maxSearches := GetAISearchBudgetsWithDB(db)
	stagedBilling := GetAIStagedTokenBillingSettingsWithDB(db)
	compression := GetAIContextCompressionSettingsWithDB(db)

	return AISearchConfigView{
		Enabled:                          config.Enabled,
		BaseURL:                          config.BaseURL,
		APIKey:                           config.APIKey,
		Model:                            config.Model,
		BackupModel:                      config.BackupModel,
		ManualModels:                     manualModels,
		Prompt:                           effectiveAISearchPrompt(config.Prompt),
		Remark:                           config.Remark,
		Temperature:                      0,
		TopP:                             0,
		MaxAISearchModelCalls:            maxModelCalls,
		MaxAISearches:                    maxSearches,
		MaxAISearchRetries:               GetAISearchRetriesWithDB(db),
		SearchPricePerCall:               GetAISearchPricePerCallWithDB(db),
		MaxAIWebReads:                    getPositiveIntSystemSetting(db, MaxAIWebReadsSettingKey, DefaultMaxAIWebReads),
		WebReadPricePerCall:              GetWebReadPricePerCallWithDB(db),
		ImageOCRPricePerCall:             GetImageOCRPricePerCallWithDB(db),
		TikuSearchPricePerCall:           GetTikuSearchPricePerCallWithDB(db),
		EnableAIStagedTokenBilling:       stagedBilling.Enabled,
		SmoothAIStagedTokenMultiplier:    stagedBilling.SmoothFactor,
		StagedTokenMultiplierPoints:      cloneAIStagedTokenMultiplierPoints(stagedBilling.MultiplierPoints),
		ContextCompressionEnabled:        compression.Enabled,
		UseRemoteContextCompression:      compression.UseRemote,
		ContextCompressionTriggerPercent: compression.TriggerPercent,
		ContextCompressionTargetPercent:  compression.TargetPercent,
		ContextCompressionMaxAttempts:    compression.MaxAttempts,
		ContextCompressionOrder:          append([]string(nil), compression.Order...),
	}, nil
}

func normalizeAISearchPositiveMultiplier(value float64) float64 {
	if value > 0 {
		return value
	}
	return 1
}

func normalizeAISearchBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	return strings.TrimRight(trimmed, "/")
}

func normalizeAISearchManualModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	normalized := make([]string, 0, len(models))
	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func mergeAISearchModelLists(base []string, extra []string) []string {
	merged := make([]string, 0, len(base)+len(extra))
	merged = append(merged, normalizeAISearchManualModels(base)...)
	seen := make(map[string]struct{}, len(merged))
	for _, model := range merged {
		seen[model] = struct{}{}
	}
	for _, model := range normalizeAISearchManualModels(extra) {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		merged = append(merged, model)
	}
	return merged
}

func buildAIModelEndpoints(baseURL string) []string {
	normalized := normalizeAISearchBaseURL(baseURL)
	if normalized == "" {
		return nil
	}

	endpoints := []string{normalized + "/models"}
	if !strings.HasSuffix(normalized, "/v1") {
		endpoints = append(endpoints, normalized+"/v1/models")
	}
	return endpoints
}

func fetchRemoteAISearchModels(baseURL string, apiKey string) ([]string, error) {
	var lastErr error
	for _, endpoint := range buildAIModelEndpoints(baseURL) {
		models, statusCode, err := requestRemoteAISearchModels(endpoint, apiKey)
		if err == nil {
			return models, nil
		}
		if statusCode == http.StatusNotFound {
			lastErr = err
			continue
		}
		return nil, err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("remote ai models unavailable")
}

func requestRemoteAISearchModels(endpoint string, apiKey string) ([]string, int, error) {
	httpReq, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(apiKey) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}

	httpClient := util.NewSafeHTTPClient(15 * time.Second)
	resp, err := util.DoReadOnlyWithRetry(httpClient, httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, resp.StatusCode, errors.New("remote ai models endpoint not found")
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, resp.StatusCode, errors.New("remote ai models request failed")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, resp.StatusCode, err
	}

	var payload openAIModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, resp.StatusCode, err
	}

	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		models = append(models, item.ID)
	}
	return normalizeAISearchManualModels(models), resp.StatusCode, nil
}

func marshalAISearchManualModels(models []string) (string, error) {
	if len(models) == 0 {
		return "[]", nil
	}
	buf, err := json.Marshal(models)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func unmarshalAISearchManualModels(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{}, nil
	}
	var models []string
	if err := json.Unmarshal([]byte(trimmed), &models); err != nil {
		return nil, err
	}
	return normalizeAISearchManualModels(models), nil
}
