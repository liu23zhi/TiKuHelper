package uapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestFuturePolicyNoKeyClientNeverCallsPhoneOrIP(t *testing.T) {
	var calls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ip":"8.8.8.8","province":"广东","city":"深圳"}`))
	}))
	defer s.Close()
	for _, key := range []string{"", "  "} {
		c := NewClient(key).SetPhoneInfoURL(s.URL).SetIPInfoURL(s.URL)
		if _, err := c.GetPhoneInfo(context.Background(), "13800138000"); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("phone key=%q err=%v", key, err)
		}
		for _, source := range []string{"", SourceCommercial} {
			if _, err := c.GetIPInfo(context.Background(), "8.8.8.8", source); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("IP source=%q key=%q err=%v", source, key, err)
			}
		}
	}
	t.Logf("no-key client requests=%d", calls.Load())
	if calls.Load() != 0 {
		t.Errorf("no-key client requests must be 0")
	}
}
