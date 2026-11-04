package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/uapi"
	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/model"
)

type testAISearchTool struct {
	queries []string
	err     error
}

type testAITikuSearchTool struct {
	calls  int
	err    error
	result AITikuSearchResult
}

func (tool *testAITikuSearchTool) SearchTiku(_ context.Context, request model.SearchRequest) (AITikuSearchResult, error) {
	tool.calls++
	if tool.err != nil {
		return AITikuSearchResult{}, tool.err
	}
	result := tool.result
	result.Query = request.Question
	return result, nil
}

type openAIResponsesTestRequest struct {
	Tools    []openAIResponsesTool `json:"tools"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
		Type    string `json:"type"`
		CallID  string `json:"call_id"`
		Output  any    `json:"output"`
	} `json:"input"`
}

func (tool *testAISearchTool) SearchAggregate(ctx context.Context, req uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	tool.queries = append(tool.queries, req.Query)
	return uapi.SearchAggregateResponse{Query: req.Query, TotalResults: 1, Results: []uapi.SearchResult{{Title: "参考资料", URL: "https://example.com/source", Snippet: "材料给出的答案为4。"}}}, tool.err
}

func writeAIToolStream(w http.ResponseWriter, callID, query string) {
	w.Header().Set("Content-Type", "text/event-stream")
	arguments, _ := json.Marshal(map[string]string{"query": query})
	split := len(arguments) / 2
	for split > 0 && !utf8.RuneStart(arguments[split]) {
		split--
	}
	added := map[string]any{"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": "web_search", "arguments": ""}
	done := map[string]any{"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": "web_search", "arguments": string(arguments), "status": "completed"}
	writeResponsesEvent(w, "response.output_item.added", map[string]any{"output_index": 0, "item": added})
	writeResponsesEvent(w, "response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": "fc_" + callID, "delta": string(arguments[:split])})
	writeResponsesEvent(w, "response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": "fc_" + callID, "delta": string(arguments[split:])})
	writeResponsesEvent(w, "response.output_item.done", map[string]any{"output_index": 0, "item": done})
	writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{
		"id": "resp_" + callID, "status": "completed", "output": []any{done},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14, "input_tokens_details": map[string]any{"cached_tokens": 2}},
	}})
}

func writeAITikuSearchToolStream(w http.ResponseWriter, callID string, args map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	encoded, _ := json.Marshal(args)
	item := map[string]any{"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": "tiku_search", "arguments": string(encoded), "status": "completed"}
	writeResponsesEvent(w, "response.output_item.done", map[string]any{"output_index": 0, "item": item})
	writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_" + callID, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 8, "output_tokens": 3, "total_tokens": 11}}})
}

func TestDeepThinkingSearchesWebFirstThenOptionallySearchesLocalTiku(t *testing.T) {
	webTool := &testAISearchTool{}
	tikuTool := &testAITikuSearchTool{result: AITikuSearchResult{MatchKind: "exact", CandidateCount: 1, Candidates: []AITikuSearchCandidate{{Question: "2+2=?", Type: -1, Answers: []string{"4"}, Source: "Chaoxing", Status: "unverified", Evidence: `{"grade":"review"}`, Score: 1}}}}
	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		round++
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		switch round {
		case 1:
			choice, _ := payload["tool_choice"].(map[string]any)
			if choice["name"] != "web_search" {
				t.Errorf("first tool must remain web_search: %v", payload["tool_choice"])
			}
			for _, item := range payload["tools"].([]any) {
				if item.(map[string]any)["name"] == "tiku_search" {
					t.Error("local search must wait until after the initial web search")
				}
			}
			writeAIToolStream(w, "initial-web", "2+2 calculation")
		case 2:
			foundTiku := false
			for _, item := range payload["tools"].([]any) {
				if item.(map[string]any)["name"] == "tiku_search" {
					foundTiku = true
				}
			}
			if !foundTiku {
				t.Errorf("optional local search tool missing: %v", payload["tools"])
			}
			writeAITikuSearchToolStream(w, "local-tiku", map[string]any{"question": "2+2=?", "type": -1, "options": []string{"4"}})
		case 3:
			foundLocalEvidence := false
			for _, raw := range payload["input"].([]any) {
				item, _ := raw.(map[string]any)
				if item["type"] == "function_call_output" && strings.Contains(fmt.Sprint(item["output"]), `"source":"Chaoxing"`) {
					foundLocalEvidence = true
				}
			}
			if !foundLocalEvidence {
				t.Errorf("model final round did not receive source and evidence: %v", payload["input"])
			}
			writeAIFinalStream(w)
		default:
			t.Errorf("unexpected model round %d", round)
		}
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "test-key", Model: "test-model", SearchTool: webTool, TikuSearchTool: tikuTool, MaxAISearches: 3, MaxAISearchModelCalls: 4}
	answers, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", Type: -1, Options: []string{"4"}, DeepThinking: true})
	if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	if webTool.queries == nil || len(webTool.queries) != 1 || tikuTool.calls != 1 || len(usage.TikuSearchCalls) != 1 || usage.TikuSearchCalls[0].Status != "success" {
		t.Fatalf("unexpected tool usage: web=%+v localCalls=%d usage=%+v", webTool.queries, tikuTool.calls, usage)
	}
}

func writeAIFinalStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	content := `{"answers":["4"]}`
	message := map[string]any{"type": "message", "id": "msg_final", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": content, "annotations": []any{}}}}
	writeResponsesEvent(w, "response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "delta": content})
	writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{
		"id": "resp_final", "status": "completed", "output": []any{message},
		"usage": map[string]any{"input_tokens": 20, "output_tokens": 3, "total_tokens": 23},
	}})
}

func TestFormatAIModelCompleteResponseAggregatesToolCallsWithoutSSEFrames(t *testing.T) {
	result := aiToolStreamResult{
		Content:          "",
		ReasoningContent: "先核对搜索结果，再返回答案。",
		Calls: []openAIToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: openAIToolFunction{
				Name:      "web_search",
				Arguments: `{"query":"美少女日常电视剧 动漫 生活幻想 选择题"}`,
			},
		}},
		FinishReason:   "tool_calls",
		ReasoningChars: 12,
		Usage:          AISearchUsage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14},
	}

	complete := formatAIModelCompleteResponse(result)
	if strings.Contains(complete, "data:") || strings.Contains(complete, "[DONE]") {
		t.Fatalf("complete response must not contain SSE framing: %s", complete)
	}
	for _, expected := range []string{"web_search", "美少女日常电视剧 动漫 生活幻想 选择题", "tool_calls", "先核对搜索结果，再返回答案。", `"input_tokens":10`} {
		if !strings.Contains(complete, expected) {
			t.Fatalf("complete response is missing %q: %s", expected, complete)
		}
	}
}

func TestBuildAIToolTestPromptSupportsOCRAndCombinedTools(t *testing.T) {
	for _, test := range []struct {
		name, tool, target, want string
	}{
		{name: "ocr", tool: "image_ocr", target: "https://example.com/question.png", want: "OCR"},
		{name: "combined", tool: "image_ocr_and_tools", target: "https://example.com/question.png", want: "web_search"},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt, err := BuildAIToolTestPrompt(test.tool, test.target)
			if err != nil || !strings.Contains(prompt, test.want) {
				t.Fatalf("prompt=%q err=%v", prompt, err)
			}
		})
	}
}

func TestAISearchToolsSearchTwiceAndAccumulateUsage(t *testing.T) {
	tool := &testAISearchTool{}
	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if _, exists := payload["max_output_tokens"]; exists {
			t.Errorf("tool answer requests must use the upstream output allowance: %v", payload["max_output_tokens"])
		}
		round++
		if round == 1 {
			choice, ok := payload["tool_choice"].(map[string]any)
			if !ok || choice["type"] != "function" || choice["name"] != "web_search" {
				t.Errorf("first round must require search: %v", payload["tool_choice"])
			}
			writeAIToolStream(w, "search1", "二加二 计算")
			return
		}
		messages := payload["input"].([]any)
		foundSearchEvidence := false
		for _, rawMessage := range messages {
			message, ok := rawMessage.(map[string]any)
			if !ok || message["type"] != "function_call_output" {
				continue
			}
			content, ok := message["output"].(string)
			if ok && strings.Contains(content, "example.com/source") {
				foundSearchEvidence = true
				break
			}
		}
		if !foundSearchEvidence {
			t.Errorf("missing search evidence: %v", messages)
		}
		if round == 2 {
			writeAIToolStream(w, "search2", "加法 验算")
			return
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool}
	answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", Options: []string{"3", "4"}, DeepThinking: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(answer, [][]string{{"4"}}) || len(tool.queries) != 2 || round != 3 {
		t.Fatalf("answer=%v queries=%v rounds=%d", answer, tool.queries, round)
	}
	if usage.InputTokens != 40 || usage.CachedInputTokens != 4 || usage.CacheWriteTokens != 0 || usage.OutputTokens != 11 || usage.TotalTokens != 51 {
		t.Fatalf("usage=%+v", usage)
	}
	if len(usage.ModelCalls) != 3 {
		t.Fatalf("expected one model call usage per tool round, got %+v", usage.ModelCalls)
	}
	for i, call := range usage.ModelCalls {
		if call.Index != i+1 || call.Model != "test" || call.Round != i+1 || call.Attempt != 1 {
			t.Fatalf("unexpected model call %d: %+v", i, call)
		}
	}
	if len(usage.SearchCalls) != 2 || usage.SearchCalls[0].Query != "二加二 计算" || usage.SearchCalls[1].Query != "加法 验算" {
		t.Fatalf("expected each actual search query in usage, got %+v", usage.SearchCalls)
	}
	if len(usage.Trace) < 8 {
		t.Fatalf("expected complete model and tool trace, got %+v", usage.Trace)
	}
	traceStages := make(map[string]bool)
	fullResponseCount := 0
	rawResponseCount := 0
	for _, trace := range usage.Trace {
		traceStages[trace.Stage] = true
		if trace.ModelCallBudget <= 0 {
			t.Fatalf("trace is missing model call budget: %+v", trace)
		}
		if trace.FullResponse != "" {
			fullResponseCount++
			if strings.Contains(trace.FullResponse, "data:") || strings.Contains(trace.FullResponse, "response.completed") {
				t.Fatalf("model trace must contain completed content rather than SSE frames: %s", trace.FullResponse)
			}
		}
		if trace.RawResponse != "" {
			rawResponseCount++
		}
		if strings.Contains(trace.Stage, "模型") && strings.Contains(trace.Stage, "响应") && trace.TotalTokens == 0 {
			t.Fatalf("model response trace is missing token usage: %+v", trace)
		}
		if strings.Contains(trace.Stage, "搜索 API") && trace.TotalTokens != 0 {
			t.Fatalf("search API trace must not contain model token usage: %+v", trace)
		}
	}
	for _, stage := range []string{"模型调用开始", "模型响应", "搜索工具调用开始", "搜索工具完成", "搜索 API 请求开始", "搜索 API 响应"} {
		if !traceStages[stage] {
			t.Fatalf("complete trace is missing stage %q: %+v", stage, usage.Trace)
		}
	}
	if fullResponseCount == 0 || rawResponseCount == 0 {
		t.Fatalf("complete model response is missing full=%d raw=%d", fullResponseCount, rawResponseCount)
	}
	for _, call := range usage.SearchCalls {
		if call.API != uapi.DefaultSearchAggregateURL || call.Status != "success" || call.ResultCount != 1 {
			t.Fatalf("unexpected search call detail: %+v", call)
		}
	}
}

func TestAISearchDefaultsToOneShotWithoutDeepThinking(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, exists := payload["tools"]; exists {
			t.Fatal("deepThinking=false must preserve the original one-shot request")
		}
		// 普通搜索是「一次性直出答案」：不提供工具，也不该播报任何预算。
		messages := payload["input"].([]any)
		system := messages[0].(map[string]any)["content"].(string)
		if strings.Contains(system, "当前预算") || strings.Contains(system, "web_search") || strings.Contains(system, "web_read") {
			t.Fatalf("one-shot system prompt must not mention tools or budgets: %s", system)
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()

	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 5}
	answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: false})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(tool.queries) != 0 || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("answer=%v modelCalls=%d searches=%v", answer, calls, tool.queries)
	}
}

func TestAISearchToolsRejectLongQueryAndReuseDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		calls       int
	}{{"long", strings.Repeat("题", 121), 0}, {"duplicate", "同一查询", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &testAISearchTool{}
			round := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				round++
				if round <= 2 {
					writeAIToolStream(w, fmt.Sprintf("call%d", round), tc.query)
					return
				}
				writeAIFinalStream(w)
			}))
			defer server.Close()
			client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool}
			if _, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "题目", DeepThinking: true}); err != nil {
				t.Fatal(err)
			}
			if len(tool.queries) != tc.calls {
				t.Fatalf("actual searches=%d want=%d", len(tool.queries), tc.calls)
			}
		})
	}
}

func TestAISearchToolsStopsSameRoundCallsAfterSearchFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			searchCalls := 0
			searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				searchCalls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"code":"SEARCH_UNAVAILABLE","message":"search unavailable"}`)
			}))
			defer searchServer.Close()
			tool := uapi.NewClient("uapi-test").SetSearchAggregateURL(searchServer.URL).SetMaxRetries(0)
			modelCalls := 0
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				modelCalls++
				var payload openAIResponsesTestRequest
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode model request: %v", err)
					return
				}
				if modelCalls == 1 {
					w.Header().Set("Content-Type", "text/event-stream")
					output := make([]any, 0, 3)
					for index, call := range []struct{ id, query string }{{"search1", "first query"}, {"search2", "second query"}, {"search3", "third query"}} {
						item := map[string]any{"type": "function_call", "id": fmt.Sprintf("fc-%s", call.id), "call_id": call.id, "name": "web_search", "arguments": fmt.Sprintf(`{"query":"%s"}`, call.query), "status": "completed"}
						output = append(output, item)
						writeResponsesEvent(w, "response.output_item.done", map[string]any{"output_index": index, "item": item})
					}
					writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": output, "usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}}})
					return
				}
				if len(payload.Tools) != 0 {
					t.Error("search failure must force a final answer without tools")
				}
				responses := make(map[string]bool)
				for _, message := range payload.Messages {
					if message.Type != "function_call_output" {
						continue
					}
					content, ok := message.Output.(string)
					var failure map[string]string
					if !ok || json.Unmarshal([]byte(content), &failure) != nil || failure["error"] == "" {
						t.Errorf("missing valid error response for tool %s: %v", message.CallID, message.Output)
					}
					if responses[message.CallID] {
						t.Errorf("duplicate tool response for %s", message.CallID)
					}
					responses[message.CallID] = true
				}
				if len(responses) != 3 || !responses["search1"] || !responses["search2"] || !responses["search3"] {
					t.Errorf("all tool calls need a response before the final model request: %v", responses)
				}
				writeAIFinalStream(w)
			}))
			defer modelServer.Close()

			client := &ChatGptClient{Enable: true, BaseURL: modelServer.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 3, MaxAISearches: 3}
			answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
			if err != nil || !reflect.DeepEqual(answer, [][]string{{"4"}}) || modelCalls != 2 {
				t.Fatalf("answer=%v modelCalls=%d err=%v", answer, modelCalls, err)
			}
			if searchCalls != 1 || len(usage.SearchCalls) != 1 || usage.SearchCalls[0].Query != "first query" || usage.SearchCalls[0].Status != "error" {
				t.Fatalf("failed first search must stop later external calls and billing usage: requests=%d usage=%+v", searchCalls, usage.SearchCalls)
			}
		})
	}
}

