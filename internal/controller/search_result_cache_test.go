package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"errors"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/pkg/model"
	"strings"
)

func TestSearchReusesCachedResultForIdenticalQuestionAndOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(authorization string, _ string, _ url.Values) (resolvedSearchRequestContext, error) {
			accessTokenID := int32(0)
			userID := int32(0)
			if authorization == "Basic token-a" {
				accessTokenID = 101
				userID = 201
			}
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: accessTokenID,
				UserID:        userID,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					return [][]string{{"A"}}, nil
				},
			}
		},
		nil,
		func() {
			first := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			second := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")

			if first.Answer.AnswerText != "A" || second.Answer.AnswerText != "A" {
				t.Fatalf("expected both responses to contain cached answer, got first=%+v second=%+v", first.Answer, second.Answer)
			}
			if upstreamCalls != 1 {
				t.Fatalf("expected one upstream call, got %d", upstreamCalls)
			}
		})
}

// 缓存按用户隔离：不同用户的相同问题不得互相复用。
func TestSearchDoesNotReuseCachedResultAcrossUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(authorization string, _ string, _ url.Values) (resolvedSearchRequestContext, error) {
			accessTokenID := int32(0)
			userID := int32(0)
			switch authorization {
			case "Basic token-a":
				accessTokenID = 101
				userID = 201
			case "Basic token-b":
				accessTokenID = 202
				userID = 202
			}
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: accessTokenID,
				UserID:        userID,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					return [][]string{{"缓存答案"}}, nil
				},
			}
		},
		nil,
		func() {
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-b")

			if upstreamCalls != 2 {
				t.Fatalf("expected separate upstream calls for different users, got %d", upstreamCalls)
			}
		})
}

// 缓存按用户隔离，因此同一用户名下的多把令牌应复用同一份结果。
func TestSearchReusesCachedResultAcrossTokensOfSameUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(authorization string, _ string, _ url.Values) (resolvedSearchRequestContext, error) {
			// 两把令牌属于同一个用户 201。
			accessTokenID := int32(101)
			if authorization == "Basic token-b" {
				accessTokenID = 102
			}
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: accessTokenID,
				UserID:        201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					return [][]string{{"缓存答案"}}, nil
				},
			}
		},
		nil,
		func() {
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-b")

			if upstreamCalls != 1 {
				t.Fatalf("expected the same user's second token to reuse the cache, got %d upstream calls", upstreamCalls)
			}
		})
}

// 匿名请求（无用户身份）不参与缓存，避免所有匿名调用共用一份结果。
func TestSearchDoesNotCacheAnonymousRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				Tokens:     map[string]string{"icodef": "test-token"},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					return [][]string{{"缓存答案"}}, nil
				},
			}
		},
		nil,
		func() {
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic anonymous")
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic anonymous")

			if upstreamCalls != 2 {
				t.Fatalf("expected anonymous requests not to share a cache, got %d upstream calls", upstreamCalls)
			}
		})
}

// 回归：缓存必须有失效手段。
//
// 历史实现只有 TTL，没有任何失效入口：题目答案被人工修正（或批量导入、
// AI 配置变更）后，同一个问题仍会在整个 TTL 内继续返回旧答案。
func TestInvalidateSearchResultCacheDropsAllCachedAnswers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamCalls := 0
	answer := "旧答案"
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: 101,
				UserID:        201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					return [][]string{{answer}}, nil
				},
			}
		},
		nil,
		func() {
			first := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题"}`, "Basic token-a")
			if first.Answer.AnswerText != "旧答案" || upstreamCalls != 1 {
				t.Fatalf("expected first call to hit upstream, got %q calls=%d", first.Answer.AnswerText, upstreamCalls)
			}

			// 缓存命中：不再调用上游。
			cached := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题"}`, "Basic token-a")
			if cached.Answer.AnswerText != "旧答案" || upstreamCalls != 1 {
				t.Fatalf("expected cache hit, got %q calls=%d", cached.Answer.AnswerText, upstreamCalls)
			}

			// 答案被修正 + 缓存失效。
			answer = "新答案"
			invalidateSearchResultCaches("test")

			refreshed := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题"}`, "Basic token-a")
			if refreshed.Answer.AnswerText != "新答案" {
				t.Fatalf("expected the invalidated cache to re-fetch, got %q", refreshed.Answer.AnswerText)
			}
			if upstreamCalls != 2 {
				t.Fatalf("expected exactly one extra upstream call after invalidation, got %d", upstreamCalls)
			}
		})
}

