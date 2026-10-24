package uapi

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// testAPIKey 仅用于测试的假密钥，不是真实密钥
	testAPIKey    = "uapi-test-key"
	phoneInfoPath = "/api/v1/misc/phoneinfo"
	ipInfoPath    = "/api/v1/network/ipinfo"

	// phoneRaw 参考官方文档的 200 响应样例
	phoneRaw = `{"city":"杭州市","province":"浙江省","sp":"中国移动","area_code":"0571"}`
	// ipRaw 参考官方文档的 200 响应样例
	ipRaw = `{"ip":"8.8.8.8","region":"美国 加利福尼亚州","isp":"Google LLC","llc":"Google LLC","asn":15169,"latitude":37.4056,"longitude":-122.0775,"beginip":"8.8.8.0","endip":"8.8.8.255"}`
)

// ---------- 测试辅助 ----------

type recordedRequest struct {
	method string
	path   string
	query  url.Values
	header http.Header
	rawURL string
}

// requestRecorder 记录服务器收到的请求，避免在 httptest 处理器与测试协程间产生数据竞争
type requestRecorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

func (rec *requestRecorder) record(r *http.Request) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.reqs = append(rec.reqs, recordedRequest{
		method: r.Method,
		path:   r.URL.Path,
		query:  r.URL.Query(),
		header: r.Header.Clone(),
		rawURL: r.URL.String(),
	})
}

func (rec *requestRecorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.reqs)
}

func (rec *requestRecorder) last(t *testing.T) recordedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.reqs) == 0 {
		t.Fatal("服务器没有收到任何请求")
	}
	return rec.reqs[len(rec.reqs)-1]
}

// newServer 启动记录请求的 httptest 服务器
func newServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *requestRecorder) {
	t.Helper()
	rec := &requestRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// staticJSON 返回固定状态码与 body 的处理器
func staticJSON(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// newTestClient 构造指向测试服务器的客户端
func newTestClient(serverURL string) *Client {
	c := NewClient(testAPIKey)
	c.SetPhoneInfoURL(serverURL + phoneInfoPath)
	c.SetIPInfoURL(serverURL + ipInfoPath)
	c.jitter = func(delay time.Duration) time.Duration { return delay }
	return c
}

// sleepRecorder 记录退避时长，替代真实等待
type sleepRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *sleepRecorder) hook(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delays = append(s.delays, d)
	return nil
}

func (s *sleepRecorder) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}

func assertDelays(t *testing.T, got []time.Duration, want ...time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("退避序列 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 次退避 = %v，期望 %v（完整序列 %v）", i+1, got[i], want[i], got)
		}
	}
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// ---------- 默认配置 ----------

func TestDefaultURLsAreComplete(t *testing.T) {
	if DefaultPhoneInfoURL != "https://uapis.cn/api/v1/misc/phoneinfo" {
		t.Fatalf("DefaultPhoneInfoURL = %q", DefaultPhoneInfoURL)
	}
	if DefaultIPInfoURL != "https://uapis.cn/api/v1/network/ipinfo" {
		t.Fatalf("DefaultIPInfoURL = %q", DefaultIPInfoURL)
	}
	c := NewClient(testAPIKey)
	if c.phoneInfoAddress() != DefaultPhoneInfoURL || c.ipInfoAddress() != DefaultIPInfoURL {
		t.Fatalf("默认地址 = %q / %q", c.phoneInfoAddress(), c.ipInfoAddress())
	}
	if c.timeout != 5*time.Second {
		t.Fatalf("默认超时 = %v，期望 5s", c.timeout)
	}
	// 密钥不得出现在 URL 中
	for _, u := range []string{c.phoneInfoAddress(), c.ipInfoAddress()} {
		if strings.Contains(u, testAPIKey) {
			t.Fatalf("地址中出现了密钥: %s", u)
		}
	}
}

// ---------- 成功解析 ----------