func TestAISearchToolsBoundRoundsAndDisableToolsForFinal(t *testing.T) {
	tool := &testAISearchTool{}
	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		round++
		if round <= 3 {
			writeAIToolStream(w, fmt.Sprintf("call%d", round), fmt.Sprintf("query%d", round))
			return
		}
		if _, exists := payload["tools"]; exists {
			t.Error("final round must not expose tools")
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool}
	_, debug, err := client.SearchAnswerDebug(model.SearchRequest{Question: "编写完整项目，生成war、PDF及截图", Type: -4, DeepThinking: true})
	if err != nil {
		t.Fatal(err)
	}
	if round != 4 || len(tool.queries) != 3 {
		t.Fatalf("rounds=%d searches=%d", round, len(tool.queries))
	}
	if !strings.Contains(debug.RequestBody, "不执行") || debug.Usage.TotalTokens != 65 {
		t.Fatalf("debug=%+v", debug)
	}
}

func TestAISearchToolsHonorsConfiguredModelCallAndSearchBudgets(t *testing.T) {
	t.Run("single model call must answer directly", func(t *testing.T) {
		tool := &testAISearchTool{}
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if _, exists := payload["tools"]; exists {
				t.Fatal("single configured model call must be the final answer call")
			}
			messages := payload["input"].([]any)
			budget := messages[len(messages)-1].(map[string]any)["content"].(string)
			if !strings.Contains(budget, "还剩模型调用 0 次") || !strings.Contains(budget, "最后一次模型调用") {
				t.Fatalf("missing final budget instruction: %s", budget)
			}
			writeAIFinalStream(w)
		}))
		defer server.Close()

		client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 1, MaxAISearches: 3}
		answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 || len(tool.queries) != 0 || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
			t.Fatalf("answer=%v calls=%d searches=%v", answer, calls, tool.queries)
		}
	})

	t.Run("search budget forces final answer", func(t *testing.T) {
		tool := &testAISearchTool{}
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if calls == 1 {
				if _, ok := payload["tools"]; !ok {
					t.Fatal("first call should allow the required search")
				}
				writeAIToolStream(w, "search1", "预算测试")
				return
			}
			if _, exists := payload["tools"]; exists {
				t.Fatal("search budget exhausted; second call must be final")
			}
			messages := payload["input"].([]any)
			budget := messages[len(messages)-1].(map[string]any)["content"].(string)
			if !strings.Contains(budget, "web_search 搜索还剩 0 次") {
				t.Fatalf("missing exhausted search budget: %s", budget)
			}
			writeAIFinalStream(w)
		}))
		defer server.Close()

		client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 3, MaxAISearches: 1}
		answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "预算测试", DeepThinking: true})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 || len(tool.queries) != 1 || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
			t.Fatalf("answer=%v calls=%d searches=%v", answer, calls, tool.queries)
		}
	})
}

