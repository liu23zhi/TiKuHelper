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
	previousDescribe := describeLogRetentionCapability
	applyCalls := 0
	resolveLogRetentionDB = func() *gorm.DB { return db }
	loadLogRetentionSettings = service.GetLogRetentionSettingsWithDB
	updateLogRetentionSettings = service.UpdateLogRetentionSettingsWithDB
	describeLogRetentionCapability = service.DescribeLogRetentionCapability
	applyLogRetentionSettings = func(target *gorm.DB) (service.LogRetentionApplyResult, error) {
		applyCalls++
		// 用**真实的** ApplyLogRetentionConfig 语义构造结果，但不碰真实 logger：
		// 这样「单文件上限用不了」这条常驻事实会原样出现在响应里，
		// 断言才有意义（若这里手搓一个干净结果，就测不出传输层有没有丢字段）。
		settings := service.GetLogRetentionSettingsWithDB(target)
		result := service.DescribeLogRetentionCapability(settings)
		return result, nil
	}
	t.Cleanup(func() {
		resolveLogRetentionDB = previousDB
		loadLogRetentionSettings = previousLoad
		updateLogRetentionSettings = previousUpdate
		applyLogRetentionSettings = previousApply
		describeLogRetentionCapability = previousDescribe
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
	applyCalls := installLogSettingsTestDB(t, db)
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
	// GET 的 Apply 是「能力报告」，不是「本次请求应用了」。它必须**有值**：
	// 否则页面刚打开时看不到「单文件上限用不了」，只有点过保存才出现。
	if response.Apply.MaxAgeDays != 30 || response.Apply.MaxTotalBytes != 10*(1<<30) {
		t.Fatalf("GET must carry a capability report for the current settings, got %+v", response.Apply)
	}
	if response.Apply.FileSizeApplied {
		t.Fatal("GET must still report the file size limit as not applied")
	}
	if response.Apply.FileSizeUnsupportedReason == "" {
		t.Fatal("GET must explain why the file size limit cannot be applied")
	}
	// warning 只装本次请求的问题：GET 没有应用动作，所以必须为空。
	// 常驻限制走 Apply.FileSizeUnsupportedReason，两者不能混。
	if response.Warning != "" {
		t.Fatalf("GET has no per-request failure, warning must be empty, got %q", response.Warning)
	}
	// 但仍然必须**不触发 apply**（否则一个 GET 会删日志文件）。
	if *applyCalls != 0 {
		t.Fatalf("GET must not apply anything, got %d calls", *applyCalls)
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
	if response.Apply.FileSizeApplied {
		t.Fatal("file size limit has no setter AND no startup path — it must never report applied")
	}
	if response.Apply.FileSizeUnsupportedReason == "" {
		t.Fatal("PUT must explain why the file size limit cannot be applied")
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
	if response.Apply.MaxAgeDays != 77 {
		t.Fatalf("clean must use the persisted age (77), got %+v", response.Apply)
	}
	if response.Apply.FileSizeUnsupportedReason == "" {
		t.Fatal("clean response must carry the file size limitation too (all three paths)")
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
		// 只注入**本次失败**；其余字段仍由真实能力报告生成，
		// 否则这个桩会让「常驻的单文件上限限制」凭空消失，
		// 用例就变成在测一个现实中不存在的结果形态。
		result := service.DescribeLogRetentionCapability(service.GetLogRetentionSettingsWithDB(db))
		return result, fmt.Errorf("logger is not initialized")
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
	// 两条信息必须**同时**可读，但在不同字段里：
	//   - warning：本次请求的偶发失败（要重试）；
	//   - apply.fileSizeUnsupportedReason：常驻的能力限制（要去找开发）。
	// 若实现把常驻限制塞进 warning 或反之，前端就分不清该重试还是该报障。
	if response.Apply.FileSizeUnsupportedReason == "" {
		t.Fatal("PUT response must still carry the standing file size limitation in apply")
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
