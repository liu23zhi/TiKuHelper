package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestSearchFailureCollectorUsesStableOrderAcrossConcurrentProviders(t *testing.T) {
	collector := &searchFailureCollector{}
	resolved := resolvedSearchRequestContext{Tokens: map[string]string{"provider": "private-provider-key"}}
	var wg sync.WaitGroup
	for i := 31; i >= 0; i-- {
		provider := fmt.Sprintf("provider-%02d", i)
		collector.beginAttempt()
		wg.Add(1)
		go func() {
			defer wg.Done()
			collector.record(resolved, provider, fmt.Errorf("upstream failed key=%s", resolved.Tokens["provider"]))
		}()
	}
	wg.Wait()
	status, failures := collector.snapshot(false, false)
	if status != "failed" || len(failures) != 32 {
		t.Fatalf("lost concurrent failures: status=%s failures=%d", status, len(failures))
	}
	for i, failure := range failures {
		if failure.Provider != fmt.Sprintf("provider-%02d", i) || strings.Contains(failure.Error, "private-provider-key") {
			t.Fatalf("unstable or unredacted failure: %+v", failure)
		}
	}
}

func TestSearchAuditPreservesFailureDetailsAndDistinguishesNoAnswer(t *testing.T) {
	for _, tc := range []struct {
		name         string
		answers      [][]string
		err          error
		status       string
		summary      string
		failureCount int
	}{
		{name: "failure", err: errors.New(strings.Repeat("上游接口不可用", 60)), status: "failed", summary: "搜索失败", failureCount: 1},
		{name: "partial", answers: [][]string{{"答案A"}}, err: errors.New("HTTP 503 upstream unavailable"), status: "partial_failure", summary: "部分渠道失败", failureCount: 1},
		{name: "no answer", err: pkgerrors.ErrTargetNoAnswer, status: "no_answer", summary: "未找到答案"},
		{name: "unconfigured", err: pkgerrors.ErrTokenRequired, status: "unavailable", summary: "未配置"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := newSearchControllerTestDB(t)
			withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
				func(string, string, url.Values) (resolvedSearchRequestContext, error) {
					return resolvedSearchRequestContext{SearchMode: searchModeNormal, RequestID: "req-failure-snapshot", Tokens: map[string]string{"wanneng": "secret-provider-token"}}, nil
				},
				func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
					clients := []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) { return nil, tc.err }}
					if len(tc.answers) > 0 {
						clients = append(clients, func(model.SearchRequest) ([][]string, error) { return tc.answers, nil })
					}
					return clients
				}, nil, func() {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1", bytes.NewBufferString(`{"question":"完整失败详情","type":0,"options":["答案A","答案B"]}`))
					ctx.Request.Header.Set("Content-Type", "application/json")
					Search(ctx)
					if recorder.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
					}
					var publicResp map[string]any
					if err := json.Unmarshal(recorder.Body.Bytes(), &publicResp); err != nil {
						t.Fatal(err)
					}
					if _, ok := publicResp["failures"]; ok {
						t.Fatal("internal failure audit leaked into public response")
					}
					if tc.status == "failed" {
						failure, ok := publicResp["error"].(map[string]any)
						if !ok || failure["code"] != "UPSTREAM_ERROR" || failure["message"] != "服务器内部错误，搜题失败。" {
							t.Fatalf("expected generic public error alongside the legacy answer shape: %s", recorder.Body.String())
						}
						if strings.Contains(recorder.Body.String(), tc.err.Error()) {
							t.Fatal("upstream details leaked into public search failure")
						}
					} else if _, exists := publicResp["error"]; exists {
						t.Fatalf("non-failure response must retain its existing shape: %s", recorder.Body.String())
					}
					var response model.SearchResponse
					if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if len(tc.answers) > 0 && (len(response.Answer.BestAnswer) != 1 || response.Answer.BestAnswer[0] != "答案A") {
						t.Fatalf("partial provider failure lost successful answer: %+v", response)
					}
				})
			var entries []entity.Log
			if err := db.Where("module = ?", searchAuditModule).Find(&entries).Error; err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("expected one audit per search, got %d", len(entries))
			}
			entry := entries[0]
			if !strings.Contains(entry.Description, tc.summary) {
				t.Fatalf("description=%q, want %q", entry.Description, tc.summary)
			}
			if len([]rune(entry.Description)) > 240 {
				t.Fatalf("description exceeds database limit: %d", len([]rune(entry.Description)))
			}
			var snapshot struct {
				Status   string `json:"status"`
				Failures []struct {
					Provider string `json:"provider"`
					Code     string `json:"code"`
					Error    string `json:"error"`
				} `json:"failures"`
			}
			if err := json.Unmarshal([]byte(entry.NewAnswer), &snapshot); err != nil {
				t.Fatalf("unmarshal audit: %v", err)
			}
			if snapshot.Status != tc.status || len(snapshot.Failures) != tc.failureCount {
				t.Fatalf("unexpected snapshot: %+v", snapshot)
			}
			if tc.failureCount > 0 && (snapshot.Failures[0].Provider != "wanneng" || snapshot.Failures[0].Code == "" || snapshot.Failures[0].Error != tc.err.Error()) {
				t.Fatalf("missing full failure detail: %+v", snapshot)
			}
		})
	}
}