func TestAISearchToolsEndpointFallbackConsumesModelBudget(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/responses" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if _, exists := payload["tools"]; exists {
			writeAIToolStream(w, "fallback-search", "fallback query")
			return
		}
		messages := payload["input"].([]any)
		budget := messages[len(messages)-1].(map[string]any)["content"].(string)
		if !strings.Contains(budget, "还剩模型调用 0 次") || !strings.Contains(budget, "最后一次模型调用") {
			t.Errorf("missing final budget on fallback: %s", budget)
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 2, MaxAISearches: 3}
	answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if err != nil || calls != 4 || len(tool.queries) != 1 || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("answer=%v calls=%d searches=%v err=%v", answer, calls, tool.queries, err)
	}
}

func TestAISearchToolsRetriesFailedRoundWithoutLeakingRoundError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test",
		SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 1, MaxAISearchRetries: 2,
	}
	answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if err != nil || calls != 2 || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("retry should recover one failed round: answer=%v calls=%d err=%v", answer, calls, err)
	}
}

func TestAISearchToolsDoesNotReplayEmptyResponse(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			writeAIResponsesCompleted(w, " ", map[string]any{
				"input_tokens": 8, "output_tokens": 1, "total_tokens": 9,
			})
		case 2:
			writeAIToolStream(w, "empty-retry-search", "空响应重试")
		default:
			writeAIFinalStream(w)
		}
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test",
		SearchTool: tool, MaxAISearchModelCalls: 2, MaxAISearches: 3, MaxAISearchRetries: 2,
	}
	answer, debug, err := client.searchAnswerWithTools(t.Context(), model.SearchRequest{
		Question: "2+2=?", Options: []string{"3", "4"}, DeepThinking: true,
	})
	if err == nil {
		t.Fatal("empty successful response should fail without replay")
	}
	if calls != 1 || len(tool.queries) != 0 || len(answer) != 0 {
		t.Fatalf("empty response must not be replayed: answer=%v calls=%d searches=%v", answer, calls, tool.queries)
	}
	if len(debug.Usage.ModelCalls) != 1 || debug.Usage.ModelCalls[0].TotalTokens != 9 {
		t.Fatalf("the charged response must be metered: %+v", debug.Usage.ModelCalls)
	}
}

