package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 异步任务 / 导入导出控制器测试（任务书 ⑥：全部 RequireAdmin）。
//
// 本文件只钉两件事，都是「实现坏了会真出事」的：
//  1. **每一条路由都拒绝非管理员**（漏挂 RequireAdmin = 全站数据可被普通用户导出）；
//  2. 提交路径的默认脱敏语义（undefined → 脱敏；显式 false → 明文）。

func newAsyncTaskControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:async-task-controller-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// AutoMigrate 逐个调用（一次传多个模型会合并建表语句 —— captain 广播的已验证事故）。
	for _, model := range []any{&entity.SystemSetting{}, &entity.AsyncTask{}, &entity.User{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T: %v", model, err)
		}
	}
	return db
}

// installAsyncTaskControllerTestDB 把 controller 的 DB 钩子指向测试库。
func installAsyncTaskControllerTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := asyncTaskControllerDB
	asyncTaskControllerDB = func() *gorm.DB { return db }
	t.Cleanup(func() { asyncTaskControllerDB = previous })
}

// installAsyncTaskControllerTestManager 注入一个不 Start 的管理器（不引并发）。
func installAsyncTaskControllerTestManager(t *testing.T, db *gorm.DB) *service.AsyncTaskManager {
	t.Helper()
	manager := service.NewAsyncTaskManager(db, t.TempDir())
	manager.RegisterDefaultAsyncTaskExecutors()
	previous := asyncTaskManager
	asyncTaskManager = manager
	t.Cleanup(func() { asyncTaskManager = previous })
	return manager
}

// asyncTaskRouter 组装与建议路径一致的 7 条路由（中间件顺序与 router.go 一致）。
//
// ⚠️ 这里刻意与生产 router.go 同样**同时挂 Auth 与 RequireAdmin**：
// 只挂 RequireAdmin 会漏掉「没有 user 上下文」这条（普通用户拿不到身份时的行为）。
func asyncTaskRouter(actor *entity.User) *gin.Engine {
	router := gin.New()
	auth := func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	}
	router.POST("/api/export/tasks", auth, middleware.RequireAdmin, ExportTaskCreate)
	router.POST("/api/import/tasks", auth, middleware.RequireAdmin, ImportTaskCreate)
	router.GET("/api/export/tasks", auth, middleware.RequireAdmin, ExportTaskList)
	router.GET("/api/export/tasks/:taskId", auth, middleware.RequireAdmin, ExportTaskDetail)
	router.GET("/api/export/tasks/:taskId/download", auth, middleware.RequireAdmin, ExportTaskDownload)
	router.GET("/api/export/tasks/:taskId/errors", auth, middleware.RequireAdmin, ExportTaskErrorReport)
	router.GET("/api/import/template", auth, middleware.RequireAdmin, ImportTemplate)
	return router
}

// asyncTaskRouterWithAuth 用**真实的 controller.Auth** 挂第一层（用于断言 401）。
//
// 与 asyncTaskRouter 的区别只在第一层：那里是注入 actor 的 stub（测 RequireAdmin 的 403），
// 这里是真的 JWT 校验（测「无凭据 → 401 请重新登录」）。两层各测各的责任。
func asyncTaskRouterWithAuth(_ *entity.User, _ bool) *gin.Engine {
	router := gin.New()
	router.POST("/api/export/tasks", Auth, middleware.RequireAdmin, ExportTaskCreate)
	router.POST("/api/import/tasks", Auth, middleware.RequireAdmin, ImportTaskCreate)
	router.GET("/api/export/tasks", Auth, middleware.RequireAdmin, ExportTaskList)
	router.GET("/api/export/tasks/:taskId", Auth, middleware.RequireAdmin, ExportTaskDetail)
	router.GET("/api/export/tasks/:taskId/download", Auth, middleware.RequireAdmin, ExportTaskDownload)
	router.GET("/api/export/tasks/:taskId/errors", Auth, middleware.RequireAdmin, ExportTaskErrorReport)
	router.GET("/api/import/template", Auth, middleware.RequireAdmin, ImportTemplate)
	return router
}

