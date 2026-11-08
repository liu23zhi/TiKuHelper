package search

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestImageOCRToolTestDownloadsImageAndReportsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(makeTransparentFormulaImage(16, 16))
	}))
	defer server.Close()
	client := &ChatGptClient{OCRAPI: "https://uapis.cn/api/v1/image/ocr", OCRProvider: func(_ context.Context, sourceURL, imageName, contentType string, body []byte) (string, error) {
		if sourceURL != server.URL+"/question.png" || imageName != "question.png" || contentType != "image/png" || len(body) == 0 {
			t.Fatalf("unexpected OCR test input: url=%q name=%q type=%q bytes=%d", sourceURL, imageName, contentType, len(body))
		}
		return "图片中的答案", nil
	}}
	text, debug, err := client.TestImageOCR(context.Background(), server.URL+"/question.png")
	if err != nil || text != "图片中的答案" || len(debug.Usage.OCRCalls) != 1 || debug.Usage.OCRCalls[0].Status != "success" {
		t.Fatalf("unexpected OCR tool-test result: text=%q usage=%+v err=%v", text, debug.Usage, err)
	}
}

func TestImageOCRToolTestRejectsNonImageAndEmptyOCR(t *testing.T) {
	for _, kind := range []string{"html", "empty", "error", "cached"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if kind == "html" {
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte("<html>not an image</html>"))
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(makeTransparentFormulaImage(16, 16))
			}))
			defer server.Close()
			calls := 0
			client := &ChatGptClient{OCRCacheProvider: func(context.Context, string, string, string, []byte) (string, bool, error) {
				calls++
				if kind == "error" {
					return "", false, errors.New("OCR unavailable")
				}
				if kind == "cached" {
					return "缓存文字", true, nil
				}
				return "", false, nil
			}}
			text, debug, err := client.TestImageOCR(t.Context(), server.URL+"/image.png")
			if kind == "cached" {
				if err != nil || text != "缓存文字" || len(debug.Usage.OCRCalls) != 1 || !debug.Usage.OCRCalls[0].Cached {
					t.Fatalf("cached result: %q %+v %v", text, debug, err)
				}
			} else if err == nil {
				t.Fatal("invalid OCR result passed")
			}
			if kind == "html" && calls != 0 {
				t.Fatal("non-image reached OCR")
			}
		})
	}
}

func TestOCRToolCallUsesImageAndRunsSelectedWebTools(t *testing.T) {
	for _, toolName := range []string{"image_ocr", "image_ocr_and_tools"} {
		t.Run(toolName, func(t *testing.T) {
			tool := &diagnosticTestTool{}
			modelCalls, imageCalls, ocrCalls := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					imageCalls++
					if r.URL.Path != "/中文图.png" {
						t.Errorf("image URL changed: %s", r.URL)
					}
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(makeTransparentFormulaImage(16, 16))
					return
				}
				modelCalls++
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if modelCalls == 1 && (!strings.Contains(fmt.Sprint(payload["input"]), "图中文字") || !strings.Contains(fmt.Sprint(payload["input"]), "input_image")) {
					t.Error("model did not receive image and OCR evidence")
				}
				if toolName == "image_ocr_and_tools" && modelCalls == 1 {
					writeAIToolStream(w, "ocr-search", "图中文字")
					return
				}
				if toolName == "image_ocr_and_tools" && modelCalls == 2 {
					writeAIWebReadToolStream(w, "ocr-read", "https://example.com/source")
					return
				}
				if payload["tools"] != nil {
					t.Error("final response should not offer web tools")
				}
				writeAIFinalStream(w)
			}))
			defer server.Close()
			client := &ChatGptClient{Enable: true, BaseURL: server.URL, APIKey: "test-key", Model: "test-model", OCRCacheProvider: func(context.Context, string, string, string, []byte) (string, bool, error) {
				ocrCalls++
				return "图中文字", true, nil
			}}
			if toolName == "image_ocr_and_tools" {
				client.SearchTool = tool
			}
			answers, debug, err := client.TestToolCall(t.Context(), toolName, server.URL+"/中文图.png?x=1&y=2")
			if err != nil || len(answers) == 0 || imageCalls != 1 || ocrCalls != 1 || len(debug.Usage.OCRCalls) != 1 || !debug.Usage.OCRCalls[0].Cached {
				t.Fatalf("OCR test failed: answers=%v images=%d ocr=%d usage=%+v err=%v", answers, imageCalls, ocrCalls, debug.Usage, err)
			}
			wantCalls, wantWeb := 1, 0
			if toolName == "image_ocr_and_tools" {
				wantCalls, wantWeb = 3, 1
			}
			if modelCalls != wantCalls || tool.searches != wantWeb || tool.reads != wantWeb {
				t.Fatalf("unexpected calls: model=%d tool=%+v", modelCalls, tool)
			}
		})
	}
}

