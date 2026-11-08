package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func writeAIMCPOutputStream(w http.ResponseWriter, content string, extra []any, inputTokens int64) {
	w.Header().Set("Content-Type", "text/event-stream")
	output := append(append([]any(nil), extra...), map[string]any{
		"type": "message", "id": "mcp-message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": content}},
	})
	writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{
		"id": "mcp-compaction-test", "status": "completed", "output": output,
		"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 5, "total_tokens": inputTokens + 5},
	}})
}

func TestAIMCPPreservesResponseOutputAndRestoresCompactionAnchors(t *testing.T) {
	for _, scenario := range []struct {
		serverCompaction bool
		deltaOnlyText    bool
	}{{}, {serverCompaction: true}, {serverCompaction: true, deltaOnlyText: true}} {
		t.Run(fmt.Sprintf("server=%t/deltaOnly=%t", scenario.serverCompaction, scenario.deltaOnlyText), func(t *testing.T) {
			serverCompaction := scenario.serverCompaction
			tool := &testAISearchTool{}
			calls := 0
			searchAction := `{"action":"web_search","arguments":{"query":"historical evidence"}}`
			extra := []any{map[string]any{"type": "reasoning", "id": "mcp-reasoning", "encrypted_content": "reasoning-state", "future_field": "preserve"}}
			if serverCompaction {
				extra = append(extra, map[string]any{"type": "compaction", "id": "mcp-compaction", "encrypted_content": "compacted-state", "future_field": []any{"preserve", 7}})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/responses" {
					t.Errorf("server compaction must not call a separate endpoint: %s", r.URL.Path)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if (payload["context_management"] != nil) != serverCompaction {
					t.Errorf("unexpected context_management: %#v", payload["context_management"])
				}
				if calls == 1 {
					if scenario.deltaOnlyText {
						w.Header().Set("Content-Type", "text/event-stream")
						writeResponsesEvent(w, "response.output_text.delta", map[string]any{"output_index": len(extra), "content_index": 0, "delta": searchAction})
						writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{
							"status": "completed", "output": extra,
							"usage": map[string]any{"input_tokens": 100, "output_tokens": 5, "total_tokens": 105},
						}})
					} else {
						writeAIMCPOutputStream(w, searchAction, extra, 100)
					}
					return
				}
				input, _ := payload["input"].([]any)
				for _, expected := range extra {
					found := false
					for _, item := range input {
						found = found || aiContextCanonicalKey(item) == aiContextCanonicalKey(expected)
					}
					if !found {
						t.Errorf("lost canonical output item: %#v", expected)
					}
				}
				assistantMessages := 0
				for _, value := range input {
					item, _ := value.(map[string]any)
					if item["role"] == "assistant" {
						assistantMessages++
					}
				}
				if assistantMessages != 1 {
					t.Errorf("assistant output duplicated or missing: %d", assistantMessages)
				}
				encoded, _ := json.Marshal(input)
				for _, expected := range []string{"original-question-marker", "original-rule-marker", "本地MCP工具 web_search 返回", "https://example.com/source"} {
					if !strings.Contains(string(encoded), expected) {
						t.Errorf("missing original task/tool state %q", expected)
					}
				}
				if serverCompaction && !strings.Contains(string(encoded), aiContextRestoreRules) {
					t.Error("compaction did not reinject original execution rules")
				}
				writeAIMCPOutputStream(w, `{"answers":["4"]}`, nil, 50)
			}))
			defer server.Close()
			client := &ChatGptClient{
				BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model", Prompt: "original-rule-marker",
				SearchTool: tool, MaxAISearchModelCalls: 2, MaxAISearches: 1, MaxAIWebReads: 1, MaxAISearchRetries: 1,
				ContextCompression: AIContextCompressionConfig{Enabled: serverCompaction, StrategyOrder: []string{"server", "remote", "local"}, ContextWindowTokens: 20000},
			}
			answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "original-question-marker", Type: 4})
			if err != nil {
				t.Fatalf("MCP continuation failed: %v", err)
			}
			if calls != 2 || len(tool.queries) != 1 || len(answers) != 1 || answers[0][0] != "4" {
				t.Fatalf("unexpected model/tool execution: calls=%d queries=%v answers=%v", calls, tool.queries, answers)
			}
			if debug.Usage.TotalTokens != 160 || len(debug.Usage.ModelCalls) != 2 {
				t.Fatalf("server compaction charged separately or lost usage: %+v", debug.Usage)
			}
		})
	}
}

