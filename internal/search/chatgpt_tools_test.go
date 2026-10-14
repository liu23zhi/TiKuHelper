package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/model"
)

type testAISearchTool struct {
	queries []string
	err     error
}

func (tool *testAISearchTool) SearchAggregate(ctx context.Context, req uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	tool.queries = append(tool.queries, req.Query)
	return uapi.SearchAggregateResponse{Query: req.Query, TotalResults: 1, Results: []uapi.SearchResult{{Title: "参考资料", URL: "https://example.com/source", Snippet: "材料给出的答案为4。"}}}, tool.err
}

func writeAIToolStream(w http.ResponseWriter, callID, query string) {
	w.Header().Set("Content-Type", "text/event-stream")
	arguments, _ := json.Marshal(map[string]string{"query": query})
	first, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": callID, "type": "function", "function": map[string]any{"name": "web_search", "arguments": string(arguments[:len(arguments)/2])}}}}}}})
	second, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": string(arguments[len(arguments)/2:])}}}}, "finish_reason": "tool_calls"}}})
	fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", first, second)
	fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14,\"prompt_tokens_details\":{\"cached_tokens\":2},\"cache_creation\":{\"ephemeral_5m_input_tokens\":1}}}\n\ndata: [DONE]\n\n")
}

func writeAIFinalStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"answers\\\":[\\\"4\\\"]}\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":3,\"total_tokens\":23}}\n\ndata: [DONE]\n\n")
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
		if payload["max_tokens"] != float64(4096) {
			t.Errorf("missing output bound: %v", payload["max_tokens"])
		}
		round++
		if round == 1 {
			choice, ok := payload["tool_choice"].(map[string]any)
			if !ok || choice["type"] != "function" {
				t.Errorf("first round must require search: %v", payload["tool_choice"])
			}
			writeAIToolStream(w, "search1", "二加二 计算")
			return
		}
		messages := payload["messages"].([]any)
		foundSearchEvidence := false
		for _, rawMessage := range messages {
			message, ok := rawMessage.(map[string]any)
			if !ok || message["role"] != "tool" {
				continue
			}
			content, ok := message["content"].(string)
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
	answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", Options: []string{"3", "4"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(answer, [][]string{{"4"}}) || len(tool.queries) != 2 || round != 3 {
		t.Fatalf("answer=%v queries=%v rounds=%d", answer, tool.queries, round)
	}
	if usage != (AISearchUsage{InputTokens: 40, CachedInputTokens: 4, CacheWriteTokens: 2, OutputTokens: 11, TotalTokens: 51}) {
		t.Fatalf("usage=%+v", usage)
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
			if _, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "题目"}); err != nil {
				t.Fatal(err)
			}
			if len(tool.queries) != tc.calls {
				t.Fatalf("actual searches=%d want=%d", len(tool.queries), tc.calls)
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
	_, debug, err := client.SearchAnswerDebug(model.SearchRequest{Question: "编写完整项目，生成war、PDF及截图", Type: -4})
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
			messages := payload["messages"].([]any)
			budget := messages[len(messages)-1].(map[string]any)["content"].(string)
			if !strings.Contains(budget, "还剩模型调用 0 次") || !strings.Contains(budget, "最后一次模型调用") {
				t.Fatalf("missing final budget instruction: %s", budget)
			}
			writeAIFinalStream(w)
		}))
		defer server.Close()

		client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 1, MaxAISearches: 3}
		answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?"})
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
			messages := payload["messages"].([]any)
			budget := messages[len(messages)-1].(map[string]any)["content"].(string)
			if !strings.Contains(budget, "web_search 搜索还剩 0 次") {
				t.Fatalf("missing exhausted search budget: %s", budget)
			}
			writeAIFinalStream(w)
		}))
		defer server.Close()

		client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 3, MaxAISearches: 1}
		answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "预算测试"})
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
		if r.URL.Path == "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if _, exists := payload["tools"]; exists {
			t.Error("fallback is the last model call and must answer without tools")
		}
		messages := payload["messages"].([]any)
		budget := messages[len(messages)-1].(map[string]any)["content"].(string)
		if !strings.Contains(budget, "还剩模型调用 0 次") || !strings.Contains(budget, "最后一次模型调用") {
			t.Errorf("missing final budget on fallback: %s", budget)
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 2, MaxAISearches: 3}
	answer, _, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?"})
	if err != nil || calls != 2 || len(tool.queries) != 0 || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("answer=%v calls=%d searches=%v err=%v", answer, calls, tool.queries, err)
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
		Question: strings.Repeat("题", 100000),
		Options:  []string{strings.Repeat("选项", 10000)},
	})
	if err == nil || requestBytes > 200000 {
		t.Fatalf("oversized task should be rejected by the required search round, requestBytes=%d err=%v", requestBytes, err)
	}
}
