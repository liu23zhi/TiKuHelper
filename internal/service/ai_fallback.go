package service

import (
	"context"
	stderrs "errors"
	"net/url"
	"strconv"
	"strings"

	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

type AIFallbackErrorCode string

const (
	AIFallbackErrorCodeAIUnavailable       AIFallbackErrorCode = "AI_UNAVAILABLE"
	AIFallbackErrorCodeNoAnswer            AIFallbackErrorCode = "NO_ANSWER"
	AIFallbackErrorCodeUpstreamError       AIFallbackErrorCode = "UPSTREAM_ERROR"
	AIFallbackErrorCodeInvalidInput        AIFallbackErrorCode = "INVALID_INPUT"
	AIFallbackErrorCodeUnsafeToAnswer      AIFallbackErrorCode = "UNSAFE_TO_ANSWER"
	AIFallbackErrorCodeInsufficientBalance AIFallbackErrorCode = "INSUFFICIENT_BALANCE"
)

type AIFallbackResult struct {
	Success bool                         `json:"success"`
	Result  *AIFallbackAnswer            `json:"result,omitempty"`
	Error   *AIFallbackError             `json:"error,omitempty"`
	Usage   internalsearch.AISearchUsage `json:"-"`
	Model   string                       `json:"-"`
	// FailureDetail is retained for private audit snapshots, never the public API.
	FailureDetail string `json:"-"`

	// UnpricedModel 表示本次回答所用模型没有配置价格（按 0 计费）。
	// 调用方据此提示管理员去价格表补价，但答案本身照常返回，不阻断流程。
	UnpricedModel bool `json:"-"`

	// Warnings 是面向调用方的非阻断提示，例如「AI 正在使用未配置价格的模型」。
	// 正常响应不会带该字段，避免影响既有客户端。
	Warnings []string `json:"warnings,omitempty"`
}

type AIFallbackAnswer struct {
	Question string `json:"question,omitempty"`
	Answer   string `json:"answer,omitempty"`
}

type AIFallbackError struct {
	Code    AIFallbackErrorCode `json:"code"`
	Message string              `json:"message,omitempty"`
}

var runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
	return client.SearchAnswerWithUsage(req)
}

func BuildAIFallbackUnavailableResult() AIFallbackResult {
	return buildAIFallbackErrorResult(AIFallbackErrorCodeAIUnavailable, "AI 搜题服务未配置")
}

func BuildAIFallbackInvalidInputResult(message string) AIFallbackResult {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "题目不能为空"
	}
	return buildAIFallbackErrorResult(AIFallbackErrorCodeInvalidInput, msg)
}

func BuildAIFallbackInsufficientBalanceResult(message string) AIFallbackResult {
	return buildAIFallbackErrorResult(AIFallbackErrorCodeInsufficientBalance, message)
}

func SearchAIFallbackWithDB(db *gorm.DB, req model.SearchRequest) AIFallbackResult {
	return SearchAIFallbackWithContext(req.RequestContext, db, req)
}

