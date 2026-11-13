package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 敏感图片检测管理页的控制器测试（Q1，109-*.md §2.5）。
//
// 用真 sqlite 内存库 + 真实 requireAdmin 中间件，不 mock service：
// 「非管理员拿不到数据」「手动触发绕过额度闸门」这类断言只有在真实链路
// （中间件 → handler → service → 库）上才有意义。

// newSensitiveImageControllerTestDB 建控制器测试库（含两张新表与必要前置表）。
//
// ⚠️ **每个模型单独一次 AutoMigrate**。一次传多个模型时 GORM 会合并建表语句，
// 实测会导致物理表列缺失（表建出来了但缺列，排查时只看到 "no such column"）。
func newSensitiveImageControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:sensitive-image-controller-%s?mode=memory&cache=shared",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	for _, model := range []any{
		&entity.User{},
		&entity.SystemSetting{},
		&entity.TicketAttachment{},
		&entity.SensitiveImageCacheEntry{},
		&entity.SensitiveImageDailyQuota{},
	} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate test db (%T): %v", model, err)
		}
	}
	return db
}

// installSensitiveImageAdminTestDB 把控制器的库句柄指向测试库。
//
// sensitiveImageAdminDB 是包级变量（与 blacklistAdminDB 同构），单测里没有 manager
// 时它返回 nil —— 必须替换掉，否则每个 handler 都会 500。
func installSensitiveImageAdminTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := sensitiveImageAdminDB
	sensitiveImageAdminDB = func(*gin.Context) *gorm.DB { return db }
	t.Cleanup(func() { sensitiveImageAdminDB = previous })
}

// sensitiveImageAdminRouter 组装与 router.go 一致的中间件顺序（Auth 意图 + RequireAdmin）。
func sensitiveImageAdminRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/adapter-service/sensitive-image/entries", auth, middleware.RequireAdmin, SensitiveImageEntryList)
	router.GET("/adapter-service/sensitive-image/quota", auth, middleware.RequireAdmin, SensitiveImageQuotaStatus)
	router.POST("/adapter-service/sensitive-image/quota", auth, middleware.RequireAdmin, SensitiveImageQuotaUpdateHandler)
	router.POST("/adapter-service/sensitive-image/detect", auth, middleware.RequireAdmin, SensitiveImageManualDetect)
	return router
}

// sensitiveImageRequest 发一次请求并返回 recorder。
func sensitiveImageRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

// sensitiveImageSeedActor 建一个指定角色的账户（RequireAdmin 依据 role + status）。
func sensitiveImageSeedActor(t *testing.T, db *gorm.DB, id int32, role string) entity.User {
	t.Helper()
	actor := entity.User{
		ID: id, Username: fmt.Sprintf("SYNTHETIC_SENSITIVE_IMAGE_%d", id), Password: "123456",
		Role: role, Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatalf("create actor: %v", err)
	}
	return actor
}