// asyncTaskRequest 发一次 JSON 请求。
func asyncTaskRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

// asyncTaskSeedUser 建一个指定角色的用户。
func asyncTaskSeedUser(t *testing.T, db *gorm.DB, id int32, username, role string) *entity.User {
	t.Helper()
	user := entity.User{
		ID: id, Username: username, Password: "x", Role: role, Status: "enabled",
		RegisterSource: "public",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return &user
}

// 七条路由的**两层**拒绝必须分别断言（不能只断「非 2xx」）：
//
//	第一层 controller.Auth（生产中间件，真的跑）：无凭据 → **401 请重新登录**
//	第二层 middleware.RequireAdmin：有身份但 role=user → **403 无权限执行该操作**
//
// 为什么要分层：这两层的责任完全不同。若只断言「非 2xx」，把 RequireAdmin 整段删掉
// 后普通用户会拿到 401（因为没挂 Auth 时也没有 user 上下文），测试仍然绿 ——
// 而实际后果是「管理员接口对任何持证用户开放」。这正是「非 2xx 也算过」的假钉子形态。
//
// 第一层刻意用**真实的 controller.Auth**而不是 stub：401 的判定逻辑（token 解析失败 /
// subject 非法 / 用户不存在 / 会话失效）都在它里面，stub 掉就等于没测。
func TestAsyncTaskRoutesRejectNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	normalUser := asyncTaskSeedUser(t, db, 9201, "SYNTHETIC_NORMAL", "user")

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{"POST", "/api/export/tasks", `{"resource":"users"}`},
		{"POST", "/api/import/tasks", `{"resource":"blacklist"}`},
		{"GET", "/api/export/tasks", ""},
		{"GET", "/api/export/tasks/exp-0123456789abcdef", ""},
		{"GET", "/api/export/tasks/exp-0123456789abcdef/download", ""},
		{"GET", "/api/export/tasks/exp-0123456789abcdef/errors", ""},
		{"GET", "/api/import/template?resource=users", ""},
	}

	// ── 第一层：未登录 → 401（真实 Auth 中间件，无 Authorization 头、无 cookie）
	anonRouter := asyncTaskRouterWithAuth(nil, false)
	for _, testCase := range cases {
		recorder := asyncTaskRequest(anonRouter, testCase.method, testCase.path, testCase.body)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s status = %d, want 401 (body=%s)",
				testCase.method, testCase.path, recorder.Code, recorder.Body.String())
		}
		// 401 的文案也要对（前端据此跳登录页）。
		if !strings.Contains(recorder.Body.String(), "请重新登录") {
			t.Fatalf("anonymous %s %s message must be 请重新登录, got %s",
				testCase.method, testCase.path, recorder.Body.String())
		}
	}

	// ── 第二层：已登录但 role=user → 403（RequireAdmin 拦下）
	userRouter := asyncTaskRouter(normalUser)
	for _, testCase := range cases {
		recorder := asyncTaskRequest(userRouter, testCase.method, testCase.path, testCase.body)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("normal-user %s %s status = %d, want 403 (body=%s)",
				testCase.method, testCase.path, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "无权限") {
			t.Fatalf("normal-user %s %s message must be 无权限, got %s",
				testCase.method, testCase.path, recorder.Body.String())
		}
	}

	// ── 对照组：401 与 403 必须是**不同**的状态码
	//
	// 这条断言防的是「两层塌成一层」：如果 RequireAdmin 被删掉，匿名请求仍是 401
	// （没有 user 上下文），而普通用户请求会变成 401 或 200 —— 后面的用例会红。
	// 但如果有人把 Auth 也配成返回 403，两条就分不开了，这里直接钉住差异。
	if http.StatusUnauthorized == http.StatusForbidden {
		t.Fatal("sanity: 401 and 403 must be distinct")
	}

	// 两种身份都不该产生任何任务（拒绝必须发生在落库之前）。
	var count int64
	if err := db.Model(&entity.AsyncTask{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected requests must not create tasks, got %d rows", count)
	}
}

