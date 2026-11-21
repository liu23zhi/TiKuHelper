package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestOCRMultipartRequestBudgetIncludesNonFileFields(t *testing.T) {
	for _, item := range []struct {
		path    string
		limit   int
		handler gin.HandlerFunc
	}{{"/document", service.OCRDocumentMaxBytes, OCRDocumentPreview}, {"/image", service.OCRUploadMaxBytes, OCRImageUpload}} {
		t.Run(item.path, func(t *testing.T) {
			router := gin.New()
			router.POST(item.path, func(c *gin.Context) { c.Set("user", &entity.User{ID: 1, Role: "admin"}) }, item.handler)
			response := asyncTaskMultipartRequest(t, router, item.path, map[string]string{"padding": strings.Repeat("x", item.limit+(128<<10))}, "tiny.docx", []byte("tiny"))
			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("request body was not bounded: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestOCRImageUploadUsesExistingWalletAndCachedOCRBilling(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.AIOCRCache{}, &entity.AIOCRCacheSequence{}); err != nil {
		t.Fatal(err)
	}
	user := seedControllerUser(t, db, 0, "upload-ocr-admin", "admin")
	if err := db.Model(&user).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.SetImageOCRPricePerCallWithDB(db, 0.015); err != nil {
		t.Fatal(err)
	}
	original := newAIToolTestOCRProvider
	t.Cleanup(func() { newAIToolTestOCRProvider = original })
	calls := 0
	newAIToolTestOCRProvider = func(*gorm.DB) searchpkg.AIImageOCRCacheProvider {
		return func(_ context.Context, address, name, contentType string, data []byte) (string, bool, error) {
			calls++
			if name != "document-image.png" || contentType != "image/png" || len(data) == 0 {
				t.Fatalf("invalid provider input: %s %s", name, contentType)
			}
			return "付费上传识别", true, nil
		}
	}
	router := gin.New()
	router.POST("/upload", func(c *gin.Context) { c.Set("user", &user) }, OCRImageUpload)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		response := asyncTaskMultipartRequest(t, router, "/upload", nil, "document-image.png", data.Bytes())
		var body struct {
			Success   bool   `json:"success"`
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK || !body.Success || body.RequestID == "" {
			t.Fatalf("response=%d %s", response.Code, response.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("provider calls=%d", calls)
	}
	var records []entity.BillingRecord
	if err := db.Where("provider = ?", "ai_ocr").Find(&records).Error; err != nil || len(records) != 2 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	for _, record := range records {
		if record.UserID != user.ID || record.Amount != 0.015 {
			t.Fatalf("unbilled cached upload: %+v", record)
		}
	}
	invalid := asyncTaskMultipartRequest(t, router, "/upload", nil, "spoofed.png", []byte("not an image"))
	if invalid.Code != http.StatusOK || calls != 2 {
		t.Fatalf("invalid image executed OCR: %d %s", invalid.Code, invalid.Body.String())
	}
	var count int64
	db.Model(&entity.BillingRecord{}).Where("provider = ?", "ai_ocr").Count(&count)
	if count != 2 {
		t.Fatalf("invalid image billed: %d", count)
	}
}
