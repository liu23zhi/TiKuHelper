package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gookit/goutil/strutil"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/global"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	searchModeNormal                = "normal"
	searchModeAI                    = "ai"
	searchBillingProviderLocal      = "local"
	searchBillingRecordTypeNormal   = "normal_search"
	searchAuditModule               = "search"
	maxSearchUnauthorizedBodyLength = 4096
)

var errAccessTokenQuotaExceeded = errors.New("访问令牌累计额度不足")

type searchUnauthorizedRequestSnapshot struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Origin        string `json:"origin"`
	ContentType   string `json:"contentType"`
	UserAgent     string `json:"userAgent"`
	ClientIP      string `json:"clientIp"`
	Authorization string `json:"authorization"`
	Body          string `json:"body"`
}

type searchUnauthorizedResponse struct {
	ErrCode int                               `json:"errCode"`
	Message string                            `json:"message"`
	Detail  string                            `json:"detail"`
	Request searchUnauthorizedRequestSnapshot `json:"request"`
}

type searchAuditMetadata struct {
	HitProvider     string
	RawRequest      string
	RawResponse     string
	Anonymous       bool
	LocalQuestionID int32
}

type resolvedSearchRequestContext struct {
	Tokens                     map[string]string
	EnabledSearchProviders     []string
	AIConfig                   service.AISearchRequestConfig
	AccessTokenCtx             *service.AccessTokenAuthContext
	UsedDefaultSearch          bool
	UsedDefaultSearchProviders []string
	UsedDefaultAI              bool
	UserID                     int32
	AccessTokenID              int32
	RequestID                  string
	SearchMode                 string
}

type searchBillingContext struct {
	UserID             int32
	AccessTokenID      int32
	RequestID          string
	SearchMode         string
	UsedDefaultAccount bool
	Billable           bool
}

type searchBillingCharge struct {
	Provider     string
	Amount       float64
	Snapshot     string
	ModelName    string
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

var searchBillingReporter = func(ctx searchBillingContext) {}

var defaultDBSearchProvider = func() func(model.SearchRequest) ([][]string, error) {
	return search.GetDBSearch().SearchAnswer
}

var resolveSearchRequestContext = func(authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
	return resolveSearchRequestContextWithDB(manager.GetManager().GetDB(), authorization, clientIP, query)
}

type searchClientBinding struct {
	Provider string
	Search   func(model.SearchRequest) ([][]string, error)
}

func isNormalSearchProviderEnabledBySystem(resolved resolvedSearchRequestContext, provider string) bool {
	trimmedProvider := strings.TrimSpace(provider)
	if trimmedProvider == "" {
		return false
	}
	for _, enabledProvider := range resolved.EnabledSearchProviders {
		if strings.TrimSpace(enabledProvider) == trimmedProvider {
			return true
		}
	}
	return false
}

func shouldEnableNormalSearchProvider(resolved resolvedSearchRequestContext, useQuery string, provider string) bool {
	trimmedProvider := strings.TrimSpace(provider)
	if trimmedProvider == "" {
		return false
	}
	if strings.TrimSpace(useQuery) != "" {
		if !strings.Contains(useQuery, trimmedProvider) {
			return false
		}
		if trimmedProvider == searchBillingProviderLocal {
			return isNormalSearchProviderEnabledBySystem(resolved, trimmedProvider)
		}
		if service.SearchAccountProviderRequiresToken(trimmedProvider) {
			return strings.TrimSpace(resolved.Tokens[trimmedProvider]) != ""
		}
		return isNormalSearchProviderEnabledBySystem(resolved, trimmedProvider)
	}
	if trimmedProvider == searchBillingProviderLocal {
		return false
	}
	return isNormalSearchProviderEnabledBySystem(resolved, trimmedProvider)
}

var buildSearchClientBindings = func(resolved resolvedSearchRequestContext, useQuery string) []searchClientBinding {
	clients := []searchClientBinding{
		{
			Provider: "buguake",
			Search: (&search.BuguakeClient{
				Enable: shouldEnableNormalSearchProvider(resolved, useQuery, "buguake"),
			}).SearchAnswer,
		},
		{
			Provider: "icodef",
			Search: (&search.IcodefClient{
				Token:  resolved.Tokens["icodef"],
				Enable: shouldEnableNormalSearchProvider(resolved, useQuery, "icodef"),
			}).SearchAnswer,
		},
		{
			Provider: "wanneng",
			Search: (&search.WannengClient{
				Token:  resolved.Tokens["wanneng"],
				Enable: shouldEnableNormalSearchProvider(resolved, useQuery, "wanneng"),
			}).SearchAnswer,
		},
		{
			Provider: "tikuhai",
			Search: (&search.TikuhaiClient{
				Enable: shouldEnableNormalSearchProvider(resolved, useQuery, "tikuhai"),
				Token:  resolved.Tokens["tikuhai"],
			}).SearchAnswer,
		},
		{
			Provider: "enncy",
			Search: (&search.EnncyClient{
				Token:  resolved.Tokens["enncy"],
				Enable: shouldEnableNormalSearchProvider(resolved, useQuery, "enncy"),
			}).SearchAnswer,
		},
		{
			Provider: "aidian",
			Search: (&search.AidianClient{
				Enable: shouldEnableNormalSearchProvider(resolved, useQuery, "aidian"),
				YToken: resolved.Tokens["aidian"],
			}).SearchAnswer,
		},
		{
			Provider: "lemon",
			Search: (&search.LemonClient{
				Enable: shouldEnableNormalSearchProvider(resolved, useQuery, "lemon"),
				Token:  resolved.Tokens["lemon"],
			}).SearchAnswer,
		},
	}
	cfg := manager.GetManager().GetConfig()
	for _, api := range cfg.API {
		if strings.Contains(useQuery, api.Name) {
			clients = append(clients, searchClientBinding{
				Provider: strings.TrimSpace(api.Name),
				Search:   api.SearchAnswer,
			})
		}
	}
	return clients
}

var buildSearchClients = func(resolved resolvedSearchRequestContext, useQuery string) []func(model.SearchRequest) ([][]string, error) {
	bindings := buildSearchClientBindings(resolved, useQuery)
	clients := make([]func(model.SearchRequest) ([][]string, error), 0, len(bindings))
	for _, binding := range bindings {
		clients = append(clients, binding.Search)
	}
	return clients
}

var searchAIAnswer = func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, search.AISearchUsage, error) {
	client := &search.ChatGptClient{
		Enable:      shouldUseAISearch("ai", resolved.AIConfig),
		BaseURL:     resolved.AIConfig.BaseURL,
		APIKey:      resolved.AIConfig.APIKey,
		Model:       resolved.AIConfig.Model,
		Prompt:      resolved.AIConfig.Prompt,
		Temperature: resolved.AIConfig.Temperature,
		TopP:        resolved.AIConfig.TopP,
	}
	logger.SysLog(fmt.Sprintf("AI搜题开始 requestId=%s %s", strings.TrimSpace(resolved.RequestID), buildSearchRequestDebugSummary(req)))
	answer, usage, err := client.SearchAnswerWithUsage(req)
	if err != nil {
		logger.SysError(fmt.Sprintf("AI搜题失败 requestId=%s usage=%s answers=%s error=%s", strings.TrimSpace(resolved.RequestID), buildAISearchUsageDebugSummary(usage), buildSearchAnswerGroupsDebugSummary(answer), err.Error()))
		return answer, usage, err
	}
	logger.SysLog(fmt.Sprintf("AI搜题完成 requestId=%s usage=%s answers=%s", strings.TrimSpace(resolved.RequestID), buildAISearchUsageDebugSummary(usage), buildSearchAnswerGroupsDebugSummary(answer)))
	return answer, usage, err
}