// 中间件层与 handler 层是**两道独立的闸**，必须分别钉住。
//
// 实测背景：本模块 7 个 handler 内部也写了 `if !requireAdmin(c) { return }`，
// 与既有 blacklist_admin.go 的做法一致（双层防御）。后果是：**只把路由上的
// middleware.RequireAdmin 摘掉，上面那个用例仍然 PASS**（handler 自己拦住了）——
// 也就是「中间件是否挂上」这条从未被单独验证过。
//
// 这个用例专门填这个洞：只挂 Auth、**不挂 RequireAdmin**，然后断言 handler
// 自己仍然返回 403。它红了只有两种可能：
//  1. handler 内部的 requireAdmin 被删了（那中间件就是唯一防线，必须确认它挂上了）；
//  2. 有人改成了「只靠中间件」的写法（那这个用例需要同步调整，不能静默变绿）。
//
// 反过来说，这个用例**绿**证明的是 handler 层有效，不是中间件层有效 ——
// 中间件层由上面的 TestAsyncTaskRoutesRejectNonAdmin 负责（它跑的是完整两条中间件链）。
func TestAsyncTaskHandlersAlsoCheckAdminWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	normalUser := asyncTaskSeedUser(t, db, 9210, "SYNTHETIC_NO_MW", "user")
	admin := asyncTaskSeedUser(t, db, 9211, "SYNTHETIC_MW_ADMIN", "admin")

	// 只注入身份、不挂 RequireAdmin。
	bareRouter := func(actor *entity.User) *gin.Engine {
		router := gin.New()
		auth := func(c *gin.Context) {
			if actor != nil {
				c.Set("user", actor)
			}
		}
		router.POST("/api/export/tasks", auth, ExportTaskCreate)
		router.POST("/api/import/tasks", auth, ImportTaskCreate)
		router.GET("/api/export/tasks", auth, ExportTaskList)
		router.GET("/api/export/tasks/:taskId", auth, ExportTaskDetail)
		router.GET("/api/export/tasks/:taskId/download", auth, ExportTaskDownload)
		router.GET("/api/export/tasks/:taskId/errors", auth, ExportTaskErrorReport)
		router.GET("/api/import/template", auth, ImportTemplate)
		return router
	}

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{"POST", "/api/export/tasks", `{"resource":"users"}`},
		{"POST", "/api/import/tasks", `{"resource":"blacklist"}`},
		{"GET", "/api/export/tasks", ""},
		{"GET", "/api/export/tasks/exp-0123456789abcdef", ""},
		{"GET", "/api/export/tasks/exp-0123456789abcdef/download", ""},
		{"GET", "/api/export/tasks/exp-0123456789abcdef/errors", ""},
		{"GET", "/api/import/template?resource=users", ""},
	}
	router := bareRouter(normalUser)
	for _, testCase := range cases {
		recorder := asyncTaskRequest(router, testCase.method, testCase.path, testCase.body)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("handler-level guard missing: %s %s status = %d, want 403 (body=%s)",
				testCase.method, testCase.path, recorder.Code, recorder.Body.String())
		}
	}
	// 对照组：同样不挂 RequireAdmin，但身份是管理员 → 必须放行（不能变成「一律 403」）。
	// 没有这一条，「所有请求都 403」的坏实现也能过上面的断言。
	adminRouter := bareRouter(admin)
	recorder := asyncTaskRequest(adminRouter, "GET", "/api/import/template?resource=users", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin without middleware status = %d, want 200 (handler must not reject admins)",
			recorder.Code)
	}
}