func TestGetPhoneInfoSuccess(t *testing.T) {
	srv, rec := newServer(t, staticJSON(http.StatusOK, phoneRaw))

	info, err := newTestClient(srv.URL).GetPhoneInfo(context.Background(), "13800138000")
	if err != nil {
		t.Fatalf("GetPhoneInfo 出错: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("请求次数 = %d，期望 1", rec.count())
	}
	got := rec.last(t)
	if got.method != http.MethodGet {
		t.Fatalf("方法 = %s，期望 GET", got.method)
	}
	if got.path != phoneInfoPath {
		t.Fatalf("路径 = %s，期望 %s", got.path, phoneInfoPath)
	}
	if got.query.Get("phone") != "13800138000" {
		t.Fatalf("phone 参数 = %q", got.query.Get("phone"))
	}
	if auth := got.header.Get("Authorization"); auth != "Bearer "+testAPIKey {
		t.Fatalf("Authorization = %q，期望 %q", auth, "Bearer "+testAPIKey)
	}
	if strings.Contains(got.rawURL, testAPIKey) {
		t.Fatalf("密钥出现在 URL 中: %s", got.rawURL)
	}
	if info.City != "杭州市" || info.Province != "浙江省" || info.SP != "中国移动" || info.AreaCode != "0571" {
		t.Fatalf("解析结果 = %+v", info)
	}
	if info.RawJSON != phoneRaw {
		t.Fatalf("RawJSON = %q，期望 %q", info.RawJSON, phoneRaw)
	}
}

func TestGetIPInfoSuccess(t *testing.T) {
	srv, rec := newServer(t, staticJSON(http.StatusOK, ipRaw))

	info, err := newTestClient(srv.URL).GetIPInfo(context.Background(), "8.8.8.8", SourceCommercial)
	if err != nil {
		t.Fatalf("GetIPInfo 出错: %v", err)
	}
	got := rec.last(t)
	if got.method != http.MethodGet {
		t.Fatalf("方法 = %s，期望 GET", got.method)
	}
	if got.path != ipInfoPath {
		t.Fatalf("路径 = %s，期望 %s", got.path, ipInfoPath)
	}
	if got.query.Get("ip") != "8.8.8.8" {
		t.Fatalf("ip 参数 = %q", got.query.Get("ip"))
	}
	if got.query.Get("source") != SourceCommercial {
		t.Fatalf("source 参数 = %q", got.query.Get("source"))
	}
	if auth := got.header.Get("Authorization"); auth != "Bearer "+testAPIKey {
		t.Fatalf("Authorization = %q", auth)
	}
	if info.IP != "8.8.8.8" || info.Region != "美国 加利福尼亚州" || info.ISP != "Google LLC" ||
		info.LLC != "Google LLC" || info.ASN != "15169" || info.BeginIP != "8.8.8.0" || info.EndIP != "8.8.8.255" {
		t.Fatalf("解析结果 = %+v", info)
	}
	if !almostEqual(info.Latitude, 37.4056) || !almostEqual(info.Longitude, -122.0775) {
		t.Fatalf("经纬度 = %v / %v", info.Latitude, info.Longitude)
	}
	if info.RawJSON != ipRaw {
		t.Fatalf("RawJSON = %q，期望 %q", info.RawJSON, ipRaw)
	}
}

func TestParseIPInfoPreservesNumericASNForCachedResponse(t *testing.T) {
	info, err := ParseIPInfo([]byte(ipRaw))
	if err != nil {
		t.Fatalf("ParseIPInfo 出错: %v", err)
	}
	if info.ASN != "15169" || info.Region != "美国 加利福尼亚州" || info.IP != "8.8.8.8" {
		t.Fatalf("解析缓存响应丢字段: %+v", info)
	}
	if info.RawJSON != ipRaw {
		t.Fatalf("RawJSON = %q，期望原始响应 %q", info.RawJSON, ipRaw)
	}
}

func TestParsePhoneInfoPreservesRawResponse(t *testing.T) {
	info, err := ParsePhoneInfo([]byte(phoneRaw))
	if err != nil {
		t.Fatalf("ParsePhoneInfo 出错: %v", err)
	}
	if info.Province != "浙江省" || info.City != "杭州市" || info.SP != "中国移动" || info.AreaCode != "0571" {
		t.Fatalf("解析缓存响应丢字段: %+v", info)
	}
	if info.RawJSON != phoneRaw {
		t.Fatalf("RawJSON = %q，期望原始响应 %q", info.RawJSON, phoneRaw)
	}
}

func TestGetIPInfoSourceVariants(t *testing.T) {
	t.Run("空 source 不下发 query", func(t *testing.T) {
		srv, rec := newServer(t, staticJSON(http.StatusOK, ipRaw))
		if _, err := newTestClient(srv.URL).GetIPInfo(context.Background(), "1.1.1.1", ""); err != nil {
			t.Fatalf("GetIPInfo 出错: %v", err)
		}
		if rec.last(t).query.Has("source") {
			t.Fatalf("source 不应出现在 query 中: %v", rec.last(t).query)
		}
	})
	t.Run("source 去空白后合法", func(t *testing.T) {
		srv, rec := newServer(t, staticJSON(http.StatusOK, ipRaw))
		if _, err := newTestClient(srv.URL).GetIPInfo(context.Background(), "1.1.1.1", " commercial "); err != nil {
			t.Fatalf("GetIPInfo 出错: %v", err)
		}
		if got := rec.last(t).query.Get("source"); got != SourceCommercial {
			t.Fatalf("source 参数 = %q", got)
		}
	})
	t.Run("数字与字符串写法都能解析", func(t *testing.T) {
		body := `{"ip":"1.1.1.1","region":"澳大利亚","isp":"Cloudflare","llc":"APNIC","asn":"AS13335","latitude":"-33.494","longitude":"143.2104","beginip":"1.1.1.0","endip":"1.1.1.255"}`
		srv, _ := newServer(t, staticJSON(http.StatusOK, body))
		info, err := newTestClient(srv.URL).GetIPInfo(context.Background(), "1.1.1.1", "")
		if err != nil {
			t.Fatalf("GetIPInfo 出错: %v", err)
		}
		if info.ASN != "AS13335" {
			t.Fatalf("ASN = %q", info.ASN)
		}
		if !almostEqual(info.Latitude, -33.494) || !almostEqual(info.Longitude, 143.2104) {
			t.Fatalf("经纬度 = %v / %v", info.Latitude, info.Longitude)
		}
	})
}

func TestRawJSONPreservedExactly(t *testing.T) {
	// 含未公开字段、缩进与转义，必须原样保留
	raw := "{\n  \"ip\": \"9.9.9.9\",\n  \"region\": \"未知\",\n  \"extra\": {\"a\": [1, 2, 3]},\n  \"unicode\": \"\\u4e2d\\u6587\"\n}"
	srv, _ := newServer(t, staticJSON(http.StatusOK, raw))

	info, err := newTestClient(srv.URL).GetIPInfo(context.Background(), "9.9.9.9", SourceCommercial)
	if err != nil {
		t.Fatalf("GetIPInfo 出错: %v", err)
	}
	if info.RawJSON != raw {
		t.Fatalf("RawJSON = %q，期望 %q", info.RawJSON, raw)
	}
	if info.IP != "9.9.9.9" || info.Region != "未知" {
		t.Fatalf("解析结果 = %+v", info)
	}

	srvPhone, _ := newServer(t, staticJSON(http.StatusOK, phoneRaw))
	p, err := newTestClient(srvPhone.URL).GetPhoneInfo(context.Background(), "13800138000")
	if err != nil {
		t.Fatalf("GetPhoneInfo 出错: %v", err)
	}
	if p.RawJSON != phoneRaw {
		t.Fatalf("RawJSON = %q，期望 %q", p.RawJSON, phoneRaw)
	}
}

func TestSuccessResponseInvalidJSON(t *testing.T) {
	srv, _ := newServer(t, staticJSON(http.StatusOK, "<html>oops</html>"))
	_, err := newTestClient(srv.URL).GetPhoneInfo(context.Background(), "13800138000")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v，期望包装 ErrUpstream", err)
	}
}

