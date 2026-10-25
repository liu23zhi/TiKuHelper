package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

var transportFallbackSequence atomic.Uint64

// RequestCorrelation provides a transport-only diagnostic ID. It does not use
// incoming IDs or replace search/audit/billing request IDs.
func RequestCorrelation(c *gin.Context) {
	requestCorrelation(c, rand.Reader, time.Now(), &transportFallbackSequence)
}

func generateTransportRequestID(reader io.Reader, now time.Time, sequence *atomic.Uint64) string {
	var random [16]byte
	if _, err := io.ReadFull(reader, random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	// This explicit fallback is not a security token or a globally unique ID.
	// Auxiliary entropy failure must not change the business response.
	return "fallback-" + strconv.FormatInt(now.UnixNano(), 10) + "-" + strconv.FormatUint(sequence.Add(1), 10)
}

func requestCorrelation(c *gin.Context, reader io.Reader, now time.Time, sequence *atomic.Uint64) {
	started := time.Now()
	id := generateTransportRequestID(reader, now, sequence)
	c.Header("X-Request-ID", id)
	c.Set("transport_request_id", id)
	panicking := true
	defer func() {
		status := c.Writer.Status()
		// The existing outer Gin recovery writes 500 only before headers have
		// been committed. Preserve an already committed response on panic.
		if panicking && !c.Writer.Written() {
			status = 500
		}
		route := c.FullPath()
		if route == "" {
			route = "__unmatched__"
		}
		duration := time.Since(started).Milliseconds()
		if duration < 0 {
			duration = 0
		}
		writeTransportCompletion(transportCompletion{
			Event:      "transport_complete",
			RequestID:  id,
			Route:      route,
			Method:     transportLogMethod(c.Request.Method),
			Status:     status,
			DurationMS: duration,
		}, panicking)
	}()
	c.Next()
	panicking = false
}

type transportCompletion struct {
	Event      string `json:"event"`
	RequestID  string `json:"request_id"`
	Route      string `json:"route"`
	Method     string `json:"method"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

func transportLogMethod(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "CONNECT", "OPTIONS", "TRACE":
		return method
	default:
		return "OTHER"
	}
}

func writeTransportCompletion(record transportCompletion, panicking bool) {
	// Best-effort auxiliary logging must neither fail a successful handler
	// nor replace an original business panic. Existing Gin recovery is kept.
	defer func() { _ = recover() }()
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	line := "transport_complete " + string(encoded)
	switch {
	case panicking || record.Status >= 500:
		logger.SysError(line)
	case record.Status >= 400:
		logger.SysWarn(line)
	default:
		logger.SysLog(line)
	}
}
