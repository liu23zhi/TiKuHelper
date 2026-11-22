package logger

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHTTPAccessLogOmitsAuthorizationQueriesAndScriptAddresses(t *testing.T) {
	var output bytes.Buffer
	previous := gin.DefaultWriter
	gin.DefaultWriter = &output
	t.Cleanup(func() { gin.DefaultWriter = previous })
	router := gin.New()
	router.Use(AccessLogger())
	router.GET("/plain", func(c *gin.Context) { c.Status(200) })
	router.NoRoute(func(c *gin.Context) { c.Set(ScriptEntryLogKey, true); c.Status(200) })
	for _, target := range []string{"/plain?l=secret-capability&token=another-secret", "/secret-short-entry?l=secret-capability"} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil))
	}
	logged := output.String()
	for _, secret := range []string{"secret-capability", "another-secret", "secret-short-entry"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("HTTP log exposed %q", secret)
		}
	}
	if !strings.Contains(logged, "/plain") || !strings.Contains(logged, "[script-entry]") {
		t.Fatal("request diagnostics lost safe route labels")
	}
}

func TestHTTPAccessLogUsesRegisteredRoutesForUnmarkedRequests(t *testing.T) {
	var output bytes.Buffer
	previous := gin.DefaultWriter
	gin.DefaultWriter = &output
	t.Cleanup(func() { gin.DefaultWriter = previous })
	router := gin.New()
	router.Use(AccessLogger())
	router.GET("/api/scripts/:id", func(c *gin.Context) { c.Status(200) })
	// Public script lookup can exit before the controller identifies the entry.
	router.NoRoute(func(c *gin.Context) {
		switch c.Request.URL.Path {
		case "/private-scheduled":
			c.Status(403)
		case "/private-lookup-error":
			c.Status(500)
		default:
			c.Status(404)
		}
	})
	for _, sample := range []struct{ method, url, label string }{
		{"GET", "/private-scheduled", "[unmatched]"},
		{"HEAD", "/private-head", "[unmatched]"},
		{"GET", "/private-lookup-error", "[unmatched]"},
		{"GET", "/api/scripts/private-id?token=private-query", "/api/scripts/:id"},
	} {
		output.Reset()
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(sample.method, sample.url, nil))
		line := output.String()
		if strings.Contains(line, "private-") || !strings.Contains(line, sample.label) {
			t.Errorf("%s %s did not log only the safe route label: %q", sample.method, sample.url, line)
		}
	}
}
