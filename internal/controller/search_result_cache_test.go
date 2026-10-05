package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestSearchReusesCachedResultForIdenticalQuestionAndOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(authorization string, _ string, _ url.Values) (resolvedSearchRequestContext, error) {
			accessTokenID := int32(0)
			if authorization == "Basic token-a" {
				accessTokenID = 101
			}
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: accessTokenID,
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

func TestSearchDoesNotReuseCachedResultAcrossAccessTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamCalls := 0
	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(authorization string, _ string, _ url.Values) (resolvedSearchRequestContext, error) {
			accessTokenID := int32(0)
			switch authorization {
			case "Basic token-a":
				accessTokenID = 101
			case "Basic token-b":
				accessTokenID = 202
			}
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token"},
				AccessTokenID: accessTokenID,
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
				t.Fatalf("expected separate upstream calls for different access tokens, got %d", upstreamCalls)
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
			defaultSearchResultCache.now = func() time.Time {
				return now
			}

			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			now = now.Add(defaultSearchResultCacheTTL)
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
