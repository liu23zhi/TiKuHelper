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
		"/api/search?noRecord=1",
		"/api/search?noRecord=1",
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
		"/api/search?use=icodef&noRecord=1",
		"/api/search?use=icodef&noRecord=1",
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
		"/api/search?use=icodef&noRecord=1",
		"/api/search?use=wanneng&noRecord=1",
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
			performSearchRequest(t, "/api/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"],"type":0}`, "Basic token-a")
			performSearchRequest(t, "/api/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"],"type":3}`, "Basic token-a")
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
		"/api/search?use=icodef,wanneng&noRecord=1",
		"/api/search?use=wanneng,icodef&noRecord=1",
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
			performSearchRequest(t, "/api/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			performSearchRequest(t, "/api/search?use=icodef&noRecord=1", `{"question":"  同一道题  ","options":["A","B"]}`, "Basic token-a")
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
			performSearchRequest(t, "/api/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"]}`, "Basic token-a")
			performSearchRequest(t, "/api/search?use=icodef&noRecord=1", `{"question":"同一道题","options":["A","B"],"skipCache":false}`, "Basic token-a")
		})

	if upstream != 1 {
		t.Fatalf("expected skipCache=false to still use the cache, got %d upstream calls", upstream)
	}
}

// ---- 缓存键归一化：线上实测的「同题不同标点」必须同键 ----
//
// 背景（线上事故）：同一用户 64 秒内对同一道填空题请求 3 次，被计费 3 次
// （billing_records id 830/831/832，共 17134 tokens）。
// 三次的**题面完全相同**，差异只在选项里的标点：
//   830: `…机器人.我课后…相关内容,及时复习…巩固所学知识`
//   832: `…机器人。我课后…相关内容，及时复习…巩固所学知识。`
// 归一化前这两份算出不同的键，缓存必然不命中，于是每次都真调上游、每次计费。

func cacheKeyForTest(t *testing.T, question string, options []string, questionType int, deepThinking ...bool) string {
	t.Helper()
	deep := len(deepThinking) > 0 && deepThinking[0]
	key, _, ok := buildSearchResultCacheKey(
		resolvedSearchRequestContext{UserID: 1},
		model.SearchRequest{Question: question, Options: options, Type: questionType, DeepThinking: deep},
		"",
	)
	if !ok {
		t.Fatal("expected the cache key to be constructible")
	}
	return key
}

// 选项标点不同（`.` vs `。`、`,` vs `，`、行尾多余标点）必须归一为同一个键。
func TestSearchCacheKeyIgnoresOptionPunctuationDrift(t *testing.T) {
	const question = "请简单列举常见的科技发明。课后是否可以好好的学习相关内容？"
	halfWidth := []string{
		"我列举的常见科技发明有电话、电视、电脑和机器人.我课后可以认真学习相关内容,及时复习",
		"}else if ((max > 0 && count > max) || (min > 0 && min > count)) {",
	}
	fullWidth := []string{
		"我列举的常见科技发明有电话、电视、电脑和机器人。我课后可以认真学习相关内容，及时复习。",
		"}else if ((max > 0 && count > max) || (min > 0 && min > count)) {",
	}

	if got, want := cacheKeyForTest(t, question, fullWidth, 2), cacheKeyForTest(t, question, halfWidth, 2); got != want {
		t.Fatalf("expected punctuation-only option drift to share a cache key, got %s vs %s", got[:16], want[:16])
	}
}

// 题面标点不同同样必须归一为同一个键。
func TestSearchCacheKeyIgnoresQuestionPunctuationDrift(t *testing.T) {
	options := []string{"同一份选项"}
	full := cacheKeyForTest(t, "请简单列举常见的科技发明。课后是否可以好好的学习相关内容？", options, 2)
	half := cacheKeyForTest(t, "请简单列举常见的科技发明.课后是否可以好好的学习相关内容", options, 2)

	if full != half {
		t.Fatalf("expected punctuation-only question drift to share a cache key, got %s vs %s", full[:16], half[:16])
	}
}

// 选项首尾空白不同必须归一为同一个键。
func TestSearchCacheKeyIgnoresOptionWhitespace(t *testing.T) {
	const question = "题干"
	padded := cacheKeyForTest(t, question, []string{"  选项  "}, 2)
	trimmed := cacheKeyForTest(t, question, []string{"选项"}, 2)

	if padded != trimmed {
		t.Fatalf("expected surrounding whitespace to be ignored, got %s vs %s", padded[:16], trimmed[:16])
	}
}

// 归一化绝不能让**真正不同**的题共用缓存。
func TestSearchCacheKeyStillDistinguishesRealDifferences(t *testing.T) {
	const question = "题干"
	base := cacheKeyForTest(t, question, []string{"选项一", "选项二"}, 2)

	others := map[string]string{
		"不同题面":     cacheKeyForTest(t, "另一道完全不同的题目", []string{"选项一", "选项二"}, 2),
		"不同深度思考模式": cacheKeyForTest(t, question, []string{"选项一", "选项二"}, 2, true),
		"不同选项内容":   cacheKeyForTest(t, question, []string{"选项一", "完全不同的选项"}, 2),
		"选项顺序不同":   cacheKeyForTest(t, question, []string{"选项二", "选项一"}, 2),
		"选项数量不同":   cacheKeyForTest(t, question, []string{"选项一"}, 2),
		"不同题型":     cacheKeyForTest(t, question, []string{"选项一", "选项二"}, 0),
		"选项中间标点不同": cacheKeyForTest(t, question, []string{"选项一", "选项,二"}, 2),
	}
	for name, key := range others {
		if key == base {
			t.Fatalf("expected %s to produce a different cache key", name)
		}
	}
}

// 归一化只作用于缓存键：发给上游与写回响应的题目必须是原始值。
func TestCacheKeyNormalizationDoesNotMutateRequest(t *testing.T) {
	original := model.SearchRequest{
		Question: "题目。带全角标点？",
		Options:  []string{"选项。带标点，", "  第二项  "},
		Type:     2,
	}
	snapshotQuestion := original.Question
	snapshotOptions := append([]string(nil), original.Options...)

	_, _, ok := buildSearchResultCacheKey(resolvedSearchRequestContext{UserID: 1}, original, "")
	if !ok {
		t.Fatal("expected the cache key to be constructible")
	}

	if original.Question != snapshotQuestion {
		t.Fatalf("request question was mutated: %q", original.Question)
	}
	if len(original.Options) != len(snapshotOptions) {
		t.Fatalf("request options length changed: %v", original.Options)
	}
	for i := range snapshotOptions {
		if original.Options[i] != snapshotOptions[i] {
			t.Fatalf("request option %d was mutated: %q", i, original.Options[i])
		}
	}
}

// nil 与空切片选项必须算出同一个键（两种「无选项」表示不应分裂缓存）。
func TestSearchCacheKeyTreatsNilAndEmptyOptionsAlike(t *testing.T) {
	const question = "题干"
	nilOptions := cacheKeyForTest(t, question, nil, 0)
	emptyOptions := cacheKeyForTest(t, question, []string{}, 0)

	if nilOptions != emptyOptions {
		t.Fatalf("expected nil and empty options to share a cache key, got %s vs %s", nilOptions[:16], emptyOptions[:16])
	}
}

// ---- 线上事故的端到端回归 ----
//
// 用真实 body 形态重现：同一道填空题、选项仅标点不同，在 64 秒内请求 3 次。
// 修复前 3 次全部未命中缓存、全部真调上游（线上 billing_records 830/831/832
// 各计费一次，共 17134 tokens）；修复后只应调用上游 **1** 次。
func TestSearchReusesCacheAcrossPunctuationDriftEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := 0
	withStubSearchDependencies(t,
		func(model.SearchRequest) ([][]string, error) {
			upstream++
			return [][]string{{"答案"}}, nil
		},
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{
				SearchMode:             searchModeNormal,
				UserID:                 1,
				AccessTokenID:          101,
				Tokens:                 map[string]string{"icodef": "test-token"},
				EnabledSearchProviders: []string{"icodef"},
			}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){
				func(model.SearchRequest) ([][]string, error) {
					upstream++
					return [][]string{{"答案"}}, nil
				},
			}
		},
		nil,
		func() {
			// 与线上一致：题面相同；选项[0] 半角/全角标点不同，选项[1] 为编辑器残留 JS。
			question := "请简单列举常见的科技发明。课后是否可以好好的学习相关内容？"
			halfWidth := `{"question":"` + question + `","options":["我列举的常见科技发明有电话、电视和机器人.我课后可以认真学习相关内容,及时复习","}else if ((max > 0 && count > max) || (min > 0 && min > count)) {"],"type":2}`
			fullWidth := `{"question":"` + question + `","options":["我列举的常见科技发明有电话、电视和机器人。我课后可以认真学习相关内容，及时复习。","}else if ((max > 0 && count > max) || (min > 0 && min > count)) {"],"type":2}`

			target := "/api/search?use=icodef&noRecord=1&recordEmptyAnswer=1"
			performSearchRequest(t, target, halfWidth, "Basic token-a")
			performSearchRequest(t, target, fullWidth, "Basic token-a")
			performSearchRequest(t, target, halfWidth, "Basic token-a")
		})

	if upstream != 1 {
		t.Fatalf("expected exactly 1 upstream call across 3 punctuation-variant requests, got %d", upstream)
	}
}
