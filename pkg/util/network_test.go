package util

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSafeHTTPClientBlocksLoopbackByDefault(t *testing.T) {
	t.Setenv("TIKU_ALLOW_PRIVATE_OUTBOUND", "false")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if _, err := NewSafeHTTPClient(time.Second).Get(server.URL); err == nil {
		t.Fatal("expected loopback request to be blocked")
	}
}
