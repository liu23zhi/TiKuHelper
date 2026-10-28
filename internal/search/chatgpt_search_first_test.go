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

func TestResearchRequiresInitialSearchWhenBudgetAllowsIt(t *testing.T) {
	for _, route := range []string{"native", "MCP"} {
		t.Run(route, func(t *testing.T) {
			for _, response := range []string{"final", "web_read"} {
				t.Run(response, func(t *testing.T) {
					calls := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						if response == "web_read" {
							if route == "native" {
								writeAIWebReadToolStream(w, "read-before-search", "https://example.com/")
							} else {
								writeMCPTextStream(w, `{"action":"web_read","arguments":{"url":"https://example.com/"}}`, 10)
							}
							return
						}
						writeMCPTextStream(w, `{"answers":["4"]}`, 10)
					}))
					defer server.Close()
					tool := &testAISearchTool{}
					client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 2, MaxAISearchRetries: 1}
					var debug AISearchDebugResult
					var err error
					if route == "native" {
						_, debug, err = client.searchAnswerWithToolMode(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 2}, "")
					} else {
						_, debug, err = client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 2})
					}
					if err == nil || !strings.Contains(err.Error(), "首次工具调用必须为 web_search") {
						t.Fatalf("answer without the required search must not be accepted: %v", err)
					}
					if calls != 1 || len(tool.queries) != 0 || len(debug.Usage.ModelCalls) != 1 || debug.Usage.TotalTokens != 14 {
						t.Fatalf("do not replay a paid response or invent a search: calls=%d queries=%v usage=%+v", calls, tool.queries, debug.Usage)
					}
				})
			}
		})
	}
}

func TestMCPFirstSearchThenFinalUsesUncappedRequests(t *testing.T) {
	tool := &testAISearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, exists := payload["max_output_tokens"]; exists {
			t.Error("MCP answer requests must not impose a fixed output limit")
		}
		if calls == 1 {
			input, _ := json.Marshal(payload["input"])
			if !strings.Contains(string(input), "本轮首次工具调用必须为 web_search") {
				t.Error("the first MCP round must require a search action")
			}
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"2+2"}}`, 10)
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 20)
	}))
	defer server.Close()
	client := &ChatGptClient{BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 2, MaxAISearchRetries: 1}
	answers, _, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 2})
	if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) || !reflect.DeepEqual(tool.queries, []string{"2+2"}) || calls != 2 {
		t.Fatalf("search must fit before final synthesis: answers=%v queries=%v calls=%d err=%v", answers, tool.queries, calls, err)
	}
}

func TestSingleModelCallDoesNotConsumeUnavailableSearchRound(t *testing.T) {
	for _, route := range []string{"native", "MCP"} {
		t.Run(route, func(t *testing.T) {
			tool := &testAISearchTool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeMCPTextStream(w, `{"answers":["4"]}`, 10)
			}))
			defer server.Close()
			client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 1, MaxAISearchRetries: 1}
			var answers [][]string
			var err error
			if route == "native" {
				answers, _, err = client.searchAnswerWithToolMode(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 2}, "")
			} else {
				answers, _, err = client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 2})
			}
			if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) || len(tool.queries) != 0 {
				t.Fatalf("one model opportunity must remain a final answer: answers=%v queries=%v err=%v", answers, tool.queries, err)
			}
		})
	}
}

func TestResearchWithoutSearchToolOnlyAnswersFromExistingInformation(t *testing.T) {
	for _, route := range []string{"native", "MCP"} {
		t.Run(route, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if _, exists := payload["tools"]; exists {
					t.Error("unavailable tools must not be advertised")
				}
				writeMCPTextStream(w, `{"answers":["4"]}`, 10)
			}))
			defer server.Close()
			client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", MaxAISearchModelCalls: 2, MaxAISearchRetries: 1}
			var answers [][]string
			var debug AISearchDebugResult
			var err error
			if route == "native" {
				answers, debug, err = client.searchAnswerWithToolMode(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 2}, "")
			} else {
				answers, debug, err = client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "2+2=?", Type: 2})
			}
			if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) || calls != 1 || len(debug.Usage.SearchCalls) != 0 {
				t.Fatalf("do not invent unavailable searches: answers=%v calls=%d usage=%+v err=%v", answers, calls, debug.Usage, err)
			}
		})
	}
}
