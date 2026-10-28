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
)

func TestBuildAICompressionToolTestPromptRequiresHistoryVerification(t *testing.T) {
	prompt, err := BuildAICompressionToolTestPrompt("both", "OpenAI Responses API context compaction")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"调用一次 web_search", "调用一次 web_read", "【历史校验】", "【关键事实校验】", "【待办校验】", "历史字段缺失"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("compression prompt does not contain %q: %s", want, prompt)
		}
	}
	for _, leaked := range []string{"HISTORY-KEEP-7F3A", "城市=宁波", "数量=17"} {
		if strings.Contains(prompt, leaked) {
			t.Errorf("compression prompt leaks expected history value %q: %s", leaked, prompt)
		}
	}
}

func compressionDiagnosticClient(endpoint string) *ChatGptClient {
	return &ChatGptClient{Enable: true, BaseURL: endpoint, APIKey: "test-key", Model: "test-model", Prompt: "Return answers JSON; preserve every original task.",
		ContextCompression: AIContextCompressionConfig{Enabled: false, RemoteEnabled: true, MaxAttempts: 2}}
}

func TestContextCompressionDiagnosticForcesLocalAndDoesNotMutateConfig(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/compact") {
			t.Error("explicit local test attempted remote compression")
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		encoded, _ := json.Marshal(payload)
		if strings.Contains(string(encoded), "压缩测试保留锚点") || payload["tools"] != nil {
			t.Errorf("summary received protected task or tools: %s", encoded)
		}
		writeCompactionTestSummary(w, "Test evidence retained; verify pending task.", "completed")
	}))
	defer server.Close()
	client := compressionDiagnosticClient(server.URL + "/v1")
	original := client.ContextCompression
	result, debug, err := client.TestContextCompression(context.Background(), strings.Repeat("Synthetic historical evidence for compression. ", 500), "local")
	if err != nil || !result.Success || result.Strategy != "local" || result.RemoteFallback || result.Summary == "" || calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	if result.BeforeTokens <= result.AfterTokens || result.AfterTokens <= 0 || debug.Usage.TotalTokens != 120 || len(debug.Usage.ModelCalls) != 1 || len(debug.Trace) != 1 {
		t.Fatalf("missing reduction, usage or trace: result=%+v debug=%+v", result, debug)
	}
	if !reflect.DeepEqual(client.ContextCompression, original) {
		t.Fatal("one-off diagnostic changed saved compression config")
	}
}

func TestContextCompressionDiagnosticRemoteSuccessAndFallback(t *testing.T) {
	for _, remoteSuccess := range []bool{true, false} {
		t.Run(fmt.Sprint(remoteSuccess), func(t *testing.T) {
			remote, local := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/compact") {
					remote++
					if !remoteSuccess {
						w.WriteHeader(http.StatusNotFound)
						_, _ = fmt.Fprint(w, `{"error":"unsupported endpoint"}`)
						return
					}
					_, _ = fmt.Fprint(w, `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque-sensitive-state"}],"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}`)
					return
				}
				local++
				writeCompactionTestSummary(w, "Local fallback retained test evidence.", "completed")
			}))
			defer server.Close()
			client := compressionDiagnosticClient(server.URL + "/v1")
			result, debug, err := client.TestContextCompression(context.Background(), strings.Repeat("Synthetic remote history. ", 1000), "remote")
			strategy, wantLocal, tokens := "remote", 0, 110
			if !remoteSuccess {
				strategy, wantLocal, tokens = "local", 1, 120
			}
			if err != nil || !result.Success || result.Strategy != strategy || result.RemoteFallback != !remoteSuccess || remote != 1 || local != wantLocal || debug.Usage.TotalTokens != int64(tokens) {
				t.Fatalf("result=%+v remote=%d local=%d usage=%+v err=%v", result, remote, local, debug.Usage, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "opaque-sensitive-state") {
				t.Fatal("diagnostic exposes encrypted remote context")
			}
		})
	}
}

