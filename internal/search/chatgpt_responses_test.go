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
		if _, exists := payload["max_output_tokens"]; exists {
			t.Fatalf("ordinary answer requests must use the upstream output allowance: %v", payload["max_output_tokens"])
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

func TestReadAIResponsesStreamPreservesLongProjectAnswerSnapshots(t *testing.T) {
	answer := strings.Repeat("实现代码和验证步骤必须完整保留。\n", 50000)
	content, err := json.Marshal(map[string]any{"answers": []string{answer}})
	if err != nil {
		t.Fatal(err)
	}
	var stream strings.Builder
	writeAIResponsesTextStream(&stream, []string{string(content)}, map[string]any{"input_tokens": 100, "output_tokens": 50000, "total_tokens": 50100})
	if stream.Len() <= 2*1024*1024 {
		t.Fatal("fixture must exceed the former stream limit")
	}
	result, err := readAIResponsesStream(strings.NewReader(stream.String()), nil)
	if err != nil {
		t.Fatalf("long valid answer and complete snapshot must be accepted: %v", err)
	}
	if result.Content != string(content) || result.Usage.OutputTokens != 50000 {
		t.Fatalf("long answer was truncated or duplicated: length=%d want=%d usage=%+v", len(result.Content), len(content), result.Usage)
	}
}

func TestReadAIResponsesStreamStillBoundsTransportMemory(t *testing.T) {
	const keepalive = ": keepalive\n"
	stream := strings.Repeat(keepalive, aiToolMaxStreamBytes/len(keepalive)+1)
	_, err := readAIResponsesStream(strings.NewReader(stream), nil)
	if err == nil || !strings.Contains(err.Error(), "响应流超过大小限制") {
		t.Fatalf("oversized transport must still fail explicitly: %v", err)
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

func TestReadAIResponsesStreamRetainsUsageOnFailureEvents(t *testing.T) {
	for _, eventType := range []string{"error", "response.failed"} {
		t.Run(eventType, func(t *testing.T) {
			var stream strings.Builder
			failure := map[string]any{"code": "context_length_exceeded", "message": "maximum context length exceeded"}
			payload := map[string]any{
				"response": map[string]any{
					"status": "failed", "error": failure,
					"usage": map[string]any{
						"input_tokens": 30, "output_tokens": 2, "total_tokens": 32,
						"input_tokens_details": map[string]any{"cached_tokens": 3},
					},
				},
			}
			if eventType == "error" {
				payload["error"] = failure
			}
			writeResponsesEvent(&stream, eventType, payload)

			result, err := readAIResponsesStream(strings.NewReader(stream.String()), nil)
			if err == nil || !strings.Contains(err.Error(), "context_length_exceeded") {
				t.Fatalf("failed events must still return the upstream error: %v", err)
			}
			if result.Usage.InputTokens != 30 || result.Usage.OutputTokens != 2 || result.Usage.TotalTokens != 32 || result.Usage.CachedInputTokens != 3 {
				t.Fatalf("failed response usage must remain available for billing: %+v", result.Usage)
			}
			if !strings.Contains(result.Raw, `"status":"failed"`) {
				t.Fatalf("failed response raw stream must remain available: %q", result.Raw)
			}
		})
	}
}