// ---------- 4 种错误结构 ----------

func TestErrorShapes(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantCode    string
		wantMessage string
		wantErr     error
	}{
		{
			name:        "code+message+details",
			status:      http.StatusBadRequest,
			body:        `{"code":"INVALID_ARGUMENT","message":"参数不合法","details":{"phone":"格式错误"}}`,
			wantCode:    "INVALID_ARGUMENT",
			wantMessage: "参数不合法",
			wantErr:     ErrInvalidArgument,
		},
		{
			name:        "error+details",
			status:      http.StatusBadRequest,
			body:        `{"error":"INVALID_ARGUMENT","details":"缺少 ip 参数"}`,
			wantCode:    "INVALID_ARGUMENT",
			wantMessage: "缺少 ip 参数",
			wantErr:     ErrInvalidArgument,
		},
		{
			name:        "error+message+docs",
			status:      http.StatusUnauthorized,
			body:        `{"error":"UNAUTHORIZED","message":"密钥无效","docs":{"url":"https://uapis.cn/docs"}}`,
			wantCode:    "UNAUTHORIZED",
			wantMessage: "密钥无效",
			wantErr:     ErrUnauthorized,
		},
		{
			name:        "数字 code + limit",
			status:      http.StatusTooManyRequests,
			body:        `{"code":429,"message":"请求过于频繁","limit":60}`,
			wantCode:    "429",
			wantMessage: "请求过于频繁",
			wantErr:     ErrRateLimited,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "req-abc-123")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			c := newTestClient(srv.URL)
			sleeps := &sleepRecorder{}
			c.sleep = sleeps.hook

			_, err := c.GetPhoneInfo(context.Background(), "13800138000")
			if err == nil {
				t.Fatal("期望错误，实际 nil")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("errors.Is(%v) = false，err = %v", tt.wantErr, err)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("errors.As(*APIError) = false，err = %v", err)
			}
			if apiErr.HTTPStatus != tt.status {
				t.Fatalf("HTTPStatus = %d，期望 %d", apiErr.HTTPStatus, tt.status)
			}
			if apiErr.Code != tt.wantCode {
				t.Fatalf("Code = %q，期望 %q", apiErr.Code, tt.wantCode)
			}
			if apiErr.Message != tt.wantMessage {
				t.Fatalf("Message = %q，期望 %q", apiErr.Message, tt.wantMessage)
			}
			if apiErr.RequestID != "req-abc-123" {
				t.Fatalf("RequestID = %q，期望 req-abc-123", apiErr.RequestID)
			}
			if !strings.Contains(err.Error(), tt.wantCode) {
				t.Fatalf("Error() = %q，未包含错误码", err.Error())
			}
		})
	}
}

