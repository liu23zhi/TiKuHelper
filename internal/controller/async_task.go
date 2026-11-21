package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 异步任务与导入导出的 HTTP 控制器（107 Q12/Q13、111 §4、121 §4.10，任务书 ⑥）。
//
// 六条路由，**全部 requireAdmin**（任务书 ⑥「全部 RequireAdmin」）：
//
//	POST /export/tasks                     提交导出（CSV / JSON / XLSX / XML / HTML，异步）
//	POST /import/tasks                     提交导入（CSV / JSON / XLSX / XLS / XML / HTML / TXT）
//	GET  /export/tasks/:taskId             查单个任务状态（导出与导入共用，按 taskId 查）
//	GET  /export/tasks                     任务列表（管理页）
//	GET  /export/tasks/:taskId/download    下载结果文件（过期 → 410）
//	GET  /export/tasks/:taskId/errors      下载失败行报告（errors.csv）
//	GET  /import/template                  下载导入模板（题库 / 黑名单 / 用户）
//
// 路由注册不在本文件：internal/api/router.go 是既有的、被多人共用的文件，
// 本任务不碰它（任务书「严禁触碰」）。接线由集成方把这七个 handler 挂上去，
// 路径建议与上方注释一致。
//
// # ⚠️ 双保险：路由中间件 + handler 内 requireAdmin(c)，**不得删除任何一道**
//
// 本文件 7 个 handler 的第一句都是 `if !requireAdmin(c) { return }`，
// 而路由上同时挂着 `middleware.RequireAdmin`。看起来冗余，但**两道都要保留**：
//
//   - 中间件层管「这个路由组整体要不要管理员」，新增路由若忘了写 handler 内检查，
//     中间件仍然兜住；
//   - handler 层管「这个 handler 自己是不是安全」，路由被复制到别的 group
//     （漏挂中间件）时 handler 仍然兜住。
//
// 真正的风险不是多写一行，而是**两道互相遮蔽导致「两道都在」从未被验证**：
// 实测过 —— 只把路由上的 `middleware.RequireAdmin` 摘掉，集成测试**仍然全绿**
// （handler 自己拦住了）；只把 handler 内的 `requireAdmin` 短路，也**仍然全绿**
// （中间件拦住了）。所以「删一道测试会红」这个直觉在这里**不成立**。
//
// 与之配套的是两条各自独立的回归钉子，**改本文件时必须同时确认它们都能各自变红**：
//
//	TestAsyncTaskRoutesRejectNonAdmin                 —— 跑完整两条中间件链（401 + 403 分层）
//	TestAsyncTaskHandlersAlsoCheckAdminWithoutMiddleware —— 只挂 Auth、不挂 RequireAdmin
//	                                                       （含「管理员必须 200」的对照组）
//
// 若将来有人把某道防线删掉，这两条中必有一条会红；把**两条都删掉**才会双绿。
// 这就是为什么两道防线不能只有一条测试覆盖 —— 那正是本条注释要防的事故形态。

// asyncTaskControllerDB 返回当前数据库句柄；未初始化时返回 nil（调用方负责降级）。
//
// 与 blacklistAdminDB / referralDB / reviewRebateDB 同构：单测里没有 manager 时不能 panic。
var asyncTaskControllerDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

// asyncTaskManager 是本进程的管理器实例（由 cmd 侧注入）。
//
// 用可替换变量而不是在 controller 里 new 一个：一个进程只能有一个 manager
// （它持有 worker 与清理 goroutine），controller 每次 new 会起多份 worker，
// 表现为「同一个任务被执行两次」。
var asyncTaskManager *service.AsyncTaskManager

const questionPreviewTTL = 30 * time.Minute
const questionPreviewPageSize = 25

type questionImportPreview struct {
	id        string
	actorID   int32
	filename  string
	format    string
	content   []byte
	rows      [][]string
	header    []string
	expiresAt time.Time
	confirmed bool
}

var questionImportPreviews = struct {
	sync.Mutex
	items map[string]*questionImportPreview
}{items: make(map[string]*questionImportPreview)}

type questionPreviewItem struct {
	Row        int      `json:"row"`
	Question   string   `json:"question"`
	Type       string   `json:"type"`
	Options    []string `json:"options"`
	Answer     []string `json:"answer"`
	Extra      string   `json:"extra"`
	CourseName string   `json:"courseName"`
	Source     string   `json:"source"`
}

