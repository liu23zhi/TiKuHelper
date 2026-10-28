package uapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadWebPageCommonWaitingStatesPollSameTask(t *testing.T) {
	for _, status := range []string{
		"accepted", "submitted", "waiting", "scheduled", "enqueued", "in_queue",
		"running", "started", "in_progress", " IN-PROGRESS ", "in progress",
	} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			var submissions, polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					submissions.Add(1)
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "same-task", "status": status})
					return
				}
				polls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/async/same-task" {
					t.Errorf("unexpected polling request: %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"status":"completed","result":{"markdown":"# Page"}}`))
			}))
			defer server.Close()
			client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL + "/async").SetMaxRetries(0)
			task, err := client.ReadWebPage(context.Background(), "https://example.com/article")
			if err != nil || task.Status != "completed" || task.TaskID != "same-task" || task.Result == nil || task.Result.Markdown != "# Page" || submissions.Load() != 1 || polls.Load() != 1 {
				t.Fatalf("waiting state %q must poll without resubmitting: task=%+v err=%v submits=%d polls=%d", status, task, err, submissions.Load(), polls.Load())
			}
		})
	}
}

func TestReadWebPageCommonSuccessStatesRequireBody(t *testing.T) {
	for _, status := range []string{"complete", "success", " SuCcEeDeD ", "done", "finished"} {
		for _, markdown := range []string{"# Page", "  "} {
			name := status + "/body"
			if strings.TrimSpace(markdown) == "" {
				name = status + "/empty"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				response, err := json.Marshal(map[string]any{
					"task_id": "finished-task", "status": status,
					"result": map[string]any{"markdown": markdown},
				})
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					_, _ = w.Write(response)
				}))
				defer server.Close()
				client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL).SetMaxRetries(0)
				task, err := client.ReadWebPage(context.Background(), "https://example.com/article")
				if strings.TrimSpace(markdown) == "" {
					if !errors.Is(err, ErrUpstream) || !strings.Contains(err.Error(), "结果为空") {
						t.Fatalf("success alias without body must fail: task=%+v err=%v", task, err)
					}
				} else if err != nil || task.Result == nil || task.Result.Markdown != markdown {
					t.Fatalf("success alias with body did not complete: task=%+v err=%v", task, err)
				}
				if task.Status != "completed" || task.RawJSON != string(response) || requests.Load() != 1 {
					t.Fatalf("success state should be canonical and terminal with raw evidence: task=%+v requests=%d", task, requests.Load())
				}
			})
		}
	}
}

func TestReadWebPageCommonFailureStatesStopEvenWithBody(t *testing.T) {
	for _, scenario := range []struct {
		status, canonical, fallbackMessage string
	}{
		{"failure", "failed", "任务失败"},
		{"error", "failed", "任务失败"},
		{"errored", "failed", "任务失败"},
		{"rejected", "failed", "任务失败"},
		{"canceled", "cancelled", "任务已取消"},
		{" CANCELLED ", "cancelled", "任务已取消"},
		{"aborted", "cancelled", "任务已取消"},
		{"timed_out", "timeout", "任务超时"},
		{"timed-out", "timeout", "任务超时"},
		{"expired", "timeout", "任务超时"},
	} {
		t.Run(scenario.status, func(t *testing.T) {
			var requests atomic.Int32
			response, err := json.Marshal(map[string]any{
				"task_id": "stopped-task", "status": scenario.status,
				"result": map[string]any{"markdown": "# Must not count as success"},
			})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write(response)
			}))
			defer server.Close()
			client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL).SetMaxRetries(0)
			task, err := client.ReadWebPage(context.Background(), "https://example.com/article")
			if !errors.Is(err, ErrUpstream) || !strings.Contains(err.Error(), scenario.fallbackMessage) || task.Status != scenario.canonical || task.RawJSON != string(response) || requests.Load() != 1 {
				t.Fatalf("failure alias must stop without accepting the body: task=%+v err=%v requests=%d", task, err, requests.Load())
			}
		})
	}
}
