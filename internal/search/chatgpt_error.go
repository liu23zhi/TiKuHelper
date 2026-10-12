package search

import (
	stderrs "errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

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