// SetAsyncTaskManager 由启动路径注入管理器实例（未注入时提交接口返回 503）。
func SetAsyncTaskManager(m *service.AsyncTaskManager) { asyncTaskManager = m }

// GetAsyncTaskManager 返回当前管理器（可为 nil；测试与启动路径共用）。
func GetAsyncTaskManager() *service.AsyncTaskManager { return asyncTaskManager }

// AsyncTaskMaxUploadBytes 是导入文件的大小上限。
//
// 与既有 UploadFile 的 maxUploadBytes（16 MiB）同一口径：
// 5000 行的 CSV 大约几百 KB，16 MiB 足够 10 万行的 xlsx，
// 再大就说明用户导错了文件（比如把整个题库导成了二进制）。
const AsyncTaskMaxUploadBytes = maxUploadBytes

// requireAsyncTaskDB 取一个带低优先级的会话（运维动作不抢答题链路连接）。
func requireAsyncTaskDB() (*gorm.DB, error) {
	db := asyncTaskControllerDB()
	if db == nil {
		return nil, service.ErrAsyncTaskDBUnavailable
	}
	return withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), nil
}

// 请求体 DTO。

type exportTaskRequest struct {
	// Resource 取 questions / users / billing / gifts / logs / tickets / blacklist /
	// archived_users / access_tokens。
	Resource string `json:"resource"`
	// Format 接受 CSV / JSON / XLSX / XML / HTML。
	Format string `json:"format"`
	// Masked 为 nil 时**默认脱敏**（service.DefaultExportMasked）。
	// 只有显式传 false 才导出明文 —— 默认值的唯一来源在 service 层。
	Masked *bool `json:"masked"`
	// Limit 可选，覆盖单任务行数上限（0 = 默认 10 万）。
	Limit       int     `json:"limit"`
	QuestionIDs []int64 `json:"questionIds"`
}

type importTaskRequest struct {
	// Resource 取 questions / blacklist / users。
	Resource string `json:"resource"`
	// DryRun 只校验不落库（预览）。
	DryRun bool `json:"dryRun"`
	// ConfirmWeakPassword 弱密码二次确认（users 专用，任务书 ④）。
	ConfirmWeakPassword bool `json:"confirmWeakPassword"`
	// Filename 用于推断格式（也可以直接从上传文件名取）。
	Filename string `json:"filename"`
}

