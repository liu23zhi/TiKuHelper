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

// The positive upstream recovery fixtures need one additional summary opportunity
// under the confirmed local shared budget. These lower-budget counterparts must
// still refuse recovery, without replaying tools or losing already billed usage.
type upstreamIntegrationBudgetTool struct {
	inner   aiMCPLargeSearchTool
	queries []string
}

func (tool *upstreamIntegrationBudgetTool) SearchAggregate(ctx context.Context, req uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	tool.queries = append(tool.queries, req.Query)
	return tool.inner.SearchAggregate(ctx, req)
}

func TestUpstreamMCPRecoveryRetainsLocalSharedBudgetAndExecutedUsage(t *testing.T) {
	for _, kind := range []string{"server_context_limit", "ignored_server_compaction"} {
		t.Run(kind, func(t *testing.T) {
			budget := 2
			if kind == "ignored_server_compaction" {
				budget = 3
			}
			tool := &upstreamIntegrationBudgetTool{}
			modelCalls, summaries := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" {
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("invalid synthetic request: %v", err)
				}
				if stream, _ := payload["stream"].(bool); !stream {
					summaries++
					t.Error("exhausted shared budget must refuse summary before its model request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				modelCalls++
				if payload["max_output_tokens"] != float64(aiToolMaxOutputTokens) {
					t.Errorf("formal request lost local output cap: %v", payload["max_output_tokens"])
				}
				if payload["context_management"] == nil {
					t.Error("original server strategy must run before fallback")
				}
				switch modelCalls {
				case 1:
					if kind == "server_context_limit" {
						writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"historical evidence"}}`, 10)
						return
					}
					action, _ := json.Marshal(map[string]any{
						"action": "web_search", "arguments": map[string]string{"query": "first evidence"},
						"notes": strings.Repeat("Preserved historical fact. ", 1000),
					})
					writeMCPTextStream(w, string(action), 10)
				case 2:
					if kind == "server_context_limit" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"maximum context length exceeded"}}`)
						return
					}
					if estimateAIContextTokens([]any{payload["input"]}) < 10000 {
						t.Error("negative ignored-compaction fixture must reach the actual server threshold")
					}
					writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"second evidence"}}`, 20000)
				default:
					t.Error("budget rejection must not replay a model request or invent a final answer")
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			window := int64(100000)
			trigger, target := 80, 50
			wantQueries := []string{"historical evidence"}
			wantInputs := []int64{10}
			wantTokens := int64(14)
			if kind == "ignored_server_compaction" {
				window, trigger, target = 20000, 50, 30
				wantQueries = []string{"first evidence", "second evidence"}
				wantInputs = []int64{10, 20000}
				wantTokens = 20018
			}
			client := &ChatGptClient{
				BaseURL: server.URL + "/v1", Model: "test-model", Prompt: "Keep facts and return JSON.", SearchTool: tool,
				MaxAISearchModelCalls: budget, MaxAISearches: 2, MaxAIWebReads: 1, MaxAISearchRetries: 1,
				ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "local", "remote"}, ContextWindowTokens: window, TriggerPercent: trigger, TargetPercent: target, MaxAttempts: 1},
			}
			answers, debug, err := client.searchAnswerWithMCP(t.Context(), model.SearchRequest{Question: "protected-question", Type: 4})
			if err == nil || !strings.Contains(err.Error(), errAIContextModelBudgetExceeded.Error()) || len(answers) != 0 {
				t.Fatalf("must reject rather than claim an unexecuted final answer: answers=%v err=%v", answers, err)
			}
			if modelCalls != 2 || summaries != 0 || modelCalls+summaries > budget {
				t.Fatalf("actual model/summary requests escaped local shared budget: formal=%d summary=%d budget=%d", modelCalls, summaries, budget)
			}
			if tool.inner.calls != len(wantQueries) || !reflect.DeepEqual(tool.queries, wantQueries) || len(debug.Usage.SearchCalls) != len(wantQueries) {
				t.Fatalf("executed tools were replayed or usage lost: actual=%v usage=%+v", tool.queries, debug.Usage.SearchCalls)
			}
			if len(debug.Usage.ModelCalls) != len(wantInputs) || debug.Usage.TotalTokens != wantTokens {
				t.Fatalf("already executed model usage must be delivered once despite budget rejection: %+v", debug.Usage)
			}
			for i, call := range debug.Usage.ModelCalls {
				if call.Index != i+1 || call.InputTokens != wantInputs[i] || call.OutputTokens != 4 || call.TotalTokens != wantInputs[i]+4 {
					t.Fatalf("lost/duplicated model usage at index%d: %+v", i, call)
				}
			}
			for i, call := range debug.Usage.SearchCalls {
				if call.Query != wantQueries[i] {
					t.Fatalf("executed tool usage did not retain real query order: %+v", debug.Usage.SearchCalls)
				}
			}
			t.Logf("UPSTREAM_LOCAL_BUDGET_REJECTION budget=%d physicalModelRequests=%d summaryRequests=%d executedTools=%d deliveredModelUsage=%d deliveredTokens=%d noFakeFinalAnswer=true", budget, modelCalls, summaries, tool.inner.calls, len(debug.Usage.ModelCalls), debug.Usage.TotalTokens)
		})
	}
}
