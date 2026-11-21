package controller

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

const userBanImportPreviewTTL = 10 * time.Minute
const userBanImportMaxBytes = 2 << 20

type userBanImportPreview struct {
	actorID   int32
	sessionID string
	targets   []service.UserBanImportTarget
	expiresAt time.Time
	consumed  bool
}

var userBanImportPreviews = struct {
	sync.Mutex
	items map[string]*userBanImportPreview
}{items: make(map[string]*userBanImportPreview)}

func UserBanImportPreviewCreate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	actor, ok := currentOperator(c)
	if !ok {
		return
	}
	db, err := requireUserBanDB()
	if err != nil {
		writeUserBanAdminError(c, err)
		return
	}
	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请选择 CSV / JSON 文件"})
		return
	}
	if header.Size > userBanImportMaxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "封禁导入文件不能超过 2 MiB"})
		return
	}
	content, err := readAsyncTaskUpload(header)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "无法读取导入文件"})
		return
	}
	format, err := service.DetectImportFormat(header.Filename, content)
	if err != nil || (format != service.ImportFormatCSV && format != service.ImportFormatJSON) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "封禁导入仅支持 CSV / JSON"})
		return
	}
	targets, err := service.PreviewUserBanImportWithDB(db, format, content, time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	id, err := service.NewQuestionImportPreviewID()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "无法创建封禁预览"})
		return
	}
	now := time.Now()
	userBanImportPreviews.Lock()
	for key, item := range userBanImportPreviews.items {
		if !item.expiresAt.After(now) {
			delete(userBanImportPreviews.items, key)
		}
	}
	if len(userBanImportPreviews.items) >= 100 {
		userBanImportPreviews.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"message": "封禁预览过多，请稍后重试"})
		return
	}
	userBanImportPreviews.items[id] = &userBanImportPreview{actorID: actor.ID, sessionID: actor.LoginSessionID, targets: targets, expiresAt: now.Add(userBanImportPreviewTTL)}
	userBanImportPreviews.Unlock()
	c.JSON(http.StatusOK, gin.H{"previewId": id, "total": len(targets), "targets": targets, "expiresAt": now.Add(userBanImportPreviewTTL)})
}

func UserBanImportPreviewConfirm(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	actor, ok := currentOperator(c)
	if !ok {
		return
	}
	var input struct {
		Confirmed bool `json:"confirmed"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || !input.Confirmed {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请先核对账号详情并明确确认封禁"})
		return
	}
	db, err := requireUserBanDB()
	if err != nil {
		writeUserBanAdminError(c, err)
		return
	}
	id := strings.TrimSpace(c.Param("previewId"))
	userBanImportPreviews.Lock()
	preview := userBanImportPreviews.items[id]
	if preview == nil || preview.actorID != actor.ID || preview.sessionID != actor.LoginSessionID {
		userBanImportPreviews.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"message": "封禁预览不存在"})
		return
	}
	if !preview.expiresAt.After(time.Now()) {
		delete(userBanImportPreviews.items, id)
		userBanImportPreviews.Unlock()
		c.JSON(http.StatusGone, gin.H{"message": "封禁预览已过期，请重新上传并核对"})
		return
	}
	if preview.consumed {
		userBanImportPreviews.Unlock()
		c.JSON(http.StatusConflict, gin.H{"message": "此封禁预览已提交，请刷新列表核对"})
		return
	}
	preview.consumed = true
	targets := append([]service.UserBanImportTarget(nil), preview.targets...)
	preview.targets = nil
	userBanImportPreviews.Unlock()
	count, err := service.ApplyUserBanImportWithDB(db, targets, actor.ID, time.Now())
	if err != nil {
		if errors.Is(err, service.ErrUserBanImportStale) {
			c.JSON(http.StatusConflict, gin.H{"message": "账号资料或到期时间已变化，未执行封禁，请重新预览"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "封禁导入未完成，请刷新列表核对后重新预览"})
		}
		return
	}
	ids := make([]int32, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.UserID)
	}
	recordAccountAudit(c, &actor, "user_ban_import", 1, "确认批量封禁账号", map[string]any{"userIds": ids, "count": count})
	c.JSON(http.StatusOK, gin.H{"succeeded": count})
}

// 取消只释放预览，不改变任何账号状态。
func UserBanImportPreviewDelete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	actor, ok := currentOperator(c)
	if !ok {
		return
	}
	userBanImportPreviews.Lock()
	id := strings.TrimSpace(c.Param("previewId"))
	preview := userBanImportPreviews.items[id]
	if preview != nil && preview.actorID == actor.ID && preview.sessionID == actor.LoginSessionID {
		delete(userBanImportPreviews.items, id)
	}
	userBanImportPreviews.Unlock()
	c.Status(http.StatusNoContent)
}