// asyncTaskResponse 是把 entity.AsyncTask 转成前端友好形态。
//
// 刻意**不直接返回 entity**：SourcePath / FilePath 是服务器上的绝对路径，
// 泄漏它们等于告诉调用方「这台机器的目录结构和部署位置」。
// 前端只需要 downloadUrl，路径由后端拼。
type asyncTaskResponse struct {
	TaskID        string     `json:"taskId"`
	Type          string     `json:"type"`
	Resource      string     `json:"resource"`
	Format        string     `json:"format"`
	Status        string     `json:"status"`
	Masked        bool       `json:"masked"`
	DryRun        bool       `json:"dryRun"`
	Total         int        `json:"total"`
	Processed     int        `json:"processed"`
	Succeeded     int        `json:"succeeded"`
	Skipped       int        `json:"skipped"`
	Failed        int        `json:"failed"`
	Message       string     `json:"message"`
	ErrorMessage  string     `json:"errorMessage,omitempty"`
	DownloadURL   string     `json:"downloadUrl,omitempty"`
	ErrorURL      string     `json:"errorReportUrl,omitempty"`
	ResultDeleted bool       `json:"resultDeleted"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	ActorUsername string     `json:"actorUsername"`
}

// buildAsyncTaskResponse 把任务转成响应体。
//
// downloadUrl 只在成功且有文件时给：失败任务给一个下载链接，
// 前端点了会拿到 409，用户会以为是「系统坏了」而不是「任务失败了」。
func buildAsyncTaskResponse(task entity.AsyncTask) asyncTaskResponse {
	response := asyncTaskResponse{
		TaskID:        task.TaskID,
		Type:          task.Type,
		Resource:      task.Resource,
		Format:        task.Format,
		CreatedAt:     task.CreatedAt,
		Status:        task.Status,
		Masked:        task.Masked,
		DryRun:        task.DryRun,
		Total:         task.Total,
		Processed:     task.Processed,
		Succeeded:     task.Succeeded,
		Skipped:       task.Skipped,
		Failed:        task.Failed,
		Message:       task.Message,
		ErrorMessage:  task.ErrorMessage,
		ExpiresAt:     task.ExpiresAt,
		FinishedAt:    task.FinishedAt,
		ActorUsername: task.ActorUsername,
		ResultDeleted: task.Type == entity.AsyncTaskTypeExport && entity.AsyncTaskIsTerminal(task.Status) && strings.TrimSpace(task.FilePath) == "",
	}
	if task.Status == entity.AsyncTaskStatusSucceeded && strings.TrimSpace(task.FilePath) != "" {
		response.DownloadURL = fmt.Sprintf("/api/export/tasks/%s/download", task.TaskID)
	}
	if task.Status == entity.AsyncTaskStatusSucceeded && strings.TrimSpace(task.ErrorReportPath) != "" {
		response.ErrorURL = fmt.Sprintf("/api/export/tasks/%s/errors", task.TaskID)
	}
	return response
}

// ExportTaskCreate 提交一个异步导出任务（管理员）。
//
// 三条校验都是明确的 400，绝不静默修正：
//   - resource 必须在支持清单内（否则用户等到任务跑完才知道导错了）；
//   - format 支持 CSV；题库另支持 JSON / XLSX / XML / HTML；
//   - 未注入 manager 时 503（不能假装提交成功然后什么都不发生）。
//
// 响应 202 + {taskId}：前端拿它去轮询（111 Q12-1 选 B：立即返回任务 ID，前端轮询）。
func ExportTaskCreate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input exportTaskRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	resource := strings.ToLower(strings.TrimSpace(input.Resource))
	if _, err := service.ExportColumnsOf(resource); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = service.ImportFormatCSV
	}
	switch format {
	case service.ExportFormatCSV, service.ExportFormatJSON, service.ExportFormatXlsx, service.ExportFormatXML, service.ExportFormatHTML:
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "导出格式只支持 json / csv / xlsx / xml / html",
		})
		return
	}
	if resource != service.ExportResourceQuestions && format != service.ExportFormatCSV && format != service.ExportFormatJSON && format != service.ExportFormatXlsx {
		c.JSON(http.StatusBadRequest, gin.H{"message": "管理数据支持 CSV / JSON / XLSX；XML / HTML 导出仅支持题库"})
		return
	}
	if resource != service.ExportResourceQuestions && len(input.QuestionIDs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "仅题库导出支持指定题目 ID"})
		return
	}
	// 默认脱敏的解析只在 service 层做一次（resolveExportMasked 是唯一实现点）。
	masked := service.DefaultExportMasked
	if input.Masked != nil {
		masked = *input.Masked
	}
	managerInstance := asyncTaskManager
	if managerInstance == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "异步任务未启用，请联系管理员"})
		return
	}
	params, err := json.Marshal(map[string]any{
		"resource":    resource,
		"format":      format,
		"masked":      masked,
		"limit":       input.Limit,
		"questionIds": input.QuestionIDs,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "生成任务参数失败"})
		return
	}
	task, err := managerInstance.SubmitAsyncTask(entity.AsyncTaskTypeExport, resource, format,
		masked, string(params), "", operator)
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	recordAccountAudit(c, &operator, "export", 1, "提交异步导出任务", map[string]any{
		"taskId":   task.TaskID,
		"resource": resource,
		// 审计必须记 masked：这是「这次导出有没有带明文」的唯一证据。
		"masked":    masked,
		"createdAt": task.CreatedAt,
	})
	c.JSON(http.StatusAccepted, gin.H{
		"taskId": task.TaskID,
		"status": task.Status,
		// 前端提示文案要用到：默认脱敏时提醒「已脱敏」。
		"masked": masked,
	})
}

// ImportTaskCreate 提交一个异步导入任务（管理员）。
//
// 与导出的两个差异：
//
//  1. **文件先落盘再提交**（SourcePath）：异步任务要能在进程重启后重跑，
//     把文件放在请求内存里做不到这一点；
//  2. **弱密码在提交阶段同步校验一次**（users）：需要在写任何一行之前拿到行号清单，
//     输出给前端做二次确认（任务书 ④）。「确认后重跑」是干净的一次导入。
//
// 落盘位置在 manager.BaseDir 下的 imports/，文件名用随机任务前缀 + 原扩展名，
// 不用用户提供的文件名（原文件名可能含路径分隔符或 ../）。
func ImportTaskCreate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	managerInstance := asyncTaskManager
	if managerInstance == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "异步任务未启用，请联系管理员"})
		return
	}
	db, dbErr := requireAsyncTaskDB()
	if dbErr != nil {
		writeAsyncTaskError(c, dbErr)
		return
	}

	resource := strings.ToLower(strings.TrimSpace(c.PostForm("resource")))
	if resource == "" {
		resource = strings.ToLower(strings.TrimSpace(c.Query("resource")))
	}
	supported := false
	for _, candidate := range service.ImportResourceList() {
		if resource == candidate {
			supported = true
			break
		}
	}
	if !supported {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": fmt.Sprintf("不支持的导入资源；可选：%s", strings.Join(service.ImportResourceList(), " / ")),
		})
		return
	}
	dryRun := parseAsyncTaskBool(c.PostForm("dryRun"))
	confirmWeak := parseAsyncTaskBool(c.PostForm("confirmWeakPassword"))

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请选择要导入的文件"})
		return
	}
	if fileHeader.Size > AsyncTaskMaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"message": fmt.Sprintf("文件超过 %d MiB 上限", AsyncTaskMaxUploadBytes/(1<<20)),
		})
		return
	}
	content, readErr := readAsyncTaskUpload(fileHeader)
	if readErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "读取上传文件失败"})
		return
	}
	format, detectErr := service.DetectImportFormat(fileHeader.Filename, content)
	if detectErr != nil {
		// Detect 的错误文案已经含「请选择 .csv / .json / .xlsx / .xls 文件」。
		c.JSON(http.StatusBadRequest, gin.H{"message": detectErr.Error()})
		return
	}
	// 提交前的同步预检：只跑「解析 + 弱密码」，不落库。
	// 这一步让用户立刻拿到「表头缺列」「哪几行是弱密码」，不必等任务排队执行完。
	preview, previewErr := service.ImportDataWithDB(db, service.ImportDataOptions{
		Resource:            resource,
		Format:              format,
		Filename:            fileHeader.Filename,
		Content:             content,
		Actor:               operator,
		DryRun:              true,
		ConfirmWeakPassword: confirmWeak,
	})
	if previewErr != nil {
		if errors.Is(previewErr, service.ErrImportWeakPasswordConfirmRequired) {
			// 409 而不是 400：文件本身没错，缺的是一个确认动作。
			c.JSON(http.StatusConflict, gin.H{
				"message":          previewErr.Error(),
				"weakPasswordRows": preview.WeakPasswordRows,
				"needConfirm":      "weakPassword",
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"message": previewErr.Error()})
		return
	}

	sourcePath, saveErr := saveAsyncTaskUpload(managerInstance.BaseDir, fileHeader.Filename, content)
	if saveErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存导入文件失败"})
		return
	}
	params, _ := json.Marshal(map[string]any{
		"resource":            resource,
		"dryRun":              dryRun,
		"confirmWeakPassword": confirmWeak,
		"filename":            fileHeader.Filename,
	})
	task, submitErr := managerInstance.SubmitAsyncTask(entity.AsyncTaskTypeImport, resource, format,
		// 导入不涉及导出脱敏，Masked 记 false 并注明语义：
		// 它表达的是「这份导入文件里的敏感值没有被脱敏」，不是「是否要脱敏」。
		false, string(params), sourcePath, operator)
	if submitErr != nil {
		writeAsyncTaskError(c, submitErr)
		return
	}
	recordAccountAudit(c, &operator, "export", 1, "提交异步导入任务", map[string]any{
		"taskId":   task.TaskID,
		"resource": resource,
		"format":   format,
		"dryRun":   dryRun,
	})
	c.JSON(http.StatusAccepted, gin.H{
		"taskId": task.TaskID,
		"status": task.Status,
		"format": format,
		// 预检总数：前端可以立刻显示「将导入 N 行」。
		"total": preview.Total,
	})
}

func ImportQuestionPreviewCreate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	resource := strings.ToLower(strings.TrimSpace(c.PostForm("resource")))
	if resource != service.ImportResourceQuestions {
		c.JSON(http.StatusBadRequest, gin.H{"message": "该预览入口仅支持题库导入"})
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请选择要导入的文件"})
		return
	}
	if fileHeader.Size > AsyncTaskMaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": fmt.Sprintf("文件超过 %d MiB 上限", AsyncTaskMaxUploadBytes/(1<<20))})
		return
	}
	content, err := readAsyncTaskUpload(fileHeader)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "读取上传文件失败"})
		return
	}
	format, err := service.DetectImportFormat(fileHeader.Filename, content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	header, rows, err := service.ParseImportRows(format, content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if len(rows) > service.QuestionImportMaxRows {
		c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("导入行数超过单批上限 %d 行", service.QuestionImportMaxRows)})
		return
	}
	index := make(map[string]int, len(header))
	for position, name := range header {
		index[strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", ""))] = position
	}
	if _, ok := index["question"]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "导入文件的表头缺少必填列：question"})
		return
	}
	previewID, err := service.NewQuestionImportPreviewID()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "创建预览失败"})
		return
	}
	preview := &questionImportPreview{id: previewID, actorID: operator.ID, filename: filepath.Base(fileHeader.Filename), format: format, content: content, rows: rows, header: header, expiresAt: time.Now().Add(questionPreviewTTL)}
	questionImportPreviews.Lock()
	for key, value := range questionImportPreviews.items {
		if time.Now().After(value.expiresAt) {
			delete(questionImportPreviews.items, key)
		}
	}
	questionImportPreviews.items[previewID] = preview
	questionImportPreviews.Unlock()
	items := buildQuestionPreviewItems(preview, 1, questionPreviewPageSize)
	c.JSON(http.StatusOK, gin.H{"previewId": previewID, "total": len(rows), "page": 1, "pageSize": questionPreviewPageSize, "items": items})
}

func ImportQuestionPreviewPage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	preview, status := getQuestionImportPreview(c.Param("previewId"), operator.ID)
	if status != http.StatusOK {
		c.JSON(status, gin.H{"message": "题库预览不存在或已过期"})
		return
	}
	page, pageSize := blacklistPagination(c)
	items := buildQuestionPreviewItems(preview, page, pageSize)
	c.JSON(http.StatusOK, gin.H{"previewId": preview.id, "total": len(preview.rows), "page": page, "pageSize": pageSize, "items": items})
}

func ImportQuestionPreviewConfirm(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	managerInstance := asyncTaskManager
	if managerInstance == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "异步任务未启用，请联系管理员"})
		return
	}
	preview, status := getQuestionImportPreview(c.Param("previewId"), operator.ID)
	if status != http.StatusOK {
		c.JSON(status, gin.H{"message": "题库预览不存在或已过期"})
		return
	}
	questionImportPreviews.Lock()
	if preview.confirmed {
		questionImportPreviews.Unlock()
		c.JSON(http.StatusConflict, gin.H{"message": "该预览已提交导入"})
		return
	}
	preview.confirmed = true
	questionImportPreviews.Unlock()
	path, err := saveAsyncTaskUpload(managerInstance.BaseDir, preview.filename, preview.content)
	if err != nil {
		questionImportPreviews.Lock()
		preview.confirmed = false
		questionImportPreviews.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存待导入文件失败"})
		return
	}
	params, err := json.Marshal(map[string]any{"resource": service.ImportResourceQuestions, "filename": preview.filename, "dryRun": false})
	if err != nil {
		_ = os.Remove(path)
		questionImportPreviews.Lock()
		preview.confirmed = false
		questionImportPreviews.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"message": "生成导入参数失败"})
		return
	}
	task, err := managerInstance.SubmitAsyncTask(entity.AsyncTaskTypeImport, service.ImportResourceQuestions, preview.format, service.DefaultExportMasked, string(params), path, operator)
	if err != nil {
		_ = os.Remove(path)
		questionImportPreviews.Lock()
		preview.confirmed = false
		questionImportPreviews.Unlock()
		writeAsyncTaskError(c, err)
		return
	}
	questionImportPreviews.Lock()
	delete(questionImportPreviews.items, preview.id)
	questionImportPreviews.Unlock()
	recordAccountAudit(c, &operator, "import", 1, "确认题库文件导入", map[string]any{"taskId": task.TaskID, "format": preview.format, "total": len(preview.rows)})
	c.JSON(http.StatusAccepted, gin.H{"taskId": task.TaskID, "status": task.Status, "createdAt": task.CreatedAt})
}

func ImportQuestionPreviewDelete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	questionImportPreviews.Lock()
	preview, exists := questionImportPreviews.items[c.Param("previewId")]
	if !exists || preview.actorID != operator.ID {
		questionImportPreviews.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"message": "题库预览不存在或已过期"})
		return
	}
	if time.Now().After(preview.expiresAt) {
		delete(questionImportPreviews.items, preview.id)
		questionImportPreviews.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"message": "题库预览不存在或已过期"})
		return
	}
	if preview.confirmed {
		questionImportPreviews.Unlock()
		c.JSON(http.StatusConflict, gin.H{"message": "已提交的导入任务不能取消"})
		return
	}
	delete(questionImportPreviews.items, preview.id)
	questionImportPreviews.Unlock()
	c.Status(http.StatusNoContent)
}

func getQuestionImportPreview(id string, actorID int32) (*questionImportPreview, int) {
	questionImportPreviews.Lock()
	defer questionImportPreviews.Unlock()
	preview, ok := questionImportPreviews.items[strings.TrimSpace(id)]
	if !ok {
		return nil, http.StatusNotFound
	}
	if time.Now().After(preview.expiresAt) {
		delete(questionImportPreviews.items, preview.id)
		return nil, http.StatusGone
	}
	if preview.actorID != actorID {
		return nil, http.StatusNotFound
	}
	return preview, http.StatusOK
}

func buildQuestionPreviewItems(preview *questionImportPreview, page, pageSize int) []questionPreviewItem {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	start := (page - 1) * pageSize
	if start >= len(preview.rows) {
		return []questionPreviewItem{}
	}
	end := minIntController(start+pageSize, len(preview.rows))
	index := make(map[string]int, len(preview.header))
	for position, name := range preview.header {
		index[strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", ""))] = position
	}
	items := make([]questionPreviewItem, 0, end-start)
	for rowIndex, row := range preview.rows[start:end] {
		read := func(key string) string {
			if position, ok := index[key]; ok && position < len(row) {
				return row[position]
			}
			return ""
		}
		options := decodePreviewStringArray(read("options"))
		answer := decodePreviewStringArray(read("answer"))
		source := read("source_name")
		if source == "" {
			source = "人工导入"
		}
		items = append(items, questionPreviewItem{Row: start + rowIndex + 2, Question: read("question"), Type: read("type"), Options: options, Answer: answer, Extra: read("extra"), CourseName: read("course_name"), Source: source})
	}
	return items
}

func minIntController(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func decodePreviewStringArray(value string) []string {
	var items []string
	if json.Unmarshal([]byte(value), &items) != nil || items == nil {
		return []string{}
	}
	return items
}

// parseAsyncTaskBool 解析表单里的布尔值（"true"/"1"/"on" 都算真）。
func parseAsyncTaskBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "on", "yes":
		return true
	default:
		return false
	}
}

// readAsyncTaskUpload 读上传文件内容（带上限保护）。
//
// 逐字节读进内存而不是流式解析：导入的行数上限是 5000（service.DefaultImportMaxRows），
// 5000 行的 csv/json/xlsx 都在几十 MB 以内，一次性读入换来的「能重复解析两次」
// （一次是提交前的同步预检，一次是异步执行）是值得的。
func readAsyncTaskUpload(fileHeader *multipart.FileHeader) ([]byte, error) {
	if fileHeader == nil {
		return nil, fmt.Errorf("上传文件为空")
	}
	src, err := fileHeader.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = src.Close() }()
	// 再兜一道上限：fileHeader.Size 来自 Content-Length 声明，可以撒谎。
	reader := io.LimitReader(src, AsyncTaskMaxUploadBytes+1)
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > AsyncTaskMaxUploadBytes {
		return nil, fmt.Errorf("文件超过上限")
	}
	return content, nil
}

// saveAsyncTaskUpload 把上传内容落盘（返回绝对路径）。
//
// 文件名的构造刻意**不含用户提供的名字**：原文件名可能含 `../` 或 Windows 保留名，
// 直接拼进路径就是任意文件写入。这里只用随机 ID + 白名单扩展名。
func saveAsyncTaskUpload(baseDir, filename string, content []byte) (string, error) {
	if strings.TrimSpace(baseDir) == "" {
		return "", fmt.Errorf("未配置异步任务目录")
	}
	dir := filepath.Join(baseDir, "imports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	switch extension {
	case ".csv", ".json", ".xlsx", ".xls", ".xml", ".html", ".htm", ".txt":
	default:
		// 扩展名不可用时按内容推断，保证落盘文件名与解析器一致。
		detected, err := service.DetectImportFormat(filename, content)
		if err != nil {
			return "", err
		}
		extension = "." + detected
	}
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "import-"+hex.EncodeToString(token)+extension)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ExportTaskDetail 查单个任务状态（导出的轮询接口，导入也复用）。
func ExportTaskDetail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireAsyncTaskDB()
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	task, err := service.GetAsyncTaskWithDB(db, c.Param("taskId"))
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, buildAsyncTaskResponse(task))
}

// ExportTaskList 返回任务列表（管理页）。
func ExportTaskList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireAsyncTaskDB()
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	page, pageSize := blacklistPagination(c)
	rows, total, err := service.ListAsyncTasksWithDB(db, service.AsyncTaskListQuery{
		Type:     strings.TrimSpace(c.Query("type")),
		Status:   strings.TrimSpace(c.Query("status")),
		Resource: strings.TrimSpace(c.Query("resource")),
	}, page, pageSize)
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	items := make([]asyncTaskResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, buildAsyncTaskResponse(row))
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "page": page, "pageSize": pageSize})
}

// ExportTaskDownload 下载结果文件（过期 → 410 Gone）。
//
// 410 而不是 404：文件曾经存在、现在过期了，这是「明确的过期」而不是「没有这个东西」。
// 前端据此提示「文件已过期，请重新导出」（111 §4.8 的 T1:21 行为）。
func ExportTaskDownload(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireAsyncTaskDB()
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	task, err := service.GetAsyncTaskWithDB(db, c.Param("taskId"))
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	file, name, err := service.OpenAsyncTaskResultFile(task, time.Now())
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	defer func() { _ = file.Close() }()
	c.Header("Content-Type", "text/csv; charset=utf-8")
	if task.Format != "" {
		c.Header("Content-Type", exportContentType(task.Format))
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, asyncTaskDownloadName(task, name)))
	c.File(file.Name())
}

// ExportTaskErrorReport 下载失败行报告（errors.csv）。
func ExportTaskErrorReport(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireAsyncTaskDB()
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	task, err := service.GetAsyncTaskWithDB(db, c.Param("taskId"))
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	if task.ExpiresAt != nil && !time.Now().Before(*task.ExpiresAt) {
		writeAsyncTaskError(c, service.ErrAsyncTaskFileExpired)
		return
	}
	path := strings.TrimSpace(task.ErrorReportPath)
	if path == "" {
		c.JSON(http.StatusNotFound, gin.H{"message": "该任务没有失败行报告"})
		return
	}
	if _, statErr := os.Stat(path); statErr != nil {
		writeAsyncTaskError(c, service.ErrAsyncTaskFileExpired)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-errors.csv"`, task.TaskID))
	c.File(path)
}

