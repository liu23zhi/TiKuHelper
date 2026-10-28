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
	"time"
)

func compactionTestInput() ([]any, []any) {
	anchors := []any{
		buildAIResponsesMessage("system", "Original rules: return answers JSON."),
		buildAIResponsesMessage("user", []any{map[string]any{"type": "input_text", "text": "Complete all tasks; options A and B."}, map[string]any{"type": "input_image", "image_url": "https://images.example/question.png"}}),
	}
	input := append([]any(nil), anchors...)
	input = append(input,
		map[string]any{"type": "function_call", "call_id": "call-search", "name": "web_search", "arguments": `{"query":"OpenAI context limits"}`, "status": "completed"},
		map[string]any{"type": "function_call_output", "call_id": "call-search", "output": `{"status":"success","query":"OpenAI context limits","results":[{"url":"https://example.com/reference","snippet":"` + strings.Repeat("Historical evidence. ", 2500) + `"}]}`},
	)
	return anchors, input
}

func writeCompactionTestSummary(w http.ResponseWriter, summary string, status string) {
	text, _ := json.Marshal(map[string]string{"summary": summary})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "response", "status": status,
		"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(text)}}}},
		"usage":  map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "input_tokens_details": map[string]any{"cached_tokens": 10}},
	})
}

func TestAIContextCompactionDisabledAndUnknownWindowDoNotCall(t *testing.T) {
	anchors, input := compactionTestInput()
	for _, config := range []AIContextCompressionConfig{{}, {Enabled: true}} {
		client := &ChatGptClient{ContextCompression: config}
		result, err := client.newAIContextCompactor(anchors, &AISearchUsage{}).prepare(context.Background(), "invalid", input, false, 2)
		if err != nil || !reflect.DeepEqual(result, input) {
			t.Fatalf("disabled/unknown-window changed input: err=%v", err)
		}
	}
}

func TestAIContextCompactionRemotePreservesCanonicalOutputAndAnchors(t *testing.T) {
	anchors, input := compactionTestInput()
	canonical := []any{
		map[string]any{"type": "message", "role": "user", "content": "retained user item", "future_field": map[string]any{"keep": true}},
		map[string]any{"type": "compaction", "id": "cmp_1", "encrypted_content": "opaque-state", "future_field": []any{1.0, "keep"}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/compact" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected compact request %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["model"] != "test-model" || payload["instructions"] == nil || payload["max_output_tokens"] != nil {
			t.Errorf("invalid compact request payload: %#v", payload)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "response.compaction", "output": canonical, "usage": map[string]any{"input_tokens": 300, "output_tokens": 30, "total_tokens": 330}})
	}))
	defer server.Close()
	usage := AISearchUsage{}
	client := &ChatGptClient{APIKey: "test-key", Model: "test-model", ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: true, ContextWindowTokens: 20000}}
	result, err := client.newAIContextCompactor(anchors, &usage).prepare(context.Background(), server.URL+"/v1/responses", input, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := range canonical {
		got, _ := json.Marshal(result[i])
		want, _ := json.Marshal(canonical[i])
		if string(got) != string(want) {
			t.Fatalf("canonical item %d was changed: %s != %s", i, got, want)
		}
	}
	if !reflect.DeepEqual(result[len(result)-len(anchors):], anchors) {
		t.Fatal("original system/user/image anchors were not restored exactly")
	}
	if usage.TotalTokens != 330 || len(usage.ModelCalls) != 1 || usage.ModelCalls[0].Round != 2 || len(usage.Trace) != 1 {
		t.Fatalf("missing compaction billing/trace: %+v", usage)
	}
}

