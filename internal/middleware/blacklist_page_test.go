package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

// 拉黑页面 vs JSON 的分流（45-*.md §0.1.1 Q63）。
//
// 判据见 WantsBlacklistHTML 的注释。这些用例同时钉住「浏览器拿到页面、
// API 拿到 JSON」两个方向 —— 只测一个方向会把另一个方向改坏而不自知。

func TestWantsBlacklistHTMLMatrix(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		accept string
		want   bool
	}{
		// 浏览器导航：Accept 含 text/html → 页面。
		{"browser navigation", http.MethodGet, "/", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", true},
		{"browser on login page", http.MethodGet, "/user/login", "text/html,application/xhtml+xml", true},
		{"browser on admin login", http.MethodGet, "/admin-login", "text/html", true},

		// 客户端明确要 JSON → JSON，哪怕同时声明了 text/html。
		{"explicit json", http.MethodGet, "/api/search", "application/json", false},
		{"json wins over html", http.MethodGet, "/", "text/html, application/json", false},
		{"json with charset", http.MethodPost, "/api/search", "application/json; charset=utf-8", false},

		// API 前缀 + 无 Accept → JSON（兜住 curl / 老客户端）。
		{"api prefix no accept", http.MethodPost, "/api/search", "", false},
		{"api prefix star accept", http.MethodGet, "/api/user", "*/*", false},
		{"api prefix put", http.MethodPut, "/api/user/profile", "", false},

		// 非 API 路径的 GET 且无 Accept → 页面（SPA 路由必须能出）。
		{"spa route no accept", http.MethodGet, "/dashboard", "", true},
		{"root no accept", http.MethodGet, "/", "", true},
		{"head request", http.MethodHead, "/", "", true},

		// 非 GET 且无 Accept → JSON（写操作按 API 处理）。
		{"post no accept non-api", http.MethodPost, "/some-form", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(tc.method, tc.path, nil)
			if tc.accept != "" {
				c.Request.Header.Set("Accept", tc.accept)
			}
			if got := WantsBlacklistHTML(c); got != tc.want {
				t.Fatalf("WantsBlacklistHTML(%s %s, Accept=%q) = %v, want %v",
					tc.method, tc.path, tc.accept, got, tc.want)
			}
		})
	}
	// nil 安全：不能 panic。
	if WantsBlacklistHTML(nil) {
		t.Fatal("nil context must not want HTML")
	}
}

// 拉黑页面必须显示 reason，且**绝不**泄漏 pattern。
func TestRenderBlacklistPageShowsReasonNotPattern(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	const reason = "请求过于频繁，请稍后再试"
	RenderBlacklistPage(c, reason)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, reason) {
		t.Fatalf("page must show the reason, body=%q", body[:min(400, len(body))])
	}
	// 状态说明与管理员入口都必须出现。
	if !strings.Contains(body, BlacklistPageStatusMessage) {
		t.Fatal("page must contain the status message")
	}
	if !strings.Contains(body, AdminLoginPagePath) {
		t.Fatal("page must contain the admin login link")
	}
	if !strings.Contains(body, "管理员登录") {
		t.Fatal("page must contain the admin login label")
	}
	// Content-Type 必须是 HTML。
	if ct := recorder.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	// CSP 必须禁止脚本（纵深防御：即使转义被改坏也执行不了）。
	if csp := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'none'") {
		t.Fatalf("CSP must forbid scripts, got %q", csp)
	}
}

// reason 是运营填写的自由文本 → 必须 HTML 转义，否则是存储型 XSS。
func TestRenderBlacklistPageEscapesReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	RenderBlacklistPage(c, `<script>alert(1)</script><img src=x onerror=alert(2)>`)
	body := recorder.Body.String()

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("reason must be HTML-escaped: raw script tag leaked into the page")
	}
	if strings.Contains(body, "onerror=alert(2)>") {
		t.Fatal("reason must be HTML-escaped: raw event handler leaked into the page")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("escaped reason should still be visible as text")
	}
}