// Search 搜题接口
func Search(c *gin.Context) {
	rawBody := readAndRestoreRequestBody(c)

	var req model.SearchRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, global.ErrorParam)
		return
	}

	query := c.Request.URL.Query()
	useQuery := query.Get("use")
	resolveResult, resolveErr := resolveSearchRequestContext(c.GetHeader("Authorization"), c.ClientIP(), query)
	if resolveErr != nil {
		if errors.Is(resolveErr, service.ErrInvalidAccessTokenAuthorization) {
			writeSearchUnauthorized(c, resolveErr, rawBody)
			return
		}
		logger.SysError(fmt.Sprintf("构建搜题请求上下文失败：%s", resolveErr.Error()))
		resolveResult = resolvedSearchRequestContext{
			Tokens:     service.BuildRequestSearchTokens(query),
			RequestID:  uuid.NewString(),
			SearchMode: detectSearchMode(query),
			AIConfig: service.AISearchRequestConfig{
				BaseURL: strings.TrimSpace(query.Get("url")),
				APIKey:  strings.TrimSpace(query.Get("key")),
				Model:   strings.TrimSpace(query.Get("model")),
			},
		}
	}
	if strings.Contains(useQuery, searchBillingProviderLocal) && resolveResult.AccessTokenID == 0 {
		writeSearchUnauthorized(c, fmt.Errorf("%w: 匿名请求不能使用本地题库，请提供有效访问令牌", service.ErrInvalidAccessTokenAuthorization), rawBody)
		return
	}
	if strings.Contains(useQuery, searchBillingProviderLocal) && !shouldEnableNormalSearchProvider(resolveResult, useQuery, searchBillingProviderLocal) {
		writeSearchUnauthorized(c, fmt.Errorf("%w: 本地题库未启用，请先在默认账号配置中启用后再调用", service.ErrInvalidAccessTokenAuthorization), rawBody)
		return
	}

	var result [][]string      // 最后所有的答案的二维数组
	var localAnswer [][]string // 本地答案
	resolved := resolveResult
	aiEnabled := shouldUseAISearch(useQuery, resolved.AIConfig)
	aiUsage := search.AISearchUsage{}
	usedLocalSearch := false
	usedSearchResultCache := false
	auditHitProvider := ""
	cacheEligible := canUseSearchResultCache(resolved, useQuery)
	cacheKey := ""
	if cacheEligible {
		cacheKey = buildSearchResultCacheKey(resolved.AccessTokenID, req)
		if !shouldBypassSearchResultCache(req) {
			if cached, ok := defaultSearchResultCache.get(cacheKey); ok {
				result = cached
				usedSearchResultCache = true
				auditHitProvider = searchResultCacheProvider
			}
		}
	}
	if !usedSearchResultCache {
		if err := ensureSearchQuotaAvailable(resolved, useQuery, aiEnabled); err != nil {
			writeSearchUnauthorized(c, fmt.Errorf("%w: 当前访问令牌累计额度不足，请调整令牌额度后重试", service.ErrInvalidAccessTokenAuthorization), rawBody)
			return
		}
	}
	logSearchRoutingDecision(c, query, resolved, aiEnabled)
	logger.SysLog(fmt.Sprintf("搜题请求摘要 requestId=%s %s", strings.TrimSpace(resolved.RequestID), buildSearchRequestDebugSummary(req)))
	if !usedSearchResultCache && strings.Contains(useQuery, "local") { // 使用本地题库的话
		localAnswer, err = defaultDBSearchProvider()(req)
		if err != nil {
			logger.SysError(fmt.Sprintf("查询本地答案出错：%s requestId=%s", err.Error(), strings.TrimSpace(resolved.RequestID)))
		}
		logger.SysLog(fmt.Sprintf("本地搜题结果 requestId=%s answers=%s error=%t", strings.TrimSpace(resolved.RequestID), buildSearchAnswerGroupsDebugSummary(localAnswer), err != nil))
		if len(localAnswer) > 0 {
			usedLocalSearch = true
			auditHitProvider = searchBillingProviderLocal
		}
		result = append(result, localAnswer...)
	}

	// 再查询第三方
	if !usedSearchResultCache && len(result) == 0 {
		if resolved.SearchMode == searchModeAI {
			aiResult, usage, aiErr := searchAIAnswer(resolved, req)
			if aiErr != nil {
				logger.SysError(fmt.Sprintf("AI独占搜题失败 requestId=%s error=%s", strings.TrimSpace(resolved.RequestID), aiErr.Error()))
			} else if len(aiResult) == 0 {
				logger.SysLog(fmt.Sprintf("AI独占搜题无结果 requestId=%s", strings.TrimSpace(resolved.RequestID)))
			} else {
				result = append(result, aiResult...)
				aiUsage = usage
				auditHitProvider = searchModeAI
			}
		} else {
			clientBindings := buildSearchClientBindings(resolved, useQuery)
			if aiEnabled {
				clientBindings = append(clientBindings, searchClientBinding{
					Provider: searchModeAI,
					Search: func(req model.SearchRequest) ([][]string, error) {
						answers, usage, err := searchAIAnswer(resolved, req)
						if err == nil && len(answers) > 0 {
							aiUsage = usage
						}
						return answers, err
					},
				})
			}

			var wg sync.WaitGroup
			var mu sync.Mutex

			for i := range clientBindings {
				binding := clientBindings[i]
				provider := strings.TrimSpace(binding.Provider)
				if provider != searchModeAI && !shouldEnableNormalSearchProvider(resolved, useQuery, provider) {
					continue
				}
				wg.Add(1)
				go func(binding searchClientBinding) {
					defer wg.Done()
					res, err := binding.Search(req)
					provider := strings.TrimSpace(binding.Provider)
					if err != nil {
						logger.SysError(fmt.Sprintf("渠道搜题失败 requestId=%s provider=%s error=%s", strings.TrimSpace(resolved.RequestID), provider, err.Error()))
						return
					}
					if len(res) == 0 {
						logger.SysLog(fmt.Sprintf("渠道搜题无结果 requestId=%s provider=%s", strings.TrimSpace(resolved.RequestID), provider))
						return
					}
					logger.SysLog(fmt.Sprintf("渠道搜题命中 requestId=%s provider=%s answers=%s", strings.TrimSpace(resolved.RequestID), provider, buildSearchAnswerGroupsDebugSummary(res)))
					mu.Lock()
					defer mu.Unlock()
					if provider == searchModeAI {
						if !hasAISearchUsage(aiUsage) {
							aiUsage = search.AISearchUsage{TotalTokens: 1}
						}
					}
					if auditHitProvider == "" {
						auditHitProvider = provider
					}
					result = append(result, res...)
				}(clientBindings[i])
			}
			wg.Wait()
		}
	}
	resp := util.FillAnswerResponse(result, &req)
	logger.SysLog(fmt.Sprintf("搜题响应摘要 requestId=%s hitProvider=%s usedLocal=%t usedCache=%t aiEnabled=%t %s", strings.TrimSpace(resolved.RequestID), strings.TrimSpace(auditHitProvider), usedLocalSearch, usedSearchResultCache, aiEnabled, buildSearchResponseDebugSummary(resp)))
	audit := buildSearchAuditMetadata(c, resolved, query, rawBody, useQuery, result, usedLocalSearch, aiEnabled, auditHitProvider, req, resp)
	if usedSearchResultCache {
		writeCompletedSearchAuditLog(resolved, useQuery, result, "", c.ClientIP(), audit)
	} else {
		reportCompletedSearchBilling(resolved, useQuery, aiEnabled, result, hasAISearchUsage(aiUsage), usedLocalSearch)
		if err := persistCompletedSearchBilling(resolved, useQuery, aiEnabled, req, result, aiUsage, usedLocalSearch, c.ClientIP(), audit); err != nil {
			writeSearchUnauthorized(c, fmt.Errorf("%w: 当前访问令牌累计额度不足，请调整令牌额度后重试", service.ErrInvalidAccessTokenAuthorization), rawBody)
			return
		}
		if cacheEligible {
			defaultSearchResultCache.put(cacheKey, result)
		}
	}

	if !usedSearchResultCache && len(localAnswer) == 0 && c.Query("noRecord") == "" { // 只有非缓存结果且本地题库没有答案时才记录
		middleware.CollectAnswer(resp, req.CourseName, req.Extra, audit.HitProvider)
	}
	c.JSON(http.StatusOK, resp)
}