// ---------- 状态码映射与重试策略 ----------

func TestStatusMappingAndRetry(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantErr     error
		wantCount   int
		wantRetries int
	}{
		{"400", http.StatusBadRequest, `{"code":"INVALID_ARGUMENT","message":"bad"}`, ErrInvalidArgument, 1, 0},
		{"401", http.StatusUnauthorized, `{"error":"UNAUTHORIZED","message":"bad key"}`, ErrUnauthorized, 1, 0},
		{"402", http.StatusPaymentRequired, `{"code":"INSUFFICIENT_CREDITS","message":"no credits"}`, ErrInsufficientCredits, 1, 0},
		{"403", http.StatusForbidden, `{"error":"FORBIDDEN","message":"denied"}`, ErrForbidden, 1, 0},
		{"404", http.StatusNotFound, `{"code":"NOT_FOUND","message":"not found"}`, ErrNotFound, 1, 0},
		{"408", http.StatusRequestTimeout, `{"code":"TIMEOUT","message":"timeout"}`, ErrUpstream, 3, 2},
		{"425", http.StatusTooEarly, `{"code":"TOO_EARLY","message":"retry later"}`, ErrUpstream, 3, 2},
		{"429", http.StatusTooManyRequests, `{"code":"RATE_LIMITED","message":"slow down"}`, ErrRateLimited, 3, 2},
		{"429 配额耗尽", http.StatusTooManyRequests, `{"code":"VISITOR_MONTHLY_QUOTA_EXHAUSTED","message":"quota"}`, ErrInsufficientCredits, 1, 0},
		{"500", http.StatusInternalServerError, `{"code":"INTERNAL_SERVER_ERROR","message":"boom"}`, ErrUpstream, 1, 0},
		{"502", http.StatusBadGateway, ``, ErrUpstream, 1, 0},
		{"503", http.StatusServiceUnavailable, ``, ErrUpstream, 1, 0},
		{"未列举状态码 418", http.StatusTeapot, ``, ErrUpstream, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := newServer(t, staticJSON(tt.status, tt.body))
			c := newTestClient(srv.URL)
			sleeps := &sleepRecorder{}
			c.sleep = sleeps.hook

			_, err := c.GetPhoneInfo(context.Background(), "13800138000")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("errors.Is(%v) = false，err = %v", tt.wantErr, err)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("errors.As(*APIError) = false，err = %v", err)
			}
			if apiErr.HTTPStatus != tt.status {
				t.Fatalf("HTTPStatus = %d，期望 %d", apiErr.HTTPStatus, tt.status)
			}
			if rec.count() != tt.wantCount {
				t.Fatalf("请求次数 = %d，期望 %d", rec.count(), tt.wantCount)
			}
			if got := len(sleeps.recorded()); got != tt.wantRetries {
				t.Fatalf("退避次数 = %d，期望 %d（%v）", got, tt.wantRetries, sleeps.recorded())
			}
		})
	}
}

