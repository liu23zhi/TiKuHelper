package service

import (
	stderrs "errors"
	"net/url"
	"strconv"
	"strings"

	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
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
	question := strings.TrimSpace(req.Question)
	if question == "" {
		return BuildAIFallbackInvalidInputResult("题目不能为空")
	}

	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		return buildAIFallbackErrorResult(AIFallbackErrorCodeUpstreamError, "AI 配置加载失败")
	}
	if !IsAISearchConfigUsable(resolved.Config) {
		return BuildAIFallbackUnavailableResult()
	}

	client := &internalsearch.ChatGptClient{
		Enable:      true,
		BaseURL:     resolved.Config.BaseURL,
		APIKey:      resolved.Config.APIKey,
		Model:       resolved.Config.Model,
		Prompt:      resolved.Config.Prompt,
		Temperature: 0,
		TopP:        0,
	}

	answers, usage, searchErr := runAIFallbackSearchWithUsage(client, model.SearchRequest{
		Question: question,
		Options:  req.Options,
		Type:     req.Type,
	})
	if searchErr != nil {
		if stderrs.Is(searchErr, pkgerrors.ErrTargetNoAnswer) {
			return buildAIFallbackErrorResult(AIFallbackErrorCodeNoAnswer, "未找到可用答案")
		}
		return buildAIFallbackErrorResult(AIFallbackErrorCodeUpstreamError, "AI 上游请求失败")
	}

	normalizedAnswers, resultCode := collectUsableAIFallbackAnswers(answers)
	if len(normalizedAnswers) == 0 {
		if resultCode == AIFallbackErrorCodeUnsafeToAnswer {
			return buildAIFallbackErrorResult(AIFallbackErrorCodeUnsafeToAnswer, "AI 判断当前题目不适合作答")
		}
		return buildAIFallbackErrorResult(AIFallbackErrorCodeNoAnswer, "未找到可用答案")
	}

	normalizedAnswers = normalizeAIFallbackAnswersForRequest(normalizedAnswers, req)
	if len(normalizedAnswers) == 0 {
		return buildAIFallbackErrorResult(AIFallbackErrorCodeNoAnswer, "未找到可用答案")
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

func buildAIFallbackErrorResult(code AIFallbackErrorCode, message string) AIFallbackResult {
	return AIFallbackResult{
		Success: false,
		Error: &AIFallbackError{
			Code:    code,
			Message: strings.TrimSpace(message),
		},
	}
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

func normalizeAIFallbackAnswersForRequest(answers []string, req model.SearchRequest) []string {
	if len(answers) == 0 {
		return nil
	}

	normalizedOptions := util.FormatOptions(req.Options, req.Type)
	if len(normalizedOptions) == 0 {
		result := make([]string, 0, len(answers))
		seen := make(map[string]struct{})
		preserveRepeatedAnswers := req.Type == 2
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
		return result
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

	response := util.FillAnswerResponse(answerGroups, &model.SearchRequest{
		Question: req.Question,
		Options:  normalizedOptions,
		Type:     req.Type,
	})
	return response.Answer.BestAnswer
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