func TestAIContextCompactionUnsupportedFallsBackToModelSummary(t *testing.T) {
	anchors, input := compactionTestInput()
	remote, local := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/compact") {
			remote++
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"code":"unsupported_endpoint"}}`)
			return
		}
		local++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		encoded, _ := json.Marshal(payload)
		if strings.Contains(string(encoded), "Complete all tasks") || strings.Contains(string(encoded), "question.png") {
			t.Error("protected question/image anchors leaked into summary request")
		}
		if payload["tools"] != nil || payload["tool_choice"] != nil || payload["max_output_tokens"].(float64) <= 0 {
			t.Errorf("summary must disable tools and bound only summary output: %s", encoded)
		}
		writeCompactionTestSummary(w, "Sources establish context behavior; next verify implementation.", "completed")
	}))
	defer server.Close()
	usage := AISearchUsage{}
	client := &ChatGptClient{APIKey: "test-key", Model: "test-model", ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: true, MaxAttempts: 1, ContextWindowTokens: 20000}}
	manager := client.newAIContextCompactor(anchors, &usage)
	result, err := manager.prepare(context.Background(), server.URL+"/v1/responses", input, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	if remote != 1 || local != 1 {
		t.Fatalf("unexpected remote/local calls %d/%d", remote, local)
	}
	if !reflect.DeepEqual(result[len(result)-len(anchors):], anchors) {
		t.Fatal("summary altered original anchors")
	}
	encoded, _ := json.Marshal(result)
	for _, keep := range []string{"OpenAI context limits", "https://example.com/reference", "call-search", "success", "Sources establish"} {
		if !strings.Contains(string(encoded), keep) {
			t.Fatalf("missing provenance %q in %s", keep, encoded)
		}
	}
	if strings.Contains(string(encoded), `"type":"function_call_output"`) || usage.TotalTokens != 120 || usage.CachedInputTokens != 10 || len(usage.ModelCalls) != 1 {
		t.Fatalf("dangling call output or missing summary billing: %s %+v", encoded, usage)
	}
}

func TestAIContextCompactionRejectsFakeRemoteAndRetriesLocalWithUsage(t *testing.T) {
	anchors, input := compactionTestInput()
	remote, local := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/compact") {
			remote++
			_, _ = fmt.Fprint(w, `{"object":"response.compaction","output":[{"type":"message","role":"assistant","content":[]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}`)
			return
		}
		local++
		if local == 1 {
			writeCompactionTestSummary(w, "", "completed")
			return
		}
		writeCompactionTestSummary(w, "Evidence retained; continue unfinished work.", "completed")
	}))
	defer server.Close()
	usage := AISearchUsage{}
	client := &ChatGptClient{Model: "test", ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: true, MaxAttempts: 2, ContextWindowTokens: 20000}}
	_, err := client.newAIContextCompactor(anchors, &usage).prepare(context.Background(), server.URL+"/v1/responses", input, true, 3)
	if err != nil || remote != 2 || local != 2 || len(usage.ModelCalls) != 4 || usage.TotalTokens != 252 {
		t.Fatalf("fake success/retry usage: err=%v remote=%d local=%d usage=%+v", err, remote, local, usage)
	}
}

func TestAIContextCompactionCancellationNeverStartsRequest(t *testing.T) {
	anchors, input := compactionTestInput()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: true}}
	_, err := client.newAIContextCompactor(anchors, nil).prepare(ctx, "invalid", input, true, 2)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestAIContextCompactionRefusesDanglingCalls(t *testing.T) {
	anchors, input := compactionTestInput()
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, MaxAttempts: 1}}
	for _, history := range [][]any{{input[len(input)-1]}} {
		_, err := client.newAIContextCompactor(anchors, nil).prepare(context.Background(), "invalid", append(append([]any(nil), anchors...), history...), true, 2)
		if err == nil {
			t.Fatal("unreadable/invalid history silently accepted")
		}
	}
}

func TestAIContextCompactionLocalPreservesOpaqueWithoutSummarizingIt(t *testing.T) {
	anchors, input := compactionTestInput()
	opaque := map[string]any{"type": "reasoning", "encrypted_content": "encrypted-secret", "future_field": map[string]any{"keep": true}}
	input = append(input, opaque)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		encoded, _ := json.Marshal(payload)
		if strings.Contains(string(encoded), "encrypted-secret") {
			t.Error("opaque state must not be sent to the summary model as text")
		}
		writeCompactionTestSummary(w, "Visible tool results summarized; verify pending work.", "completed")
	}))
	defer server.Close()
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, ContextWindowTokens: 20000}}
	result, err := client.newAIContextCompactor(anchors, nil).prepare(context.Background(), server.URL+"/responses", input, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(opaque)
	found := false
	for _, item := range result {
		actual, _ := json.Marshal(item)
		found = found || string(actual) == string(encoded)
	}
	if !found {
		t.Fatal("opaque item was not retained exactly")
	}
}

func TestAIContextCompactionLocalKeepsEntirePriorRemoteCanonicalGroup(t *testing.T) {
	anchors, input := compactionTestInput()
	canonical := []any{
		anchors[0],
		anchors[1],
		map[string]any{"type": "message", "role": "user", "content": "retained canonical input", "unknown": "keep"},
		map[string]any{"type": "compaction", "encrypted_content": "encrypted-state"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/compact") {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "response.compaction", "output": canonical})
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		encoded, _ := json.Marshal(payload)
		if strings.Contains(string(encoded), "retained canonical input") || strings.Contains(string(encoded), "encrypted-state") {
			t.Error("prior canonical group was converted into model summary material")
		}
		writeCompactionTestSummary(w, "New visible results summarized.", "completed")
	}))
	defer server.Close()
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: true, ContextWindowTokens: 20000}}
	manager := client.newAIContextCompactor(anchors, nil)
	compacted, err := manager.prepare(context.Background(), server.URL+"/responses", input, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	manager.remoteUnavailable = true
	compacted = append(compacted, buildAIResponsesMessage("assistant", strings.Repeat("New tool-independent evidence. ", 1200)))
	result, err := manager.prepare(context.Background(), server.URL+"/responses", compacted, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := range canonical {
		if aiContextCanonicalKey(result[i]) != aiContextCanonicalKey(canonical[i]) {
			t.Fatal("entire canonical group must remain in original order before the new summary")
		}
	}
}

func TestAIContextCompactionRequestHonorsGlobalConcurrencySlot(t *testing.T) {
	anchors, input := compactionTestInput()
	for i := 0; i < cap(aiSearchRequestSemaphore); i++ {
		aiSearchRequestSemaphore <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(aiSearchRequestSemaphore); i++ {
			<-aiSearchRequestSemaphore
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true}}
	_, err := client.newAIContextCompactor(anchors, nil).prepare(ctx, "http://invalid.example/responses", input, true, 2)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("compression bypassed global concurrency protection: %v", err)
	}
}

func TestAIContextCompactionAnchorAboveTargetCanStillShrink(t *testing.T) {
	anchors := []any{buildAIResponsesMessage("system", "Keep rules."), buildAIResponsesMessage("user", strings.Repeat("Question text. ", 600))}
	input := append(append([]any(nil), anchors...), buildAIResponsesMessage("assistant", strings.Repeat("Historical answer detail. ", 200)))
	window := estimateAIContextTokens(input) * 100 / 85
	if estimateAIContextTokens(anchors) <= window/2 {
		t.Fatal("fixture must place protected question above the nominal 50% target")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCompactionTestSummary(w, "Continue remaining task.", "completed")
	}))
	defer server.Close()
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, ContextWindowTokens: window}}
	result, err := client.newAIContextCompactor(anchors, nil).prepare(context.Background(), server.URL+"/responses", input, false, 2)
	if err != nil || estimateAIContextTokens(result) >= estimateAIContextTokens(input) {
		t.Fatalf("protected question above target must still allow useful compaction: %v", err)
	}
}

func TestAIContextCompactionLocalChunksCompleteToolGroups(t *testing.T) {
	anchors, _ := compactionTestInput()
	input := append([]any(nil), anchors...)
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("call-%d", i)
		input = append(input,
			map[string]any{"type": "function_call", "call_id": id, "name": "web_search", "arguments": fmt.Sprintf(`{"query":"query-%d"}`, i)},
			map[string]any{"type": "function_call_output", "call_id": id, "output": fmt.Sprintf(`{"status":"success","url":"https://example.com/%d","text":"%s"}`, i, strings.Repeat("Evidence data. ", 180))},
		)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		parts := payload["input"].([]any)
		if estimateAIContextTokens(parts)+int64(payload["max_output_tokens"].(float64)) >= 3000 {
			t.Error("summary chunk plus output reserve exceeds context window")
		}
		var material struct {
			History []any `json:"untrusted_history"`
		}
		_ = json.Unmarshal([]byte(parts[1].(map[string]any)["content"].(string)), &material)
		if _, err := aiContextHistoryProvenance(material.History); err != nil {
			t.Errorf("chunk split a function-call/output pair: %v", err)
		}
		writeCompactionTestSummary(w, "Search evidence supports pending work.", "completed")
	}))
	defer server.Close()
	usage := AISearchUsage{}
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, ContextWindowTokens: 3000}}
	result, err := client.newAIContextCompactor(anchors, &usage).prepare(context.Background(), server.URL+"/responses", input, true, 2)
	if err != nil || calls < 3 || len(usage.ModelCalls) != calls {
		t.Fatalf("history must be summarized in bounded chunks, then merged: calls=%d err=%v", calls, err)
	}
	encoded, _ := json.Marshal(result)
	for i := 0; i < 9; i++ {
		if !strings.Contains(string(encoded), fmt.Sprintf("call-%d", i)) || !strings.Contains(string(encoded), fmt.Sprintf("query-%d", i)) {
			t.Fatal("chunk merge lost call provenance")
		}
	}
}

func TestAIContextCompactionAutomaticNoHistoryKeepsCriticalInput(t *testing.T) {
	anchors, _ := compactionTestInput()
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, ContextWindowTokens: 10}}
	manager := client.newAIContextCompactor(anchors, nil)
	// A retained canonical user message can have a different JSON field order.
	encoded, _ := json.Marshal(anchors[0])
	var canonical map[string]any
	_ = json.Unmarshal(encoded, &canonical)
	input := []any{canonical, anchors[1]}
	result, err := manager.prepare(context.Background(), "invalid", input, false, 1)
	if err != nil || !reflect.DeepEqual(input, result) {
		t.Fatalf("automatic compaction must skip protected-only input: %v", err)
	}
	if _, err = manager.prepare(context.Background(), "invalid", input, true, 1); err == nil {
		t.Fatal("forced compaction must explain protected-only overflow")
	}
}

func TestAIContextCompactionPermanentRemoteStatusesSkipRetries(t *testing.T) {
	for _, status := range []int{401, 403, 404, 405, 501} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			anchors, input := compactionTestInput()
			remote, local := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/compact") {
					remote++
					w.WriteHeader(status)
					_, _ = fmt.Fprint(w, `{"error":"compact endpoint unavailable"}`)
					return
				}
				local++
				writeCompactionTestSummary(w, "Useful history and remaining task.", "completed")
			}))
			defer server.Close()
			client := &ChatGptClient{Model: "test", ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: true, ContextWindowTokens: 20000}}
			manager := client.newAIContextCompactor(anchors, &AISearchUsage{})
			for i := 0; i < 2; i++ {
				if _, err := manager.prepare(context.Background(), server.URL+"/responses", input, true, i+1); err != nil {
					t.Fatal(err)
				}
			}
			if remote != 1 || local != 2 {
				t.Fatalf("permanent status repeated or skipped local: remote=%d local=%d", remote, local)
			}
		})
	}
}

func TestAIContextCompactionTransientRemoteRetriesKeepAllUsage(t *testing.T) {
	anchors, input := compactionTestInput()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":"temporary","usage":{"input_tokens":11,"output_tokens":2,"total_tokens":13}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}`)
	}))
	defer server.Close()
	usage := AISearchUsage{}
	client := &ChatGptClient{Model: "test", ContextCompression: AIContextCompressionConfig{Enabled: true, RemoteEnabled: true, MaxAttempts: 2, ContextWindowTokens: 20000}}
	if _, err := client.newAIContextCompactor(anchors, &usage).prepare(context.Background(), server.URL+"/responses", input, true, 2); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || usage.TotalTokens != 123 || len(usage.ModelCalls) != 2 || usage.ModelCalls[1].Index != 2 {
		t.Fatalf("transient compaction usage mismatch: calls=%d usage=%+v", calls, usage)
	}
}

