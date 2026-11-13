package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 日志管理分区的三条管理员接口测试（task_doc/107 Q14 → 111 §2.5）。
//
// 全部用真 sqlite 内存库、不 mock service：「保存后回读是新值」「非法值不落库」
// 这类断言只有在真实 GORM 上才有意义。
//
// 唯一被替换的是 applyLogRetentionSettings：真实实现会去改**全局 logger** 的
// 策略并立刻清理当前进程的日志目录 —— 那在控制器测试里既会污染其它用例，
// 又依赖「logger 已初始化」。它替换的是 controller 的注入点，不是 service 逻辑；
// 「配置真的被应用到 logger」由 service/log_retention_test.go 用真 logger +
// 真目录来钉。

// newLogSettingsControllerTestDB 建控制器测试库（只需 system_settings）。
func newLogSettingsControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	return db
}

// installLogSettingsTestDB 把三条 handler 的数据库取用点与「应用」注入点换成测试桩。
func installLogSettingsTestDB(t *testing.T, db *gorm.DB) *int {
	t.Helper()

	previousDB := resolveLogRetentionDB
	previousLoad := loadLogRetentionSettings
	previousUpdate := updateLogRetentionSettings
	previousApply := applyLogRetentionSettings
	applyCalls := 0
	resolveLogRetentionDB = func() *gorm.DB { return db }
	loadLogRetentionSettings = service.GetLogRetentionSettingsWithDB
	updateLogRetentionSettings = service.UpdateLogRetentionSettingsWithDB
	applyLogRetentionSettings = func(target *gorm.DB) (service.LogRetentionApplyResult, error) {
		applyCalls++
		// 调用真实的「读配置」但不碰 logger：把结果原样回报，
		// 让断言能确认 handler 传的是**库里的**配置而不是请求里的。
		settings := service.GetLogRetentionSettingsWithDB(target)
		return service.LogRetentionApplyResult{
			Enabled:                settings.Enabled,
			Applied:                true,
			MaxAgeDays:             settings.MaxAgeDays,
			MaxTotalBytes:          settings.MaxTotalBytes,
			MaxFileBytes:           settings.MaxFileBytes,
			AgeApplied:             true,
			TotalSizeApplied:       true,
			FileSizePendingRestart: true,
		}, nil
	}
	t.Cleanup(func() {
		resolveLogRetentionDB = previousDB
		loadLogRetentionSettings = previousLoad
		updateLogRetentionSettings = previousUpdate
		applyLogRetentionSettings = previousApply
	})
	return &applyCalls
}

// logSettingsRouter 组装与生产 router.go 完全一致的中间件顺序：
// Auth 注入身份 → middleware.RequireAdmin → handler 内再 requireAdmin。
func logSettingsRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.GET("/adapter-service/admin/log-settings", auth, middleware.RequireAdmin, LogRetentionSettingsDetail)
	router.PUT("/adapter-service/admin/log-settings", auth, middleware.RequireAdmin, UpdateLogRetentionSettings)
	router.POST("/adapter-service/admin/log-settings/clean", auth, middleware.RequireAdmin, CleanLogRetentionNow)
	return router
}

// logSettingsRequest 发一次请求并返回 recorder。
func logSettingsRequest(router *gin.Engine, method string, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

// decodeLogSettingsResponse 解出统一响应体。
func decodeLogSettingsResponse(t *testing.T, recorder *httptest.ResponseRecorder) logRetentionResponse {
	t.Helper()

	var response logRetentionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, recorder.Body.String())
	}
	return response
}

