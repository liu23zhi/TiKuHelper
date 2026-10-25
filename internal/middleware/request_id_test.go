package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type transportTestWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *transportTestWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *transportTestWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

type transportFailReader struct{}

func (transportFailReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic entropy failure")
}

type transportFailWriter struct{ panics bool }

func (w transportFailWriter) Write([]byte) (int, error) {
	if w.panics {
		panic("synthetic logging panic must not replace business outcome")
	}
	return 0, errors.New("synthetic logging failure")
}
func transportTestCapture(t *testing.T, writer io.Writer) {
	t.Helper()
	oldOut, oldErr := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = writer, writer
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldOut, oldErr })
}
func transportTestRows(t *testing.T, text string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for _, line := range strings.Split(text, "\n") {
		const marker = "transport_complete "
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line[i+len(marker):]), &row); err != nil {
			t.Fatalf("completion JSON: %v", err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestTransportIDUsesFullRandomBytes(t *testing.T) {
	var sequence atomic.Uint64
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	id := generateTransportRequestID(bytes.NewReader(data), time.Unix(1, 0), &sequence)
	if id != "000102030405060708090a0b0c0d0e0f" || sequence.Load() != 0 {
		t.Fatal("all sixteen random bytes must be encoded, without fallback")
	}
}

func TestTransportIDPartialEntropyUsesExplicitFallback(t *testing.T) {
	var sequence atomic.Uint64
	clock := time.Unix(1700000000, 123)
	id := generateTransportRequestID(bytes.NewReader([]byte{1, 2}), clock, &sequence)
	if id != "fallback-1700000000000000123-1" {
		t.Fatalf("partial entropy must use explicit fallback, got %q", id)
	}
}

func TestTransportFallbackPreservesResponsesAndConcurrentUniqueness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writer := &transportTestWriter{}
	transportTestCapture(t, writer)
	var sequence atomic.Uint64
	var handled atomic.Uint64
	router := gin.New()
	router.Use(func(c *gin.Context) {
		requestCorrelation(c, transportFailReader{}, time.Unix(1700000000, 0), &sequence)
	})
	router.GET("/fallback", func(c *gin.Context) {
		handled.Add(1)
		id, ok := c.Get("transport_request_id")
		if !ok || id != c.Writer.Header().Get("X-Request-ID") {
			c.AbortWithStatus(500)
			return
		}
		c.String(200, "unchanged business success")
	})
	const count = 32
	ids := make([]string, count)
	codes := make([]int, count)
	bodies := make([]string, count)
	var group sync.WaitGroup
	for i := range ids {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			request := httptest.NewRequest("GET", "/fallback", nil)
			request.Header.Set("X-Request-ID", "client-controlled")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			ids[i], codes[i], bodies[i] = response.Header().Get("X-Request-ID"), response.Code, response.Body.String()
		}(i)
	}
	group.Wait()
	seen := map[string]bool{}
	for i, id := range ids {
		if !strings.HasPrefix(id, "fallback-1700000000000000000-") || seen[id] || codes[i] != 200 || bodies[i] != "unchanged business success" {
			t.Fatal("fallback must preserve responses and be process-locally unique")
		}
		seen[id] = true
	}
	if handled.Load() != count || sequence.Load() != count || len(transportTestRows(t, writer.text())) != count {
		t.Fatal("all fallback requests must execute and remain correlated")
	}
}

