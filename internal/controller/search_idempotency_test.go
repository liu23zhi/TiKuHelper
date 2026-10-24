package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestSearchWithIdempotencyReplaysCompletedResultWithoutRepeatingCharge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatalf("migrate idempotency records: %v", err)
	}
	user := seedControllerUser(t, db, 0, "sync-idempotent-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 12.5).Error; err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "sync-idempotent-token", 0)
	seedControllerDefaultSearchProviderConfig(t, db, "icodef", 2.5)
	var providerCalls atomic.Int32

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:                 searchModeNormal,
				UsedDefaultSearch:          true,
				UsedDefaultSearchProviders: []string{"icodef"},
				Tokens:                     map[string]string{"icodef": "default-icodef"},
				UserID:                     user.ID,
				AccessTokenID:              token.ID,
				RequestID:                  "req-sync-idempotent",
				AccessTokenCtx:             &service.AccessTokenAuthContext{User: user, AccessToken: token},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) {
				providerCalls.Add(1)
				return [][]string{{"幂等答案"}}, nil
			}}
		},
		func(resolvedSearchRequestContext, model.SearchRequest) ([][]string, searchpkg.AISearchUsage, error) {
			return nil, searchpkg.AISearchUsage{}, nil
		},
		func() {
			router := gin.New()
			router.POST("/adapter-service/search", SearchWithIdempotency)
			path := "/adapter-service/search?use=icodef&noRecord=1"
			body := `{"question":"同步幂等题目","options":["A","B"],"skipCache":true}`
			first := performIdempotentSearchRequest(router, path, body, "stable-key", token.TokenValue)
			if first.Code != http.StatusOK {
				t.Fatalf("first search failed: %d %s", first.Code, first.Body.String())
			}
			var firstResult model.SearchResponse
			if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil || len(firstResult.Answer.BestAnswer) == 0 {
				t.Fatalf("first search did not return answer: %+v err=%v", firstResult, err)
			}
			// The client never sees the first response and repeats the same POST.
			replayed := performIdempotentSearchRequest(router, path, body, "stable-key", token.TokenValue)
			if replayed.Code != first.Code || replayed.Body.String() != first.Body.String() {
				t.Fatalf("retry must replay original HTTP result: first=%d %q replay=%d %q", first.Code, first.Body.String(), replayed.Code, replayed.Body.String())
			}
			if got := providerCalls.Load(); got != 1 {
				t.Fatalf("skipCache=true retry called provider %d times", got)
			}
			var billCount int64
			if err := db.Model(&entity.BillingRecord{}).Where("user_id = ?", user.ID).Count(&billCount).Error; err != nil || billCount != 1 {
				t.Fatalf("expected one billing record, count=%d err=%v", billCount, err)
			}
			var storedUser entity.User
			if err := db.First(&storedUser, user.ID).Error; err != nil || storedUser.Balance != 10 {
				t.Fatalf("expected one charge, balance=%v err=%v", storedUser.Balance, err)
			}
			conflict := performIdempotentSearchRequest(router, path, `{"question":"不同题目","skipCache":true}`, "stable-key", token.TokenValue)
			if conflict.Code != http.StatusConflict || providerCalls.Load() != 1 {
				t.Fatalf("same key with different body must conflict without search, got %d calls=%d", conflict.Code, providerCalls.Load())
			}
			queryConflict := performIdempotentSearchRequest(router, "/adapter-service/search?use=ai&noRecord=1", body, "stable-key", token.TokenValue)
			if queryConflict.Code != http.StatusConflict || providerCalls.Load() != 1 {
				t.Fatalf("same key with different query must conflict without search, got %d calls=%d", queryConflict.Code, providerCalls.Load())
			}
			invalid := performIdempotentSearchRequest(router, path, body, "another-key", "invalid-token")
			if invalid.Code != http.StatusUnauthorized || providerCalls.Load() != 1 {
				t.Fatalf("invalid keyed authorization must fail before search, got %d calls=%d", invalid.Code, providerCalls.Load())
			}
			missing := performIdempotentSearchRequest(router, path, body, "another-key", "")
			if missing.Code != http.StatusUnauthorized || providerCalls.Load() != 1 {
				t.Fatalf("missing keyed authorization must fail before search, got %d calls=%d", missing.Code, providerCalls.Load())
			}
			if err := db.Model(&entity.IdempotencyRecord{}).Where("operation = ?", "search").Update("status", service.IdempotencyStatusUnknown).Error; err != nil {
				t.Fatalf("mark stored result unknown: %v", err)
			}
			unknown := performIdempotentSearchRequest(router, path, body, "stable-key", token.TokenValue)
			if unknown.Code != http.StatusConflict || providerCalls.Load() != 1 {
				t.Fatalf("unknown result must not reexecute search, status=%d calls=%d", unknown.Code, providerCalls.Load())
			}
		},
	)
}