// 默认值必须是 30 天 / 10 GiB / 100 MiB，且响应里带单位口径（Q14⑧）。
func TestLogSettingsDetailReturnsDefaults(t *testing.T) {
	db := newLogSettingsControllerTestDB(t)
	installLogSettingsTestDB(t, db)
	router := logSettingsRouter(&entity.User{ID: 1, Role: "admin"})

	recorder := logSettingsRequest(router, http.MethodGet, "/adapter-service/admin/log-settings", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeLogSettingsResponse(t, recorder)
	if !response.Settings.Enabled {
		t.Fatal("default must be enabled")
	}
	if response.Settings.MaxAgeDays != 30 || response.Settings.MaxTotalGiB != 10 || response.Settings.MaxFileMiB != 100 {
		t.Fatalf("unexpected defaults: %+v", response.Settings)
	}
	if response.Settings.MaxTotalBytes != 10*(1<<30) || response.Settings.MaxFileBytes != 100*(1<<20) {
		t.Fatalf("derived byte values must use 1024-based units: %+v", response.Settings)
	}
	// 单位必须显式下发，否则前端会把 maxTotalGiB 当字节用。
	if !strings.Contains(response.Units, "GiB") || !strings.Contains(response.Units, "MiB") {
		t.Fatalf("units must be spelled out, got %q", response.Units)
	}
	// GET 不应用配置：Apply 必须为空，否则前端会以为「打开页面就清理了一次」。
	if response.Apply != nil {
		t.Fatalf("GET must not apply the config, got %+v", response.Apply)
	}
}

// 保存成功后必须回读新值，并把「单文件上限待重启」如实回报。
func TestLogSettingsUpdatePersistsAndReportsApply(t *testing.T) {
	db := newLogSettingsControllerTestDB(t)
	applyCalls := installLogSettingsTestDB(t, db)
	router := logSettingsRouter(&entity.User{ID: 1, Role: "admin"})

	recorder := logSettingsRequest(router, http.MethodPut, "/adapter-service/admin/log-settings",
		`{"maxAgeDays":45,"maxTotalGiB":20,"maxFileMiB":50,"cleanupIntervalMinutes":15}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if *applyCalls != 1 {
		t.Fatalf("apply must be attempted exactly once, got %d", *applyCalls)
	}
	response := decodeLogSettingsResponse(t, recorder)
	if response.Settings.MaxAgeDays != 45 || response.Settings.MaxTotalGiB != 20 || response.Settings.MaxFileMiB != 50 {
		t.Fatalf("response must echo persisted values: %+v", response.Settings)
	}
	if response.Apply == nil {
		t.Fatal("PUT must report the apply result")
	}
	if !response.Apply.FileSizePendingRestart {
		t.Fatal("file size limit has no runtime setter and must be reported as pending restart")
	}
	if response.Warning != "" {
		t.Fatalf("successful apply must not carry a warning, got %q", response.Warning)
	}
	// 落库校验：直接从库读，不信响应。
	stored := service.GetLogRetentionSettingsWithDB(db)
	if stored.MaxAgeDays != 45 || stored.MaxTotalGiB != 20 || stored.MaxFileMiB != 50 || stored.CleanupIntervalMinutes != 15 {
		t.Fatalf("settings must be persisted, got %+v", stored)
	}
}

// 非法值：400 + 明确原因，且**不落库、不触发应用**。
//
// 三条断言缺一不可：
//   - 状态码证明分类正确（填错是 400，不是 500）；
//   - 文案证明原因可读（不能只回「参数错误」）；
//   - 不应用 + 不落库证明「拒绝」是真的拒绝，不是「写了一半后报错」。
func TestLogSettingsUpdateRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{"age-zero", `{"maxAgeDays":0}`, "日志保留天数"},
		{"age-negative", `{"maxAgeDays":-7}`, "日志保留天数"},
		{"total-zero", `{"maxTotalGiB":0}`, "日志总大小上限"},
		{"total-negative", `{"maxTotalGiB":-1}`, "日志总大小上限"},
		{"file-zero", `{"maxFileMiB":0}`, "日志单文件大小上限"},
		{"file-negative", `{"maxFileMiB":-100}`, "日志单文件大小上限"},
		{"interval-zero", `{"cleanupIntervalMinutes":0}`, "日志清理间隔"},
		{"interval-negative", `{"cleanupIntervalMinutes":-5}`, "日志清理间隔"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			db := newLogSettingsControllerTestDB(t)
			applyCalls := installLogSettingsTestDB(t, db)
			router := logSettingsRouter(&entity.User{ID: 1, Role: "admin"})

			recorder := logSettingsRequest(router, http.MethodPut, "/adapter-service/admin/log-settings", item.body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if !strings.Contains(body.Message, item.wantMessage) {
				t.Fatalf("message must name the offending field (%q), got %q", item.wantMessage, body.Message)
			}
			if *applyCalls != 0 {
				t.Fatalf("rejected update must not apply anything, got %d calls", *applyCalls)
			}
			stored := service.GetLogRetentionSettingsWithDB(db)
			if stored.MaxAgeDays != 30 || stored.MaxTotalGiB != 10 || stored.MaxFileMiB != 100 {
				t.Fatalf("rejected update must not persist anything, got %+v", stored)
			}
		})
	}
}

// 跨字段约束也要走 HTTP 层：总上限装不下两个单文件 → 400。
func TestLogSettingsUpdateRejectsTotalBelowTwoFiles(t *testing.T) {
	db := newLogSettingsControllerTestDB(t)
	applyCalls := installLogSettingsTestDB(t, db)
	router := logSettingsRouter(&entity.User{ID: 1, Role: "admin"})

	recorder := logSettingsRequest(router, http.MethodPut, "/adapter-service/admin/log-settings",
		`{"maxTotalGiB":1,"maxFileMiB":600}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if *applyCalls != 0 {
		t.Fatalf("rejected update must not apply anything, got %d", *applyCalls)
	}
}

// 「立即清理一次」必须用**库里的**配置，忽略请求体。
//
// 若按请求体里的参数清理，就会出现「按钮传了一套、保存的是另一套」，
// 管理员以为改了配置而实际只影响那一次清理。
func TestLogSettingsCleanUsesPersistedConfigAndIgnoresBody(t *testing.T) {
	db := newLogSettingsControllerTestDB(t)
	installLogSettingsTestDB(t, db)
	age := 77
	if _, err := service.UpdateLogRetentionSettingsWithDB(db, service.UpdateLogRetentionSettingsInput{MaxAgeDays: &age}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	router := logSettingsRouter(&entity.User{ID: 1, Role: "admin"})

	recorder := logSettingsRequest(router, http.MethodPost, "/adapter-service/admin/log-settings/clean",
		`{"maxAgeDays":1}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeLogSettingsResponse(t, recorder)
	if response.Apply == nil || response.Apply.MaxAgeDays != 77 {
		t.Fatalf("clean must use the persisted age (77), got %+v", response.Apply)
	}
	// 请求体里的 1 绝不能落库。
	stored := service.GetLogRetentionSettingsWithDB(db)
	if stored.MaxAgeDays != 77 {
		t.Fatalf("clean must not write settings, got maxAgeDays=%d", stored.MaxAgeDays)
	}
}

// 应用失败时：配置已保存 → 仍 200，但必须带 warning（页面提示）且不崩。
//
// 为什么不是 5xx：配置已落库，重启后按新配置生效；返回 500 会让管理员
// 以为保存失败而反复提交同一个请求。
func TestLogSettingsUpdateSucceedsWithWarningWhenApplyFails(t *testing.T) {
	db := newLogSettingsControllerTestDB(t)
	installLogSettingsTestDB(t, db)
	applyLogRetentionSettings = func(*gorm.DB) (service.LogRetentionApplyResult, error) {
		return service.LogRetentionApplyResult{Enabled: true}, fmt.Errorf("logger is not initialized")
	}
	router := logSettingsRouter(&entity.User{ID: 1, Role: "admin"})

	recorder := logSettingsRequest(router, http.MethodPut, "/adapter-service/admin/log-settings",
		`{"maxAgeDays":31}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("apply failure must not fail the save, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeLogSettingsResponse(t, recorder)
	if response.Warning == "" {
		t.Fatal("apply failure must be surfaced as a warning")
	}
	if !strings.Contains(response.Warning, "logger is not initialized") {
		t.Fatalf("warning must carry the underlying cause, got %q", response.Warning)
	}
	stored := service.GetLogRetentionSettingsWithDB(db)
	if stored.MaxAgeDays != 31 {
		t.Fatalf("settings must still be persisted on apply failure, got %d", stored.MaxAgeDays)
	}
}

// 三条接口都是管理员专属：非管理员 403，未登录 401，且都不得触达 service。
func TestLogSettingsRequiresAdmin(t *testing.T) {
	paths := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/adapter-service/admin/log-settings", ""},
		{http.MethodPut, "/adapter-service/admin/log-settings", `{"maxAgeDays":31}`},
		{http.MethodPost, "/adapter-service/admin/log-settings/clean", ""},
	}
	for _, item := range paths {
		t.Run(item.method, func(t *testing.T) {
			db := newLogSettingsControllerTestDB(t)
			applyCalls := installLogSettingsTestDB(t, db)

			// 普通用户：被 RequireAdmin 中间件挡下。
			router := logSettingsRouter(&entity.User{ID: 2, Role: "user"})
			recorder := logSettingsRequest(router, item.method, item.path, item.body)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("non-admin must get 403, got %d body=%s", recorder.Code, recorder.Body.String())
			}

			// 未登录：同样 403（RequireAdmin 比 handler 内的 401 先跑）。
			router = logSettingsRouter(nil)
			recorder = logSettingsRequest(router, item.method, item.path, item.body)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("anonymous must get 403, got %d body=%s", recorder.Code, recorder.Body.String())
			}

			if *applyCalls != 0 {
				t.Fatalf("rejected requests must not apply anything, got %d", *applyCalls)
			}
			// 非管理员的 PUT 也绝不能落库。
			stored := service.GetLogRetentionSettingsWithDB(db)
			if stored.MaxAgeDays != 30 {
				t.Fatalf("non-admin must not change settings, got %d", stored.MaxAgeDays)
			}
		})
	}
}

// 请求体坏 JSON → 400，而不是 500 或崩。
func TestLogSettingsUpdateRejectsMalformedBody(t *testing.T) {
	db := newLogSettingsControllerTestDB(t)
	applyCalls := installLogSettingsTestDB(t, db)
	router := logSettingsRouter(&entity.User{ID: 1, Role: "admin"})

	recorder := logSettingsRequest(router, http.MethodPut, "/adapter-service/admin/log-settings", `{"maxAgeDays":`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed json, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if *applyCalls != 0 {
		t.Fatalf("malformed body must not apply anything, got %d", *applyCalls)
	}
}