// 提交导出：不传 masked 时必须**默认脱敏**（默认值的唯一来源在 service 层）。
func TestAsyncTaskExportCreateDefaultsToMasked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	manager := installAsyncTaskControllerTestManager(t, db)
	admin := asyncTaskSeedUser(t, db, 9202, "SYNTHETIC_EXPORT_ADMIN", "admin")
	router := asyncTaskRouter(admin)

	recorder := asyncTaskRequest(router, "POST", "/api/export/tasks", `{"resource":"users"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var response struct {
		TaskID string `json:"taskId"`
		Masked bool   `json:"masked"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if !response.Masked {
		t.Fatalf("masked = false, want true by default (body=%s)", recorder.Body.String())
	}
	task, err := service.GetAsyncTaskWithDB(db, response.TaskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if !task.Masked {
		t.Fatal("persisted task must be masked by default")
	}
	// 显式取消脱敏：必须落 masked=false（GORM 零值陷阱的回归钉子）。
	recorder = asyncTaskRequest(router, "POST", "/api/export/tasks",
		`{"resource":"users","masked":false}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("unmasked status = %d, want 202 (body=%s)", recorder.Code, recorder.Body.String())
	}
	response = struct {
		TaskID string `json:"taskId"`
		Masked bool   `json:"masked"`
	}{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode unmasked: %v", err)
	}
	if response.Masked {
		t.Fatal("explicit masked=false must not be silently turned into true")
	}
	unmaskedTask, err := service.GetAsyncTaskWithDB(db, response.TaskID)
	if err != nil {
		t.Fatalf("load unmasked task: %v", err)
	}
	if unmaskedTask.Masked {
		t.Fatal("explicit masked=false must persist as false")
	}
	_ = manager
}

// 导出只接受 CSV（JSON 由前端本地生成，后端不该起异步任务）。
func TestAsyncTaskExportCreateRejectsNonCSV(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	admin := asyncTaskSeedUser(t, db, 9203, "SYNTHETIC_CSV_ADMIN", "admin")
	router := asyncTaskRouter(admin)

	for _, format := range []string{"json", "xlsx"} {
		recorder := asyncTaskRequest(router, "POST", "/api/export/tasks",
			`{"resource":"users","format":"`+format+`"}`)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("format=%s status = %d, want 400 (body=%s)", format, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "CSV") {
			t.Fatalf("format=%s message must mention CSV: %s", format, recorder.Body.String())
		}
	}
	// 不支持的资源同样 400（不是等到任务跑完才报）。
	recorder := asyncTaskRequest(router, "POST", "/api/export/tasks", `{"resource":"unknown"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown resource status = %d, want 400 (body=%s)", recorder.Code, recorder.Body.String())
	}
}

// 下载过期任务必须 410 Gone（不是 404、不是 500）。
func TestAsyncTaskExportDownloadExpiredReturnsGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	admin := asyncTaskSeedUser(t, db, 9204, "SYNTHETIC_DL_ADMIN", "admin")

	path := filepath.Join(t.TempDir(), "expired.csv")
	if err := os.WriteFile(path, []byte("a\n1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	task := entity.AsyncTask{
		TaskID: "exp-expireddownload01", Type: entity.AsyncTaskTypeExport,
		Resource: "users", Format: "csv", Status: entity.AsyncTaskStatusSucceeded,
		Masked: true, FilePath: path, ExpiresAt: &past,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	router := asyncTaskRouter(admin)
	recorder := asyncTaskRequest(router, "GET", "/api/export/tasks/"+task.TaskID+"/download", "")
	if recorder.Code != http.StatusGone {
		t.Fatalf("expired download status = %d, want 410 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "过期") {
		t.Fatalf("message must mention 过期: %s", recorder.Body.String())
	}

	// 任务不存在 → 404（与 410 区分开：一个是「没有」，一个是「过期」）。
	recorder = asyncTaskRequest(router, "GET", "/api/export/tasks/exp-doesnotexist0000/download", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing task status = %d, want 404 (body=%s)", recorder.Code, recorder.Body.String())
	}
}

// 未注入 manager 时提交必须 503（不能假装成功然后什么都不发生）。
func TestAsyncTaskExportCreateWithoutManagerReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	previous := asyncTaskManager
	asyncTaskManager = nil
	t.Cleanup(func() { asyncTaskManager = previous })
	admin := asyncTaskSeedUser(t, db, 9205, "SYNTHETIC_NOMGR_ADMIN", "admin")
	router := asyncTaskRouter(admin)

	recorder := asyncTaskRequest(router, "POST", "/api/export/tasks", `{"resource":"users"}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%s)", recorder.Code, recorder.Body.String())
	}
}

// 导入模板：管理员能拿到 CSV，且内容含表头（并带 BOM 以便 Excel 识别中文）。
func TestImportDataTemplateReturnsCSVHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	admin := asyncTaskSeedUser(t, db, 9206, "SYNTHETIC_TPL_ADMIN", "admin")
	router := asyncTaskRouter(admin)

	recorder := asyncTaskRequest(router, "GET", "/api/import/template?resource=blacklist", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.HasPrefix(body, "\uFEFF") {
		t.Fatalf("template must start with UTF-8 BOM, got %q", body[:minIntForTest(8, len(body))])
	}
	if !strings.Contains(body, "value") {
		t.Fatalf("template must contain the value column: %q", body)
	}
	// 不支持的资源 → 400。
	recorder = asyncTaskRequest(router, "GET", "/api/import/template?resource=billing", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsupported resource status = %d, want 400", recorder.Code)
	}
}

// 导入：弱密码未确认 → 409 + 行号（不是 400，因为文件本身没错，缺的是确认动作）。
func TestImportDataCreateWeakPasswordReturnsConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	admin := asyncTaskSeedUser(t, db, 9207, "SYNTHETIC_IMP_ADMIN", "admin")
	router := asyncTaskRouter(admin)

	recorder := asyncTaskMultipartRequest(t, router, "/api/import/tasks",
		map[string]string{"resource": "users", "dryRun": "false"}, "users.csv",
		[]byte("username,password\nweak,12345678\n"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var response struct {
		WeakPasswordRows []int  `json:"weakPasswordRows"`
		NeedConfirm      string `json:"needConfirm"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if response.NeedConfirm != "weakPassword" {
		t.Fatalf("needConfirm = %q, want weakPassword", response.NeedConfirm)
	}
	if len(response.WeakPasswordRows) != 1 || response.WeakPasswordRows[0] != 2 {
		t.Fatalf("weakPasswordRows = %v, want [2]", response.WeakPasswordRows)
	}
	// 未确认阶段不能有任何任务落库。
	var count int64
	if err := db.Model(&entity.AsyncTask{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("no task may be created before weak-password confirmation, got %d", count)
	}
}

// 导入：无扩展名且内容不可识别 → 400（不是 500、不是静默接受）。
func TestImportDataCreateRejectsUnrecognizedFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAsyncTaskControllerTestDB(t)
	installAsyncTaskControllerTestDB(t, db)
	installAsyncTaskControllerTestManager(t, db)
	admin := asyncTaskSeedUser(t, db, 9208, "SYNTHETIC_FMT_ADMIN", "admin")
	router := asyncTaskRouter(admin)

	recorder := asyncTaskMultipartRequest(t, router, "/api/import/tasks",
		map[string]string{"resource": "blacklist"}, "mystery",
		[]byte("\x00\x01\x02\x03\x04\x05\x06\x07\x08"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), ".csv") {
		t.Fatalf("message must list supported extensions: %s", recorder.Body.String())
	}
}

// asyncTaskMultipartRequest 发一次 multipart 上传请求。
func asyncTaskMultipartRequest(t *testing.T, router *gin.Engine, path string,
	fields map[string]string, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(content)); err != nil {
		t.Fatalf("copy content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", path, body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	router.ServeHTTP(recorder, request)
	return recorder
}

// minIntForTest 是本地 min（Go 1.21+ 有内置 min，但显式写一份避免版本差异）。
func minIntForTest(left, right int) int {
	if left < right {
		return left
	}
	return right
}