func TestSearchWithIdempotencyDoesNotReplayProcessingRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatalf("migrate idempotency records: %v", err)
	}
	user := seedControllerUser(t, db, 0, "sync-pending-user", "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 10).Error; err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	token := seedControllerTokenForUser(t, db, user, "sync-pending-token", 0)
	started := make(chan struct{})
	release := make(chan struct{})
	var providerCalls atomic.Int32
	originalWaitLimit := searchIdempotencyWaitLimit
	searchIdempotencyWaitLimit = 30 * time.Millisecond
	defer func() { searchIdempotencyWaitLimit = originalWaitLimit }()

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeNormal, EnabledSearchProviders: []string{"stub"}, Tokens: map[string]string{"stub": "token"}, UserID: user.ID, AccessTokenID: token.ID, RequestID: "req-sync-pending"}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) {
				providerCalls.Add(1)
				close(started)
				<-release
				return [][]string{{"答案"}}, nil
			}}
		},
		nil,
		func() {
			router := gin.New()
			router.POST("/adapter-service/search", SearchWithIdempotency)
			path := "/adapter-service/search?use=stub&noRecord=1"
			body := `{"question":"处理中题目","skipCache":true}`
			firstDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				firstDone <- performIdempotentSearchRequest(router, path, body, "processing-key", token.TokenValue)
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("first search did not start")
			}
			pending := performIdempotentSearchRequest(router, path, body, "processing-key", token.TokenValue)
			if pending.Code != http.StatusTooEarly || pending.Header().Get("Retry-After") != "1" || providerCalls.Load() != 1 {
				t.Fatalf("processing retry must not run a second search: status=%d header=%q calls=%d", pending.Code, pending.Header().Get("Retry-After"), providerCalls.Load())
			}
			close(release)
			select {
			case first := <-firstDone:
				if first.Code != http.StatusOK {
					t.Fatalf("first request failed: %d %s", first.Code, first.Body.String())
				}
			case <-time.After(time.Second):
				t.Fatal("first search did not complete")
			}
			replayed := performIdempotentSearchRequest(router, path, body, "processing-key", token.TokenValue)
			if replayed.Code != http.StatusOK || providerCalls.Load() != 1 {
				t.Fatalf("completed retry must replay result: status=%d calls=%d", replayed.Code, providerCalls.Load())
			}
		},
	)
}

func TestSearchWithIdempotencyPreservesLegacyNoKeyAnonymousSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeNormal, EnabledSearchProviders: []string{"stub"}, Tokens: map[string]string{"stub": "token"}, RequestID: "req-old-sync"}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) {
				calls.Add(1)
				return [][]string{{"旧接口答案"}}, nil
			}}
		},
		nil,
		func() {
			router := gin.New()
			router.POST("/adapter-service/search", SearchWithIdempotency)
			for i := 0; i < 2; i++ {
				result := performIdempotentSearchRequest(router, "/adapter-service/search?use=stub&noRecord=1", `{"question":"旧接口题目","skipCache":true}`, "", "")
				if result.Code != http.StatusOK {
					t.Fatalf("anonymous legacy request %d failed: %d %s", i, result.Code, result.Body.String())
				}
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("keyless old search should keep original behavior, calls=%d", got)
			}
		},
	)
}

func performIdempotentSearchRequest(router http.Handler, path, body, key, token string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}