func TestAISearchToolsFallsBackToMCPAfterEmptyNativeResponses(t *testing.T) {
	tool := &testAISearchTool{}
	nativeCalls := 0
	mcpCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		tools, hasTools := payload["tools"].([]any)
		if hasTools && len(tools) > 0 {
			nativeCalls++
			writeAIResponsesCompleted(w, "", map[string]any{})
			return
		}
		mcpCalls++
		if mcpCalls == 1 {
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"2+2"}}`, 10)
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 20)
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test",
		SearchTool: tool, MaxAISearchModelCalls: 3, MaxAISearches: 3,
		MaxAIWebReads: 1, MaxAISearchRetries: 2,
	}
	answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{
		Question: "2+2=?", Options: []string{"3", "4"}, DeepThinking: true,
	})
	if err != nil {
		t.Fatalf("empty native responses should fall back to MCP, got %v", err)
	}
	if nativeCalls != 1 || mcpCalls != 2 {
		t.Fatalf("nativeCalls=%d mcpCalls=%d", nativeCalls, mcpCalls)
	}
	if usage.Route != AISearchRouteDeepMCP {
		t.Fatalf("expected deep_mcp route, got %q", usage.Route)
	}
	if !reflect.DeepEqual(tool.queries, []string{"2+2"}) {
		t.Fatalf("fallback must search exactly once before answering: %v", tool.queries)
	}
	if !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("unexpected answers: %#v", answer)
	}
}

func TestAISearchToolsDoesNotReplayTokenBearingStreamError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			writeAIResponsesCompleted(w, "", map[string]any{"input_tokens": 8, "output_tokens": 3, "total_tokens": 11})
			fmt.Fprint(w, "data: {malformed\n\n")
			return
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "retry-model",
		SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 1, MaxAISearchRetries: 2,
	}
	answer, debug, err := client.searchAnswerWithTools(t.Context(), model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if err == nil || calls != 1 || len(answer) != 0 {
		t.Fatalf("stream error must not be replayed: answer=%v calls=%d err=%v", answer, calls, err)
	}
	if len(debug.Usage.ModelCalls) != 1 || debug.Usage.ModelCalls[0].TotalTokens != 11 {
		t.Fatalf("expected the single token-bearing response, got %+v", debug.Usage.ModelCalls)
	}
	if debug.Usage.TotalTokens != 11 {
		t.Fatalf("the charged attempt must remain in usage, got %+v", debug.Usage)
	}
}

func TestAISearchToolsAcceptsWrappedFinalAnswerJSON(t *testing.T) {
	// The helper's normal final answer is a JSON object; exercise wrapped
	// payloads directly through the shared parser to keep this test deterministic.
	for _, content := range []string{`<escape>{"answers":["4"]}</escape>`, "```json\n{\"answers\":[\"4\"]}\n```"} {
		got, err := parseAISearchAnswersJSON(content, []string{"3", "4"}, 3)
		if err != nil || !reflect.DeepEqual(got, []string{"4"}) {
			t.Fatalf("wrapped final answer content=%q: got=%v err=%v", content, got, err)
		}
	}
}

