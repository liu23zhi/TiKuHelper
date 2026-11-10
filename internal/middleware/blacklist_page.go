package middleware

import (
	"html"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// 拉黑页面（45-*.md §0.1.1 Q63 定稿）。
//
// 用户要求：被拉黑 IP「打开网页即显示拉黑原因，无法登录等」，而不是一个 403 JSON。
// 同时必须保留 API 请求的 JSON 语义 —— 客户端（浏览器扩展、脚本）拿到 HTML 会解析失败。
//
// 所以命中后要**按请求类型分叉**：
//
//	HTML 页面请求 → 渲染拉黑页面（含 reason + 管理员登录入口）
//	API / 其他请求 → 仍然 403 JSON（固定文案，不含 reason、不含规则细节）

// BlacklistPageReasonFallback 是规则没有填写 reason 时的兜底文案。
//
// 不能显示空字符串：用户看到一片空白会以为是页面坏了。
const BlacklistPageReasonFallback = "您的网络访问已被限制"

// BlacklistPageStatusMessage 是拉黑页面的状态说明（45-*.md §0.1.1）。
const BlacklistPageStatusMessage = "当前访问受限，无法登录或使用服务"

// AdminLoginPagePath 是管理员专用登录入口的路径（45-*.md §0.1.2）。
//
// 它与 /user/login 分开：普通登录要被黑名单拦，管理员入口不拦。
const AdminLoginPagePath = "/admin-login"

// WantsBlacklistHTML 判断这个请求是否应当拿到拉黑**页面**而不是 JSON。
//
// 判据（按优先级）：
//
//  1. **Accept 头含 text/html** → 页面。这是浏览器的自然行为：
//     地址栏导航发 `Accept: text/html,application/xhtml+xml,...`，
//     而 fetch/XHR 通常发 `Accept: */*` 或显式 `application/json`。
//  2. **Accept 显式含 application/json** → JSON。哪怕同时含 text/html
//     （有些客户端会两个都带上），也按 JSON 处理 —— 因为「客户端明确声明能读 JSON」
//     时返回 JSON 更安全，HTML 会让它解析失败。
//  3. **路径在 API 前缀下**（/adapter-service/）→ JSON。
//     这一条兜住「没带 Accept 头」的 API 调用（老客户端、curl 默认 `Accept: */*`）。
//  4. **其余 GET 请求**（如 `/`、`/admin-login`、SPA 路由）→ 页面。
//     这些是「用户在浏览器里打开网页」的形态；SPA 的 index.html 也必须能出来，
//     否则被拉黑的人连拉黑页面都看不到。
//  5. 其余（非 GET 且无 Accept）→ JSON。写操作一律按 API 处理。
//
// ⚠️ 不要用 User-Agent 判断：UA 可以任意伪造，而且「用 UA 决定响应格式」
// 会让 curl 与真实浏览器得到不一致的结果，排障时极其困惑。
func WantsBlacklistHTML(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	accept := strings.ToLower(strings.TrimSpace(c.GetHeader("Accept")))

	// 2) 客户端明确要 JSON → 优先按 JSON（放在 text/html 判断之前：
	//    有些客户端会同时声明两者，此时尊重更明确的 JSON 意图）。
	if strings.Contains(accept, "application/json") {
		return false
	}
	// 1) 浏览器导航 → 页面。
	if strings.Contains(accept, "text/html") {
		return true
	}
	// 3) API 前缀 → JSON（兜住没有 Accept 头的 API 调用）。
	if strings.HasPrefix(c.Request.URL.Path, "/adapter-service") {
		return false
	}
	// 4) 其余 GET → 页面（SPA 路由、/、/admin-login 等）。
	if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
		return true
	}
	// 5) 其余（POST 且无 Accept）→ JSON。
	return false
}