func TestAISearchOCRReportsCachedProviderResultsWithDownloadedImage(t *testing.T) {
	client := &ChatGptClient{OCRCacheProvider: func(context.Context, string, string, string, []byte) (string, bool, error) {
		return "缓存的识别结构", true, nil
	}}
	text, calls := client.runAISearchImageOCR(context.Background(), []downloadedAISearchAttachment{
		{sourceURL: "https://images.example/cached.png", imageName: "cached.png", contentType: "image/png", body: []byte("image")},
	})
	if !strings.Contains(text, "缓存的识别结构") || len(calls) != 1 || calls[0].Status != "success" || !calls[0].Cached {
		t.Fatalf("cached OCR result was not included or marked: text=%q usage=%+v", text, calls)
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

func TestAISearchOCRUsesOriginalAttachmentIndex(t *testing.T) {
	client := &ChatGptClient{OCRProvider: func(_ context.Context, _, _, _ string, _ []byte) (string, error) {
		return "公式文字", nil
	}}
	text, calls := client.runAISearchImageOCR(context.Background(), []downloadedAISearchAttachment{
		{sourceIndex: 1, sourceURL: "https://example.com/page", contentType: "text/html", body: []byte("<html>")},
		{sourceIndex: 2, sourceURL: "https://example.com/question.png", imageName: "question.png", contentType: "image/png", body: []byte("png")},
		{sourceIndex: 4, sourceURL: "https://example.com/choice-b.png", imageName: "choice-b.png", contentType: "image/png", body: []byte("png")},
	})
	if len(calls) != 2 {
		t.Fatalf("expected two OCR calls, got %+v", calls)
	}
	if !strings.Contains(text, "图二 OCR：公式文字") || !strings.Contains(text, "图四 OCR：公式文字") {
		t.Fatalf("OCR text lost original image numbering: %q", text)
	}
	if strings.Contains(text, "图一 OCR") || strings.Contains(text, "图三 OCR") {
		t.Fatalf("OCR text used image-only indexes: %q", text)
	}
}

func TestAISearchOCRReportsCachedProviderResultsWithExplicitMetadata(t *testing.T) {
	client := &ChatGptClient{OCRCacheProvider: func(_ context.Context, _, _, _ string, _ []byte) (string, bool, error) {
		return "缓存的识别结构", true, nil
	}}
	text, calls := client.runAISearchImageOCR(context.Background(), []downloadedAISearchAttachment{{
		sourceURL:   "https://example.com/image.png",
		imageName:   "image.png",
		contentType: "image/png",
		body:        []byte("png"),
	}})
	if !strings.Contains(text, "缓存的识别结构") {
		t.Fatalf("cached OCR text was not added to prompt: %q", text)
	}
	if len(calls) != 1 || calls[0].Status != "success" || !calls[0].Cached {
		t.Fatalf("cached OCR usage was not marked as cached: %+v", calls)
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

func TestAISearchOCRPreservesSourceIndexAfterMissingImage(t *testing.T) {
	client := &ChatGptClient{OCRProvider: func(_ context.Context, _, _, contentType string, _ []byte) (string, error) {
		if contentType != "image/png" {
			return "", fmt.Errorf("unexpected OCR content type: %s", contentType)
		}
		return "second-image", nil
	}}
	text, calls := client.runAISearchImageOCR(t.Context(), []downloadedAISearchAttachment{{
		sourceIndex: 2, sourceURL: "https://example.invalid/second.png", imageName: "second.png", contentType: "image/png", body: []byte("synthetic-png"),
	}})
	if !strings.Contains(text, "图二 OCR：second-image") || strings.Contains(text, "图一 OCR：") {
		t.Fatalf("OCR must retain original image 2 after image 1 failed, got %q", text)
	}
	if len(calls) != 1 || calls[0].ImageURL != "https://example.invalid/second.png" || calls[0].Status != "success" {
		t.Fatalf("source numbering must not change executed OCR usage: %+v", calls)
	}
	t.Log("OCR_SOURCE_INDEX missing_first_image labels=2 executedOCR=1")
}

func TestAISearchOCRPreservesSourceIndexAcrossHTML(t *testing.T) {
	client := &ChatGptClient{OCRProvider: func(_ context.Context, _, imageName, contentType string, _ []byte) (string, error) {
		if contentType != "image/png" {
			return "", fmt.Errorf("non-image reached OCR: %s", contentType)
		}
		return imageName, nil
	}}
	text, calls := client.runAISearchImageOCR(t.Context(), []downloadedAISearchAttachment{
		{sourceIndex: 1, sourceURL: "https://example.invalid/first.png", imageName: "first.png", contentType: "image/png", body: []byte("synthetic-first")},
		{sourceIndex: 2, sourceURL: "https://example.invalid/page", contentType: "text/html", body: []byte("<html>synthetic</html>")},
		{sourceIndex: 3, sourceURL: "https://example.invalid/third.png", imageName: "third.png", contentType: "image/png", body: []byte("synthetic-third")},
	})
	if !strings.Contains(text, "图一 OCR：first.png") || !strings.Contains(text, "图三 OCR：third.png") || strings.Contains(text, "图二 OCR：") {
		t.Fatalf("OCR must retain source indices 1 and 3 when HTML is skipped, got %q", text)
	}
	if len(calls) != 2 || calls[0].ImageURL != "https://example.invalid/first.png" || calls[1].ImageURL != "https://example.invalid/third.png" || calls[0].Status != "success" || calls[1].Status != "success" {
		t.Fatalf("HTML filtering must preserve actual OCR call order and usage: %+v", calls)
	}
	t.Log("OCR_SOURCE_INDEX image_html_image labels=1,3 executedOCR=2")
}

func TestAISearchMCPFallbackReusesOCRPromptAndImageNumbers(t *testing.T) {
	tool := &testAISearchTool{}
	var imageDownloads, missingDownloads, ocrCalls, nativeCalls, mcpCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing.png":
			missingDownloads.Add(1)
			http.NotFound(w, r)
			return
		case "/second.png":
			imageDownloads.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(makeTransparentFormulaImage(16, 16))
			return
		case "/v1/responses":
		default:
			t.Errorf("unexpected synthetic endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("invalid synthetic model request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
			nativeCalls.Add(1)
			writeAIResponsesCompleted(w, "", map[string]any{"input_tokens": 8, "output_tokens": 1, "total_tokens": 9})
			return
		}
		call := mcpCalls.Add(1)
		input, _ := json.Marshal(payload["input"])
		if !strings.Contains(string(input), "图二 OCR：OCR-second-image") || !strings.Contains(string(input), "图一下载失败") || strings.Contains(string(input), "图一 OCR：") {
			t.Errorf("MCP fallback lost native OCR evidence or original image numbers: %s", input)
		}
		if payload["max_output_tokens"] != float64(aiToolMaxOutputTokens) {
			t.Errorf("MCP fallback lost local output cap: %v", payload["max_output_tokens"])
		}
		if call == 1 {
			writeMCPTextStream(w, `{"action":"web_search","arguments":{"query":"OCR evidence"}}`, 10)
			return
		}
		writeMCPTextStream(w, `{"answers":["4"]}`, 20)
	}))
	defer server.Close()
	client := &ChatGptClient{
		Enable: true, BaseURL: server.URL + "/v1", APIKey: "sk-synthetic", Model: "synthetic-model", SearchTool: tool,
		MaxAISearchModelCalls: 3, MaxAISearches: 1, MaxAIWebReads: 1, MaxAISearchRetries: 1,
		OCRProvider: func(_ context.Context, sourceURL, _, _ string, _ []byte) (string, error) {
			ocrCalls.Add(1)
			if sourceURL != server.URL+"/second.png" {
				return "", fmt.Errorf("unexpected synthetic image: %s", sourceURL)
			}
			return "OCR-second-image", nil
		},
	}
	answers, usage, err := client.SearchAnswerWithUsage(model.SearchRequest{Question: "Question " + server.URL + "/missing.png " + server.URL + "/second.png", DeepThinking: true})
	if err != nil || len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "4" {
		t.Fatalf("MCP fallback must still deliver the answer: answers=%v err=%v", answers, err)
	}
	if imageDownloads.Load() != 1 || missingDownloads.Load() != 1 || ocrCalls.Load() != 1 || nativeCalls.Load() != 1 || mcpCalls.Load() != 2 {
		t.Fatalf("fallback must reuse already prepared attachments/OCR, image=%d missing=%d OCR=%d native=%d MCP=%d", imageDownloads.Load(), missingDownloads.Load(), ocrCalls.Load(), nativeCalls.Load(), mcpCalls.Load())
	}
	if usage.Route != AISearchRouteDeepMCP || len(usage.OCRCalls) != 1 || len(usage.SearchCalls) != 1 || len(usage.ModelCalls) != 3 || usage.TotalTokens != 47 {
		t.Fatalf("fallback must preserve executed usage exactly once: %+v", usage)
	}
	t.Log("MCP_OCR_REUSE imageDownloads=1 OCR=1 native=1 MCP=2 models=3 tokens=47 labels=2")
}
