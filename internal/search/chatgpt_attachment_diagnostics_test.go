package search

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAISearchAttachmentDiagnosticsPreserveSourceAndSendOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<p>reference</p>"))
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/formula.bmp":
			w.Header().Set("Content-Type", "image/bmp")
			_, _ = w.Write(makeBMPImage(2, 2))
		case "/broken.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("broken-image"))
		}
	}))
	defer server.Close()
	client := &ChatGptClient{OCRProvider: func(context.Context, string, string, string, []byte) (string, error) { return "x+y", nil }}
	req := model.SearchRequest{Question: server.URL + "/page?secret=canary " + server.URL + "/missing " + server.URL + "/formula.bmp " + server.URL + "/broken.png"}
	for _, tools := range []bool{false, true} {
		var calls []AIOCRCallUsage
		var diagnostics []AIAttachmentDiagnostic
		if tools {
			_, calls, diagnostics = client.buildAISearchToolPromptPartsWithOCRDetailed(context.Background(), req, server.Client())
		} else {
			_, calls, diagnostics = client.buildAISearchPromptPartsWithOCRDetailed(context.Background(), req, server.Client())
		}
		if len(diagnostics) != 4 || len(calls) != 2 {
			t.Fatalf("tools=%v diagnostics=%+v OCR=%+v", tools, diagnostics, calls)
		}
		for i, diagnostic := range diagnostics {
			if diagnostic.SourceIndex != i+1 {
				t.Fatalf("source order: %+v", diagnostics)
			}
		}
		if diagnostics[0].SentIndex != 1 || diagnostics[1].SentIndex != 0 || diagnostics[2].SentIndex != 2 || diagnostics[3].SentIndex != 3 {
			t.Fatalf("send order: %+v", diagnostics)
		}
		if diagnostics[1].DownloadStatus != "error" || diagnostics[1].FailureCode != "http_status" || diagnostics[1].HTTPStatus != 404 {
			t.Fatalf("missing image: %+v", diagnostics[1])
		}
		converted := diagnostics[2]
		if converted.OriginalContentType != "image/bmp" || converted.DownloadedBytes == 0 || converted.PreparedContentType != "image/png" || converted.PreparedBytes == 0 || converted.Preprocessing != "png_normalized" {
			t.Fatalf("BMP conversion: %+v", converted)
		}
		if diagnostics[3].Preprocessing != "decode_failed" || diagnostics[3].PreparedBytes != len("broken-image") {
			t.Fatalf("fallback: %+v", diagnostics[3])
		}
		if calls[0].SourceIndex != 3 || calls[1].SourceIndex != 4 || calls[0].ContentType != "image/png" || calls[0].ImageBytes == 0 {
			t.Fatalf("OCR metadata: %+v", calls)
		}
		encoded, _ := json.Marshal(diagnostics)
		if strings.Contains(string(encoded), "canary") || strings.Contains(string(encoded), server.URL) {
			t.Fatalf("diagnostics leaked source URL: %s", encoded)
		}
	}
}

func TestDirectOCRDownloadFailureHasDiagnosticsWithoutOCRCall(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	called := false
	client := &ChatGptClient{OCRProvider: func(context.Context, string, string, string, []byte) (string, error) { called = true; return "", nil }}
	_, debug, err := client.TestImageOCR(context.Background(), server.URL+"/missing.png")
	if err == nil || called || len(debug.Usage.OCRCalls) != 0 || len(debug.Usage.Attachments) != 1 || debug.Usage.Attachments[0].SourceIndex != 1 || debug.Usage.Attachments[0].FailureCode != "http_status" {
		t.Fatalf("download failure must not count as OCR: usage=%+v called=%v err=%v", debug.Usage, called, err)
	}
}

func TestAISearchUsageAggregatesAttachmentDiagnostics(t *testing.T) {
	total := AISearchUsage{}
	addAISearchUsage(&total, AISearchUsage{Attachments: []AIAttachmentDiagnostic{{SourceIndex: 3, DownloadStatus: "success"}}})
	if len(total.Attachments) != 1 || total.Attachments[0].SourceIndex != 3 {
		t.Fatalf("lost attachments: %+v", total)
	}
}

func TestAISearchAttachmentDiagnosticsReportOversizeNormalizationFallback(t *testing.T) {
	// A compressed JPEG can fit the download limit while its lossless PNG does
	// not. Decoding success alone must not be reported as PNG normalization.
	source := image.NewRGBA(image.Rect(0, 0, 2048, 1536))
	random := rand.New(rand.NewSource(1))
	for i := 0; i < len(source.Pix); i += 4 {
		source.Pix[i], source.Pix[i+1], source.Pix[i+2], source.Pix[i+3] = byte(random.Intn(256)), byte(random.Intn(256)), byte(random.Intn(256)), 255
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, source, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(encoded.Bytes())
	}))
	defer server.Close()
	_, diagnostic, ok := downloadAISearchAttachmentDetailed(server.Client(), server.URL+"/image.jpg", 1)
	if !ok || diagnostic.Preprocessing != "size_fallback" || diagnostic.PreparedContentType != "image/jpeg" || diagnostic.PreparedBytes != encoded.Len() {
		t.Fatalf("incorrect normalization fallback: ok=%t diagnostic=%+v", ok, diagnostic)
	}
}
