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

func TestReadAIResponsesStreamUsesCompleteTextSnapshots(t *testing.T) {
	const complete = `{"action":"final","answers":["4"]}`
	for _, snapshot := range []string{"response.output_text.done", "response.output_item.done", "response.completed"} {
		t.Run(snapshot, func(t *testing.T) {
			var stream strings.Builder
			writeResponsesEvent(&stream, "response.output_text.delta", map[string]any{
				"output_index": 0, "content_index": 0, "delta": "action",
			})
			switch snapshot {
			case "response.output_text.done":
				writeResponsesEvent(&stream, snapshot, map[string]any{
					"output_index": 0, "content_index": 0, "text": complete,
				})
			case "response.output_item.done":
				writeResponsesEvent(&stream, snapshot, map[string]any{
					"output_index": 0,
					"item": map[string]any{"id": "msg_snapshot", "type": "message", "content": []any{
						map[string]any{"type": "output_text", "text": complete},
					}},
				})
			}
			text := ""
			if snapshot == "response.completed" {
				text = complete
			}
			writeAIResponsesCompleted(&stream, text, map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14})

			result, err := readAIToolStream(strings.NewReader(stream.String()))
			if err != nil {
				t.Fatalf("read complete stream: %v", err)
			}
			if result.Content != complete {
				t.Fatalf("complete snapshot must replace partial delta: got %q", result.Content)
			}
			if result.Usage.TotalTokens != 14 || !strings.Contains(result.Raw, `"delta":"action"`) {
				t.Fatalf("usage and raw stream must be retained: %+v", result)
			}
		})
	}
}

func TestReadAIResponsesStreamKeepsTextPartsInOutputOrder(t *testing.T) {
	var stream strings.Builder
	for _, part := range []struct {
		output, content int
		text            string
	}{
		{1, 0, `"乙"]}`},
		{0, 1, `"甲","甲",`},
		{0, 0, `{"answers":[`},
	} {
		writeResponsesEvent(&stream, "response.output_text.delta", map[string]any{
			"output_index": part.output, "content_index": part.content, "delta": part.text,
		})
		writeResponsesEvent(&stream, "response.output_text.done", map[string]any{
			"output_index": part.output, "content_index": part.content, "text": part.text,
		})
	}
	writeAIResponsesCompleted(&stream, "", nil)
	result, err := readAIToolStream(strings.NewReader(stream.String()))
	if err != nil {
		t.Fatalf("read multi-part stream: %v", err)
	}
	if result.Content != `{"answers":["甲","甲","乙"]}` {
		t.Fatalf("text parts must be ordered and snapshots must not duplicate deltas: %q", result.Content)
	}
}

func TestReadAIResponsesStreamKeepsCompletedItemsInOutputOrder(t *testing.T) {
	var stream strings.Builder
	for _, part := range []struct {
		index int
		text  string
	}{{1, `"4"]}`}, {0, `{"answers":[`}} {
		writeResponsesEvent(&stream, "response.output_text.delta", map[string]any{
			"output_index": part.index, "content_index": 0, "delta": "partial",
		})
		writeResponsesEvent(&stream, "response.output_item.done", map[string]any{
			"output_index": part.index,
			"item": map[string]any{"type": "message", "content": []any{
				map[string]any{"type": "output_text", "text": part.text},
			}},
		})
	}
	writeAIResponsesCompleted(&stream, "", nil)
	result, err := readAIToolStream(strings.NewReader(stream.String()))
	if err != nil {
		t.Fatalf("read complete item stream: %v", err)
	}
	if result.Content != `{"answers":["4"]}` {
		t.Fatalf("completed items must follow output_index even without a final output snapshot: %q", result.Content)
	}
}
