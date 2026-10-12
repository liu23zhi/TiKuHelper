package uapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const searchEnginesRaw = `{"engine":{"name":"uapi-search","display_name":"UAPI Pro Search","description":"搜索引擎","available":true,"version":"2.0","features":["site","filetype"]},"limits":{"default":10,"max":100},"supported_parameters":["query","site"],"future_field":{"kept":true}}`
const searchAggregateRaw = `{"query":"Go版本","total_results":1,"results":[{"title":"Go发布","url":"https://go.dev/blog/go1.26","snippet":"Go 1.26发布","domain":"go.dev","source":"uapi-searchv1","position":1,"score":0.93,"publish_time":"2026-02-10T00:00:00Z","future_content":"原文"}],"sources":[{"name":"uapi-searchv1","status":"success","result_count":1,"elapsed_ms":3675,"first_result_host":"go.dev"}],"process_time_ms":3675,"metadata":{"request_params":{"query":"Go版本","limit":10,"page":1,"timeout_ms":60000,"sort":"relevance"},"dedupe_removed":0,"rerank_applied":true,"content_fetched":0}}`

func TestSearchDefaultAddresses(t *testing.T) {
	if DefaultSearchEnginesURL != "https://uapis.cn/api/v1/search/engines" || DefaultSearchAggregateURL != "https://uapis.cn/api/v1/search/aggregate" {
		t.Fatal("搜索地址必须保留完整 /api/v1 路径")
	}
}

func TestGetSearchEnginesSuccess(t *testing.T) {
	server, requests := newServer(t, staticJSON(http.StatusOK, searchEnginesRaw))
	client := NewClient(testAPIKey).SetSearchEnginesURL(server.URL + "/api/v1/search/engines")
	result, err := client.GetSearchEngines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := requests.last(t)
	if request.method != http.MethodGet || request.path != "/api/v1/search/engines" || len(request.query) != 0 {
		t.Fatalf("错误的请求: %+v", request)
	}
	if request.header.Get("Authorization") != "Bearer "+testAPIKey || strings.Contains(request.rawURL, testAPIKey) {
		t.Fatal("密钥只能放入 Bearer 请求头")
	}
	if result.Engine.Name != "uapi-search" || result.Engine.DisplayName != "UAPI Pro Search" || result.Engine.Description != "搜索引擎" || !result.Engine.Available || result.Engine.Version != "2.0" || !reflect.DeepEqual(result.Engine.Features, []string{"site", "filetype"}) || result.Limits.Default != 10 || result.Limits.Max != 100 || !reflect.DeepEqual(result.SupportedParameters, []string{"query", "site"}) || result.RawJSON != searchEnginesRaw {
		t.Fatalf("配置未完整解析: %+v", result)
	}
}

func TestSearchAggregateSuccess(t *testing.T) {
	var body map[string]interface{}
	server, requests := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		staticJSON(http.StatusOK, searchAggregateRaw)(w, r)
	})
	client := NewClient(testAPIKey).SetSearchAggregateURL(server.URL + "/api/v1/search/aggregate")
	result, err := client.SearchAggregate(context.Background(), SearchAggregateRequest{Query: " Go版本 ", Site: " go.dev ", FileType: " pdf ", FetchFull: true, Sort: " date ", TimeRange: " week "})
	if err != nil {
		t.Fatal(err)
	}
	request := requests.last(t)
	if request.method != http.MethodPost || request.path != "/api/v1/search/aggregate" || len(request.query) != 0 || !strings.HasPrefix(request.header.Get("Content-Type"), "application/json") || request.header.Get("Authorization") != "Bearer "+testAPIKey {
		t.Fatalf("错误的请求: %+v", request)
	}
	want := map[string]interface{}{"query": "Go版本", "site": "go.dev", "filetype": "pdf", "fetch_full": true, "sort": "date", "time_range": "week"}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("请求体 = %v，期望 %v", body, want)
	}
	if result.Query != "Go版本" || result.TotalResults != 1 || len(result.Results) != 1 || result.Results[0].URL != "https://go.dev/blog/go1.26" || result.Results[0].PublishTime != "2026-02-10T00:00:00Z" || result.Results[0].Score != 0.93 || len(result.Sources) != 1 || result.Sources[0].ElapsedMS != 3675 || result.ProcessTimeMS != 3675 || result.Metadata.RequestParams.TimeoutMS != 60000 || !result.Metadata.RerankApplied || result.RawJSON != searchAggregateRaw {
		t.Fatalf("搜索结果未完整解析: %+v", result)
	}
}

func TestSearchAggregateDoesNotInventRequestParameters(t *testing.T) {
	var body map[string]interface{}
	server, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		staticJSON(http.StatusOK, searchAggregateRaw)(w, r)
	})
	_, err := NewClient(testAPIKey).SetSearchAggregateURL(server.URL).SearchAggregate(nil, SearchAggregateRequest{Query: "Go版本"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body, map[string]interface{}{"query": "Go版本"}) {
		t.Fatalf("最小请求体 = %v", body)
	}
}

