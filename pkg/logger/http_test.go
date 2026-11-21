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
