package search

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompressionFinalFailureRedactsAllFallbackReasons(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback-local-%t", fallback), func(t *testing.T) {
			client := &ChatGptClient{RequestID: "final-failure-redaction", ContextCompression: AIContextCompressionConfig{Enabled: true, StrategyOrder: []string{"server"}}}
			if fallback {
				client.ContextCompression.StrategyOrder = append(client.ContextCompression.StrategyOrder, "local")
			}
			usage := AISearchUsage{}
			manager := client.newAIContextCompactor(nil, &usage)
			content, _, _, _ := captureCompressionThinkingLog(t, func() (AIContextCompressionTestResult, AISearchDebugResult, error) {
				manager.handleServerFailure(fmt.Errorf(`invalid compaction state: {"encrypted_content":"private-server-state"}`))
				if fallback {
					manager.advanceStrategy(fmt.Errorf(`summary service unavailable: {"encrypted_content":"private-local-state"}`))
				}
				return AIContextCompressionTestResult{}, AISearchDebugResult{}, nil
			})
			for _, text := range []string{content, strings.Join(manager.fallbackReasons, "; ")} {
				if strings.Contains(text, "private-server-state") || strings.Contains(text, "private-local-state") {
					t.Fatal("opaque state leaked through final failure or stored fallback reasons")
				}
				if !strings.Contains(text, "invalid compaction state") || !strings.Contains(text, "REDACTED") {
					t.Fatal("fallback redaction lost the actionable rejection cause")
				}
			}
			if len(compressionLogEvents(t, content, "compression_failed")) != 1 || len(usage.Trace) != 0 || len(usage.ModelCalls) != 0 {
				t.Fatal("final outcome logging duplicated events or fabricated model usage")
			}
		})
	}
}

func TestCompressionErrorsRedactEchoedOpaqueStateBeforeTruncation(t *testing.T) {
	const opaque = "private-upstream-compaction-ciphertext"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/compact") {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "compression service unavailable", "input": []any{map[string]any{"type": "compaction", "encrypted_content": strings.Repeat(opaque, 4000)}}}})
			return
		}
		writeCompactionTestSummary(w, "Historical facts and unverified tasks preserved.", "completed")
	}))
	defer server.Close()
	client := compressionDiagnosticClient(server.URL + "/v1")
	client.RequestID = "opaque-error-redaction"
	client.ContextCompression.MaxAttempts = 1
	content, result, debug, err := captureCompressionThinkingLog(t, func() (AIContextCompressionTestResult, AISearchDebugResult, error) {
		return client.TestContextCompression(t.Context(), strings.Repeat("Synthetic historical evidence. ", 1000), "remote")
	})
	if err != nil || !result.Success || result.Strategy != "local" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	encoded, _ := json.Marshal(struct {
		Result AIContextCompressionTestResult
		Debug  AISearchDebugResult
	}{result, debug})
	for _, text := range []string{content, string(encoded)} {
		if strings.Contains(text, opaque) {
			t.Fatal("echoed ciphertext leaked in logs or diagnostics")
		}
		if !strings.Contains(text, "compression service unavailable") || !strings.Contains(text, "REDACTED") {
			t.Fatal("redaction lost the upstream cause or did not mark removed state")
		}
	}
}

func TestCompressionOpaqueRedactionHandlesEscapedAndTruncatedErrors(t *testing.T) {
	for _, input := range []string{
		`{"message":"unavailable","encrypted_content":"private-state"}`,
		`{"message":"unavailable","encryptedContent":"private-state"}`,
		`{"message":"unavailable","encrypted_content":"private-state`,
		`{"message":"unavailable: {\"encrypted_content\":\"private-state\"}"}`,
		`{"message":"unavailable: {\"encrypted_content\":\"private-state`,
	} {
		result := redactAIContextOpaqueText(input)
		if strings.Contains(result, "private-state") || !strings.Contains(result, "unavailable") || !strings.Contains(result, "REDACTED") {
			t.Fatalf("unsafe or incomplete error redaction: %s", result)
		}
	}
}
