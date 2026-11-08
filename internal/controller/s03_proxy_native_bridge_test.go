package controller

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/middleware"
)

const s03FixtureCRC = "S03+中/value%"

type s03WireTrace struct {
	ID                 int64  `json:"id"`
	Mode               string `json:"mode"`
	Method             string `json:"method"`
	Path               string `json:"path"`
	RawQuery           string `json:"rawQuery"`
	ContentType        string `json:"contentType"`
	Body               string `json:"body"`
	Host               string `json:"host"`
	Scheme             string `json:"scheme"`
	Authorization      string `json:"authorization"`
	ProxyAuthorization string `json:"proxyAuthorization"`
	Cookie             string `json:"cookie"`
	DeadlineMS         int64  `json:"deadlineMs"`
}

type s03BridgeEvent struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Actor  string `json:"actor"`
	Status int    `json:"status"`
}

type s03ProxyNativeBridge struct {
	server           *httptest.Server
	upstream         *httptest.Server
	transport        *http.Transport
	proxy            *sqpProxy
	nonce            string
	stop             chan struct{}
	stopOnce         sync.Once
	controlMu        sync.Mutex
	mu               sync.Mutex
	mode             string
	held             int
	hold             chan struct{}
	holdOnce         *sync.Once
	nextID           int64
	traces           []s03WireTrace
	upstreamRequests []s03WireTrace
	audits           []sqpAudit
	events           []s03BridgeEvent
	violations       []string
	recoveries       int
}

