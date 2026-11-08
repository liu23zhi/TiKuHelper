package controller

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

const aiFallbackAuditModule = "ai_fallback"

type aiFallbackInput struct {
	Question         string                  `json:"question"`
	Type             int                     `json:"type"`
	Options          []string                `json:"options"`
	DeepThinking     bool                    `json:"deepThinking,omitempty"`
	QuestionEvidence *model.QuestionEvidence `json:"questionEvidence,omitempty"`
	// SkipCache 为显式 true 时跳过读取兜底缓存；缺省与 false 都正常使用缓存。
	// 与搜题的 skipCache 语义保持一致。
	SkipCache *bool `json:"skipCache,omitempty"`
}

var runAIFallbackService = func(req model.SearchRequest) service.AIFallbackResult {
	req.Question = strings.TrimSpace(req.Question)
	return service.SearchAIFallbackWithDB(withDatabasePriority(manager.GetManager().GetDB(), req.RequestContext, dbqueue.PriorityHigh), req)
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
	// Preserve this synchronous admitted-work policy, not a global context override.
	ownerContext := context.WithoutCancel(databaseRequestContext(c))
	billingCtx, err := resolveAIFallbackBillingContextWithContext(c, ownerContext)
	if err != nil {
		result := buildAIFallbackSpendingRejection(err)
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
		RequestContext:   ownerContext,
		Question:         strings.TrimSpace(input.Question),
		Type:             input.Type,
		Options:          input.Options,
		SkipCache:        input.SkipCache,
		DeepThinking:     input.DeepThinking || billingCtx.ScriptBilling.managed(),
		QuestionEvidence: input.QuestionEvidence,
		RequestID:        requestID,
	}

	actorScope := scriptRunActorScope(c)

	// 缓存命中：与搜题一致 —— 不再调用 AI、不再计费。
	// 只有显式 skipCache:true 才跳过读取；跳过读仍会把新结果写回（刷新语义）。
	if !shouldBypassFallbackResultCache(req) {
		if cached, ok := loadCachedAIFallbackResult(billingCtx.UserID, req, actorScope); ok {
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
	storeCachedAIFallbackResult(billingCtx.UserID, req, result, actorScope)
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
	return resolveAIFallbackBillingContextWithContext(c, databaseRequestContext(c))
}

func resolveAIFallbackBillingContextWithContext(c *gin.Context, requestContext context.Context) (searchBillingContext, error) {
	userValue, exists := c.Get("user")
	user, ok := userValue.(*entity.User)
	if !exists || !ok || user == nil || user.ID <= 0 {
		return searchBillingContext{}, errors.New("请重新登录后再使用 AI 兜底")
	}

	db := withDatabasePriority(resolveSearchPersistenceDB(), requestContext, dbqueue.PriorityHigh)
	if db == nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}

	var freshUser entity.User
	if err := db.Where("id = ?", user.ID).First(&freshUser).Error; err != nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}
	session := aIFallbackVerifiedScriptSession(c)
	if freshUser.Balance <= 0 && session.ID <= 0 {
		return searchBillingContext{}, errors.New("当前余额不足，AI 兜底已禁用，请充值后再使用")
	}

	var accessTokenID int32

	if tokenValue, exists := c.Get("access_token"); exists {
		token, ok := tokenValue.(*entity.AccessToken)
		if !ok || token == nil || token.ID <= 0 {
			return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
		}
		accessTokenID = token.ID
	}

	billingCtx, err := loadAIFallbackBillingContextWithDB(db, freshUser.ID, accessTokenID, session)
	billingCtx.ClientIP = strings.TrimSpace(c.ClientIP())
	return billingCtx, err
}

func aIFallbackVerifiedScriptSession(c *gin.Context) entity.ScriptRunSession {
	value, _ := c.Get("script_session")
	session, _ := value.(entity.ScriptRunSession)
	// The queued task keeps the authenticated binding, never the credential.
	session.TokenHash = ""
	return session
}

func loadAIFallbackBillingContextWithDB(db *gorm.DB, userID int32, accessTokenID int32, session entity.ScriptRunSession) (searchBillingContext, error) {
	if db == nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}

	var user entity.User
	if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}
	if user.Balance <= 0 && session.ID <= 0 {
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
		if session.ID != 0 {
			return searchBillingContext{}, service.ErrInvalidAccessTokenAuthorization
		}
		if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") {
			return searchBillingContext{}, errors.New("用户已禁用")
		}
		return billingCtx, nil
	}
	var token entity.AccessToken
	if err := db.Where("id = ? AND user_id = ?", accessTokenID, user.ID).First(&token).Error; err != nil {
		return searchBillingContext{}, errors.New("访问令牌已失效")
	}
	if token.ScriptID != 0 || session.ID != 0 {
		if err := validateAIFallbackScriptSessionWithDB(db, user.ID, token, session); err != nil {
			return searchBillingContext{}, err
		}
		billingCtx.ScriptID = token.ScriptID
	}
	scriptPolicy, _, err := checkResolvedScriptSpendingAdmission(db, user, token, session)
	if err != nil {
		return searchBillingContext{}, err
	}
	billingCtx.ScriptBilling = scriptPolicy
	return billingCtx, nil
}

