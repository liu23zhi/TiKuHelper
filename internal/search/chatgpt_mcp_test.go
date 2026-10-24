package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func writeMCPTextStream(w http.ResponseWriter, content string, inputTokens int64) {
	w.Header().Set("Content-Type", "text/event-stream")
	writeResponsesEvent(w, "response.output_text.delta", map[string]any{
		"output_index":  0,
		"content_index": 0,
		"delta":         content,
	})
	writeResponsesEvent(w, "response.completed", map[string]any{
		"response": map[string]any{
			"id":     "resp-mcp-test",
			"status": "completed",
			"output": []any{},
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": 4,
				"total_tokens":  inputTokens + 4,
			},
		},
	})
}

func TestDeepThinkingFallsBackToMCPAfterConfirmedToolCapabilityFailure(t *testing.T) {
	tool := &testAISearchTool{}
	var nativeToolCalls int
	var modelCallsWithoutTools int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
			nativeToolCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"openai_error","type":"bad_response_status_code","code":"bad_response_status_code"}}`))
			return
		}

		modelCallsWithoutTools++
		switch modelCallsWithoutTools {
		case 1:
			writeMCPTextStream(w, "OK", 10)
		case 2:
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"2+2"}}`, 12)
		case 3:
			writeMCPTextStream(w, `{"action":"final","answers":["4"]}`, 14)
		default:
			t.Fatalf("unexpected model call without tools: %d", modelCallsWithoutTools)
		}
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:                true,
		BaseURL:               server.URL + "/v1",
		APIKey:                "sk-test",
		Model:                 "gpt-6.1-sol",
		SearchTool:            tool,
		MaxAISearchModelCalls: 3,
		MaxAISearches:         1,
		MaxAIWebReads:         1,
		MaxAISearchRetries:    1,
	}

	answers, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{
		Question:     "2+2=?",
		DeepThinking: true,
	})
	if err != nil {
		t.Fatalf("expected MCP fallback to succeed, got %v", err)
	}
	if usage.Route != AISearchRouteDeepMCP {
		t.Fatalf("expected deep_mcp route, got %q", usage.Route)
	}
	if nativeToolCalls != 2 {
		t.Fatalf("expected one native call and one capability probe, got %d", nativeToolCalls)
	}
	if modelCallsWithoutTools != 3 {
		t.Fatalf("expected one baseline probe and two MCP model calls, got %d", modelCallsWithoutTools)
	}
	if len(tool.queries) != 1 || tool.queries[0] != "2+2" {
		t.Fatalf("unexpected MCP search queries: %#v", tool.queries)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "4" {
		t.Fatalf("unexpected answers: %#v", answers)
	}
	if usage.TotalTokens <= 0 || len(usage.ModelCalls) < 3 {
		t.Fatalf("expected probe and MCP usage to be retained: %+v", usage)
	}
}

func TestToolCapabilityProbeCandidateDoesNotTreatArbitraryBadRequestAsUnsupported(t *testing.T) {
	client := &ChatGptClient{APIKey: "sk-test"}
	if isToolCapabilityProbeCandidate(client.upstreamError("tool_http", http.StatusBadRequest, `{"error":{"message":"invalid_request"}}`, nil)) {
		t.Fatal("arbitrary HTTP 400 must not trigger MCP fallback")
	}
}

func TestParseAIMCPActionAcceptsDirectAnswerPayloads(t *testing.T) {
	tests := []struct {
		name    string
		content string
		answers []string
	}{
		{
			name:    "answers object without action",
			content: `{"answers":["B. 看情况"]}`,
			answers: []string{"B. 看情况"},
		},
		{
			name:    "answers object missing opening object delimiter",
			content: `answers":["B. 看情况"]}`,
			answers: []string{"B. 看情况"},
		},
		{
			name:    "loose answer object without action",
			content: `{"answer":"A","explanation":"符合题意"}`,
			answers: []string{"A"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, err := parseAIMCPAction(tt.content)
			if err != nil {
				t.Fatalf("parse direct answer: %v", err)
			}
			if action.Action != "final" {
				t.Fatalf("expected final action, got %q", action.Action)
			}
			if !reflect.DeepEqual(action.Answers, tt.answers) {
				t.Fatalf("unexpected answers: %#v", action.Answers)
			}
		})
	}
}

