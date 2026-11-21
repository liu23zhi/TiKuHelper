package search

import (
	"bytes"
	"context"
	"errors"
	"image"
	"net/http"
	"path"
	"strings"
)

// TestImageOCRBytes 复用图片识别和用量统计，上传文件不经过 URL 下载。
func (in *ChatGptClient) TestImageOCRBytes(ctx context.Context, sourceURL, name string, body []byte) (string, AISearchDebugResult, error) {
	debug := AISearchDebugResult{}
	if in == nil || (in.OCRProvider == nil && in.OCRCacheProvider == nil) {
		return "", debug, errors.New("未配置图片 OCR 工具")
	}
	if len(body) == 0 || len(body) > 5<<20 {
		return "", debug, errors.New("上传图片不能为空且不能超过 5 MiB")
	}
	contentType := http.DetectContentType(body)
	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || !strings.HasPrefix(contentType, "image/") || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return "", debug, errors.New("上传文件不是受支持的图片或图片尺寸过大")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attachment := downloadedAISearchAttachment{sourceIndex: 1, sourceURL: sourceURL, imageName: path.Base(name), contentType: contentType, body: body}
	debug.Usage.Attachments = append(debug.Usage.Attachments, AIAttachmentDiagnostic{SourceIndex: 1, DownloadStatus: "uploaded", OriginalContentType: contentType, DownloadedBytes: len(body), PreparedContentType: contentType, PreparedBytes: len(body)})
	text, call := in.recognizeAISearchImage(ctx, attachment)
	debug.Usage.OCRCalls = append(debug.Usage.OCRCalls, call)
	if call.Status != "success" {
		return text, debug, errors.New(call.Error)
	}
	if strings.TrimSpace(text) == "" {
		return text, debug, errors.New("OCR 未返回识别文本")
	}
	return text, debug, nil
}
