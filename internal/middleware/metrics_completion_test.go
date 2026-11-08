package middleware

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/diagnostics"
)

func TestMetricsTransportCompletionPreservesResponseAndPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name   string
		code   int
		panic  bool
		commit bool
	}{
		{"success", 200, false, false}, {"denied", 403, false, false}, {"failure", 500, false, false},
		{"panic", 500, true, false}, {"committed-panic", 202, true, true}, {"unmatched", 404, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Date(2026, 10, 29, 1, 0, 0, 0, time.FixedZone("fixture", 8*3600))
			c := diagnostics.NewCollector(func() time.Time { return clock })
			old := diagnostics.Default
			diagnostics.Default = c
			t.Cleanup(func() { diagnostics.Default = old })
			router := gin.New()
			router.Use(gin.RecoveryWithWriter(io.Discard), RequestCorrelation)
			router.GET("/fixture/:id", func(ctx *gin.Context) {
				inside := c.Snapshot().HTTP
				if inside.Total != 1 || inside.InFlight != 1 {
					t.Errorf("HTTP_START not recorded: %+v", inside)
				}
				clock = clock.Add(27 * time.Millisecond)
				if tc.panic {
					if tc.commit {
						ctx.Status(tc.code)
						ctx.Writer.WriteHeaderNow()
					}
					panic("SYNTHETIC_PRIVATE_PANIC")
				}
				ctx.String(tc.code, "unchanged fixture response")
			})
			path := "/fixture/PRIVATE_PATH?key=PRIVATE_QUERY"
			if tc.name == "unmatched" {
				path = "/missing/PRIVATE_PATH"
				clock = clock.Add(27 * time.Millisecond)
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("Authorization", "Bearer PRIVATE_TOKEN")
			req.Header.Set("Cookie", "fixture=PRIVATE_COOKIE")
			router.ServeHTTP(w, req)
			s := c.Snapshot()
			wantFailed := uint64(0)
			if tc.code >= 400 {
				wantFailed = 1
			}
			if w.Code != tc.code || s.HTTP.Total != 1 || s.HTTP.InFlight != 0 || s.HTTP.Failed != wantFailed || s.HTTP.LatencyCount != 1 {
				t.Fatalf("HTTP_COMPLETION: HTTP=%d snapshot=%+v", w.Code, s.HTTP)
			}
			if !tc.panic && tc.name != "unmatched" && w.Body.String() != "unchanged fixture response" {
				t.Fatal("business body changed")
			}
			b, _ := json.Marshal(s)
			for _, sentinel := range []string{"PRIVATE_PATH", "PRIVATE_QUERY", "PRIVATE_TOKEN", "PRIVATE_COOKIE", "SYNTHETIC_PRIVATE_PANIC"} {
				if strings.Contains(string(b), sentinel) {
					t.Fatal("diagnostics retained private input")
				}
			}
			t.Logf("B04_HTTP status=%d total=%d inFlight=%d failed=%d samples=%d", w.Code, s.HTTP.Total, s.HTTP.InFlight, s.HTTP.Failed, s.HTTP.LatencyCount)
		})
	}
}

func TestMetricsTransportLoggingFailureDoesNotChangeBusiness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprint(panics), func(t *testing.T) {
			transportTestCapture(t, transportFailWriter{panics: panics})
			collector := diagnostics.NewCollector(nil)
			old := diagnostics.Default
			diagnostics.Default = collector
			t.Cleanup(func() { diagnostics.Default = old })
			router := gin.New()
			router.Use(RequestCorrelation)
			router.GET("/fixture", func(c *gin.Context) { c.String(200, "unchanged") })
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/fixture", nil))
			h := collector.Snapshot().HTTP
			if w.Code != 200 || w.Body.String() != "unchanged" || h.Total != 1 || h.Failed != 0 || h.LatencyCount != 1 {
				t.Fatalf("auxiliary logging changed response/counts: %d %+v", w.Code, h)
			}
		})
	}
}