// TestS03ProxyNativeBridge exercises the production proxy over real loopback HTTP.
// Only fixture identity and the fixed-target transport destination are replaced.
// It does not read manager state, any DB, SQL_DSN, credentials, or business APIs.
func TestS03ProxyNativeBridge(t *testing.T) {
	t.Run("backend-selfchecks", s03ProxyNativeSelfchecks)
	if t.Failed() {
		return
	}
	mode := os.Getenv("TIKU_S03_BRIDGE_MODE")
	if mode == "" {
		return
	}
	if mode != "ui" {
		t.Fatalf("unsupported TIKU_S03_BRIDGE_MODE %q", mode)
	}
	readyPath := os.Getenv("TIKU_S03_BRIDGE_READY_FILE")
	if readyPath == "" || !filepath.IsAbs(readyPath) {
		t.Fatal("UI mode requires an absolute TIKU_S03_BRIDGE_READY_FILE")
	}
	b := newS03ProxyNativeBridge(t)
	ready, err := json.Marshal(map[string]string{"url": b.server.URL, "nonce": b.nonce})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyPath, append(ready, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(readyPath); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	t.Logf("UI bridge ready at loopback URL %s; fixture identity only", b.server.URL)
	timer := time.NewTimer(180 * time.Second)
	defer timer.Stop()
	select {
	case <-b.stop:
		t.Log("UI bridge shutdown acknowledged")
	case <-timer.C:
		t.Fatal("UI bridge exceeded 180s without fixture shutdown")
	}
}

func s03FixturePaper() []map[string]any {
	return []map[string]any{
		{"content": "S03_ZERO_OPTION", "typeAlias": "单选题", "options": []map[string]any{{"name": "A", "value": 0}, {"name": "B", "value": "一"}}, "answer": []any{"A"}},
		{"content": "S03_ZERO_REPEATED", "typeAlias": "填空题", "answer": []any{0, 0}},
		{"content": "S03_TEXT_REPEATED", "typeAlias": "简答题", "answer": []any{"重复", "重复"}},
		{"content": "S03_READING_REPEATED", "typeAlias": "阅读理解", "answer": []any{"同文", "同文"}},
		{"content": "S03_EMPTY_ANSWER", "typeAlias": "简答题", "answer": []any{}},
	}
}

func newS03ProxyNativeBridge(t *testing.T) *s03ProxyNativeBridge {
	t.Helper()
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	b := &s03ProxyNativeBridge{
		nonce: hex.EncodeToString(nonce), stop: make(chan struct{}), mode: "success",
		traces: []s03WireTrace{}, upstreamRequests: []s03WireTrace{}, audits: []sqpAudit{},
		events: []s03BridgeEvent{}, violations: []string{},
		transport: &http.Transport{Proxy: nil},
	}
	b.upstream = httptest.NewServer(http.HandlerFunc(b.serveLocalUpstream))
	target, err := url.Parse(b.upstream.URL)
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" {
		t.Fatal("fixture upstream must be loopback HTTP")
	}
	b.proxy = newSQPProxy(func() configs.SQPProxyConfig {
		mode := b.currentMode()
		enabled := mode != "closed"
		config := configs.SQPProxyConfig{Enabled: &enabled}
		if mode == "timeout" {
			config.TimeoutSeconds = 1
		}
		return config
	}, sqpTestTransport(func(req *http.Request) (*http.Response, error) {
		// Observe the production Director before changing the test transport URL.
		if req.URL.Scheme != "http" || req.URL.Host != "mooc2-ans.chaoxing.com" || req.Host != req.URL.Host {
			b.violate("fixed upstream target changed")
			return nil, errors.New("fixture rejected non-fixed proxy target")
		}
		if req.Header.Get("Authorization") != "" || req.Header.Get("Proxy-Authorization") != "" || req.Header.Get("Cookie") != "UID=1" {
			b.violate("credential projection changed")
			return nil, errors.New("fixture rejected credential projection")
		}
		dl, ok := req.Context().Deadline()
		if !ok || time.Until(dl) <= 0 || time.Until(dl) > 60*time.Second {
			b.violate("bounded request deadline missing")
			return nil, errors.New("fixture rejected missing deadline")
		}
		var body []byte
		if req.Body != nil {
			var err error
			body, err = io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
		}
		trace := s03WireTrace{
			Mode: b.currentMode(), Method: req.Method, Path: req.URL.Path, RawQuery: req.URL.RawQuery,
			ContentType: req.Header.Get("Content-Type"), Body: string(body), Host: req.Host, Scheme: req.URL.Scheme,
			Authorization: req.Header.Get("Authorization"), ProxyAuthorization: req.Header.Get("Proxy-Authorization"),
			Cookie: req.Header.Get("Cookie"), DeadlineMS: time.Until(dl).Milliseconds(),
		}
		b.mu.Lock()
		b.nextID++
		trace.ID = b.nextID
		b.traces = append(b.traces, trace)
		b.mu.Unlock()
		copy := req.Clone(req.Context())
		u := *req.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		copy.URL, copy.Host = &u, target.Host
		copy.Header = req.Header.Clone()
		// Fixture capability/identity must not leave the bridge even for local IO.
		copy.Header.Del("X-S03-Nonce")
		copy.Header.Del("X-S03-Actor")
		copy.Header.Set("X-S03-Fixture-Mode", trace.Mode)
		copy.Body = io.NopCloser(bytes.NewReader(body))
		copy.ContentLength = int64(len(body))
		// RoundTrip does not follow Location redirects, use proxies, or resolve a
		// URL from request data; the sole destination is this local httptest port.
		return b.transport.RoundTrip(copy)
	}), func(event sqpAudit) {
		b.mu.Lock()
		b.audits = append(b.audits, event)
		mode := b.mode
		b.mu.Unlock()
		if mode == "auditpanic" || mode == "audit-upstream-error" {
			panic("S03_SYNTHETIC_AUDIT_FAILURE")
		}
	})
	router := gin.New()
	// Real Gin recovery is intentionally outside p.serve. A panic after response
	// commit cannot retroactively replace a successful status/body.
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		b.mu.Lock()
		b.recoveries++
		b.mu.Unlock()
		c.AbortWithStatus(http.StatusInternalServerError)
	}))
	router.Use(func(c *gin.Context) {
		actor := c.GetHeader("X-S03-Actor")
		if actor != "admin" && actor != "user" {
			actor = "anonymous"
		}
		c.Set("s03FixtureActor", actor)
		c.Next()
		b.mu.Lock()
		b.events = append(b.events, s03BridgeEvent{c.Request.Method, c.Request.URL.Path, actor, c.Writer.Status()})
		b.mu.Unlock()
	})
	router.Use(func(c *gin.Context) {
		if c.GetHeader("X-S03-Nonce") != b.nonce {
			c.AbortWithStatus(http.StatusForbidden)
		}
	})
	auth := func(c *gin.Context) {
		actor, _ := c.Get("s03FixtureActor")
		if actor == "anonymous" || b.currentMode() == "unauth" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "fixture authentication required"})
		}
	}
	router.Any("/sqp/*path", auth, middleware.BodyLimit(16<<20), b.proxy.serve)
	fixture := router.Group("/__fixture__")
	setMode := func(c *gin.Context) {
		var input struct {
			Mode string `json:"mode"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		if err := b.setMode(input.Mode); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "unknown fixture mode"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"mode": b.currentMode()})
	}
	fixture.POST("/mode", setMode)
	fixture.POST("/control", setMode)
	fixture.GET("/snapshot", func(c *gin.Context) { c.JSON(http.StatusOK, b.snapshot()) })
	fixture.POST("/release", func(c *gin.Context) { b.releaseDeferred(); c.JSON(http.StatusOK, gin.H{"released": true}) })
	fixture.POST("/shutdown", func(c *gin.Context) { b.shutdown(); c.JSON(http.StatusOK, gin.H{"shutdown": true}) })
	router.NoRoute(func(c *gin.Context) {
		if c.Request.Method == http.MethodGet {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusTeapot, gin.H{"message": "no question/provider/database writes in fixture bridge"})
	})
	b.server = httptest.NewServer(router)
	t.Cleanup(func() { b.shutdown(); b.server.Close(); b.upstream.Close(); b.transport.CloseIdleConnections() })
	return b
}

func (b *s03ProxyNativeBridge) currentMode() string { b.mu.Lock(); defer b.mu.Unlock(); return b.mode }
func (b *s03ProxyNativeBridge) violate(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.violations = append(b.violations, reason)
}
func (b *s03ProxyNativeBridge) setMode(mode string) error {
	switch mode {
	case "rate-limit":
		mode = "busy"
	case "upstream-error":
		mode = "upstream5xx"
	case "audit-error":
		mode = "auditpanic"
	}
	switch mode {
	case "success", "closed", "unauth", "timeout", "busy", "upstream5xx", "auditpanic", "audit-upstream-error", "deferred":
	default:
		return errors.New("unknown fixture mode")
	}
	b.controlMu.Lock()
	defer b.controlMu.Unlock()
	// These slots are fixture-owned, not claims of a real user peak/load test.
	for b.held > 0 {
		b.proxy.release()
		b.held--
	}
	if mode == "busy" {
		for i := 0; i < sqpDefaultMaxConcurrent; i++ {
			if !b.proxy.acquire(sqpDefaultMaxConcurrent) {
				for b.held > 0 {
					b.proxy.release()
					b.held--
				}
				return errors.New("cannot reserve fixture slots while another request is active")
			}
			b.held++
		}
	}
	b.mu.Lock()
	b.mode = mode
	if mode == "deferred" && b.hold == nil {
		b.hold = make(chan struct{})
		b.holdOnce = &sync.Once{}
	}
	b.mu.Unlock()
	return nil
}
func (b *s03ProxyNativeBridge) releaseDeferred() {
	b.mu.Lock()
	hold, once := b.hold, b.holdOnce
	b.hold = nil
	b.holdOnce = nil
	b.mu.Unlock()
	if hold != nil {
		once.Do(func() { close(hold) })
	}
}
func (b *s03ProxyNativeBridge) shutdown() {
	b.releaseDeferred()
	b.stopOnce.Do(func() { close(b.stop) })
}
func (b *s03ProxyNativeBridge) snapshot() map[string]any {
	b.mu.Lock()
	result := map[string]any{
		"mode": b.mode, "traces": append([]s03WireTrace{}, b.traces...),
		"upstreamRequests": append([]s03WireTrace{}, b.upstreamRequests...),
		"audits":           append([]sqpAudit{}, b.audits...), "events": append([]s03BridgeEvent{}, b.events...),
		"violations": append([]string{}, b.violations...), "recoveries": b.recoveries,
	}
	b.mu.Unlock()
	b.proxy.mu.Lock()
	result["active"] = b.proxy.active
	b.proxy.mu.Unlock()
	return result
}
func (b *s03ProxyNativeBridge) serveLocalUpstream(w http.ResponseWriter, r *http.Request) {
	mode := r.Header.Get("X-S03-Fixture-Mode")
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	b.upstreamRequests = append(b.upstreamRequests, s03WireTrace{Mode: mode, Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, ContentType: r.Header.Get("Content-Type"), Body: string(raw), Authorization: r.Header.Get("Authorization"), ProxyAuthorization: r.Header.Get("Proxy-Authorization"), Cookie: r.Header.Get("Cookie")})
	hold := b.hold
	b.mu.Unlock()
	if mode == "timeout" {
		<-r.Context().Done()
		return
	}
	if mode == "deferred" && hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if mode == "upstream5xx" || mode == "audit-upstream-error" {
		w.WriteHeader(http.StatusBadGateway)
		// Deliberate fake private upstream text must never become page feedback.
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "S03_PRIVATE_UPSTREAM_TOKEN", "paper": s03FixturePaper()})
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/sqp/api/upload":
		copy := r.Clone(r.Context())
		copy.Body = io.NopCloser(bytes.NewReader(raw))
		if err := copy.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer copy.MultipartForm.RemoveAll()
		file, header, err := copy.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || !strings.HasSuffix(strings.ToLower(header.Filename), ".docx") || !bytes.Equal(content, []byte("S03_SYNTHETIC_DOCX_BYTES")) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"crc": s03FixtureCRC})
	case r.Method == http.MethodGet && r.URL.Path == "/sqp/api/parse":
		if r.URL.Query().Get("file") != s03FixtureCRC {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"paper": s03FixturePaper()})
	case r.Method == http.MethodPost && r.URL.Path == "/sqp/api/parse":
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		values, err := url.ParseQuery(string(raw))
		if err != nil || values.Get("html") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"paper": s03FixturePaper()})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func s03ProxyNativeSelfchecks(t *testing.T) {
	b := newS03ProxyNativeBridge(t)
	client := b.server.Client()
	client.Timeout = 5 * time.Second
	request := func(t *testing.T, method, path, ctype string, body []byte, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, b.server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-S03-Nonce", b.nonce)
		req.Header.Set("X-S03-Actor", "admin")
		req.Header.Set("Authorization", "S03_SYNTHETIC_AUTH")
		req.Header.Set("Proxy-Authorization", "S03_SYNTHETIC_PROXY")
		req.Header.Set("Cookie", "tiku_token=S03_SYNTHETIC_COOKIE")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		output, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read=%v close=%v", readErr, closeErr)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s want=%d got=%d body=%s", method, path, want, res.StatusCode, output)
		}
		return output
	}
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, err := writer.CreateFormFile("file", "s03-fixture.docx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte("S03_SYNTHETIC_DOCX_BYTES")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw := request(t, "POST", "/sqp/api/upload", writer.FormDataContentType(), upload.Bytes(), http.StatusOK)
	var receipt struct {
		CRC string `json:"crc"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.CRC != s03FixtureCRC {
		t.Fatalf("upload CRC mismatch %s", raw)
	}
	path := "/sqp/api/parse?" + url.Values{"file": []string{s03FixtureCRC}}.Encode()
	raw = request(t, "GET", path, "", nil, http.StatusOK)
	var parsed struct {
		Paper []map[string]any `json:"paper"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Paper) != 5 {
		t.Fatalf("actual parse paper mismatch: %s", raw)
	}
	form := url.Values{"html": []string{"<p>S03_SYNTHETIC_EDIT & 中文</p>"}}.Encode()
	request(t, "POST", "/sqp/api/parse", "application/x-www-form-urlencoded", []byte(form), http.StatusOK)
	b.mu.Lock()
	traces := append([]s03WireTrace{}, b.traces...)
	upstreamRequests := append([]s03WireTrace{}, b.upstreamRequests...)
	b.mu.Unlock()
	if len(traces) != 3 || len(upstreamRequests) != 3 {
		t.Fatal("three actual HTTP wires not observed")
	}
	for i := range traces {
		wire, local := traces[i], upstreamRequests[i]
		if wire.Method != local.Method || wire.Path != local.Path || wire.RawQuery != local.RawQuery || wire.Body != local.Body || wire.ContentType != local.ContentType || wire.Authorization != local.Authorization || wire.ProxyAuthorization != local.ProxyAuthorization || wire.Cookie != local.Cookie {
			t.Fatal("local upstream changed actual forwarded wire")
		}
	}
	for _, tc := range []struct {
		mode   string
		status int
	}{
		{"closed", 503}, {"unauth", 401}, {"timeout", 504}, {"busy", 429}, {"upstream5xx", 502}, {"audit-upstream-error", 502}, {"auditpanic", 200},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			if err := b.setMode(tc.mode); err != nil {
				t.Fatal(err)
			}
			request(t, "POST", "/sqp/api/parse", "application/x-www-form-urlencoded", []byte(form), tc.status)
			if err := b.setMode("success"); err != nil {
				t.Fatal(err)
			}
			request(t, "GET", path, "", nil, http.StatusOK)
			b.proxy.mu.Lock()
			active := b.proxy.active
			b.proxy.mu.Unlock()
			if active != 0 {
				t.Fatalf("proxy capacity not released: %d", active)
			}
		})
	}
	// An unsupported write is a trap, not a question/provider endpoint.
	request(t, "POST", "/adapter-service/questions", "application/json", []byte("{}"), http.StatusTeapot)
	b.mu.Lock()
	violations := append([]string{}, b.violations...)
	recoveries := b.recoveries
	b.mu.Unlock()
	if len(violations) != 0 {
		t.Fatalf("proxy invariants: %v", violations)
	}
	if recoveries != 2 {
		t.Fatalf("audit panic recovery mismatch: %d", recoveries)
	}
	t.Log("real HTTP upload/GET parse/POST parse and seven fault/recovery modes passed; no DB/provider calls")
}
