package uapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

const (
	// DefaultPhoneInfoURL 手机归属地接口完整地址
	DefaultPhoneInfoURL = "https://uapis.cn/api/v1/misc/phoneinfo"
	// DefaultIPInfoURL IP 查询接口完整地址
	DefaultIPInfoURL = "https://uapis.cn/api/v1/network/ipinfo"
	// defaultTimeout 默认请求超时，与 internal/search 下的客户端保持一致
	defaultTimeout = 5 * time.Second
	// maxRetries 只读请求最多重试两次，共三次尝试。
	maxRetries = 2
	// backoffBase 第一次退避时间，其后为 1s。
	backoffBase = 500 * time.Millisecond
)

// Client uapis.cn 客户端。
// 密钥由调用方通过 NewClient 注入，仅用于 Authorization 请求头，
// 不硬编码、不写入 URL、不落盘。
type Client struct {
	apiKey             string
	timeout            time.Duration
	phoneInfoURL       string
	ipInfoURL          string
	searchEnginesURL   string
	searchAggregateURL string
	statusUsageURL     string
	webToMarkdownURL   string
	imageOCRURL        string
	http               *resty.Client
	// maxRetries 本客户端单次业务请求允许的内部重试次数（不含首次）。
	//
	// 默认 maxRetries，即共 3 次尝试。上层若自己负责重试编排（例如按
	// 「连续失败 3 次就整批放弃」控制上游压力），应当通过 SetMaxRetries(0)
	// 关掉内部重试，否则重试次数会相乘。
	maxRetries int
	// sleep 退避等待实现，测试注入后可跳过真实等待
	sleep func(ctx context.Context, d time.Duration) error
	// jitter 只作用于普通退避，Retry-After 不加抖动。
	jitter func(time.Duration) time.Duration
}

// NewClient 创建 uapi 客户端，apiKey 为 uapis.cn 签发的密钥（uapi- 开头）
func NewClient(apiKey string) *Client {
	c := &Client{
		apiKey:             strings.TrimSpace(apiKey),
		timeout:            defaultTimeout,
		phoneInfoURL:       DefaultPhoneInfoURL,
		ipInfoURL:          DefaultIPInfoURL,
		searchEnginesURL:   DefaultSearchEnginesURL,
		searchAggregateURL: DefaultSearchAggregateURL,
		statusUsageURL:     DefaultStatusUsageURL,
		maxRetries:         maxRetries,
		jitter:             jitterRetryBackoff,
	}
	c.http = resty.New().SetTimeout(c.timeout)
	return c
}

// SetMaxRetries 覆盖内部重试次数（不含首次请求）。
//
// 传 0 表示**不做内部重试**：适合上层自己编排重试的场景，避免重试次数相乘。
func (c *Client) SetMaxRetries(retries int) *Client {
	if c == nil || retries < 0 {
		return c
	}
	if retries > maxRetries {
		retries = maxRetries
	}
	c.maxRetries = retries
	return c
}

// SetTimeout 覆盖请求超时时间（默认 5s）
func (c *Client) SetTimeout(timeout time.Duration) *Client {
	if c == nil || timeout <= 0 {
		return c
	}
	c.timeout = timeout
	if c.http != nil {
		c.http.SetTimeout(timeout)
	}
	return c
}

// SetPhoneInfoURL 覆盖手机归属地接口地址（默认 DefaultPhoneInfoURL），仅用于测试/代理
func (c *Client) SetPhoneInfoURL(url string) *Client {
	if c == nil {
		return c
	}
	c.phoneInfoURL = strings.TrimSpace(url)
	return c
}

// SetIPInfoURL 覆盖 IP 查询接口地址（默认 DefaultIPInfoURL），仅用于测试/代理
func (c *Client) SetIPInfoURL(url string) *Client {
	if c == nil {
		return c
	}
	c.ipInfoURL = strings.TrimSpace(url)
	return c
}

