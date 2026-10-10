package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

type AISearchConfigView struct {
	Enabled      bool     `json:"enabled"`
	BaseURL      string   `json:"baseUrl"`
	APIKey       string   `json:"apiKey"`
	Model        string   `json:"model"`
	ManualModels []string `json:"manualModels"`
	Prompt       string   `json:"prompt"`
	Remark       string   `json:"remark"`
	Temperature  float64  `json:"temperature"`
	TopP         float64  `json:"topP"`
}

type UpdateAISearchConfigInput struct {
	Enabled      bool     `json:"enabled"`
	BaseURL      string   `json:"baseUrl"`
	APIKey       string   `json:"apiKey"`
	Model        string   `json:"model"`
	ManualModels []string `json:"manualModels"`
	Prompt       string   `json:"prompt"`
	Remark       string   `json:"remark"`
	Temperature  float64  `json:"temperature"`
	TopP         float64  `json:"topP"`
}

type AISearchRequestConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	Prompt      string
	Temperature float64
	TopP        float64
}

type ResolvedAISearchConfig struct {
	Config             AISearchRequestConfig `json:"config"`
	UsedDefaultAccount bool                  `json:"usedDefaultAccount"`
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

func GetAISearchConfigWithDB(db *gorm.DB) (AISearchConfigView, error) {
	config, exists, err := getAISearchConfigRecord(db)
	if err != nil {
		return AISearchConfigView{}, err
	}
	if !exists {
		return AISearchConfigView{
			Prompt:      defaultAISearchPrompt(),
			Temperature: 0,
			TopP:        0,
		}, nil
	}
	return buildAISearchConfigView(config)
}

func UpdateAISearchConfigWithDB(db *gorm.DB, input UpdateAISearchConfigInput) (AISearchConfigView, error) {
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
	config.ManualModels = manualModelsJSON
	config.Prompt = strings.TrimSpace(input.Prompt)
	config.Remark = strings.TrimSpace(input.Remark)
	config.Temperature = 0
	config.TopP = 0

	if err := db.Save(&config).Error; err != nil {
		return AISearchConfigView{}, err
	}

	return buildAISearchConfigView(config)
}

func BuildAISearchRequestConfigWithDB(db *gorm.DB, query url.Values, allowDefaultAccount bool, explicitConfigAllowed ...bool) (ResolvedAISearchConfig, error) {
	resolved := ResolvedAISearchConfig{
		Config: AISearchRequestConfig{
			Prompt:      defaultAISearchPrompt(),
			Temperature: 0,
			TopP:        0,
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

func buildAISearchConfigView(config entity.AISearchConfig) (AISearchConfigView, error) {
	manualModels, err := unmarshalAISearchManualModels(config.ManualModels)
	if err != nil {
		return AISearchConfigView{}, err
	}

	return AISearchConfigView{
		Enabled:      config.Enabled,
		BaseURL:      config.BaseURL,
		APIKey:       config.APIKey,
		Model:        config.Model,
		ManualModels: manualModels,
		Prompt:       effectiveAISearchPrompt(config.Prompt),
		Remark:       config.Remark,
		Temperature:  0,
		TopP:         0,
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
	resp, err := httpClient.Do(httpReq)
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