// Revalidate a trusted run-session binding at admission, including when the
// worker starts after queuing. Settlement of already executed work does not
// depend on the session remaining active.
func validateAIFallbackScriptSessionWithDB(db *gorm.DB, userID int32, token entity.AccessToken, session entity.ScriptRunSession) error {
	rejected := fmt.Errorf("%w: 脚本运行会话无效或已过期", service.ErrInvalidAccessTokenAuthorization)
	if token.ScriptID <= 0 || session.ID <= 0 || session.UserID != userID || session.ScriptID != token.ScriptID ||
		session.AccessTokenID != token.ID || session.IdentityHash == "" {
		return rejected
	}
	var current entity.ScriptRunSession
	if err := db.Select("script_id", "identity_hash", "revoked_at", "expires_at", "lease_slot", "source_zone_hash", "last_heartbeat_at").
		Where("id = ? AND script_id = ? AND user_id = ? AND access_token_id = ?",
			session.ID, session.ScriptID, userID, token.ID).First(&current).Error; err != nil ||
		!service.ScriptRunSessionUsableAt(current, time.Now()) ||
		subtle.ConstantTimeCompare([]byte(session.IdentityHash), []byte(current.IdentityHash)) != 1 {
		return rejected
	}
	var script entity.UserScript
	if err := db.Select("base_domain", "use_wildcard_domain").
		Where("id = ? AND user_id = ? AND access_token_id = ? AND enabled = ? AND deleted_at IS NULL",
			session.ScriptID, userID, token.ID, true).First(&script).Error; err != nil {
		return rejected
	}
	if err := service.ValidateScriptDomainWithDB(db, script.BaseDomain, script.UseWildcardDomain); err != nil {
		return rejected
	}
	return nil
}

func persistAIFallbackBilling(billingCtx searchBillingContext, requestID string, req model.SearchRequest, result service.AIFallbackResult) error {
	_, _, err := persistAIFallbackBillingWithWarning(billingCtx, requestID, req, result)
	return err
}

// persistAIFallbackBillingWithWarning 落账 AI 兜底计费，并回报「模型未配置价格」的情况。
// 未定价时按 0 计费，不阻断答案返回，只把提示交给调用方。
func persistAIFallbackBillingWithWarning(billingCtx searchBillingContext, requestID string, req model.SearchRequest, result service.AIFallbackResult) (string, service.AIFallbackResult, error) {
	usage := result.Usage
	// Failed answers do not erase already executed model/summary usage.
	// The existing atomic settlement retains Key limits and money precision.
	if !hasAISearchUsage(usage) {
		return "", result, nil
	}
	db := withDatabasePriority(resolveSearchPersistenceDB(), context.Background(), dbqueue.PriorityHigh)
	if db == nil {
		return "", result, errors.New("AI 兜底计费失败，请稍后重试")
	}

	billingCtx.RequestID = strings.TrimSpace(requestID)
	billingCtx.DeepThinking = req.DeepThinking
	billingCtx.AIRoute = strings.TrimSpace(result.Usage.Route)
	billingCtx.captureScriptExecutionUsage(usage)
	resolved := resolvedSearchRequestContext{
		RequestID: billingCtx.RequestID,
		AIConfig: service.AISearchRequestConfig{
			Model: strings.TrimSpace(result.Model),
		},
	}
	warning := ""
	charges := make([]searchBillingCharge, 0, 1+len(usage.SearchCalls)+len(usage.WebReadCalls)+len(usage.OCRCalls)+len(usage.TikuSearchCalls))
	if hasAIModelUsage(usage) {
		resolvedCharge, err := resolveCompletedAIBillingCharge(db, resolved, req, usage)
		if err != nil {
			if billingCtx.ScriptBilling.mode != entity.ScriptBillingModeActivation || !billingCtx.ScriptExecutionUsage.present() {
				return "", result, err
			}
			logger.SysError(fmt.Sprintf("激活码脚本 AI 兜底价格读取失败 requestId=%s: %s", billingCtx.RequestID, logger.RedactSecrets(err.Error())))
			billingCtx.ScriptExecutionUsage.CostUnavailable = true
		} else {
			// 调用方在计费完成后写审阅记录，因此把倍率信息写入返回结果，确保同步、异步兜底
			// 与普通搜题的审阅快照包含相同的最终倍率信息。
			result.Usage.Trace = resolvedCharge.Trace
			if resolvedCharge.UnpricedModel {
				warning = buildUnpricedAIModelWarning(resolvedCharge.Charge.ModelName)
			}
			charges = append(charges, resolvedCharge.Charge)
		}
	}
	charges = appendAIToolBillingCharges(charges, db, req, usage)
	if len(charges) == 0 && !billingCtx.ScriptExecutionUsage.present() {
		return warning, result, nil
	}
	queued, err := persistSearchBillingRecordsWithRetry(db, billingCtx, charges, searchBillingRetries)
	if err != nil {
		if !queued {
			return warning, result, err
		}
		return appendSearchBillingRetryWarning(warning), result, nil
	}
	return warning, result, nil
}

func buildAIFallbackBillingFailureResult(err error) service.AIFallbackResult {
	if errors.Is(err, errAccessTokenQuotaExceeded) || errors.Is(err, service.ErrInsufficientSearchBalance) {
		return buildAIFallbackSpendingRejection(err)
	}
	return service.AIFallbackResult{
		Success: false,
		Error: &service.AIFallbackError{
			Code:    service.AIFallbackErrorCodeUpstreamError,
			Message: "AI 兜底计费失败，请稍后重试",
		},
	}
}
