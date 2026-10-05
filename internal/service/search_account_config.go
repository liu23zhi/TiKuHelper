package service

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

var ErrUnsupportedSearchAccountProvider = errors.New("不支持的题库 provider")
var ErrInvalidAccessTokenAuthorization = errors.New("无效的访问令牌")

const aiSearchAccountProvider = "ai"

type SearchAccountProviderMeta struct {
	Provider      string
	ProviderLabel string
	TokenKey      string
}

type SearchAccountConfigView struct {
	Provider      string  `json:"provider"`
	ProviderLabel string  `json:"providerLabel"`
	TokenKey      string  `json:"tokenKey"`
	TokenValue    string  `json:"tokenValue"`
	Enabled       bool    `json:"enabled"`
	Remark        string  `json:"remark"`
	FlatPrice     float64 `json:"flatPrice"`
}

type UpdateSearchAccountConfigInput struct {
	TokenValue string  `json:"tokenValue"`
	Enabled    bool    `json:"enabled"`
	Remark     string  `json:"remark"`
	FlatPrice  float64 `json:"flatPrice"`
}

type ResolvedSearchTokens struct {
	Tokens               map[string]string `json:"tokens"`
	UsedDefaultAccount   bool              `json:"usedDefaultAccount"`
	UsedDefaultProviders []string          `json:"usedDefaultProviders"`
	EnabledProviders     []string          `json:"enabledProviders"`
}

type AccessTokenAuthContext struct {
	User        entity.User        `json:"user"`
	AccessToken entity.AccessToken `json:"accessToken"`
}

var supportedSearchAccountProviders = []SearchAccountProviderMeta{
	{Provider: "icodef", ProviderLabel: "Icodef 题库", TokenKey: "icodefToken"},
	{Provider: "wanneng", ProviderLabel: "万能题库", TokenKey: "wannengToken"},
	{Provider: "tikuhai", ProviderLabel: "题库海", TokenKey: "tikuhaiToken"},
	{Provider: "enncy", ProviderLabel: "Enncy 言溪题库", TokenKey: "enncyToken"},
	{Provider: "aidian", ProviderLabel: "爱点题库", TokenKey: "aidianYToken"},
	{Provider: "lemon", ProviderLabel: "柠檬题库", TokenKey: "lemonToken"},
	{Provider: "local", ProviderLabel: "本地题库", TokenKey: ""},
	{Provider: aiSearchAccountProvider, ProviderLabel: "AI 搜索", TokenKey: ""},
}

func ListSearchAccountConfigsWithDB(db *gorm.DB) ([]SearchAccountConfigView, error) {
	var configs []entity.SearchAccountConfig
	if err := db.Find(&configs).Error; err != nil {
		return nil, err
	}

	configMap := make(map[string]entity.SearchAccountConfig, len(configs))
	for _, config := range configs {
		configMap[config.Provider] = config
	}

	aiConfig, aiExists, err := getAISearchConfigRecord(db)
	if err != nil {
		return nil, err
	}

	views := make([]SearchAccountConfigView, 0, len(supportedSearchAccountProviders))
	for _, meta := range supportedSearchAccountProviders {
		view := SearchAccountConfigView{
			Provider:      meta.Provider,
			ProviderLabel: meta.ProviderLabel,
			TokenKey:      meta.TokenKey,
		}

		if meta.Provider == aiSearchAccountProvider {
			if aiExists {
				view.Enabled = aiConfig.Enabled
				view.Remark = aiConfig.Remark
			}
			views = append(views, view)
			continue
		}

		config, ok := configMap[meta.Provider]
		if ok {
			view.TokenValue = config.TokenValue
			view.Enabled = config.Enabled
			view.Remark = config.Remark
			view.FlatPrice = config.FlatPrice
		}
		views = append(views, view)
	}

	return views, nil
}