// 非管理员 / 无身份：四条管理接口全部 403，且**不产生任何写**。
func TestSensitiveImageAdminRejectsNonAdminOnEveryRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSensitiveImageControllerTestDB(t)
	installSensitiveImageAdminTestDB(t, db)
	user := sensitiveImageSeedActor(t, db, 9201, "user")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"list", http.MethodGet, "/adapter-service/sensitive-image/entries", ""},
		{"quota", http.MethodGet, "/adapter-service/sensitive-image/quota?userId=9201", ""},
		{"update", http.MethodPost, "/adapter-service/sensitive-image/quota", `{"dailyUserLimit":5}`},
		{"detect", http.MethodPost, "/adapter-service/sensitive-image/detect", `{"ticketId":1}`},
	}
	for _, actor := range []*entity.User{nil, &user} {
		router := sensitiveImageAdminRouter(actor)
		for _, tc := range cases {
			recorder := sensitiveImageRequest(router, tc.method, tc.path, tc.body)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("%s with actor=%v: expected 403, got %d (%s)",
					tc.name, actor, recorder.Code, recorder.Body.String())
			}
		}
	}
	// 没有任何写发生：额度配置仍是缺省值，缓存与额度表都是空的。
	admin := sensitiveImageSeedActor(t, db, 9202, "admin")
	recorder := sensitiveImageRequest(sensitiveImageAdminRouter(&admin), http.MethodGet,
		"/adapter-service/sensitive-image/quota", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var status struct {
		Enabled        bool `json:"enabled"`
		PerTicketLimit int  `json:"perTicketLimit"`
		DailyUserLimit int  `json:"dailyUserLimit"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode quota status: %v", err)
	}
	if !status.Enabled ||
		status.PerTicketLimit != service.DefaultSensitiveImagePerTicketLimit ||
		status.DailyUserLimit != service.DefaultSensitiveImageDailyUserLimit {
		t.Fatalf("a rejected request must not change the config, got %+v", status)
	}
	var cacheRows int64
	if err := db.Model(&entity.SensitiveImageCacheEntry{}).Count(&cacheRows).Error; err != nil {
		t.Fatalf("count cache: %v", err)
	}
	if cacheRows != 0 {
		t.Fatalf("a rejected request must not write the cache, found %d rows", cacheRows)
	}
}

// 列表：status 筛选、分页、非法 SHA256 拒绝，且回包不含图片字节。
func TestSensitiveImageAdminEntryListFiltersAndPaginates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSensitiveImageControllerTestDB(t)
	installSensitiveImageAdminTestDB(t, db)
	admin := sensitiveImageSeedActor(t, db, 9203, "admin")

	now := time.Date(2026, 11, 13, 12, 0, 0, 0, loggerShanghai())
	rows := []entity.SensitiveImageCacheEntry{
		{ImageSHA256: strings.Repeat("1", 64), Status: entity.SensitiveImageCacheStatusClean, UpdatedAt: now.Add(-2 * time.Hour)},
		{ImageSHA256: strings.Repeat("2", 64), Status: entity.SensitiveImageCacheStatusSensitive, Reason: "上游判定为敏感图片", UpdatedAt: now.Add(-1 * time.Hour)},
		{ImageSHA256: strings.Repeat("3", 64), Status: entity.SensitiveImageCacheStatusPending, UpdatedAt: now},
	}
	for _, row := range rows {
		row := row
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed cache row: %v", err)
		}
	}
	router := sensitiveImageAdminRouter(&admin)

	// 全量：3 条，按 updated_at DESC 排序（最新的 pending 在最前）。
	recorder := sensitiveImageRequest(router, http.MethodGet, "/adapter-service/sensitive-image/entries", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", got)
	}
	var list struct {
		Data     []entity.SensitiveImageCacheEntry `json:"data"`
		Total    int64                             `json:"total"`
		Page     int                               `json:"page"`
		PageSize int                               `json:"pageSize"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, recorder.Body.String())
	}
	if list.Total != 3 || len(list.Data) != 3 {
		t.Fatalf("expected 3 rows, got total=%d len=%d", list.Total, len(list.Data))
	}
	if list.Data[0].Status != entity.SensitiveImageCacheStatusPending {
		t.Fatalf("expected the newest row first, got %q", list.Data[0].Status)
	}

	// status 筛选（「两个 Tab」在服务端就是一个 status 参数）。
	recorder = sensitiveImageRequest(router, http.MethodGet, "/adapter-service/sensitive-image/entries?status=sensitive", "")
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode filtered list: %v", err)
	}
	if list.Total != 1 || list.Data[0].ImageSHA256 != strings.Repeat("2", 64) {
		t.Fatalf("expected the single sensitive row, got total=%d", list.Total)
	}

	// 分页：pageSize=2 取第一页，total 仍是 3。
	recorder = sensitiveImageRequest(router, http.MethodGet, "/adapter-service/sensitive-image/entries?page=1&pageSize=2", "")
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode paged list: %v", err)
	}
	if list.Total != 3 || len(list.Data) != 2 {
		t.Fatalf("expected total 3 with 2 rows on page 1, got total=%d len=%d", list.Total, len(list.Data))
	}

	// 非法 SHA256：400（长度不对 / 非十六进制都不接受）。
	for _, bad := range []string{"abc", strings.Repeat("z", 64)} {
		recorder = sensitiveImageRequest(router, http.MethodGet,
			"/adapter-service/sensitive-image/entries?imageSha256="+bad, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %q, got %d", bad, recorder.Code)
		}
	}

	// 按 SHA256 精确查。
	recorder = sensitiveImageRequest(router, http.MethodGet,
		"/adapter-service/sensitive-image/entries?imageSha256="+strings.Repeat("3", 64), "")
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode sha list: %v", err)
	}
	if list.Total != 1 || list.Data[0].ImageSHA256 != strings.Repeat("3", 64) {
		t.Fatalf("expected the exact sha256 row, got total=%d", list.Total)
	}
}

