package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/itihey/tikuAdapter/internal/uapi"
)

type diagnosticTestTool struct {
	searches int
	reads    int
	err      error
	empty    bool
}

func (tool *diagnosticTestTool) SearchAggregate(_ context.Context, request uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	tool.searches++
	if tool.empty || tool.err != nil {
		return uapi.SearchAggregateResponse{}, tool.err
	}
	return uapi.SearchAggregateResponse{Query: request.Query, Results: []uapi.SearchResult{{Title: "资料", URL: "https://example.com/source", Snippet: "答案是4"}}}, nil
}

func (tool *diagnosticTestTool) ReadWebPage(context.Context, string) (uapi.WebToMarkdownTask, error) {
	tool.reads++
	markdown := "答案是4"
	if tool.empty {
		markdown = ""
	}
	return uapi.WebToMarkdownTask{TaskID: "test-read", Status: "completed", Result: &uapi.WebToMarkdownResult{Markdown: markdown}}, tool.err
}

func TestToolCallSelectsOnlyRequestedToolAndRetainsUsage(t *testing.T) {
	for _, toolName := range []string{"web_search", "web_read"} {
		t.Run(toolName, func(t *testing.T) {
			tool := &diagnosticTestTool{}
			modelCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				modelCalls++
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if modelCalls == 1 {
					tools, _ := payload["tools"].([]any)
					choice, _ := payload["tool_choice"].(map[string]any)
					if len(tools) != 1 || tools[0].(map[string]any)["name"] != toolName || choice["name"] != toolName {
						t.Errorf("test must force only %s, got tools=%v choice=%v", toolName, tools, choice)
					}
					if toolName == "web_search" {
						writeAIToolStream(w, "test-tool", "二加二")
					} else {
						writeAIWebReadToolStream(w, "test-tool", "https://example.com/source")
					}
					return
				}
				if payload["tools"] != nil || payload["tool_choice"] != nil {
					t.Errorf("final round must withdraw tools, got %v", payload)
				}
				foundEvidence := false
				for _, raw := range payload["input"].([]any) {
					item := raw.(map[string]any)
					if item["type"] == "function_call_output" && strings.Contains(fmt.Sprint(item["output"]), "答案是4") {
						foundEvidence = true
					}
				}
				if !foundEvidence {
					t.Error("final request did not receive the real tool result")
				}
				writeAIFinalStream(w)
			}))
			defer server.Close()
			client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "test-key", Model: "test-model", SearchTool: tool, MaxAISearchModelCalls: 1}
			answers, debug, err := client.TestToolCall(context.Background(), toolName, "https://example.com/source")
			if err != nil || len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "4" {
				t.Fatalf("answers=%v err=%v", answers, err)
			}
			if modelCalls != 2 || tool.searches+tool.reads != 1 || debug.Usage.InputTokens != 30 || debug.Usage.TotalTokens != 37 || len(debug.Usage.ModelCalls) != 2 {
				t.Fatalf("incomplete usage: rounds=%d tool=%+v usage=%+v", modelCalls, tool, debug.Usage)
			}
			if toolName == "web_search" && (tool.searches != 1 || debug.Usage.CachedInputTokens != 2 || len(debug.Usage.SearchCalls) != 1) {
				t.Fatalf("missing search usage: %+v", debug.Usage)
			}
			if toolName == "web_read" && (tool.reads != 1 || len(debug.Usage.WebReadCalls) != 1 || debug.Usage.WebReadCalls[0].Status != "success") {
				t.Fatalf("missing read usage: %+v", debug.Usage)
			}
		})
	}
}

