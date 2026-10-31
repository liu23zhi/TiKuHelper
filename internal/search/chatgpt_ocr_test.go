package search

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAISearchOCRUsesDownloadedBytesAndKeepsVisionParts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		if r.URL.Path == "/missing.png" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(makeTransparentFormulaImage(48, 22))
	}))
	defer server.Close()

	var ocrURLs []string
	client := &ChatGptClient{
		OCRAPI: "https://uapis.cn/api/v1/image/ocr",
		OCRProvider: func(_ context.Context, sourceURL, imageName, contentType string, body []byte) (string, error) {
			ocrURLs = append(ocrURLs, sourceURL)
			if imageName != "first.png" || contentType != "image/png" || len(body) == 0 {
				t.Fatalf("unexpected OCR upload metadata: name=%q type=%q bytes=%d", imageName, contentType, len(body))
			}
			return "识别出的公式", nil
		},
	}
	parts, calls := client.buildAISearchPromptPartsWithOCR(context.Background(), model.SearchRequest{
		Question: "题目 " + server.URL + "/first.png " + server.URL + "/missing.png",
	}, server.Client())

	imageParts := 0
	joinedText := strings.Builder{}
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL != nil {
			imageParts++
		}
		if part.Type == "text" {
			joinedText.WriteString(part.Text)
		}
	}
	if imageParts != 1 {
		t.Fatalf("expected one successful vision image, got %d", imageParts)
	}
	if !strings.Contains(joinedText.String(), "识别出的公式") || !strings.Contains(joinedText.String(), "图二下载失败") {
		t.Fatalf("OCR/reference text mismatched: %s", joinedText.String())
	}
	if len(calls) != 1 || calls[0].Status != "success" || len(ocrURLs) != 1 || ocrURLs[0] != server.URL+"/first.png" {
		t.Fatalf("unexpected OCR usage: calls=%+v urls=%v", calls, ocrURLs)
	}
}

func TestAISearchMissingAllImagesStillLeavesTheModelAQuestion(t *testing.T) {
	client := &ChatGptClient{}
	parts, calls := client.buildAISearchPromptPartsWithOCR(context.Background(), model.SearchRequest{
		Question: "请判断题目 https://images.example/question.png，选项 A https://images.example/a.png",
	}, nil)
	if len(calls) != 0 {
		t.Fatalf("missing images must not create OCR calls, got %+v", calls)
	}
	var prompt strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			prompt.WriteString(part.Text)
		}
	}
	text := prompt.String()
	if !strings.Contains(text, "图一下载失败") || !strings.Contains(text, "图二下载失败") || !strings.Contains(text, "不要仅因缺图返回不确定") {
		t.Fatalf("missing-image guidance was not preserved: %s", text)
	}
}

func TestPrepareAISearchImageAttachmentConvertsBMPToPNG(t *testing.T) {
	prepared, contentType := prepareAISearchImageAttachment(makeBMPImage(2, 2), "image/bmp")
	if contentType != "image/png" {
		t.Fatalf("content type = %q", contentType)
	}
	decoded, _, err := image.Decode(bytes.NewReader(prepared))
	if err != nil {
		t.Fatalf("converted PNG does not decode: %v", err)
	}
	if decoded.Bounds().Dx() < 2 || decoded.Bounds().Dy() < 2 {
		t.Fatalf("converted bounds = %v", decoded.Bounds())
	}
}

func TestAISearchOCRSkipsHTMLAttachments(t *testing.T) {
	client := &ChatGptClient{OCRProvider: func(_ context.Context, _, _, contentType string, _ []byte) (string, error) {
		if contentType != "image/png" {
			t.Fatalf("OCR received non-image content type %q", contentType)
		}
		return "图中文字", nil
	}}
	text, calls := client.runAISearchImageOCR(context.Background(), []downloadedAISearchAttachment{
		{sourceURL: "https://example.com/page", contentType: "text/html", body: []byte("<html>")},
		{sourceURL: "https://example.com/image.png", imageName: "image.png", contentType: "image/png", body: []byte("png")},
	})
	if !strings.Contains(text, "图中文字") || len(calls) != 1 {
		t.Fatalf("unexpected OCR result: text=%q calls=%+v", text, calls)
	}
}

func makeBMPImage(width, height int) []byte {
	rowSize := (width*3 + 3) &^ 3
	dataSize := rowSize * height
	fileSize := 14 + 40 + dataSize
	data := make([]byte, fileSize)
	data[0], data[1] = 'B', 'M'
	binary.LittleEndian.PutUint32(data[2:], uint32(fileSize))
	binary.LittleEndian.PutUint32(data[10:], 54)
	binary.LittleEndian.PutUint32(data[14:], 40)
	binary.LittleEndian.PutUint32(data[18:], uint32(width))
	binary.LittleEndian.PutUint32(data[22:], uint32(height))
	binary.LittleEndian.PutUint16(data[26:], 1)
	binary.LittleEndian.PutUint16(data[28:], 24)
	binary.LittleEndian.PutUint32(data[34:], uint32(dataSize))
	for i := 54; i < len(data); i++ {
		data[i] = 0xFF
	}
	return data
}
