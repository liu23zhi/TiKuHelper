package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestAISearchToolsSearchTwiceAndAccumulateUsage(t *testing.T) {
	tool := &testAISearchTool{}
	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if payload["max_output_tokens"] != float64(4096) {
			t.Errorf("missing output bound: %v", payload["max_output_tokens"])
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
			http.Error(w, "temporary upstream failure", http.StatusBadGateway)
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

func TestAISearchToolsRetainsTokenBearingRetryResponses(t *testing.T) {
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
	answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if err != nil || calls != 2 || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("retry should recover one failed round: answer=%v calls=%d err=%v", answer, calls, err)
	}
	if len(usage.ModelCalls) != 2 || usage.ModelCalls[0].TotalTokens != 11 || usage.ModelCalls[1].TotalTokens != 23 {
		t.Fatalf("expected both token-bearing model responses, got %+v", usage.ModelCalls)
	}
	if usage.TotalTokens != 34 {
		t.Fatalf("tool retry aggregate should include both actual responses, got %+v", usage)
	}
}

func TestAISearchToolsExhaustedRoundPreservesPrivateAuditError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "temporary upstream failure", http.StatusBadGateway)
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
	if !strings.Contains(err.Error(), "AI联网搜题第1轮失败") || !strings.Contains(err.Error(), "已尝试2次") || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("private error should preserve round/attempt/upstream detail: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected configured total attempts 2, got %d", calls)
	}
}

func TestAISearchToolsRetriesBadRequestAndReportsConfiguredAttemptCount(t *testing.T) {
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
	if calls != 3 || !errors.Is(err, pkgerrors.ErrTargetServerError) {
		t.Fatalf("failed model calls must use all configured attempts: calls=%d error=%v", calls, err)
	}
	if !strings.Contains(err.Error(), "上游服务器异常") || strings.Contains(err.Error(), "对方服务器异常") || !strings.Contains(err.Error(), "已尝试3次") {
		t.Fatalf("private diagnostics must use upstream wording and actual attempts: %v", err)
	}
	if !strings.Contains(err.Error(), "account only supports Chat Completions") {
		t.Fatalf("private diagnostics lost the upstream cause: %v", err)
	}
}

func TestAISearchToolsEndpointFailuresCountAsOneRetryAttempt(t *testing.T) {
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
	if calls["/responses"] != 1 || calls["/v1/responses"] != 1 {
		t.Fatalf("expected both endpoint requests within one retry cycle, got %v", calls)
	}
}

func TestAISearchToolsBoundsOriginalTaskBeforeModelCall(t *testing.T) {
	tool := &testAISearchTool{}
	var requestBytes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		requestBytes = len(body)
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool}
	_, _, err := client.SearchAnswerWithUsage(model.SearchRequest{
		Question:     strings.Repeat("题", 100000),
		DeepThinking: true,
		Options:      []string{strings.Repeat("选项", 10000)},
	})
	if err == nil || requestBytes > 200000 {
		t.Fatalf("oversized task should be rejected by the required search round, requestBytes=%d err=%v", requestBytes, err)
	}
}
