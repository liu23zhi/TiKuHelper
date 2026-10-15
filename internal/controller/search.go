package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	Status          string
	Failures        []searchProviderFailure
	AISearchTrace   []search.AISearchTrace
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
	// ClientIP 随账单一起落库，供账单明细展示来源 IP 归属地。
	// logs 表没有 request_id，事后无法关联，只能在这里带上。
	ClientIP string
}

type searchBillingCharge struct {
	Provider          string
	Amount            float64
	Snapshot          string
	ModelName         string
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	TotalTokens       int64
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
		Enable:                shouldUseAISearch("ai", resolved.AIConfig),
		BaseURL:               resolved.AIConfig.BaseURL,
		APIKey:                resolved.AIConfig.APIKey,
		Model:                 resolved.AIConfig.Model,
		Prompt:                resolved.AIConfig.Prompt,
		Temperature:           resolved.AIConfig.Temperature,
		TopP:                  resolved.AIConfig.TopP,
		MaxAISearchModelCalls: resolved.AIConfig.MaxAISearchModelCalls,
		MaxAISearches:         resolved.AIConfig.MaxAISearches,
		MaxAISearchRetries:    resolved.AIConfig.MaxAISearchRetries,
		SearchTool:            service.BuildUAPISearchToolWithDB(manager.GetManager().GetDB()),
		RequestID:             resolved.RequestID,
	}
	logger.SysLog(fmt.Sprintf("AI搜题开始 requestId=%s %s", strings.TrimSpace(resolved.RequestID), buildSearchRequestDebugSummary(req)))
	answer, usage, err := client.SearchAnswerWithUsage(req)
	if err != nil {
		logger.SysError(fmt.Sprintf("AI搜题失败 requestId=%s usage=%s answers=%s errorCode=%s", strings.TrimSpace(resolved.RequestID), buildAISearchUsageBasicSummary(usage), buildSearchAnswerGroupsDebugSummary(answer), searchFailureCode(err)))
		logger.AIThinkingWithSecrets(fmt.Sprintf("AI搜题最终结果 requestId=%s status=failed deepThinking=%t usage=%s answers=%s error=%s", strings.TrimSpace(resolved.RequestID), req.DeepThinking, buildAISearchUsageDebugSummary(usage), buildSearchAnswerGroupsDebugSummary(answer), err.Error()), resolved.AIConfig.APIKey)
		return answer, usage, err
	}
	logger.SysLog(fmt.Sprintf("AI搜题完成 requestId=%s usage=%s answers=%s", strings.TrimSpace(resolved.RequestID), buildAISearchUsageBasicSummary(usage), buildSearchAnswerGroupsDebugSummary(answer)))
	logger.AIThinkingWithSecrets(fmt.Sprintf("AI搜题最终结果 requestId=%s status=success deepThinking=%t usage=%s answers=%s", strings.TrimSpace(resolved.RequestID), req.DeepThinking, buildAISearchUsageDebugSummary(usage), buildSearchAnswerGroupsDebugSummary(answer)), resolved.AIConfig.APIKey)
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
	resolved := resolveResult
	req.RequestID = strings.TrimSpace(resolved.RequestID)

	var result [][]string      // 最后所有的答案的二维数组
	var localAnswer [][]string // 本地答案
	failureCollector := &searchFailureCollector{}
	aiEnabled := shouldUseAISearch(useQuery, resolved.AIConfig)
	if aiEnabled {
		clearAIResponseWriteDeadline(c)
	}
	aiUsage := search.AISearchUsage{}
	usedLocalSearch := false
	usedSearchResultCache := false
	auditHitProvider := ""
	cacheEligible := canUseSearchResultCache(resolved, useQuery)
	cacheKey := ""
	// 记录本次结果所属的缓存代次：若期间缓存被失效，写入时会被丢弃。
	var cacheEpoch uint64
	if cacheEligible {
		key, epoch, ok := buildSearchResultCacheKey(resolved, req, useQuery)
		if ok {
			cacheKey, cacheEpoch = key, epoch
			// 只有显式 skipCache:true 才跳过读取；跳过读仍会把新结果写回（刷新语义）。
			if !shouldBypassSearchResultCache(req) {
				if cached, found := searchResultCache.get(cacheKey, cacheEpoch); found {
					result = cloneSearchAnswers(cached.answers)
					usedSearchResultCache = true
					auditHitProvider = searchResultCacheProvider
				}
			}
		} else {
			// 键序列化失败时放弃缓存，但不影响正常搜题。
			cacheEligible = false
		}
	}

	// 并发合并：缓存写入发生在上游返回之后，所以「完全相同的并发请求」
	// 会同时读到空缓存、各自打一次上游、各自计一次费。
	// 这里让同 key 的并发请求只执行一次：搭车者复用结果且不计费。
	//
	// 只在缓存未命中且未显式跳过缓存时合并，条件与缓存复用完全一致
	// （key 含用户、题目、选项、代次，任一不同都不会搭车）。
	var flightCall *flightCall[searchFlightPayload]
	flightKey := ""
	if cacheEligible && !usedSearchResultCache && !shouldBypassSearchResultCache(req) {
		flightKey = cacheKey
		isLeader, borrowed, leader := beginSearchFlight(flightKey)
		if isLeader {
			flightCall = leader
			// 兜底释放：无论从哪条路径提前 return，都必须唤醒搭车者，
			// 否则它们会一直等到超时才自行重试。
			// finish 内部用 once 保证幂等，重复调用安全。
			defer func() {
				finishSearchFlight(flightKey, flightCall, nil)
			}()
		} else if borrowed != nil {
			// 复用并发请求的结果：与命中缓存等价，不做上游调用、不计费。
			result = borrowed
			usedSearchResultCache = true
			auditHitProvider = searchResultCacheProvider
			logger.SysLog(fmt.Sprintf("搜题并发合并命中 requestId=%s 复用进行中的相同请求结果", strings.TrimSpace(resolved.RequestID)))
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
		failureCollector.beginAttempt()
		localAnswer, err = defaultDBSearchProvider()(req)
		if err != nil {
			failureCollector.record(resolved, searchBillingProviderLocal, err)
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
			failureCollector.beginAttempt()
			aiResult, usage, aiErr := searchAIAnswer(resolved, req)
			aiUsage = usage
			if aiErr != nil {
				failureCollector.record(resolved, searchModeAI, aiErr)
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
						if hasAISearchUsage(usage) {
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
				failureCollector.beginAttempt()
				go func(binding searchClientBinding) {
					defer wg.Done()
					res, err := binding.Search(req)
					provider := strings.TrimSpace(binding.Provider)
					if err != nil {
						failureCollector.record(resolved, provider, err)
						return
					}
					if len(res) == 0 {
						logger.SysLog(fmt.Sprintf("渠道搜题无结果 requestId=%s provider=%s", strings.TrimSpace(resolved.RequestID), provider))
						return
					}
					logger.SysLog(fmt.Sprintf("渠道搜题命中 requestId=%s provider=%s answers=%s", strings.TrimSpace(resolved.RequestID), provider, buildSearchAnswerGroupsDebugSummary(res)))
					mu.Lock()
					defer mu.Unlock()
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
	audit.AISearchTrace = append([]search.AISearchTrace(nil), aiUsage.Trace...)
	status, failures := failureCollector.snapshot(len(result) > 0, resolved.SearchMode == searchModeAI && !aiEnabled)
	audit = addSearchFailuresToAudit(audit, status, failures, resp)
	if len(failures) > 0 {
		logger.SysError(fmt.Sprintf("%s requestId=%s userId=%d type=%d question=%s %s", searchAuditSummary(status), resolved.RequestID, resolved.UserID, req.Type, truncateForDebug(req.Question, 120), searchFailureSummary(failures)))
	}
	if usedSearchResultCache {
		writeCompletedSearchAuditLog(resolved, useQuery, result, "", c.ClientIP(), audit)
	} else {
		reportCompletedSearchBilling(resolved, useQuery, aiEnabled, result, hasAISearchUsage(aiUsage), usedLocalSearch)
		billingUsage := aiUsage
		if len(result) == 0 {
			billingUsage.InputTokens = 0
			billingUsage.CachedInputTokens = 0
			billingUsage.CacheWriteTokens = 0
			billingUsage.OutputTokens = 0
			billingUsage.TotalTokens = 0
		}
		billingErr, warning := persistCompletedSearchBilling(resolved, useQuery, aiEnabled, req, result, billingUsage, usedLocalSearch, c.ClientIP(), audit)
		if billingErr != nil {
			writeSearchUnauthorized(c, fmt.Errorf("%w: 当前访问令牌累计额度不足，请调整令牌额度后重试", service.ErrInvalidAccessTokenAuthorization), rawBody)
			return
		}
		// AI 用到了未配置价格的模型时给出非阻断提示，答案照常返回。
		if strings.TrimSpace(warning) != "" {
			resp.Warnings = append(resp.Warnings, warning)
			logger.SysLog(fmt.Sprintf("搜题计费提示 requestId=%s warning=%s", strings.TrimSpace(resolved.RequestID), warning))
		}
		if cacheEligible {
			searchResultCache.put(cacheKey, searchAnswerPayload{answers: cloneSearchAnswers(result)}, cacheEpoch, searchAnswerPayloadUsable)
		}
	}

	if !usedSearchResultCache && len(localAnswer) == 0 && c.Query("noRecord") == "" { // 只有非缓存结果且本地题库没有答案时才记录
		middleware.CollectAnswer(resp, req.CourseName, req.Extra, audit.HitProvider)
	}

	// 广播给搭车者，并释放 key。
	// 必须在写响应之前完成，避免搭车者等到响应写完后才被唤醒而无谓地多等一个 RTT。
	if flightCall != nil {
		finishSearchFlight(flightKey, flightCall, result)
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
	parts := []string{
		fmt.Sprintf("inputTokens=%d", usage.InputTokens),
		fmt.Sprintf("cachedInputTokens=%d", usage.CachedInputTokens),
		fmt.Sprintf("outputTokens=%d", usage.OutputTokens),
		fmt.Sprintf("totalTokens=%d", usage.TotalTokens),
		fmt.Sprintf("searchCallCount=%d", len(usage.SearchCalls)),
	}
	for index, call := range usage.SearchCalls {
		if index >= 10 {
			parts = append(parts, fmt.Sprintf("searchCallsOmitted=%d", len(usage.SearchCalls)-index))
			break
		}
		parts = append(parts, fmt.Sprintf("searchCall%d=%s|%s|%s|results:%d/%d|durationMs:%d", index+1, logger.RedactSecrets(call.API), truncateForDebug(call.Query, 120), call.Status, call.ResultCount, call.TotalResults, call.DurationMs))
	}
	return strings.Join(parts, " ")
}

func buildAISearchUsageBasicSummary(usage search.AISearchUsage) string {
	return fmt.Sprintf("inputTokens=%d outputTokens=%d totalTokens=%d modelTraceCount=%d searchCallCount=%d", usage.InputTokens, usage.OutputTokens, usage.TotalTokens, len(usage.Trace), len(usage.SearchCalls))
}

func hasAISearchUsage(usage search.AISearchUsage) bool {
	return hasAIModelUsage(usage) || len(usage.SearchCalls) > 0
}

func hasAIModelUsage(usage search.AISearchUsage) bool {
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
	auditUser := resolveSearchUnauthorizedAuditUser(c.GetHeader("Authorization"))
	requestSnapshot, _ := json.Marshal(resp.Request)
	responseSnapshot, _ := json.Marshal(resp)
	writeAuditLog(
		auditUser,
		searchAuditModule,
		logActionSearch,
		truncateForDebug(fmt.Sprintf(
			"搜索失败 detail=%s method=%s path=%s clientIp=%s authorization=%s",
			resp.Detail,
			resp.Request.Method,
			resp.Request.Path,
			resp.Request.ClientIP,
			resp.Request.Authorization,
		), 240),
		resp.Request.ClientIP,
		0,
		string(requestSnapshot),
		string(responseSnapshot),
	)
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

func resolveSearchUnauthorizedAuditUser(authorization string) *entity.User {
	if strings.TrimSpace(authorization) == "" {
		return nil
	}
	db := resolveSearchPersistenceDB()
	if db == nil {
		return nil
	}
	authCtx, err := service.ResolveAccessTokenFromBasicAuth(db, authorization)
	if err != nil {
		return nil
	}
	return &authCtx.User
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
	if !ctx.Billable || (len(result) == 0 && !hasAIUsage) {
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
	return util.RoundMoney(accessToken.TotalAmountUsed) >= util.RoundMoney(accessToken.TotalQuotaLimit)
}

func accessTokenQuotaWouldExceed(accessToken entity.AccessToken, amount float64) bool {
	if accessToken.TotalQuotaLimit <= 0 || amount <= 0 {
		return false
	}
	return util.RoundMoney(accessToken.TotalAmountUsed+amount) > util.RoundMoney(accessToken.TotalQuotaLimit)
}

func persistCompletedSearchBilling(resolved resolvedSearchRequestContext, useQuery string, aiEnabled bool, req model.SearchRequest, result [][]string, aiUsage search.AISearchUsage, usedLocalSearch bool, clientIP string, audit searchAuditMetadata) (error, string) {
	ctx := buildCompletedSearchBillingContext(resolved, useQuery, aiEnabled, result, hasAISearchUsage(aiUsage), usedLocalSearch)
	ctx.ClientIP = strings.TrimSpace(clientIP)
	if !ctx.Billable {
		writeCompletedSearchAuditLog(resolved, useQuery, result, "", clientIP, audit)
		return nil, ""
	}

	db := resolveSearchPersistenceDB()
	if db == nil {
		logger.SysError(fmt.Sprintf("搜索计费落账失败：数据库不可用 requestId=%s", resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_db_unavailable", clientIP, audit)
		return nil, ""
	}

	charges, warning, err := resolveCompletedSearchBillingCharges(db, resolved, useQuery, req, aiUsage, usedLocalSearch, len(result) > 0)
	if err != nil {
		logger.SysError(fmt.Sprintf("搜索计费落账失败：%s requestId=%s", err.Error(), resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_charge_resolve_failed", clientIP, audit)
		return nil, warning
	}
	if len(charges) == 0 {
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_zero_amount", clientIP, audit)
		return nil, warning
	}

	if err := persistSearchBillingRecordsWithDB(db, ctx, charges); err != nil {
		if errors.Is(err, errAccessTokenQuotaExceeded) {
			logger.SysLog(fmt.Sprintf("搜索计费拒绝：访问令牌累计额度不足 requestId=%s", resolved.RequestID))
			writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_quota_exceeded", clientIP, audit)
			return err, warning
		}
		logger.SysError(fmt.Sprintf("搜索计费落账失败：%s requestId=%s", err.Error(), resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_persist_failed", clientIP, audit)
		return nil, warning
	}

	writeCompletedSearchAuditLog(resolved, useQuery, result, completedSearchBillingProviderLabel(charges), clientIP, audit)
	return nil, warning
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

// resolveCompletedSearchBillingCharges 汇总本次搜索的所有计费项。
// 第二个返回值是面向调用方的非阻断提示（目前只有「AI 模型未配置价格」），
// 不影响答案返回，只提醒管理员去价格表补价。
func resolveCompletedSearchBillingCharges(db *gorm.DB, resolved resolvedSearchRequestContext, useQuery string, req model.SearchRequest, aiUsage search.AISearchUsage, usedLocalSearch bool, hasSearchAnswer bool) ([]searchBillingCharge, string, error) {
	charges := make([]searchBillingCharge, 0, 2+len(aiUsage.SearchCalls))
	warning := ""

	if resolved.SearchMode == searchModeNormal && hasSearchAnswer {
		provider := resolveCompletedSearchBillingProvider(useQuery, resolved.UsedDefaultSearchProviders, usedLocalSearch)
		if provider != "" {
			var config entity.SearchAccountConfig
			if err := db.Where("provider = ?", provider).First(&config).Error; err != nil {
				return nil, warning, err
			}

			amount := util.RoundMoney(config.FlatPrice)
			charges = append(charges, searchBillingCharge{
				Provider: provider,
				Amount:   amount,
				Snapshot: buildCompletedSearchBillingSnapshot(db, provider, amount, req, usedLocalSearch),
			})
		}
	}

	// Default search can execute AI while SearchMode remains "normal". AI usage is
	// billed as a separate record so it never replaces the normal provider charge.
	if resolved.UsedDefaultAI && hasAIModelUsage(aiUsage) {
		resolvedCharge, err := resolveCompletedAIBillingCharge(db, resolved, req, aiUsage)
		if err != nil {
			return nil, warning, err
		}
		if resolvedCharge.UnpricedModel {
			warning = buildUnpricedAIModelWarning(resolvedCharge.Charge.ModelName)
		}
		charges = append(charges, resolvedCharge.Charge)
	}
	if len(aiUsage.SearchCalls) > 0 {
		searchPrice := service.GetAISearchPricePerCallWithDB(db)
		if searchPrice > 0 {
			for index, call := range aiUsage.SearchCalls {
				amount := util.RoundMoney(searchPrice)
				charges = append(charges, searchBillingCharge{
					Provider: "ai_search",
					Amount:   amount,
					Snapshot: buildAISearchCallBillingSnapshot(req, call, index+1, searchPrice, amount),
				})
			}
		}
	}

	return charges, warning, nil
}

func buildAISearchCallBillingSnapshot(req model.SearchRequest, call search.AISearchCallUsage, index int, price float64, amount float64) string {
	parts := []string{
		"provider=ai_search",
		fmt.Sprintf("searchCall=%d", index),
		"searchAPI=" + url.QueryEscape(strings.TrimSpace(call.API)),
		"searchQuery=" + url.QueryEscape(strings.TrimSpace(call.Query)),
		"searchStatus=" + url.QueryEscape(strings.TrimSpace(call.Status)),
		fmt.Sprintf("searchDurationMs=%d", call.DurationMs),
		fmt.Sprintf("searchResultCount=%d", call.ResultCount),
		fmt.Sprintf("searchTotalResults=%d", call.TotalResults),
		"searchSources=" + url.QueryEscape(strings.TrimSpace(call.SourceDetails)),
		"searchError=" + url.QueryEscape(logger.RedactSecrets(strings.TrimSpace(call.Error))),
		fmt.Sprintf("unitPrice=%.6f", util.RoundMoney(price)),
		fmt.Sprintf("amount=%.6f", util.RoundMoney(amount)),
	}
	if question := strings.TrimSpace(req.Question); question != "" {
		parts = append(parts, "question="+url.QueryEscape(question))
	}
	return strings.Join(parts, " ")
}

// buildUnpricedAIModelWarning 生成给调用方看的未定价提示。
func buildUnpricedAIModelWarning(modelName string) string {
	trimmed := strings.TrimSpace(modelName)
	if trimmed == "" {
		trimmed = "当前模型"
	}
	return fmt.Sprintf("AI 模型「%s」尚未配置价格，本次调用按 0 元计费；请管理员在「AI 搜索配置 → 模型价格表」中为该模型补充输入/缓存输入/输出单价。", trimmed)
}

func completedSearchBillingAmount(charges []searchBillingCharge) float64 {
	total := float64(0)
	for _, charge := range charges {
		total += charge.Amount
	}
	return util.RoundMoney(total)
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

// resolveCompletedAIBillingCharge 按模型价格表计算 AI 计费。
//
// 单价口径：模型价格表里的输入 / 缓存输入 / 输出单价均为「元 / 百万 token」，
// 与用户余额同币种，因此不再需要美元汇率换算。
// 计费时先按 usage 拆分出未命中缓存的输入 token 与命中缓存的输入 token，再分别计价。
// 价格表是唯一计费依据：模型不在价格表中时金额为 0，并在快照里记录未定价原因，
// 便于管理员补齐价格，而不是回退到已废弃的倍率算法。
// resolvedAIBillingCharge 包装 AI 计费结果，并额外标记该模型是否缺少价格配置。
type resolvedAIBillingCharge struct {
	Charge        searchBillingCharge
	UnpricedModel bool
}

func resolveCompletedAIBillingCharge(db *gorm.DB, resolved resolvedSearchRequestContext, req model.SearchRequest, usage search.AISearchUsage) (resolvedAIBillingCharge, error) {
	modelName := strings.TrimSpace(resolved.AIConfig.Model)

	price, matched, err := service.ResolveAIModelTokenPriceWithDB(db, modelName)
	if err != nil {
		return resolvedAIBillingCharge{}, err
	}

	var baseAmount, amount float64
	snapshot := ""
	if matched {
		baseAmount, amount = calculateAIModelPriceTableAmount(price, usage)
		snapshot = buildAIModelPriceTableBillingSnapshot(resolved, req, usage, price, baseAmount, amount)
	} else {
		snapshot = buildUnpricedAIModelBillingSnapshot(resolved, req, usage)
	}

	totalTokens := usage.TotalTokens
	if totalTokens == 0 {
		totalTokens = usage.InputTokens + usage.OutputTokens
	}
	return resolvedAIBillingCharge{
		UnpricedModel: !matched,
		Charge: searchBillingCharge{
			Provider:          searchModeAI,
			Amount:            amount,
			Snapshot:          snapshot,
			ModelName:         modelName,
			InputTokens:       usage.InputTokens,
			CachedInputTokens: usage.CachedInputTokens,
			OutputTokens:      usage.OutputTokens,
			TotalTokens:       totalTokens,
		},
	}, nil
}

// buildUnpricedAIModelBillingSnapshot 记录「模型未配置价格」的零金额账单快照。
// 保留 token 用量是为了让管理员能据此判断该怎么补价格，而不是丢失这次调用记录。
func buildUnpricedAIModelBillingSnapshot(resolved resolvedSearchRequestContext, req model.SearchRequest, usage search.AISearchUsage) string {
	parts := []string{
		"provider=ai",
		fmt.Sprintf("model=%s", strings.TrimSpace(resolved.AIConfig.Model)),
		"pricing=unpriced_model",
		"pricingNote=模型未在模型价格表中配置，本次按 0 计费，请在「模型价格表」中补充单价",
		fmt.Sprintf("inputTokens=%d", usage.InputTokens),
		fmt.Sprintf("cachedInputTokens=%d", usage.CachedInputTokens),
		fmt.Sprintf("outputTokens=%d", usage.OutputTokens),
		fmt.Sprintf("totalTokens=%d", usage.TotalTokens),
		"baseAmount=0.000000",
		"amount=0.000000",
	}
	if questionText := strings.TrimSpace(req.Question); questionText != "" {
		parts = append(parts, fmt.Sprintf("question=%s", url.QueryEscape(questionText)))
	}
	return strings.Join(parts, " ")
}

// calculateAIModelPriceTableAmount 按「输入 / 缓存输入 / 缓存写入 / 输出」四段单价计算金额。
// 传入的 UnitPrice 已经是「单价 × 倍率」，这里不再重复乘倍率。
// 返回的 baseAmount 是未取整的精确值，amount 是按金额精度入账的金额。
func calculateAIModelPriceTableAmount(price service.ResolvedAIModelTokenPrice, usage search.AISearchUsage) (float64, float64) {
	const tokensPerPriceUnit = 1000000.0

	cachedTokens := usage.CachedInputTokens
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > usage.InputTokens {
		cachedTokens = usage.InputTokens
	}
	freshInputTokens := usage.InputTokens - cachedTokens
	if freshInputTokens < 0 {
		freshInputTokens = 0
	}

	cacheWriteTokens := usage.CacheWriteTokens
	if cacheWriteTokens < 0 {
		cacheWriteTokens = 0
	}

	outputTokens := usage.OutputTokens
	if outputTokens < 0 {
		outputTokens = 0
	}

	baseAmount := float64(freshInputTokens)/tokensPerPriceUnit*price.InputUnitPrice +
		float64(cachedTokens)/tokensPerPriceUnit*price.CachedUnitPrice +
		float64(cacheWriteTokens)/tokensPerPriceUnit*price.CacheWriteUnitPrice +
		float64(outputTokens)/tokensPerPriceUnit*price.OutputUnitPrice
	if baseAmount < 0 {
		baseAmount = 0
	}
	return baseAmount, util.RoundMoney(baseAmount)
}

func buildAIModelPriceTableBillingSnapshot(resolved resolvedSearchRequestContext, req model.SearchRequest, usage search.AISearchUsage, price service.ResolvedAIModelTokenPrice, baseAmount float64, amount float64) string {
	cachedTokens := usage.CachedInputTokens
	if cachedTokens > usage.InputTokens {
		cachedTokens = usage.InputTokens
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	freshInputTokens := usage.InputTokens - cachedTokens
	if freshInputTokens < 0 {
		freshInputTokens = 0
	}

	parts := []string{
		"provider=ai",
		fmt.Sprintf("model=%s", strings.TrimSpace(resolved.AIConfig.Model)),
		"pricing=model_price_table",
		fmt.Sprintf("priceId=%d", price.PriceID),
		fmt.Sprintf("multiplier=%.4f", price.Multiplier),
		fmt.Sprintf("inputPrice=%.4f", price.InputPrice),
		fmt.Sprintf("cachedPrice=%.4f", price.CachedPrice),
		fmt.Sprintf("cacheWritePrice=%.4f", price.CacheWritePrice),
		fmt.Sprintf("outputPrice=%.4f", price.OutputPrice),
		fmt.Sprintf("inputUnitPrice=%.8f", price.InputUnitPrice),
		fmt.Sprintf("cachedUnitPrice=%.8f", price.CachedUnitPrice),
		fmt.Sprintf("cacheWriteUnitPrice=%.8f", price.CacheWriteUnitPrice),
		fmt.Sprintf("outputUnitPrice=%.8f", price.OutputUnitPrice),
		fmt.Sprintf("inputTokens=%d", freshInputTokens),
		fmt.Sprintf("cachedInputTokens=%d", cachedTokens),
		fmt.Sprintf("cacheWriteTokens=%d", usage.CacheWriteTokens),
		fmt.Sprintf("outputTokens=%d", usage.OutputTokens),
		fmt.Sprintf("totalTokens=%d", usage.TotalTokens),
		fmt.Sprintf("baseAmount=%.6f", baseAmount),
		fmt.Sprintf("amount=%.6f", amount),
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

	// hash 公式集中在 pkg/util.BuildTikuHashCandidates：返回「当前公式 + 旧 plat 公式 +
	// 更早的未格式化题面变体」，保证公式切换与灰度共存期间已有题库不会失联。
	hashCandidates := util.BuildTikuHashCandidates(req.Question, req.Options, req.Type)

	rows := make([]localSearchMatchRow, 0)
	query := db.Table(entity.TableNameTiku).Select("id, source_name").Where("hash IN ?", hashCandidates)
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

func persistSearchBillingRecordsWithDB(db *gorm.DB, ctx searchBillingContext, charges []searchBillingCharge) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var accessToken entity.AccessToken
		if ctx.AccessTokenID > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ctx.AccessTokenID).First(&accessToken).Error; err != nil {
				return err
			}
		}

		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ctx.UserID).First(&user).Error; err != nil {
			return err
		}

		totalCharged := completedSearchBillingAmount(charges)
		if len(charges) == 0 {
			return nil
		}
		if ctx.AccessTokenID > 0 {
			if accessTokenQuotaWouldExceed(accessToken, totalCharged) {
				return errAccessTokenQuotaExceeded
			}
		}

		// 余额扣的是精确消耗额，只在每个记录边界取整：
		// 若先按入账金额取整再相减，被丢弃的 1e-7 级差额无法靠再次取整挽回，
		// 会让账单里的余额变化与实际消耗对不上。
		exactBalance := user.Balance
		for _, charge := range charges {
			balanceBefore := util.RoundMoney(exactBalance)
			exactBalance -= charge.Amount
			balanceAfter := util.RoundMoney(exactBalance)
			billingRecord := entity.BillingRecord{
				UserID:             ctx.UserID,
				AccessTokenID:      ctx.AccessTokenID,
				Type:               searchBillingRecordTypeNormal,
				UsedDefaultAccount: ctx.UsedDefaultAccount,
				Provider:           charge.Provider,
				ModelName:          charge.ModelName,
				InputTokens:        charge.InputTokens,
				CachedInputTokens:  charge.CachedInputTokens,
				OutputTokens:       charge.OutputTokens,
				TotalTokens:        charge.TotalTokens,
				Amount:             charge.Amount,
				BalanceBefore:      balanceBefore,
				BalanceAfter:       balanceAfter,
				RequestID:          ctx.RequestID,
				UnitPriceSnapshot:  charge.Snapshot,
				// 题目原文单独落一列，供账单列表按关键词搜索。
				// 快照里已经是 URL 编码的 question=，这里解码后存明文。
				QuestionText: service.ExtractBillingSnapshotField(charge.Snapshot, "question"),
				ClientIP:     strings.TrimSpace(ctx.ClientIP),
				Remark:       "搜索答题消耗",
			}
			if err := tx.Create(&billingRecord).Error; err != nil {
				return err
			}
		}

		if ctx.AccessTokenID > 0 {
			totalAmountUsedAfter := util.RoundMoney(accessToken.TotalAmountUsed + totalCharged)
			if err := tx.Model(&accessToken).Where("id = ?", accessToken.ID).Update("total_amount_used", totalAmountUsedAfter).Error; err != nil {
				return err
			}
		}

		return tx.Model(&user).Where("id = ?", user.ID).Update("balance", util.RoundMoney(exactBalance)).Error
	})
}

func writeCompletedSearchAuditLog(resolved resolvedSearchRequestContext, useQuery string, result [][]string, billingProvider string, clientIP string, audit searchAuditMetadata) {
	var user *entity.User
	if resolved.AccessTokenCtx != nil {
		user = &resolved.AccessTokenCtx.User
	}

	descriptionParts := []string{
		searchAuditSummary(audit.Status),
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
	if len(audit.Failures) > 0 {
		descriptionParts = append(descriptionParts, searchFailureSummary(audit.Failures))
	}
	writeAuditLog(
		user,
		searchAuditModule,
		logActionSearch,
		truncateAuditDescription(strings.Join(descriptionParts, " ")),
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