func shouldUseAISearch(useQuery string, config service.AISearchRequestConfig) bool {
	return service.IsAISearchConfigUsable(config)
}

func logSearchRoutingDecision(c *gin.Context, query url.Values, resolved resolvedSearchRequestContext, aiEnabled bool) {
	useQuery := strings.TrimSpace(query.Get("use"))
	parts := []string{
		"搜题路由判定",
		fmt.Sprintf("requestId=%s", strings.TrimSpace(resolved.RequestID)),
		fmt.Sprintf("path=%s", c.Request.URL.Path),
		fmt.Sprintf("useQuery=%s", useQuery),
		fmt.Sprintf("resolvedMode=%s", strings.TrimSpace(resolved.SearchMode)),
		fmt.Sprintf("aiConfigUsable=%t", service.IsAISearchConfigUsable(resolved.AIConfig)),
		fmt.Sprintf("aiEnabled=%t", aiEnabled),
		fmt.Sprintf("hasAuthorization=%t", strings.TrimSpace(c.GetHeader("Authorization")) != ""),
		fmt.Sprintf("accessTokenID=%d", resolved.AccessTokenID),
	}
	if resolved.SearchMode == searchModeAI && !aiEnabled {
		parts = append(parts, fmt.Sprintf("aiDisabledReason=%s", explainAISearchDisabledReason(resolved.AIConfig)))
	}
	if resolved.SearchMode != searchModeAI {
		parts = append(parts, fmt.Sprintf("normalModeReason=%s", explainNormalSearchReason(query, aiEnabled)))
	}
	logger.SysLog(strings.Join(parts, " "))
}

func explainAISearchDisabledReason(config service.AISearchRequestConfig) string {
	if !service.IsAISearchConfigUsable(config) {
		missing := make([]string, 0, 3)
		if strings.TrimSpace(config.BaseURL) == "" {
			missing = append(missing, "url")
		}
		if strings.TrimSpace(config.APIKey) == "" {
			missing = append(missing, "key")
		}
		if strings.TrimSpace(config.Model) == "" {
			missing = append(missing, "model")
		}
		if len(missing) > 0 {
			return "missing_ai_config_" + strings.Join(missing, ",")
		}
		return "ai_config_unusable"
	}
	return "unknown"
}

func explainNormalSearchReason(query url.Values, aiEnabled bool) string {
	if strings.TrimSpace(query.Get("use")) != "" {
		if aiEnabled {
			return "explicit_provider_mix_with_ai"
		}
		return "explicit_provider_only"
	}
	if aiEnabled {
		return "default_provider_mix_with_ai"
	}
	return "missing_ai_config"
}

