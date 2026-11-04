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

func TestMCPInstructionsDescribeLocalTikuSearchAction(t *testing.T) {
	if !strings.Contains(aiMCPInstructions, `"action":"tiku_search"`) || !strings.Contains(aiMCPInstructions, "待核验") {
		t.Fatal("MCP instructions must expose local Tiku search as unverified candidate evidence")
	}
}

func TestMCPDoesNotRestartAfterLocalTikuLookup(t *testing.T) {
	if canFallbackToMCP(AISearchUsage{TikuSearchCalls: []AITikuSearchCallUsage{{Status: "success"}}}) {
		t.Fatal("a completed local lookup must not be replayed in a new MCP orchestration")
	}
}

func TestSearchAnswerWithMCPSharesWebAndTikuBudgetWithoutBillingCacheHits(t *testing.T) {
	webTool := &testAISearchTool{}
	tikuTool := &testAITikuSearchTool{result: AITikuSearchResult{MatchKind: "exact", CandidateCount: 1, Candidates: []AITikuSearchCandidate{{Question: "2+2=?", Type: -1, Answers: []string{"4"}, Source: "Chaoxing", Status: "unverified", Evidence: `{"page":"review"}`, Score: 1}}}}
	modelCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		modelCalls++
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(payload)
		if _, hasTools := payload["tools"]; hasTools {
			t.Error("MCP must keep tools in the JSON action protocol")
		}
		switch modelCalls {
		case 1:
			if !strings.Contains(string(encoded), "首次工具调用必须为 web_search") {
				t.Error("MCP must retain the initial web search requirement")
			}
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"2+2"}}`, 10)
		case 2, 3:
			writeMCPTextStream(w, `{"action":"tiku_search","arguments":{"question":"2+2=?","type":-1,"options":["4"]}}`, 10)
		case 4:
			if !strings.Contains(string(encoded), `\"source\":\"Chaoxing\"`) {
				t.Error("MCP must deliver local source and evidence to the next model call")
			}
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"addition proof"}}`, 10)
		case 5:
			if !strings.Contains(string(encoded), "这是最后一轮") || !strings.Contains(string(encoded), "web_search/tiku_search 共用 0 次") {
				t.Errorf("shared search budget must be exhausted before final answer: %s", encoded)
			}
			writeMCPTextStream(w, `{"answers":["4"]}`, 10)
		default:
			t.Errorf("unexpected MCP model call %d", modelCalls)
			writeMCPTextStream(w, `{"answers":["4"]}`, 10)
		}
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test-model", SearchTool: webTool, TikuSearchTool: tikuTool, MaxAISearchModelCalls: 6, MaxAISearches: 3, MaxAISearchRetries: 1}
	answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: -1, Options: []string{"4"}, DeepThinking: true})
	if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	if modelCalls != 5 || len(webTool.queries) != 2 || tikuTool.calls != 1 || len(debug.Usage.TikuSearchCalls) != 1 || debug.Usage.TikuSearchCalls[0].Status != "success" {
		t.Fatalf("unexpected shared-budget/cache usage: models=%d web=%v localCalls=%d usage=%+v", modelCalls, webTool.queries, tikuTool.calls, debug.Usage)
	}
	starts := 0
	for _, trace := range debug.Usage.Trace {
		if trace.Stage == "本地题库检索开始" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("only the real local lookup should produce a billing-sequence trace: got %d", starts)
	}
}

func TestSearchAnswerWithMCPRejectsLocalTikuLookupBeforeInitialWebSearch(t *testing.T) {
	tikuTool := &testAITikuSearchTool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		writeMCPTextStream(w, `{"action":"tiku_search","arguments":{"question":"2+2=?","type":-1,"options":[]}}`, 10)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test-model", SearchTool: &testAISearchTool{}, TikuSearchTool: tikuTool, MaxAISearchModelCalls: 3, MaxAISearches: 2, MaxAISearchRetries: 1}
	_, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: -1, DeepThinking: true})
	if err == nil || !strings.Contains(err.Error(), "首次工具调用必须为 web_search") || tikuTool.calls != 0 || len(debug.Usage.TikuSearchCalls) != 0 {
		t.Fatalf("local lookup must not bypass initial web search: calls=%d usage=%+v err=%v", tikuTool.calls, debug.Usage, err)
	}
}

func TestSearchAnswerWithMCPLocalTikuArgumentsUseDedicatedSizeLimit(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		question string
		wantErr  bool
	}{
		{name: "long Chinese question exceeds old shared limit", question: strings.Repeat("长题干", 500)},
		{name: "oversized local arguments are rejected", question: strings.Repeat("x", aiToolArgumentLimit("tiku_search")+1), wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			webTool := &testAISearchTool{}
			tikuTool := &testAITikuSearchTool{}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls++
				switch calls {
				case 1:
					writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"2+2"}}`, 10)
				case 2:
					content, _ := json.Marshal(map[string]any{"action": "tiku_search", "arguments": map[string]any{"question": testCase.question, "type": -1, "options": []string{}}})
					writeMCPTextStream(w, string(content), 10)
				default:
					writeMCPTextStream(w, `{"answers":["4"]}`, 10)
				}
			}))
			defer server.Close()
			client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test-model", SearchTool: webTool, TikuSearchTool: tikuTool, MaxAISearchModelCalls: 3, MaxAISearches: 2, MaxAISearchRetries: 1}
			answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: -1, DeepThinking: true})
			if testCase.wantErr {
				if err == nil || !strings.Contains(err.Error(), "参数超过大小限制") || tikuTool.calls != 0 || len(debug.Usage.TikuSearchCalls) != 0 {
					t.Fatalf("oversized arguments must not reach local lookup: calls=%d usage=%+v err=%v", tikuTool.calls, debug.Usage, err)
				}
				return
			}
			if err != nil || tikuTool.calls != 1 || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
				t.Fatalf("long local question must remain supported: answers=%v calls=%d err=%v", answers, tikuTool.calls, err)
			}
		})
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
		MaxAISearchModelCalls: 1,
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

