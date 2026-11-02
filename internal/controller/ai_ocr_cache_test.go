package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newAIOCRCacheControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:ocr-controller-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.AIOCRCache{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	original := aiOCRCacheDatabase
	aiOCRCacheDatabase = func(c *gin.Context) *gorm.DB { return db.WithContext(databaseRequestContext(c)) }
	t.Cleanup(func() { aiOCRCacheDatabase = original })
	return db
}

func callAIOCRCacheAdmin(t *testing.T, handler gin.HandlerFunc, query string, role string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-search-config/ocr-cache"+query, nil)
	if role != "" {
		c.Set("user", &entity.User{ID: 1, Role: role, Status: "enabled"})
	}
	handler(c)
	return recorder
}

func TestAIOCRCacheAdminEndpointsRequireAdminBeforeDatabaseAccess(t *testing.T) {
	original := aiOCRCacheDatabase
	aiOCRCacheDatabase = func(*gin.Context) *gorm.DB {
		t.Fatal("unauthorized OCR cache request reached the database")
		return nil
	}
	t.Cleanup(func() { aiOCRCacheDatabase = original })
	for _, handler := range []gin.HandlerFunc{AIOCRCacheList, AIOCRCacheStatus, AIOCRCacheImage, ClearAIOCRCache} {
		for role, want := range map[string]int{"": http.StatusUnauthorized, "user": http.StatusForbidden} {
			response := callAIOCRCacheAdmin(t, handler, "", role)
			if response.Code != want {
				t.Fatalf("role=%q: status=%d want=%d body=%s", role, response.Code, want, response.Body.String())
			}
		}
	}
}

func callAIOCRCacheImage(t *testing.T, hash string, role string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/ai-search-config/ocr-cache/entries/"+hash+"/image", nil)
	if role != "" {
		c.Set("user", &entity.User{ID: 1, Role: role, Status: "enabled"})
	}
	c.Params = gin.Params{{Key: "url_hash", Value: hash}}
	AIOCRCacheImage(c)
	return recorder
}

type imageRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn imageRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestAIOCRCacheImageServesStoredImageForAdmins(t *testing.T) {
	db := newAIOCRCacheControllerTestDB(t)
	const storedURL = "https://images.example.test/question.png?token=private%2Fvalue&x=1"
	if err := db.Create(&entity.AIOCRCache{URLHash: strings.Repeat("a", 64), URL: storedURL, ImageSHA256: "sha", OCRText: "识别"}).Error; err != nil {
		t.Fatal(err)
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	original := newAIOCRCacheImageHTTPClient
	t.Cleanup(func() { newAIOCRCacheImageHTTPClient = original })
	newAIOCRCacheImageHTTPClient = func() *http.Client {
		return &http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != storedURL {
				t.Errorf("request URL=%q, want stored URL %q", req.URL, storedURL)
			}
			if req.Header.Get("Referer") != "https://mooc1.chaoxing.com/" || req.Header.Get("Origin") != "https://mooc1.chaoxing.com" || req.Header.Get("User-Agent") != "Mozilla/5.0" {
				t.Errorf("missing source image headers: %+v", req.Header)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(bytes.NewReader(imageBytes.Bytes())), ContentLength: int64(imageBytes.Len()), Request: req}, nil
		})}
	}
	response := callAIOCRCacheImage(t, strings.Repeat("a", 64), "admin")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cache-Control") != "private, no-store" || !bytes.Equal(response.Body.Bytes(), imageBytes.Bytes()) {
		t.Fatalf("image response: status=%d headers=%v body=%d bytes", response.Code, response.Header(), response.Body.Len())
	}
}

func TestAIOCRCacheImageRejectsMissingURLAndUnsafeURL(t *testing.T) {
	db := newAIOCRCacheControllerTestDB(t)
	for _, row := range []entity.AIOCRCache{
		{URLHash: strings.Repeat("d", 64), ImageSHA256: "a", OCRText: "legacy"},
		{URLHash: strings.Repeat("e", 64), URL: "http://127.0.0.1/private", ImageSHA256: "b", OCRText: "unsafe"},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		hash string
		want int
	}{{strings.Repeat("f", 64), http.StatusNotFound}, {strings.Repeat("d", 64), http.StatusNotFound}, {strings.Repeat("e", 64), http.StatusBadRequest}} {
		response := callAIOCRCacheImage(t, test.hash, "admin")
		if response.Code != test.want {
			t.Errorf("hash=%s status=%d want=%d body=%s", test.hash, response.Code, test.want, response.Body.String())
		}
	}
}

