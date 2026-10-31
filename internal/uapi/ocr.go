package uapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

const (
	// DefaultImageOCRURL is the complete documented UAPI OCR endpoint.
	DefaultImageOCRURL = "https://uapis.cn/api/v1/image/ocr"
	// MaxImageOCRBytes is the documented maximum upload size.
	MaxImageOCRBytes = 10 * 1024 * 1024
)

// ImageOCRRequest describes a server-side image upload to the UAPI OCR
// endpoint. The image is deliberately represented as bytes: callers can add
// the required source headers while downloading the image and then upload the
// exact downloaded bytes without exposing the source URL to UAPI.
type ImageOCRRequest struct {
	Image     []byte
	ImageName string

	NeedLocation   bool
	ReturnMarkdown bool
	EnableCLS      bool
}

type ImageOCRWord struct {
	Words            string          `json:"words"`
	Location         json.RawMessage `json:"location,omitempty"`
	VertexesLocation json.RawMessage `json:"vertexes_location,omitempty"`
	Score            float64         `json:"score,omitempty"`
}

// ImageOCRResponse keeps the documented fields and preserves unknown fields
// through RawJSON so an upstream response extension does not discard data.
type ImageOCRResponse struct {
	Text           string          `json:"text"`
	PlainText      string          `json:"plain_text"`
	Markdown       string          `json:"markdown,omitempty"`
	WordsResult    []ImageOCRWord  `json:"words_result,omitempty"`
	WordsResultNum int             `json:"words_result_num,omitempty"`
	NeedLocation   bool            `json:"need_location"`
	Timing         json.RawMessage `json:"timing,omitempty"`
	Summary        json.RawMessage `json:"summary,omitempty"`
	Image          json.RawMessage `json:"image,omitempty"`
	Lines          json.RawMessage `json:"lines,omitempty"`
	Blocks         json.RawMessage `json:"blocks,omitempty"`
	Pages          json.RawMessage `json:"pages,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"`
	RawJSON        string          `json:"-"`
}

// SetImageOCRURL overrides the OCR endpoint for tests or a controlled proxy.
// Production code uses the complete documented URL by default.
func (c *Client) SetImageOCRURL(address string) *Client {
	if c != nil {
		c.imageOCRURL = strings.TrimSpace(address)
	}
	return c
}

func (c *Client) imageOCRAddress() string {
	if c != nil && strings.TrimSpace(c.imageOCRURL) != "" {
		return strings.TrimSpace(c.imageOCRURL)
	}
	return DefaultImageOCRURL
}

// ImageOCR uploads one locally downloaded image. OCR is a billable POST, so
// this method deliberately performs one attempt and leaves any retry decision
// to the caller after it has established that replay is safe.
func (c *Client) ImageOCR(ctx context.Context, request ImageOCRRequest) (ImageOCRResponse, error) {
	if err := c.ensureReady(); err != nil {
		return ImageOCRResponse{}, err
	}
	if !strings.HasPrefix(c.apiKey, "uapi-") || len(c.apiKey) <= len("uapi-") {
		return ImageOCRResponse{}, fmt.Errorf("%w: uapi 密钥格式无效", ErrUnauthorized)
	}
	if err := validateImageOCRRequest(request); err != nil {
		return ImageOCRResponse{}, err
	}

	filename := sanitizeImageOCRName(request.ImageName)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return ImageOCRResponse{}, fmt.Errorf("%w: 创建 OCR 文件字段失败: %w", ErrNetwork, err)
	}
	if _, err := part.Write(request.Image); err != nil {
		return ImageOCRResponse{}, fmt.Errorf("%w: 写入 OCR 图片失败: %w", ErrNetwork, err)
	}
	if err := writer.WriteField("image_name", filename); err != nil {
		return ImageOCRResponse{}, fmt.Errorf("%w: 写入 OCR 图片名称失败: %w", ErrNetwork, err)
	}
	if err := writer.WriteField("need_location", boolFormValue(request.NeedLocation)); err != nil {
		return ImageOCRResponse{}, fmt.Errorf("%w: 写入 OCR need_location 失败: %w", ErrNetwork, err)
	}
	if err := writer.WriteField("return_markdown", boolFormValue(request.ReturnMarkdown)); err != nil {
		return ImageOCRResponse{}, fmt.Errorf("%w: 写入 OCR return_markdown 失败: %w", ErrNetwork, err)
	}
	if err := writer.WriteField("enable_cls", boolFormValue(request.EnableCLS)); err != nil {
		return ImageOCRResponse{}, fmt.Errorf("%w: 写入 OCR enable_cls 失败: %w", ErrNetwork, err)
	}
	if err := writer.Close(); err != nil {
		return ImageOCRResponse{}, fmt.Errorf("%w: 完成 OCR multipart 请求失败: %w", ErrNetwork, err)
	}

	response, err := c.postMultipart(ctx, c.imageOCRAddress(), body.Bytes(), writer.FormDataContentType())
	if err != nil {
		return ImageOCRResponse{}, err
	}
	if err := checkResponse(response); err != nil {
		return ImageOCRResponse{}, err
	}

	raw := append([]byte(nil), response.Body()...)
	var result ImageOCRResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return ImageOCRResponse{}, fmt.Errorf("%w: 解析 OCR 响应失败: %w", ErrUpstream, err)
	}
	result.RawJSON = string(raw)
	if strings.TrimSpace(result.Text) == "" && strings.TrimSpace(result.PlainText) == "" {
		return ImageOCRResponse{}, fmt.Errorf("%w: OCR 响应缺少 text/plain_text", ErrUpstream)
	}
	if strings.TrimSpace(result.Text) == "" {
		result.Text = result.PlainText
	}
	if strings.TrimSpace(result.PlainText) == "" {
		result.PlainText = result.Text
	}
	return result, nil
}

func (c *Client) postMultipart(ctx context.Context, address string, body []byte, contentType string) (*resty.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*c.timeout+2*time.Second)
		defer cancel()
	}
	request := c.http.R().SetContext(ctx).
		SetHeader("Content-Type", contentType).
		SetHeader("Accept", "application/json").
		SetBody(body)
	if c.apiKey != "" {
		request.SetHeader("Authorization", "Bearer "+c.apiKey)
	}
	response, err := request.Execute(http.MethodPost, address)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, contextRequestError(ctxErr)
		}
		if isTimeoutError(err) {
			return nil, fmt.Errorf("%w: %w: %w", ErrNetwork, ErrTimeout, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrNetwork, err)
	}
	if response == nil {
		return nil, fmt.Errorf("%w: 上游返回空响应", ErrNetwork)
	}
	return response, nil
}

func validateImageOCRRequest(request ImageOCRRequest) error {
	if len(request.Image) == 0 {
		return fmt.Errorf("%w: OCR 图片不能为空", ErrInvalidArgument)
	}
	if len(request.Image) > MaxImageOCRBytes {
		return fmt.Errorf("%w: OCR 图片不能超过 %d 字节", ErrInvalidArgument, MaxImageOCRBytes)
	}
	name := sanitizeImageOCRName(request.ImageName)
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".bmp", ".gif":
	default:
		return fmt.Errorf("%w: OCR 图片格式必须为 jpg、jpeg、png、webp、bmp 或 gif", ErrInvalidArgument)
	}
	return nil
}

func sanitizeImageOCRName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "" || name == "\\" || name == "/" {
		return "image.png"
	}
	return name
}

func boolFormValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