// ExportTaskDelete removes a completed export artifact while retaining its task
// row as history. Only the submitting administrator may remove their result.
func ExportTaskDelete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	db, err := requireAsyncTaskDB()
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	task, err := service.GetAsyncTaskWithDB(db, c.Param("taskId"))
	if err != nil {
		writeAsyncTaskError(c, err)
		return
	}
	if task.Type != entity.AsyncTaskTypeExport || task.Resource != service.ExportResourceQuestions {
		c.JSON(http.StatusNotFound, gin.H{"message": "题库导出任务不存在"})
		return
	}
	if task.ActorUserID != operator.ID {
		c.JSON(http.StatusForbidden, gin.H{"message": "只能删除自己提交的导出结果"})
		return
	}
	if !entity.AsyncTaskIsTerminal(task.Status) {
		c.JSON(http.StatusConflict, gin.H{"message": "任务完成后才能删除导出结果"})
		return
	}
	if strings.TrimSpace(task.FilePath) == "" {
		c.JSON(http.StatusNoContent, nil)
		return
	}
	if err := os.Remove(task.FilePath); err != nil && !os.IsNotExist(err) {
		logger.SysError("删除导出结果失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "删除导出结果失败"})
		return
	}
	if err := db.Model(&entity.AsyncTask{}).Where("id = ? AND actor_user_id = ? AND status IN ?", task.ID, operator.ID, []string{entity.AsyncTaskStatusSucceeded, entity.AsyncTaskStatusFailed}).Update("file_path", "").Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "更新任务记录失败"})
		return
	}
	recordAccountAudit(c, &operator, "export", 1, "删除题库导出结果", map[string]any{"taskId": task.TaskID, "resource": task.Resource})
	c.Status(http.StatusNoContent)
}