// 空 reason 要有兜底文案（否则用户看到一片空白，以为页面坏了）。
func TestRenderBlacklistPageFallsBackForEmptyReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, reason := range []string{"", "   ", "\t\n"} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		RenderBlacklistPage(c, reason)
		if !strings.Contains(recorder.Body.String(), BlacklistPageReasonFallback) {
			t.Fatalf("empty reason %q must fall back to %q", reason, BlacklistPageReasonFallback)
		}
	}
}

// 超长 reason 按字符截断，不能把页面撑爆、也不能按字节切坏中文。
func TestRenderBlacklistPageTruncatesLongReasonByRunes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	longReason := strings.Repeat("原", 1000)
	RenderBlacklistPage(c, longReason)
	body := recorder.Body.String()

	// ⚠️ 不能用 strings.Count(body, "原") 数：页面模板里「受限原因」也含「原」，
	// 会多数 1 个（这里曾因此误判成「截断没生效」）。改用唯一的连续串来定位。
	if strings.Contains(body, strings.Repeat("原", 256)) {
		t.Fatal("reason must be truncated to 255 runes (256 consecutive runes found)")
	}
	if !strings.Contains(body, strings.Repeat("原", 255)) {
		t.Fatal("reason should keep the first 255 runes")
	}
	// 截断后仍然是合法 UTF-8 且页面完整（不能切出半个字符）。
	if !strings.Contains(body, "</html>") {
		t.Fatal("page must still be complete after truncation")
	}
	if !utf8.ValidString(body) {
		t.Fatal("truncation must not split a multi-byte character")
	}
}

// 端到端：命中黑名单时，浏览器拿页面、API 拿 JSON。
func TestBlacklistMiddlewareSplitsPageAndJSON(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	entry := seedBlacklistIPRule(t, db, "0.0.0.0/0", service.BlacklistMatchCIDR, true)
	// 给规则填一个 reason（拉黑页面要显示它）。
	if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entry.ID).
		Update("reason", "请求过于频繁").Error; err != nil {
		t.Fatalf("set reason: %v", err)
	}

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))

	// 浏览器请求 → 拉黑页面（HTML），且含 reason。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("browser status = %d, want 403", recorder.Code)
	}
	if ct := recorder.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("browser content-type = %q, want text/html", ct)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "请求过于频繁") {
		t.Fatalf("page must show the rule reason, body=%q", body[:min(300, len(body))])
	}
	if !strings.Contains(body, AdminLoginPagePath) {
		t.Fatal("page must link to the admin login entry")
	}

	// API 请求 → JSON，且**不含 reason、不含规则细节**。
	req = httptest.NewRequest(http.MethodPost, "/api/search", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	req.Header.Set("Accept", "application/json")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("api status = %d, want 403", recorder.Code)
	}
	if ct := recorder.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("api content-type = %q, want application/json", ct)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("api body must be JSON: %v (body=%q)", err, recorder.Body.String())
	}
	if payload["code"] != float64(403) {
		t.Fatalf("api code = %v, want 403", payload["code"])
	}
	if payload["message"] != "当前访问受限，如有疑问请联系管理员" {
		t.Fatalf("api message = %v", payload["message"])
	}
	// JSON 分支绝不能出现 reason 或规则边界。
	raw := recorder.Body.String()
	for _, leak := range []string{"请求过于频繁", "0.0.0.0", "cidr", "reason", "pattern"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("api JSON must not leak %q: %s", leak, raw)
		}
	}
}

// 无 Accept 头的 API 调用（curl 默认 `*/*`）仍要拿到 JSON。
func TestBlacklistMiddlewareAPIWithoutAcceptGetsJSON(t *testing.T) {
	db := newBlacklistMiddlewareTestDB(t)
	setBlacklistSwitch(t, db, true)
	seedBlacklistIPRule(t, db, "0.0.0.0/0", service.BlacklistMatchCIDR, true)

	cache := loadBlacklistCache(t, db)
	router := newBlacklistTestRouter(BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), db))

	req := httptest.NewRequest(http.MethodPost, "/api/search", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	// 不设 Accept（httptest 默认没有该头）。
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if ct := recorder.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json for an API path without Accept", ct)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