func TestAISearchToolsSafetyRetriesThenUsesBackupModel(t *testing.T) {
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
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "primary", BackupModel: "backup", SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 1, MaxAISearchRetries: 2}
	answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if err != nil || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("backup native model should answer: answer=%v err=%v", answer, err)
	}
	if calls["primary"] != 2 || calls["backup"] != 1 {
		t.Fatalf("native safety retry/fallback mismatch: calls=%v", calls)
	}
	if len(usage.ModelCalls) != 1 || usage.ModelCalls[0].Model != "backup" {
		t.Fatalf("native usage must retain backup model: %+v", usage.ModelCalls)
	}
}

func TestAISearchToolsExhaustedRoundPreservesPrivateAuditError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "temporary upstream failure", http.StatusTooEarly)
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test",
		SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 1, MaxAISearchRetries: 2,
	}
	_, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if !errors.Is(err, pkgerrors.ErrTargetServerError) {
		t.Fatalf("expected generic upstream error, got %v", err)
	}
	if !strings.Contains(err.Error(), "AI联网搜题第1轮失败") || !strings.Contains(err.Error(), "已尝试2次") || !strings.Contains(err.Error(), "HTTP 425") {
		t.Fatalf("private error should preserve round/attempt/upstream detail: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected configured total attempts 2, got %d", calls)
	}
}

