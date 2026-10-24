package search

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPaidProviderDoesNotReplayLostResponse(t *testing.T) {
	for _, test := range []struct {
		name string
		post func(string) error
	}{
		{"icodef", func(url string) error { _, err := (&IcodefClient{}).getHTTPClient().R().Post(url); return err }},
		{"wanneng", func(url string) error { _, err := (&WannengClient{}).getHTTPClient().R().Post(url); return err }},
		{"tikuhai", func(url string) error { _, err := (&TikuhaiClient{}).getHTTPClient().R().Post(url); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()
			if err := test.post(server.URL); err == nil || calls.Load() != 1 {
				t.Fatalf("provider POST with unknown result must not replay: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestProviderRetriesOnlyExplicitRateLimit(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		post func(string) error
	}{
		{"icodef", "触发流控限制", func(url string) error { _, err := (&IcodefClient{}).getHTTPClient().R().Post(url); return err }},
		{"wanneng", "已限流,正在重新请求...", func(url string) error { _, err := (&WannengClient{}).getHTTPClient().R().Post(url); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					_, _ = w.Write([]byte(test.body))
					return
				}
				_, _ = w.Write([]byte("ok"))
			}))
			defer server.Close()
			if err := test.post(server.URL); err != nil || calls.Load() != 2 {
				t.Fatalf("explicit rate limit should retry once: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestTikuhaiDoesNotReplayGatewayError(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	resp, err := (&TikuhaiClient{}).getHTTPClient().R().Post(server.URL)
	if err != nil || resp.StatusCode() != http.StatusBadGateway || calls.Load() != 1 {
		t.Fatalf("5xx result may be charged and must not be replayed: calls=%d resp=%v err=%v", calls.Load(), resp, err)
	}
}
