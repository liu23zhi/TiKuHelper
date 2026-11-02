package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	for _, handler := range []gin.HandlerFunc{AIOCRCacheList, AIOCRCacheStatus, ClearAIOCRCache} {
		for role, want := range map[string]int{"": http.StatusUnauthorized, "user": http.StatusForbidden} {
			response := callAIOCRCacheAdmin(t, handler, "", role)
			if response.Code != want {
				t.Fatalf("role=%q: status=%d want=%d body=%s", role, response.Code, want, response.Body.String())
			}
		}
	}
}

func TestAIOCRCacheListReturnsCamelCaseEntriesAndFilteredTotals(t *testing.T) {
	db := newAIOCRCacheControllerTestDB(t)
	updatedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	entries := []entity.AIOCRCache{
		{URLHash: "one", ImageSHA256: "first", ContentType: "image/png", OCRText: "OCR_100%", UpdatedAt: updatedAt},
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
	if len(row) != 5 || row["urlHash"] != "one" || row["imageSha256"] != "first" || row["contentType"] != "image/png" || row["ocrText"] != "OCR_100%" || row["updatedAt"] == nil {
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