func TestAIMCPServerParameterRejectionWithoutHistoryContinues(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if calls == 1 {
			if payload["context_management"] == nil {
				t.Error("initial request did not opt into server compaction")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"message":"Unknown parameter: context_management","code":"unknown_parameter"}}`)
			return
		}
		if payload["context_management"] != nil {
			t.Error("rejected compaction parameter was retried")
		}
		if calls == 2 {
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"4"}}`, 12)
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 14)
	}))
	defer server.Close()
	client := &ChatGptClient{
		BaseURL: server.URL + "/v1", Model: "test-model", SearchTool: tool,
		MaxAISearchModelCalls: 2, MaxAISearches: 1, MaxAIWebReads: 1, MaxAISearchRetries: 1,
		ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "remote", "local"}, ContextWindowTokens: 20000, MaxAttempts: 1},
	}
	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 4})
	if err != nil {
		t.Fatalf("parameter rejection with no history should retry without compaction: %v", err)
	}
	if calls != 3 || len(tool.queries) != 1 || len(answers) != 1 || answers[0][0] != "4" || len(debug.Usage.ModelCalls) != 2 {
		t.Fatalf("unexpected replay/compaction billing: calls=%d queries=%v answers=%v usage=%+v", calls, tool.queries, answers, debug.Usage)
	}
}

func TestAIMCPServerTransientFailureRetriesBeforeFallback(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if calls <= 2 {
			if payload["context_management"] == nil {
				t.Error("server strategy advanced before model retry budget was exhausted")
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"error":{"message":"upstream temporarily unavailable"}}`)
			return
		}
		if payload["context_management"] != nil {
			t.Error("server strategy was not replaced after retries were exhausted")
		}
		if calls == 3 {
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"4"}}`, 12)
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 14)
	}))
	defer server.Close()
	client := &ChatGptClient{
		BaseURL: server.URL + "/v1", Model: "test-model", SearchTool: tool,
		MaxAISearchModelCalls: 2, MaxAISearches: 1, MaxAIWebReads: 1, MaxAISearchRetries: 2,
		ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "remote", "local"}, ContextWindowTokens: 20000, MaxAttempts: 1},
	}
	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 4})
	if err != nil {
		t.Fatalf("transient server failures should retry then fall back: %v", err)
	}
	if calls != 4 || len(tool.queries) != 1 || len(answers) != 1 || answers[0][0] != "4" || len(debug.Usage.ModelCalls) != 2 {
		t.Fatalf("unexpected transient fallback/replay: calls=%d queries=%v answers=%v usage=%+v", calls, tool.queries, answers, debug.Usage)
	}
}

