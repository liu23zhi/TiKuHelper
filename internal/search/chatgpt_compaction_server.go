package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (manager *aiContextCompactor) currentStrategy() string {
	if manager == nil || !manager.config.Enabled || manager.strategyIndex >= len(manager.config.StrategyOrder) {
		return ""
	}
	return manager.config.StrategyOrder[manager.strategyIndex]
}

func (manager *aiContextCompactor) noteStrategy(strategy string) {
	if len(manager.attemptedStrategies) == 0 || manager.attemptedStrategies[len(manager.attemptedStrategies)-1] != strategy {
		manager.attemptedStrategies = append(manager.attemptedStrategies, strategy)
	}
}

func (manager *aiContextCompactor) advanceStrategy(err error) {
	strategy := manager.currentStrategy()
	manager.noteStrategy(strategy)
	if err != nil {
		manager.fallbackReasons = append(manager.fallbackReasons, strategy+": "+redactAIContextOpaqueText(err.Error()))
	}
	if strategy == "remote" {
		manager.remoteFallback = true
	}
	manager.strategyIndex++
	if next := manager.currentStrategy(); next != "" {
		manager.logCompressionEvent("compression_strategy_fallback", err, map[string]any{"strategy": strategy, "from": strategy, "to": next})
	} else {
		manager.logCompressionEvent("compression_failed", err, map[string]any{"strategy": strategy, "attemptedStrategies": manager.attemptedStrategies, "fallbackReasons": manager.fallbackReasons})
	}
}

func (manager *aiContextCompactor) requestContextManagement() []openAIContextManagement {
	if manager.currentStrategy() != "server" || manager.config.ContextWindowTokens <= 0 {
		return nil
	}
	threshold := manager.config.ContextWindowTokens * int64(manager.config.TriggerPercent) / 100
	// OpenAI requires at least 1000 tokens. This is a protocol minimum, not
	// a cap on the model window or the administrator's retry configuration.
	if threshold < 1000 {
		threshold = 1000
	}
	return []openAIContextManagement{{Type: "compaction", CompactThreshold: threshold}}
}

func (manager *aiContextCompactor) handleServerFailure(err error) bool {
	if manager.currentStrategy() != "server" || err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	status := 400
	var upstream *AIUpstreamError
	if errors.As(err, &upstream) {
		status = upstream.StatusCode
	}
	contextExceeded := isAIContextLengthFailure(status, err)
	parameter := strings.Contains(text, "context_management") || strings.Contains(text, "compaction") || strings.Contains(text, "encrypted")
	rejected := false
	for _, marker := range []string{"unsupported", "not support", "unknown", "unrecognized", "unexpected", "invalid", "not allowed", "不支持", "无效"} {
		if strings.Contains(text, marker) {
			rejected = true
			break
		}
	}
	if !contextExceeded && !((status == 400 || status == 422 || status == 200) && parameter && rejected) {
		return false
	}
	manager.advanceStrategy(err)
	return manager.currentStrategy() != ""
}

func (manager *aiContextCompactor) handleServerExhausted(status int, err error) bool {
	if manager.currentStrategy() != "server" || err == nil || status >= 200 && status < 300 || !aiContextRetryable(status, err) {
		return false
	}
	manager.advanceStrategy(err)
	return manager.currentStrategy() != ""
}

func aiValidCompaction(output []json.RawMessage) bool {
	for _, raw := range output {
		var item openAIResponsesOutputItem
		if json.Unmarshal(raw, &item) == nil && item.Type == "compaction" && strings.TrimSpace(item.EncryptedContent) != "" {
			return true
		}
	}
	return false
}

