package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/uapi"
	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
)

type searchProviderFailure struct {
	Provider string `json:"provider"`
	Code     string `json:"code"`
	Error    string `json:"error"`
}

type searchFailureCollector struct {
	mu                  sync.Mutex
	failures            []searchProviderFailure
	unavailableAttempts int
	attempts            int
}

func (s *searchFailureCollector) beginAttempt() {
	s.mu.Lock()
	s.attempts++
	s.mu.Unlock()
}

func (s *searchFailureCollector) record(resolved resolvedSearchRequestContext, provider string, err error) {
	if err == nil || errors.Is(err, pkgerrors.ErrTargetNoAnswer) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if errors.Is(err, pkgerrors.ErrTokenRequired) {
		s.unavailableAttempts++
		return
	}
	secrets := []string{resolved.AIConfig.APIKey}
	for _, token := range resolved.Tokens {
		secrets = append(secrets, token)
	}
	s.failures = append(s.failures, searchProviderFailure{
		Provider: strings.TrimSpace(provider),
		Code:     searchFailureCode(err),
		Error:    logger.RedactSecrets(err.Error(), secrets...),
	})
}

func (s *searchFailureCollector) snapshot(hasAnswer bool, unavailableAI bool) (string, []searchProviderFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	failures := append([]searchProviderFailure(nil), s.failures...)
	sort.Slice(failures, func(i, j int) bool {
		if failures[i].Provider != failures[j].Provider {
			return failures[i].Provider < failures[j].Provider
		}
		if failures[i].Code != failures[j].Code {
			return failures[i].Code < failures[j].Code
		}
		return failures[i].Error < failures[j].Error
	})
	if len(failures) > 0 {
		if hasAnswer {
			return "partial_failure", failures
		}
		return "failed", failures
	}
	if hasAnswer {
		return "success", failures
	}
	if unavailableAI || s.attempts == s.unavailableAttempts {
		return "unavailable", failures
	}
	return "no_answer", failures
}

func searchFailureCode(err error) string {
	switch {
	case errors.Is(err, pkgerrors.ErrTargetAPIFlow), errors.Is(err, uapi.ErrRateLimited):
		return "RATE_LIMITED"
	case errors.Is(err, pkgerrors.ErrParserJSON):
		return "INVALID_RESPONSE"
	case errors.Is(err, context.DeadlineExceeded):
		return "REQUEST_TIMEOUT"
	case errors.Is(err, context.Canceled):
		return "REQUEST_CANCELED"
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return "REQUEST_TIMEOUT"
	}
	var apiErr *uapi.APIError
	if errors.As(err, &apiErr) && strings.TrimSpace(apiErr.Code) != "" {
		return apiErr.Code
	}
	return "UPSTREAM_ERROR"
}

func searchAuditSummary(status string) string {
	switch status {
	case "failed":
		return "搜索失败"
	case "partial_failure":
		return "搜索完成，部分渠道失败"
	case "no_answer":
		return "搜索完成，未找到答案"
	case "unavailable":
		return "搜索未执行，服务未配置或未启用"
	default:
		return "搜索完成"
	}
}

func addSearchFailuresToAudit(audit searchAuditMetadata, status string, failures []searchProviderFailure, resp model.SearchResponse) searchAuditMetadata {
	audit.Status = status
	audit.Failures = failures
	// Embed the public response unchanged, adding diagnostics only to the stored audit.
	raw, err := json.Marshal(struct {
		model.SearchResponse
		Status        string                  `json:"status"`
		Failures      []searchProviderFailure `json:"failures,omitempty"`
		AISearchTrace []search.AISearchTrace  `json:"aiSearchTrace,omitempty"`
	}{SearchResponse: resp, Status: status, Failures: failures, AISearchTrace: audit.AISearchTrace})
	if err == nil {
		audit.RawResponse = string(raw)
	}
	return audit
}

func searchFailureSummary(failures []searchProviderFailure) string {
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		parts = append(parts, fmt.Sprintf("provider=%s code=%s error=%s", failure.Provider, failure.Code, failure.Error))
	}
	return strings.Join(parts, "; ")
}
