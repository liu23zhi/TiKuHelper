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
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

const aiFallbackAuditModule = "ai_fallback"

type aiFallbackInput struct {
	Question string   `json:"question"`
	Type     int      `json:"type"`
	Options  []string `json:"options"`
}

var runAIFallbackService = func(question string, questionType int, options []string) service.AIFallbackResult {
	return service.SearchAIFallbackWithDB(manager.GetManager().GetDB(), model.SearchRequest{
		Question: strings.TrimSpace(question),
		Type:     questionType,
		Options:  options,
	})
}

func AIFallback(c *gin.Context) {
	var input aiFallbackInput
	if err := c.ShouldBindJSON(&input); err != nil {
		result := service.BuildAIFallbackInvalidInputResult("参数错误")
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), uuid.NewString(), "", "input", "sync", model.SearchRequest{}, result)
		c.JSON(http.StatusOK, result)
		return
	}

	if strings.TrimSpace(input.Question) == "" {
		result := service.BuildAIFallbackInvalidInputResult("题目不能为空")
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), uuid.NewString(), "", "input", "sync", model.SearchRequest{
			Type:    input.Type,
			Options: input.Options,
		}, result)
		c.JSON(http.StatusOK, result)
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
		c.JSON(http.StatusOK, result)
		return
	}

	requestID := uuid.NewString()
	result := runAIFallbackService(input.Question, input.Type, input.Options)
	req := model.SearchRequest{
		Question: strings.TrimSpace(input.Question),
		Type:     input.Type,
		Options:  input.Options,
	}
	if !result.Success {
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "result", "sync", req, result)
		c.JSON(http.StatusOK, result)
		return
	}
	warning, err := persistAIFallbackBillingWithWarning(billingCtx, requestID, req, result)
	if err != nil {
		failure := buildAIFallbackBillingFailureResult(err)
		writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "billing", "sync", req, failure)
		c.JSON(http.StatusOK, failure)
		return
	}
	if strings.TrimSpace(warning) != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	writeAIFallbackAuditLog(newAIFallbackAuditIdentity(c), requestID, "", "result", "sync", req, result)
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

	user := &entity.User{ID: identity.UserID, Username: strings.TrimSpace(identity.Username)}
	writeAuditLog(
		user,
		aiFallbackAuditModule,
		logActionAIFallback,
		truncateForDebug(strings.Join(descriptionParts, " "), 240),
		identity.ClientIP,
		0,
		buildAIFallbackAuditSnapshot(req),
		buildAIFallbackAuditSnapshot(result),
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
		var freshToken entity.AccessToken
		if err := db.Where("id = ?", token.ID).First(&freshToken).Error; err != nil {
			return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
		}
		if accessTokenQuotaExhausted(freshToken) {
			return searchBillingContext{}, errors.New("当前访问令牌累计额度不足，AI 兜底已禁用，请调整令牌额度后重试")
		}
		accessTokenID = freshToken.ID
	}

	return loadAIFallbackBillingContextWithDB(db, freshUser.ID, accessTokenID)
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

	var token entity.AccessToken
	if err := db.Where("id = ?", accessTokenID).First(&token).Error; err != nil {
		return searchBillingContext{}, errors.New("AI 兜底计费校验失败，请稍后重试")
	}
	if accessTokenQuotaExhausted(token) {
		return searchBillingContext{}, errors.New("当前访问令牌累计额度不足，AI 兜底已禁用，请调整令牌额度后重试")
	}
	return billingCtx, nil
}

func persistAIFallbackBilling(billingCtx searchBillingContext, requestID string, req model.SearchRequest, result service.AIFallbackResult) error {
	_, err := persistAIFallbackBillingWithWarning(billingCtx, requestID, req, result)
	return err
}

// persistAIFallbackBillingWithWarning 落账 AI 兜底计费，并回报「模型未配置价格」的情况。
// 未定价时按 0 计费，不阻断答案返回，只把提示交给调用方。
func persistAIFallbackBillingWithWarning(billingCtx searchBillingContext, requestID string, req model.SearchRequest, result service.AIFallbackResult) (string, error) {
	if !result.Success || !hasAISearchUsage(result.Usage) {
		return "", nil
	}
	db := resolveSearchPersistenceDB()
	if db == nil {
		return "", errors.New("AI 兜底计费失败，请稍后重试")
	}

	billingCtx.RequestID = strings.TrimSpace(requestID)
	resolved := resolvedSearchRequestContext{
		RequestID: billingCtx.RequestID,
		AIConfig: service.AISearchRequestConfig{
			Model: strings.TrimSpace(result.Model),
		},
	}
	resolvedCharge, err := resolveCompletedAIBillingCharge(db, resolved, req, result.Usage)
	if err != nil {
		return "", err
	}

	warning := ""
	if resolvedCharge.UnpricedModel {
		warning = buildUnpricedAIModelWarning(resolvedCharge.Charge.ModelName)
	}
	if resolvedCharge.Charge.Amount <= 0 {
		return warning, nil
	}
	return warning, persistSearchBillingRecordsWithDB(db, billingCtx, []searchBillingCharge{resolvedCharge.Charge})
}

func buildAIFallbackBillingFailureResult(err error) service.AIFallbackResult {
	if errors.Is(err, errAccessTokenQuotaExceeded) {
		return service.BuildAIFallbackInsufficientBalanceResult("当前访问令牌累计额度不足，AI 兜底已禁用，请调整令牌额度后重试")
	}
	return service.AIFallbackResult{
		Success: false,
		Error: &service.AIFallbackError{
			Code:    service.AIFallbackErrorCodeUpstreamError,
			Message: "AI 兜底计费失败，请稍后重试",
		},
	}
}
