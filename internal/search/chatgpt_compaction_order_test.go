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

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestOrderedCompressionServerUsesNormalResponsesAndPreservesAnchors(t *testing.T) {
	anchors, input := compactionTestInput()
	usage := AISearchUsage{}
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "remote", "local"}, ContextWindowTokens: 20000}}
	manager := client.newAIContextCompactor(anchors, &usage)
	prepared, err := manager.prepare(context.Background(), "invalid", input, false, 1)
	if err != nil || !reflect.DeepEqual(prepared, input) || len(usage.ModelCalls) != 0 {
		t.Fatalf("automatic server strategy must await ordinary response: %v", err)
	}
	if config := manager.requestContextManagement(); len(config) != 1 || config[0].CompactThreshold != 16000 {
		t.Fatalf("missing server compaction setting: %+v", config)
	}
	output := []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`), json.RawMessage(`{"type":"function_call","call_id":"next","name":"web_search","arguments":"{}"}`)}
	prepared = manager.acceptServerOutput(prepared, output)
	if manager.lastStrategy != "server" || !reflect.DeepEqual(prepared[len(prepared)-len(anchors):], anchors) || len(usage.ModelCalls) != 0 {
		t.Fatalf("server compression lost anchors or fabricated usage: %+v", manager)
	}
	prepared = append(prepared, map[string]any{"type": "function_call_output", "call_id": "next", "output": "latest evidence"})
	if !manager.handleServerFailure(fmt.Errorf("invalid encrypted compaction content")) {
		t.Fatal("unsupported encrypted state did not advance fallback")
	}
	recovered := manager.expandServerInput(prepared)
	encoded, _ := json.Marshal(recovered)
	for _, want := range []string{"Historical evidence", "latest evidence", "call-search", "next"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("fallback lost history %q", want)
		}
	}
	if strings.Contains(string(encoded), `"type":"compaction"`) {
		t.Fatal("fallback retained unsupported server state instead of original history")
	}
}

func TestNativeServerCompressionPreservesToolPairAndBillsOnlyRealCalls(t *testing.T) {
	tool := &contextRecoverySearchTool{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if r.URL.Path != "/v1/responses" || payload["context_management"] == nil || payload["max_output_tokens"] != nil {
			t.Errorf("automatic compression must share ordinary unbounded answer request: %+v", payload)
		}
		if calls == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{
				"status": "completed", "output": []any{
					map[string]any{"type": "function_call", "call_id": "native-server", "name": "web_search", "arguments": `{"query":"verified evidence"}`},
					map[string]any{"type": "compaction", "encrypted_content": "opaque-native", "future_field": "keep"},
				}, "usage": map[string]any{"input_tokens": 100, "output_tokens": 10, "total_tokens": 110},
			}})
			return
		}
		encoded, _ := json.Marshal(payload["input"])
		for _, want := range []string{"opaque-native", "future_field", `"call_id":"native-server"`, `"type":"function_call_output"`, "PROTECTED question", "上下文压缩后的执行规则"} {
			if !strings.Contains(string(encoded), want) {
				t.Errorf("missing %q in continued context", want)
			}
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 2,
		ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "remote", "local"}, ContextWindowTokens: 100000}}
	answers, debug, err := client.searchAnswerWithToolMode(t.Context(), model.SearchRequest{Question: "PROTECTED question", Type: 2}, "")
	if err != nil || !reflect.DeepEqual(answers, [][]string{{"4"}}) || calls != 2 || tool.calls != 1 || len(debug.Usage.ModelCalls) != 2 || debug.Usage.TotalTokens != 133 {
		t.Fatalf("calls=%d tools=%d usage=%+v answers=%v err=%v", calls, tool.calls, debug.Usage, answers, err)
	}
}

func TestOrderedCompressionFailureAndCancellationStop(t *testing.T) {
	anchors, input := compactionTestInput()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"unsupported compression request"}`)
	}))
	defer server.Close()
	client := &ChatGptClient{BaseURL: server.URL + "/v1", ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"local", "remote", "server"}, MaxAttempts: 1, ContextWindowTokens: 20000}}
	manager := client.newAIContextCompactor(anchors, &AISearchUsage{})
	if _, err := manager.prepare(t.Context(), server.URL+"/v1/responses", input, true, 1); err == nil || calls != 3 {
		t.Fatalf("must stop after configured chain: calls=%d err=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.newAIContextCompactor(anchors, &AISearchUsage{}).prepare(ctx, server.URL+"/v1/responses", input, true, 1); err == nil || calls != 3 {
		t.Fatalf("cancelled chain invoked upstream: %v", err)
	}
}

