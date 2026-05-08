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

var buildSearchClientBindings = func(resolved resolvedSearchRequestContext, useQuery string) []searchClientBinding {
	clients := []searchClientBinding{
		{
			Provider: "buguake",
			Search: (&search.BuguakeClient{
				Enable: strings.Contains(useQuery, "buguake") || useQuery == "",
			}).SearchAnswer,
		},
		{
			Provider: "icodef",
			Search: (&search.IcodefClient{
				Token:  resolved.Tokens["icodef"],
				Enable: strings.Contains(useQuery, "icodef") || useQuery == "",
			}).SearchAnswer,
		},
		{
			Provider: "wanneng",
			Search: (&search.WannengClient{
				Token:  resolved.Tokens["wanneng"],
				Enable: strings.Contains(useQuery, "wanneng") || useQuery == "",
			}).SearchAnswer,
		},
		{
			Provider: "tikuhai",
			Search: (&search.TikuhaiClient{
				Enable: strings.Contains(useQuery, "tikuhai") || useQuery == "",
				Token:  resolved.Tokens["tikuhai"],
			}).SearchAnswer,
		},
		{
			Provider: "enncy",
			Search: (&search.EnncyClient{
				Token:  resolved.Tokens["enncy"],
				Enable: strings.Contains(useQuery, "enncy"),
			}).SearchAnswer,
		},
		{
			Provider: "aidian",
			Search: (&search.AidianClient{
				Enable: strings.Contains(useQuery, "aidian"),
				YToken: resolved.Tokens["aidian"],
			}).SearchAnswer,
		},
		{
			Provider: "lemon",
			Search: (&search.LemonClient{
				Enable: strings.Contains(useQuery, "lemon"),
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

var searchAIAnswer = func(resolved resolvedSearchRequestContext, req model.SearchRequest) ([][]string, bool, error) {
	client := &search.ChatGptClient{
		Enable:      shouldUseAISearch("ai", resolved.AIConfig),
		BaseURL:     resolved.AIConfig.BaseURL,
		APIKey:      resolved.AIConfig.APIKey,
		Model:       resolved.AIConfig.Model,
		Prompt:      resolved.AIConfig.Prompt,
		Temperature: resolved.AIConfig.Temperature,
		TopP:        resolved.AIConfig.TopP,
	}
	answer, usage, err := client.SearchAnswerWithUsage(req)
	return answer, usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.TotalTokens > 0, err
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

	var result [][]string      // 最后所有的答案的二维数组
	var localAnswer [][]string // 本地答案
	resolved := resolveResult
	aiEnabled := shouldUseAISearch(useQuery, resolved.AIConfig)
	hasAIUsage := false
	usedLocalSearch := false
	auditHitProvider := ""
	if strings.Contains(useQuery, "local") { // 使用本地题库的话
		localAnswer, err = defaultDBSearchProvider()(req)
		if err != nil {
			logger.SysError(fmt.Sprintf("查询本地答案出错：%s", err.Error()))
		}
		if len(localAnswer) > 0 {
			usedLocalSearch = true
			auditHitProvider = searchBillingProviderLocal
		}
		result = append(result, localAnswer...)
	}

	// 再查询第三方
	if len(result) == 0 {
		if resolved.SearchMode == searchModeAI {
			aiResult, aiUsage, aiErr := searchAIAnswer(resolved, req)
			if aiErr == nil && len(aiResult) > 0 {
				result = append(result, aiResult...)
				hasAIUsage = aiUsage
				auditHitProvider = searchModeAI
			}
		} else {
			clientBindings := buildSearchClientBindings(resolved, useQuery)

			var wg sync.WaitGroup
			var mu sync.Mutex

			for i := range clientBindings {
				wg.Add(1)
				go func(binding searchClientBinding) {
					defer wg.Done()
					res, err := binding.Search(req)
					if err == nil && len(res) > 0 {
						mu.Lock()
						defer mu.Unlock()
						if auditHitProvider == "" {
							auditHitProvider = strings.TrimSpace(binding.Provider)
						}
						result = append(result, res...)
					}
				}(clientBindings[i])
			}
			wg.Wait()
		}
	}
	resp := util.FillAnswerResponse(result, &req)
	audit := buildSearchAuditMetadata(c, resolved, query, rawBody, useQuery, result, usedLocalSearch, aiEnabled, auditHitProvider, req, resp)
	reportCompletedSearchBilling(resolved, useQuery, aiEnabled, result, hasAIUsage, usedLocalSearch)
	persistCompletedSearchBilling(resolved, useQuery, aiEnabled, req, result, hasAIUsage, usedLocalSearch, c.ClientIP(), audit)

	if len(localAnswer) == 0 && c.Query("noRecord") == "" { // 只有本地题库没有答案或者明确标志不要记录
		middleware.CollectAnswer(resp, req.CourseName, req.Extra, audit.HitProvider)
	}
	c.JSON(http.StatusOK, resp)
}

func shouldUseAISearch(useQuery string, config service.AISearchRequestConfig) bool {
	if !strings.Contains(useQuery, "ai") {
		return false
	}
	return config.BaseURL != "" && config.APIKey != "" && config.Model != ""
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
		fmt.Sprintf("query=%s", query.Encode()),
		fmt.Sprintf("contentType=%s", c.ContentType()),
		fmt.Sprintf("origin=%s", c.GetHeader("Origin")),
		fmt.Sprintf("userAgent=%s", c.GetHeader("User-Agent")),
		fmt.Sprintf("clientIp=%s", c.ClientIP()),
		fmt.Sprintf("authorization=%s", authorization),
		fmt.Sprintf("body=%s", rawBody),
	}
	return strings.Join(parts, " ")
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

	if resolved.AccessTokenID == 0 {
		return ctx
	}

	switch resolved.SearchMode {
	case searchModeAI:
		ctx.UsedDefaultAccount = aiEnabled && resolved.UsedDefaultAI
		ctx.Billable = ctx.UsedDefaultAccount
	case searchModeNormal:
		explicitProvider := resolveExplicitNormalSearchProvider(useQuery)
		hasExplicitProviderToken := explicitProvider != "" && strings.TrimSpace(resolved.Tokens[explicitProvider]) != ""
		ctx.UsedDefaultAccount = didUseDefaultNormalSearchProvider(useQuery, resolved.UsedDefaultSearchProviders)
		ctx.Billable = usedLocalSearch || ctx.UsedDefaultAccount || hasExplicitProviderToken
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

func persistCompletedSearchBilling(resolved resolvedSearchRequestContext, useQuery string, aiEnabled bool, req model.SearchRequest, result [][]string, hasAIUsage bool, usedLocalSearch bool, clientIP string, audit searchAuditMetadata) {
	ctx := buildCompletedSearchBillingContext(resolved, useQuery, aiEnabled, result, hasAIUsage, usedLocalSearch)
	if !ctx.Billable {
		writeCompletedSearchAuditLog(resolved, useQuery, result, "", clientIP, audit)
		return
	}

	db := resolveSearchPersistenceDB()
	if db == nil {
		logger.SysError(fmt.Sprintf("搜索计费落账失败：数据库不可用 requestId=%s", resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_db_unavailable", clientIP, audit)
		return
	}

	billingProvider, billingAmount, snapshot, err := resolveCompletedSearchBillingCharge(db, resolved, useQuery, req, usedLocalSearch)
	if err != nil {
		logger.SysError(fmt.Sprintf("搜索计费落账失败：%s requestId=%s", err.Error(), resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_charge_resolve_failed", clientIP, audit)
		return
	}
	if billingAmount <= 0 {
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_zero_amount", clientIP, audit)
		return
	}

	if err := persistSearchBillingRecordWithDB(db, ctx, billingProvider, billingAmount, snapshot); err != nil {
		logger.SysError(fmt.Sprintf("搜索计费落账失败：%s requestId=%s", err.Error(), resolved.RequestID))
		writeCompletedSearchAuditLog(resolved, useQuery, result, "billing_persist_failed", clientIP, audit)
		return
	}

	writeCompletedSearchAuditLog(resolved, useQuery, result, billingProvider, clientIP, audit)
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

func resolveCompletedSearchBillingCharge(db *gorm.DB, resolved resolvedSearchRequestContext, useQuery string, req model.SearchRequest, usedLocalSearch bool) (string, float64, string, error) {
	if resolved.SearchMode != searchModeNormal {
		return "", 0, "", nil
	}

	provider := resolveCompletedSearchBillingProvider(useQuery, resolved.UsedDefaultSearchProviders, usedLocalSearch)
	if provider == "" {
		return "", 0, "", nil
	}

	var config entity.SearchAccountConfig
	if err := db.Where("provider = ?", provider).First(&config).Error; err != nil {
		return "", 0, "", err
	}

	amount := roundCurrencyAmount(config.FlatPrice)
	return provider, amount, buildCompletedSearchBillingSnapshot(db, provider, amount, req, usedLocalSearch), nil
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

func persistSearchBillingRecordWithDB(db *gorm.DB, ctx searchBillingContext, provider string, amount float64, snapshot string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var accessToken entity.AccessToken
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ctx.AccessTokenID).First(&accessToken).Error; err != nil {
			return err
		}

		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ctx.UserID).First(&user).Error; err != nil {
			return err
		}

		balanceBefore := roundCurrencyAmount(user.Balance)
		balanceAfter := roundCurrencyAmount(balanceBefore - amount)
		billingRecord := entity.BillingRecord{
			UserID:             ctx.UserID,
			AccessTokenID:      ctx.AccessTokenID,
			Type:               searchBillingRecordTypeNormal,
			UsedDefaultAccount: ctx.UsedDefaultAccount,
			Provider:           provider,
			Amount:             amount,
			BalanceBefore:      balanceBefore,
			BalanceAfter:       balanceAfter,
			RequestID:          ctx.RequestID,
			UnitPriceSnapshot:  snapshot,
			Remark:             "搜索答题消耗",
		}
		if err := tx.Create(&billingRecord).Error; err != nil {
			return err
		}

		totalAmountUsedAfter := roundCurrencyAmount(accessToken.TotalAmountUsed + amount)
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

func detectSearchMode(query url.Values) string {
	if strings.Contains(query.Get("use"), "ai") {
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
		if authCtx.User.Balance < 0 {
			if !hasExplicitSearchProviderOverride(query) {
				return resolved, fmt.Errorf("%w: 当前余额不足，系统题库已禁用，请传入渠道参数后重试", service.ErrInvalidAccessTokenAuthorization)
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
	resolved.UsedDefaultSearch = resolvedTokens.UsedDefaultAccount
	resolved.UsedDefaultSearchProviders = resolvedTokens.UsedDefaultProviders

	resolvedAI, err := service.BuildAISearchRequestConfigWithDB(db, query, allowDefaultAccount)
	if err != nil {
		return resolved, err
	}
	resolved.AIConfig = resolvedAI.Config
	resolved.UsedDefaultAI = resolvedAI.UsedDefaultAccount

	return resolved, nil
}
