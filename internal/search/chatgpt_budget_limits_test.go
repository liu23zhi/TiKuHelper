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

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAIConfiguredBudgetsAreNotClamped(t *testing.T) {
	client := &ChatGptClient{MaxAISearchModelCalls: 1000, MaxAISearches: 700, MaxAIWebReads: 600, MaxAISearchRetries: 80,
		ContextCompression: AIContextCompressionConfig{MaxAttempts: 40}}
	models, searches, reads := client.aiSearchBudgets()
	if models != 1000 || searches != 700 || reads != 600 || client.aiSearchRetryAttempts() != 80 || client.newAIContextCompactor(nil, nil).config.MaxAttempts != 40 {
		t.Fatalf("configured budgets were silently clamped: %d/%d/%d retries=%d compression=%d", models, searches, reads, client.aiSearchRetryAttempts(), client.newAIContextCompactor(nil, nil).config.MaxAttempts)
	}
}

func TestAIResearchUsesConfiguredCountsBeyondTwenty(t *testing.T) {
	for _, route := range []string{"native", "MCP"} {
		for _, toolName := range []string{"web_search", "web_read"} {
			t.Run(route+"/"+toolName, func(t *testing.T) {
				tool := &webReadTestTool{}
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls == 27 {
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
				if err != nil || calls != 27 || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
					t.Fatalf("configured rounds did not run: calls=%d answers=%v err=%v", calls, answers, err)
				}
				if toolName == "web_read" && len(tool.urls) != 25 {
					t.Fatalf("web reads stopped early: %d", len(tool.urls))
				}
				if toolName == "web_search" && len(tool.queries) != 26 {
					t.Fatalf("searches stopped early: %d", len(tool.queries))
				}
			})
		}
	}
}

func TestAIToolResultLimitDoesNotConsumeLaterCallBudgets(t *testing.T) {
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
			client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: remote, MaxAttempts: 8}}
			if _, err := client.newAIContextCompactor(anchors, &AISearchUsage{}).prepare(t.Context(), server.URL+"/responses", input, true, 2); err != nil || calls != 8 {
				t.Fatalf("compression attempts clamped: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAIModelRetriesBeyondTen(t *testing.T) {
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
	if err != nil || calls != 12 || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
		t.Fatalf("retry budget clamped: calls=%d answers=%v err=%v", calls, answers, err)
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
