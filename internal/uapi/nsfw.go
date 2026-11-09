package uapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"strings"
)

const (
	// DefaultImageNSFWURL 是文档给出的敏感图片检测完整地址。
	DefaultImageNSFWURL = "https://uapis.cn/api/v1/image/nsfw"
	// MaxImageNSFWBytes 是文档给出的最大上传大小（20 MB）。
	MaxImageNSFWBytes = 20 * 1024 * 1024
)

// ImageNSFWRequest 是服务端上传一张图片做敏感内容检测的请求。
// 只支持本地字节（file 字段）：url 字段会让上游替我们去抓地址，
// 等于把内网可达性判断交给第三方，因此本实现不暴露 url 模式。
type ImageNSFWRequest struct {
	Image     []byte
	ImageName string
}

// ImageNSFWResponse 保留文档列出的全部字段与未知字段原文（RawJSON）。
//
// 注意 IsNSFW 用指针：上游万一不返回该字段，必须能与「明确返回 false」区分开 ——
// 前者按未检测处理（fail-closed），后者才可能放行。
type ImageNSFWResponse struct {
	NSFWScore       *float64 `json:"nsfw_score,omitempty"`
	NormalScore     *float64 `json:"normal_score,omitempty"`
	IsNSFW          *bool    `json:"is_nsfw,omitempty"`
	Label           string   `json:"label,omitempty"`
	Suggestion      string   `json:"suggestion,omitempty"`
	RiskLevel       string   `json:"risk_level,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	InferenceTimeMS int64    `json:"inference_time_ms,omitempty"`
	RawJSON         string   `json:"-"`
}

// SetImageNSFWURL 覆盖检测端点，仅用于测试或受控代理；生产用文档完整地址。
func (c *Client) SetImageNSFWURL(address string) *Client {
	if c != nil {
		c.imageNSFWURL = strings.TrimSpace(address)
	}
	return c
}

func (c *Client) imageNSFWAddress() string {
	if c != nil && strings.TrimSpace(c.imageNSFWURL) != "" {
		return strings.TrimSpace(c.imageNSFWURL)
	}
	return DefaultImageNSFWURL
}

// ImageNSFW 上传一张图片做敏感检测。
//
// 语义与 ImageOCR 一致：这是**可能计费的 POST**，本方法只尝试一次，重试编排交给
// 调用方（工单附件的重试由 service 层按 attempt_count 做退避，不再叠加内部重试）。
func (c *Client) ImageNSFW(ctx context.Context, request ImageNSFWRequest) (ImageNSFWResponse, error) {
	if err := c.ensureReady(); err != nil {
		return ImageNSFWResponse{}, err
	}
	if !strings.HasPrefix(c.apiKey, "uapi-") || len(c.apiKey) <= len("uapi-") {
		return ImageNSFWResponse{}, fmt.Errorf("%w: uapi 密钥格式无效", ErrUnauthorized)
	}
	if err := validateImageNSFWRequest(request); err != nil {
		return ImageNSFWResponse{}, err
	}

	filename := sanitizeImageNSFWName(request.ImageName)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return ImageNSFWResponse{}, fmt.Errorf("%w: 创建检测文件字段失败: %w", ErrNetwork, err)
	}
	if _, err := part.Write(request.Image); err != nil {
		return ImageNSFWResponse{}, fmt.Errorf("%w: 写入检测图片失败: %w", ErrNetwork, err)
	}
	if err := writer.Close(); err != nil {
		return ImageNSFWResponse{}, fmt.Errorf("%w: 完成检测 multipart 请求失败: %w", ErrNetwork, err)
	}

	response, err := c.postMultipart(ctx, c.imageNSFWAddress(), body.Bytes(), writer.FormDataContentType())
	if err != nil {
		return ImageNSFWResponse{}, err
	}
	if err := checkResponse(response); err != nil {
		return ImageNSFWResponse{}, err
	}

	raw := append([]byte(nil), response.Body()...)
	var result ImageNSFWResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return ImageNSFWResponse{}, fmt.Errorf("%w: 解析敏感图片检测响应失败: %w", ErrUpstream, err)
	}
	result.RawJSON = string(raw)
	// 200 但没有判定字段 = 上游没给出结论。这里返回错误，让调用方走 fail-closed，
	// 绝不能因为「HTTP 200」就把图片当成正常图片放行。
	if result.IsNSFW == nil && strings.TrimSpace(result.Suggestion) == "" && strings.TrimSpace(result.RiskLevel) == "" {
		return ImageNSFWResponse{}, fmt.Errorf("%w: 检测响应缺少判定字段", ErrUpstream)
	}
	return result, nil
}

func validateImageNSFWRequest(request ImageNSFWRequest) error {
	if len(request.Image) == 0 {
		return fmt.Errorf("%w: 检测图片不能为空", ErrInvalidArgument)
	}
	if len(request.Image) > MaxImageNSFWBytes {
		return fmt.Errorf("%w: 检测图片不能超过 %d 字节", ErrInvalidArgument, MaxImageNSFWBytes)
	}
	name := sanitizeImageNSFWName(request.ImageName)
	ext := strings.ToLower(extOf(name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
	default:
		return fmt.Errorf("%w: 检测图片格式必须为 jpg、jpeg、png、gif 或 webp", ErrInvalidArgument)
	}
	return nil
}

func sanitizeImageNSFWName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "image.png"
	}
	// 只保留扩展名判断所需的安全片段，避免把调用方的路径带进 multipart。
	base := name
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	if base == "" || base == "." || base == ".." {
		return "image.png"
	}
	return base
}

func extOf(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx:]
	}
	return ""
}
