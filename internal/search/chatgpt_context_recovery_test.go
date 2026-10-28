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

	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAIContextLengthFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		detail string
		want   bool
	}{
		{"code", 400, `{"error":{"code":"context_length_exceeded"}}`, true},
		{"anthropic", 400, "prompt is too long: 210000 tokens > 200000 maximum", true},
		{"gemini", 400, "The input token count exceeds the maximum number of tokens allowed", true},
		{"stream", 200, "maximum context length is 8192 tokens", true},
		{"proxy", 413, "context window exceeded", true},
		{"validation", 422, "input is too long for this model", true},
		{"auth", 401, "context_length_exceeded", false},
		{"rate", 429, "token rate limit exceeded", false},
		{"quota", 400, "insufficient_quota", false},
		{"payload", 413, "request entity too large", false},
		{"output", 400, "max_output_tokens exceeds model output limit", false},
		{"provider", 500, "context window unavailable", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isAIContextLengthFailure(test.status, fmt.Errorf("%s", test.detail)); got != test.want {
				t.Fatalf("classification=%v want=%v: %s", got, test.want, test.detail)
			}
		})
	}
	if isAIContextLengthFailure(400, nil) {
		t.Fatal("a successful response cannot be a context error")
	}
}

type contextRecoverySearchTool struct {
	calls int
}

func (tool *contextRecoverySearchTool) SearchAggregate(_ context.Context, req uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	tool.calls++
	response := uapi.SearchAggregateResponse{Query: req.Query, TotalResults: 5}
	for index := 0; index < 5; index++ {
		response.Results = append(response.Results, uapi.SearchResult{
			Title: "Evidence", URL: fmt.Sprintf("https://example.com/reference/%d", index),
			Snippet: strings.Repeat("这是用于模型摘要的历史证据。", 60),
		})
	}
	return response, nil
}

func TestAnswerContextRecoveryRestoresAnchorsAndBillsSummaryWithoutRepeatingTools(t *testing.T) {
	for _, route := range []string{"native", "MCP"} {
		for _, overflowAgain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/repeated-overflow=%v", route, overflowAgain), func(t *testing.T) {
				answerCalls, summaryCalls := 0, 0
				var originalAnchors []any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					if payload["max_output_tokens"] != nil {
						summaryCalls++
						writeCompactionTestSummary(w, "搜索证据已核验，继续完成全部小问。", "completed")
						return
					}
					answerCalls++
					input := payload["input"].([]any)
					if answerCalls == 1 {
						originalAnchors = append([]any(nil), input[:2]...)
						if route == "native" {
							writeAIToolStream(w, "context-search", "关键事实")
						} else {
							writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"关键事实"}}`, 10)
						}
						return
					}
					if answerCalls == 2 || overflowAgain {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"maximum context length exceeded"}}`)
						return
					}
					// The current budget follows the exact system/user anchors.
					if !reflect.DeepEqual(input[len(input)-3:len(input)-1], originalAnchors) {
						t.Error("compression recovery lost the original rules, question or options")
					}
					encoded, _ := json.Marshal(input)
					if !strings.Contains(string(encoded), "不可信历史摘要") || !strings.Contains(string(encoded), "example.com/reference") {
						t.Error("recovered answer lost the model summary or source provenance")
					}
					writeAIFinalStream(w)
				}))
				defer server.Close()
				tool := &contextRecoverySearchTool{}
				client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", Prompt: "PROTECTED output rules", SearchTool: tool,
					MaxAISearchModelCalls: 2, MaxAISearchRetries: 3, ContextCompression: AIContextCompressionConfig{Enabled: true, MaxAttempts: 3}}
				req := model.SearchRequest{Question: "请回答全部问题，保留题目", Type: 2, Options: []string{"原始选项"}}
				var answers [][]string
				var debug AISearchDebugResult
				var err error
				if route == "native" {
					answers, debug, err = client.searchAnswerWithToolMode(t.Context(), req, "")
				} else {
					answers, debug, err = client.searchAnswerWithMCP(t.Context(), req)
				}
				if answerCalls != 3 || summaryCalls != 1 || tool.calls != 1 {
					t.Fatalf("recovery multiplied requests or repeated a tool: answer=%d summary=%d tools=%d err=%v", answerCalls, summaryCalls, tool.calls, err)
				}
				if overflowAgain {
					if err == nil || len(debug.Usage.ModelCalls) != 2 || debug.Usage.TotalTokens != 134 {
						t.Fatalf("repeated overflow must stop and retain billed calls: err=%v usage=%+v", err, debug.Usage)
					}
				} else if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) || len(debug.Usage.ModelCalls) != 3 || debug.Usage.TotalTokens != 157 {
					t.Fatalf("recovery lost final answer or summary billing: answers=%v err=%v usage=%+v", answers, err, debug.Usage)
				}
			})
		}
	}
}