func TestSearchPartialFailurePreservesBillingAndSuccessfulCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 328, "partial-audit-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	token := seedControllerTokenForUser(t, db, user, "partial-audit-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "wanneng", 1.6)
	providerCalls := 0
	withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeNormal, RequestID: "req-partial-billing", UserID: user.ID, AccessTokenID: token.ID, Tokens: map[string]string{"wanneng": "configured"}, AccessTokenCtx: &service.AccessTokenAuthContext{User: user, AccessToken: token}}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					providerCalls++
					return [][]string{{"保留成功答案"}}, nil
				},
				func(model.SearchRequest) ([][]string, error) { return nil, errors.New("failed secondary provider") },
			}
		}, nil, func() {
			for i := 0; i < 2; i++ {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=wanneng&noRecord=1", bytes.NewBufferString(`{"question":"部分渠道失败计费","type":4}`))
				ctx.Request.Header.Set("Content-Type", "application/json")
				Search(ctx)
				var response model.SearchResponse
				if recorder.Code != http.StatusOK {
					t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if len(response.Answer.BestAnswer) != 1 || response.Answer.BestAnswer[0] != "保留成功答案" {
					t.Fatalf("lost answer: %+v", response)
				}
			}
		})
	if providerCalls != 1 {
		t.Fatalf("partial success cache changed: provider calls=%d", providerCalls)
	}
	var records []entity.BillingRecord
	if err := db.Where("user_id = ?", user.ID).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Amount != 1.6 {
		t.Fatalf("partial failure changed successful billing: %+v", records)
	}
	var entries []entity.Log
	if err := db.Where("module = ?", searchAuditModule).Order("id").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !strings.Contains(entries[0].NewAnswer, `"status":"partial_failure"`) || !strings.Contains(entries[1].NewAnswer, `"status":"success"`) {
		t.Fatalf("duplicate or missing request audits: %+v", entries)
	}
}

func TestSearchAIExclusiveFailureAuditRedactsKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeAI, RequestID: "req-ai-failure", AIConfig: service.AISearchRequestConfig{BaseURL: "https://ai.example/v1", APIKey: "sk-secret-for-audit", Model: "audit-model"}}, nil
		}, func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) { return nil },
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return nil, searchpkg.AISearchUsage{}, errors.New("HTTP 401 Bearer sk-secret-for-audit authentication failed")
		},
		func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/search?use=ai&noRecord=1", bytes.NewBufferString(`{"question":"AI错误题目"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			Search(ctx)
		})
	var entry entity.Log
	if err := db.Where("module = ?", searchAuditModule).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry.Description, "搜索失败") || !strings.Contains(entry.NewAnswer, `"provider":"ai"`) {
		t.Fatalf("failure not audited: %+v", entry)
	}
	if strings.Contains(entry.Description+entry.NewAnswer, "sk-secret-for-audit") {
		t.Fatal("API key leaked into audit")
	}
	if !strings.Contains(entry.NewAnswer, "authentication failed") {
		t.Fatal("redaction discarded failure reason")
	}
}