func TestTransportCompletionUsesOnlySafeFieldsAndRoutePattern(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writer := &transportTestWriter{}
	transportTestCapture(t, writer)
	router := gin.New()
	router.Use(RequestCorrelation)
	router.POST("/resource/:id", func(c *gin.Context) { c.String(200, "original") })
	request := httptest.NewRequest("POST", "/resource/PATH_SENTINEL?token=QUERY_SENTINEL", strings.NewReader("BODY_SENTINEL"))
	request.Header[http.CanonicalHeaderKey("X-Request-ID")] = []string{"CLIENT_SENTINEL", "DUPLICATE_SENTINEL"}
	request.Header.Set("Cookie", "fixture=COOKIE_SENTINEL")
	request.Header.Set("Authorization", "Bearer AUTH_SENTINEL")
	request.Header.Set("X-API-Key", "APIKEY_SENTINEL")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	rows := transportTestRows(t, writer.text())
	if response.Code != 200 || response.Body.String() != "original" || len(rows) != 1 {
		t.Fatal("completion must not change the response")
	}
	row := rows[0]
	keys := []string{}
	for key := range row {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "duration_ms,event,method,request_id,route,status" || row["event"] != "transport_complete" || row["route"] != "/resource/:id" || row["method"] != "POST" || row["status"] != float64(200) || row["request_id"] != response.Header().Get("X-Request-ID") {
		t.Fatal("completion field set or association changed")
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(response.Header().Get("X-Request-ID")) {
		t.Fatal("normal production path must provide random-format server ID")
	}
	if value, ok := row["duration_ms"].(float64); !ok || value < 0 {
		t.Fatal("duration must be a nonnegative number")
	}
	for _, sentinel := range []string{"PATH_SENTINEL", "QUERY_SENTINEL", "BODY_SENTINEL", "CLIENT_SENTINEL", "DUPLICATE_SENTINEL", "COOKIE_SENTINEL", "AUTH_SENTINEL", "APIKEY_SENTINEL"} {
		if strings.Contains(writer.text(), sentinel) {
			t.Fatal("new completion must not contain client input")
		}
	}
}

func TestTransportCompletionRecordsOrdinary500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writer := &transportTestWriter{}
	transportTestCapture(t, writer)
	router := gin.New()
	router.Use(RequestCorrelation)
	router.GET("/failure", func(c *gin.Context) { c.String(500, "original failure") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/failure", nil))
	rows := transportTestRows(t, writer.text())
	if response.Code != 500 || response.Body.String() != "original failure" || len(rows) != 1 || rows[0]["status"] != float64(500) || !strings.Contains(writer.text(), "[ERROR]") {
		t.Fatal("ordinary 500 must retain response and safe error-level completion")
	}
}

func TestTransportCompletionPreservesPanicRecoveryStatus(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "before-headers"
		if committed {
			name = "already-committed202"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			writer := &transportTestWriter{}
			transportTestCapture(t, writer)
			router := gin.New()
			router.Use(gin.RecoveryWithWriter(io.Discard), RequestCorrelation)
			router.GET("/panic", func(c *gin.Context) {
				if committed {
					c.Status(202)
					c.Writer.WriteHeaderNow()
				}
				panic("PANIC_VALUE_SENTINEL")
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("GET", "/panic?query=QUERY_SENTINEL", nil))
			expected := 500
			if committed {
				expected = 202
			}
			rows := transportTestRows(t, writer.text())
			if response.Code != expected || len(rows) != 1 || rows[0]["status"] != float64(expected) || !strings.Contains(writer.text(), "[ERROR]") {
				t.Fatal("new log must preserve original recovery/committed status")
			}
			if strings.Contains(writer.text(), "PANIC_VALUE_SENTINEL") || strings.Contains(writer.text(), "QUERY_SENTINEL") {
				t.Fatal("completion must not copy panic values or query")
			}
		})
	}
}

func TestTransportCompletionLoggingFailuresDoNotChangeBusiness(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "writer-error"
		if panics {
			name = "writer-panic"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			transportTestCapture(t, transportFailWriter{panics: panics})
			router := gin.New()
			router.Use(RequestCorrelation)
			router.GET("/success", func(c *gin.Context) { c.String(200, "business preserved") })
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("GET", "/success", nil))
			if response.Code != 200 || response.Body.String() != "business preserved" || response.Header().Get("X-Request-ID") == "" {
				t.Fatal("auxiliary logger failure must not change successful business")
			}
		})
	}
}

func TestTransportCompletionLoggingPanicDoesNotReplaceBusinessPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	transportTestCapture(t, transportFailWriter{panics: true})
	original := errors.New("original business panic identity")
	router := gin.New()
	router.Use(RequestCorrelation)
	router.GET("/panic", func(c *gin.Context) { panic(original) })
	var observed any
	func() {
		defer func() { observed = recover() }()
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/panic", nil))
	}()
	if observed != original {
		t.Fatal("best-effort logging must not swallow or replace the original business panic")
	}
}
