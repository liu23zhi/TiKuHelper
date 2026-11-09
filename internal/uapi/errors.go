package uapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 导出哨兵错误，上层用 errors.Is 判别失败类别。
var (
	// ErrInvalidArgument 请求参数不合法（HTTP 400，或本地参数校验失败；不重试）
	ErrInvalidArgument = errors.New("uapi: 请求参数不合法")
	// ErrUnauthorized 鉴权失败或密钥缺失（HTTP 401；不重试）
	ErrUnauthorized = errors.New("uapi: 鉴权失败")
	// ErrInsufficientCredits 积分或配额不足（HTTP 402，或 429 且 code 含
	// VISITOR_MONTHLY_QUOTA_EXHAUSTED；不重试）
	ErrInsufficientCredits = errors.New("uapi: 积分或配额不足")
	// ErrForbidden 请求被拒绝（HTTP 403；不重试）
	ErrForbidden = errors.New("uapi: 请求被拒绝")
	// ErrNotFound 资源不存在（HTTP 404；不重试）
	ErrNotFound = errors.New("uapi: 资源不存在")
	// ErrRateLimited 触发限流（HTTP 429；可重试）
	ErrRateLimited = errors.New("uapi: 请求过于频繁")
	// ErrUpstream 上游服务异常（HTTP 408/5xx，以及其它未列举状态码；可重试）
	ErrUpstream = errors.New("uapi: 上游服务异常")
	// ErrTextTooLong 送检文本超出上游长度限制（HTTP 413）。
	//
	// 单独成一个哨兵是为了**可区分**：413 落到 ErrUpstream 时，「文本太长」与
	// 「上游挂了」在监控里长得一模一样，超长会一直被当成偶发故障。
	// 实测该限制是 text 字段 262144 字节（见 internal/service/sensitive_words.go
	// 的 sensitiveWordMaxTextBytes）。
	//
	// 注意：413 的错误**同时**满足 errors.Is(err, ErrUpstream)（见 newAPIError），
	// 所以既有按 ErrUpstream 判定的调用方行为不变，只是多了一个更精确的判别入口。
	ErrTextTooLong = errors.New("uapi: 送检文本超出上游长度限制")
	// ErrTimeout 网络请求超时（可重试）
	ErrTimeout = errors.New("uapi: 请求超时")
	// ErrNetwork 网络层错误（DNS、连接失败、超时等，不含上游返回的错误响应）
	ErrNetwork = errors.New("uapi: 网络请求失败")
)

// APIError uapis.cn 返回的错误响应，可用 errors.As 取出。
type APIError struct {
	HTTPStatus int           // HTTP 状态码
	Code       string        // 错误码：优先字符串形式 code，其次 error，最后数字形式 code
	Message    string        // 错误信息（原样保留）
	RequestID  string        // 响应头 X-Request-ID
	RetryAfter time.Duration // 响应头 Retry-After 解析结果（秒数或 HTTP 日期）

	// kind 对应的导出哨兵错误，供 errors.Is 判别
	kind error
}

// Error 实现 error 接口
func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("uapi: HTTP ")
	b.WriteString(strconv.Itoa(e.HTTPStatus))
	if e.Code != "" {
		b.WriteString(" ")
		b.WriteString(e.Code)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.RequestID != "" {
		b.WriteString(" (request_id=")
		b.WriteString(e.RequestID)
		b.WriteString(")")
	}
	if e.RetryAfter > 0 {
		b.WriteString(" (retry_after=")
		b.WriteString(e.RetryAfter.String())
		b.WriteString(")")
	}
	return b.String()
}

// Unwrap 暴露对应哨兵错误，使 errors.Is(err, ErrRateLimited) 之类的判断成立
func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.kind
}

// errorPayload 兼容官方 FAQ Q32 列出的 4 种错误响应结构：
//
//	{"code":"STR","message":...,"details":...}
//	{"error":"STR","details":...}
//	{"error":"STR","message":...,"docs":{...}}
//	{"code":429,"message":...,"limit":...}
type errorPayload struct {
	Code    json.RawMessage `json:"code"`
	Error   json.RawMessage `json:"error"`
	Message json.RawMessage `json:"message"`
	Details json.RawMessage `json:"details"`
	Docs    json.RawMessage `json:"docs"`
	Limit   json.RawMessage `json:"limit"`
}

// code 按“字符串 code → error → 数字 code”的优先级取错误码
func (p errorPayload) code() string {
	var s string
	if err := json.Unmarshal(p.Code, &s); err == nil && s != "" {
		return s
	}
	if err := json.Unmarshal(p.Error, &s); err == nil && s != "" {
		return s
	}
	// 少数结构（如 {code:429,...}）的 code 是数字
	return rawString(p.Code)
}

// message 取错误信息：优先 message 字段（原样保留）；
// 结构 {"error":"STR","details":"STR"} 没有 message，此时退化为 details 字符串。
func (p errorPayload) message() string {
	if s := rawString(p.Message); s != "" {
		return s
	}
	return rawString(p.Details)
}

// newAPIError 由非 2xx 响应构造 APIError
func newAPIError(status int, body []byte, header http.Header) *APIError {
	var payload errorPayload
	// 容错：错误响应不是合法 JSON 时字段留空，仍然返回状态码信息
	_ = json.Unmarshal(body, &payload)
	code := payload.code()
	return &APIError{
		HTTPStatus: status,
		Code:       code,
		Message:    payload.message(),
		RequestID:  header.Get("X-Request-ID"),
		RetryAfter: retryAfterFromHeader(header),
		kind:       sentinelForStatus(status, code),
	}
}

// sentinelForStatus 状态码 → 哨兵错误
func sentinelForStatus(status int, code string) error {
	switch status {
	case http.StatusBadRequest: // 400
		return ErrInvalidArgument
	case http.StatusUnauthorized: // 401
		return ErrUnauthorized
	case http.StatusPaymentRequired: // 402
		return ErrInsufficientCredits
	case http.StatusForbidden: // 403
		return ErrForbidden
	case http.StatusNotFound: // 404
		return ErrNotFound
	case http.StatusRequestTimeout: // 408
		return ErrUpstream
	case http.StatusRequestEntityTooLarge: // 413
		// 双哨兵：既要能区分「超长」（ErrTextTooLong），又要保持既有
		// errors.Is(err, ErrUpstream) 判定不变（ocr/nsfw 等调用方已按 ErrUpstream 处理）。
		// errors.Join 的 Unwrap() []error 让两个 errors.Is 同时成立。
		return errors.Join(ErrTextTooLong, ErrUpstream)
	case http.StatusTooManyRequests: // 429
		if strings.Contains(strings.ToUpper(code), "VISITOR_MONTHLY_QUOTA_EXHAUSTED") {
			return ErrInsufficientCredits
		}
		return ErrRateLimited
	}
	// 5xx 及其它未列举的状态码统一归为上游异常
	return ErrUpstream
}

// retryAfterFromHeader 解析 Retry-After 响应头，兼容“秒数”与 HTTP 日期两种格式；
// 缺失或非法时返回 0。
func retryAfterFromHeader(header http.Header) time.Duration {
	if header == nil {
		return 0
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}