func UpdateSearchAccountConfigWithDB(db *gorm.DB, provider string, input UpdateSearchAccountConfigInput) (SearchAccountConfigView, error) {
	meta, ok := getSearchAccountProviderMeta(provider)
	if !ok {
		return SearchAccountConfigView{}, fmt.Errorf("%w: %s", ErrUnsupportedSearchAccountProvider, provider)
	}

	if provider == aiSearchAccountProvider {
		config, err := upsertAISearchAccountConfig(db, input)
		if err != nil {
			return SearchAccountConfigView{}, err
		}

		return SearchAccountConfigView{
			Provider:      meta.Provider,
			ProviderLabel: meta.ProviderLabel,
			TokenKey:      meta.TokenKey,
			Enabled:       config.Enabled,
			Remark:        config.Remark,
		}, nil
	}

	var config entity.SearchAccountConfig
	if err := db.Where("provider = ?", provider).First(&config).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return SearchAccountConfigView{}, err
		}
		config.Provider = provider
	}

	config.TokenKey = meta.TokenKey
	config.Enabled = input.Enabled
	config.Remark = input.Remark
	config.FlatPrice = input.FlatPrice
	if config.Provider == "" {
		config.Provider = provider
	}
	config.TokenValue = mergeSecretInput(input.TokenValue, config.TokenValue)

	if err := db.Save(&config).Error; err != nil {
		return SearchAccountConfigView{}, err
	}

	return SearchAccountConfigView{
		Provider:      meta.Provider,
		ProviderLabel: meta.ProviderLabel,
		TokenKey:      meta.TokenKey,
		TokenValue:    config.TokenValue,
		Enabled:       config.Enabled,
		Remark:        config.Remark,
		FlatPrice:     config.FlatPrice,
	}, nil
}

func BuildRequestSearchTokens(query url.Values) map[string]string {
	tokens := make(map[string]string, len(supportedSearchAccountProviders))
	for _, meta := range supportedSearchAccountProviders {
		tokens[meta.Provider] = query.Get(meta.TokenKey)
	}
	return tokens
}

func BuildSearchTokensWithDB(db *gorm.DB, query url.Values, allowDefaultAccount bool) (ResolvedSearchTokens, error) {
	resolved := ResolvedSearchTokens{
		Tokens: BuildRequestSearchTokens(query),
	}

	var configs []entity.SearchAccountConfig
	if err := db.Find(&configs).Error; err != nil {
		return resolved, err
	}

	configMap := make(map[string]entity.SearchAccountConfig, len(configs))
	for _, config := range configs {
		configMap[config.Provider] = config
	}

	for _, meta := range supportedSearchAccountProviders {
		config, ok := configMap[meta.Provider]
		if ok && config.Enabled {
			resolved.EnabledProviders = append(resolved.EnabledProviders, meta.Provider)
		}
		if !allowDefaultAccount {
			continue
		}
		if resolved.Tokens[meta.Provider] != "" {
			continue
		}
		if !ok {
			continue
		}
		if !config.Enabled {
			continue
		}
		if strings.TrimSpace(config.TokenValue) == "" {
			continue
		}
		resolved.Tokens[meta.Provider] = config.TokenValue
		resolved.UsedDefaultAccount = true
		resolved.UsedDefaultProviders = append(resolved.UsedDefaultProviders, meta.Provider)
	}

	return resolved, nil
}

