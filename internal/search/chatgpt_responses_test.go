package search

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func writeResponsesEvent(w io.Writer, eventType string, payload map[string]any) {
	encoded, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, encoded)
}

func writeAIResponsesCompleted(w io.Writer, content string, usage map[string]any) {
	output := []any{}
	if content != "" {
		output = append(output, map[string]any{
			"type": "message", "id": "msg_test", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": content, "annotations": []any{}}},
		})
	}
	writeResponsesEvent(w, "response.completed", map[string]any{
		"response": map[string]any{"id": "resp_test", "status": "completed", "output": output, "usage": usage},
	})
}

func writeAIResponsesTextStream(w io.Writer, chunks []string, usage map[string]any) {
	var content strings.Builder
	for index, chunk := range chunks {
		content.WriteString(chunk)
		writeResponsesEvent(w, "response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "sequence_number": index, "delta": chunk})
	}
	writeAIResponsesCompleted(w, content.String(), usage)
}

func TestChatGptClientUsesResponsesEndpointAndParsesOutputText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("expected Responses endpoint, got %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := payload["input"]; !ok {
			t.Fatalf("expected Responses input, got %v", payload)
		}
		if payload["max_output_tokens"] != float64(aiToolMaxOutputTokens) {
			t.Fatalf("expected max_output_tokens, got %v", payload["max_output_tokens"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponsesEvent(w, "response.output_text.delta", map[string]any{"delta": `{"answers":["4"]}`})
		writeResponsesEvent(w, "response.completed", map[string]any{
			"response": map[string]any{
				"status": "completed",
				"usage":  map[string]any{"input_tokens": 11, "output_tokens": 7, "total_tokens": 18},
			},
		})
	}))
	defer server.Close()

	client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "gpt-6.1-sol"}
	answer, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "2+2=?", Type: 2})
	if err != nil {
		t.Fatalf("search answer: %v", err)
	}
	if !reflect.DeepEqual(answer, [][]string{{"4"}}) {
		t.Fatalf("unexpected answer: %v", answer)
	}
	if usage.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}