func TestSearchAnswerWithMCPAcceptsDirectAnswerWithoutAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeMCPTextStream(w, `{"answers":["B. 看情况"]}`, 20)
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:                true,
		BaseURL:               server.URL + "/v1",
		APIKey:                "sk-test",
		Model:                 "gpt-6.1-sol",
		SearchTool:            &testAISearchTool{},
		MaxAISearchModelCalls: 2,
		MaxAISearches:         1,
		MaxAIWebReads:         1,
		MaxAISearchRetries:    1,
	}

	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{
		Question:     "周末有人组局，你去吗？",
		Options:      []string{"A. 去", "B. 看情况", "C. 不去"},
		Type:         0,
		DeepThinking: true,
	})
	if err != nil {
		t.Fatalf("expected direct MCP answer to succeed, got %v", err)
	}
	if debug.Usage.Route != "" {
		t.Fatalf("searchAnswerWithMCP must leave route selection to the caller, got %q", debug.Usage.Route)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "看情况" {
		t.Fatalf("unexpected answers: %#v", answers)
	}
}

func TestSearchAnswerWithMCPRetriesEmptyModelResponse(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeMCPTextStream(w, "", 20)
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 22)
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable:                true,
		BaseURL:               server.URL + "/v1",
		APIKey:                "sk-test",
		Model:                 "gpt-6.1-sol",
		SearchTool:            &testAISearchTool{},
		MaxAISearchModelCalls: 1,
		MaxAISearches:         1,
		MaxAIWebReads:         1,
		MaxAISearchRetries:    2,
	}

	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{
		Question:     "2+2=?",
		DeepThinking: true,
	})
	if err != nil {
		t.Fatalf("expected retry after empty MCP response to succeed, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 model calls, got %d", calls)
	}
	if len(debug.Usage.ModelCalls) != 2 {
		t.Fatalf("expected both model calls to be metered, got %+v", debug.Usage.ModelCalls)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "4" {
		t.Fatalf("unexpected answers: %#v", answers)
	}
}

func TestSearchAnswerWithMCPUsesCompleteResponseBeforeExecutingTool(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponsesEvent(w, "response.output_text.delta", map[string]any{"delta": "action"})
		if calls == 1 {
			writeAIResponsesCompleted(w, `{"action":"web_search","arguments":{"query":"2+2"}}`, map[string]any{
				"input_tokens": 10, "output_tokens": 4, "total_tokens": 14,
			})
			return
		}
		writeAIResponsesCompleted(w, `{"answers":["4","4"]}`, map[string]any{
			"input_tokens": 20, "output_tokens": 5, "total_tokens": 25,
		})
	}))
	defer server.Close()
	client := &ChatGptClient{
		BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test",
		SearchTool: tool, MaxAISearchModelCalls: 2, MaxAISearchRetries: 1,
	}
	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 2})
	if err != nil {
		t.Fatalf("complete response must recover the partial MCP JSON: %v", err)
	}
	if !reflect.DeepEqual(answers, [][]string{{"4", "4"}}) || calls != 2 {
		t.Fatalf("expected two answer parts without retry: answers=%v calls=%d", answers, calls)
	}
	if !reflect.DeepEqual(tool.queries, []string{"2+2"}) {
		t.Fatalf("tool must execute once: %v", tool.queries)
	}
	if debug.Usage.TotalTokens != 39 || len(debug.Usage.ModelCalls) != 2 {
		t.Fatalf("all model usage must be retained once: %+v", debug.Usage)
	}
}

func TestSearchAnswerWithMCPCountsEndpointFailuresAsOneAttempt(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		writeMCPTextStream(w, "action", 10)
	}))
	defer server.Close()
	tool := &testAISearchTool{}
	client := &ChatGptClient{
		BaseURL: server.URL, APIKey: "sk-test", Model: "test", SearchTool: tool,
		MaxAISearchModelCalls: 2, MaxAISearchRetries: 3,
	}
	_, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?"})
	if err == nil || !strings.Contains(err.Error(), "已尝试3次") || !strings.Contains(err.Error(), "invalid character 'a'") {
		t.Fatalf("expected the original parse error after three full attempts, got %v", err)
	}
	if !reflect.DeepEqual(paths, []string{"/responses", "/v1/responses", "/responses", "/v1/responses", "/responses", "/v1/responses"}) {
		t.Fatalf("each attempt must try both endpoints: %v", paths)
	}
	if len(tool.queries) != 0 || debug.Usage.TotalTokens != 84 || len(debug.Usage.ModelCalls) != 6 || len(debug.Usage.Trace) != 6 {
		t.Fatalf("invalid actions must not execute tools or lose usage: queries=%v usage=%+v", tool.queries, debug.Usage)
	}
	for i, trace := range debug.Usage.Trace {
		if trace.Attempt != i/2+1 || trace.Error == "" {
			t.Fatalf("trace must record the endpoint's parse failure and shared attempt number: %+v", trace)
		}
	}
}