func TestErrorBodyNotJSONStillMapsStatus(t *testing.T) {
	srv, _ := newServer(t, staticJSON(http.StatusNotFound, "<html>404</html>"))
	_, err := newTestClient(srv.URL).GetPhoneInfo(context.Background(), "13800138000")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v，期望 ErrNotFound", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(*APIError) = false")
	}
	if apiErr.Code != "" || apiErr.Message != "" {
		t.Fatalf("Code/Message 应为空: %+v", apiErr)
	}
}

// ---------- 重试细节 ----------

func TestRetryOn500ThenSuccess(t *testing.T) {
	var calls atomic.Int64
	srv, rec := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, phoneRaw)
	})
	c := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	c.sleep = sleeps.hook

	resp, err := c.get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("read-only GET failed: %v", err)
	}
	if !resp.IsSuccess() {
		t.Fatalf("read-only GET status = %d", resp.StatusCode())
	}
	if rec.count() != 3 {
		t.Fatalf("请求次数 = %d，期望 3", rec.count())
	}
	assertDelays(t, sleeps.recorded(), 500*time.Millisecond, time.Second)
}

func Test500MakesAtMostThreeAttempts(t *testing.T) {
	srv, rec := newServer(t, staticJSON(http.StatusInternalServerError, `{"code":"INTERNAL_SERVER_ERROR","message":"boom"}`))
	c := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	c.sleep = sleeps.hook

	resp, err := c.get(context.Background(), srv.URL, nil)
	if err != nil || resp.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("read-only GET must return final 500 response, resp=%v err=%v", resp, err)
	}
	// 首次 + 最多 2 次重试
	if rec.count() != 3 {
		t.Fatalf("请求次数 = %d，期望 3", rec.count())
	}
	assertDelays(t, sleeps.recorded(), 500*time.Millisecond, time.Second)
}

func Test400NotRetried(t *testing.T) {
	srv, rec := newServer(t, staticJSON(http.StatusBadRequest, `{"code":"INVALID_ARGUMENT","message":"bad phone"}`))
	c := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	c.sleep = sleeps.hook

	_, err := c.GetPhoneInfo(context.Background(), "13800138000")
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v，期望 ErrInvalidArgument", err)
	}
	if rec.count() != 1 {
		t.Fatalf("请求次数 = %d，期望 1（不重试）", rec.count())
	}
	if len(sleeps.recorded()) != 0 {
		t.Fatalf("不应有退避: %v", sleeps.recorded())
	}
}

func Test429HonorsRetryAfter(t *testing.T) {
	srv, rec := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":429,"message":"请求过于频繁","limit":60}`)
	})
	c := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	c.sleep = sleeps.hook

	_, err := c.GetPhoneInfo(context.Background(), "13800138000")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v，期望 ErrRateLimited", err)
	}
	if rec.count() != 3 {
		t.Fatalf("请求次数 = %d，期望 3", rec.count())
	}
	assertDelays(t, sleeps.recorded(), time.Second, time.Second)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(*APIError) = false")
	}
	if apiErr.RetryAfter != time.Second {
		t.Fatalf("RetryAfter = %v，期望 1s", apiErr.RetryAfter)
	}
}

func Test429RetryAfterThenSuccess(t *testing.T) {
	var calls atomic.Int64
	srv, rec := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, phoneRaw)
	})
	c := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	c.sleep = sleeps.hook

	if _, err := c.GetPhoneInfo(context.Background(), "13800138000"); err != nil {
		t.Fatalf("GetPhoneInfo 出错: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("请求次数 = %d，期望 2", rec.count())
	}
	assertDelays(t, sleeps.recorded(), 2*time.Second)
}

