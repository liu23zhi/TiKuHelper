package search

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

// The local question bank may return much larger evidence than web_search.
// Both orchestration routes must share the same output budget and preserve
// the one real lookup in usage, even when a cached repeat cannot be returned.
func TestReleaseTikuSearchRespectsSharedContentBudget(t *testing.T) {
	for _, route := range []string{"native", "mcp"} {
		t.Run(route, func(t *testing.T) {
			candidates := make([]AITikuSearchCandidate, 5)
			for i := range candidates {
				candidates[i] = AITikuSearchCandidate{
					Question: "2+2=?", Type: -1, Answers: []string{"4"},
					Source: "synthetic", Status: "unverified",
					Evidence: strings.Repeat("e", 4000), Score: 1,
				}
			}
			webTool := &testAISearchTool{}
			tikuTool := &testAITikuSearchTool{result: AITikuSearchResult{
				MatchKind: "exact", Ambiguous: true, Candidates: candidates, CandidateCount: len(candidates),
			}}
			round := 0
			const localAction = `{"action":"tiku_search","arguments":{"question":"2+2=?","type":-1,"options":["4"]}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				round++
				var payload openAIResponsesTestRequest
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				switch round {
				case 1:
					if route == "native" {
						writeAIToolStream(w, "first-web", "addition")
					} else {
						writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"addition"}}`, 10)
					}
				case 2, 3:
					if route == "native" {
						writeAITikuSearchToolStream(w, fmt.Sprintf("local-%d", round), map[string]any{"question": "2+2=?", "type": -1, "options": []string{"4"}})
					} else {
						writeMCPTextStream(w, localAction, 10)
					}
				case 4:
					totalEvidence, successes, budgetErrors := 0, 0, 0
					for _, item := range payload.Messages {
						output, _ := item.Output.(string)
						if route == "mcp" && item.Role == "user" {
							if text, ok := item.Content.(string); ok && strings.HasPrefix(text, "本地MCP工具 ") {
								_, output, _ = strings.Cut(text, "\n")
							}
						}
						if strings.Contains(output, `"matchKind":`) {
							var result AITikuSearchResult
							if err := json.Unmarshal([]byte(output), &result); err != nil ||
								!result.Ambiguous || result.CandidateCount != len(result.Candidates) || result.CandidateCount == 0 || result.CandidateCount >= len(candidates) {
								t.Errorf("local evidence must be a bounded JSON shortlist retaining ambiguity; candidates=%d count=%d err=%v", len(result.Candidates), result.CandidateCount, err)
							}
							totalEvidence += len(output)
							successes++
						}
						if strings.Contains(output, `"total_results":`) {
							totalEvidence += len(output)
						}
						if strings.Contains(output, "累计资料预算") {
							budgetErrors++
						}
					}
					if totalEvidence == 0 || totalEvidence > aiToolMaxContentBytes || successes != 1 || budgetErrors != 1 {
						t.Errorf("round exceeded shared content budget: bytes=%d successes=%d budgetErrors=%d", totalEvidence, successes, budgetErrors)
					}
					if route == "native" {
						if len(payload.Tools) != 0 {
							t.Error("budget exhaustion must force the final answer without tools")
						}
						writeAIFinalStream(w)
					} else {
						writeMCPTextStream(w, `{"answers":["4"]}`, 10)
					}
				default:
					t.Errorf("unexpected model round %d", round)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()

			client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model",
				SearchTool: webTool, TikuSearchTool: tikuTool, MaxAISearches: 4, MaxAISearchModelCalls: 5, MaxAISearchRetries: 1}
			req := model.SearchRequest{Question: "2+2=?", Type: -1, Options: []string{"4"}, DeepThinking: true}
			var answers [][]string
			var usage AISearchUsage
			var err error
			if route == "native" {
				answers, usage, err = client.SearchAnswerWithUsage(req)
			} else {
				var debug AISearchDebugResult
				answers, debug, err = client.searchAnswerWithMCP(t.Context(), req)
				usage = debug.Usage
			}
			if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
				t.Fatalf("answers=%v err=%v", answers, err)
			}
			if len(webTool.queries) != 1 || tikuTool.calls != 1 || len(usage.TikuSearchCalls) != 1 ||
				usage.TikuSearchCalls[0].Status != "success" || usage.TikuSearchCalls[0].CandidateCount != len(candidates) {
				t.Fatalf("real usage lost, extra lookup, or rebilled cached evidence: web=%d local=%d usage=%v",
					len(webTool.queries), tikuTool.calls, usage.TikuSearchCalls)
			}
		})
	}
}

// A provider can emit multiple function calls even though only the first
// web search was required. It must not execute an unadvertised local lookup.
func TestReleaseNativeTikuCannotRunBeforeItWasOffered(t *testing.T) {
	webTool := &testAISearchTool{}
	tikuTool := &testAITikuSearchTool{}
	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		round++
		var payload openAIResponsesTestRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if round == 1 {
			for _, tool := range payload.Tools {
				if tool.Name == "tiku_search" {
					t.Error("local lookup was advertised before the initial web search")
				}
			}
			web := map[string]any{"type": "function_call", "id": "web", "call_id": "web", "name": "web_search",
				"arguments": `{"query":"addition"}`}
			local := map[string]any{"type": "function_call", "id": "local", "call_id": "local", "name": "tiku_search",
				"arguments": `{"question":"2+2=?","type":-1,"options":["4"]}`}
			w.Header().Set("Content-Type", "text/event-stream")
			writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{
				"id": "two-tool-calls", "status": "completed", "output": []any{web, local},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14},
			}})
			return
		}
		rejected := false
		for _, item := range payload.Messages {
			output, _ := item.Output.(string)
			if item.CallID == "local" && strings.Contains(output, "本轮未提供本地题库检索工具") {
				rejected = true
			}
		}
		if !rejected {
			t.Error("next round did not receive rejection of the unadvertised call")
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "test-model",
		SearchTool: webTool, TikuSearchTool: tikuTool, MaxAISearches: 4, MaxAISearchModelCalls: 2, MaxAISearchRetries: 1}
	answers, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", Type: -1, Options: []string{"4"}, DeepThinking: true})
	if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	if len(webTool.queries) != 1 || tikuTool.calls != 0 || len(usage.TikuSearchCalls) != 0 {
		t.Fatalf("unadvertised tool executed or billed: web=%d local=%d usage=%d", len(webTool.queries), tikuTool.calls, len(usage.TikuSearchCalls))
	}
}
