package registry_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/controller"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/pkg/util"
)

func TestQuietSQLiteUserPUTWithRealAuth(t *testing.T) {
	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previousMode) })
	db := registry.NewQuietSQLiteTransactionTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}, &entity.BillingRecord{}); err != nil {
		t.Fatal(err)
	}
	previousQuery, previousLog, previousTiku, previousUser := *dao.Q, dao.Log, dao.Tiku, dao.User
	dao.SetDefault(db)
	t.Cleanup(func() {
		*dao.Q = previousQuery
		dao.Log, dao.Tiku, dao.User = previousLog, previousTiku, previousUser
	})
	admin := entity.User{ID: 1, Username: "http-admin", Password: "admin-hash", Role: "admin", Status: "enabled"}
	regular := entity.User{ID: 2, Username: "http-regular", Password: "regular-hash", Role: "user", Status: "enabled"}
	target := entity.User{ID: 3, Username: "http-target", Password: "target-hash", Role: "user", Status: "enabled", Nickname: "original", Balance: 12.345678}
	if err := db.Create(&[]entity.User{admin, regular, target}).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.PUT("/adapter-service/user/:id", controller.Auth, controller.UpdateUser)
	request := func(operator *entity.User, payload string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/adapter-service/user/3", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if operator != nil {
			req.AddCookie(&http.Cookie{Name: "tiku_token", Value: util.GenerateJwtForUser(operator.ID, operator.Password)})
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}
	const adminPayload = `{"nickname":"updated through HTTP","role":"user","status":"enabled","phone":"13800000000","wechatId":"openid-http-updated"}`
	response := request(&admin, adminPayload)
	if response.Code != http.StatusOK {
		t.Fatalf("admin PUT failed: status=%d body=%s", response.Code, response.Body.String())
	}
	var body entity.User
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.ID != target.ID || body.Nickname != "updated through HTTP" {
		t.Fatalf("unexpected user response: user=%+v err=%v", body, err)
	}
	assertStored := func() {
		t.Helper()
		var stored entity.User
		if err := db.First(&stored, target.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Nickname != "updated through HTTP" || stored.Phone == nil || *stored.Phone != "13800000000" || stored.PhoneBoundAt == nil || stored.WechatID == nil || *stored.WechatID != "openid-http-updated" || stored.WechatBoundAt == nil || stored.Balance != target.Balance {
			t.Fatalf("HTTP update lost binding/profile fields or changed balance: %+v", stored)
		}
	}
	assertStored()
	response = request(&regular, `{"nickname":"forbidden change","role":"admin","status":"disabled","phone":"13900000000","wechatId":"openid-forbidden"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-admin must be forbidden: status=%d body=%s", response.Code, response.Body.String())
	}
	assertStored()
	response = request(nil, adminPayload)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing JWT must fail Auth: status=%d body=%s", response.Code, response.Body.String())
	}
	assertStored()
	var billed int64
	if err := db.Model(&entity.BillingRecord{}).Count(&billed).Error; err != nil || billed != 0 {
		t.Fatalf("profile-only update created a ledger entry: count=%d err=%v", billed, err)
	}
}
