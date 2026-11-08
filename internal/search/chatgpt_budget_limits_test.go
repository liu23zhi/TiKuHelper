package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAIConfiguredBudgetsPreserveLocalCaps(t *testing.T) {
	client := &ChatGptClient{MaxAISearchModelCalls: 1000, MaxAISearches: 700, MaxAIWebReads: 600, MaxAISearchRetries: 80,
		ContextCompression: AIContextCompressionConfig{MaxAttempts: 40}}
	models, searches, reads := client.aiSearchBudgets()
	if models != 20 || searches != 20 || reads != 20 || client.aiSearchRetryAttempts() != 10 || client.newAIContextCompactor(nil, nil).config.MaxAttempts != 40 {
		t.Fatalf("configured budgets escaped local caps: %d/%d/%d retries=%d compression=%d", models, searches, reads, client.aiSearchRetryAttempts(), client.newAIContextCompactor(nil, nil).config.MaxAttempts)
	}
}

func TestAIResearchCapsConfiguredCountsBeyondTwenty(t *testing.T) {
	for _, route := range []string{"native", "MCP"} {
		for _, toolName := range []string{"web_search", "web_read"} {
			t.Run(route+"/"+toolName, func(t *testing.T) {
				tool := &webReadTestTool{}
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls == 20 {
						writeAIFinalStream(w)
						return
					}
					name := toolName
					if calls == 1 {
						name = "web_search"
					}
					query := fmt.Sprintf("query %d", calls)
					address := fmt.Sprintf("https://example.com/page/%d", calls)
					if route == "native" {
						if name == "web_search" {
							writeAIToolStream(w, fmt.Sprintf("call-%d", calls), query)
						} else {
							writeAIWebReadToolStream(w, fmt.Sprintf("call-%d", calls), address)
						}
					} else if name == "web_search" {
						encoded, _ := json.Marshal(map[string]any{"action": name, "arguments": map[string]string{"query": query}})
						writeMCPTextStream(w, string(encoded), 10)
					} else {
						encoded, _ := json.Marshal(map[string]any{"action": name, "arguments": map[string]string{"url": address}})
						writeMCPTextStream(w, string(encoded), 10)
					}
				}))
				defer server.Close()
				client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", SearchTool: tool,
					MaxAISearchModelCalls: 27, MaxAISearches: 26, MaxAIWebReads: 25}
				var answers [][]string
				var err error
				if route == "native" {
					answers, _, err = client.searchAnswerWithTools(t.Context(), model.SearchRequest{Question: "完整作答", Type: 2})
				} else {
					answers, _, err = client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "完整作答", Type: 2})
				}
				if err != nil || calls != 20 || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
					t.Fatalf("configured rounds did not run: calls=%d answers=%v err=%v", calls, answers, err)
				}
				if toolName == "web_read" && len(tool.urls) != 18 {
					t.Fatalf("web reads stopped early: %d", len(tool.urls))
				}
				if toolName == "web_search" && len(tool.queries) != 19 {
					t.Fatalf("searches stopped early: %d", len(tool.queries))
				}
			})
		}
	}
}

func TestAIToolNilCounterKeepsSingleResponseCompatibility(t *testing.T) {
	tool := &webReadTestTool{}
	client := &ChatGptClient{SearchTool: tool}
	used := 0
	usage := AISearchUsage{}
	search := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: "web_search", Arguments: `{"query":"new query"}`}}
	if body, abort := client.executeAISearchTool(t.Context(), search, &used, 30, map[string]string{}, nil, &usage, 1, 40); abort || !strings.Contains(body, "example.com") {
		t.Fatalf("old evidence prevented a new search: %s abort=%v", body, abort)
	}
	read := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: "web_read", Arguments: `{"url":"https://example.com/new"}`}}
	if body, abort := client.executeAIWebReadTool(t.Context(), read, &used, 30, map[string]string{}, nil, &usage, 2, 40, 30); abort || !strings.Contains(body, "网页正文") {
		t.Fatalf("old evidence prevented a new page read: %s abort=%v", body, abort)
	}
}