// RenderBlacklistPage 输出拉黑页面。
//
// 安全要点（每一条都对应一个真实攻击面）：
//
//   - **只显示 reason，绝不显示 pattern / CIDR / entry id**（45-*.md §0.1.1 与 46-*.md §3.2）。
//     泄漏 pattern 等于告诉攻击者精确的绕过边界。
//   - **reason 必须 HTML 转义**：它是运营填写的自由文本，若直接拼进 HTML，
//     运营（或拿到运营账号的人）可以写入 <script> 造成存储型 XSS。
//   - **不引用任何外部资源**（无 CDN、无外链字体）：被拉黑的 IP 本来就可能
//     连不上外网，而且外部资源会把「谁在什么时候被拦」泄漏给第三方。
//   - **CSP 头**：即使将来有人改坏了转义，内联脚本也执行不了。
//   - **不设置任何 Cookie、不读任何凭据**：这是纯静态响应。
func RenderBlacklistPage(c *gin.Context, reason string) {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		trimmed = BlacklistPageReasonFallback
	}
	// 长度上限：reason 来自 varchar(255)，但这里是纵深防御 ——
	// 万一有超长 reason 也不会把页面撑爆。
	trimmed = truncateRunes(trimmed, 255)
	escapedReason := html.EscapeString(trimmed)

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	c.Header("Referrer-Policy", "no-referrer")
	// 只允许内联样式（下面 style 标签用 nonce 免签），不允许任何脚本。
	c.Header("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; img-src 'none'; script-src 'none'; base-uri 'none'; form-action 'none'")

	c.Status(http.StatusForbidden)
	_, _ = c.Writer.WriteString(blacklistPageHTML(escapedReason))
	c.Abort()
}

// blacklistPageHTML 生成拉黑页面的 HTML（reason 已转义）。
//
// 刻意做成内联样式 + 零外部依赖的单个 HTML：被拉黑 IP 可能连不上任何 CDN，
// 页面必须在「只能访问本站」的条件下完整显示。
func blacklistPageHTML(escapedReason string) string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>访问受限</title>
<style>
:root { color-scheme: light dark; }
* { box-sizing: border-box; }
body {
  margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
  background: #f5f6f8; color: #1f2328; padding: 24px;
}
.card {
  max-width: 520px; width: 100%; background: #fff; border-radius: 12px;
  padding: 32px 28px; box-shadow: 0 1px 3px rgba(0,0,0,.08), 0 8px 24px rgba(0,0,0,.06);
  border: 1px solid #e6e8eb;
}
.icon {
  width: 44px; height: 44px; border-radius: 50%; background: #fdecea; color: #c0392b;
  display: flex; align-items: center; justify-content: center; font-size: 22px; font-weight: 700;
  margin-bottom: 18px;
}
h1 { font-size: 19px; margin: 0 0 10px; font-weight: 600; }
.status { font-size: 14px; color: #57606a; margin: 0 0 20px; line-height: 1.6; }
.reason-box {
  background: #f6f8fa; border: 1px solid #e6e8eb; border-left: 3px solid #d0d7de;
  border-radius: 6px; padding: 12px 14px; margin: 0 0 22px;
}
.reason-label { font-size: 12px; color: #6e7781; margin: 0 0 6px; letter-spacing: .02em; }
.reason { font-size: 14px; margin: 0; line-height: 1.65; word-break: break-word; white-space: pre-wrap; }
.actions { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; }
.btn {
  display: inline-block; padding: 9px 18px; border-radius: 6px; font-size: 14px;
  text-decoration: none; font-weight: 500; border: 1px solid transparent;
}
.btn-primary { background: #1f6feb; color: #fff; }
.btn-primary:hover { background: #1a5fd0; }
.btn-ghost { background: #fff; color: #57606a; border-color: #d0d7de; }
.btn-ghost:hover { background: #f6f8fa; }
.hint { margin: 18px 0 0; font-size: 12px; color: #8c959f; line-height: 1.6; }
@media (prefers-color-scheme: dark) {
  body { background: #0d1117; color: #e6edf3; }
  .card { background: #161b22; border-color: #30363d; box-shadow: 0 8px 24px rgba(0,0,0,.4); }
  .status { color: #8b949e; }
  .reason-box { background: #0d1117; border-color: #30363d; }
  .reason-label { color: #8b949e; }
  .btn-ghost { background: #21262d; color: #c9d1d9; border-color: #30363d; }
  .hint { color: #6e7781; }
}
</style>
</head>
<body>
<main class="card" role="main">
  <div class="icon" aria-hidden="true">!</div>
  <h1>访问受限</h1>
  <p class="status">` + BlacklistPageStatusMessage + `</p>
  <div class="reason-box">
    <p class="reason-label">受限原因</p>
    <p class="reason">` + escapedReason + `</p>
  </div>
  <div class="actions">
    <a class="btn btn-primary" href="` + AdminLoginPagePath + `">管理员登录</a>
  </div>
  <p class="hint">如非本人操作或认为判断有误，请通过管理员入口登录后核查处理。</p>
</main>
</body>
</html>
`
}

// truncateRunes 按**字符**截断（判定与切片必须同单位）。
func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
