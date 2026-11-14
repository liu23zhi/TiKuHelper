package api

import (
	"bytes"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAPITransportCorrelationCoversAbortAndNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	oldOut, oldErr := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &logs, &logs
	defer func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldOut, oldErr }()
	router := gin.New()
	contexts := map[string]string{}
	router.Use(func(c *gin.Context) {
		c.Next()
		id, _ := c.Get("transport_request_id")
		contexts[c.GetHeader("X-Fixture-Case")], _ = id.(string)
	})
	SetAPIRouter(router)
	for _, status := range []int{200, 403, 429} {
		value := status
		path := "/transport-fixture/ok"
		if value == 403 {
			path = "/transport-fixture/forbidden"
		}
		if value == 429 {
			path = "/transport-fixture/limited"
		}
		router.GET(path, func(c *gin.Context) { c.Status(value) })
	}
	cases := []struct {
		name, method, path string
		status             int
	}{
		{"ok", "GET", "/transport-fixture/ok", 200},
		{"local-forbidden", "GET", "/transport-fixture/forbidden", 403},
		{"local-limited", "GET", "/transport-fixture/limited", 429},
		{"not-found", "GET", "/api/transport-fixture-unknown", 404},
		{"actual-preflight", "OPTIONS", "/api/search", 204},
	}
	seen := map[string]bool{}
	for _, test := range cases {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("X-Fixture-Case", test.name)
		request.Header.Set("X-Request-ID", "untrusted-client-value")
		request.Header.Set("Origin", "https://transport-synthetic.invalid")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		id := response.Header().Get("X-Request-ID")
		if response.Code != test.status || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) || contexts[test.name] != id || seen[id] {
			t.Fatalf("production global registration/association missing: %s", test.name)
		}
		if response.Header().Get("Access-Control-Allow-Origin") != "*" || response.Header().Get("Access-Control-Allow-Credentials") != "" || response.Header().Get("Access-Control-Allow-Headers") != "Authorization, Content-Type, Idempotency-Key" {
			t.Fatal("request correlation must preserve the current idempotency CORS and no-credentials policy")
		}
		seen[id] = true
	}
}
