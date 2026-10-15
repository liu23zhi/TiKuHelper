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

type webReadTestTool struct {
	urls []string
}

func (tool *webReadTestTool) SearchAggregate(context.Context, uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	return uapi.SearchAggregateResponse{}, fmt.Errorf("unexpected web_search call")
}

func (tool *webReadTestTool) ReadWebPage(_ context.Context, rawURL string) (uapi.WebToMarkdownTask, error) {
	tool.urls = append(tool.urls, rawURL)
	return uapi.WebToMarkdownTask{TaskID: "web-task-1", Status: "completed", Result: &uapi.WebToMarkdownResult{Markdown: "网页正文：答案是4。"}, RawJSON: `{"status":"completed"}`}, nil
}

func writeAIWebReadToolStream(w http.ResponseWriter, callID, rawURL string) {
	w.Header().Set("Content-Type", "text/event-stream")
	arguments, _ := json.Marshal(map[string]string{"url": rawURL})
	event, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": callID, "type": "function", "function": map[string]any{"name": "web_read", "arguments": string(arguments)}}}}, "finish_reason": "tool_calls"}}})
	fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\ndata: [DONE]\n\n", event)
}

func TestAISearchWebReadToolUsesBudgetAndRecordsUsage(t *testing.T) {
	tool := &webReadTestTool{}
	modelCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if modelCalls == 1 {
			tools, ok := payload["tools"].([]any)
			if !ok || len(tools) != 2 {
				t.Fatalf("expected web_search and web_read tools, got %#v", payload["tools"])
			}
			budget := payload["messages"].([]any)
			last := budget[len(budget)-1].(map[string]any)["content"].(string)
			if !strings.Contains(last, "web_read 网页读取还剩 1 次") {
				t.Fatalf("missing web-read budget: %s", last)
			}
			writeAIWebReadToolStream(w, "read-1", "https://example.com/article")
			return
		}
		for _, raw := range payload["messages"].([]any) {
			message := raw.(map[string]any)
			if message["role"] == "tool" && strings.Contains(message["content"].(string), "网页正文") {
				writeAIFinalStream(w)
				return
			}
		}
		t.Fatal("model did not receive web-read evidence")
	}))
	defer server.Close()

	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "test", SearchTool: tool, MaxAISearchModelCalls: 3, MaxAISearches: 3, MaxAIWebReads: 1}
	answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", DeepThinking: true})
	if err != nil || !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("answer=%v err=%v", answer, err)
	}
	if modelCalls != 2 || len(tool.urls) != 1 || len(usage.WebReadCalls) != 1 || usage.WebReadCalls[0].Status != "success" || usage.WebReadCalls[0].TaskID != "web-task-1" {
		t.Fatalf("calls=%d urls=%v webReads=%+v", modelCalls, tool.urls, usage.WebReadCalls)
	}
}