func resolveSearchQuestionTypeLabel(questionType int) string {
	switch questionType {
	case 0:
		return "单选题"
	case 1:
		return "多选题"
	case 2:
		return "填空题/连线题"
	case 3:
		return "判断题"
	case 4:
		return "简答题/阅读理解题"
	case -4:
		return "大作业"
	case 15:
		return "阅读理解"
	default:
		return fmt.Sprintf("其他题型(%d)", questionType)
	}
}

func buildSearchRequestDebugSummary(req model.SearchRequest) string {
	return strings.Join([]string{
		fmt.Sprintf("type=%d", req.Type),
		fmt.Sprintf("typeLabel=%s", resolveSearchQuestionTypeLabel(req.Type)),
		fmt.Sprintf("options=%d", len(req.Options)),
		fmt.Sprintf("hasCourseName=%t", strings.TrimSpace(req.CourseName) != ""),
		fmt.Sprintf("hasExtra=%t", strings.TrimSpace(req.Extra) != ""),
		fmt.Sprintf("question=%s", truncateForDebug(strings.TrimSpace(req.Question), 120)),
	}, " ")
}

func buildSearchAnswerGroupsDebugSummary(answers [][]string) string {
	flattened := make([]string, 0)
	for _, group := range answers {
		for _, item := range group {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			flattened = append(flattened, trimmed)
		}
	}
	preview := ""
	if len(flattened) > 0 {
		preview = truncateForDebug(strings.Join(flattened, " | "), 160)
	}
	return strings.Join([]string{
		fmt.Sprintf("groups=%d", len(answers)),
		fmt.Sprintf("items=%d", len(flattened)),
		fmt.Sprintf("preview=%s", preview),
	}, " ")
}

func buildAISearchUsageDebugSummary(usage search.AISearchUsage) string {
	return strings.Join([]string{
		fmt.Sprintf("inputTokens=%d", usage.InputTokens),
		fmt.Sprintf("outputTokens=%d", usage.OutputTokens),
		fmt.Sprintf("totalTokens=%d", usage.TotalTokens),
	}, " ")
}

func hasAISearchUsage(usage search.AISearchUsage) bool {
	return usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.TotalTokens > 0
}

func buildSearchResponseDebugSummary(resp model.SearchResponse) string {
	return strings.Join([]string{
		fmt.Sprintf("type=%d", resp.Type),
		fmt.Sprintf("typeLabel=%s", resolveSearchQuestionTypeLabel(resp.Type)),
		fmt.Sprintf("bestAnswerCount=%d", len(resp.Answer.BestAnswer)),
		fmt.Sprintf("allAnswerGroups=%d", len(resp.Answer.AllAnswer)),
		fmt.Sprintf("answerText=%s", truncateForDebug(strings.TrimSpace(resp.Answer.AnswerText), 160)),
	}, " ")
}

func readAndRestoreRequestBody(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return ""
	}

	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.Request.Body = io.NopCloser(bytes.NewReader(nil))
		return ""
	}

	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return string(raw)
}

func truncateForDebug(value string, limit int) string {
	if limit <= 0 {
		return ""
	}

	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "...(truncated)"
}

func sanitizeSearchUnauthorizedBody(rawBody string) string {
	return truncateForDebug(rawBody, maxSearchUnauthorizedBodyLength)
}

func maskSecret(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	runes := []rune(trimmed)
	if len(runes) <= 8 {
		return "****"
	}
	return string(runes[:4]) + "..." + string(runes[len(runes)-4:])
}

func maskAuthorizationHeader(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	parts := strings.SplitN(trimmed, " ", 2)
	if len(parts) == 1 {
		return maskSecret(parts[0])
	}
	return parts[0] + " " + maskSecret(parts[1])
}

func extractUnauthorizedDetail(err error) string {
	if err == nil {
		return "访问令牌无效"
	}

	msg := strings.TrimSpace(err.Error())
	prefix := strings.TrimSpace(service.ErrInvalidAccessTokenAuthorization.Error())
	if strings.HasPrefix(msg, prefix) {
		msg = strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(msg, prefix), ":： "))
	}
	if msg == "" {
		return "访问令牌无效"
	}
	return msg
}

func buildSearchUnauthorizedResponse(c *gin.Context, err error, rawBody string) searchUnauthorizedResponse {
	path := ""
	method := ""
	if c != nil && c.Request != nil {
		method = c.Request.Method
		if c.Request.URL != nil {
			path = c.Request.URL.Path
		}
	}

	return searchUnauthorizedResponse{
		ErrCode: http.StatusUnauthorized,
		Message: "访问令牌鉴权失败",
		Detail:  extractUnauthorizedDetail(err),
		Request: searchUnauthorizedRequestSnapshot{
			Method:        method,
			Path:          path,
			Origin:        c.GetHeader("Origin"),
			ContentType:   c.ContentType(),
			UserAgent:     c.GetHeader("User-Agent"),
			ClientIP:      c.ClientIP(),
			Authorization: maskAuthorizationHeader(c.GetHeader("Authorization")),
			Body:          sanitizeSearchUnauthorizedBody(rawBody),
		},
	}
}

func buildSearchAuditMetadata(c *gin.Context, resolved resolvedSearchRequestContext, query url.Values, rawBody string, useQuery string, result [][]string, usedLocalSearch bool, aiEnabled bool, auditHitProvider string, req model.SearchRequest, resp model.SearchResponse) searchAuditMetadata {
	responsePayload, err := json.Marshal(resp)
	if err != nil {
		responsePayload = []byte("{}")
	}

	hitProvider := strings.TrimSpace(auditHitProvider)
	if hitProvider == "" {
		hitProvider = resolveSearchAuditHitProvider(useQuery, resolved.UsedDefaultSearchProviders, usedLocalSearch, aiEnabled, len(result) > 0)
	}

	localQuestionID := int32(0)
	if usedLocalSearch {
		localQuestionID = resolveLocalQuestionID(resolveSearchPersistenceDB(), req, true)
	}

	return searchAuditMetadata{
		HitProvider:     hitProvider,
		RawRequest:      buildSearchAuditRawRequest(c, query, rawBody, resolved.AccessTokenID == 0),
		RawResponse:     string(responsePayload),
		Anonymous:       resolved.AccessTokenID == 0,
		LocalQuestionID: localQuestionID,
	}
}

