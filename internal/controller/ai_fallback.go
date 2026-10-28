package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

const aiFallbackAuditModule = "ai_fallback"

type aiFallbackInput struct {
	Question     string   `json:"question"`
	Type         int      `json:"type"`
	Options      []string `json:"options"`
	DeepThinking bool     `json:"deepThinking,omitempty"`
	// SkipCache 为显式 true 时跳过读取兜底缓存；缺省与 false 都正常使用缓存。
	// 与搜题的 skipCache 语义保持一致。
	SkipCache *bool `json:"skipCache,omitempty"`
}

var runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
	req.Question = strings.TrimSpace(req.Question)
	return service.SearchAIFallbackWithDB(manager.GetManager().GetDB(), req)
}

func AIFallback(c *gin.Context) {
	var input aiFallbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		result := service.BuildAIFallbackInvalidInputResult("参数错误")
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), uuid.NewString(), "", "input", "sync", model.SearchRequest{}, result)
		writeAIFallbackResult(c, result)
		return
	}

	if strings.TrimSpace(input.Question) == "" {
		result := service.BuildAIFallbackInvalidInputResult("题目不能为空")
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), uuid.NewString(), "", "input", "sync", model.SearchRequest{
			Type:    input.Type,
			Options: input.Options,
		}, result)
		writeAIFallbackResult(c, result)
		return
	}
	clearAIResponseWriteDeadline(c)
	billingCtx, err := resolveAIFallbackBillingContext(c)
	if err != nil {
		result := service.BuildAIFallbackInsufficientBalanceResult(err.Error())
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), uuid.NewString(), "", "billing_precheck", "sync", model.SearchRequest{
			Question: strings.TrimSpace(input.Question),
			Type:     input.Type,
			Options:  input.Options,
		}, result)
		writeAIFallbackResult(c, result)
		return
	}

	requestID := uuid.NewString()
	req := model.SearchRequest{
		RequestContext: c.Request.Context(),
		Question:       strings.TrimSpace(input.Question),
		Type:           input.Type,
		Options:        input.Options,
		SkipCache:      input.SkipCache,
		DeepThinking:   input.DeepThinking,
		RequestID:      requestID,
	}

	// 缓存命中：与搜题一致 —— 不再调用 AI、不再计费。
	// 只有显式 skipCache:true 才跳过读取；跳过读仍会把新结果写回（刷新语义）。
	if !shouldBypassFallbackResultCache(req) {
		if cached, ok := loadCachedAIFallbackResult(billingCtx.UserID, req); ok {
			logger.SysLog(fmt.Sprintf("AI 兜底缓存命中 requestId=%s", requestID))
			writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "result", "sync", req, cached)
			writeAIFallbackResult(c, cached)
			return
		}
	}

	result := func() service.AIFallbackResult {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "panic", "sync", req, buildAIFallbackPanicResult(recovered))
				panic(recovered)
			}
		}()
		return runAIFallbackService(req)
	}()
	if !result.Success {
		warning, billedResult, billingErr := persistAIFallbackBillingWithWarning(billingCtx, requestID, req, result)
		if billingErr != nil {
			failure := buildAIFallbackBillingFailureResult(billingErr)
			writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "billing", "sync", req, failure)
			writeAIFallbackResult(c, failure)
			return
		}
		result = billedResult
		if strings.TrimSpace(warning) != "" {
			result.Warnings = append(result.Warnings, warning)
		}
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "result", "sync", req, result)
		writeAIFallbackResult(c, result)
		return
	}
	warning, billedResult, err := persistAIFallbackBillingWithWarning(billingCtx, requestID, req, result)
	if err != nil {
		failure := buildAIFallbackBillingFailureResult(err)
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "billing", "sync", req, failure)
		writeAIFallbackResult(c, failure)
		return
	}
	result = billedResult
	if strings.TrimSpace(warning) != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	storeCachedAIFallbackResult(billingCtx.UserID, req, result)
	writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "result", "sync", req, result)
	writeAIFallbackResult(c, result)
}

func writeAIFallbackResult(c *gin.Context, result service.AIFallbackResult) {
	result.Error = service.PublicAIFallbackError(result.Error)
	c.JSON(http.StatusOK, result)
}

type aiFallbackAuditIdentity struct {
	UserID   int32
	Username string
	ClientIP string
}

func newAIFallbackAuditIdentity(c *gin.Context) aiFallbackAuditIdentity {
	identity := aiFallbackAuditIdentity{}
	if c == nil {
		return identity
	}
	identity.ClientIP = strings.TrimSpace(c.ClientIP())
	userValue, exists := c.Get("user")
	user, ok := userValue.(*entity.User)
	if exists && ok && user != nil {
		identity.UserID = user.ID
		identity.Username = strings.TrimSpace(user.Username)
	}
	return identity
}

