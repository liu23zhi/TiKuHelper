package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

type ocrUploadedImage struct {
	name string
	data []byte
}

func ocrUploadFileHeader(c *gin.Context, limit int, message string) (*multipart.FileHeader, bool) {
	// 在 multipart 解析前限制整包，避免通过额外字段绕过文件大小检查。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(limit+(64<<10)))
	header, err := c.FormFile("file")
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || (err == nil && header.Size > int64(limit)) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": message})
		return nil, false
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": message})
		return nil, false
	}
	return header, true
}

func OCRDocumentPreview(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	header, ok := ocrUploadFileHeader(c, service.OCRDocumentMaxBytes, "请选择不超过 10 MiB 的 DOCX / DOC 文档")
	if !ok {
		return
	}
	stream, err := header.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "无法读取 DOCX 文档"})
		return
	}
	defer stream.Close()
	content, err := io.ReadAll(io.LimitReader(stream, service.OCRDocumentMaxBytes+1))
	if err != nil || len(content) > service.OCRDocumentMaxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"message": "无法读取文档或文档超过 10 MiB"})
		return
	}
	images, err := service.ExtractOCRDocumentImages(header.Filename, content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"images": images, "total": len(images)})
}

// OCRImageUpload 将本地图片送入现有工具诊断的管理员钱包、OCR 计费和缓存确认流程。
func OCRImageUpload(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	header, ok := ocrUploadFileHeader(c, service.OCRUploadMaxBytes, "请选择不超过 5 MiB 的图片")
	if !ok {
		return
	}
	stream, err := header.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "无法读取上传图片"})
		return
	}
	defer stream.Close()
	body, err := io.ReadAll(io.LimitReader(stream, service.OCRUploadMaxBytes+1))
	if err != nil || len(body) > service.OCRUploadMaxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"message": "无法读取图片或图片超过 5 MiB"})
		return
	}
	hash := sha256.Sum256(body)
	address := fmt.Sprintf("https://ocr-upload.invalid/sha256/%s", hex.EncodeToString(hash[:]))
	executeAIToolCall(c, aiToolTestInput{Mode: "direct", Tool: "image_ocr", URL: address}, &ocrUploadedImage{name: filepath.Base(header.Filename), data: body})
}