// GetPhoneInfo 查询手机号归属地。
// 密钥可选，未配置时使用上游访客额度且不发送 Authorization。
// 参数不合法时在本地直接返回错误，不发起 HTTP 请求、不消耗积分。
func (c *Client) GetPhoneInfo(ctx context.Context, phone string) (PhoneInfo, error) {
	if err := c.ensureInitialized(); err != nil {
		return PhoneInfo{}, err
	}
	normalized, err := NormalizePhone(phone)
	if err != nil {
		return PhoneInfo{}, err
	}
	resp, err := c.getBillable(ctx, c.phoneInfoAddress(), map[string]string{"phone": normalized})
	if err != nil {
		return PhoneInfo{}, err
	}
	if err := checkResponse(resp); err != nil {
		return PhoneInfo{}, err
	}
	info, err := ParsePhoneInfo(resp.Body())
	if err != nil {
		return PhoneInfo{}, fmt.Errorf("%w: 解析手机归属地响应失败: %w", ErrUpstream, err)
	}
	return info, nil
}

// GetIPInfo 查询 IP 归属地。
// source 为空时不下发该 query；唯一合法值为 "commercial"，其它取值在本地拒绝，
// 不发起 HTTP 请求。普通来源允许不配置密钥，商业增强来源仍要求密钥。
func (c *Client) GetIPInfo(ctx context.Context, ip string, source string) (IPInfo, error) {
	if err := c.ensureInitialized(); err != nil {
		return IPInfo{}, err
	}
	normalized, err := NormalizeIPQuery(ip)
	if err != nil {
		return IPInfo{}, err
	}
	source = strings.TrimSpace(source)
	if source != "" && source != SourceCommercial {
		return IPInfo{}, fmt.Errorf("%w: source 唯一合法值为 %q", ErrInvalidArgument, SourceCommercial)
	}
	if source == SourceCommercial {
		if err := c.ensureReady(); err != nil {
			return IPInfo{}, err
		}
	}
	query := map[string]string{"ip": normalized}
	if source != "" {
		query["source"] = source
	}
	resp, err := c.getBillable(ctx, c.ipInfoAddress(), query)
	if err != nil {
		return IPInfo{}, err
	}
	if err := checkResponse(resp); err != nil {
		return IPInfo{}, err
	}
	info, err := ParseIPInfo(resp.Body())
	if err != nil {
		return IPInfo{}, fmt.Errorf("%w: 解析 IP 查询响应失败: %w", ErrUpstream, err)
	}
	return info, nil
}

func (c *Client) ensureInitialized() error {
	if c == nil || c.http == nil {
		return fmt.Errorf("%w: uapi 客户端未初始化", ErrUnauthorized)
	}
	return nil
}

// ensureReady 校验需要密钥的客户端配置，避免密钥缺失时白跑一次请求。
func (c *Client) ensureReady() error {
	if err := c.ensureInitialized(); err != nil {
		return err
	}
	if c.apiKey == "" {
		return fmt.Errorf("%w: 缺少 uapi 密钥", ErrUnauthorized)
	}
	return nil
}

// phoneInfoAddress 返回手机归属地接口地址，未配置时回退到默认地址
func (c *Client) phoneInfoAddress() string {
	if c.phoneInfoURL != "" {
		return c.phoneInfoURL
	}
	return DefaultPhoneInfoURL
}

// ipInfoAddress 返回 IP 查询接口地址，未配置时回退到默认地址
func (c *Client) ipInfoAddress() string {
	if c.ipInfoURL != "" {
		return c.ipInfoURL
	}
	return DefaultIPInfoURL
}

// checkResponse 非 2xx 响应转换为 APIError
func checkResponse(resp *resty.Response) error {
	if resp == nil {
		return fmt.Errorf("%w: 上游返回空响应", ErrNetwork)
	}
	if resp.IsSuccess() {
		return nil
	}
	return newAPIError(resp.StatusCode(), resp.Body(), resp.Header())
}

// get 发起 GET 请求。
// 仅在 408/425/429/5xx 与瞬时网络错误时重试，最多 maxRetries 次；
// 429 优先按 Retry-After 等待。返回的响应必然非 nil，非 2xx 由调用方转换。
func (c *Client) get(ctx context.Context, url string, query map[string]string) (*resty.Response, error) {
	return c.requestWithPolicy(ctx, http.MethodGet, url, query, nil, retryReadOnly)
}

func (c *Client) getBillable(ctx context.Context, url string, query map[string]string) (*resty.Response, error) {
	return c.requestWithPolicy(ctx, http.MethodGet, url, query, nil, retryBillable)
}

