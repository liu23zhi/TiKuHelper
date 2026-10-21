package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