func TestContextCompressionDiagnosticRetriesLocalAndRetainsFailedUsage(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				summary := ""
				if recover && calls == 2 {
					summary = "Recovered test summary."
				}
				writeCompactionTestSummary(w, summary, "completed")
			}))
			defer server.Close()
			result, debug, err := compressionDiagnosticClient(server.URL).TestContextCompression(context.Background(), strings.Repeat("Synthetic repeated history. ", 1000), "local")
			if (err == nil) != recover || result.Success != recover || calls != 2 || len(debug.Usage.ModelCalls) != 2 || debug.Usage.TotalTokens != 240 || len(debug.Trace) != 2 {
				t.Fatalf("result=%+v calls=%d usage=%+v err=%v", result, calls, debug.Usage, err)
			}
			if !recover && result.Error == "" {
				t.Fatal("failed diagnostic omitted final error")
			}
			if debug.Trace[0].Status != "200" || debug.Trace[0].Error == "" {
				t.Fatalf("HTTP 200 with an invalid empty summary was falsely logged as success: %+v", debug.Trace[0])
			}
			if (debug.Trace[1].Error == "") != recover {
				t.Fatalf("final summary validation status disagrees with final outcome: %+v", debug.Trace[1])
			}
		})
	}
}

func TestToolCallWithCompressionRestoresTaskAndContinuesRealToolLoop(t *testing.T) {
	tool := &testAISearchTool{}
	compressions, rounds := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["stream"] != true {
			compressions++
			writeCompactionTestSummary(w, "History establishes test evidence; complete original search task.", "completed")
			return
		}
		rounds++
		encoded, _ := json.Marshal(payload)
		for _, preserved := range []string{"Return answers JSON", "test keyword", "上下文压缩后的执行规则", "History establishes test evidence", "【历史校验】", "【关键事实校验】", "【待办校验】", "历史字段缺失"} {
			if !strings.Contains(string(encoded), preserved) {
				t.Errorf("compressed AI request lost %q: %s", preserved, encoded)
			}
		}
		if strings.Contains(string(encoded), "UNCOMPRESSED-HISTORY-LINE") {
			t.Error("tool loop reused raw history instead of compressed context")
		}
		if rounds == 1 {
			writeAIToolStream(w, "diagnostic-search", "test keyword")
		} else {
			writeAIFinalStream(w)
		}
	}))
	defer server.Close()
	client := compressionDiagnosticClient(server.URL)
	client.SearchTool = tool
	answers, debug, compression, err := client.TestToolCallWithCompression(context.Background(), "web_search", "test keyword", strings.Repeat("UNCOMPRESSED-HISTORY-LINE ", 1000), "local")
	if err != nil || !compression.Success || compression.Strategy != "local" || compressions != 1 || rounds != 2 || !reflect.DeepEqual(answers, [][]string{{"4"}}) || !reflect.DeepEqual(tool.queries, []string{"test keyword"}) {
		t.Fatalf("answers=%v compression=%+v calls=%d/%d queries=%v err=%v", answers, compression, compressions, rounds, tool.queries, err)
	}
	if debug.Usage.TotalTokens != 157 || len(debug.Usage.ModelCalls) != 3 || len(debug.Usage.SearchCalls) != 1 {
		t.Fatalf("summary usage omitted from full tool-test billing: %+v", debug.Usage)
	}
}

func TestContextCompressionDiagnosticConfiguredStrategyAndValidation(t *testing.T) {
	for _, remoteEnabled := range []bool{true, false} {
		t.Run(fmt.Sprint(remoteEnabled), func(t *testing.T) {
			remote, local := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/compact") {
					remote++
					_, _ = fmt.Fprint(w, `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}]}`)
					return
				}
				local++
				writeCompactionTestSummary(w, "Configured local summary.", "completed")
			}))
			defer server.Close()
			client := compressionDiagnosticClient(server.URL)
			client.ContextCompression.RemoteEnabled = remoteEnabled
			result, _, err := client.TestContextCompression(context.Background(), strings.Repeat("Synthetic configured history. ", 1000), "configured")
			if err != nil || !result.Success || (remote == 1) != remoteEnabled || (local == 1) == remoteEnabled {
				t.Fatalf("result=%+v remote=%d local=%d err=%v", result, remote, local, err)
			}
			before := remote + local
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, _, err := client.TestContextCompression(ctx, "history", "local"); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled diagnostic did not stop: %v", err)
			}
			for _, invalid := range []struct{ history, mode string }{{" ", "local"}, {"history", "invalid"}} {
				if _, _, err := client.TestContextCompression(context.Background(), invalid.history, invalid.mode); err == nil {
					t.Fatalf("accepted invalid arguments: %+v", invalid)
				}
			}
			if before != remote+local {
				t.Fatal("cancelled/invalid diagnostic reached external model")
			}
		})
	}
}

