package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/logger"
)

func captureCompressionThinkingLog(t *testing.T, invoke func() (AIContextCompressionTestResult, AISearchDebugResult, error)) (string, AIContextCompressionTestResult, AISearchDebugResult, error) {
	t.Helper()
	dir := t.TempDir()
	previousDir := *logger.LogDir
	*logger.LogDir = dir
	t.Cleanup(func() {
		_ = logger.Close()
		*logger.LogDir = previousDir
	})
	if err := logger.SetupGinLog(); err != nil {
		t.Fatal(err)
	}
	result, debug, callErr := invoke()
	// Close before reading and TempDir cleanup, including on Windows.
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Tiku-thinking.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data), result, debug, callErr
}

func compressionLogEvents(t *testing.T, content string, name string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(content, "\n") {
		const marker = "AI压缩事件 "
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(line[index+len(marker):])), &event); err != nil {
			t.Fatalf("compression event is not one parseable JSON line: %v", err)
		}
		if event["event"] == name {
			events = append(events, event)
		}
	}
	return events
}

func assertCompressionLogSource(t *testing.T, event map[string]any, endpoint, requestID, strategy string, index int) {
	t.Helper()
	for key, want := range map[string]any{
		"requestId": requestID, "strategy": strategy, "round": float64(1), "attempt": float64(1),
		"attemptBudget": float64(1), "endpoint": endpoint + "/responses", "httpStatus": "200", "modelCallIndex": float64(index),
	} {
		if event[key] != want {
			t.Errorf("event %v field %s=%v want=%v", event["event"], key, event[key], want)
		}
	}
}

func TestCompressionThinkingLogExplainsServerValidationAndFallback(t *testing.T) {
	for _, fixture := range []struct {
		name, status, incompleteReason, wantError, rejectionCode string
		withCompaction, oversized, hasUnknownType                bool
	}{
		{name: "incomplete-with-valid-compaction", status: "incomplete", incompleteReason: "max_output_tokens", withCompaction: true, wantError: "未完成", rejectionCode: "response_not_completed"},
		{name: "completed-without-compaction", status: "completed", wantError: "未返回有效压缩项", rejectionCode: "missing_compaction"},
		{name: "completed-without-reduction", status: "completed", withCompaction: true, oversized: true, hasUnknownType: true, wantError: "未缩小", rejectionCode: "context_not_reduced"},
	} {
		for _, fallback := range []bool{false, true} {
			t.Run(fixture.name+map[bool]string{false: "/server-only", true: "/fallback-local"}[fallback], func(t *testing.T) {
				const privateKey = "sk-compression-logging-private-key"
				const privateOpaque = "private-compaction-state-do-not-log"
				const privateType = "private-output-type-do-not-log"
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						return
					}
					if payload["context_management"] == nil {
						writeCompactionTestSummary(w, "Retained source facts and pending verification.", "completed")
						return
					}
					output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Preserved history."}}}}
					if fixture.withCompaction {
						opaque := privateOpaque
						if fixture.oversized {
							opaque = strings.Repeat(privateOpaque, 4000)
						}
						output = append(output, map[string]any{"type": "compaction", "encrypted_content": opaque})
					}
					if fixture.hasUnknownType {
						output = append(output, map[string]any{"type": privateType, "encrypted_content": privateOpaque})
					}
					response := map[string]any{"object": "response", "status": fixture.status, "output": output,
						"usage": map[string]any{"input_tokens": 4000, "output_tokens": 50, "total_tokens": 4050}}
					if fixture.incompleteReason != "" {
						response["incomplete_details"] = map[string]any{"reason": fixture.incompleteReason}
					}
					_ = json.NewEncoder(w).Encode(response)
				}))
				defer server.Close()
				endpoint := server.URL + "/v1"
				client := compressionDiagnosticClient(endpoint)
				client.APIKey = privateKey
				client.RequestID = "compression-log-" + fixture.name
				client.ContextCompression.MaxAttempts = 1
				client.ContextCompression.StrategyOrder = []string{"server"}
				if fallback {
					client.ContextCompression.StrategyOrder = append(client.ContextCompression.StrategyOrder, "local")
				}
				content, result, debug, err := captureCompressionThinkingLog(t, func() (AIContextCompressionTestResult, AISearchDebugResult, error) {
					return client.TestContextCompression(t.Context(), strings.Repeat("Historical evidence and unfinished verification. ", 1500), "configured")
				})
				if result.Success != fallback || (err == nil) != fallback {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				rejected := compressionLogEvents(t, content, "compression_validation_rejected")
				if len(rejected) != 1 {
					t.Fatalf("validation must persist exactly once after HTTP success, got %d", len(rejected))
				}
				event := rejected[0]
				assertCompressionLogSource(t, event, endpoint, client.RequestID, "server", 1)
				if event["responseStatus"] != fixture.status || event["incompleteReason"] != fixture.incompleteReason || !strings.Contains(event["error"].(string), fixture.wantError) {
					t.Errorf("validation did not preserve status and precise rejection: %+v", event)
				}
				if event["rejectionCode"] != fixture.rejectionCode || event["tokenEstimate"] != true || event["compactThreshold"] != float64(1000) || event["maxOutputTokens"] != float64(512) {
					t.Errorf("validation omitted machine-readable cause or effective probe limits: %+v", event)
				}
				wantOutputCount, wantCompactions := 1, 0
				if fixture.withCompaction {
					wantOutputCount++
					wantCompactions++
				}
				if fixture.hasUnknownType {
					wantOutputCount++
				}
				if event["outputItemCount"] != float64(wantOutputCount) || event["validCompactionCount"] != float64(wantCompactions) {
					t.Errorf("validation output counts incorrect: %+v", event)
				}
				types := event["outputTypes"].(map[string]any)
				if types["message"] != float64(1) || fixture.withCompaction && types["compaction"] != float64(1) || fixture.hasUnknownType && types["unknown"] != float64(1) {
					t.Errorf("allowlisted output types incorrect: %+v", types)
				}
				for _, key := range []string{"beforeTokens", "afterTokens", "targetTokens"} {
					if value, ok := event[key].(float64); !ok || value <= 0 {
						t.Errorf("missing estimated %s: %v", key, event[key])
					}
				}
				fallbacks := compressionLogEvents(t, content, "compression_strategy_fallback")
				wantCalls, wantTokens := 1, int64(4050)
				if fallback {
					wantCalls, wantTokens = 2, 4170
					if len(fallbacks) != 1 || fallbacks[0]["from"] != "server" || fallbacks[0]["to"] != "local" {
						t.Errorf("fallback did not log from/to: %+v", fallbacks)
					}
					finished := compressionLogEvents(t, content, "compression_finished")
					if len(finished) != 1 {
						t.Fatalf("expected one finished local compression event, got %d", len(finished))
					}
					assertCompressionLogSource(t, finished[0], endpoint, client.RequestID, "local", 2)
				} else {
					failed := compressionLogEvents(t, content, "compression_failed")
					if len(fallbacks) != 0 || len(failed) != 1 {
						t.Fatalf("single strategy failure event missing or fallback invented")
					}
					assertCompressionLogSource(t, failed[0], endpoint, client.RequestID, "server", 1)
				}
				if calls != wantCalls || len(debug.Usage.ModelCalls) != wantCalls || len(debug.Usage.Trace) != wantCalls || len(debug.Trace) != wantCalls || debug.Usage.TotalTokens != wantTokens {
					t.Errorf("diagnostic logging changed requests, usage or billing trace: calls=%d usage=%+v", calls, debug.Usage)
				}
				if debug.Usage.Trace[0].Error == "" || !strings.Contains(debug.Usage.Trace[0].ResultSummary, "responseStatus") {
					t.Error("existing request trace was not updated with validation rejection")
				}
				for _, secret := range []string{privateKey, privateOpaque, privateType} {
					if strings.Contains(content, secret) {
						t.Error("compression log exposes a key, opaque state or untrusted output type")
					}
				}
			})
		}
	}
}