// asyncTaskDownloadName 生成下载文件名（含资源与时间，便于运营归档）。
func asyncTaskDownloadName(task entity.AsyncTask, fallback string) string {
	resource := strings.TrimSpace(task.Resource)
	if resource == "" {
		resource = "task"
	}
	if task.FinishedAt != nil {
		format := strings.ToLower(strings.TrimSpace(task.Format))
		if format == "" {
			format = "csv"
		}
		return fmt.Sprintf("%s-export-%s.%s", resource, task.FinishedAt.In(logger.ShanghaiLocation()).Format("20060102-1504"), format)
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	format := strings.ToLower(strings.TrimSpace(task.Format))
	if format == "" {
		format = "csv"
	}
	return resource + "-export." + format
}

func exportContentType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case service.ExportFormatJSON:
		return "application/json; charset=utf-8"
	case service.ExportFormatXlsx:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case service.ExportFormatXML:
		return "application/xml; charset=utf-8"
	case service.ExportFormatHTML:
		return "text/html; charset=utf-8"
	default:
		return "text/csv; charset=utf-8"
	}
}

// ImportTemplate 下载导入模板（只有表头）。
func ImportTemplate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	resource := strings.ToLower(strings.TrimSpace(c.Query("resource")))
	format := strings.ToLower(strings.TrimSpace(c.Query("format")))
	if format == "" {
		format = service.ExportFormatCSV
	}
	content, err := service.ImportTemplateData(resource, format)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	contentType := exportContentType(format)
	extension := format
	if extension == service.ExportFormatXlsx {
		extension = "xlsx"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-import-template.%s"`, resource, extension))
	c.Data(http.StatusOK, contentType, content)
}

