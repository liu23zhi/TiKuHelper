package service

import (
	stderrs "errors"
	"net/url"
	"strings"

	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

type AIFallbackErrorCode string

const (
	AIFallbackErrorCodeAIUnavailable  AIFallbackErrorCode = "AI_UNAVAILABLE"
	AIFallbackErrorCodeNoAnswer       AIFallbackErrorCode = "NO_ANSWER"
	AIFallbackErrorCodeUpstreamError  AIFallbackErrorCode = "UPSTREAM_ERROR"
	AIFallbackErrorCodeInvalidInput   AIFallbackErrorCode = "INVALID_INPUT"
	AIFallbackErrorCodeUnsafeToAnswer AIFallbackErrorCode = "UNSAFE_TO_ANSWER"
)

type AIFallbackResult struct {
	Success bool             `json:"success"`
	Result  *AIFallbackAnswer `json:"result,omitempty"`
	Error   *AIFallbackError  `json:"error,omitempty"`
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

	answers, _, searchErr := runAIFallbackSearchWithUsage(client, model.SearchRequest{
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

	return AIFallbackResult{
		Success: true,
		Result: &AIFallbackAnswer{
			Question: question,
			Answer:   strings.Join(normalizedAnswers, "#"),
		},
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
