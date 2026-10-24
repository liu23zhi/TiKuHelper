package util

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoReadOnlyWithRetryRecoversTransientStatus(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := DoReadOnlyWithRetry(&http.Client{Timeout: 5 * time.Second}, req)
	if err != nil || resp.StatusCode != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("read-only GET retry: status=%v calls=%d err=%v", resp, calls.Load(), err)
	}
	_ = resp.Body.Close()
}

func TestDoReadOnlyWithRetryDoesNotReplayBadRequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := DoReadOnlyWithRetry(&http.Client{Timeout: 5 * time.Second}, req)
	if err != nil || resp.StatusCode != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatalf("400 must not retry: status=%v calls=%d err=%v", resp, calls.Load(), err)
	}
	_ = resp.Body.Close()
}

func TestDoReadOnlyWithRetryRejectsMutation(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/submit", nil)
	if _, err := DoReadOnlyWithRetry(&http.Client{}, req); err == nil {
		t.Fatal("POST must not enter read-only retry helper")
	}
}

func TestDoReadOnlyWithRetryRespectsCancellation(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	_, err := DoReadOnlyWithRetry(&http.Client{}, req)
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("canceled request must not be sent: calls=%d err=%v", calls.Load(), err)
	}
}

func TestDoReadOnlyWithRetryDoesNotOutwaitDeadline(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	started := time.Now()
	resp, err := DoReadOnlyWithRetry(&http.Client{Timeout: time.Second}, req)
	if err != nil || resp.StatusCode != http.StatusTooManyRequests || calls.Load() != 1 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("long Retry-After must return final response: status=%v calls=%d err=%v", resp, calls.Load(), err)
	}
	_ = resp.Body.Close()
}

func TestReadOnlyRetryStatusClassification(t *testing.T) {
	for _, test := range []struct {
		status int
		want   bool
	}{
		{408, true}, {425, true}, {429, true}, {500, true}, {599, true},
		{400, false}, {401, false}, {402, false}, {403, false}, {404, false}, {600, false},
	} {
		if got := retryableReadStatus(test.status); got != test.want {
			t.Errorf("status %d: retryable=%t, want %t", test.status, got, test.want)
		}
	}
}

func TestDoReadOnlyWithRetryExhaustsAtThreeAttempts(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := DoReadOnlyWithRetry(&http.Client{Timeout: 5 * time.Second}, req)
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable || calls.Load() != 3 {
		t.Fatalf("retry exhaustion: status=%v calls=%d err=%v", resp, calls.Load(), err)
	}
	_ = resp.Body.Close()
}

func TestDoReadOnlyWithRetryHonorsZeroRetryAfter(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	started := time.Now()
	resp, err := DoReadOnlyWithRetry(&http.Client{Timeout: 5 * time.Second}, req)
	if err != nil || resp.StatusCode != http.StatusOK || calls.Load() != 2 || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("Retry-After: 0 should retry immediately: status=%v calls=%d err=%v", resp, calls.Load(), err)
	}
	_ = resp.Body.Close()
}

func TestDoReadOnlyWithRetryRecoversLostResponse(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := DoReadOnlyWithRetry(&http.Client{Timeout: 5 * time.Second}, req)
	if err != nil || resp.StatusCode != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("lost response should retry: status=%v calls=%d err=%v", resp, calls.Load(), err)
	}
	_ = resp.Body.Close()
}

func TestDoReadOnlyWithRetryStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	_, err := DoReadOnlyWithRetry(&http.Client{Timeout: 5 * time.Second}, req)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("canceled retry must stop: calls=%d err=%v", calls.Load(), err)
	}
}

type readRetryRoundTrip func(*http.Request) (*http.Response, error)

func (fn readRetryRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

type countedReadCloser struct {
	io.ReadCloser
	closed *atomic.Int64
}

func (body *countedReadCloser) Close() error {
	body.closed.Add(1)
	return body.ReadCloser.Close()
}

func TestDoReadOnlyWithRetryClosesDiscardedBody(t *testing.T) {
	var calls atomic.Int64
	var closed atomic.Int64
	client := &http.Client{Timeout: 5 * time.Second, Transport: readRetryRoundTrip(func(_ *http.Request) (*http.Response, error) {
		status := http.StatusServiceUnavailable
		if calls.Add(1) == 2 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: &countedReadCloser{ReadCloser: io.NopCloser(strings.NewReader("response")), closed: &closed}}, nil
	})}
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/read", nil)
	resp, err := DoReadOnlyWithRetry(client, req)
	if err != nil || calls.Load() != 2 || closed.Load() != 1 {
		t.Fatalf("discarded response body must close before retry: calls=%d closed=%d err=%v", calls.Load(), closed.Load(), err)
	}
	_ = resp.Body.Close()
	if closed.Load() != 2 {
		t.Fatalf("final response body must remain caller-owned: closed=%d", closed.Load())
	}
}