// buildSearchAnswerCacheKeyForTest 为指定缓存实例构造搜题缓存键，并返回当前代次。
// 便于用独立实例单测缓存原语，不污染全局缓存。
func buildSearchAnswerCacheKeyForTest(cache *resultCache[searchAnswerPayload], userID int32, req model.SearchRequest) (string, uint64) {
	epoch := cache.currentEpoch()
	key, _ := marshalCacheKey(searchResultCacheKey{
		Epoch:        epoch,
		UserID:       userID,
		Question:     strings.TrimSpace(req.Question),
		Options:      req.Options,
		Type:         req.Type,
		Use:          "",
		SourceDigest: "",
	})
	return key, epoch
}

// 失效后，进行中的旧请求不能把陈旧结果写回缓存。
func TestSearchResultCacheRejectsWriteFromStaleEpoch(t *testing.T) {
	cache := newResultCache[searchAnswerPayload](time.Minute, time.Now)

	key, epoch := buildSearchAnswerCacheKeyForTest(cache, 101, model.SearchRequest{Question: "同一道题"})
	cache.invalidate()

	// 模拟「旧代次请求晚到」：写入必须被丢弃。
	cache.put(key, searchAnswerPayload{answers: [][]string{{"陈旧答案"}}}, epoch, searchAnswerPayloadUsable)
	if _, ok := cache.get(key, epoch); ok {
		t.Fatal("expected the stale write to be rejected")
	}

	// 新代次的写入正常生效。
	newKey, newEpoch := buildSearchAnswerCacheKeyForTest(cache, 101, model.SearchRequest{Question: "同一道题"})
	cache.put(newKey, searchAnswerPayload{answers: [][]string{{"新答案"}}}, newEpoch, searchAnswerPayloadUsable)
	got, ok := cache.get(newKey, newEpoch)
	if !ok || len(got.answers) != 1 || got.answers[0][0] != "新答案" {
		t.Fatalf("expected the new answer to be cached, got %v ok=%t", got, ok)
	}
}

// 失效必须立刻生效，而不是等 TTL 到期。
func TestSearchResultCacheTTLStillAppliesAfterInvalidation(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	cache := newResultCache[searchAnswerPayload](time.Minute, func() time.Time { return now })

	key, epoch := buildSearchAnswerCacheKeyForTest(cache, 101, model.SearchRequest{Question: "q"})
	cache.put(key, searchAnswerPayload{answers: [][]string{{"答案"}}}, epoch, searchAnswerPayloadUsable)
	if _, ok := cache.get(key, epoch); !ok {
		t.Fatal("expected a fresh entry to be readable")
	}

	// 超过 TTL 后过期。
	now = now.Add(2 * time.Minute)
	if _, ok := cache.get(key, epoch); ok {
		t.Fatal("expected the entry to expire after the TTL")
	}
}

// 回归：失败的搜题结果绝不能入缓存。
//
// 缓存必须拒绝「不可用结果」，否则一次上游抖动产生的空答案会被固化整个 TTL，
// 用户在此期间怎么重试都只能拿到空答案（且不会重新调用上游）。
func TestSearchDoesNotCacheFailedResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetSearchFlights()
	defer resetSearchFlights()

	upstreamCalls := 0
	shouldFail := true

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode: searchModeNormal,
				Tokens:     map[string]string{"icodef": "test-token"},
				UserID:     201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					if shouldFail {
						return nil, errors.New("上游失败")
					}
					return [][]string{{"A"}}, nil
				},
			}
		},
		nil,
		func() {
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			if upstreamCalls != 1 {
				t.Fatalf("expected the first call to reach upstream, got %d", upstreamCalls)
			}
			if size := searchResultCache.size(); size != 0 {
				t.Fatalf("expected the failed result not to be cached, got %d entries", size)
			}

			// 上游恢复后，第二次必须**真的重新调用上游**并拿到答案。
			shouldFail = false
			recovered := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			if upstreamCalls != 2 {
				t.Fatalf("expected the retry to call upstream again, got %d calls", upstreamCalls)
			}
			if recovered.Answer.AnswerText == "" {
				t.Fatal("expected a recovered answer after the upstream failure")
			}
		})
}