func Test429WithoutRetryAfterUsesBackoff(t *testing.T) {
	srv, rec := newServer(t, staticJSON(http.StatusTooManyRequests, `{"code":"RATE_LIMITED","message":"slow down"}`))
	c := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	c.sleep = sleeps.hook

	if _, err := c.GetPhoneInfo(context.Background(), "13800138000"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v，期望 ErrRateLimited", err)
	}
	if rec.count() != 3 {
		t.Fatalf("请求次数 = %d，期望 3", rec.count())
	}
	assertDelays(t, sleeps.recorded(), 500*time.Millisecond, time.Second)
}

func TestRetryBackoffTable(t *testing.T) {
	header := func(k, v string) http.Header { return http.Header{k: []string{v}} }
	tests := []struct {
		name    string
		attempt int
		status  int
		header  http.Header
		want    time.Duration
	}{
		{"第 1 次退避", 0, 0, nil, 500 * time.Millisecond},
		{"第 2 次退避", 1, 0, nil, time.Second},
		{"第 3 次退避", 2, 0, nil, 2 * time.Second},
		{"超出序列仍为 2s", 3, 0, nil, 2 * time.Second},
		{"429 优先 Retry-After", 0, http.StatusTooManyRequests, header("Retry-After", "5"), 5 * time.Second},
		{"429 非法 Retry-After 回退", 1, http.StatusTooManyRequests, header("Retry-After", "abc"), time.Second},
		{"500 不按 Retry-After", 0, http.StatusInternalServerError, header("Retry-After", "5"), 500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryBackoff(tt.attempt, tt.status, tt.header); got != tt.want {
				t.Fatalf("retryBackoff = %v，期望 %v", got, tt.want)
			}
		})
	}
}

func TestRetryAfterFromHeader(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"空 header", nil, 0},
		{"缺失", http.Header{}, 0},
		{"秒数", http.Header{"Retry-After": []string{"3"}}, 3 * time.Second},
		{"带空白", http.Header{"Retry-After": []string{" 2 "}}, 2 * time.Second},
		{"0 表示不等待", http.Header{"Retry-After": []string{"0"}}, 0},
		{"负数", http.Header{"Retry-After": []string{"-2"}}, 0},
		{"非法值", http.Header{"Retry-After": []string{"abc"}}, 0},
		{"过去的日期", http.Header{"Retry-After": []string{past}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryAfterFromHeader(tt.header); got != tt.want {
				t.Fatalf("retryAfterFromHeader = %v，期望 %v", got, tt.want)
			}
		})
	}
	t.Run("HTTP 日期", func(t *testing.T) {
		got := retryAfterFromHeader(http.Header{"Retry-After": []string{future}})
		if got <= 59*time.Minute || got > time.Hour {
			t.Fatalf("RetryAfter = %v，期望接近 1h", got)
		}
	})
}

// ---------- 超时 ----------

func TestTimeoutIsRetriedAndReported(t *testing.T) {
	srv, rec := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, phoneRaw)
	})
	c := newTestClient(srv.URL)
	c.SetTimeout(50 * time.Millisecond)
	sleeps := &sleepRecorder{}
	c.sleep = sleeps.hook

	start := time.Now()
	_, err := c.get(context.Background(), srv.URL, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望超时错误，实际 nil")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("errors.Is(err, ErrTimeout) = false，err = %v", err)
	}
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("errors.Is(err, ErrNetwork) = false，err = %v", err)
	}
	// 网络超时可重试：首次 + 2 次重试
	if rec.count() != 3 {
		t.Fatalf("请求次数 = %d，期望 3", rec.count())
	}
	assertDelays(t, sleeps.recorded(), 500*time.Millisecond, time.Second)
	if elapsed > 5*time.Second {
		t.Fatalf("超时未及时返回，耗时 %v", elapsed)
	}
}

func TestPaidGETDoesNotReplayLostResponse(t *testing.T) {
	var calls atomic.Int64
	srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	})
	client := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	client.sleep = sleeps.hook
	_, err := client.GetPhoneInfo(context.Background(), "13800138000")
	if !errors.Is(err, ErrNetwork) || calls.Load() != 1 || len(sleeps.recorded()) != 0 {
		t.Fatalf("paid GET must not replay an unknown result: calls=%d sleeps=%v err=%v", calls.Load(), sleeps.recorded(), err)
	}
}

