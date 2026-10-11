package uapi

import (
	"context"
	"errors"
	"fmt"
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
	// maxRetries 单个业务请求最多重试次数（不含首次请求），共 4 次尝试
	maxRetries = 3
	// backoffBase 第 1 次重试的退避时间，其后 1s、2s
	backoffBase = 500 * time.Millisecond
)

// Client uapis.cn 客户端。
// 密钥由调用方通过 NewClient 注入，仅用于 Authorization 请求头，
// 不硬编码、不写入 URL、不落盘。
type Client struct {
	apiKey       string
	timeout      time.Duration
	phoneInfoURL string
	ipInfoURL    string
	http         *resty.Client
	// sleep 退避等待实现，测试注入后可跳过真实等待
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient 创建 uapi 客户端，apiKey 为 uapis.cn 签发的密钥（uapi- 开头）
func NewClient(apiKey string) *Client {
	c := &Client{
		apiKey:       strings.TrimSpace(apiKey),
		timeout:      defaultTimeout,
		phoneInfoURL: DefaultPhoneInfoURL,
		ipInfoURL:    DefaultIPInfoURL,
	}
	c.http = resty.New().SetTimeout(c.timeout)
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
// 参数不合法时在本地直接返回错误，不发起 HTTP 请求、不消耗积分。
func (c *Client) GetPhoneInfo(ctx context.Context, phone string) (PhoneInfo, error) {
	if err := c.ensureReady(); err != nil {
		return PhoneInfo{}, err
	}
	normalized, err := NormalizePhone(phone)
	if err != nil {
		return PhoneInfo{}, err
	}
	resp, err := c.get(ctx, c.phoneInfoAddress(), map[string]string{"phone": normalized})
	if err != nil {
		return PhoneInfo{}, err
	}
	if err := checkResponse(resp); err != nil {
		return PhoneInfo{}, err
	}
	info, err := decodePhoneInfo(resp.Body())
	if err != nil {
		return PhoneInfo{}, fmt.Errorf("%w: 解析手机归属地响应失败: %w", ErrUpstream, err)
	}
	return info, nil
}

// GetIPInfo 查询 IP 归属地。
// source 为空时不下发该 query；唯一合法值为 "commercial"，其它取值在本地拒绝，
// 不发起 HTTP 请求。
func (c *Client) GetIPInfo(ctx context.Context, ip string, source string) (IPInfo, error) {
	if err := c.ensureReady(); err != nil {
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
	query := map[string]string{"ip": normalized}
	if source != "" {
		query["source"] = source
	}
	resp, err := c.get(ctx, c.ipInfoAddress(), query)
	if err != nil {
		return IPInfo{}, err
	}
	if err := checkResponse(resp); err != nil {
		return IPInfo{}, err
	}
	info, err := decodeIPInfo(resp.Body())
	if err != nil {
		return IPInfo{}, fmt.Errorf("%w: 解析 IP 查询响应失败: %w", ErrUpstream, err)
	}
	return info, nil
}

// ensureReady 校验客户端配置，避免密钥缺失时白跑一次请求
func (c *Client) ensureReady() error {
	if c == nil || c.http == nil {
		return fmt.Errorf("%w: uapi 客户端未初始化", ErrUnauthorized)
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
// 仅在 408/429/5xx 与网络超时时重试，最多 maxRetries 次，退避 500ms → 1s → 2s；
// 429 优先按 Retry-After 等待。返回的响应必然非 nil，非 2xx 由调用方转换。
func (c *Client) get(ctx context.Context, url string, query map[string]string) (*resty.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.http.R().
			SetContext(ctx).
			SetHeader("Authorization", "Bearer "+c.apiKey).
			SetQueryParams(query).
			Get(url)
		if err != nil {
			// 调用方上下文已结束：不再重试，直接返回其错误
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("%w: %w", ErrNetwork, ctxErr)
			}
			timeout := isTimeoutError(err)
			if timeout && attempt < maxRetries {
				if waitErr := c.wait(ctx, retryBackoff(attempt, 0, nil)); waitErr != nil {
					return nil, fmt.Errorf("%w: %w", ErrNetwork, waitErr)
				}
				continue
			}
			if timeout {
				return nil, fmt.Errorf("%w: %w: %w", ErrNetwork, ErrTimeout, err)
			}
			// 非超时的网络错误（DNS、连接被拒等）不做重试
			return nil, fmt.Errorf("%w: %w", ErrNetwork, err)
		}
		if resp == nil {
			return nil, fmt.Errorf("%w: 上游返回空响应", ErrNetwork)
		}
		if resp.IsSuccess() || !retryableStatus(resp.StatusCode()) || attempt >= maxRetries {
			return resp, nil
		}
		if waitErr := c.wait(ctx, retryBackoff(attempt, resp.StatusCode(), resp.Header())); waitErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrNetwork, waitErr)
		}
	}
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

// retryableStatus 判断状态码是否可重试：408、429、5xx
func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests ||
		status >= 500
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
