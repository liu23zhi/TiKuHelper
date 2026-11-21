package service

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
)

const OCRBatchMaxImages = 50
const OCRUploadMaxBytes = 5 << 20
const OCRDocumentMaxBytes = 10 << 20
const ocrDocumentExpandedMaxBytes = 20 << 20

type OCRDocumentImage struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Data        []byte `json:"data"`
}

// ExtractOCRDocumentImages 仅提取内嵌图片，不执行文档内容、外部关系或网络请求。
func ExtractOCRDocumentImages(filename string, content []byte) ([]OCRDocumentImage, error) {
	extension := strings.ToLower(path.Ext(filename))
	if extension != ".docx" && extension != ".doc" {
		return nil, fmt.Errorf("请选择 DOCX / DOC 文档，旧版 DOC 请另存为 DOCX")
	}
	if bytes.HasPrefix(content, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return nil, fmt.Errorf("此文件是旧版 DOC 二进制格式，当前无法提取其内嵌图片，请用 Word 或 WPS 另存为 DOCX 后上传")
	}
	if len(content) > OCRDocumentMaxBytes {
		return nil, fmt.Errorf("DOCX 文档不能超过 10 MiB")
	}
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("无法读取 DOCX 文档")
	}
	if len(reader.File) > 2000 {
		return nil, fmt.Errorf("文档内文件数量过多")
	}
	files := make([]*zip.File, 0)
	hasDocument := false
	hasContentTypes := false
	for _, file := range reader.File {
		if file.Name == "word/document.xml" {
			hasDocument = true
		}
		if file.Name == "[Content_Types].xml" {
			hasContentTypes = true
		}
		if strings.HasPrefix(file.Name, "word/media/") && !file.FileInfo().IsDir() {
			files = append(files, file)
		}
	}
	if !hasDocument || !hasContentTypes {
		return nil, fmt.Errorf("文件不是有效的 DOCX 文档")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	if len(files) > OCRBatchMaxImages {
		return nil, fmt.Errorf("单次最多识别 %d 张内嵌图片", OCRBatchMaxImages)
	}
	items := make([]OCRDocumentImage, 0, len(files))
	seen := make(map[[32]byte]bool, len(files))
	total := 0
	for _, file := range files {
		if file.UncompressedSize64 > OCRUploadMaxBytes {
			return nil, fmt.Errorf("内嵌图片 %s 超过 5 MiB", path.Base(file.Name))
		}
		stream, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("无法读取内嵌图片")
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, OCRUploadMaxBytes+1))
		_ = stream.Close()
		if readErr != nil || len(data) > OCRUploadMaxBytes {
			return nil, fmt.Errorf("无法读取内嵌图片或图片超过 5 MiB")
		}
		total += len(data)
		if total > ocrDocumentExpandedMaxBytes {
			return nil, fmt.Errorf("内嵌图片总大小不能超过 20 MiB")
		}
		contentType := http.DetectContentType(data)
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || !strings.HasPrefix(contentType, "image/") || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40_000_000 {
			return nil, fmt.Errorf("文档包含无法识别或尺寸过大的图片：%s", path.Base(file.Name))
		}
		hash := sha256.Sum256(data)
		if seen[hash] {
			continue
		}
		seen[hash] = true
		items = append(items, OCRDocumentImage{Name: path.Base(file.Name), ContentType: contentType, Data: data})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("文档中没有可识别的内嵌图片")
	}
	return items, nil
}