func buildSearchAuditRawRequest(c *gin.Context, query url.Values, rawBody string, anonymous bool) string {
	if c == nil || c.Request == nil {
		return rawBody
	}

	authorization := maskAuthorizationHeader(c.GetHeader("Authorization"))
	if anonymous {
		authorization = strings.TrimSpace(c.GetHeader("Authorization"))
	}

	parts := []string{
		fmt.Sprintf("method=%s", strings.TrimSpace(c.Request.Method)),
		fmt.Sprintf("path=%s", c.Request.URL.Path),
		fmt.Sprintf("query=%s", sanitizeSearchQueryForLog(query)),
		fmt.Sprintf("contentType=%s", c.ContentType()),
		fmt.Sprintf("origin=%s", c.GetHeader("Origin")),
		fmt.Sprintf("userAgent=%s", c.GetHeader("User-Agent")),
		fmt.Sprintf("clientIp=%s", c.ClientIP()),
		fmt.Sprintf("authorization=%s", authorization),
		fmt.Sprintf("body=%s", rawBody),
	}
	return strings.Join(parts, " ")
}

func sanitizeSearchQueryForLog(query url.Values) string {
	if len(query) == 0 {
		return ""
	}
	sanitized := make(url.Values, len(query))
	for key, values := range query {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "key") ||
			strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "password") ||
			strings.Contains(lowerKey, "authorization") {
			sanitized[key] = []string{maskSecret(strings.Join(values, ","))}
			continue
		}
		sanitized[key] = append([]string(nil), values...)
	}
	return sanitized.Encode()
}

func resolveExplicitNormalSearchProvider(useQuery string) string {
	trimmedUse := strings.TrimSpace(useQuery)
	knownProviders := []string{"icodef", "wanneng", "tikuhai", "enncy", "aidian", "lemon", "buguake"}
	for _, provider := range knownProviders {
		if strings.Contains(trimmedUse, provider) {
			return provider
		}
	}
	return ""
}

func resolveSearchAuditHitProvider(useQuery string, defaultProviders []string, usedLocalSearch bool, aiEnabled bool, hasResult bool) string {
	if !hasResult {
		return ""
	}
	if usedLocalSearch {
		return searchBillingProviderLocal
	}
	if aiEnabled {
		return searchModeAI
	}

	if provider := resolveExplicitNormalSearchProvider(useQuery); provider != "" {
		return provider
	}
	if strings.TrimSpace(useQuery) == "" && len(defaultProviders) > 0 {
		return strings.TrimSpace(defaultProviders[0])
	}
	return ""
}

func writeSearchUnauthorized(c *gin.Context, err error, rawBody string) {
	resp := buildSearchUnauthorizedResponse(c, err, rawBody)
	logger.SysError(fmt.Sprintf(
		"搜索接口鉴权失败 detail=%s method=%s path=%s origin=%s contentType=%s clientIp=%s authorization=%s body=%s",
		resp.Detail,
		resp.Request.Method,
		resp.Request.Path,
		resp.Request.Origin,
		resp.Request.ContentType,
		resp.Request.ClientIP,
		resp.Request.Authorization,
		resp.Request.Body,
	))
	c.JSON(http.StatusUnauthorized, resp)
}

func didUseDefaultNormalSearchProvider(useQuery string, defaultProviders []string) bool {
	if len(defaultProviders) == 0 {
		return false
	}
	if strings.TrimSpace(useQuery) == "" {
		return true
	}
	for _, provider := range defaultProviders {
		if strings.Contains(useQuery, provider) {
			return true
		}
	}
	return false
}

func buildSearchBillingContext(resolved resolvedSearchRequestContext, useQuery string, aiEnabled bool, usedLocalSearch bool) searchBillingContext {
	ctx := searchBillingContext{
		UserID:        resolved.UserID,
		AccessTokenID: resolved.AccessTokenID,
		RequestID:     resolved.RequestID,
		SearchMode:    resolved.SearchMode,
	}
	usedDefaultAI := aiEnabled && resolved.UsedDefaultAI

	if resolved.AccessTokenID == 0 {
		return ctx
	}

	switch resolved.SearchMode {
	case searchModeAI:
		ctx.UsedDefaultAccount = usedDefaultAI
		ctx.Billable = ctx.UsedDefaultAccount
	case searchModeNormal:
		explicitProvider := resolveExplicitNormalSearchProvider(useQuery)
		hasExplicitProviderToken := explicitProvider != "" && strings.TrimSpace(resolved.Tokens[explicitProvider]) != ""
		usedDefaultNormal := didUseDefaultNormalSearchProvider(useQuery, resolved.UsedDefaultSearchProviders)
		ctx.UsedDefaultAccount = usedDefaultNormal || usedDefaultAI
		ctx.Billable = usedLocalSearch || usedDefaultNormal || usedDefaultAI || hasExplicitProviderToken
	}
	return ctx
}

func shouldBillCompletedSearch(ctx searchBillingContext, result [][]string, hasAIUsage bool) bool {
	if !ctx.Billable || len(result) == 0 {
		return false
	}

	switch ctx.SearchMode {
	case searchModeNormal:
		return true
	case searchModeAI:
		return hasAIUsage
	default:
		return false
	}
}

func finalizeSearchBillingContext(ctx searchBillingContext, result [][]string, hasAIUsage bool) searchBillingContext {
	ctx.Billable = shouldBillCompletedSearch(ctx, result, hasAIUsage)
	return ctx
}

func buildCompletedSearchBillingContext(resolved resolvedSearchRequestContext, useQuery string, aiEnabled bool, result [][]string, hasAIUsage bool, usedLocalSearch bool) searchBillingContext {
	return finalizeSearchBillingContext(buildSearchBillingContext(resolved, useQuery, aiEnabled, usedLocalSearch), result, hasAIUsage)
}

func reportCompletedSearchBilling(resolved resolvedSearchRequestContext, useQuery string, aiEnabled bool, result [][]string, hasAIUsage bool, usedLocalSearch bool) {
	searchBillingReporter(buildCompletedSearchBillingContext(resolved, useQuery, aiEnabled, result, hasAIUsage, usedLocalSearch))
}