func TestReadOnlyGETRetriesLostResponse(t *testing.T) {
	var calls atomic.Int64
	srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	client := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	client.sleep = sleeps.hook
	resp, err := client.get(context.Background(), srv.URL, nil)
	if err != nil || !resp.IsSuccess() || calls.Load() != 2 || len(sleeps.recorded()) != 1 {
		t.Fatalf("read-only GET should retry transient transport loss: calls=%d sleeps=%v err=%v", calls.Load(), sleeps.recorded(), err)
	}
}

func TestCallerContextDeadlineNoRetry(t *testing.T) {
	srv, rec := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	c := newTestClient(srv.URL)
	sleeps := &sleepRecorder{}
	c.sleep = sleeps.hook

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.GetPhoneInfo(ctx, "13800138000")
	if err == nil {
		t.Fatal("期望超时错误，实际 nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false，err = %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("请求次数 = %d，期望 1（调用方上下文已结束不再重试）", rec.count())
	}
	if len(sleeps.recorded()) != 0 {
		t.Fatalf("不应有退避: %v", sleeps.recorded())
	}
}

func TestWaitRespectsContextAndDelay(t *testing.T) {
	c := NewClient(testAPIKey)
	if err := c.wait(context.Background(), 0); err != nil {
		t.Fatalf("0 时长不应等待: %v", err)
	}
	start := time.Now()
	if err := c.wait(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("wait 出错: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("等待未生效，耗时 %v", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的上下文应返回 context.Canceled，实际 %v", err)
	}

	// 注入的 sleep 生效
	rec := &sleepRecorder{}
	c.sleep = rec.hook
	if err := c.wait(context.Background(), 7*time.Millisecond); err != nil {
		t.Fatalf("wait 出错: %v", err)
	}
	assertDelays(t, rec.recorded(), 7*time.Millisecond)
}

// ---------- 本地参数校验：不发请求 ----------

func TestLocalValidationSendsNoRequest(t *testing.T) {
	srv, rec := newServer(t, staticJSON(http.StatusOK, phoneRaw))
	c := newTestClient(srv.URL)

	t.Run("非法手机号", func(t *testing.T) {
		phones := []string{
			"1380013800",    // 10 位
			"138001380000",  // 12 位
			"1380013800a",   // 非数字
			"23800138000",   // 不以 1 开头
			"138 0013 8000", // 含空白
			"",              // 空
		}
		for _, phone := range phones {
			before := rec.count()
			_, err := c.GetPhoneInfo(context.Background(), phone)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("手机号 %q：err = %v，期望 ErrInvalidArgument", phone, err)
			}
			if rec.count() != before {
				t.Fatalf("手机号 %q：本地校验失败仍发起了请求", phone)
			}
		}
	})

	t.Run("非法 IP 查询", func(t *testing.T) {
		ips := []string{
			"1.2.3.4/24",             // 非法字符
			"1.2.3.4?source=x",       // 非法字符
			"not a domain",           // 含空白
			strings.Repeat("a", 254), // 超长
			"例子.中国",                  // 非 ASCII，需上层转 punycode
			"",
		}
		for _, ip := range ips {
			before := rec.count()
			_, err := c.GetIPInfo(context.Background(), ip, "")
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("IP %q：err = %v，期望 ErrInvalidArgument", ip, err)
			}
			if rec.count() != before {
				t.Fatalf("IP %q：本地校验失败仍发起了请求", ip)
			}
		}
	})

	t.Run("非法 source", func(t *testing.T) {
		for _, source := range []string{"free", "COMMERCIAL", "Commercial", "commercial2"} {
			before := rec.count()
			_, err := c.GetIPInfo(context.Background(), "8.8.8.8", source)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("source %q：err = %v，期望 ErrInvalidArgument", source, err)
			}
			if rec.count() != before {
				t.Fatalf("source %q：本地校验失败仍发起了请求", source)
			}
		}
	})
}

