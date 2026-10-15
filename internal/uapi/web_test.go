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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"任务不存在","message":"已过期","code":404}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task_id":"task-2","status":"pending"}`))
	}))
	defer server.Close()
	client := NewClient(testAPIKey).SetWebToMarkdownURL(server.URL).SetMaxRetries(0)
	if _, err := client.GetWebToMarkdownStatus(context.Background(), "task-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if _, err := client.ReadWebPage(context.Background(), "https://example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("submission status lookup should preserve not found, got %v", err)
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
