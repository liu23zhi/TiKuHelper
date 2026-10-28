package uapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebToMarkdownUsesDocumentedEndpointsAndBearerHeader(t *testing.T) {
	var submitted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.URL.Path != "/api/v1/web/tomarkdown/async" {
				t.Fatalf("unexpected submit path: %s", r.URL.Path)
			}
			if got := r.URL.Query().Get("url"); got != "https://example.com/a?b=1" {
				t.Fatalf("url query = %q", got)
			}
			if r.Header.Get("Authorization") != "Bearer "+testAPIKey {
				t.Fatalf("missing bearer header: %q", r.Header.Get("Authorization"))
			}
			submitted.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "task-1", "status": "pending", "url": gotURL(r)})
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/web/tomarkdown/async/task-1" {
			t.Fatalf("unexpected status request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"task_id": "task-1", "status": "completed", "result": map[string]any{"markdown": "# 标题", "size": 8}})
	}))
	defer server.Close()

	client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL + "/api/v1/web/tomarkdown/async").SetMaxRetries(0)
	task, err := client.ReadWebPage(context.Background(), " https://example.com/a?b=1 ")
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Load() != 1 || task.TaskID != "task-1" || task.Status != "completed" || task.Result == nil || task.Result.Markdown != "# 标题" {
		t.Fatalf("unexpected task: %+v submissions=%d", task, submitted.Load())
	}
}

func TestWebToMarkdownRejectsInvalidURLWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL)
	for _, raw := range []string{"", "ftp://example.com", "http://127.0.0.1/", "http://localhost/", "http://192.168.1.1/", "https://user:pass@example.com/", "https://example.com/#fragment"} {
		if _, err := client.SubmitWebToMarkdown(context.Background(), raw); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("url %q should be rejected: %v", raw, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid URLs must not issue requests")
	}
}

func TestWebToMarkdownMapsNotFoundAndFailedStatus(t *testing.T) {
	const submittedResponse = `{"task_id":"task-2","status":"pending"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"任务不存在","message":"已过期","code":404}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(submittedResponse))
	}))
	defer server.Close()
	client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL).SetMaxRetries(0)
	if _, err := client.GetWebToMarkdownStatus(context.Background(), "task-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	task, err := client.ReadWebPage(context.Background(), "https://example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("submission status lookup should preserve not found, got %v", err)
	}
	if task.TaskID != "task-2" || task.Status != "pending" || task.RawJSON != submittedResponse {
		t.Fatalf("failed status lookup lost the last submitted task: %+v", task)
	}
}

func TestReadWebPagePreservesFailedTaskResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"task_id":"task-failed","status":"pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"task_id":"task-failed","status":"failed","error":"抓取失败","message":"上游拒绝"}`))
	}))
	defer server.Close()
	client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL).SetMaxRetries(0)
	task, err := client.ReadWebPage(context.Background(), "https://example.com")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("expected upstream failure, got %v", err)
	}
	if task.Status != "failed" || !strings.Contains(task.RawJSON, `"error":"抓取失败"`) {
		t.Fatalf("failed task response was not preserved: %+v", task)
	}
}

