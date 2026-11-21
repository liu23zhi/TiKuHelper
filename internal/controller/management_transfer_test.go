package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestManagementExportTasksAcceptJSONAndLocationImports(t *testing.T) {
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.AIOCRCache{}, &entity.AIOCRCacheSequence{}, &entity.Log{}); err != nil {
		t.Fatal(err)
	}
	admin := asyncTaskSeedUser(t, db, 9251, "management-transfer-admin", "admin")
	router := asyncTaskRouter(admin)
	for _, resource := range []string{"logs", "billing", "ip_locations", "phone_locations", "wechat_bindings", "phone_bindings", "ocr_cache"} {
		recorder := asyncTaskRequest(router, "POST", "/api/export/tasks", `{"resource":"`+resource+`","format":"json"}`)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("%s status=%d body=%s", resource, recorder.Code, recorder.Body.String())
		}
	}
	for _, resource := range []string{"ip_locations", "phone_locations"} {
		key := "203.0.113.5"
		if resource == "phone_locations" {
			key = "13812345678"
		}
		recorder := asyncTaskMultipartRequest(t, router, "/api/import/tasks", map[string]string{"resource": resource}, "locations.json", []byte(`[{"lookup_key":"`+key+`","address":"测试"}]`))
		if recorder.Code != http.StatusAccepted || !strings.Contains(recorder.Body.String(), "taskId") {
			t.Fatalf("%s import status=%d body=%s", resource, recorder.Code, recorder.Body.String())
		}
	}
	ocr := asyncTaskMultipartRequest(t, router, "/api/import/tasks", map[string]string{"resource": "ocr_cache"}, "ocr.json", []byte(`[{"url":"https://img.example/a.png","image_sha256":"`+strings.Repeat("a", 64)+`","ocr_text":"识别"}]`))
	if ocr.Code != http.StatusAccepted {
		t.Fatalf("OCR import=%d %s", ocr.Code, ocr.Body.String())
	}
}
