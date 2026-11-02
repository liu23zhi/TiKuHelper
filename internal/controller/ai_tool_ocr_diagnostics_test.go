package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

func TestAIToolOCRReportsOnlyMatchingPersistedCacheWithoutChangingBilling(t *testing.T) {
	for _, scenario := range []string{"saved", "missing", "changed-image", "changed-text", "missing-url", "cleared-after-ocr", "read-failed"} {
		t.Run(scenario, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			if err := db.AutoMigrate(&entity.AIOCRCache{}); err != nil {
				t.Fatal(err)
			}
			user := seedControllerUser(t, db, 0, "ocr-persisted-admin", "admin")
			if err := db.Model(&user).Update("balance", 10).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.SetImageOCRPricePerCallWithDB(db, 0.015); err != nil {
				t.Fatal(err)
			}
			address := "https://images.example/image.png?token=full%2Fvalue&x=1"
			imageBytes := []byte("image fixture")
			urlHash, imageHash := sha256.Sum256([]byte("uapi-image-ocr-plain-v1\x00"+address)), sha256.Sum256(imageBytes)
			stored := entity.AIOCRCache{URLHash: hex.EncodeToString(urlHash[:]), URL: address, ImageSHA256: hex.EncodeToString(imageHash[:]), ContentType: "image/png", OCRText: "识别原文"}
			if scenario == "changed-image" {
				stored.ImageSHA256 = strings.Repeat("f", 64)
			} else if scenario == "changed-text" {
				stored.OCRText = "另一版本文字"
			} else if scenario == "missing-url" {
				stored.URL = ""
			}
			if scenario != "missing" {
				if err := db.Create(&stored).Error; err != nil {
					t.Fatal(err)
				}
			}
			originalOCR, originalDirect := newAIToolTestOCRProvider, runAIImageOCRTest
			t.Cleanup(func() { newAIToolTestOCRProvider, runAIImageOCRTest = originalOCR, originalDirect })
			ocrCalls := 0
			newAIToolTestOCRProvider = func(*gorm.DB) searchpkg.AIImageOCRCacheProvider {
				return func(context.Context, string, string, string, []byte) (string, bool, error) {
					ocrCalls++
					return "识别原文", false, nil
				}
			}
			runAIImageOCRTest = func(ctx context.Context, client *searchpkg.ChatGptClient, target string) (string, searchpkg.AISearchDebugResult, error) {
				text, cached, err := client.OCRCacheProvider(ctx, target, "image.png", "image/png", imageBytes)
				if scenario == "cleared-after-ocr" {
					if _, clearErr := service.ClearAIOCRCacheWithDB(db); clearErr != nil {
						t.Fatal(clearErr)
					}
				} else if scenario == "read-failed" {
					if callbackErr := db.Callback().Query().Before("gorm:query").Register("fail_cache_confirmation", func(tx *gorm.DB) {
						if tx.Statement.Table == entity.TableNameAIOCRCache {
							tx.AddError(errors.New("cache read unavailable"))
						}
					}); callbackErr != nil {
						t.Fatal(callbackErr)
					}
				}
				return text, searchpkg.AISearchDebugResult{Usage: searchpkg.AISearchUsage{OCRCalls: []searchpkg.AIOCRCallUsage{{API: client.OCRAPI, ImageURL: target, Status: "success", TextChars: len([]rune(text)), Cached: cached}}}}, err
			}
			body, _ := json.Marshal(gin.H{"mode": "direct", "tool": "image_ocr", "url": address})
			res := callToolTest(t, user, string(body))
			var response struct {
				Success         bool               `json:"success"`
				OCRCacheEntry   *entity.AIOCRCache `json:"ocrCacheEntry"`
				OCRCacheWarning string             `json:"ocrCacheWarning"`
				RequestID       string             `json:"requestId"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if res.Code != http.StatusOK || !response.Success || ocrCalls != 1 || response.RequestID == "" {
				t.Fatalf("OCR execution changed: status=%d calls=%d body=%s", res.Code, ocrCalls, res.Body)
			}
			if scenario == "saved" {
				if response.OCRCacheEntry == nil || response.OCRCacheEntry.URL != logger.RedactSecrets(address) || response.OCRCacheEntry.URLHash != stored.URLHash || response.OCRCacheEntry.OCRText != stored.OCRText || response.OCRCacheWarning != "" {
					t.Fatalf("stored OCR cache was not confirmed: %s", res.Body)
				}
			} else if response.OCRCacheEntry != nil || response.OCRCacheWarning == "" {
				t.Fatalf("unconfirmed OCR cache was reported saved: %s", res.Body)
			}
			var records []entity.BillingRecord
			if err := db.Where("provider = ?", "ai_ocr").Find(&records).Error; err != nil || len(records) != 1 || records[0].Amount != 0.015 {
				t.Fatalf("cache confirmation changed billing: records=%+v err=%v", records, err)
			}
		})
	}
}

func TestAIToolOCRRejectsInvalidInputAndMissingProvider(t *testing.T) {
	for _, body := range []string{
		`{"mode":"direct","tool":"image_ocr_and_tools","url":"https://example.com/image.png"}`,
		`{"mode":"ai","tool":"image_ocr","target":"keywords"}`,
		`{"mode":"ai","tool":"image_ocr_and_tools","target":"keywords"}`,
		`{"mode":"direct","tool":"image_ocr","url":"file:///image.png"}`,
		`{"mode":"ai","tool":"image_ocr","target":"https://user:password@example.com/image.png"}`,
		`{"mode":"ai","tool":"image_ocr","target":"https://example.com/image.png#fragment"}`,
	} {
		var input aiToolTestInput
		if err := json.Unmarshal([]byte(body), &input); err != nil {
			t.Fatal(err)
		}
		if _, err := input.validate(); err == nil {
			t.Fatalf("invalid OCR input accepted: %s", body)
		}
	}
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "ocr-missing-provider-admin", "admin")
	if err := db.Model(&user).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	original := newAIToolTestOCRProvider
	t.Cleanup(func() { newAIToolTestOCRProvider = original })
	newAIToolTestOCRProvider = func(*gorm.DB) searchpkg.AIImageOCRCacheProvider { return nil }
	res := callToolTest(t, user, `{"mode":"direct","tool":"image_ocr","url":"https://example.com/image.png"}`)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "UApiPro") {
		t.Fatalf("missing OCR provider: %d %s", res.Code, res.Body)
	}
}

func TestAIToolOCRDiagnosticsPreserveResultsAndBilling(t *testing.T) {
	for _, mode := range []string{"direct", "ai", "combined"} {
		t.Run(mode, func(t *testing.T) {
			db := newSearchControllerTestDB(t)
			user := seedControllerUser(t, db, 0, "ocr-"+mode+"-admin", "admin")
			if err := db.Model(&user).Update("balance", 10).Error; err != nil {
				t.Fatal(err)
			}
			if err := service.SetImageOCRPricePerCallWithDB(db, 0.015); err != nil {
				t.Fatal(err)
			}
			seedControllerAIModelPrice(t, db, "tool-model", 1, 0.1, 2)
			originalOCR, originalTool := newAIToolTestOCRProvider, newAIToolTestTool
			originalDirect, originalDebug := runAIImageOCRTest, runAIToolDebug
			t.Cleanup(func() {
				newAIToolTestOCRProvider, newAIToolTestTool = originalOCR, originalTool
				runAIImageOCRTest, runAIToolDebug = originalDirect, originalDebug
			})
			ocrCalls, modelCalls := 0, 0
			newAIToolTestOCRProvider = func(*gorm.DB) searchpkg.AIImageOCRCacheProvider {
				return func(_ context.Context, address, name, contentType string, data []byte) (string, bool, error) {
					ocrCalls++
					if !strings.HasSuffix(address, "/image.png") || contentType != "image/png" || len(data) == 0 {
						t.Errorf("unexpected OCR upload: %q %s %d", address, contentType, len(data))
					}
					return "识别原文", true, nil
				}
			}
			tool := &toolTestStub{}
			newAIToolTestTool = func(*gorm.DB) searchpkg.AISearchTool { return tool }
			// Search-package tests exercise real image downloads and model streams.
			// This fixture isolates controller wiring and billing without relaxing
			// validation of private image URLs or making a public network request.
			runAIImageOCRTest = func(ctx context.Context, client *searchpkg.ChatGptClient, target string) (string, searchpkg.AISearchDebugResult, error) {
				text, cached, err := client.OCRCacheProvider(ctx, target, "image.png", "image/png", []byte("fixture"))
				debug := searchpkg.AISearchDebugResult{Usage: searchpkg.AISearchUsage{OCRCalls: []searchpkg.AIOCRCallUsage{{API: client.OCRAPI, ImageURL: target, Status: "success", TextChars: len([]rune(text)), Cached: cached}}}}
				return text, debug, err
			}
			runAIToolDebug = func(ctx context.Context, client *searchpkg.ChatGptClient, selected, target string) ([][]string, searchpkg.AISearchDebugResult, error) {
				_, debug, err := runAIImageOCRTest(ctx, client, target)
				if err != nil {
					return nil, debug, err
				}
				modelCalls = 1
				if selected == "image_ocr_and_tools" {
					modelCalls = 3
					result, err := client.SearchTool.SearchAggregate(ctx, uapi.SearchAggregateRequest{Query: "图片主题"})
					if err != nil {
						return nil, debug, err
					}
					task, err := client.SearchTool.(searchpkg.AIWebReadTool).ReadWebPage(ctx, result.Results[0].URL)
					if err != nil {
						return nil, debug, err
					}
					debug.Usage.SearchCalls = []searchpkg.AISearchCallUsage{{Status: "success", ResultCount: len(result.Results)}}
					debug.Usage.WebReadCalls = []searchpkg.AIWebReadCallUsage{{Status: "success", MarkdownBytes: len(task.Result.Markdown)}}
				} else if client.SearchTool != nil {
					t.Fatal("OCR-only test unexpectedly enabled web tools")
				}
				for i := 0; i < modelCalls; i++ {
					debug.Usage.ModelCalls = append(debug.Usage.ModelCalls, searchpkg.AIModelCallUsage{Model: client.Model, InputTokens: 100, OutputTokens: 10, TotalTokens: 110})
					debug.Usage.InputTokens += 100
					debug.Usage.OutputTokens += 10
					debug.Usage.TotalTokens += 110
				}
				return [][]string{{"图片内容已识别"}}, debug, nil
			}
			inputMode, toolName := mode, "image_ocr"
			if mode == "combined" {
				inputMode, toolName = "ai", "image_ocr_and_tools"
			}
			body, _ := json.Marshal(gin.H{"mode": inputMode, "tool": toolName, "target": "https://example.com/image.png", "url": "https://example.com/image.png", "baseUrl": "https://model.example.com", "apiKey": "test-private-key", "model": "tool-model"})
			res := callToolTest(t, user, string(body))
			var response struct {
				Success    bool                    `json:"success"`
				Usage      searchpkg.AISearchUsage `json:"usage"`
				ToolResult []struct {
					Tool   string `json:"tool"`
					Text   string `json:"text"`
					Cached bool   `json:"cached"`
				} `json:"toolResult"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if res.Code != http.StatusOK || !response.Success || ocrCalls != 1 || len(response.Usage.OCRCalls) != 1 || !response.Usage.OCRCalls[0].Cached || len(response.ToolResult) == 0 || response.ToolResult[0].Text != "识别原文" || !response.ToolResult[0].Cached {
				t.Fatalf("OCR diagnostic response: %d %s", res.Code, res.Body)
			}
			var records []entity.BillingRecord
			if err := db.Where("provider = ?", "ai_ocr").Find(&records).Error; err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Amount != 0.015 || !strings.Contains(records[0].QuestionText, "图片 OCR") {
				t.Fatalf("cached OCR billing changed: %+v", records)
			}
			wantWeb, wantModel := 0, 0
			if mode == "ai" {
				wantModel = 1
			}
			if mode == "combined" {
				wantWeb, wantModel = 1, 3
			}
			if tool.searches != wantWeb || tool.reads != wantWeb || modelCalls != wantModel {
				t.Fatalf("unexpected calls: model=%d tool=%+v", modelCalls, tool)
			}
		})
	}
}