func TestSearchRejectsInvalidParametersWithoutRequest(t *testing.T) {
	server, requests := newServer(t, staticJSON(http.StatusOK, searchAggregateRaw))
	client := NewClient(testAPIKey).SetSearchAggregateURL(server.URL)
	for _, input := range []SearchAggregateRequest{{}, {Query: "\t "}, {Query: "query", Site: "site:go.dev"}, {Query: "query", FileType: "exe"}, {Query: "query", Sort: "newest"}, {Query: "query", TimeRange: "all"}} {
		if _, err := client.SearchAggregate(context.Background(), input); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("参数 %+v 应在本地拒绝: %v", input, err)
		}
	}
	if requests.count() != 0 {
		t.Fatalf("非法参数外呼了 %d 次", requests.count())
	}
}

func TestSearchRejectsInvalidAPIKeysWithoutRequest(t *testing.T) {
	server, requests := newServer(t, staticJSON(http.StatusOK, searchEnginesRaw))
	for _, key := range []string{"", "sk-key", "uapi-", "uapi-key\nheader"} {
		client := NewClient(key).SetSearchEnginesURL(server.URL).SetSearchAggregateURL(server.URL)
		if _, err := client.GetSearchEngines(context.Background()); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("非法密钥应本地拒绝: %v", err)
		}
		if _, err := client.SearchAggregate(context.Background(), SearchAggregateRequest{Query: "query"}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("非法密钥应本地拒绝: %v", err)
		}
	}
	if requests.count() != 0 {
		t.Fatalf("非法密钥外呼了 %d 次", requests.count())
	}
}

func TestSearchAggregateStatusMappingAndRetry(t *testing.T) {
	for _, test := range []struct {
		status   int
		code     string
		kind     error
		attempts int
	}{{400, "INVALID_REQUEST", ErrInvalidArgument, 1}, {401, "UNAUTHORIZED", ErrUnauthorized, 1}, {429, "RATE_LIMIT_EXCEEDED", ErrRateLimited, 4}, {500, "INTERNAL_ERROR", ErrUpstream, 4}} {
		t.Run(test.code, func(t *testing.T) {
			server, requests := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "2")
				w.Header().Set("X-Request-ID", "search-request")
				staticJSON(test.status, `{"code":"`+test.code+`","message":"上游失败"}`)(w, r)
			})
			client := NewClient(testAPIKey).SetSearchAggregateURL(server.URL)
			delays := &sleepRecorder{}
			client.sleep = delays.hook
			_, err := client.SearchAggregate(context.Background(), SearchAggregateRequest{Query: "query"})
			var upstream *APIError
			if !errors.Is(err, test.kind) || !errors.As(err, &upstream) || upstream.Code != test.code || upstream.RequestID != "search-request" || upstream.RetryAfter != 2*time.Second || requests.count() != test.attempts {
				t.Fatalf("响应/次数错误: err=%v requests=%d", err, requests.count())
			}
			if test.status == http.StatusTooManyRequests {
				assertDelays(t, delays.recorded(), 2*time.Second, 2*time.Second, 2*time.Second)
			}
		})
	}
}

func TestSearchRejectsMalformedSuccess(t *testing.T) {
	for _, body := range []string{"null", "{}", "[]", "not-json"} {
		t.Run(body, func(t *testing.T) {
			server, _ := newServer(t, staticJSON(http.StatusOK, body))
			client := NewClient(testAPIKey).SetSearchEnginesURL(server.URL).SetSearchAggregateURL(server.URL)
			if _, err := client.GetSearchEngines(nil); !errors.Is(err, ErrUpstream) {
				t.Fatalf("无效引擎配置应失败: %v", err)
			}
			if _, err := client.SearchAggregate(nil, SearchAggregateRequest{Query: "query"}); !errors.Is(err, ErrUpstream) {
				t.Fatalf("无效搜索结果应失败: %v", err)
			}
		})
	}
}

func TestSearchAggregateHonorsTotalDeadlineDuringRateLimit(t *testing.T) {
	server, requests := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		staticJSON(http.StatusTooManyRequests, `{"code":"RATE_LIMIT_EXCEEDED","message":"请稍后"}`)(w, r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := NewClient(testAPIKey).SetSearchAggregateURL(server.URL).SearchAggregate(ctx, SearchAggregateRequest{Query: "query"})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTimeout) || requests.count() != 1 || time.Since(started) > time.Second {
		t.Fatalf("总时限未生效: err=%v requests=%d elapsed=%v", err, requests.count(), time.Since(started))
	}
}

func TestSearchAggregateCallerCancellationDoesNotRetry(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := NewClient(testAPIKey).SetSearchAggregateURL(server.URL).SearchAggregate(ctx, SearchAggregateRequest{Query: "query"})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTimeout) || requests.Load() != 1 {
		t.Fatalf("调用方取消未终止外呼: err=%v requests=%d", err, requests.Load())
	}
}

func TestSearchAggregateNetworkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, searchAggregateRaw) }))
	server.Close()
	_, err := NewClient(testAPIKey).SetSearchAggregateURL(server.URL).SearchAggregate(nil, SearchAggregateRequest{Query: "query"})
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("网络错误未分类: %v", err)
	}
}
