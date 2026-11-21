package search

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadedOCRUsesSameProviderAndBillableUsageWithoutDownload(t *testing.T) {
	image := makeTransparentFormulaImage(8, 8)
	calls := 0
	client := &ChatGptClient{OCRCacheProvider: func(_ context.Context, address, name, contentType string, data []byte) (string, bool, error) {
		calls++
		if address != "https://ocr-upload.invalid/image" || name != "picture.png" || contentType != "image/png" || !bytes.Equal(data, image) {
			t.Fatalf("upload changed %s %s %s", address, name, contentType)
		}
		return "识别上传图片", true, nil
	}}
	text, debug, err := client.TestImageOCRBytes(context.Background(), "https://ocr-upload.invalid/image", "picture.png", image)
	if err != nil || text != "识别上传图片" || calls != 1 || len(debug.Usage.OCRCalls) != 1 || !debug.Usage.OCRCalls[0].Cached || debug.Usage.OCRCalls[0].Status != "success" {
		t.Fatalf("text=%s debug=%+v err=%v", text, debug, err)
	}
	if _, _, err := client.TestImageOCRBytes(context.Background(), "https://ocr-upload.invalid/image", "bad.png", []byte("invalid")); err == nil || calls != 1 {
		t.Fatal("invalid image reached OCR")
	}
}

func TestOCRDownloadBlocksPrivateDestinationsAndOversizedImageDimensions(t *testing.T) {
	calls, hits := 0, 0
	image := makeTransparentFormulaImage(8, 8)
	binary.BigEndian.PutUint32(image[16:20], 100_000)
	binary.BigEndian.PutUint32(image[20:24], 100_000)
	binary.BigEndian.PutUint32(image[29:33], crc32.ChecksumIEEE(image[12:29]))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(image)
	}))
	defer server.Close()
	client := &ChatGptClient{OCRProvider: func(context.Context, string, string, string, []byte) (string, error) {
		calls++
		return "unexpected", nil
	}}
	t.Setenv("TIKU_ALLOW_PRIVATE_OUTBOUND", "false")
	if _, _, err := client.TestImageOCR(context.Background(), server.URL+"/image.png"); err == nil || hits != 0 || calls != 0 {
		t.Fatalf("private outbound reached server hits=%d calls=%d err=%v", hits, calls, err)
	}
	t.Setenv("TIKU_ALLOW_PRIVATE_OUTBOUND", "true")
	if _, debug, err := client.TestImageOCR(context.Background(), server.URL+"/image.png"); err == nil || calls != 0 || len(debug.Usage.Attachments) != 1 || debug.Usage.Attachments[0].FailureCode != "image_dimensions" {
		t.Fatalf("oversized image reached OCR calls=%d debug=%+v err=%v", calls, debug, err)
	}
}