func TestReadWebPagePollsQueuedTaskUntilCompleted(t *testing.T) {
	var submissions, polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			submissions.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"created_at":"2026-10-28T09:45:41.244156206Z","message":"任务已提交，请使用task_id查询结果","status":"queued","task_id":"task-queued","url":"https://example.com/article"}`))
		case http.MethodGet:
			if r.URL.Path != "/async/task-queued" {
				t.Errorf("polling the wrong task: %s", r.URL.Path)
			}
			switch polls.Add(1) {
			case 1:
				_, _ = w.Write([]byte(`{"status":"queued"}`))
			case 2:
				_, _ = w.Write([]byte(`{"status":"processing"}`))
			default:
				_, _ = w.Write([]byte(`{"status":"completed","result":{"markdown":"# Real page body"}}`))
			}
		default:
			t.Errorf("unexpected request: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL + "/async").SetMaxRetries(0)
	task, err := client.ReadWebPage(context.Background(), "https://example.com/article")
	if err != nil || task.Status != "completed" || task.TaskID != "task-queued" || task.Result == nil || task.Result.Markdown != "# Real page body" {
		t.Fatalf("queued task did not complete: task=%+v err=%v", task, err)
	}
	if submissions.Load() != 1 || polls.Load() != 3 {
		t.Fatalf("queued state must poll the same task without resubmitting: submits=%d polls=%d", submissions.Load(), polls.Load())
	}
}

func TestReadWebPageQueuedCancellationKeepsTaskDiagnostics(t *testing.T) {
	const submittedResponse = `{"task_id":"task-waiting","status":"queued","message":"任务已提交"}`
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			var requests atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := NewClient(testAPIKey).SetWebToMarkdownURL("https://example.com/async").SetMaxRetries(0)
			client.http.SetTransport(webTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				if r.Method == http.MethodPost {
					return &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(submittedResponse)), Request: r}, nil
				}
				if r.Method != http.MethodGet || r.URL.Path != "/async/task-waiting" {
					t.Errorf("unexpected status request: %s %s", r.Method, r.URL.Path)
				}
				// Reaching the status request proves that the queued response was parsed.
				if deadline {
					return nil, context.DeadlineExceeded
				}
				cancel()
				return nil, r.Context().Err()
			}))
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			}
			task, err := client.ReadWebPage(ctx, "https://example.com/article")
			if !errors.Is(err, want) || task.TaskID != "task-waiting" || task.Status != "queued" || task.RawJSON != submittedResponse || requests.Load() != 2 {
				t.Fatalf("cancelled queued task lost its diagnostics or continued calling: task=%+v err=%v requests=%d", task, err, requests.Load())
			}
		})
	}
}

func TestReadWebPageTerminalStatesDoNotPoll(t *testing.T) {
	for _, scenario := range []struct {
		name, response, wantMessage string
	}{
		{"failed", `{"task_id":"terminal","status":"failed","error":"抓取失败"}`, "抓取失败"},
		{"timeout", `{"task_id":"terminal","status":"timeout","message":"任务超时"}`, "任务超时"},
		{"empty-completed", `{"task_id":"terminal","status":"completed","result":{"markdown":"  "}}`, "结果为空"},
		{"unknown", `{"task_id":"terminal","status":"unexpected","result":{"markdown":"# Must not imply success"}}`, "未知任务状态"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(scenario.response))
			}))
			defer server.Close()
			client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL).SetMaxRetries(0)
			task, err := client.ReadWebPage(context.Background(), "https://example.com/article")
			if !errors.Is(err, ErrUpstream) || !strings.Contains(err.Error(), scenario.wantMessage) || task.RawJSON != scenario.response || requests.Load() != 1 {
				t.Fatalf("terminal state did not stop with original evidence: task=%+v err=%v requests=%d", task, err, requests.Load())
			}
		})
	}
}

func TestReadWebPageNormalizesStatusForCallers(t *testing.T) {
	const response = `{"task_id":"normalized","status":" COMPLETED ","result":{"markdown":"# Page"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()
	client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL).SetMaxRetries(0)
	task, err := client.ReadWebPage(context.Background(), "https://example.com/article")
	if err != nil || task.Status != "completed" || task.RawJSON != response {
		t.Fatalf("callers should receive canonical status without losing raw response: task=%+v err=%v", task, err)
	}
}

func TestNormalizeWebURLKeepsEncodedQuery(t *testing.T) {
	got, err := normalizeWebURL("https://example.com/a?q=中 文")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "q=中%20文") && !strings.Contains(got, "q=中 文") {
		t.Fatalf("unexpected normalized URL: %q", got)
	}
}

func gotURL(r *http.Request) string { return r.URL.Query().Get("url") }

type webTestRoundTripper func(*http.Request) (*http.Response, error)

func (transport webTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}