func TestToolCallWithCompressionFailureStopsBeforeToolsAndRetainsUsage(t *testing.T) {
	tool := &testAISearchTool{}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeCompactionTestSummary(w, "", "completed")
	}))
	defer server.Close()
	client := compressionDiagnosticClient(server.URL)
	client.SearchTool = tool
	answers, debug, result, err := client.TestToolCallWithCompression(context.Background(), "web_search", "test keyword", strings.Repeat("Synthetic historical details. ", 1000), "local")
	if err == nil || result.Success || result.Error == "" || len(answers) != 0 || len(tool.queries) != 0 || requests != 2 || debug.Usage.TotalTokens != 240 || len(debug.Usage.ModelCalls) != 2 {
		t.Fatalf("answers=%v result=%+v requests=%d queries=%v usage=%+v err=%v", answers, result, requests, tool.queries, debug.Usage, err)
	}
}

func TestCompressionDiagnosticsResolveV1EndpointWithoutSpendingRetryBudget(t *testing.T) {
	for _, withTools := range []bool{false, true} {
		for _, strategy := range []string{"local", "remote"} {
			t.Run(fmt.Sprintf("tools=%t/%s", withTools, strategy), func(t *testing.T) {
				probes, summaries, remotes, rounds := 0, 0, 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.HasPrefix(r.URL.Path, "/v1/") {
						probes++
						w.WriteHeader(http.StatusNotFound)
						_, _ = fmt.Fprint(w, `{"error":"wrong API path"}`)
						return
					}
					if r.URL.Path == "/v1/responses/compact" {
						remotes++
						_, _ = fmt.Fprint(w, `{"object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}`)
						return
					}
					if r.URL.Path != "/v1/responses" {
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					var payload map[string]any
					_ = json.NewDecoder(r.Body).Decode(&payload)
					if payload["stream"] != true {
						summaries++
						writeCompactionTestSummary(w, "Resolved v1 endpoint and retained historical evidence.", "completed")
						return
					}
					rounds++
					if rounds == 1 {
						writeAIToolStream(w, "resolved-search", "test keyword")
					} else {
						writeAIFinalStream(w)
					}
				}))
				defer server.Close()
				client := compressionDiagnosticClient(server.URL)
				client.ContextCompression.MaxAttempts = 1
				client.MaxAISearchRetries = 1
				history := strings.Repeat("Synthetic history for API endpoint fallback. ", 1000)
				var result AIContextCompressionTestResult
				var debug AISearchDebugResult
				var err error
				if withTools {
					tool := &testAISearchTool{}
					client.SearchTool = tool
					var answers [][]string
					answers, debug, result, err = client.TestToolCallWithCompression(context.Background(), "web_search", "test keyword", history, strategy)
					if err == nil && (!reflect.DeepEqual(answers, [][]string{{"4"}}) || len(tool.queries) != 1 || rounds != 2) {
						t.Fatalf("resolved endpoint did not complete tool loop: answers=%v queries=%v rounds=%d", answers, tool.queries, rounds)
					}
				} else {
					result, debug, err = client.TestContextCompression(context.Background(), history, strategy)
				}
				wantSummaries, wantRemotes, wantCalls, wantTokens := 1, 0, 1, int64(120)
				if strategy == "remote" {
					wantSummaries, wantRemotes, wantTokens = 0, 1, 110
				}
				if withTools {
					wantCalls += 2
					wantTokens += 37
				}
				if err != nil || !result.Success || result.Strategy != strategy || result.RemoteFallback || probes == 0 || summaries != wantSummaries || remotes != wantRemotes || len(debug.Usage.ModelCalls) != wantCalls || debug.Usage.TotalTokens != wantTokens {
					t.Fatalf("result=%+v probes=%d summaries=%d remote=%d usage=%+v err=%v", result, probes, summaries, remotes, debug.Usage, err)
				}
			})
		}
	}
}
