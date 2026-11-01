package search

import (
	"encoding/json"
	stderrs "errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

type aiErrorMetadata struct {
	Code    string
	Type    string
	Message string
}

func decodeAIErrorMetadata(raw []byte) aiErrorMetadata {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return aiErrorMetadata{}
	}
	metadata := aiErrorMetadata{}
	decodeString := func(key string) string {
		var text string
		if encoded, ok := value[key]; ok && json.Unmarshal(encoded, &text) == nil {
			return strings.TrimSpace(text)
		}
		return ""
	}
	metadata.Code, metadata.Type, metadata.Message = decodeString("code"), decodeString("type"), decodeString("message")
	for _, nestedKey := range []string{"error", "response"} {
		if nested, ok := value[nestedKey]; ok {
			nestedMetadata := decodeAIErrorMetadata(nested)
			if metadata.Code == "" {
				metadata.Code = nestedMetadata.Code
			}
			if metadata.Type == "" {
				metadata.Type = nestedMetadata.Type
			}
			if metadata.Message == "" {
				metadata.Message = nestedMetadata.Message
			}
		}
	}
	return metadata
}

func aiErrorMetadataFromError(err error) aiErrorMetadata {
	if err == nil {
		return aiErrorMetadata{}
	}
	var eventErr *aiResponsesUpstreamError
	if stderrs.As(err, &eventErr) {
		return aiErrorMetadata{Code: eventErr.Code, Type: eventErr.Type, Message: eventErr.Message}
	}
	var upstream *AIUpstreamError
	if !stderrs.As(err, &upstream) || upstream == nil {
		return aiErrorMetadata{}
	}
	detail := strings.TrimSpace(upstream.Detail)
	if detail == "" {
		return aiErrorMetadata{}
	}
	if metadata := decodeAIErrorMetadata([]byte(detail)); metadata.Code != "" || metadata.Type != "" || metadata.Message != "" {
		return metadata
	}
	if start := strings.IndexByte(detail, '{'); start >= 0 {
		if end := strings.LastIndexByte(detail, '}'); end > start {
			return decodeAIErrorMetadata([]byte(detail[start : end+1]))
		}
	}
	return aiErrorMetadata{}
}

// IsAISearchSafetyPolicyError identifies explicit safety-policy rejections,
// including invalid_prompt responses that arrive inside an HTTP 200 SSE stream.
// It deliberately avoids treating generic 400 errors as safety failures.
func IsAISearchSafetyPolicyError(err error) bool {
	metadata := aiErrorMetadataFromError(err)
	code := strings.ToLower(metadata.Code)
	typ := strings.ToLower(metadata.Type)
	message := strings.ToLower(metadata.Message)
	raw := ""
	if err != nil {
		raw = strings.ToLower(err.Error())
	}
	if code == "invalid_prompt" || code == "content_policy_violation" || code == "safety_policy_violation" ||
		strings.Contains(raw, "invalid_prompt") || strings.Contains(raw, "content_policy_violation") || strings.Contains(raw, "safety_policy_violation") {
		return true
	}
	if typ == "invalid_prompt" || typ == "content_policy_violation" || typ == "safety_policy_violation" {
		return true
	}
	return strings.Contains(message, "safety policy") || strings.Contains(message, "content policy") || strings.Contains(message, "prompt was flagged") ||
		strings.Contains(raw, "safety policy") || strings.Contains(raw, "content policy") || strings.Contains(raw, "prompt was flagged")
}

// shouldRetryAISearchFailure is shared by normal, native-tool and MCP paths.
// HTTP retry statuses retain the existing policy; structured SSE errors add
// only transient server/rate-limit and explicit safety-policy cases.
func shouldRetryAISearchFailure(status int, err error) bool {
	if isRetryableAIHTTPStatus(status) || IsAISearchSafetyPolicyError(err) {
		return true
	}
	metadata := aiErrorMetadataFromError(err)
	switch strings.ToLower(metadata.Code) {
	case "server_error", "rate_limit_exceeded", "temporarily_unavailable", "upstream_error":
		return true
	}
	switch strings.ToLower(metadata.Type) {
	case "server_error", "rate_limit_error", "temporarily_unavailable", "upstream_error":
		return true
	}
	return false
}

const aiUpstreamErrorMaxDetailBytes = 4096

// AIUpstreamError preserves actionable diagnostics without exposing credentials.
type AIUpstreamError struct {
	Stage      string
	StatusCode int
	Detail     string
	cause      error
}

func (err *AIUpstreamError) Error() string {
	description := fmt.Sprintf("%s (stage=%s", errors.ErrTargetServerError, err.Stage)
	if err.StatusCode != 0 {
		description += fmt.Sprintf(", HTTP %d", err.StatusCode)
	}
	description += ")"
	if err.Detail != "" {
		description += ": " + err.Detail
	}
	return description
}

func (err *AIUpstreamError) Unwrap() error {
	return errors.ErrTargetServerError
}

func (err *AIUpstreamError) Is(target error) bool {
	return target == errors.ErrTargetServerError || stderrs.Is(err.cause, target)
}

func (err *AIUpstreamError) As(target any) bool {
	return stderrs.As(err.cause, target)
}

func (in *ChatGptClient) upstreamError(stage string, statusCode int, body string, cause error) error {
	var detail []string
	if cause != nil {
		// net/http's url.Error includes the entire request URL, which may carry credentials.
		var urlErr *url.Error
		if stderrs.As(cause, &urlErr) && urlErr.Err != nil {
			detail = append(detail, urlErr.Err.Error())
		} else {
			detail = append(detail, cause.Error())
		}
	}
	if strings.TrimSpace(body) != "" {
		detail = append(detail, strings.TrimSpace(body))
	}
	text := strings.Join(detail, "; response: ")
	text = aiSearchURLPattern.ReplaceAllStringFunc(text, func(address string) string {
		parsed, err := url.Parse(address)
		if err != nil {
			return "[upstream URL]"
		}
		parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
		parsed.ForceQuery = false
		return parsed.String()
	})
	secrets := []string{in.APIKey, strings.TrimSpace(in.APIKey)}
	if parsed, err := url.Parse(in.BaseURL); err == nil {
		if parsed.User != nil {
			secrets = append(secrets, parsed.User.Username())
			if password, ok := parsed.User.Password(); ok {
				secrets = append(secrets, password)
			}
		}
		for _, values := range parsed.Query() {
			secrets = append(secrets, values...)
		}
	}
	text = logger.RedactSecrets(text, secrets...)
	if len(text) > aiUpstreamErrorMaxDetailBytes {
		end := aiUpstreamErrorMaxDetailBytes
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end] + "..."
	}
	return &AIUpstreamError{Stage: stage, StatusCode: statusCode, Detail: text, cause: cause}
}