func TestAISearchToolsBadRequestStopsWithoutReplay(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":{"message":"model requires Responses; account only supports Chat Completions"}}`, http.StatusBadRequest)
	}))
	defer server.Close()
	client := &ChatGptClient{
		Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test",
		SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 3, MaxAISearchRetries: 3,
	}
	_, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if calls != 1 || !errors.Is(err, pkgerrors.ErrTargetServerError) {
		t.Fatalf("bad request should stop after one call: calls=%d error=%v", calls, err)
	}
	if !strings.Contains(err.Error(), "上游服务器异常") || strings.Contains(err.Error(), "对方服务器异常") || !strings.Contains(err.Error(), "已尝试1次") {
		t.Fatalf("private diagnostics must use upstream wording and actual attempts: %v", err)
	}
	if !strings.Contains(err.Error(), "account only supports Chat Completions") {
		t.Fatalf("private diagnostics lost the upstream cause: %v", err)
	}
}

func TestAISearchToolsBadRequestDoesNotProbeAlternateEndpoint(t *testing.T) {
	calls := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		status := http.StatusBadRequest
		if r.URL.Path == "/v1/responses" {
			status = http.StatusBadGateway
		}
		http.Error(w, "temporary endpoint failure", status)
	}))
	defer server.Close()

	client := &ChatGptClient{
		Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "test",
		SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 1, MaxAISearchRetries: 1,
	}
	_, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if err == nil || !strings.Contains(err.Error(), "已尝试1次") {
		t.Fatalf("two endpoint failures in one retry cycle must count once, got %v", err)
	}
	if calls["/responses"] != 1 || calls["/v1/responses"] != 0 {
		t.Fatalf("bad request must not probe another endpoint, got %v", calls)
	}
}