// SearchAIFallbackWithContext lets synchronous callers cancel paid upstream
// work, while the legacy entry point and async tasks may use a nil context.
func SearchAIFallbackWithContext(ctx context.Context, db *gorm.DB, req model.SearchRequest) AIFallbackResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		result := buildAIFallbackErrorResult(AIFallbackErrorCodeUpstreamError, "AI 上游请求失败")
		result.FailureDetail = err.Error()
		return result
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return BuildAIFallbackInvalidInputResult("题目不能为空")
	}

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		result := buildAIFallbackErrorResult(AIFallbackErrorCodeUpstreamError, "AI 配置加载失败")
		result.FailureDetail = logger.RedactSecrets(err.Error())
		return result
	}
	if !IsAISearchConfigUsable(resolved.Config) {
		return BuildAIFallbackUnavailableResult()
	}

	client := &internalsearch.ChatGptClient{
		Context:               ctx,
		Enable:                true,
		BaseURL:               resolved.Config.BaseURL,
		APIKey:                resolved.Config.APIKey,
		Model:                 resolved.Config.Model,
		Prompt:                resolved.Config.Prompt,
		Temperature:           0,
		TopP:                  0,
		MaxAISearchModelCalls: resolved.Config.MaxAISearchModelCalls,
		MaxAISearches:         resolved.Config.MaxAISearches,
		MaxAISearchRetries:    resolved.Config.MaxAISearchRetries,
		MaxAIWebReads:         resolved.Config.MaxAIWebReads,
		SearchTool:            BuildUAPISearchToolWithDB(db),
		OCRProvider:           BuildUAPIImageOCRProviderWithDB(db),
		OCRAPI:                "https://uapis.cn/api/v1/image/ocr",
		ContextCompression:    BuildAIContextCompressionConfigWithDB(db, resolved.Config),
		RequestID:             req.RequestID,
	}

	answers, usage, searchErr := runAIFallbackSearchWithUsage(client, model.SearchRequest{
		Question:     question,
		Options:      req.Options,
		Type:         req.Type,
		DeepThinking: req.DeepThinking,
		RequestID:    req.RequestID,
	})
	if searchErr != nil {
		if stderrs.Is(searchErr, pkgerrors.ErrTargetNoAnswer) {
			return buildAIFallbackErrorResultWithUsage(AIFallbackErrorCodeNoAnswer, "未找到可用答案", usage, resolved.Config.Model)
		}
		result := buildAIFallbackErrorResultWithUsage(AIFallbackErrorCodeUpstreamError, "AI 上游请求失败", usage, resolved.Config.Model)
		result.FailureDetail = logger.RedactSecrets(searchErr.Error(), resolved.Config.APIKey)
		return result
	}

	normalizedAnswers, resultCode := collectUsableAIFallbackAnswers(answers)
	if len(normalizedAnswers) == 0 {
		if resultCode == AIFallbackErrorCodeUnsafeToAnswer {
			return buildAIFallbackErrorResultWithUsage(AIFallbackErrorCodeUnsafeToAnswer, "AI 判断当前题目不适合作答", usage, resolved.Config.Model)
		}
		return buildAIFallbackErrorResultWithUsage(AIFallbackErrorCodeNoAnswer, "未找到可用答案", usage, resolved.Config.Model)
	}

	normalizedAnswers = normalizeAIFallbackAnswersForRequest(normalizedAnswers, req)
	if len(normalizedAnswers) == 0 {
		return buildAIFallbackErrorResultWithUsage(AIFallbackErrorCodeNoAnswer, "未找到可用答案", usage, resolved.Config.Model)
	}

	return AIFallbackResult{
		Success: true,
		Result: &AIFallbackAnswer{
			Question: question,
			Answer:   strings.Join(normalizedAnswers, "#"),
		},
		Usage: usage,
		Model: resolved.Config.Model,
	}
}

func buildAIFallbackErrorResultWithUsage(code AIFallbackErrorCode, message string, usage internalsearch.AISearchUsage, modelName string) AIFallbackResult {
	result := buildAIFallbackErrorResult(code, message)
	result.Usage = usage
	result.Model = strings.TrimSpace(modelName)
	return result
}

func buildAIFallbackErrorResult(code AIFallbackErrorCode, message string) AIFallbackResult {
	if code == AIFallbackErrorCodeUpstreamError {
		message = pkgerrors.PublicSearchFailureMessage
	}
	return AIFallbackResult{
		Success: false,
		Error: &AIFallbackError{
			Code:    code,
			Message: strings.TrimSpace(message),
		},
	}
}

// PublicAIFallbackError copies the error so response sanitization never mutates audits.
func PublicAIFallbackError(failure *AIFallbackError) *AIFallbackError {
	if failure == nil {
		return nil
	}
	public := *failure
	if public.Code == AIFallbackErrorCodeUpstreamError {
		public.Message = pkgerrors.PublicSearchFailureMessage
	}
	return &public
}

func collectUsableAIFallbackAnswers(answerGroups [][]string) ([]string, AIFallbackErrorCode) {
	flattened := make([]string, 0)
	sawUnsafe := false
	for _, group := range answerGroups {
		for _, item := range group {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			if isUnsafeToAnswerAIFallbackAnswer(trimmed) {
				sawUnsafe = true
				continue
			}
			if isUncertainAIFallbackAnswer(trimmed) {
				continue
			}
			flattened = append(flattened, trimmed)
		}
	}
	if len(flattened) > 0 {
		return flattened, ""
	}
	if sawUnsafe {
		return nil, AIFallbackErrorCodeUnsafeToAnswer
	}
	return nil, AIFallbackErrorCodeNoAnswer
}