func ensureSearchQuotaAvailable(resolved resolvedSearchRequestContext, useQuery string, aiEnabled bool) error {
	if resolved.AccessTokenCtx == nil || resolved.AccessTokenID == 0 {
		return nil
	}

	accessToken := resolved.AccessTokenCtx.AccessToken
	if !accessTokenQuotaExhausted(accessToken) {
		return nil
	}

	potentialLocalSearch := strings.Contains(useQuery, searchBillingProviderLocal)
	if !buildSearchBillingContext(resolved, useQuery, aiEnabled, potentialLocalSearch).Billable {
		return nil
	}
	return errAccessTokenQuotaExceeded
}

func accessTokenQuotaExhausted(accessToken entity.AccessToken) bool {
	if accessToken.TotalQuotaLimit <= 0 {
		return false
	}
	return roundCurrencyAmount(accessToken.TotalAmountUsed) >= roundCurrencyAmount(accessToken.TotalQuotaLimit)
}

func accessTokenQuotaWouldExceed(accessToken entity.AccessToken, amount float64) bool {
	if accessToken.TotalQuotaLimit <= 0 || amount <= 0 {
		return false
	}
	return roundCurrencyAmount(accessToken.TotalAmountUsed+amount) > roundCurrencyAmount(accessToken.TotalQuotaLimit)
}