func TestCompressionThinkingLogRecordsMalformedLocalSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "response", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "not a JSON summary"}}}},
			"usage":  map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120}})
	}))
	defer server.Close()
	client := compressionDiagnosticClient(server.URL + "/v1")
	client.RequestID = "compression-log-invalid-local"
	client.ContextCompression.MaxAttempts = 1
	content, result, debug, err := captureCompressionThinkingLog(t, func() (AIContextCompressionTestResult, AISearchDebugResult, error) {
		return client.TestContextCompression(t.Context(), strings.Repeat("Historical source facts. ", 1000), "local")
	})
	if err == nil || result.Success || debug.Usage.TotalTokens != 120 || len(debug.Usage.ModelCalls) != 1 || len(debug.Usage.Trace) != 1 {
		t.Fatalf("invalid summary must fail with exactly its actual usage: result=%+v usage=%+v error=%v", result, debug.Usage, err)
	}
	events := compressionLogEvents(t, content, "compression_validation_rejected")
	if len(events) != 1 {
		t.Fatalf("missing persisted malformed-summary rejection, got %d", len(events))
	}
	assertCompressionLogSource(t, events[0], server.URL+"/v1", client.RequestID, "local", 1)
	if !strings.Contains(events[0]["error"].(string), "格式错误") || events[0]["responseStatus"] != "completed" || events[0]["validCompactionCount"] != float64(0) || len(compressionLogEvents(t, content, "compression_failed")) != 1 {
		t.Errorf("local failure lost its actual validation cause: %+v", events[0])
	}
	assertCompressionLogSource(t, compressionLogEvents(t, content, "compression_failed")[0], server.URL+"/v1", client.RequestID, "local", 1)
}

func TestCompressionThinkingLogRecordsServerSuccessOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "response", "status": "completed",
			"output": []any{map[string]any{"type": "compaction", "encrypted_content": "private-success-compaction-state"}},
			"usage":  map[string]any{"input_tokens": 4000, "output_tokens": 50, "total_tokens": 4050}})
	}))
	defer server.Close()
	client := compressionDiagnosticClient(server.URL + "/v1")
	client.RequestID = "compression-log-server-success"
	client.ContextCompression.MaxAttempts = 1
	content, result, debug, err := captureCompressionThinkingLog(t, func() (AIContextCompressionTestResult, AISearchDebugResult, error) {
		return client.TestContextCompression(t.Context(), strings.Repeat("Historical evidence. ", 1500), "server")
	})
	if err != nil || !result.Success || debug.Usage.TotalTokens != 4050 || len(debug.Usage.ModelCalls) != 1 || len(debug.Usage.Trace) != 1 {
		t.Fatalf("server success changed actual usage: result=%+v usage=%+v error=%v", result, debug.Usage, err)
	}
	finished := compressionLogEvents(t, content, "compression_finished")
	if len(finished) != 1 {
		t.Fatalf("one real server compression must have one completion event, got %d", len(finished))
	}
	assertCompressionLogSource(t, finished[0], server.URL+"/v1", client.RequestID, "server", 1)
	if strings.Contains(content, "private-success-compaction-state") {
		t.Error("success event exposes opaque compaction state")
	}
}
