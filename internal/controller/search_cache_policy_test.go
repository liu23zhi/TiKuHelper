package controller

import (
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/itihey/tikuAdapter/pkg/model"
)

// searchCacheHarness 用同一用户、可变 URL 跑两次请求，返回上游调用次数与结束时的缓存条目数。
//
// 注意 harness 的渠道启用判定走真实逻辑（shouldEnableNormalSearchProvider）：
// 非 local 的渠道必须**在 Tokens 里带 token**（且名字属于 supportedSearchAccountProviders）
// 才会被调用，否则该次请求根本不会打上游。
//
// 缓存条目数必须在 stub 作用域内取：withStubSearchDependencies 收尾会清空缓存。
func searchCacheHarness(t *testing.T, first string, second string, upstream *int) {
	t.Helper()

	withStubSearchDependencies(t,
		(&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:    searchModeNormal,
				Tokens:        map[string]string{"icodef": "test-token", "wanneng": "test-token"},
				AccessTokenID: 101,
				UserID:        201,
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					*upstream++
					return [][]string{{"缓存答案"}}, nil
				},
			}
		},
		nil,
		func() {
			performSearchRequest(t, first, `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			performSearchRequest(t, second, `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
		})
}

// 不带 use 时 harness 的渠道启用判定会跳过全部第三方渠道（EnabledSearchProviders 为空、
// AI 配置不可用），因此这类请求根本没有渠道可查 —— 空结果**不得**进缓存，
// 否则会把「本次没查」固化成「这道题没答案」。
func TestSearchDoesNotCacheEmptyResultWithoutEnabledProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := 0
	searchCacheHarness(t,
		"/adapter-service/search?noRecord=1",
		"/adapter-service/search?noRecord=1",
		&upstream)

	if upstream != 0 {
		t.Fatalf("expected no provider to be enabled without use, got %d upstream calls", upstream)
	}
	if size := searchResultCache.size(); size != 0 {
		t.Fatalf("expected the empty result not to be cached, got %d entries", size)
	}
}

// 显式指定渠道也必须能用缓存（use=icodef）。
func TestSearchCachesRequestWithProviderUse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := 0
	searchCacheHarness(t,
		"/adapter-service/search?use=icodef&noRecord=1",
		"/adapter-service/search?use=icodef&noRecord=1",
		&upstream)

	if upstream != 1 {
		t.Fatalf("expected use=icodef to be cacheable, got %d upstream calls", upstream)
	}
}

// 回归：不同渠道**不得**共用缓存。
//
// 历史缺陷：缓存键不含 use，use=icodef 的答案会被 use=wanneng 的请求直接复用，
// 用户以为在查 A 渠道，拿到的其实是 B 渠道的答案，且不再发起他指定的那次上游调用。
//
// 两个渠道都必须在 Tokens 里带 token（见 supportedSearchAccountProviders），
// 否则该次请求会被真实的渠道启用判定跳过、根本不打上游。
func TestSearchDoesNotReuseCacheAcrossDifferentUse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := 0
	searchCacheHarness(t,
		"/adapter-service/search?use=icodef&noRecord=1",
		"/adapter-service/search?use=wanneng&noRecord=1",
		&upstream)

	// 上游被调用两次 == 第二次没有命中第一次的缓存，即两个渠道各自成键。
	if upstream != 2 {
		t.Fatalf("expected different providers to use separate cache entries, got %d upstream calls", upstream)
	}
}

// use=local 被整体排除在缓存之外（本地题库必须即时反映后台编辑）。
//
// 走完整 HTTP 路径时 use=local 会先被「本地题库未启用」拦下（这也是正确行为），
// 因此这里直接断言缓存资格判定。
func TestCanUseSearchResultCacheExcludesLocalAndAnonymous(t *testing.T) {
	resolved := resolvedSearchRequestContext{SearchMode: searchModeNormal, UserID: 201}

	if !canUseSearchResultCache(resolved, "icodef") {
		t.Fatal("expected use=icodef to be cacheable")
	}
	if !canUseSearchResultCache(resolved, "") {
		t.Fatal("expected an empty use to be cacheable")
	}
	if !canUseSearchResultCache(resolved, "ai") {
		t.Fatal("expected use=ai to be cacheable")
	}
	if canUseSearchResultCache(resolved, "local") {
		t.Fatal("expected use=local to be excluded from the cache")
	}
	if canUseSearchResultCache(resolved, "local,icodef") {
		t.Fatal("expected a use containing local to be excluded from the cache")
	}

	anonymous := resolvedSearchRequestContext{SearchMode: searchModeNormal, UserID: 0}
	if canUseSearchResultCache(anonymous, "icodef") {
		t.Fatal("expected anonymous requests to be excluded from the cache")
	}
}

// 回归：不同题型**不得**共用缓存。
//
// 题型会改变最终答案（例如 type=3 判断题与 type=0 单选题的判分与输出都不同）。
func TestSearchDoesNotReuseCacheAcrossDifferentType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := 0
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
					upstream++
					return [][]string{{"缓存答案"}}, nil
				},
			}
		},
		nil,
		func() {
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"],"type":0}`, "Basic token-a")
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"],"type":3}`, "Basic token-a")
		})

	if upstream != 2 {
		t.Fatalf("expected different question types to use separate cache entries, got %d upstream calls", upstream)
	}
}

// use 的书写顺序无语义：icodef,ai 与 ai,icodef 必须视为同一个键。
func TestSearchUseKeyIsOrderInsensitive(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := 0
	searchCacheHarness(t,
		"/adapter-service/search?use=icodef,wanneng&noRecord=1",
		"/adapter-service/search?use=wanneng,icodef&noRecord=1",
		&upstream)

	if upstream != 1 {
		t.Fatalf("expected use order to be normalized away, got %d upstream calls", upstream)
	}
}

// use 里重复项、空段必须归一化掉。
func TestNormalizeSearchUseKeyCollapsesDuplicatesAndBlanks(t *testing.T) {
	testCases := []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: "   ", want: ""},
		{input: "icodef", want: "icodef"},
		{input: " icodef ", want: "icodef"},
		{input: "icodef,icodef", want: "icodef"},
		{input: "icodef,,zhihuishu", want: "icodef,zhihuishu"},
		{input: "zhihuishu,icodef", want: "icodef,zhihuishu"},
		{input: ",,", want: ""},
	}

	for _, testCase := range testCases {
		if got := normalizeSearchUseKey(testCase.input); got != testCase.want {
			t.Fatalf("normalizeSearchUseKey(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

// 题面仅首尾空白不同时视为同一道题（键归一化 TrimSpace）。
func TestSearchCacheKeyIgnoresSurroundingWhitespace(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := 0
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
					upstream++
					return [][]string{{"缓存答案"}}, nil
				},
			}
		},
		nil,
		func() {
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"  同一道题  ","options":["A","B"]}`, "Basic token-a")
		})

	if upstream != 1 {
		t.Fatalf("expected surrounding whitespace to be ignored in the cache key, got %d upstream calls", upstream)
	}
}

// skipCache=false 与缺省一样正常使用缓存；只有显式 true 才跳过读取。
func TestSkipCacheFalseStillUsesCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := 0
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
					upstream++
					return [][]string{{"缓存答案"}}, nil
				},
			}
		},
		nil,
		func() {
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			performSearchRequest(t, "/adapter-service/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"],"skipCache":false}`, "Basic token-a")
		})

	if upstream != 1 {
		t.Fatalf("expected skipCache=false to still use the cache, got %d upstream calls", upstream)
	}
}