func TestAISearchToolsPreservesOriginalTaskBeforeModelCall(t *testing.T) {
	for _, route := range []string{"native", "MCP", "admin-test"} {
		t.Run(route, func(t *testing.T) {
			req := model.SearchRequest{
				Question: strings.Repeat("题", 8600) + "题目末尾不可遗漏",
				Extra:    strings.Repeat("补充", 1100) + "补充末尾不可遗漏", CourseName: strings.Repeat("课程", 130) + "课程末尾",
				Type: 4, DeepThinking: true,
			}
			for index := 0; index < 30; index++ {
				req.Options = append(req.Options, fmt.Sprintf("第%d个完整选项%s末尾不可遗漏", index+1, strings.Repeat("正文", 400)))
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload openAIResponsesTestRequest
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				calls++
				var userText string
				for _, message := range payload.Messages {
					if message.Role != "user" {
						continue
					}
					if parts, ok := message.Content.([]any); ok {
						for _, raw := range parts {
							if part, ok := raw.(map[string]any); ok && part["type"] == "input_text" {
								userText += fmt.Sprint(part["text"])
							}
						}
					}
				}
				for _, original := range append([]string{req.Question, req.Extra, req.CourseName}, req.Options...) {
					if !strings.Contains(userText, original) {
						t.Errorf("original task was truncated before constructing prompt anchors: length=%d tail=%s", len([]rune(original)), string([]rune(original)[len([]rune(original))-8:]))
					}
				}
				if route == "admin-test" && calls == 1 {
					writeAIToolStream(w, "preserve-original", "关键词")
					return
				}
				writeMCPTextStream(w, `{"answers":["已完成"]}`, 10)
			}))
			defer server.Close()
			client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: &testAISearchTool{}, MaxAISearchModelCalls: 1}
			var err error
			if route == "MCP" {
				_, _, err = client.searchAnswerWithMCP(t.Context(), req)
			} else {
				toolMode := ""
				if route == "admin-test" {
					toolMode = "web_search"
				}
				_, _, err = client.searchAnswerWithToolMode(t.Context(), req, toolMode)
			}
			if err != nil || calls == 0 {
				t.Fatalf("request did not finish: calls=%d err=%v", calls, err)
			}
		})
	}
}
