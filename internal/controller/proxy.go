package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

// The destination is deliberately not configurable: SQP is not a general proxy.
var sqpTarget = url.URL{Scheme: "http", Host: "mooc2-ans.chaoxing.com"}

const (
	sqpDefaultTimeout       = 60 * time.Second
	sqpDefaultMaxConcurrent = 4
)

type sqpOptions struct {
	enabled       bool
	timeout       time.Duration
	maxConcurrent int
}

func resolveSQPOptions(config configs.SQPProxyConfig) sqpOptions {
	options := sqpOptions{enabled: true, timeout: sqpDefaultTimeout, maxConcurrent: sqpDefaultMaxConcurrent}
	if config.Enabled != nil {
		options.enabled = *config.Enabled
	}
	// Invalid or overflowing settings must not accidentally remove a bound.
	if config.TimeoutSeconds > 0 && uint64(config.TimeoutSeconds) <= uint64((1<<63-1)/int64(time.Second)) {
		options.timeout = time.Duration(config.TimeoutSeconds) * time.Second
	}
	if config.MaxConcurrent > 0 {
		options.maxConcurrent = config.MaxConcurrent
	}
	return options
}

type sqpAudit struct {
	Event         string `json:"event"`
	Route         string `json:"route"`
	Method        string `json:"method"`
	Status        int    `json:"status"`
	Outcome       string `json:"outcome"`
	DurationMS    int64  `json:"duration_ms"`
	MaxConcurrent int    `json:"max_concurrent"`
	TimeoutMS     int64  `json:"timeout_ms"`
}

// Only enum labels, numbers and timing are logged. No URL/query, request body,
// credential, upstream error text or upstream response is an audit field.
func writeSQPAudit(event sqpAudit) {
	b, _ := json.Marshal(event)
	logger.SysLog(string(b))
}

// Observe body-copy failures without logging arbitrary upstream error strings.
type sqpAuditBody struct {
	io.ReadCloser
	onReadError func()
}

func (b sqpAuditBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.onReadError()
	}
	return n, err
}

type sqpProxy struct {
	mu        sync.Mutex
	active    int
	config    func() configs.SQPProxyConfig
	transport http.RoundTripper
	audit     func(sqpAudit)
}

func newSQPProxy(config func() configs.SQPProxyConfig, transport http.RoundTripper, audit func(sqpAudit)) *sqpProxy {
	return &sqpProxy{config: config, transport: transport, audit: audit}
}

var defaultSQPProxy = newSQPProxy(func() configs.SQPProxyConfig {
	if current := manager.GetManager(); current != nil {
		return current.GetConfig().SQPProxy
	}
	return configs.SQPProxyConfig{}
}, nil, writeSQPAudit)

// Proxy retains the existing route signature and authenticated middleware order.
func Proxy(c *gin.Context) { defaultSQPProxy.serve(c) }

func (p *sqpProxy) acquire(limit int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active >= limit {
		return false
	}
	p.active++
	return true
}
func (p *sqpProxy) release() {
	p.mu.Lock()
	p.active--
	p.mu.Unlock()
}

func (p *sqpProxy) serve(c *gin.Context) {
	started := time.Now()
	options := resolveSQPOptions(p.config())
	event := sqpAudit{Event: "sqp_proxy", Route: "other", Method: "other", Outcome: "rejected", MaxConcurrent: options.maxConcurrent, TimeoutMS: options.timeout.Milliseconds()}
	defer func() {
		event.Status = c.Writer.Status()
		event.DurationMS = time.Since(started).Milliseconds()
		if p.audit != nil {
			p.audit(event)
		}
	}()
	if !options.enabled {
		event.Outcome = "disabled"
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": "SQP_DISABLED", "message": "智能导入已关闭"})
		return
	}
	// Reject encoded aliases as well as arbitrary paths; queries remain byte-for-byte.
	if c.Request.URL.RawPath != "" && c.Request.URL.RawPath != c.Request.URL.Path {
		event.Outcome = "path_not_allowed"
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	switch c.Request.URL.Path {
	case "/sqp/api/parse":
		event.Route = "parse"
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodPost {
			c.Header("Allow", "GET, POST")
			event.Outcome = "method_not_allowed"
			c.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}
	case "/sqp/api/upload":
		event.Route = "upload"
		if c.Request.Method != http.MethodPost {
			c.Header("Allow", "POST")
			event.Outcome = "method_not_allowed"
			c.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}
	default:
		event.Outcome = "path_not_allowed"
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	event.Method = c.Request.Method
	if !p.acquire(options.maxConcurrent) {
		event.Outcome = "concurrency_limited"
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"code": "SQP_BUSY", "message": "智能导入繁忙，请稍后重试"})
		return
	}
	defer p.release()
	ctx, cancel := context.WithTimeout(c.Request.Context(), options.timeout)
	defer cancel()
	proxy := httputil.NewSingleHostReverseProxy(&sqpTarget)
	proxy.Transport = p.transport
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode >= 500 {
			event.Outcome = "upstream_error"
		}
		if response.Body != nil {
			response.Body = sqpAuditBody{ReadCloser: response.Body, onReadError: func() {
				switch ctx.Err() {
				case context.DeadlineExceeded:
					event.Outcome = "timeout"
				case context.Canceled:
					event.Outcome = "cancelled"
				default:
					event.Outcome = "upstream_error"
				}
			}}
		}
		return nil
	}
	proxy.Director = func(req *http.Request) {
		req.Host = sqpTarget.Host
		req.URL.Scheme = sqpTarget.Scheme
		req.URL.Host = sqpTarget.Host
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
		req.Header.Set("Cookie", "UID=1")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		status := http.StatusBadGateway
		event.Outcome = "upstream_error"
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			status = http.StatusRequestEntityTooLarge
			event.Outcome = "body_too_large"
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
			event.Outcome = "timeout"
		case errors.Is(ctx.Err(), context.Canceled):
			status = http.StatusRequestTimeout
			event.Outcome = "cancelled"
		}
		// ReverseProxy's default logger includes err.Error(), which may contain user URLs.
		w.WriteHeader(status)
	}
	event.Outcome = "upstream"
	proxy.ServeHTTP(c.Writer, c.Request.WithContext(ctx))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		event.Outcome = "timeout"
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		event.Outcome = "cancelled"
	}
}
