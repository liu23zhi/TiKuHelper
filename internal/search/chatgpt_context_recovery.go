package search

import "strings"

// Match context capacity failures, not output caps, rate limits or proxy body
// limits. Compaction must never turn an unrelated failure into extra AI calls.
func isAIContextLengthFailure(status int, err error) bool {
	if err == nil || (status != 200 && status != 400 && status != 413 && status != 422) {
		return false
	}
	detail := strings.ToLower(err.Error())
	for _, marker := range []string{
		"context_length_exceeded", "context_window_exceeded", "context window exceeded",
		"maximum context length", "prompt is too long", "input is too long for this model",
		"input token count exceeds the maximum", "exceeds the model's maximum context",
		"上下文长度超出", "超过最大上下文",
	} {
		if strings.Contains(detail, marker) {
			return true
		}
	}
	return false
}