// isObjectiveAIFallbackQuestionType 判断是否为「客观题」（答案必须是给定选项之一）。
//
// 直接复用 pkg/util 的判定，避免两处各写一份 type 判断而彼此漂移。
func isObjectiveAIFallbackQuestionType(questionType int) bool {
	return util.IsObjectiveQuestionType(questionType)
}

// isUsableFreeTextAIFallbackAnswer 判断「映射不到选项」的答案能否作为自由文本采用。
//
// 填空题 / 简答题 / 阅读理解（type 2/4/-4）的答案本来就是自由文本，
// 上游平台又常把编辑器脚本一起塞进 options（实测 55 条里绝大多数是 JS 代码），
// 因此不能用「必须命中选项」来判定有效性 —— 否则一次正常的 AI 回答会被判成未找到答案。
//
// 只有「看起来就是在指代选项」的答案（单个字母 / 纯数字）才丢弃，
// 因为这类答案映射失败说明选项标号对不上，采用它会把错误标号当成答案。
func isUsableFreeTextAIFallbackAnswer(answer string) bool {
	normalized := util.FormatString(answer)
	if normalized == "" {
		return false
	}

	runes := []rune(normalized)
	if len(runes) == 1 {
		if runes[0] >= 'A' && runes[0] <= 'Z' {
			return false
		}
		if runes[0] >= 'a' && runes[0] <= 'z' {
			return false
		}
	}

	if _, err := strconv.Atoi(normalized); err == nil {
		return false
	}
	return true
}

func normalizeAIFallbackAnswersForRequest(answers []string, req model.SearchRequest) []string {
	if len(answers) == 0 {
		return nil
	}

	normalizedOptions := util.FormatOptions(req.Options, req.Type)

	// 非客观题：答案允许是自由文本，映射不到选项不应该判为失败。
	if !isObjectiveAIFallbackQuestionType(req.Type) {
		return normalizeFreeTextAIFallbackAnswers(answers, normalizedOptions, req.Type)
	}

	if len(normalizedOptions) == 0 {
		return dedupeAIFallbackAnswers(answers, util.AllowsRepeatedAnswers(req.Type))
	}

	answerGroups := make([][]string, 0, len(answers))
	for _, answer := range answers {
		mapped := mapAIFallbackAnswerToOptions(strings.TrimSpace(answer), normalizedOptions)
		if len(mapped) == 0 {
			continue
		}
		answerGroups = append(answerGroups, mapped)
	}
	if len(answerGroups) == 0 {
		return nil
	}

	// 多选题：模型常把选中的多个选项拆成 answers 数组的多个元素
	// （例如 ["A. ...","B. ...","C. ..."]）。它们属于**同一个答案集合**，
	// 必须合成一组：FillAnswerResponse 的多选题分支按「每组命中几个选项」判断，
	// 拆成多个单元素组会让每个组都被丢弃，最终误判为 NO_ANSWER
	// （线上 49 次 NO_ANSWER 中 15 次是多选题，模型其实答对了）。
	if req.Type == 1 {
		merged := make([]string, 0, len(answerGroups))
		seen := make(map[string]struct{}, len(answerGroups))
		for _, group := range answerGroups {
			for _, item := range group {
				if _, exists := seen[item]; exists {
					continue
				}
				seen[item] = struct{}{}
				merged = append(merged, item)
			}
		}
		answerGroups = [][]string{merged}
	}

	response := util.FillAnswerResponse(answerGroups, &model.SearchRequest{
		Question: req.Question,
		Options:  normalizedOptions,
		Type:     req.Type,
	})
	return response.Answer.BestAnswer
}