// 额度状态：不传 userId 只回配置；传了则带回当日已用与剩余。
func TestSensitiveImageAdminQuotaStatusReportsUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSensitiveImageControllerTestDB(t)
	installSensitiveImageAdminTestDB(t, db)
	admin := sensitiveImageSeedActor(t, db, 9204, "admin")
	usage := entity.SensitiveImageDailyQuota{UserID: 9301, QuotaDate: "2026-11-13", UsedCount: 17}
	if err := db.Create(&usage).Error; err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	router := sensitiveImageAdminRouter(&admin)

	recorder := sensitiveImageRequest(router, http.MethodGet,
		"/adapter-service/sensitive-image/quota?userId=9301&date=2026-11-13", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var status struct {
		Enabled        bool  `json:"enabled"`
		PerTicketLimit int   `json:"perTicketLimit"`
		DailyUserLimit int   `json:"dailyUserLimit"`
		UserID         int32 `json:"userId"`
		UsedCount      int32 `json:"usedCount"`
		Remaining      int   `json:"remaining"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode quota status: %v", err)
	}
	if !status.Enabled || status.PerTicketLimit != service.DefaultSensitiveImagePerTicketLimit {
		t.Fatalf("expected the defaults, got %+v", status)
	}
	if status.UsedCount != 17 {
		t.Fatalf("expected usedCount 17, got %d", status.UsedCount)
	}
	// remaining 必须按配置上限算，不能写死 20（写死会让管理员改配置后读数失真）。
	if status.Remaining != service.DefaultSensitiveImageDailyUserLimit-17 {
		t.Fatalf("expected remaining %d, got %d", service.DefaultSensitiveImageDailyUserLimit-17, status.Remaining)
	}

	// 不传 userId：只回配置，不带用户相关字段。
	recorder = sensitiveImageRequest(router, http.MethodGet, "/adapter-service/sensitive-image/quota", "")
	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, ok := raw["usedCount"]; ok {
		t.Fatal("usedCount must be absent when no userId is given")
	}
}

// 额度更新：生效 + 非法值拒绝 + 指针语义（只改传入的键）。
func TestSensitiveImageAdminQuotaUpdateAppliesAndRejectsInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSensitiveImageControllerTestDB(t)
	installSensitiveImageAdminTestDB(t, db)
	admin := sensitiveImageSeedActor(t, db, 9205, "admin")
	router := sensitiveImageAdminRouter(&admin)

	recorder := sensitiveImageRequest(router, http.MethodPost, "/adapter-service/sensitive-image/quota",
		`{"enabled":false,"perTicketLimit":8,"dailyUserLimit":4}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	policy := service.LoadSensitiveImageQuotaPolicyWithDB(db)
	if policy.Enabled || policy.PerTicketLimit != 8 || policy.DailyUserLimit != 4 {
		t.Fatalf("expected the update to take effect, got %+v", policy)
	}

	// 非法值：400，且不改动已有配置。
	recorder = sensitiveImageRequest(router, http.MethodPost, "/adapter-service/sensitive-image/quota",
		`{"perTicketLimit":0}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a zero limit, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	policy = service.LoadSensitiveImageQuotaPolicyWithDB(db)
	if policy.PerTicketLimit != 8 {
		t.Fatalf("an invalid update must not change the stored limit, got %d", policy.PerTicketLimit)
	}

	// 指针语义：只传 enabled 时另外两个键保持不变。
	recorder = sensitiveImageRequest(router, http.MethodPost, "/adapter-service/sensitive-image/quota",
		`{"enabled":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	policy = service.LoadSensitiveImageQuotaPolicyWithDB(db)
	if !policy.Enabled || policy.PerTicketLimit != 8 || policy.DailyUserLimit != 4 {
		t.Fatalf("untouched fields must keep their values, got %+v", policy)
	}
}

// 手动触发：参数校验 + 缺字节时 404（不假装重检成功）。
func TestSensitiveImageAdminManualDetectValidatesInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSensitiveImageControllerTestDB(t)
	installSensitiveImageAdminTestDB(t, db)
	admin := sensitiveImageSeedActor(t, db, 9206, "admin")
	router := sensitiveImageAdminRouter(&admin)

	// 两个寻址方式都不给：400。
	recorder := sensitiveImageRequest(router, http.MethodPost, "/adapter-service/sensitive-image/detect", `{}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when neither imageSha256 nor ticketId is given, got %d", recorder.Code)
	}
	// SHA256 格式非法：400。
	recorder = sensitiveImageRequest(router, http.MethodPost, "/adapter-service/sensitive-image/detect",
		`{"imageSha256":"not-a-hash"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid sha256, got %d", recorder.Code)
	}
	// 合法 SHA256 但库里没有对应附件（缓存表不存字节）：404，**不能**假装重检成功。
	recorder = sensitiveImageRequest(router, http.MethodPost, "/adapter-service/sensitive-image/detect",
		`{"imageSha256":"`+strings.Repeat("a", 64)+`"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when the bytes are unavailable, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	// 工单编号非法：400。
	recorder = sensitiveImageRequest(router, http.MethodPost, "/adapter-service/sensitive-image/detect",
		`{"ticketId":0}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for ticketId=0, got %d", recorder.Code)
	}
	// 工单不存在（无附件）：200 + 全零统计（不是错误，只是没什么可查）。
	recorder = sensitiveImageRequest(router, http.MethodPost, "/adapter-service/sensitive-image/detect",
		`{"ticketId":12345}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 for an empty ticket, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	var result struct {
		TicketID int64 `json:"ticketId"`
		Total    int   `json:"total"`
		Detected int   `json:"detected"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode detect result: %v", err)
	}
	if result.TicketID != 12345 || result.Total != 0 || result.Detected != 0 {
		t.Fatalf("expected an empty result, got %+v", result)
	}
}

// loggerShanghai 让上方的固定时刻不依赖 pkg/logger 的导入顺序。
func loggerShanghai() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}
