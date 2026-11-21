package controller

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"gorm.io/gorm"
)

func userBanTransferRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	identity := func(c *gin.Context) { c.Set("user", actor) }
	router.POST("/preview", identity, middleware.RequireAdmin, UserBanImportPreviewCreate)
	router.POST("/confirm/:previewId", identity, middleware.RequireAdmin, UserBanImportPreviewConfirm)
	return router
}

func TestUserBanImportConfirmationIsOwnedSingleUseAndUsesServerTargets(t *testing.T) {
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	if err := db.AutoMigrate(&entity.UserBan{}); err != nil {
		t.Fatal(err)
	}
	previous := userBanAdminDB
	userBanAdminDB = func() *gorm.DB { return db }
	t.Cleanup(func() { userBanAdminDB = previous })
	admin := asyncTaskSeedUser(t, db, 9300, "ban-import-admin", "admin")
	other := asyncTaskSeedUser(t, db, 9301, "ban-import-other-admin", "admin")
	asyncTaskSeedUser(t, db, 401, "real-target", "user")
	router := userBanTransferRouter(admin)
	recorder := asyncTaskMultipartRequest(t, router, "/preview", nil, "bans.json", []byte(`[{"user_id":401,"username":"ignored-file-name","reason":"测试"}]`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview=%d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "real-target") || strings.Contains(recorder.Body.String(), "ignored-file-name") {
		t.Fatal(recorder.Body.String())
	}
	var preview struct {
		PreviewID string `json:"previewId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil || preview.PreviewID == "" {
		t.Fatalf("%+v %v", preview, err)
	}
	var count int64
	db.Model(&entity.UserBan{}).Count(&count)
	if count != 0 {
		t.Fatal("preview wrote ban")
	}
	path := "/confirm/" + preview.PreviewID
	if r := asyncTaskRequest(router, "POST", path, `{}`); r.Code != 400 {
		t.Fatalf("missing confirmation=%d", r.Code)
	}
	if r := asyncTaskRequest(userBanTransferRouter(other), "POST", path, `{"confirmed":true}`); r.Code != 404 {
		t.Fatalf("other actor=%d %s", r.Code, r.Body.String())
	}
	sameAdminOtherSession := *admin
	sameAdminOtherSession.LoginSessionID = "another-login-session"
	if r := asyncTaskRequest(userBanTransferRouter(&sameAdminOtherSession), "POST", path, `{"confirmed":true}`); r.Code != 404 {
		t.Fatalf("other session=%d %s", r.Code, r.Body.String())
	}
	if r := asyncTaskRequest(router, "POST", path, `{"confirmed":true,"userId":9301}`); r.Code != 200 {
		t.Fatalf("confirm=%d %s", r.Code, r.Body.String())
	}
	if r := asyncTaskRequest(router, "POST", path, `{"confirmed":true}`); r.Code != 409 {
		t.Fatalf("replay=%d %s", r.Code, r.Body.String())
	}
	var ban entity.UserBan
	if err := db.First(&ban).Error; err != nil || ban.UserID != 401 || ban.BannedBy != 9300 {
		t.Fatalf("ban=%+v %v", ban, err)
	}
	newPreview := func() string {
		t.Helper()
		r := asyncTaskMultipartRequest(t, router, "/preview", nil, "bans.csv", []byte("user_id,reason\n401,test\n"))
		var value struct {
			PreviewID string `json:"previewId"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &value) != nil || value.PreviewID == "" {
			t.Fatalf("new preview=%d %s", r.Code, r.Body.String())
		}
		return value.PreviewID
	}
	expired := newPreview()
	userBanImportPreviews.Lock()
	userBanImportPreviews.items[expired].expiresAt = time.Now().Add(-time.Second)
	userBanImportPreviews.Unlock()
	if r := asyncTaskRequest(router, "POST", "/confirm/"+expired, `{"confirmed":true}`); r.Code != http.StatusGone {
		t.Fatalf("expired=%d %s", r.Code, r.Body.String())
	}
	stale := newPreview()
	if err := db.Model(&entity.User{}).Where("id = ?", 401).Update("username", "renamed-target").Error; err != nil {
		t.Fatal(err)
	}
	if r := asyncTaskRequest(router, "POST", "/confirm/"+stale, `{"confirmed":true}`); r.Code != http.StatusConflict {
		t.Fatalf("changed identity=%d %s", r.Code, r.Body.String())
	}
	if r := asyncTaskMultipartRequest(t, userBanTransferRouter(&entity.User{ID: 401, Role: "user"}), "/preview", nil, "bans.csv", []byte("user_id,reason\n401,test\n")); r.Code != http.StatusForbidden {
		t.Fatalf("ordinary user=%d", r.Code)
	}
	if r := asyncTaskMultipartRequest(t, asyncTaskRouter(admin), "/api/import/tasks", map[string]string{"resource": "user_bans"}, "bans.csv", []byte("user_id,reason\n401,test\n")); r.Code != http.StatusBadRequest {
		t.Fatalf("confirmation bypass=%d %s", r.Code, r.Body.String())
	}
}