func writeAIFallbackAuditLog(identity aiFallbackAuditIdentity, requestID string, taskID string, stage string, mode string, req model.SearchRequest, result service.AIFallbackResult) {
	summary := "AI兜底完成"
	if !result.Success {
		summary = "AI兜底失败"
	}
	descriptionParts := []string{
		summary,
		fmt.Sprintf("status=%s", aiFallbackResultStatus(result)),
		fmt.Sprintf("stage=%s", strings.TrimSpace(stage)),
		fmt.Sprintf("mode=%s", strings.TrimSpace(mode)),
		fmt.Sprintf("requestId=%s", strings.TrimSpace(requestID)),
	}
	route := strings.TrimSpace(result.Usage.Route)
	if route == "" {
		if req.DeepThinking {
			route = internalsearch.AISearchRouteDeepNative
		} else {
			route = internalsearch.AISearchRouteNormal
		}
	}
	descriptionParts = append(descriptionParts, "hitProvider=ai", fmt.Sprintf("aiRoute=%s", route))
	if strings.TrimSpace(taskID) != "" {
		descriptionParts = append(descriptionParts, fmt.Sprintf("taskId=%s", strings.TrimSpace(taskID)))
	}
	if result.Error != nil {
		descriptionParts = append(descriptionParts, fmt.Sprintf("code=%s", strings.TrimSpace(string(result.Error.Code))))
		if message := strings.TrimSpace(result.Error.Message); message != "" {
			descriptionParts = append(descriptionParts, fmt.Sprintf("message=%s", message))
		}
	}
	if result.Success {
		descriptionParts = append(descriptionParts,
			"answers=1",
			"hitProvider=ai",
			"billingProvider=ai",
			fmt.Sprintf("model=%s", strings.TrimSpace(result.Model)),
			fmt.Sprintf("inputTokens=%d", result.Usage.InputTokens),
			fmt.Sprintf("cachedInputTokens=%d", result.Usage.CachedInputTokens),
			fmt.Sprintf("outputTokens=%d", result.Usage.OutputTokens),
			fmt.Sprintf("totalTokens=%d", result.Usage.TotalTokens),
		)
	}
	if hasAISearchUsage(result.Usage) {
		descriptionParts = append(descriptionParts, buildAISearchUsageBasicSummary(result.Usage))
	}
	descriptionParts = append(descriptionParts, fmt.Sprintf("deepThinking=%t", req.DeepThinking))
	fallbackAnswer := ""
	if result.Result != nil {
		fallbackAnswer = truncateForDebug(result.Result.Answer, 240)
	}
	logger.AIThinking(fmt.Sprintf("AI兜底最终结果 requestId=%s taskId=%s stage=%s mode=%s status=%s deepThinking=%t answer=%s usage=%s error=%s", strings.TrimSpace(requestID), strings.TrimSpace(taskID), strings.TrimSpace(stage), strings.TrimSpace(mode), aiFallbackResultStatus(result), req.DeepThinking, fallbackAnswer, buildAISearchUsageDebugSummary(result.Usage), logger.RedactSecrets(result.FailureDetail)))

	user := &entity.User{ID: identity.UserID, Username: strings.TrimSpace(identity.Username)}
	if !result.Success {
		failureParts := append([]string(nil), descriptionParts...)
		failureParts = append(failureParts, fmt.Sprintf("userId=%d", identity.UserID), fmt.Sprintf("type=%d", req.Type), fmt.Sprintf("question=%s", truncateForDebug(req.Question, 120)))
		if result.Error == nil || result.Error.Code == service.AIFallbackErrorCodeUpstreamError {
			logger.SysError(strings.Join(failureParts, " "))
		} else {
			logger.SysWarn(strings.Join(failureParts, " "))
		}
	}
	writeAuditLog(
		user,
		aiFallbackAuditModule,
		logActionAIFallback,
		truncateAuditDescription(strings.Join(descriptionParts, " ")),
		identity.ClientIP,
		0,
		buildAIFallbackAuditSnapshot(req),
		buildAIFallbackResultAuditSnapshot(result),
	)
}

func aiFallbackResultStatus(result service.AIFallbackResult) string {
	if result.Success {
		return "success"
	}
	return "failed"
}