func TestAnswerContextRecoveryRetainsFailedStreamUsage(t *testing.T) {
	for _, route := range []string{"native", "MCP"} {
		t.Run(route, func(t *testing.T) {
			answerCalls, summaryCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if payload["max_output_tokens"] != nil {
					summaryCalls++
					writeCompactionTestSummary(w, "Preserve the verified search evidence and finish the answer.", "completed")
					return
				}
				answerCalls++
				switch answerCalls {
				case 1:
					if route == "native" {
						writeAIToolStream(w, "failed-stream-search", "verified facts")
					} else {
						writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"verified facts"}}`, 10)
					}
				case 2:
					w.Header().Set("Content-Type", "text/event-stream")
					writeResponsesEvent(w, "response.failed", map[string]any{"response": map[string]any{
						"status": "failed",
						"error":  map[string]any{"code": "context_length_exceeded", "message": "maximum context length exceeded"},
						"usage": map[string]any{
							"input_tokens": 30, "output_tokens": 2, "total_tokens": 32,
							"input_tokens_details": map[string]any{"cached_tokens": 3},
						},
					}})
				default:
					writeAIFinalStream(w)
				}
			}))
			defer server.Close()
			tool := &contextRecoverySearchTool{}
			client := &ChatGptClient{
				Enable: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", SearchTool: tool,
				MaxAISearchModelCalls: 2, MaxAISearchRetries: 3,
				ContextCompression: AIContextCompressionConfig{Enabled: true, MaxAttempts: 3},
			}
			req := model.SearchRequest{Question: "Answer using verified evidence.", Type: 2}
			var answers [][]string
			var debug AISearchDebugResult
			var err error
			if route == "native" {
				answers, debug, err = client.searchAnswerWithToolMode(t.Context(), req, "")
			} else {
				answers, debug, err = client.searchAnswerWithMCP(t.Context(), req)
			}
			if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
				t.Fatalf("failed HTTP 200 stream must recover to a final answer: answers=%v err=%v", answers, err)
			}
			if answerCalls != 3 || summaryCalls != 1 || tool.calls != 1 {
				t.Fatalf("recovery must not repeat successful tools: answer=%d summary=%d tools=%d", answerCalls, summaryCalls, tool.calls)
			}
			if debug.Usage.InputTokens != 160 || debug.Usage.OutputTokens != 29 || debug.Usage.TotalTokens != 189 || len(debug.Usage.ModelCalls) != 4 {
				t.Fatalf("all four model calls, including the failed stream, must be billed once: input=%d output=%d total=%d calls=%+v", debug.Usage.InputTokens, debug.Usage.OutputTokens, debug.Usage.TotalTokens, debug.Usage.ModelCalls)
			}
			failed := debug.Usage.ModelCalls[1]
			if failed.InputTokens != 30 || failed.OutputTokens != 2 || failed.TotalTokens != 32 || failed.CachedInputTokens != 3 {
				t.Fatalf("failed model call usage was lost or changed: %+v", failed)
			}
		})
	}
}