// normalizeFreeTextAIFallbackAnswers 处理非客观题答案。
//
// 若 AI 返回的是选项标号/原文（例如「A」或某个选项文本），仍优先映射成规范选项；
// 否则保留原始文本，只在完全无法映射且该答案本身就是自由文本时采用。
func normalizeFreeTextAIFallbackAnswers(answers []string, options []string, questionType int) []string {
	result := make([]string, 0, len(answers))
	seen := make(map[string]struct{})
	// 非客观题（填空题/连线题/简答题/阅读理解）保留重复答案，理由是多个空/多组连线
	// 可能指向同一文本；去重会让答案数与空数对不上。
	preserveRepeatedAnswers := util.AllowsRepeatedAnswers(questionType)

	for _, answer := range answers {
		trimmed := strings.TrimSpace(answer)
		if trimmed == "" {
			continue
		}

		if len(options) > 0 {
			// 能映射到选项就映射，保证与选项格式一致（例如返回 "B" 转成选项文本）。
			if mapped := mapAIFallbackAnswerToOptions(trimmed, options); len(mapped) > 0 {
				for _, item := range mapped {
					if !preserveRepeatedAnswers {
						if _, exists := seen[item]; exists {
							continue
						}
						seen[item] = struct{}{}
					}
					result = append(result, item)
				}
				continue
			}
		}

		if !isUsableFreeTextAIFallbackAnswer(trimmed) {
			continue
		}
		if !preserveRepeatedAnswers {
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
		}
		result = append(result, trimmed)
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// dedupeAIFallbackAnswers 在无选项场景下去重（填空题允许重复答案）。
func dedupeAIFallbackAnswers(answers []string, preserveRepeatedAnswers bool) []string {
	result := make([]string, 0, len(answers))
	seen := make(map[string]struct{})
	for _, answer := range answers {
		normalized := strings.TrimSpace(answer)
		if normalized == "" {
			continue
		}
		if !preserveRepeatedAnswers {
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
		}
		result = append(result, normalized)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func mapAIFallbackAnswerToOptions(answer string, options []string) []string {
	if answer == "" || len(options) == 0 {
		return nil
	}

	normalized := util.FormatString(answer)
	if normalized == "" {
		return nil
	}

	if exact := matchAIFallbackOption(normalized, options); exact != "" {
		return []string{exact}
	}

	// 判断题：模型常返回 对/错、√/×、true/false，而选项已被 FormatOptions 规范成
	// 正确/错误。这里按「极性」映射，和 /search 链路（util.JudgementPolarity）保持一致。
	if polarity, ok := util.JudgementPolarity(normalized); ok {
		for _, option := range options {
			if optionPolarity, optionOK := util.JudgementPolarity(option); optionOK && optionPolarity == polarity {
				return []string{option}
			}
		}
	}

	if len(normalized) == 1 {
		if index, err := strconv.Atoi(normalized); err == nil {
			idx := index - 1
			if idx >= 0 && idx < len(options) {
				return []string{options[idx]}
			}
		}
		if normalized[0] >= 'A' && normalized[0] <= 'Z' {
			idx := int(normalized[0] - 'A')
			if idx >= 0 && idx < len(options) {
				return []string{options[idx]}
			}
		}
		if normalized[0] >= 'a' && normalized[0] <= 'z' {
			idx := int(normalized[0] - 'a')
			if idx >= 0 && idx < len(options) {
				return []string{options[idx]}
			}
		}
	}

	parts := strings.FieldsFunc(normalized, func(r rune) bool {
		return r == '#' || r == ',' || r == '，' || r == ';' || r == '；' || r == '|' || r == '/' || r == '、' || r == ' '
	})
	if len(parts) > 1 {
		result := make([]string, 0, len(parts))
		seen := make(map[string]struct{})
		for _, part := range parts {
			mapped := mapAIFallbackAnswerToOptions(part, options)
			for _, item := range mapped {
				if _, exists := seen[item]; exists {
					continue
				}
				seen[item] = struct{}{}
				result = append(result, item)
			}
		}
		return result
	}

	return nil
}

func matchAIFallbackOption(answer string, options []string) string {
	for _, option := range options {
		if util.FormatString(option) == answer {
			return option
		}
	}
	return ""
}

func isUncertainAIFallbackAnswer(answer string) bool {
	normalized := normalizeAIFallbackAnswerText(answer)
	return normalized == "不确定" || normalized == "无法确定" || normalized == "不清楚" || normalized == "不太确定"
}

func isUnsafeToAnswerAIFallbackAnswer(answer string) bool {
	normalized := normalizeAIFallbackAnswerText(answer)
	return normalized == "无法回答" || normalized == "不能回答" || normalized == "不便回答" || normalized == "拒绝回答" || normalized == "无法提供答案" || strings.Contains(normalized, "不能提供") || strings.Contains(normalized, "无法提供")
}

func normalizeAIFallbackAnswerText(answer string) string {
	normalized := strings.TrimSpace(answer)
	normalized = strings.Trim(normalized, "\"'`.,;:!?，。；：！？、（）()[]{}")
	return normalized
}