// writeAsyncTaskError 把 service 错误映射成 HTTP 状态。
//
// 映射规则（每条都对应一个用户可理解的状态）：
//
//	任务不存在                      → 404
//	文件已过期                      → 410（明确的过期，不是「没有」）
//	文件未就绪 / 任务未完成          → 409
//	资源/格式/表头/上限/弱密码       → 400
//	弱密码需要确认                   → 400（本函数兜底；专用文案在 ImportTaskCreate 里）
//	数据库未初始化 / 表未就绪         → 503
//	执行器未注册 / 其它              → 500（只记日志，不回原始错误）
func writeAsyncTaskError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "异步任务操作失败"
	switch {
	case errors.Is(err, service.ErrAsyncTaskNotFound):
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, service.ErrAsyncTaskFileExpired):
		status = http.StatusGone
		message = err.Error()
	case errors.Is(err, service.ErrAsyncTaskNotReady):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrAsyncTaskTypeUnsupported):
		status = http.StatusServiceUnavailable
		message = err.Error()
	case errors.Is(err, service.ErrAsyncTaskDBUnavailable):
		status = http.StatusServiceUnavailable
		message = err.Error()
	case errors.Is(err, service.ErrExportResourceUnsupported),
		errors.Is(err, service.ErrImportResourceUnsupported),
		errors.Is(err, service.ErrImportFormatUnsupported),
		errors.Is(err, service.ErrImportFileEmpty),
		errors.Is(err, service.ErrImportHeaderInvalid),
		errors.Is(err, service.ErrImportRowLimitExceeded),
		errors.Is(err, service.ErrImportWeakPasswordConfirmRequired):
		status = http.StatusBadRequest
		message = err.Error()
	}
	if status >= http.StatusInternalServerError {
		logger.SysError("异步任务操作失败: " + logger.RedactSecrets(err.Error()))
	}
	c.JSON(status, gin.H{"message": message})
}