func TestValidQueryFormsAreAccepted(t *testing.T) {
	srv, rec := newServer(t, staticJSON(http.StatusOK, ipRaw))
	c := newTestClient(srv.URL)
	valid := []string{"8.8.8.8", "2001:4860:4860::8888", "example.com", "dns.google", "xn--fiq228c.cn"}
	for _, ip := range valid {
		before := rec.count()
		if _, err := c.GetIPInfo(context.Background(), ip, ""); err != nil {
			t.Fatalf("IP %q 应被接受，实际错误: %v", ip, err)
		}
		if rec.count() != before+1 {
			t.Fatalf("IP %q 未发起请求", ip)
		}
		if got := rec.last(t).query.Get("ip"); got != ip {
			t.Fatalf("ip 参数 = %q，期望 %q", got, ip)
		}
	}
}

func TestMissingAPIKeySendsNoRequest(t *testing.T) {
	srv, rec := newServer(t, staticJSON(http.StatusOK, phoneRaw))
	for _, key := range []string{"", "   "} {
		c := NewClient(key)
		c.SetPhoneInfoURL(srv.URL + phoneInfoPath)
		c.SetIPInfoURL(srv.URL + ipInfoPath)
		if _, err := c.GetPhoneInfo(context.Background(), "13800138000"); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("密钥 %q：err = %v，期望 ErrUnauthorized", key, err)
		}
		if _, err := c.GetIPInfo(context.Background(), "8.8.8.8", SourceCommercial); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("密钥 %q：err = %v，期望 ErrUnauthorized", key, err)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("缺少密钥时不应发起请求，实际 %d 次", rec.count())
	}
}

// ---------- 校验函数本身 ----------

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"13800138000", "13800138000", false},
		{" 13800138000 ", "13800138000", false},
		{"19912345678", "19912345678", false},
		{"1380013800", "", true},
		{"138001380000", "", true},
		{"1380013800a", "", true},
		{"23800138000", "", true},
		{"", "", true},
		{"   ", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizePhone(tt.in)
		if tt.wantErr {
			if err == nil || !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("NormalizePhone(%q) err = %v，期望 ErrInvalidArgument", tt.in, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizePhone(%q) 出错: %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("NormalizePhone(%q) = %q，期望 %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeIPQuery(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"8.8.8.8", "8.8.8.8", false},
		{" 8.8.8.8 ", "8.8.8.8", false},
		{"2001:4860:4860::8888", "2001:4860:4860::8888", false},
		{"example.com", "example.com", false},
		{"a", "a", false},
		{"1.2.3.4/24", "", true},
		{"not a domain", "", true},
		{"", "", true},
		{strings.Repeat("a", 254), "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeIPQuery(tt.in)
		if tt.wantErr {
			if err == nil || !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("NormalizeIPQuery(%q) err = %v，期望 ErrInvalidArgument", tt.in, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeIPQuery(%q) 出错: %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("NormalizeIPQuery(%q) = %q，期望 %q", tt.in, got, tt.want)
		}
	}
}

// ---------- APIError 细节 ----------

func TestAPIErrorIsAndAs(t *testing.T) {
	err := newAPIError(http.StatusTooManyRequests, []byte(`{"code":429,"message":"限流","limit":60}`),
		http.Header{"X-Request-Id": []string{"req-1"}, "Retry-After": []string{"2"}})

	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("errors.Is(ErrRateLimited) = false，err = %v", err)
	}
	if errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("不应同时匹配 ErrInsufficientCredits: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As(*APIError) = false")
	}
	if apiErr.Code != "429" || apiErr.Message != "限流" || apiErr.RequestID != "req-1" || apiErr.RetryAfter != 2*time.Second {
		t.Fatalf("APIError = %+v", apiErr)
	}
	if !strings.Contains(apiErr.Error(), "429") || !strings.Contains(apiErr.Error(), "限流") {
		t.Fatalf("Error() = %q", apiErr.Error())
	}
	if !strings.Contains(apiErr.Error(), "req-1") {
		t.Fatalf("Error() 未包含 RequestID: %q", apiErr.Error())
	}

	quota := newAPIError(http.StatusTooManyRequests, []byte(`{"code":"VISITOR_MONTHLY_QUOTA_EXHAUSTED"}`), nil)
	if !errors.Is(quota, ErrInsufficientCredits) {
		t.Fatalf("配额耗尽应匹配 ErrInsufficientCredits: %v", quota)
	}
	if errors.Is(quota, ErrRateLimited) {
		t.Fatalf("配额耗尽不应匹配 ErrRateLimited: %v", quota)
	}
}
