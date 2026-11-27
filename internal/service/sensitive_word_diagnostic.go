package service

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/uapi"
)

func sensitiveWordFailure(err error) string {
	if err == nil {
		return ""
	}
	failure := uapi.DescribeProviderFailure(err)
	// Legacy detector adapters without a typed error may emit the fixed
	// historical envelope. Recover at most the bounded numeric status.
	if failure.Class == "upstream" && failure.HTTPStatus == 0 {
		if status := legacySensitiveWordHTTPStatus(err.Error()); status != 0 {
			failure.Class, failure.HTTPStatus = sensitiveWordHTTPClass(status), status
		}
	}
	return formatSensitiveWordFailure(failure.Class, failure.HTTPStatus)
}

func formatSensitiveWordFailure(class string, status int) string {
	note := ""
	if class == SensitiveWordErrorTextTooLong {
		note = " (256KB)"
	}
	if status != 0 {
		return fmt.Sprintf("%s%s HTTP %d", class, note, status)
	}
	return class + note
}

// Historical diagnostic strings can contain provider text, URLs or IDs.
// Project them to controlled tokens; never copy any suffix or correlation
// from storage back into a new diagnostic. The SDK audit retains its own
// keyed provider correlation, independently of this durable column.
func safeSensitiveWordDiagnostic(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if message == ErrSensitiveWordNotConfigured.Error() {
		return "unconfigured"
	}
	if status := legacySensitiveWordHTTPStatus(message); status != 0 {
		return formatSensitiveWordFailure(sensitiveWordHTTPClass(status), status)
	}
	base, httpField, hasStatus := strings.Cut(message, " HTTP ")
	class := strings.TrimSuffix(base, " (256KB)")
	switch class {
	case "cancelled", "timeout", "unauthorized", "forbidden", "credits_exhausted", "rate_limited",
		"not_found", SensitiveWordErrorTextTooLong, "invalid_argument", "transport", "upstream_5xx",
		"upstream_4xx", "invalid_response", "upstream", "unconfigured":
		status := 0
		if hasStatus {
			status = sensitiveWordHTTPStatus(httpField)
		}
		return formatSensitiveWordFailure(class, status)
	default:
		return "upstream"
	}
}

func sensitiveWordHTTPStatus(value string) int {
	if len(value) != 3 || value[0] < '1' || value[0] > '5' ||
		value[1] < '0' || value[1] > '9' || value[2] < '0' || value[2] > '9' {
		return 0
	}
	status, _ := strconv.Atoi(value)
	return status
}

func legacySensitiveWordHTTPStatus(value string) int {
	rest, ok := strings.CutPrefix(value, "uapi: HTTP ")
	if !ok || len(rest) < 3 {
		return 0
	}
	if len(rest) > 3 && rest[3] != ' ' && rest[3] != ':' {
		return 0
	}
	return sensitiveWordHTTPStatus(rest[:3])
}

func sensitiveWordHTTPClass(status int) string {
	switch status {
	case 401:
		return "unauthorized"
	case 402:
		return "credits_exhausted"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 413:
		return SensitiveWordErrorTextTooLong
	case 429:
		return "rate_limited"
	}
	switch {
	case status >= 500:
		return "upstream_5xx"
	case status >= 400:
		return "upstream_4xx"
	default:
		return "invalid_response"
	}
}

func safeSensitiveWordAggregate(results []SensitiveWordScopeResult) string {
	failures := make([]string, 0, len(results))
	for _, result := range results {
		if diagnostic := safeSensitiveWordDiagnostic(result.Error); diagnostic != "" {
			scope := result.Scope
			if scope != SensitiveWordScopeQuestion && scope != SensitiveWordScopeAnswer {
				scope = "check"
			}
			failures = append(failures, scope+": "+diagnostic)
		}
	}
	return truncateSensitiveWordError(strings.Join(failures, "; "))
}