func ResolveAccessTokenFromBasicAuth(db *gorm.DB, authorization string) (AccessTokenAuthContext, error) {
	tokenValue, err := parseBasicToken(authorization)
	if err != nil {
		reason := "访问令牌不能为空"
		trimmedAuthorization := strings.TrimSpace(authorization)
		if trimmedAuthorization != "" {
			reason = "Authorization 必须使用 Basic 或 Bearer 方案，或直接传入访问令牌"
		}
		return AccessTokenAuthContext{}, fmt.Errorf("%w: %s", ErrInvalidAccessTokenAuthorization, reason)
	}

	tokenHash := hashAccessTokenValue(tokenValue)

	freshDB := db
	if freshDB == nil {
		return AccessTokenAuthContext{}, fmt.Errorf("%w: 数据库不可用", ErrInvalidAccessTokenAuthorization)
	}
	freshDB = freshDB.Session(&gorm.Session{NewDB: true})

	var accessToken entity.AccessToken
	if err := freshDB.Where("token_hash = ?", tokenHash).First(&accessToken).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AccessTokenAuthContext{}, fmt.Errorf("%w: 访问令牌不存在", ErrInvalidAccessTokenAuthorization)
		}
		return AccessTokenAuthContext{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(accessToken.Status), "enabled") {
		return AccessTokenAuthContext{}, fmt.Errorf("%w: 访问令牌已禁用", ErrInvalidAccessTokenAuthorization)
	}

	var user entity.User
	if err := freshDB.Where("id = ?", accessToken.UserID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AccessTokenAuthContext{}, fmt.Errorf("%w: 访问令牌所属用户不存在", ErrInvalidAccessTokenAuthorization)
		}
		return AccessTokenAuthContext{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") {
		return AccessTokenAuthContext{}, fmt.Errorf("%w: 访问令牌所属用户已禁用", ErrInvalidAccessTokenAuthorization)
	}

	return AccessTokenAuthContext{
		User:        user,
		AccessToken: accessToken,
	}, nil
}

func parseBasicToken(authorization string) (string, error) {
	trimmed := strings.TrimSpace(authorization)
	if trimmed == "" {
		return "", ErrInvalidAccessTokenAuthorization
	}
	parts := strings.Fields(trimmed)
	if len(parts) == 1 {
		if parts[0] == "" {
			return "", ErrInvalidAccessTokenAuthorization
		}
		return parts[0], nil
	}
	if len(parts) != 2 {
		return "", ErrInvalidAccessTokenAuthorization
	}
	if !strings.EqualFold(parts[0], "Basic") && !strings.EqualFold(parts[0], "Bearer") {
		return "", ErrInvalidAccessTokenAuthorization
	}
	tokenValue := strings.TrimSpace(parts[1])
	if tokenValue == "" {
		return "", ErrInvalidAccessTokenAuthorization
	}
	return tokenValue, nil
}

func getSearchAccountProviderMeta(provider string) (SearchAccountProviderMeta, bool) {
	for _, meta := range supportedSearchAccountProviders {
		if meta.Provider == provider {
			return meta, true
		}
	}
	return SearchAccountProviderMeta{}, false
}

func SearchAccountProviderRequiresToken(provider string) bool {
	meta, ok := getSearchAccountProviderMeta(strings.TrimSpace(provider))
	if !ok {
		return false
	}
	return strings.TrimSpace(meta.TokenKey) != ""
}

func ResolveSearchAccountProviderLabel(provider string) string {
	trimmed := strings.TrimSpace(provider)
	if trimmed == "" {
		return ""
	}
	if meta, ok := getSearchAccountProviderMeta(trimmed); ok {
		return meta.ProviderLabel
	}
	return trimmed
}

func getAISearchConfigRecord(db *gorm.DB) (entity.AISearchConfig, bool, error) {
	var config entity.AISearchConfig
	if err := db.Where("provider = ?", aiSearchAccountProvider).First(&config).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.AISearchConfig{}, false, nil
		}
		return entity.AISearchConfig{}, false, err
	}
	return config, true, nil
}

func upsertAISearchAccountConfig(db *gorm.DB, input UpdateSearchAccountConfigInput) (entity.AISearchConfig, error) {
	config, _, err := getAISearchConfigRecord(db)
	if err != nil {
		return entity.AISearchConfig{}, err
	}
	if config.Provider == "" {
		config.Provider = aiSearchAccountProvider
	}
	config.Enabled = input.Enabled
	config.Remark = input.Remark
	if err := db.Save(&config).Error; err != nil {
		return entity.AISearchConfig{}, err
	}
	return config, nil
}