func persistCompletedSearchBilling(resolved resolvedSearchRequestContext, useQuery string, aiEnabled bool, req model.SearchRequest, result [][]string, aiUsage search.AISearchUsage, usedLocalSearch bool, clientIP string, audit searchAuditMetadata) error {
	ctx := buildCompletedSearchBillingContext(resolved, useQuery, aiEnabled, result, hasAISearchUsage(aiUsage), usedLocalSearch)
	if !ctx.Billable {
		writeCompletedSearchAuditLog(resolved, useQuery, result, "", clientIP, audit)
		return nil
	}

	db := resolveSearchPersistenceDB()
	if db == nil {
		logger.SysError(fmt.Sprintf("搜索计费落账失败：数据库不可用 requestId=%s", resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_db_unavailable", clientIP, audit)
		return nil
	}

	charges, err := resolveCompletedSearchBillingCharges(db, resolved, useQuery, req, aiUsage, usedLocalSearch)
	if err != nil {
		logger.SysError(fmt.Sprintf("搜索计费落账失败：%s requestId=%s", err.Error(), resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_charge_resolve_failed", clientIP, audit)
		return nil
	}
	totalAmount := completedSearchBillingAmount(charges)
	if totalAmount <= 0 {
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_zero_amount", clientIP, audit)
		return nil
	}

	if err := persistSearchBillingRecordsWithDB(db, ctx, charges); err != nil {
		if errors.Is(err, errAccessTokenQuotaExceeded) {
			logger.SysLog(fmt.Sprintf("搜索计费拒绝：访问令牌累计额度不足 requestId=%s", resolved.RequestID))
			writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_quota_exceeded", clientIP, audit)
			return err
		}
		logger.SysError(fmt.Sprintf("搜索计费落账失败：%s requestId=%s", err.Error(), resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_persist_failed", clientIP, audit)
		return nil
	}

	writeCompletedSearchAuditLog(resolved, useQuery, result, completedSearchBillingProviderLabel(charges), clientIP, audit)
	return nil
}

func resolveSearchPersistenceDB() *gorm.DB {
	if dao.Log != nil {
		if db := dao.Log.UnderlyingDB(); db != nil {
			return db.Session(&gorm.Session{NewDB: true})
		}
	}
	currentManager := manager.GetManager()
	if currentManager == nil {
		return nil
	}
	fallback := currentManager.GetDB()
	if fallback == nil {
		return nil
	}
	return fallback.Session(&gorm.Session{NewDB: true})
}

func resolveCompletedSearchBillingCharges(db *gorm.DB, resolved resolvedSearchRequestContext, useQuery string, req model.SearchRequest, aiUsage search.AISearchUsage, usedLocalSearch bool) ([]searchBillingCharge, error) {
	charges := make([]searchBillingCharge, 0, 2)

	if resolved.SearchMode == searchModeNormal {
		provider := resolveCompletedSearchBillingProvider(useQuery, resolved.UsedDefaultSearchProviders, usedLocalSearch)
		if provider != "" {
			var config entity.SearchAccountConfig
			if err := db.Where("provider = ?", provider).First(&config).Error; err != nil {
				return nil, err
			}

			amount := roundCurrencyAmount(config.FlatPrice)
			if amount > 0 {
				charges = append(charges, searchBillingCharge{
					Provider: provider,
					Amount:   amount,
					Snapshot: buildCompletedSearchBillingSnapshot(db, provider, amount, req, usedLocalSearch),
				})
			}
		}
	}

	// Default search can execute AI while SearchMode remains "normal". AI usage is
	// billed as a separate record so it never replaces the normal provider charge.
	if resolved.UsedDefaultAI && hasAISearchUsage(aiUsage) {
		charge, err := resolveCompletedAIBillingCharge(db, resolved, req, aiUsage)
		if err != nil {
			return nil, err
		}
		if charge.Amount > 0 {
			charges = append(charges, charge)
		}
	}

	return charges, nil
}

func completedSearchBillingAmount(charges []searchBillingCharge) float64 {
	total := float64(0)
	for _, charge := range charges {
		total += charge.Amount
	}
	return roundCurrencyAmount(total)
}

func completedSearchBillingProviderLabel(charges []searchBillingCharge) string {
	providers := make([]string, 0, len(charges))
	for _, charge := range charges {
		if provider := strings.TrimSpace(charge.Provider); provider != "" {
			providers = append(providers, provider)
		}
	}
	return strings.Join(providers, ",")
}

func resolveCompletedAIBillingCharge(db *gorm.DB, resolved resolvedSearchRequestContext, req model.SearchRequest, usage search.AISearchUsage) (searchBillingCharge, error) {
	price, err := service.ResolveAIModelPriceWithDB(db, resolved.AIConfig.Model)
	if err != nil {
		return searchBillingCharge{}, err
	}
	inputUnits := float64(usage.InputTokens) / 1000
	outputUnits := float64(usage.OutputTokens) / 1000
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens > 0 {
		inputUnits = float64(usage.TotalTokens) / 1000
	}
	baseAmount := inputUnits + outputUnits
	multiplier := price.BillingMultiplier
	if multiplier <= 0 {
		multiplier = 1
	}
	amount := roundCurrencyAmount(baseAmount * multiplier)
	totalTokens := usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = usage.InputTokens + usage.OutputTokens
	}
	return searchBillingCharge{
		Provider:     searchModeAI,
		Amount:       amount,
		Snapshot:     buildCompletedAIBillingSnapshot(resolved, req, usage, price, baseAmount, amount),
		ModelName:    strings.TrimSpace(resolved.AIConfig.Model),
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  totalTokens,
	}, nil
}

func buildCompletedAIBillingSnapshot(resolved resolvedSearchRequestContext, req model.SearchRequest, usage search.AISearchUsage, price service.ResolvedAIModelPrice, baseAmount float64, amount float64) string {
	parts := []string{
		"provider=ai",
		fmt.Sprintf("model=%s", strings.TrimSpace(resolved.AIConfig.Model)),
		fmt.Sprintf("billingMultiplier=%.4f", price.BillingMultiplier),
		fmt.Sprintf("exchangeRate=%.4f", price.ExchangeRate),
		fmt.Sprintf("globalBillingMultiplier=%.4f", price.GlobalBillingMultiplier),
		fmt.Sprintf("modelBillingMultiplier=%.4f", price.ModelBillingMultiplier),
		fmt.Sprintf("inputTokens=%d", usage.InputTokens),
		fmt.Sprintf("outputTokens=%d", usage.OutputTokens),
		fmt.Sprintf("totalTokens=%d", usage.TotalTokens),
		fmt.Sprintf("baseAmount=%.2f", roundCurrencyAmount(baseAmount)),
		fmt.Sprintf("amount=%.2f", amount),
	}
	if questionText := strings.TrimSpace(req.Question); questionText != "" {
		parts = append(parts, fmt.Sprintf("question=%s", url.QueryEscape(questionText)))
	}
	return strings.Join(parts, " ")
}

func buildCompletedSearchBillingSnapshot(db *gorm.DB, provider string, amount float64, req model.SearchRequest, usedLocalSearch bool) string {
	parts := []string{fmt.Sprintf("provider=%s", provider), fmt.Sprintf("flatPrice=%.2f", amount)}
	if questionText := strings.TrimSpace(req.Question); questionText != "" {
		parts = append(parts, fmt.Sprintf("question=%s", url.QueryEscape(questionText)))
	}
	if provider == searchBillingProviderLocal && usedLocalSearch {
		if sourceName := resolveLocalSearchSourceName(db, req); sourceName != "" {
			parts = append(parts, fmt.Sprintf("sourceName=%s", sourceName))
		}
	}
	return strings.Join(parts, " ")
}

type localSearchMatchRow struct {
	ID         int32
	SourceName string
}

func resolveLocalSearchMatchRows(db *gorm.DB, req model.SearchRequest) []localSearchMatchRow {
	if db == nil {
		return nil
	}

	sortOptions := make([]string, len(req.Options))
	copy(sortOptions, req.Options)
	sort.Strings(sortOptions)
	optionPayload, err := json.Marshal(sortOptions)
	if err != nil {
		optionPayload = []byte("[]")
	}

	newHash := strutil.Md5(util.FormatString(req.Question) + string(optionPayload) + fmt.Sprintf("%d%d", req.Type, req.Plat))
	oldHash := strutil.Md5(req.Question + string(optionPayload) + fmt.Sprintf("%d%d", req.Type, req.Plat))

	rows := make([]localSearchMatchRow, 0)
	query := db.Table(entity.TableNameTiku).Select("id, source_name").Where("hash IN ?", []string{newHash, oldHash})
	if strings.TrimSpace(req.CourseName) != "" {
		query = query.Where("course_name = ?", strings.TrimSpace(req.CourseName))
	}
	if err := query.Order("id desc").Find(&rows).Error; err != nil {
		return nil
	}
	return rows
}

func resolveLocalQuestionID(db *gorm.DB, req model.SearchRequest, usedLocalSearch bool) int32 {
	if !usedLocalSearch {
		return 0
	}

	rows := resolveLocalSearchMatchRows(db, req)
	if len(rows) == 0 {
		return 0
	}
	return rows[0].ID
}

func resolveLocalSearchSourceName(db *gorm.DB, req model.SearchRequest) string {
	rows := resolveLocalSearchMatchRows(db, req)
	for _, row := range rows {
		if strings.TrimSpace(row.SourceName) != "" {
			return strings.TrimSpace(row.SourceName)
		}
	}
	return ""
}

func resolveCompletedSearchBillingProvider(useQuery string, defaultProviders []string, usedLocalSearch bool) string {
	if usedLocalSearch {
		return searchBillingProviderLocal
	}
	if provider := resolveExplicitNormalSearchProvider(useQuery); provider != "" {
		return provider
	}
	if len(defaultProviders) == 0 {
		return ""
	}
	if strings.TrimSpace(useQuery) == "" {
		return strings.TrimSpace(defaultProviders[0])
	}
	for _, provider := range defaultProviders {
		trimmedProvider := strings.TrimSpace(provider)
		if trimmedProvider == "" {
			continue
		}
		if strings.Contains(useQuery, trimmedProvider) {
			return trimmedProvider
		}
	}
	return ""
}

func roundCurrencyAmount(amount float64) float64 {
	return math.Round(amount*100) / 100
}

func persistSearchBillingRecordsWithDB(db *gorm.DB, ctx searchBillingContext, charges []searchBillingCharge) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var accessToken entity.AccessToken
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ctx.AccessTokenID).First(&accessToken).Error; err != nil {
			return err
		}

		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ctx.UserID).First(&user).Error; err != nil {
			return err
		}

		totalCharged := completedSearchBillingAmount(charges)
		if totalCharged <= 0 {
			return nil
		}
		if accessTokenQuotaWouldExceed(accessToken, totalCharged) {
			return errAccessTokenQuotaExceeded
		}

		balanceAfter := roundCurrencyAmount(user.Balance)
		for _, charge := range charges {
			if charge.Amount <= 0 {
				continue
			}
			balanceBefore := balanceAfter
			balanceAfter = roundCurrencyAmount(balanceBefore - charge.Amount)
			billingRecord := entity.BillingRecord{
				UserID:             ctx.UserID,
				AccessTokenID:      ctx.AccessTokenID,
				Type:               searchBillingRecordTypeNormal,
				UsedDefaultAccount: ctx.UsedDefaultAccount,
				Provider:           charge.Provider,
				ModelName:          charge.ModelName,
				InputTokens:        charge.InputTokens,
				OutputTokens:       charge.OutputTokens,
				TotalTokens:        charge.TotalTokens,
				Amount:             charge.Amount,
				BalanceBefore:      balanceBefore,
				BalanceAfter:       balanceAfter,
				RequestID:          ctx.RequestID,
				UnitPriceSnapshot:  charge.Snapshot,
				Remark:             "搜索答题消耗",
			}
			if err := tx.Create(&billingRecord).Error; err != nil {
				return err
			}
		}

		totalAmountUsedAfter := roundCurrencyAmount(accessToken.TotalAmountUsed + totalCharged)
		if err := tx.Model(&accessToken).Where("id = ?", accessToken.ID).Update("total_amount_used", totalAmountUsedAfter).Error; err != nil {
			return err
		}

		return tx.Model(&user).Where("id = ?", user.ID).Update("balance", balanceAfter).Error
	})
}