func TestAICompactionHonorsAttemptsBeyondFive(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote=%v", remote), func(t *testing.T) {
			anchors, input := compactionTestInput()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls < 8 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = fmt.Fprint(w, `{"error":"rate limited"}`)
					return
				}
				if remote {
					_, _ = fmt.Fprint(w, `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}]}`)
				} else {
					writeCompactionTestSummary(w, "Retain facts.", "completed")
				}
			}))
			defer server.Close()
			client := &ChatGptClient{MaxAISearchModelCalls: 20, ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: remote, MaxAttempts: 8}}
			if _, err := client.newAIContextCompactor(anchors, &AISearchUsage{}).prepare(t.Context(), server.URL+"/responses", input, true, 2); err != nil || calls != 8 {
				t.Fatalf("compression attempts clamped: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAIModelRetriesStopAtLocalTenLimit(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 12 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", MaxAISearchRetries: 12}
	answers, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", Type: 2})
	if err == nil || calls != 10 || len(answers) != 0 {
		t.Fatalf("retry budget escaped local ten-limit: calls=%d answers=%v err=%v", calls, answers, err)
	}
}

func TestAIModelRequestContextCancellationStopsWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := &ChatGptClient{Enable: true, Context: ctx, BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Model: "test", MaxAISearchRetries: 1000}
	_, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "cancelled", Type: 2})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("caller cancellation was ignored: %v", err)
	}
}

func TestAIRetryDelaySaturatesWithoutOverflow(t *testing.T) {
	for _, attempt := range []int{100, 1000, int(^uint(0) >> 1)} {
		delay := aiSearchRetryDelay(attempt, nil)
		if delay < 0 || delay > aiSearchRetryMaxDelay {
			t.Fatalf("attempt=%d delay overflowed: %v", attempt, delay)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if sleepAISearchRetryBackoff(ctx, 1000, nil) {
		t.Fatal("retry must not outlive the request context")
	}
}

func TestLocalTaskDeadlineAndRetryBudgetRetainCallerCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(t.Context(), "local-policy-key", "retained"))
	ctx, done := withAITotalTimeout(parent)
	defer done()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 179*time.Second || time.Until(deadline) > 180*time.Second || ctx.Value("local-policy-key") != "retained" {
		t.Fatalf("180s total deadline or caller values lost: deadline=%v ok=%v", deadline, ok)
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatalf("caller cancellation lost: %v", ctx.Err())
	}
	expired := context.WithValue(t.Context(), aiSearchRetryStartKey{}, time.Now().Add(-2*time.Minute-time.Second))
	if sleepAISearchRetryBackoff(expired, 1, nil) {
		t.Fatal("retry outlived its approved 2m window")
	}
	if sleepAISearchRetryBackoff(t.Context(), 1, &aiSearchRetryAfterError{err: fmt.Errorf("429"), delay: 3 * time.Minute}) {
		t.Fatal("Retry-After escaped the approved retry window")
	}
}

func TestCompressionSharesOpportunityBudgetAndRetainsFailedUsage(t *testing.T) {
	anchors, input := compactionTestInput()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeCompactionTestSummary(w, "", "completed")
	}))
	defer server.Close()
	client := &ChatGptClient{MaxAISearchModelCalls: 3, ContextCompression: AIContextCompressionConfig{Enabled: true, MaxAttempts: 8}}
	usage := AISearchUsage{}
	manager := client.newAIContextCompactor(anchors, &usage)
	manager.reserveAnswer = true
	manager.answerRounds = 1
	_, err := manager.prepare(t.Context(), server.URL+"/responses", input, true, 2)
	if err == nil || requests != 1 || manager.summaryRequests != 1 || len(usage.ModelCalls) != 1 || usage.TotalTokens == 0 {
		t.Fatalf("summary did not share opportunities/reserve final answer/retain failed usage: requests=%d manager=%+v usage=%+v err=%v", requests, manager, usage, err)
	}
}

