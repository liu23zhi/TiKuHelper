package api

import (
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type integrationLocalTransport func(*http.Request) (*http.Response, error)

func (f integrationLocalTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestIntegrationSQPRequiresAuthentication(t *testing.T) {
	before := http.DefaultTransport
	defer func() { http.DefaultTransport = before }()
	calls := 0
	http.DefaultTransport = integrationLocalTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("synthetic local-only result")), Request: r}, nil
	})
	g := gin.New()
	SetAPIRouter(g)
	r := httptest.NewRequest("GET", "/sqp/api/parse?file=synthetic", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 401 || calls != 0 {
		t.Fatalf("unauthenticated smart import status=%d synthetic upstream calls=%d", w.Code, calls)
	}
}