func writeCompletedSearchAuditLog(resolved resolvedSearchRequestContext, useQuery string, result [][]string, billingProvider string, clientIP string, audit searchAuditMetadata) {
	var user *entity.User
	if resolved.AccessTokenCtx != nil {
		user = &resolved.AccessTokenCtx.User
	}

	descriptionParts := []string{
		"搜索完成",
		fmt.Sprintf("requestId=%s", resolved.RequestID),
		fmt.Sprintf("mode=%s", strings.TrimSpace(resolved.SearchMode)),
		fmt.Sprintf("use=%s", strings.TrimSpace(useQuery)),
		fmt.Sprintf("answers=%d", len(result)),
	}
	if audit.LocalQuestionID > 0 {
		descriptionParts = append(descriptionParts, fmt.Sprintf("localQuestionId=%d", audit.LocalQuestionID))
	}
	if audit.HitProvider != "" {
		descriptionParts = append(descriptionParts, fmt.Sprintf("hitProvider=%s", audit.HitProvider))
	}
	if billingProvider != "" {
		descriptionParts = append(descriptionParts, fmt.Sprintf("billingProvider=%s", billingProvider))
	}
	if audit.Anonymous {
		descriptionParts = append(descriptionParts, "anonymous=true")
	}
	writeAuditLog(
		user,
		searchAuditModule,
		logActionSearch,
		strings.Join(descriptionParts, " "),
		strings.TrimSpace(clientIP),
		audit.LocalQuestionID,
		audit.RawRequest,
		audit.RawResponse,
	)
}

func hasExplicitAIMode(query url.Values) bool {
	if strings.Contains(strings.TrimSpace(query.Get("use")), "ai") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(query.Get("mode")), searchModeAI)
}

func detectSearchMode(query url.Values) string {
	if hasExplicitAIMode(query) {
		return searchModeAI
	}
	return searchModeNormal
}

func hasExplicitSearchProviderOverride(query url.Values) bool {
	for provider, token := range service.BuildRequestSearchTokens(query) {
		if strings.TrimSpace(provider) == searchBillingProviderLocal || strings.TrimSpace(provider) == searchModeAI {
			continue
		}
		if strings.TrimSpace(token) != "" {
			return true
		}
	}
	if strings.TrimSpace(query.Get("url")) != "" {
		return true
	}
	if strings.TrimSpace(query.Get("key")) != "" {
		return true
	}
	if strings.TrimSpace(query.Get("model")) != "" {
		return true
	}
	return false
}

func resolveSearchRequestContextWithDB(db *gorm.DB, authorization string, clientIP string, query url.Values) (resolvedSearchRequestContext, error) {
	resolved := resolvedSearchRequestContext{
		RequestID:  uuid.NewString(),
		SearchMode: detectSearchMode(query),
	}
	allowDefaultAccount := false

	if strings.TrimSpace(authorization) != "" {
		authCtx, err := service.ResolveAccessTokenFromBasicAuth(db, authorization)
		if err != nil {
			return resolved, err
		}

		now := time.Now()
		trimmedClientIP := strings.TrimSpace(clientIP)
		if err := db.Model(&authCtx.AccessToken).Where("id = ?", authCtx.AccessToken.ID).Updates(map[string]any{
			"last_used_at": now,
			"last_used_ip": trimmedClientIP,
		}).Error; err != nil {
			return resolved, err
		}
		authCtx.AccessToken.LastUsedAt = &now
		authCtx.AccessToken.LastUsedIP = trimmedClientIP

		resolved.AccessTokenCtx = &authCtx
		resolved.UserID = authCtx.User.ID
		resolved.AccessTokenID = authCtx.AccessToken.ID
		if authCtx.User.Balance <= 0 {
			if authCtx.User.Balance >= 0 || !hasExplicitSearchProviderOverride(query) {
				return resolved, fmt.Errorf("%w: 当前余额不足，系统题库已禁用，请充值后再使用", service.ErrInvalidAccessTokenAuthorization)
			}
		} else {
			allowDefaultAccount = true
		}
	}

	resolvedTokens, err := service.BuildSearchTokensWithDB(db, query, allowDefaultAccount)
	if err != nil {
		return resolved, err
	}
	resolved.Tokens = resolvedTokens.Tokens
	resolved.EnabledSearchProviders = resolvedTokens.EnabledProviders
	resolved.UsedDefaultSearch = resolvedTokens.UsedDefaultAccount
	resolved.UsedDefaultSearchProviders = resolvedTokens.UsedDefaultProviders

	resolvedAI, err := service.BuildAISearchRequestConfigWithDB(db, query, allowDefaultAccount, strings.TrimSpace(authorization) != "")
	if err != nil {
		return resolved, err
	}
	resolved.AIConfig = resolvedAI.Config
	resolved.UsedDefaultAI = resolvedAI.UsedDefaultAccount

	return resolved, nil
}