func TestAIContextCompactionLocalInvalidSummaryStopsAtLimit(t *testing.T) {
	for _, invalid := range []string{"empty", "incomplete", "malformed", "too-large"} {
		t.Run(invalid, func(t *testing.T) {
			anchors, input := compactionTestInput()
			original, _ := json.Marshal(input)
			calls := 0
			caps := []float64{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				caps = append(caps, payload["max_output_tokens"].(float64))
				switch invalid {
				case "empty":
					writeCompactionTestSummary(w, "", "completed")
				case "incomplete":
					writeCompactionTestSummary(w, "unfinished", "incomplete")
				case "malformed":
					_, _ = fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Not a structured summary"}]}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}`)
				case "too-large":
					writeCompactionTestSummary(w, strings.Repeat("Verbose summary.", 4000), "completed")
				}
			}))
			defer server.Close()
			usage := AISearchUsage{}
			client := &ChatGptClient{Model: "test", ContextCompression: AIContextCompressionConfig{Enabled: true, MaxAttempts: 2, ContextWindowTokens: 20000}}
			_, err := client.newAIContextCompactor(anchors, &usage).prepare(context.Background(), server.URL+"/responses", input, true, 2)
			if err == nil || calls != 2 || len(usage.ModelCalls) != 2 || usage.TotalTokens != 240 {
				t.Fatalf("invalid summary escaped finite retry: %v calls=%d usage=%+v", err, calls, usage)
			}
			if caps[1] >= caps[0] {
				t.Fatalf("summary retries did not tighten output budget: %v", caps)
			}
			after, _ := json.Marshal(input)
			if string(after) != string(original) {
				t.Fatal("failed compression mutated original history")
			}
		})
	}
}

func TestAIContextCompactionLocalOversizedHistoryFailsBeforeRequest(t *testing.T) {
	anchors, input := compactionTestInput()
	client := &ChatGptClient{ContextCompression: AIContextCompressionConfig{Enabled: true, ContextWindowTokens: 1000}}
	_, err := client.newAIContextCompactor(anchors, nil).prepare(context.Background(), "invalid", input, true, 1)
	if err == nil || !strings.Contains(err.Error(), "历史本身超过") {
		t.Fatalf("must report history limit without an impossible request: %v", err)
	}
}