func TestSearchAnswerWithMCPDoesNotReplayEmptyModelResponse(t *testing.T) {
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
	if err == nil {
		t.Fatal("empty successful response should fail without replay")
	}
	if calls != 1 {
		t.Fatalf("empty model response must not be replayed, got %d calls", calls)
	}
	if len(debug.Usage.ModelCalls) != 1 {
		t.Fatalf("the single model call must be metered, got %+v", debug.Usage.ModelCalls)
	}
	if len(answers) != 0 {
		t.Fatalf("empty model response must not yield answers: %#v", answers)
	}
}

func TestSearchAnswerWithMCPRetriesRateLimitWithRetryAfter(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 20)
	}))
	defer server.Close()
	client := &ChatGptClient{
		BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test",
		SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 1, MaxAISearchRetries: 2,
	}
	answers, _, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?"})
	if err != nil || calls != 2 || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
		t.Fatalf("MCP rate limit retry: answers=%v calls=%d err=%v", answers, calls, err)
	}
}

func TestSearchAnswerWithMCPSafetyRetriesThenUsesBackupModel(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		modelName, _ := payload["model"].(string)
		calls[modelName]++
		if modelName == "primary" {
			writeResponsesEvent(w, "error", map[string]any{"error": map[string]any{
				"type": "invalid_request_error", "code": "invalid_prompt", "message": "retry denied by safety policy",
			}})
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 20)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "primary", BackupModel: "backup", SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 1, MaxAISearchRetries: 2}
	answer, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if err != nil || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("backup MCP model should answer: answer=%v err=%v", answer, err)
	}
	if calls["primary"] != 2 || calls["backup"] != 1 {
		t.Fatalf("MCP safety retry/fallback mismatch: calls=%v", calls)
	}
	if len(debug.Usage.ModelCalls) != 1 || debug.Usage.ModelCalls[0].Model != "backup" {
		t.Fatalf("MCP usage must retain backup model: %+v", debug.Usage.ModelCalls)
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

func TestSearchAnswerWithMCPDoesNotReplayParseFailure(t *testing.T) {
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
	if err == nil || !strings.Contains(err.Error(), "已尝试1次") || !strings.Contains(err.Error(), "invalid character 'a'") {
		t.Fatalf("expected the original parse error after one attempt, got %v", err)
	}
	if !reflect.DeepEqual(paths, []string{"/responses"}) {
		t.Fatalf("parsed model output must not be replayed: %v", paths)
	}
	if len(tool.queries) != 0 || debug.Usage.TotalTokens != 14 || len(debug.Usage.ModelCalls) != 1 || len(debug.Usage.Trace) != 1 {
		t.Fatalf("invalid actions must not execute tools or lose usage: queries=%v usage=%+v", tool.queries, debug.Usage)
	}
	for i, trace := range debug.Usage.Trace {
		if trace.Attempt != i+1 || trace.Error == "" {
			t.Fatalf("trace must record the endpoint's parse failure and shared attempt number: %+v", trace)
		}
	}
}
