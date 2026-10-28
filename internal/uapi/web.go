package uapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultWebToMarkdownURL is the complete UAPI submission endpoint.
	DefaultWebToMarkdownURL   = "https://uapis.cn/api/v1/web/tomarkdown/async"
	webToMarkdownTotalTimeout = 65 * time.Second
	webToMarkdownPollInterval = 2 * time.Second
)

// WebToMarkdownTask is the response returned by both asynchronous endpoints.
// Result is populated only when Status is completed.
type WebToMarkdownTask struct {
	TaskID      string               `json:"task_id"`
	Status      string               `json:"status"`
	URL         string               `json:"url"`
	Progress    int                  `json:"progress,omitempty"`
	CreatedAt   string               `json:"created_at,omitempty"`
	StartedAt   string               `json:"started_at,omitempty"`
	CompletedAt string               `json:"completed_at,omitempty"`
	Elapsed     float64              `json:"elapsed,omitempty"`
	Duration    float64              `json:"duration,omitempty"`
	Message     string               `json:"message,omitempty"`
	Error       string               `json:"error,omitempty"`
	Result      *WebToMarkdownResult `json:"result,omitempty"`
	RawJSON     string               `json:"-"`
}

type WebToMarkdownResult struct {
	Markdown string `json:"markdown"`
	Size     int64  `json:"size"`
}

// SubmitWebToMarkdown submits a URL and returns the asynchronous task.
// The documented endpoint accepts url as a query parameter, so no invented
// JSON body is sent.
func (c *Client) SubmitWebToMarkdown(ctx context.Context, rawURL string) (WebToMarkdownTask, error) {
	if err := c.ensureSearchReady(); err != nil {
		return WebToMarkdownTask{}, err
	}
	normalized, err := normalizeWebURL(rawURL)
	if err != nil {
		return WebToMarkdownTask{}, err
	}
	response, err := c.request(ctx, http.MethodPost, c.webToMarkdownAddress(), map[string]string{"url": normalized}, nil)
	if err != nil {
		return WebToMarkdownTask{}, err
	}
	if err := checkResponse(response); err != nil {
		return WebToMarkdownTask{}, err
	}
	task, err := parseWebToMarkdownTask(response.Body())
	if err != nil {
		return WebToMarkdownTask{}, err
	}
	if strings.TrimSpace(task.TaskID) == "" {
		return WebToMarkdownTask{}, fmt.Errorf("%w: 网页转 Markdown 响应缺少 task_id", ErrUpstream)
	}
	if task.Status == "" {
		task.Status = "pending"
	}
	return task, nil
}

// GetWebToMarkdownStatus returns the current state for a task.
func (c *Client) GetWebToMarkdownStatus(ctx context.Context, taskID string) (WebToMarkdownTask, error) {
	if err := c.ensureSearchReady(); err != nil {
		return WebToMarkdownTask{}, err
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || strings.ContainsAny(taskID, "/?#") || len(taskID) > 256 {
		return WebToMarkdownTask{}, fmt.Errorf("%w: task_id 不合法", ErrInvalidArgument)
	}
	address := strings.TrimRight(c.webToMarkdownAddress(), "/") + "/" + url.PathEscape(taskID)
	response, err := c.request(ctx, http.MethodGet, address, nil, nil)
	if err != nil {
		return WebToMarkdownTask{}, err
	}
	if err := checkResponse(response); err != nil {
		return WebToMarkdownTask{}, err
	}
	task, err := parseWebToMarkdownTask(response.Body())
	if err != nil {
		return WebToMarkdownTask{}, err
	}
	if task.TaskID == "" {
		task.TaskID = taskID
	}
	return task, nil
}

// ReadWebPage submits and polls one URL. It returns a completed task only;
// failed and timeout tasks are classified as ErrUpstream with their message.
func (c *Client) ReadWebPage(ctx context.Context, rawURL string) (WebToMarkdownTask, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, webToMarkdownTotalTimeout)
	defer cancel()
	task, err := c.SubmitWebToMarkdown(ctx, rawURL)
	if err != nil {
		return WebToMarkdownTask{}, err
	}
	for {
		switch strings.ToLower(strings.TrimSpace(task.Status)) {
		case "completed":
			if task.Result == nil || strings.TrimSpace(task.Result.Markdown) == "" {
				return task, fmt.Errorf("%w: 网页转 Markdown 完成但结果为空", ErrUpstream)
			}
			return task, nil
		case "failed", "timeout":
			message := strings.TrimSpace(task.Error)
			if message == "" {
				message = strings.TrimSpace(task.Message)
			}
			if message == "" {
				message = "网页转 Markdown 任务失败"
			}
			return task, fmt.Errorf("%w: %s", ErrUpstream, message)
		case "pending", "processing", "":
			if err := sleepWebPoll(ctx, webToMarkdownPollInterval); err != nil {
				return WebToMarkdownTask{}, contextRequestError(err)
			}
			task, err = c.GetWebToMarkdownStatus(ctx, task.TaskID)
			if err != nil {
				return WebToMarkdownTask{}, err
			}
		default:
			return task, fmt.Errorf("%w: 未知任务状态 %q", ErrUpstream, task.Status)
		}
	}
}

func sleepWebPoll(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) webToMarkdownAddress() string {
	if c != nil && strings.TrimSpace(c.webToMarkdownURL) != "" {
		return strings.TrimSpace(c.webToMarkdownURL)
	}
	return DefaultWebToMarkdownURL
}

// SetWebToMarkdownURL overrides the complete endpoint for tests/proxies.
func (c *Client) SetWebToMarkdownURL(address string) *Client {
	if c != nil {
		c.webToMarkdownURL = strings.TrimSpace(address)
	}
	return c
}

func parseWebToMarkdownTask(body []byte) (WebToMarkdownTask, error) {
	var task WebToMarkdownTask
	if err := json.Unmarshal(body, &task); err != nil {
		return WebToMarkdownTask{}, fmt.Errorf("%w: 解析网页转 Markdown 响应失败: %w", ErrUpstream, err)
	}
	task.RawJSON = string(body)
	return task, nil
}

// ValidateWebURL applies the same validation used by ReadWebPage before a test
// starts a paid model request.
func ValidateWebURL(raw string) (string, error) {
	return normalizeWebURL(raw)
}

func normalizeWebURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: url 参数是必需的", ErrInvalidArgument)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: url 必须是不含用户信息和片段的 HTTP/HTTPS 地址", ErrInvalidArgument)
	}
	if isPrivateWebHost(parsed.Hostname()) {
		return "", fmt.Errorf("%w: 禁止读取内网或保留地址", ErrInvalidArgument)
	}
	return parsed.String(), nil
}

func isPrivateWebHost(host string) bool {
	lower := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") {
		return true
	}
	ip := net.ParseIP(lower)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