func TestToolCallReportsFailedOrEmptyToolDespiteFinalAnswer(t *testing.T) {
	for _, toolName := range []string{"web_search", "web_read"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%t", toolName, empty), func(t *testing.T) {
				tool := &diagnosticTestTool{empty: empty}
				if !empty {
					tool.err = errors.New("upstream failure")
				}
				rounds := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					rounds++
					if rounds == 1 {
						if toolName == "web_search" {
							writeAIToolStream(w, "test-failed", "二加二")
						} else {
							writeAIWebReadToolStream(w, "test-failed", "https://example.com/source")
						}
						return
					}
					writeAIFinalStream(w)
				}))
				defer server.Close()
				client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "test-key", Model: "test-model", SearchTool: tool}
				_, debug, err := client.TestToolCall(context.Background(), toolName, "https://example.com/source")
				if err == nil || !strings.Contains(err.Error(), toolName) {
					t.Fatalf("failed or empty tool must not pass: err=%v usage=%+v", err, debug.Usage)
				}
				if rounds != 2 || debug.Usage.TotalTokens != 37 || tool.searches+tool.reads != 1 {
					t.Fatalf("must retain billed usage through final response: rounds=%d tool=%+v usage=%+v", rounds, tool, debug.Usage)
				}
			})
		}
	}
}

func TestToolCallHonorsCancellationAndRejectsUnknownTools(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "test-key", Model: "test-model", SearchTool: &diagnosticTestTool{}}
	if _, _, err := client.TestToolCall(context.Background(), "shell", "test"); err == nil {
		t.Fatal("unknown tools must fail before calling the model")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := client.TestToolCall(ctx, "web_search", "test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must propagate, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("invalid/canceled diagnostics made %d model requests", requests)
	}
}

func TestToolCallDoesNotExecuteOtherOrRepeatedTools(t *testing.T) {
	tool := &diagnosticTestTool{}
	rounds := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		rounds++
		if rounds == 1 {
			items := []any{
				map[string]any{"type": "function_call", "id": "fc_other", "call_id": "other", "name": "web_search", "arguments": `{"query":"二加二"}`, "status": "completed"},
				map[string]any{"type": "function_call", "id": "fc_selected", "call_id": "selected", "name": "web_read", "arguments": `{"url":"https://example.com/source"}`, "status": "completed"},
				map[string]any{"type": "function_call", "id": "fc_extra", "call_id": "extra", "name": "web_read", "arguments": `{"url":"https://example.com/other"}`, "status": "completed"},
			}
			w.Header().Set("Content-Type", "text/event-stream")
			writeResponsesEvent(w, "response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": items, "usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14}}})
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		outputs := 0
		for _, raw := range payload["input"].([]any) {
			if raw.(map[string]any)["type"] == "function_call_output" {
				outputs++
			}
		}
		if outputs != 3 {
			t.Errorf("every requested tool call must receive an output, got %d", outputs)
		}
		writeAIFinalStream(w)
	}))
	defer server.Close()
	client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "test-key", Model: "test-model", SearchTool: tool}
	_, debug, err := client.TestToolCall(context.Background(), "web_read", "https://example.com/source")
	if err != nil || tool.reads != 1 || tool.searches != 0 || len(debug.Usage.WebReadCalls) != 1 || len(debug.Usage.SearchCalls) != 0 {
		t.Fatalf("test invoked unwanted tools: err=%v tool=%+v usage=%+v", err, tool, debug.Usage)
	}
}

func TestToolCallDoesNotDownloadTargetAsAttachment(t *testing.T) {
	for _, toolName := range []string{"web_search", "web_read"} {
		t.Run(toolName, func(t *testing.T) {
			var targetRequests atomic.Int32
			targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				targetRequests.Add(1)
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("tool target"))
			}))
			defer targetServer.Close()
			target := targetServer.URL + "/article"
			modelCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				modelCalls++
				if modelCalls == 1 {
					if toolName == "web_search" {
						writeAIToolStream(w, "test-url", target)
					} else {
						writeAIWebReadToolStream(w, "test-url", target)
					}
					return
				}
				writeAIFinalStream(w)
			}))
			defer server.Close()
			tool := &diagnosticTestTool{}
			client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "test-key", Model: "test-model", SearchTool: tool}
			if _, _, err := client.TestToolCall(context.Background(), toolName, target); err != nil {
				t.Fatal(err)
			}
			if got := targetRequests.Load(); got != 0 {
				t.Fatalf("tool test downloaded target directly %d times", got)
			}
			if modelCalls != 2 || tool.searches+tool.reads != 1 {
				t.Fatalf("expected two model rounds and one tool call: models=%d tool=%+v", modelCalls, tool)
			}
		})
	}
}