func TestAIMCPServerUnavailableFallsBackWithoutReplayingRejectedPost(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":{"message":"upstream temporarily unavailable"}}`)
			return
		}
		if payload["context_management"] != nil {
			t.Error("non-retryable ordinary POST was replayed before advancing compaction strategy")
		}
		if calls == 2 {
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"4"}}`, 12)
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 14)
	}))
	defer server.Close()
	client := &ChatGptClient{
		BaseURL: server.URL + "/v1", Model: "test-model", SearchTool: tool,
		MaxAISearchModelCalls: 2, MaxAISearches: 1, MaxAIWebReads: 1, MaxAISearchRetries: 3,
		ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "remote", "local"}, ContextWindowTokens: 20000, MaxAttempts: 1},
	}
	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 4})
	if err != nil {
		t.Fatalf("unavailable server should advance after ordinary retries stop: %v", err)
	}
	if calls != 3 || len(tool.queries) != 1 || len(answers) != 1 || answers[0][0] != "4" || len(debug.Usage.ModelCalls) != 2 {
		t.Fatalf("unexpected unavailable fallback/replay: calls=%d queries=%v answers=%v usage=%+v", calls, tool.queries, answers, debug.Usage)
	}
}

func TestAIMCPRepeatedServerCompactionRestoresFullHistoryAfterOpaqueRejection(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if calls <= 2 {
			action, _ := json.Marshal(map[string]any{"action": "web_search", "arguments": map[string]string{"query": fmt.Sprintf("evidence %d", calls)}})
			writeAIMCPOutputStream(w, string(action), []any{map[string]any{"type": "compaction", "encrypted_content": fmt.Sprintf("opaque-state-%d", calls)}}, int64(90+10*calls))
			return
		}
		if calls == 3 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"message":"Invalid encrypted compaction content"}}`)
			return
		}
		encoded, _ := json.Marshal(payload["input"])
		if payload["context_management"] != nil || strings.Contains(string(encoded), "opaque-state-") {
			t.Error("rejected opaque compaction state survived fallback")
		}
		for _, expected := range []string{"protected-question", "evidence 1", "evidence 2", "https://example.com/source"} {
			if !strings.Contains(string(encoded), expected) {
				t.Errorf("opaque rejection lost original state %q", expected)
			}
		}
		input, _ := payload["input"].([]any)
		assistantMessages, toolOutputs := 0, 0
		for _, value := range input {
			item, _ := value.(map[string]any)
			if item["role"] == "assistant" {
				assistantMessages++
			}
			content, _ := item["content"].(string)
			if strings.HasPrefix(content, "本地MCP工具 web_search 返回") {
				toolOutputs++
			}
		}
		if assistantMessages != 2 || toolOutputs != 2 {
			t.Errorf("restored history contains duplicated/missing turns: assistant=%d tool=%d", assistantMessages, toolOutputs)
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 20)
	}))
	defer server.Close()
	client := &ChatGptClient{
		BaseURL: server.URL + "/v1", Model: "test-model", SearchTool: tool,
		MaxAISearchModelCalls: 3, MaxAISearches: 2, MaxAIWebReads: 1, MaxAISearchRetries: 1,
		ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "remote", "local"}, ContextWindowTokens: 100000, MaxAttempts: 1},
	}
	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "protected-question", Type: 4})
	if err != nil {
		t.Fatalf("opaque state recovery failed: %v", err)
	}
	if calls != 4 || len(tool.queries) != 2 || len(answers) != 1 || answers[0][0] != "4" || len(debug.Usage.ModelCalls) != 3 || debug.Usage.TotalTokens != 244 {
		t.Fatalf("opaque state recovery replayed work or billing: calls=%d queries=%v answers=%v usage=%+v", calls, tool.queries, answers, debug.Usage)
	}
}

type aiMCPLargeSearchTool struct{ calls int }

func (tool *aiMCPLargeSearchTool) SearchAggregate(_ context.Context, req uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	tool.calls++
	results := make([]uapi.SearchResult, 5)
	for index := range results {
		results[index] = uapi.SearchResult{
			Title: "Historical evidence", URL: fmt.Sprintf("https://example.com/mcp-history?source=%d", index), Snippet: strings.Repeat("历史事实支持答案为四。", 100),
		}
	}
	return uapi.SearchAggregateResponse{Query: req.Query, TotalResults: len(results), Results: results}, nil
}

func TestAIMCPServerContextLimitFallsBackToLocalWithoutReplayingTool(t *testing.T) {
	tool := &aiMCPLargeSearchTool{}
	modelCalls, summaries := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if stream, _ := payload["stream"].(bool); !stream {
			summaries++
			if payload["context_management"] != nil {
				t.Error("local summary request must not recursively request compaction")
			}
			writeCompactionTestSummary(w, "Search historical evidence succeeded; source https://example.com/mcp-history supports answer 4; final answer remains pending.", "completed")
			return
		}
		modelCalls++
		switch modelCalls {
		case 1:
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"historical evidence"}}`, 10)
		case 2:
			if payload["context_management"] == nil {
				t.Error("server compression was skipped before context limit")
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"maximum context length exceeded"}}`)
		default:
			if payload["context_management"] != nil {
				t.Error("server context failure did not advance strategy")
			}
			encoded, _ := json.Marshal(payload["input"])
			for _, expected := range []string{"original-question-marker", "https://example.com/mcp-history", "final answer remains pending"} {
				if !strings.Contains(string(encoded), expected) {
					t.Errorf("local recovery lost %q", expected)
				}
			}
			writeMCPTextStream(w, `{"answers":["4"]}`, 20)
		}
	}))
	defer server.Close()
	client := &ChatGptClient{
		BaseURL: server.URL + "/v1", Model: "test-model", SearchTool: tool,
		MaxAISearchModelCalls: 3, MaxAISearches: 1, MaxAIWebReads: 1, MaxAISearchRetries: 1,
		ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "local", "remote"}, ContextWindowTokens: 100000, MaxAttempts: 1},
	}
	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "original-question-marker", Type: 4})
	if err != nil {
		t.Fatalf("server context limit recovery failed: %v", err)
	}
	if modelCalls != 3 || summaries != 1 || tool.calls != 1 || len(answers) != 1 || answers[0][0] != "4" {
		t.Fatalf("unexpected fallback/replay: model=%d summary=%d tool=%d answers=%v", modelCalls, summaries, tool.calls, answers)
	}
	if len(debug.Usage.ModelCalls) != 3 || debug.Usage.TotalTokens != 158 {
		t.Fatalf("successful model and summary calls must be charged once: %+v", debug.Usage)
	}
}