func TestSearchDoesNotReuseCachedResultForDifferentQuestionsOrOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	testCases := []struct {
		name       string
		firstBody  string
		secondBody string
	}{
		{
			name:       "different question",
			firstBody:  `{"question":"第一道题","options":["A","B"]}`,
			secondBody: `{"question":"第二道题","options":["A","B"]}`,
		},
		{
			name:       "different option content",
			firstBody:  `{"question":"同一道题","options":["A","B"]}`,
			secondBody: `{"question":"同一道题","options":["A","C"]}`,
		},
		{
			name:       "different option order",
			firstBody:  `{"question":"同一道题","options":["A","B"]}`,
			secondBody: `{"question":"同一道题","options":["B","A"]}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			upstreamCalls := 0
			withStubSearchDependencies(t,
				(&stubSearchClient{}).SearchAnswer,
				func(string, string, url.Values) (resolvedSearchRequestContext, error) {
					return resolvedSearchRequestContext{
						SearchMode:    searchModeNormal,
						Tokens:        map[string]string{"icodef": "test-token"},
						AccessTokenID: 101,
						UserID:        201,
					}, nil
				},
				func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
					return []func(model.SearchRequest) ([][]string, error){
						func(model.SearchRequest) ([][]string, error) {
							upstreamCalls++
							return [][]string{{"缓存答案"}}, nil
						},
					}
				},
				nil,
				func() {
					performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", testCase.firstBody, "Basic token-a")
					performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", testCase.secondBody, "Basic token-a")

					if upstreamCalls != 2 {
						t.Fatalf("expected separate upstream calls for different question or options, got %d", upstreamCalls)
					}
				})
		})
	}
}

func TestSearchResultCacheExpiresAfterTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	now := time.Now()
	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: 101,
				UserID:        201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					return [][]string{{"缓存答案"}}, nil
				},
			}
		},
		nil,
		func() {
			searchResultCache.now = func() time.Time {
				return now
			}

			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			now = now.Add(defaultResultCacheTTL)
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")

			if upstreamCalls != 2 {
				t.Fatalf("expected expired cache entry to trigger a new upstream call, got %d", upstreamCalls)
			}
		})
}

func TestSearchCacheHitSkipsBillingReporter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalReporter := searchBillingReporter
	defer func() {
		searchBillingReporter = originalReporter
	}()

	reported := 0
	searchBillingReporter = func(searchBillingContext) {
		reported++
	}

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: 101,
				UserID:        201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				(&stubSearchClient{enable: true, answer: [][]string{{"缓存答案"}}}).SearchAnswer,
			}
		},
		nil,
		func() {
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")

			if reported != 1 {
				t.Fatalf("expected only the upstream miss to report billing, got %d reports", reported)
			}
		})
}

func TestSearchSkipCacheBypassesCachedResultAndRefreshesCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: 101,
				UserID:        201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstreamCalls++
					if upstreamCalls == 1 {
						return [][]string{{"旧答案"}}, nil
					}
					return [][]string{{"新答案"}}, nil
				},
			}
		},
		nil,
		func() {
			first := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","type":2}`, "Basic token-a")
			refreshed := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","type":2,"skipCache":true}`, "Basic token-a")
			cachedAfterRefresh := performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","type":2}`, "Basic token-a")

			if first.Answer.AnswerText != "旧答案" {
				t.Fatalf("expected first response to use old answer, got %q", first.Answer.AnswerText)
			}
			if refreshed.Answer.AnswerText != "新答案" || cachedAfterRefresh.Answer.AnswerText != "新答案" {
				t.Fatalf("expected skipCache response and later cache hit to use new answer, got refreshed=%q later=%q", refreshed.Answer.AnswerText, cachedAfterRefresh.Answer.AnswerText)
			}
			if upstreamCalls != 2 {
				t.Fatalf("expected skipCache request to refresh cache with one new upstream call, got %d calls", upstreamCalls)
			}
		})
}

func performSearchRequest(t *testing.T, target string, body string, authorization string) model.SearchResponse {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", authorization)
	ctx.Request = request

	Search(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response model.SearchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal search response: %v", err)
	}
	return response
}
