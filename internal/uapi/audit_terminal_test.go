package uapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-resty/resty/v2"
)

func TestUAPIAuditWebTerminalOutcomeIsNotASecondProviderCall(t *testing.T) {
	for _, tc := range []struct {
		name, response, callOutcome, workflowOutcome string
		cancel                                       bool
		wantErr                                      error
	}{
		{"empty", `{"task_id":"raw-marker-task","status":"completed","result":{"markdown":"   "}}`, "empty_result", "empty_result", false, ErrUpstream},
		{"complete", `{"task_id":"raw-marker-task","status":"completed","result":{"markdown":"raw-marker-body"}}`, "completed", "completed", false, nil},
		{"provider_failure", `{"task_id":"raw-marker-task","status":"failed","error":"raw-marker-error"}`, "failed", "failed", false, ErrUpstream},
		{"interrupted_before_poll", `{"task_id":"raw-marker-task","status":"queued"}`, "queued", "cancelled", true, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sink bytes.Buffer
			old := gin.DefaultWriter
			gin.DefaultWriter = &sink
			defer func() { gin.DefaultWriter = old }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost {
					t.Error("unexpected poll/duplicate external request")
				}
				w.Header().Set("X-Request-ID", "raw-marker-header")
				fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			client := NewClient("uapi-local-test").SetWebToMarkdownURL(server.URL).SetMaxRetries(0)
			if tc.cancel {
				// Cancel after receiving submission, not before the transport call.
				client.http.OnAfterResponse(func(_ *resty.Client, _ *resty.Response) error { cancel(); return nil })
			}
			task, err := client.ReadWebPage(ctx, "https://raw-marker.invalid/")
			if !errors.Is(err, tc.wantErr) || requests.Load() != 1 || task.TaskID != "raw-marker-task" {
				t.Fatalf("existing call/return contract changed: task=%q status=%s err=%v requests=%d", task.TaskID, task.Status, err, requests.Load())
			}
			if strings.Contains(sink.String(), "raw-marker") {
				t.Fatal("a URL, provider header, error, task ID or body was logged")
			}
			var calls []providerAuditEvent
			var terminal map[string]any
			for _, line := range strings.Split(sink.String(), "\n") {
				if _, raw, ok := strings.Cut(line, "uapi_call "); ok {
					var row providerAuditEvent
					if err := json.Unmarshal([]byte(raw), &row); err != nil {
						t.Fatal(err)
					}
					calls = append(calls, row)
				}
				if _, raw, ok := strings.Cut(line, "uapi_workflow "); ok {
					if terminal != nil {
						t.Fatal("duplicated workflow completion")
					}
					if err := json.Unmarshal([]byte(raw), &terminal); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(calls) != 2 || calls[0].WorkflowID == "" || calls[0].WorkflowID != calls[1].WorkflowID ||
				calls[1].Attempts != 1 || calls[1].Outcome != tc.callOutcome || terminal["workflow_id"] != calls[1].WorkflowID ||
				terminal["outcome"] != tc.workflowOutcome || terminal["provider_call"] != false {
				t.Fatalf("wrong lifecycle: calls=%+v workflow=%+v", calls, terminal)
			}
			for _, forbidden := range []string{"category", "attempts", "retries"} {
				if _, present := terminal[forbidden]; present {
					t.Fatalf("workflow emits provider counting field %q", forbidden)
				}
			}
		})
	}
}
