package search

// Owned QA loopback mock, only inside the Root-reviewed network=none container.
// Calls the unchanged production loop/client factory; no transport injection.
// Regression coverage for existing tool-failure final loop; isolated owned loopback mock.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/model"
)

const a01FailureFinalLoopPrivate = "A01_PRIVATE_SYNTHETIC_TOOL_DIAGNOSTIC"
const a01FailureFinalLoopMarker = "A01_FAILURE_FINAL_LOOP_OBSERVATION "

type a01FailureFinalLoopTool struct {
	Calls     int
	FetchFull bool
	Query     string
}

func (tool *a01FailureFinalLoopTool) SearchAggregate(ctx context.Context, req uapi.SearchAggregateRequest) (uapi.SearchAggregateResponse, error) {
	tool.Calls++
	tool.FetchFull = req.FetchFull
	tool.Query = req.Query
	return uapi.SearchAggregateResponse{}, errors.New(a01FailureFinalLoopPrivate)
}

func a01FailureFinalLoopEvent(kind string, value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return "event: " + kind + "\ndata: " + string(body) + "\n\n"
}
func a01FailureFinalLoopToolSSE(callID string) string {
	arguments, _ := json.Marshal(map[string]string{"query": "合成算术6"})
	n := len(arguments) / 2
	for n > 0 && !utf8.RuneStart(arguments[n]) {
		n--
	}
	if !utf8.Valid(arguments[:n]) || !utf8.Valid(arguments[n:]) {
		panic("A01_FIXTURE_INFRA invalid UTF8 split")
	}
	added := map[string]any{"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": "web_search", "arguments": ""}
	done := map[string]any{"type": "function_call", "id": "fc_" + callID, "call_id": callID, "name": "web_search", "arguments": string(arguments), "status": "completed"}
	return a01FailureFinalLoopEvent("response.output_item.added", map[string]any{"output_index": 0, "item": added}) +
		a01FailureFinalLoopEvent("response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": "fc_" + callID, "delta": string(arguments[:n])}) +
		a01FailureFinalLoopEvent("response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": "fc_" + callID, "delta": string(arguments[n:])}) +
		a01FailureFinalLoopEvent("response.output_item.done", map[string]any{"output_index": 0, "item": done}) +
		a01FailureFinalLoopEvent("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{done}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14, "input_tokens_details": map[string]int{"cached_tokens": 2}}}})
}
func a01FailureFinalLoopAnswerSSE() string {
	content := `{"answers":["6"]}`
	message := map[string]any{"type": "message", "id": "msg_final", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": content, "annotations": []any{}}}}
	return a01FailureFinalLoopEvent("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "delta": content}) +
		a01FailureFinalLoopEvent("response.completed", map[string]any{"response": map[string]any{"status": "completed", "output": []any{message}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 3, "total_tokens": 23}}})
}

type a01FailureFinalLoopTransport struct {
	mu                  sync.Mutex
	Branch              string
	AllowedHost         string
	HTTP                int
	Payloads            []map[string]json.RawMessage
	Problems            []string
	InternalToolMessage string
	SecondToolsAbsent   bool
	SecondChoiceAbsent  bool
	FinalSystem         bool
	ToolMessageCount    int
}

func (rt *a01FailureFinalLoopTransport) problem(s string) { rt.Problems = append(rt.Problems, s) }
func (rt *a01FailureFinalLoopTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.HTTP++
	reject := func(message string) (*http.Response, error) {
		rt.problem(message)
		return &http.Response{StatusCode: 422, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader("synthetic strict fixture mismatch")), Request: req}, nil
	}
	if rt.HTTP > 2 {
		return reject("more than two HTTP requests")
	}
	if req.Method != http.MethodPost || req.URL.Scheme != "http" || req.URL.Host != rt.AllowedHost || req.URL.Path != "/v1/responses" || req.URL.RawQuery != "" {
		return reject("unexpected exact method/URL; never delegated to network")
	}
	if req.Header.Get("Authorization") != "Bearer synthetic-no-provider" {
		return reject("synthetic authorization mismatch")
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		return reject("request body read error")
	}
	var payload map[string]json.RawMessage
	if err = json.Unmarshal(raw, &payload); err != nil {
		return reject("request JSON mismatch")
	}
	rt.Payloads = append(rt.Payloads, payload)
	var stream bool
	if json.Unmarshal(payload["stream"], &stream) != nil || !stream {
		rt.problem("stream positive control")
	}
	var store bool
	if json.Unmarshal(payload["store"], &store) != nil || store {
		rt.problem("Responses store=false positive control")
	}
	var max int
	if json.Unmarshal(payload["max_output_tokens"], &max) != nil || max != 4096 {
		rt.problem("output-bound positive control")
	}
	var body string
	if rt.HTTP == 1 {
		var tools []json.RawMessage
		if json.Unmarshal(payload["tools"], &tools) != nil || len(tools) != 1 {
			rt.problem("first tools count must be one")
		}
		var choice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(payload["tool_choice"], &choice) != nil || choice.Type != "function" || choice.Name != "web_search" {
			rt.problem("first required web_search choice")
		}
		body = a01FailureFinalLoopToolSSE("first-failure-only-call")
	} else {
		_, hasTools := payload["tools"]
		_, hasChoice := payload["tool_choice"]
		rt.SecondToolsAbsent = !hasTools
		rt.SecondChoiceAbsent = !hasChoice
		var messages []struct {
			Role       string          `json:"role"`
			Type       string          `json:"type"`
			Content    json.RawMessage `json:"content"`
			Output     json.RawMessage `json:"output"`
			ToolCallID string          `json:"call_id"`
		}
		if json.Unmarshal(payload["input"], &messages) != nil {
			rt.problem("second messages invalid")
		}
		toolMessages := 0
		finalSystem := false
		for _, m := range messages {
			if m.Type == "function_call_output" {
				toolMessages++
				if m.ToolCallID != "first-failure-only-call" {
					rt.problem("tool-call identity mismatch")
				}
				var content string
				if json.Unmarshal(m.Output, &content) != nil {
					rt.problem("tool content not string")
				}
				rt.InternalToolMessage = content // observation only; no new internal redaction policy
				var diagnostic map[string]string
				if json.Unmarshal([]byte(content), &diagnostic) != nil || diagnostic["error"] == "" {
					rt.problem("existing generic tool-error JSON absent")
				}
			}
			if m.Role == "system" {
				var content string
				if json.Unmarshal(m.Content, &content) == nil && strings.Contains(content, "这是最后一次模型调用") && strings.Contains(content, "不得调用工具") {
					finalSystem = true
				}
			}
		}
		rt.ToolMessageCount = toolMessages
		rt.FinalSystem = finalSystem
		if rt.Branch == "final_answers_success" {
			body = a01FailureFinalLoopAnswerSSE()
		} else {
			body = a01FailureFinalLoopToolSSE("unexpected-second-call")
		}
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}
func TestA01ToolFailureThenFinalLoopFocused(t *testing.T) {
	violations := 0
	positiveAll := true
	for _, branch := range []string{"final_answers_success", "final_still_calls_tool_refusal"} {
		t.Run(branch, func(t *testing.T) {
			tool := &a01FailureFinalLoopTool{}
			transport := &a01FailureFinalLoopTransport{Branch: branch}
			listener, listenErr := net.Listen("tcp4", "127.0.0.1:0")
			if listenErr != nil {
				t.Fatalf("A01_FIXTURE_INFRA owned loopback listen: %v", listenErr)
			}
			transport.AllowedHost = listener.Addr().String()
			host, _, splitErr := net.SplitHostPort(transport.AllowedHost)
			if splitErr != nil || host != "127.0.0.1" {
				listener.Close()
				t.Fatal("A01_FIXTURE_INFRA non-owned loopback")
			}
			closed := false
			server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Server Request.URL omits authority; restore the actual incoming Host for
				// strict matching, then use the synthetic response function without dialing.
				clone := r.Clone(r.Context())
				u := *r.URL
				u.Scheme = "http"
				u.Host = r.Host
				clone.URL = &u
				response, responseErr := transport.RoundTrip(clone)
				if responseErr != nil {
					http.Error(w, "synthetic fixture response error", 422)
					return
				}
				defer response.Body.Close()
				for k, values := range response.Header {
					for _, value := range values {
						w.Header().Add(k, value)
					}
				}
				w.WriteHeader(response.StatusCode)
				io.Copy(w, response.Body)
			})}}
			server.Start()
			t.Cleanup(func() {
				if !closed {
					server.Close()
					closed = true
				}
			})
			parsed, parseErr := url.Parse(server.URL)
			if parseErr != nil || parsed.Scheme != "http" || parsed.Host != transport.AllowedHost || parsed.Hostname() != "127.0.0.1" {
				t.Fatal("A01_FIXTURE_INFRA exact mock URL")
			}
			client := &ChatGptClient{Enable: true, BaseURL: server.URL + "/v1", APIKey: "synthetic-no-provider", Model: "synthetic-no-provider", SearchTool: tool}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			answers, debug, err := client.searchAnswerWithTools(ctx, model.SearchRequest{Question: "合成算术6", Options: []string{"6"}})
			cancel()
			server.Close()
			closed = true
			expectedUsage := AISearchUsage{InputTokens: 30, CachedInputTokens: 2, CacheWriteTokens: 0, OutputTokens: 7, TotalTokens: 37}
			outward := fmt.Sprint(answers)
			outcome := err == nil && reflect.DeepEqual(answers, [][]string{{"6"}})
			if branch == "final_still_calls_tool_refusal" {
				expectedUsage = AISearchUsage{InputTokens: 20, CachedInputTokens: 4, CacheWriteTokens: 0, OutputTokens: 8, TotalTokens: 28}
				outcome = err != nil && err.Error() == "AI模型在搜索结束后仍调用工具，无法生成最终答案" && len(answers) == 0
			}
			errorText := ""
			if err != nil {
				errorText = err.Error()
				outward += " " + errorText
			}
			positive := len(transport.Problems) == 0 && closed && host == "127.0.0.1" && tool.Query == "合成算术6"
			matched := outcome && transport.HTTP == 2 && tool.Calls == 1 && !tool.FetchFull && transport.SecondToolsAbsent && transport.SecondChoiceAbsent && transport.ToolMessageCount == 1 && transport.FinalSystem && !strings.Contains(outward, "PRIVATE_SYNTHETIC") && a01FailureFinalLoopTokenComponents(debug.Usage) == a01FailureFinalLoopTokenComponents(expectedUsage) && len(debug.Usage.ModelCalls) == 2 && len(debug.Usage.SearchCalls) == 1 && debug.Usage.SearchCalls[0].Status == "error"
			if !positive {
				positiveAll = false
			}
			if !matched {
				violations++
			}
			observation := map[string]any{
				"schema": "A01_TOOL_FAILURE_FINAL_LOOP_OBSERVATION_V1", "case": branch,
				"positive_controls": positive, "control_failures": transport.Problems,
				"loop_contract_satisfied": matched, "HTTP": transport.HTTP, "tool": tool.Calls,
				"FetchFull": tool.FetchFull, "query_preserved_UTF8": tool.Query == "合成算术6",
				"second_tools_absent": transport.SecondToolsAbsent, "second_tool_choice_absent": transport.SecondChoiceAbsent,
				"final_system_message": transport.FinalSystem, "tool_message_count": transport.ToolMessageCount,
				"internal_tool_message": transport.InternalToolMessage,
				"answers":               answers, "returned_error": errorText,
				"outward_private_diagnostic_absent": !strings.Contains(outward, "PRIVATE_SYNTHETIC"),
				"usage":                             map[string]int64{"InputTokens": debug.Usage.InputTokens, "CachedInputTokens": debug.Usage.CachedInputTokens, "CacheWriteTokens": debug.Usage.CacheWriteTokens, "OutputTokens": debug.Usage.OutputTokens, "TotalTokens": debug.Usage.TotalTokens},
				"owned_mock_host":                   transport.AllowedHost, "mock_close_returned": closed,
				"real_loop_unchanged": true, "real_client_factory_unchanged": true,
				"real_provider_tool_delegation": false, "zero_socket_claimed": false,
			}
			encoded, encodeErr := json.Marshal(observation)
			if encodeErr != nil {
				t.Fatalf("A01_FIXTURE_INFRA observation JSON: %v", encodeErr)
			}
			t.Log(a01FailureFinalLoopMarker + string(encoded))
			if !positive || !matched {
				t.Errorf("loop contract outcome=%v HTTP=%d tool=%d FetchFull=%v usage=%+v controlProblems=%v err=%v", outcome, transport.HTTP, tool.Calls, tool.FetchFull, debug.Usage, transport.Problems, err)
			}
		})
	}
	summary, _ := json.Marshal(map[string]any{"cases": 2, "positive_controls": positiveAll, "loop_violations": violations})
	t.Log("A01_FAILURE_FINAL_LOOP_CONTRACT " + string(summary))
}

// Compare every pre-existing token component without rejecting new per-call/trace slices.
func a01FailureFinalLoopTokenComponents(u AISearchUsage) [5]int64 {
	return [5]int64{u.InputTokens, u.CachedInputTokens, u.CacheWriteTokens, u.OutputTokens, u.TotalTokens}
}