func TestAIMCPIgnoredServerCompactionFallsBackNextRoundWithoutReplayingTools(t *testing.T) {
	for _, actualInputTokens := range []int64{20, 20000} {
		t.Run(fmt.Sprintf("actualInputTokens=%d", actualInputTokens), func(t *testing.T) {
			shouldFallback := actualInputTokens >= 10000
			tool := &aiMCPLargeSearchTool{}
			modelCalls, summaries := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if stream, _ := payload["stream"].(bool); !stream {
					summaries++
					writeCompactionTestSummary(w, "Both historical evidence searches succeeded; final answer 4 remains pending.", "completed")
					return
				}
				modelCalls++
				switch modelCalls {
				case 1:
					action, _ := json.Marshal(map[string]any{
						"action": "web_search", "arguments": map[string]string{"query": "first evidence"},
						"notes": strings.Repeat("Preserved historical fact. ", 1000),
					})
					writeMCPTextStream(w, string(action), 10)
				case 2:
					if payload["context_management"] == nil || estimateAIContextTokens([]any{payload["input"]}) < 10000 {
						t.Error("test did not cross the estimated server compaction threshold")
					}
					writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"second evidence"}}`, actualInputTokens)
				default:
					if (payload["context_management"] == nil) != shouldFallback {
						t.Errorf("server fallback must use actual usage, not the estimate: context_management=%v", payload["context_management"])
					}
					encoded, _ := json.Marshal(payload["input"])
					for _, expected := range []string{"protected-question", "first evidence", "second evidence", "https://example.com/mcp-history"} {
						if !strings.Contains(string(encoded), expected) {
							t.Errorf("fallback lost previous state %q", expected)
						}
					}
					if shouldFallback && !strings.Contains(string(encoded), "Both historical evidence searches succeeded") {
						t.Error("local fallback did not retain its model-generated summary")
					}
					writeMCPTextStream(w, `{"answers":["4"]}`, 30)
				}
			}))
			defer server.Close()
			client := &ChatGptClient{
				BaseURL: server.URL + "/v1", Model: "test-model", Prompt: "Keep facts and return JSON.", SearchTool: tool,
				MaxAISearchModelCalls: 4, MaxAISearches: 2, MaxAIWebReads: 1, MaxAISearchRetries: 1,
				ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "local", "remote"}, ContextWindowTokens: 20000, TriggerPercent: 50, TargetPercent: 30, MaxAttempts: 1},
			}
			answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "protected-question", Type: 4})
			if err != nil {
				t.Fatalf("ignored server compaction recovery failed: %v", err)
			}
			wantSummaries := 0
			if shouldFallback {
				wantSummaries = 1
			}
			if modelCalls != 3 || summaries != wantSummaries || tool.calls != 2 || len(answers) != 1 || answers[0][0] != "4" {
				t.Fatalf("ignored server compaction replayed a model/tool: model=%d summary=%d tool=%d answers=%v", modelCalls, summaries, tool.calls, answers)
			}
			if len(debug.Usage.ModelCalls) != 3+wantSummaries || debug.Usage.TotalTokens != actualInputTokens+52+int64(wantSummaries)*120 {
				t.Fatalf("ignored compaction duplicated billing or lost summary usage: %+v", debug.Usage)
			}
		})
	}
}
