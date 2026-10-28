package search

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/itihey/tikuAdapter/pkg/logger"
)

var aiContextOpaqueFieldPattern = regexp.MustCompile(`(?i)("encrypted_?content"\s*:\s*)"(?:[^"\\]|\\.)*(?:"|$)`)
var aiContextEscapedOpaqueFieldPattern = regexp.MustCompile(`(?i)(\\"encrypted_?content\\"\s*:\s*\\").*?(?:\\"|$)`)

func redactAIContextOpaqueText(text string) string {
	text = aiContextOpaqueFieldPattern.ReplaceAllString(text, `${1}"[REDACTED]"`)
	return aiContextEscapedOpaqueFieldPattern.ReplaceAllString(text, `${1}[REDACTED]\"`)
}

// Log only allowlisted response metadata. Opaque state and historical content
// must never enter diagnostics just to explain a validation rejection.
func aiContextValidationDetails(response aiContextResponse, before, after, target int64) map[string]any {
	types := map[string]int{}
	valid := 0
	for _, raw := range response.Output {
		var item openAIResponsesOutputItem
		kind := "unknown"
		if json.Unmarshal(raw, &item) == nil {
			switch item.Type {
			case "compaction", "message", "reasoning", "function_call":
				kind = item.Type
			}
			if item.Type == "compaction" && strings.TrimSpace(item.EncryptedContent) != "" {
				valid++
			}
		}
		types[kind]++
	}
	reason := ""
	if response.IncompleteDetails != nil {
		reason = truncateAIToolText(response.IncompleteDetails.Reason, 120)
	}
	return map[string]any{
		"responseObject": truncateAIToolText(response.Object, 64), "responseStatus": truncateAIToolText(response.Status, 64),
		"incompleteReason": reason, "outputItemCount": len(response.Output), "outputTypes": types, "validCompactionCount": valid,
		"beforeTokens": before, "afterTokens": after, "targetTokens": target,
		"tokenEstimate": true,
	}
}

// These events describe validation and strategy decisions, not model requests.
// Keep them out of usage.Trace/ModelCalls so diagnostics cannot add billable use.
func (manager *aiContextCompactor) logCompressionEvent(event string, err error, details map[string]any) {
	if manager == nil || manager.client == nil {
		return
	}
	strategy := manager.currentStrategy()
	if strategy == "" && len(manager.attemptedStrategies) > 0 {
		strategy = manager.attemptedStrategies[len(manager.attemptedStrategies)-1]
	}
	entry := map[string]any{"event": event, "requestId": strings.TrimSpace(manager.client.RequestID), "strategy": strategy, "model": manager.client.Model,
		"round": 0, "attempt": 0, "attemptBudget": manager.config.MaxAttempts, "endpoint": "", "httpStatus": "", "modelCallIndex": 0}
	if manager.usage != nil && len(manager.usage.Trace) > 0 {
		trace := manager.usage.Trace[len(manager.usage.Trace)-1]
		entry["round"], entry["attempt"], entry["attemptBudget"] = trace.Round, trace.Attempt, trace.AttemptBudget
		entry["endpoint"], entry["httpStatus"], entry["modelCallIndex"] = trace.Endpoint, trace.Status, trace.ModelCallIndex
	}
	for key, value := range details {
		entry[key] = value
	}
	if err != nil {
		entry["error"] = redactAIContextOpaqueText(err.Error())
	}
	encoded, encodeErr := json.Marshal(entry)
	if encodeErr != nil {
		return
	}
	logger.AIThinkingWithSecrets("AI压缩事件 "+string(encoded), manager.client.APIKey)
}
