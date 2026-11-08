package middleware

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntegrationOpenTokenCORS(t *testing.T) {
	for _, origin := range []string{"https://arbitrary-client.fixture.invalid", "chrome-extension://fixture", "null"} {
		t.Run(origin, func(t *testing.T) {
			g := gin.New()
			g.Use(CORS)
			g.GET("/", func(c *gin.Context) { c.Status(200) })
			r := httptest.NewRequest(http.MethodOptions, "/", nil)
			r.Header.Set("Origin", origin)
			r.Header.Set("Access-Control-Request-Headers", "Authorization, Idempotency-Key")
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatalf("token-only CORS status=%d headers=%v", w.Code, w.Header())
			}
			if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") {
				t.Fatal("idempotency header lost")
			}
			if !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "X-Request-ID") || !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "Retry-After") {
				t.Fatal("diagnostic/retry headers hidden")
			}
		})
	}
}