func buildAIFallbackAuditSnapshot(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func buildAIFallbackResultAuditSnapshot(result service.AIFallbackResult) string {
	return buildAIFallbackAuditSnapshot(struct {
		service.AIFallbackResult
		FailureDetail string                         `json:"failureDetail,omitempty"`
		AISearchTrace []internalsearch.AISearchTrace `json:"aiSearchTrace,omitempty"`
	}{AIFallbackResult: result, FailureDetail: logger.RedactSecrets(result.FailureDetail), AISearchTrace: result.Usage.Trace})
}

func resolveAIFallbackBillingContext(c *gin.Context) (searchBillingContext, error) {
	userValue, exists := c.Get("user")
	user, ok := userValue.(*entity.User)
	if !exists || !ok || user == nil || user.ID <= 0 {
		return searchBillingContext{}, errors.New("请重新登录后再使用 AI 兜底")
	}

	db := resolveSearchPersistenceDB()
	if db == nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}

	var freshUser entity.User
	if err := db.Where("id = ?", user.ID).First(&freshUser).Error; err != nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}
	if freshUser.Balance <= 0 {
		return searchBillingContext{}, errors.New("当前余额不足，AI 兜底已禁用，请充值后再使用")
	}

	var accessTokenID int32

	if tokenValue, exists := c.Get("access_token"); exists {
		token, ok := tokenValue.(*entity.AccessToken)
		if !ok || token == nil || token.ID <= 0 {
			return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
		}
		// 令牌累计额度只做展示/统计，不再拦截请求：只要账号余额 > 0 就允许调用。
		accessTokenID = token.ID
	}

	billingCtx, err := loadAIFallbackBillingContextWithDB(db, freshUser.ID, accessTokenID)
	billingCtx.ClientIP = strings.TrimSpace(c.ClientIP())
	return billingCtx, err
}

func loadAIFallbackBillingContextWithDB(db *gorm.DB, userID int32, accessTokenID int32) (searchBillingContext, error) {
	if db == nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}

	var user entity.User
	if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}
	if user.Balance <= 0 {
		return searchBillingContext{}, errors.New("当前余额不足，AI 兜底已禁用，请充值后再使用")
	}

	billingCtx := searchBillingContext{
		UserID:             user.ID,
		AccessTokenID:      accessTokenID,
		SearchMode:         searchModeAI,
		UsedDefaultAccount: true,
		Billable:           true,
	}
	if accessTokenID <= 0 {
		return billingCtx, nil
	}

	return billingCtx, nil
}

func persistAIFallbackBilling(billingCtx searchBillingContext, requestID string, req model.SearchRequest, result service.AIFallbackResult) error {
	_, _, err := persistAIFallbackBillingWithWarning(billingCtx, requestID, req, result)
	return err
}

// persistAIFallbackBillingWithWarning 落账 AI 兜底计费，并回报「模型未配置价格」的情况。
// 未定价时按 0 计费，不阻断答案返回，只把提示交给调用方。
func persistAIFallbackBillingWithWarning(billingCtx searchBillingContext, requestID string, req model.SearchRequest, result service.AIFallbackResult) (string, service.AIFallbackResult, error) {
	usage := result.Usage
	if !result.Success {
		usage.InputTokens = 0
		usage.CachedInputTokens = 0
		usage.CacheWriteTokens = 0
		usage.OutputTokens = 0
		usage.TotalTokens = 0
	}
	if !hasAISearchUsage(usage) {
		return "", result, nil
	}
	db := resolveSearchPersistenceDB()
	if db == nil {
		return "", result, errors.New("AI 兜底计费失败，请稍后重试")
	}

	billingCtx.RequestID = strings.TrimSpace(requestID)
	billingCtx.DeepThinking = req.DeepThinking
	billingCtx.AIRoute = strings.TrimSpace(result.Usage.Route)
	resolved := resolvedSearchRequestContext{
		RequestID: billingCtx.RequestID,
		AIConfig: service.AISearchRequestConfig{
			Model: strings.TrimSpace(result.Model),
		},
	}
	warning := ""
	charges := make([]searchBillingCharge, 0, 1+len(usage.SearchCalls)+len(usage.WebReadCalls))
	if result.Success && hasAIModelUsage(usage) {
		resolvedCharge, err := resolveCompletedAIBillingCharge(db, resolved, req, usage)
		if err != nil {
			return "", result, err
		}
		// 调用方在计费完成后写审阅记录，因此把倍率信息写入返回结果，确保同步、异步兜底
		// 与普通搜题的审阅快照包含相同的最终倍率信息。
		result.Usage.Trace = resolvedCharge.Trace
		if resolvedCharge.UnpricedModel {
			warning = buildUnpricedAIModelWarning(resolvedCharge.Charge.ModelName)
		}
		charges = append(charges, resolvedCharge.Charge)
	}
	charges = appendAIToolBillingCharges(charges, db, req, usage)
	if len(charges) == 0 {
		return warning, result, nil
	}
	if err := persistSearchBillingRecordsWithDB(db, billingCtx, charges); err != nil {
		return warning, result, err
	}
	return warning, result, nil
}

func buildAIFallbackBillingFailureResult(err error) service.AIFallbackResult {
	return service.AIFallbackResult{
		Success: false,
		Error: &service.AIFallbackError{
			Code:    service.AIFallbackErrorCodeUpstreamError,
			Message: "AI 兜底计费失败，请稍后重试",
		},
	}
}