func TestAIOCRCacheImageRejectsNonImagesAndOversizedResponses(t *testing.T) {
	db := newAIOCRCacheControllerTestDB(t)
	for _, hash := range []string{strings.Repeat("b", 64), strings.Repeat("c", 64)} {
		if err := db.Create(&entity.AIOCRCache{URLHash: hash, URL: "https://images.example.test/image", ImageSHA256: "sha", OCRText: "识别"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	original := newAIOCRCacheImageHTTPClient
	t.Cleanup(func() { newAIOCRCacheImageHTTPClient = original })
	newAIOCRCacheImageHTTPClient = func() *http.Client {
		return &http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "image") && req.URL.Query().Get("case") == "large" {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: io.NopCloser(strings.NewReader("")), ContentLength: maxAIOCRCacheImageBytes + 1, Request: req}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<html>not an image</html>")), Request: req}, nil
		})}
	}
	for _, test := range []struct {
		hash string
		want int
	}{{strings.Repeat("b", 64), http.StatusUnsupportedMediaType}, {strings.Repeat("c", 64), http.StatusUnsupportedMediaType}} {
		response := callAIOCRCacheImage(t, test.hash, "admin")
		if response.Code != test.want {
			t.Errorf("hash=%s status=%d want=%d", test.hash[:1], response.Code, test.want)
		}
	}
	if err := db.Model(&entity.AIOCRCache{}).Where("url_hash = ?", strings.Repeat("c", 64)).Update("url", "https://images.example.test/image?case=large").Error; err != nil {
		t.Fatal(err)
	}
	response := callAIOCRCacheImage(t, strings.Repeat("c", 64), "admin")
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized response status=%d want=%d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestAIOCRCacheListReturnsCamelCaseEntriesAndFilteredTotals(t *testing.T) {
	db := newAIOCRCacheControllerTestDB(t)
	updatedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	entries := []entity.AIOCRCache{
		{URLHash: "one", URL: "https://images.example/题图.png?token=A%2FB&x=1&x=2", ImageSHA256: "first", ContentType: "image/png", OCRText: "OCR_100%", UpdatedAt: updatedAt},
		{URLHash: "two", ImageSHA256: "second", ContentType: "image/jpeg", OCRText: "OCR100", UpdatedAt: updatedAt},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	response := callAIOCRCacheAdmin(t, AIOCRCacheList, "?keyword=%25&page=1&pageSize=1", "admin")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Data     []map[string]any `json:"data"`
		Total    int64            `json:"total"`
		Page     int              `json:"page"`
		PageSize int              `json:"pageSize"`
		Summary  struct {
			Matched int64 `json:"matched"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Summary.Matched != 1 || result.Page != 1 || result.PageSize != 1 || len(result.Data) != 1 {
		t.Fatalf("unexpected list: %s", response.Body.String())
	}
	row := result.Data[0]
	if len(row) != 6 || row["urlHash"] != "one" || row["url"] != "https://images.example/题图.png?token=A%2FB&x=1&x=2" || row["imageSha256"] != "first" || row["contentType"] != "image/png" || row["ocrText"] != "OCR_100%" || row["updatedAt"] == nil {
		t.Fatalf("unexpected OCR entry JSON fields: %+v", row)
	}
}

func TestAIOCRCacheAdminInvalidFiltersReturnBadRequest(t *testing.T) {
	db := newAIOCRCacheControllerTestDB(t)
	for _, query := range []string{
		"?updatedFrom=not-a-date",
		"?updatedTo=2026-02-30",
		"?updatedFrom=2026-11-03&updatedTo=2026-11-01",
	} {
		for _, handler := range []gin.HandlerFunc{AIOCRCacheList, ClearAIOCRCache} {
			response := callAIOCRCacheAdmin(t, handler, query, "admin")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("query=%s status=%d body=%s", query, response.Code, response.Body.String())
			}
		}
	}
	if err := db.Migrator().DropTable(&entity.AIOCRCache{}); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []gin.HandlerFunc{AIOCRCacheList, ClearAIOCRCache} {
		response := callAIOCRCacheAdmin(t, handler, "?updatedFrom=not-a-date", "admin")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("missing table must not bypass validation: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestAIOCRCacheListRejectsInvalidPagination(t *testing.T) {
	newAIOCRCacheControllerTestDB(t)
	for _, query := range []string{"?page=no", "?page=0", "?page=-1", "?pageSize=no", "?pageSize=0", "?pageSize=101", "?page=9223372036854775807&pageSize=100"} {
		response := callAIOCRCacheAdmin(t, AIOCRCacheList, query, "admin")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query=%s status=%d body=%s", query, response.Code, response.Body.String())
		}
	}
}

func TestAIOCRCacheAdminDatabaseErrorsReturnInternalServerError(t *testing.T) {
	db := newAIOCRCacheControllerTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []gin.HandlerFunc{AIOCRCacheList, AIOCRCacheStatus, ClearAIOCRCache} {
		response := callAIOCRCacheAdmin(t, handler, "", "admin")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("database failure: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestClearAIOCRCacheEmptyQueryAndMissingTable(t *testing.T) {
	db := newAIOCRCacheControllerTestDB(t)
	if err := db.Create(&entity.AIOCRCache{URLHash: "one", ImageSHA256: "first", OCRText: "OCR"}).Error; err != nil {
		t.Fatal(err)
	}
	response := callAIOCRCacheAdmin(t, ClearAIOCRCache, "?keyword=&contentType=&updatedFrom=&updatedTo=", "admin")
	if response.Code != http.StatusOK || response.Body.String() != `{"deleted":1}` {
		t.Fatalf("empty filters: status=%d body=%s", response.Code, response.Body.String())
	}
	if err := db.Migrator().DropTable(&entity.AIOCRCache{}); err != nil {
		t.Fatal(err)
	}
	response = callAIOCRCacheAdmin(t, ClearAIOCRCache, "?keyword=OCR", "admin")
	if response.Code != http.StatusOK || response.Body.String() != `{"deleted":0}` {
		t.Fatalf("missing table clear: status=%d body=%s", response.Code, response.Body.String())
	}
	response = callAIOCRCacheAdmin(t, AIOCRCacheList, "", "admin")
	var result struct {
		Data []entity.AIOCRCache `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Data == nil || len(result.Data) != 0 {
		t.Fatalf("missing table list: status=%d body=%s", response.Code, response.Body.String())
	}
}
