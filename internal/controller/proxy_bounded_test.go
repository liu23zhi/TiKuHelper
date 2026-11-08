package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/spf13/viper"
)

type sqpTestTransport func(*http.Request) (*http.Response, error)

func (f sqpTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func sqpTestResponse(r *http.Request) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"fixture":"SYNTHETIC_ONLY"}`)), Request: r}
}
func sqpTestConfig(c configs.SQPProxyConfig) func() configs.SQPProxyConfig {
	return func() configs.SQPProxyConfig { return c }
}
func sqpTestRouter(p *sqpProxy) *gin.Engine {
	r := gin.New()
	r.Any("/sqp/*path", middleware.BodyLimit(16<<20), p.serve)
	return r
}
func sqpTestServe(r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func TestSQPBoundedConfiguration(t *testing.T) {
	for _, c := range []configs.SQPProxyConfig{{}, {TimeoutSeconds: -1, MaxConcurrent: -1}, {TimeoutSeconds: int(^uint(0) >> 1)}} {
		o := resolveSQPOptions(c)
		if !o.enabled || o.timeout != 60*time.Second || o.maxConcurrent != 4 {
			t.Fatalf("invalid/missing config removed defaults: %+v", o)
		}
	}
	off := false
	o := resolveSQPOptions(configs.SQPProxyConfig{Enabled: &off, TimeoutSeconds: 3, MaxConcurrent: 2})
	if o.enabled || o.timeout != 3*time.Second || o.maxConcurrent != 2 {
		t.Fatalf("explicit config not applied: %+v", o)
	}
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader("sqpProxy:\n  enabled: false\n  timeoutSeconds: 3\n  maxConcurrent: 2\n")); err != nil {
		t.Fatal(err)
	}
	var cfg configs.Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatal(err)
	}
	o = resolveSQPOptions(cfg.SQPProxy)
	if o.enabled || o.timeout != 3*time.Second || o.maxConcurrent != 2 {
		t.Fatalf("actual Viper config binding: %+v", o)
	}
}

func TestSQPBoundedThreeWires(t *testing.T) {
	for _, tc := range []struct{ method, path, ctype, body string }{{"GET", "/sqp/api/parse?file=fixture%2B%E4%B8%AD%2Fvalue%25", "", ""}, {"POST", "/sqp/api/parse", "application/x-www-form-urlencoded", "html=%3Cp%3ESYNTHETIC%3C%2Fp%3E"}, {"POST", "/sqp/api/upload", "multipart/form-data; boundary=SQP_FIXTURE", "--SQP_FIXTURE\r\nSYNTHETIC_BYTES\r\n--SQP_FIXTURE--\r\n"}} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			calls := 0
			p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{}), sqpTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				dl, ok := req.Context().Deadline()
				if !ok || time.Until(dl) > 60*time.Second || time.Until(dl) < 59*time.Second {
					t.Error("missing/default deadline drift")
				}
				if req.Host != "mooc2-ans.chaoxing.com" || req.URL.Host != req.Host || req.URL.Scheme != "http" {
					t.Error("fixed target drift")
				}
				if req.Method != tc.method || req.Header.Get("Content-Type") != tc.ctype {
					t.Error("method/content-type drift")
				}
				if req.Header.Get("Authorization") != "" || req.Header.Get("Proxy-Authorization") != "" || req.Header.Get("Cookie") != "UID=1" {
					t.Error("credential strip drift")
				}
				var b []byte
				var err error
				if req.Body != nil {
					b, err = io.ReadAll(req.Body)
				}
				if err != nil || string(b) != tc.body {
					t.Error("body drift")
				}
				return sqpTestResponse(req), nil
			}), nil)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.ctype)
			req.Header.Set("Authorization", "SYNTHETIC_AUTH")
			req.Header.Set("Proxy-Authorization", "SYNTHETIC_PROXY")
			req.Header.Set("Cookie", "tiku_token=SYNTHETIC_COOKIE")
			before := req.Header.Clone()
			query := req.URL.RawQuery
			w := sqpTestServe(sqpTestRouter(p), req)
			if w.Code != 200 || calls != 1 || !reflect.DeepEqual(before, req.Header) || req.URL.RawQuery != query || w.Body.String() != `{"fixture":"SYNTHETIC_ONLY"}` {
				t.Fatal("wire/response/incoming headers changed")
			}
		})
	}
}

func TestSQPBoundedRejectsAndDisabled(t *testing.T) {
	var calls atomic.Int32
	p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{}), sqpTestTransport(func(req *http.Request) (*http.Response, error) { calls.Add(1); return sqpTestResponse(req), nil }), nil)
	r := sqpTestRouter(p)
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"DELETE", "/sqp/api/parse", 405}, {"GET", "/sqp/api/upload", 405}, {"POST", "/sqp/api/other", 404}, {"GET", "/sqp/api/parse/", 404}, {"GET", "/sqp/api/%70arse", 404}, {"POST", "/sqp/api/upload%2f..%2fparse", 404}} {
		w := sqpTestServe(r, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s expected%d actual%d", tc.method, tc.path, tc.status, w.Code)
		}
	}
	off := false
	p.config = sqpTestConfig(configs.SQPProxyConfig{Enabled: &off})
	w := sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil))
	if w.Code != 503 || calls.Load() != 0 {
		t.Fatalf("disabled/rejected dispatched upstream status=%d calls=%d", w.Code, calls.Load())
	}
}

func TestSQPBoundedConcurrencyAndRelease(t *testing.T) {
	for _, limit := range []int{4, 2} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			started := make(chan struct{}, limit)
			release := make(chan struct{})
			var block atomic.Bool
			block.Store(true)
			p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{MaxConcurrent: limit}), sqpTestTransport(func(req *http.Request) (*http.Response, error) {
				if block.Load() {
					started <- struct{}{}
					select {
					case <-release:
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				}
				return sqpTestResponse(req), nil
			}), nil)
			r := sqpTestRouter(p)
			done := make(chan int, limit)
			for i := 0; i < limit; i++ {
				go func() { done <- sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code }()
			}
			for i := 0; i < limit; i++ {
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("admitted request never reached mock transport")
				}
			}
			w := sqpTestServe(r, httptest.NewRequest("POST", "/sqp/api/upload", nil))
			if w.Code != 429 {
				t.Errorf("over-capacity expected429 actual%d", w.Code)
			}
			block.Store(false)
			close(release)
			for i := 0; i < limit; i++ {
				select {
				case status := <-done:
					if status != 200 {
						t.Error("admitted request failed")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("admitted request did not finish")
				}
			}
			if w = sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)); w.Code != 200 {
				t.Fatal("slots not released after success")
			}
		})
	}
}

func TestSQPBoundedTimeoutCancellationAndRecovery(t *testing.T) {
	t.Run("configured-timeout", func(t *testing.T) {
		var wait atomic.Bool
		wait.Store(true)
		p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{TimeoutSeconds: 1, MaxConcurrent: 1}), sqpTestTransport(func(req *http.Request) (*http.Response, error) {
			if wait.Load() {
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			return sqpTestResponse(req), nil
		}), nil)
		r := sqpTestRouter(p)
		start := time.Now()
		w := sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil))
		if w.Code != 504 || time.Since(start) < 800*time.Millisecond || time.Since(start) > 4*time.Second {
			t.Fatalf("deadline not enforced status=%d elapsed=%s", w.Code, time.Since(start))
		}
		wait.Store(false)
		if sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code != 200 {
			t.Fatal("timeout leaked capacity")
		}
	})
	t.Run("inbound-cancellation", func(t *testing.T) {
		started := make(chan struct{})
		var wait atomic.Bool
		wait.Store(true)
		p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{MaxConcurrent: 1}), sqpTestTransport(func(req *http.Request) (*http.Response, error) {
			if wait.Load() {
				close(started)
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			return sqpTestResponse(req), nil
		}), nil)
		r := sqpTestRouter(p)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan int, 1)
		go func() {
			done <- sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil).WithContext(ctx)).Code
		}()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("mock did not start")
		}
		cancel()
		select {
		case status := <-done:
			if status != 408 {
				t.Errorf("cancel status=%d", status)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("inbound cancellation ignored")
		}
		wait.Store(false)
		if sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code != 200 {
			t.Fatal("cancellation leaked capacity")
		}
	})
}

func TestSQPBoundedBodyLimitAuditAndErrorRecovery(t *testing.T) {
	var audits []sqpAudit
	var mu sync.Mutex
	var fail atomic.Bool
	p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{MaxConcurrent: 1}), sqpTestTransport(func(req *http.Request) (*http.Response, error) {
		if fail.Load() {
			return nil, errors.New("PRIVATE_UPSTREAM_ERROR /?html=PRIVATE_QUERY authorization=PRIVATE_AUTH")
		}
		if req.Body != nil {
			if _, err := io.Copy(io.Discard, req.Body); err != nil {
				return nil, err
			}
		}
		return sqpTestResponse(req), nil
	}), func(event sqpAudit) { mu.Lock(); defer mu.Unlock(); audits = append(audits, event) })
	r := sqpTestRouter(p)
	for _, tc := range []struct{ bytes, status int }{{16 << 20, 200}, {(16 << 20) + 1, 413}, {1, 200}} {
		w := sqpTestServe(r, httptest.NewRequest("POST", "/sqp/api/upload", bytes.NewReader(bytes.Repeat([]byte("s"), tc.bytes))))
		if w.Code != tc.status {
			t.Errorf("body bytes=%d status=%d expected=%d", tc.bytes, w.Code, tc.status)
		}
	}
	fail.Store(true)
	req := httptest.NewRequest("POST", "/sqp/api/parse?html=PRIVATE_QUERY", strings.NewReader("PRIVATE_BODY"))
	req.Header.Set("Authorization", "PRIVATE_AUTH")
	w := sqpTestServe(r, req)
	if w.Code != 502 {
		t.Fatal("transport error not bounded")
	}
	fail.Store(false)
	if sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code != 200 {
		t.Fatal("error leaked capacity")
	}
	b, _ := json.Marshal(audits)
	for _, secret := range []string{"PRIVATE_QUERY", "PRIVATE_BODY", "PRIVATE_AUTH", "PRIVATE_UPSTREAM_ERROR"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("audit leaked %s", secret)
		}
	}
	if len(audits) != 5 || audits[1].Outcome != "body_too_large" || audits[3].Outcome != "upstream_error" || audits[3].Status != 502 {
		t.Fatalf("missing/error audit: %+v", audits)
	}
}

func TestSQPBoundedLiveConfigKeepsActiveOwnership(t *testing.T) {
	var limit atomic.Int32
	limit.Store(4)
	var blocking atomic.Bool
	blocking.Store(true)
	started := make(chan struct{}, 5)
	release := make(chan struct{})
	done := make(chan int, 5)
	p := newSQPProxy(func() configs.SQPProxyConfig { return configs.SQPProxyConfig{MaxConcurrent: int(limit.Load())} }, sqpTestTransport(func(req *http.Request) (*http.Response, error) {
		if blocking.Load() {
			started <- struct{}{}
			select {
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		return sqpTestResponse(req), nil
	}), nil)
	r := sqpTestRouter(p)
	launch := func() {
		go func() { done <- sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code }()
	}
	awaitStart := func() {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("upstream request did not start")
		}
	}
	for i := 0; i < 4; i++ {
		launch()
		awaitStart()
	}
	limit.Store(2)
	if sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code != 429 {
		t.Error("decreasing limit forgot existing four owners")
	}
	limit.Store(5)
	launch()
	awaitStart()
	limit.Store(1)
	if sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code != 429 {
		t.Error("reconfigured gate admitted above active count")
	}
	blocking.Store(false)
	close(release)
	for i := 0; i < 5; i++ {
		select {
		case status := <-done:
			if status != 200 {
				t.Error("configuration cancelled already-admitted request")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("existing request did not finish")
		}
	}
	if sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code != 200 {
		t.Fatal("shared ownership did not recover after limit change")
	}
}

type sqpTestErrorBody struct{}

func (sqpTestErrorBody) Read([]byte) (int, error) {
	return 0, errors.New("PRIVATE_RESPONSE_ERROR html=PRIVATE_QUERY")
}
func (sqpTestErrorBody) Close() error { return nil }
func TestSQPBoundedPartialResponseAudit(t *testing.T) {
	var event sqpAudit
	seen := false
	p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{MaxConcurrent: 1}), sqpTestTransport(func(req *http.Request) (*http.Response, error) {
		response := sqpTestResponse(req)
		response.Body = sqpTestErrorBody{}
		return response, nil
	}), func(e sqpAudit) { event = e; seen = true })
	w := sqpTestServe(sqpTestRouter(p), httptest.NewRequest("GET", "/sqp/api/parse?html=PRIVATE_QUERY", nil))
	if !seen || event.Outcome != "upstream_error" || event.Status != w.Code {
		t.Fatalf("PARTIAL_RESPONSE_AUDIT_MISSING seen=%t outcome=%s audit_status=%d wire_status=%d", seen, event.Outcome, event.Status, w.Code)
	}
	// A response body copy may already have sent 200; do not falsely claim a later 502.
	if w.Code != 200 {
		t.Fatalf("unexpected changed committed response status=%d", w.Code)
	}
	b, _ := json.Marshal(event)
	if strings.Contains(string(b), "PRIVATE_") {
		t.Fatal("partial body error leaked into audit")
	}
	p.transport = sqpTestTransport(func(req *http.Request) (*http.Response, error) { return sqpTestResponse(req), nil })
	if sqpTestServe(sqpTestRouter(p), httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code != 200 {
		t.Fatal("body read error leaked slot")
	}
}

type sqpTestContextBody struct {
	ctx     context.Context
	started chan struct{}
}

func (b sqpTestContextBody) Read([]byte) (int, error) {
	close(b.started)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (sqpTestContextBody) Close() error { return nil }
func TestSQPBoundedServerAbortAudit(t *testing.T) {
	for _, which := range []string{"timeout", "cancelled"} {
		t.Run(which, func(t *testing.T) {
			var event sqpAudit
			seen := false
			started := make(chan struct{})
			p := newSQPProxy(sqpTestConfig(configs.SQPProxyConfig{TimeoutSeconds: 1, MaxConcurrent: 1}), sqpTestTransport(func(req *http.Request) (*http.Response, error) {
				response := sqpTestResponse(req)
				response.Body = sqpTestContextBody{ctx: req.Context(), started: started}
				return response, nil
			}), func(e sqpAudit) { event = e; seen = true })
			r := sqpTestRouter(p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if which == "cancelled" {
				go func() { <-started; cancel() }()
			}
			ctx = context.WithValue(ctx, http.ServerContextKey, &http.Server{})
			w := httptest.NewRecorder()
			var caught any
			func() {
				defer func() { caught = recover() }()
				r.ServeHTTP(w, httptest.NewRequest("GET", "/sqp/api/parse?html=PRIVATE_QUERY", nil).WithContext(ctx))
			}()
			if caught != http.ErrAbortHandler {
				t.Fatalf("actual server copy-error branch not exercised: %v", caught)
			}
			if !seen || event.Outcome != which || event.Status != 200 || w.Code != 200 {
				t.Errorf("SERVER_ABORT_AUDIT_WRONG want=%s seen=%t outcome=%s audit_status=%d wire_status=%d", which, seen, event.Outcome, event.Status, w.Code)
			}
			p.transport = sqpTestTransport(func(req *http.Request) (*http.Response, error) { return sqpTestResponse(req), nil })
			if sqpTestServe(r, httptest.NewRequest("GET", "/sqp/api/parse", nil)).Code != 200 {
				t.Error("server abort leaked capacity")
			}
		})
	}
}