type requestRetryPolicy uint8

const (
	retryNever requestRetryPolicy = iota
	retryReadOnly
	retryBillable
)

// request 提交型请求不重放；调用方若需重试，必须先具备幂等创建或结果核对。
func (c *Client) request(ctx context.Context, method string, url string, query map[string]string, body interface{}) (*resty.Response, error) {
	return c.requestWithPolicy(ctx, method, url, query, body, retryNever)
}

// requestWithPolicy 依据端点费用语义决定是否可重放，不能只看 HTTP 方法。
func (c *Client) requestWithPolicy(ctx context.Context, method string, url string, query map[string]string, body interface{}, policy requestRetryPolicy) (*resty.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*c.timeout+2*time.Second)
		defer cancel()
	}
	for attempt := 0; ; attempt++ {
		request := c.http.R().
			SetContext(ctx).
			SetQueryParams(query)
		if c.apiKey != "" {
			request.SetHeader("Authorization", "Bearer "+c.apiKey)
		}
		if body != nil {
			request.SetHeader("Content-Type", "application/json").SetBody(body)
		}
		resp, err := request.Execute(method, url)
		if err != nil {
			// 调用方上下文已结束：不再重试，直接返回其错误
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, contextRequestError(ctxErr)
			}
			timeout := isTimeoutError(err)
			if policy == retryReadOnly && isRetryableTransportError(err) && attempt < c.maxRetries {
				if waitErr := c.wait(ctx, c.retryDelay(attempt, 0, nil)); waitErr != nil {
					return nil, contextRequestError(waitErr)
				}
				continue
			}
			if timeout {
				return nil, fmt.Errorf("%w: %w: %w", ErrNetwork, ErrTimeout, err)
			}
			return nil, fmt.Errorf("%w: %w", ErrNetwork, err)
		}
		if resp == nil {
			return nil, fmt.Errorf("%w: 上游返回空响应", ErrNetwork)
		}
		if resp.IsSuccess() || !retryableStatusForPolicy(policy, resp.StatusCode()) || attempt >= c.maxRetries || errors.Is(newAPIError(resp.StatusCode(), resp.Body(), resp.Header()), ErrInsufficientCredits) {
			return resp, nil
		}
		delay := c.retryDelay(attempt, resp.StatusCode(), resp.Header())
		if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
			return resp, nil
		}
		if waitErr := c.wait(ctx, delay); waitErr != nil {
			return nil, contextRequestError(waitErr)
		}
	}
}

func retryableStatusForPolicy(policy requestRetryPolicy, status int) bool {
	switch policy {
	case retryReadOnly:
		return retryableStatus(status)
	case retryBillable:
		return status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests
	default:
		return false
	}
}

func (c *Client) retryDelay(attempt int, status int, header http.Header) time.Duration {
	delay := retryBackoff(attempt, status, header)
	if status == http.StatusTooManyRequests && retryAfterFromHeader(header) > 0 {
		return delay
	}
	if c.jitter != nil {
		return c.jitter(delay)
	}
	return delay
}

func jitterRetryBackoff(delay time.Duration) time.Duration {
	return delay * time.Duration(80+rand.Intn(41)) / 100
}

func contextRequestError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w: %w", ErrNetwork, ErrTimeout, err)
	}
	return fmt.Errorf("%w: %w", ErrNetwork, err)
}

// wait 退避等待，期间响应上下文取消
func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryableStatus 判断状态码是否可重试：408、425、429、5xx
func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		(status >= 500 && status <= 599)
}

func isRetryableTransportError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// isTimeoutError 判断是否为网络超时（含 http.Client 超时与上下文超时）
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// retryBackoff 计算第 attempt 次重试前的等待时间：
// 429 且带合法 Retry-After 时优先使用该值，否则 500ms → 1s → 2s。
func retryBackoff(attempt int, status int, header http.Header) time.Duration {
	if status == http.StatusTooManyRequests {
		if d := retryAfterFromHeader(header); d > 0 {
			return d
		}
	}
	switch attempt {
	case 0:
		return backoffBase
	case 1:
		return time.Second
	default:
		return 2 * time.Second
	}
}
