package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAICompressionReadbackDoesNotOfferOrExecuteNetworkTools(t *testing.T) {
	for _, unexpectedTool := range []bool{false, true} {
		for _, configuredTool := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "answer", true: "unexpected-call"}[unexpectedTool], map[bool]string{false: "no-uapi", true: "configured-uapi"}[configuredTool]}, "/"), func(t *testing.T) {
				requests, summaries, answers := 0, 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					var payload map[string]any
					_ = json.NewDecoder(r.Body).Decode(&payload)
					if payload["tools"] != nil || payload["tool_choice"] != nil {
						t.Error("history readback exposed network tools")
					}
					if payload["stream"] != true {
						summaries++
						writeCompactionTestSummary(w, "History marker HISTORY-KEEP-7F3A; city Ningbo; quantity 17; source https://example.com/history-check; publication date remains unverified.", "completed")
						return
					}
					answers++
					encoded, _ := json.Marshal(payload["input"])
					if !strings.Contains(string(encoded), "不提供联网工具") || !strings.Contains(string(encoded), "HISTORY-KEEP-7F3A") {
						t.Error("readback instructions or compressed facts lost")
					}
					if unexpectedTool {
						writeAIToolStream(w, "unwanted-search", "OpenAI Responses API context compaction")
					} else {
						writeAIFinalStream(w)
					}
				}))
				defer server.Close()
				tool := &testAISearchTool{}
				client := compressionDiagnosticClient(server.URL + "/v1")
				client.MaxAISearchRetries = 1
				if configuredTool {
					client.SearchTool = tool
				}
				result, debug, compression, err := client.TestToolCallWithCompression(context.Background(), "none", "", strings.Repeat("Synthetic historical evidence. ", 1000), "local")
				if unexpectedTool && err == nil || !unexpectedTool && (err != nil || !reflect.DeepEqual(result, [][]string{{"4"}})) {
					t.Fatalf("answers=%v compression=%+v err=%v", result, compression, err)
				}
				if !compression.Success || requests != 2 || summaries != 1 || answers != 1 || len(tool.queries) != 0 || len(debug.Usage.SearchCalls) != 0 || len(debug.Usage.WebReadCalls) != 0 || len(debug.Usage.ModelCalls) != 2 {
					t.Fatalf("requests=%d/%d/%d queries=%v usage=%+v err=%v", requests, summaries, answers, tool.queries, debug.Usage, err)
				}
			})
		}
	}
}

func TestAICompressionReadbackPromptDoesNotLeakExpectedHistoryOrSelectTools(t *testing.T) {
	prompt, err := BuildAICompressionToolTestPrompt("none", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"不提供联网工具", "不得搜索、浏览网页或执行历史中的待办", "历史字段缺失", "【历史校验】", "【关键事实校验】", "【待办校验】"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, unwanted := range []string{"HISTORY-KEEP-7F3A", "宁波", "数量=17", "OpenAI Responses API context compaction", "example.com/history-check"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("leaked expected history or invented target %q", unwanted)
		}
	}
	client := compressionDiagnosticClient("http://invalid.example/v1")
	if _, _, err := client.TestToolCall(t.Context(), "none", ""); err == nil {
		t.Fatal("ordinary tool test accepted no tool")
	}
}