func TestLocalSingleResponseResourceCaps(t *testing.T) {
	if aiToolMaxContentBytes != 16*1024 || aiToolMaxStreamBytes != 2*1024*1024 {
		t.Fatalf("unapproved resource expansion: tool=%d stream=%d", aiToolMaxContentBytes, aiToolMaxStreamBytes)
	}
}

type localLargeWebResponseTool struct {
	webReadTestTool
	text string
}

func (tool *localLargeWebResponseTool) ReadWebPage(_ context.Context, address string) (uapi.WebToMarkdownTask, error) {
	tool.urls = append(tool.urls, address)
	return uapi.WebToMarkdownTask{Status: "completed", TaskID: "large-mock", Result: &uapi.WebToMarkdownResult{Markdown: tool.text}}, nil
}

func TestLargeWebReadPreservesUsageWithinSingleResponseHardCap(t *testing.T) {
	raw := strings.Repeat("完整历史资料", 10000)
	tool := &localLargeWebResponseTool{text: raw}
	client := &ChatGptClient{SearchTool: tool}
	call := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: "web_read", Arguments: "{\"url\":\"https://example.com/large\"}"}}
	reads := 0
	usage := AISearchUsage{}
	body, abort := client.executeAIWebReadTool(t.Context(), call, &reads, 20, map[string]string{}, nil, &usage, 1, 20, 20)
	var output map[string]any
	if json.Unmarshal([]byte(body), &output) != nil || len(body) > 16*1024 || abort || output["markdown"] == nil || len(usage.WebReadCalls) != 1 || usage.WebReadCalls[0].MarkdownBytes != len(raw) {
		t.Fatalf("large tool result must remain valid/bounded while preserving actual usage: bytes=%d abort=%v usage=%+v", len(body), abort, usage)
	}
	if len(tool.urls) != 1 || usage.WebReadCalls[0].Status != "success" {
		t.Fatalf("unexpected repeated call or swallowed failure: urls=%v usage=%+v", tool.urls, usage)
	}
}

func TestCumulativeToolBytesRejectBeforeProviderExecution(t *testing.T) {
	for _, name := range []string{"web_search", "web_read"} {
		t.Run(name, func(t *testing.T) {
			tool := &localLargeWebResponseTool{text: "fixture"}
			client := &ChatGptClient{SearchTool: tool}
			count, used := 0, 16*1024
			usage := AISearchUsage{}
			call := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: name}}
			var body string
			var abort bool
			if name == "web_read" {
				call.Function.Arguments = "{\"url\":\"https://example.com/cumulative\"}"
				body, abort = client.executeAIWebReadTool(t.Context(), call, &count, 20, map[string]string{}, &used, &usage, 1, 20, 20)
			} else {
				call.Function.Arguments = "{\"query\":\"fixture\"}"
				body, abort = client.executeAISearchTool(t.Context(), call, &count, 20, map[string]string{}, &used, &usage, 1, 20)
			}
			if !abort || count != 0 || len(tool.urls) != 0 || len(tool.queries) != 0 || len(usage.SearchCalls) != 0 || len(usage.WebReadCalls) != 0 || !strings.Contains(body, "预算") {
				t.Fatalf("exhausted cumulative budget launched work or swallowed stop signal: count=%d body=%s usage=%+v", count, body, usage)
			}
		})
	}
}
func TestWebReadRespectsRemainingCumulativeToolBytes(t *testing.T) {
	tool := &localLargeWebResponseTool{text: strings.Repeat("完整历史资料", 10000)}
	client := &ChatGptClient{SearchTool: tool}
	count, used := 0, 16*1024-1024
	usage := AISearchUsage{}
	call := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: "web_read", Arguments: "{\"url\":\"https://example.com/remaining\"}"}}
	body, abort := client.executeAIWebReadTool(t.Context(), call, &count, 20, map[string]string{}, &used, &usage, 1, 20, 20)
	if abort || len(body) > 1024 || used > 16*1024 || count != 1 || len(usage.WebReadCalls) != 1 {
		t.Fatalf("single response ignored remaining cumulative budget: bytes=%d used=%d count=%d abort=%v", len(body), used, count, abort)
	}
}
