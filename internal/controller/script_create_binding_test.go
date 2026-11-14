package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestCreateScriptRequiresBindingWithStableHTTPError(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "script-binding-fixture", "user")
	admin := seedControllerUser(t, db, 0, "script-binding-uploader", "admin")
	phone, wechat := "13800000000", "synthetic-script-wechat"
	if err := db.Model(&owner).Updates(map[string]any{"balance": 50, "phone": phone}).Error; err != nil {
		t.Fatal(err)
	}
	// The login projection is stale: both bindings are asserted by the caller
	// context, but the transaction's current account only has its phone.
	owner.Phone, owner.WechatID = &phone, &wechat
	version, err := service.UploadScriptVersionWithDB(db, admin, "fixture", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	router := gin.New()
	router.POST("/api/scripts", func(c *gin.Context) { c.Set("user", &owner) }, CreateScript)
	req := httptest.NewRequest(http.MethodPost, "/api/scripts", strings.NewReader(fmt.Sprintf(
		`{"name":"fixture","baseDomain":"fixture.test","versionId":%d}`, version.ID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "fixture-binding-create")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil ||
		len(payload) != 2 || payload["code"] != "SCRIPT_BINDINGS_REQUIRED" || payload["message"] != "创建脚本前请绑定手机号和微信" {
		t.Error("want only the stable business code and generic message")
	}
}