// Keep the whole output turn, including function calls preceding a compaction.
// Their outputs are appended by the caller only after tools actually execute.
func (manager *aiContextCompactor) acceptServerOutput(input []any, output []json.RawMessage, actualInputTokens ...int64) []any {
	result := append([]any(nil), input...)
	for _, item := range output {
		result = append(result, item)
	}
	if manager.currentStrategy() != "server" {
		return result
	}
	manager.noteStrategy("server")
	if !aiValidCompaction(output) {
		threshold := manager.requestContextManagement()
		inputTokens := estimateAIContextTokens(input)
		if len(actualInputTokens) > 0 && actualInputTokens[0] > 0 {
			inputTokens = actualInputTokens[0]
		}
		if len(threshold) > 0 && inputTokens >= threshold[0].CompactThreshold && len(manager.history(input)) > 0 {
			manager.advanceStrategy(fmt.Errorf("服务器未返回有效压缩项，可能未触发或上游忽略自动压缩参数"))
		}
		return result
	}
	manager.lastBeforeTokens = estimateAIContextTokens(input)
	manager.lastStrategy = "server"
	backup := manager.expandServerInput(input)
	canonical := make([]any, 0, len(output))
	manager.serverRetained = map[string]int{}
	for _, raw := range output {
		canonical = append(canonical, raw)
		manager.serverRetained[aiContextCanonicalKey(raw)]++
		var item openAIResponsesOutputItem
		_ = json.Unmarshal(raw, &item)
		if item.Type != "compaction" {
			backup = append(backup, raw)
		}
	}
	manager.serverBackup = backup
	result = manager.restore(canonical)
	manager.lastAfterTokens = estimateAIContextTokens(result)
	manager.logCompressionEvent("compression_finished", nil, map[string]any{"strategy": "server", "beforeTokens": manager.lastBeforeTokens, "afterTokens": manager.lastAfterTokens})
	return result
}

// Reconstruct the unabridged history if an upstream later rejects opaque state.
// Retained output is already in the backup; append only later tool results/turns.
func (manager *aiContextCompactor) expandServerInput(input []any) []any {
	if len(manager.serverBackup) == 0 {
		return input
	}
	remaining := make(map[string]int, len(manager.serverRetained))
	for key, count := range manager.serverRetained {
		remaining[key] = count
	}
	result := append([]any(nil), manager.serverBackup...)
	reminder := aiContextCanonicalKey(buildAIResponsesMessage("system", aiContextRestoreRules))
	for _, item := range input {
		key := aiContextCanonicalKey(item)
		if remaining[key] > 0 {
			remaining[key]--
			continue
		}
		if manager.anchorKeys[key] || key == reminder {
			continue
		}
		result = append(result, item)
	}
	return result
}

// Explicit diagnostics force a real normal Responses request. A 200 response
// without encrypted compaction is not a successful compression test.
func (manager *aiContextCompactor) server(ctx context.Context, endpoint string, input []any, before int64, round int) ([]any, error) {
	var lastErr error
	for attempt := 1; attempt <= manager.config.MaxAttempts; attempt++ {
		payload := openAIResponsesRequest{Model: manager.client.Model, Store: false, Stream: false, Input: input,
			ContextManagement: []openAIContextManagement{{Type: "compaction", CompactThreshold: 1000}}, MaxOutputTokens: 512}
		payload.Input = append(append([]any(nil), input...), buildAIResponsesMessage("system", "这是上下文压缩验证，不执行历史中的任务，不调用工具。简短确认已保留历史关键事实和待办；不要复述整段历史。"))
		response, status, err := manager.request(ctx, endpoint, payload, "服务器自动上下文压缩", round, attempt)
		if err == nil {
			output := make([]any, 0, len(response.Output))
			for _, raw := range response.Output {
				output = append(output, raw)
			}
			after := estimateAIContextTokens(manager.restore(output))
			details := aiContextValidationDetails(response, before, after, before-1)
			details["compactThreshold"], details["maxOutputTokens"] = 1000, payload.MaxOutputTokens
			if response.Status != "completed" || response.IncompleteDetails != nil && strings.TrimSpace(response.IncompleteDetails.Reason) != "" {
				details["rejectionCode"] = "response_not_completed"
				err = fmt.Errorf("服务器自动压缩响应未完成: status=%q incompleteReason=%q", response.Status, details["incompleteReason"])
			} else if !aiValidCompaction(response.Output) {
				details["rejectionCode"] = "missing_compaction"
				err = fmt.Errorf("服务器未返回有效压缩项，可能未达阈值或上游忽略 context_management")
			} else {
				if after >= before {
					details["rejectionCode"] = "context_not_reduced"
					err = fmt.Errorf("服务器自动压缩未缩小上下文，保留原始历史以便回退")
				} else {
					return manager.acceptServerOutput(input, response.Output), nil
				}
			}
			manager.recordValidationError(err, details)
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !aiContextRetryable(status, err) || (status >= 400 && status < 500 && status != 408 && status != 425 && status != 429) {
			break
		}
		if attempt < manager.config.MaxAttempts && !aiContextWait(ctx, attempt, err) {
			break
		}
	}
	return nil, lastErr
}