func TestOrderedCompressionRespectsOrderAndReportsFallback(t *testing.T) {
	for _, order := range [][]string{{"server", "remote", "local"}, {"local", "server", "remote"}, {"remote", "local", "server"}} {
		t.Run(strings.Join(order, "-"), func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				switch {
				case strings.HasSuffix(r.URL.Path, "/compact"):
					calls = append(calls, "remote")
					w.WriteHeader(404)
					_, _ = fmt.Fprint(w, `{"error":"unsupported endpoint"}`)
				case payload["context_management"] != nil:
					calls = append(calls, "server")
					w.WriteHeader(400)
					_, _ = fmt.Fprint(w, `{"error":"unsupported context_management"}`)
				default:
					calls = append(calls, "local")
					writeCompactionTestSummary(w, "Preserved historical facts and pending verification.", "completed")
				}
			}))
			defer server.Close()
			client := compressionDiagnosticClient(server.URL + "/v1")
			client.ContextCompression.StrategyOrder = order
			client.ContextCompression.MaxAttempts = 1
			result, _, err := client.TestContextCompression(context.Background(), strings.Repeat("Historical evidence. ", 1500), "configured")
			want := []string{}
			for _, strategy := range order {
				want = append(want, strategy)
				if strategy == "local" {
					break
				}
			}
			if err != nil || !result.Success || result.Strategy != "local" || !reflect.DeepEqual(calls, want) || !reflect.DeepEqual(result.AttemptedStrategies, want) {
				t.Fatalf("calls=%v want=%v result=%+v err=%v", calls, want, result, err)
			}
		})
	}
}

func TestServerCompressionDiagnosticRequiresActualCompaction(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				output := []any{}
				if valid {
					output = append(output, map[string]any{"type": "compaction", "encrypted_content": "private-state"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"object": "response", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 4000, "output_tokens": 50, "total_tokens": 4050}})
			}))
			defer server.Close()
			client := compressionDiagnosticClient(server.URL + "/v1")
			client.ContextCompression.MaxAttempts = 1
			result, debug, err := client.TestContextCompression(context.Background(), strings.Repeat("History. ", 2000), "server")
			if result.Success != valid || (err == nil) != valid || len(debug.Usage.ModelCalls) != 1 || debug.Usage.TotalTokens != 4050 {
				t.Fatalf("valid=%v result=%+v usage=%+v err=%v", valid, result, debug.Usage, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "private-state") {
				t.Fatal("opaque state exposed in diagnostics")
			}
		})
	}
}

func TestServerCompressionFailureClassification(t *testing.T) {
	for _, test := range []struct {
		status               int
		detail               string
		immediate, exhausted bool
	}{
		{400, "unsupported context_management", true, false},
		{422, "invalid encrypted compaction content", true, false},
		{400, "context_length_exceeded", true, false},
		{503, "compaction service temporarily unavailable", false, true},
		{401, "invalid context_management credential", false, false},
		{403, "unsupported compaction access", false, false},
		{200, "invalid answer JSON", false, false},
	} {
		t.Run(fmt.Sprintf("%d-%s", test.status, test.detail), func(t *testing.T) {
			client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server", "local"}}}
			manager := client.newAIContextCompactor(nil, nil)
			err := &AIUpstreamError{StatusCode: test.status, Detail: test.detail}
			if got := manager.handleServerFailure(err); got != test.immediate {
				t.Fatalf("immediate=%v want=%v", got, test.immediate)
			}
			manager = client.newAIContextCompactor(nil, nil)
			if got := manager.handleServerExhausted(test.status, err); got != test.exhausted {
				t.Fatalf("exhausted=%v want=%v", got, test.exhausted)
			}
		})
	}
}
