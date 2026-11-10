package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// archiveReasonMinLength 是删除（归档）原因的最小长度，按「去空格后的字符数」计。
//
// 依据 77-*.md §11.8（Q77-6 定案）：reason 去首尾空格后长度 ≥ 2，空值返回 400。
// ⚠️ 前端 required 不算数 —— 直接调接口就能绕过，后端必须独立校验。
const archiveReasonMinLength = 2

// archivedTikuMaxPageSize 限制归档列表单页大小，避免一次把整张归档表读出来。
const archivedTikuMaxPageSize = 100

// archiveReasonCode 把管理员填写的原因归一到可筛选的分类代码（写入 archived_reason 列）。
//
// 前端下拉预设的四个选项映射为稳定代码；自由填写的原因归入 admin_delete
// （表示「管理员手工删除」，与 Q77-1 的归档来源分类一致）。
// 自由文本本身不会丢：它同时写进 logs.Description 与 logs.OldAnswer 的快照 reason。
func archiveReasonCode(rawReason string) string {
	switch strings.TrimSpace(rawReason) {
	case "违规内容":
		return "violation"
	case "重复题库":
		return "duplicate"
	case "内容错误":
		return "content_error"
	case "其他":
		return "other"
	}
	return entity.TikuArchivedReasonAdminDelete
}

// archiveTikuSnapshot 生成写进 logs.OldAnswer 的题库快照。
//
// ⚠️ 必须显式挑字段，不得整结构体序列化（77-*.md §2.4 红线 2）：
// 结构体加字段时整序列化会把新字段（含未来可能出现的凭据类字段）自动带进日志，
// 而日志是要长期保留、人人可查的。这里逐列列出，等价于「白名单」。
func archiveTikuSnapshot(tiku *entity.Tiku) map[string]any {
	if tiku == nil {
		return nil
	}
	return map[string]any{
		"id":         tiku.ID,
		"question":   tiku.Question,
		"type":       tiku.Type,
		"options":    tiku.Options,
		"answer":     tiku.Answer,
		"hash":       tiku.Hash,
		"courseName": tiku.CourseName,
		"extra":      tiku.Extra,
		"sourceName": tiku.SourceName,
	}
}

// tikuArchiveDB 返回一个干净的 *gorm.DB 句柄。
//
// ⚠️ dao.Tiku.UnderlyingDB() 的 Statement 上挂着 tiku 的 schema，
// 直接拿它去 Create(&entity.Log{}) 会在 GORM 的 ConvertToCreateValues 阶段
// 用 tiku 的 StructField.Index 反射 log 值 —— 越界 panic 且只在写日志路径上触发。
// Session(NewDB: true) 清掉 model/schema、保留同一连接（含事务），是既有的标准修法。
func tikuArchiveDB() *gorm.DB {
	return dao.Tiku.UnderlyingDB().Session(&gorm.Session{NewDB: true})
}

// tikuArchiveLogCreate 在给定事务/连接里写一条 logs 行（模型显式指定为 Log）。
func tikuArchiveLogCreate(tx *gorm.DB, entry *entity.Log) error {
	if tx == nil || entry == nil {
		return errors.New("日志写入参数缺失")
	}
	return tx.Session(&gorm.Session{NewDB: true}).Model(&entity.Log{}).Create(entry).Error
}

// archiveTikuQuestion 在单个事务内把题目改为「已归档」并写审计日志。
//
// 顺序（77-*.md §11.4 的硬要求）：先写日志、后置归档标记，同一事务提交。
// 绝不能出现「归档了但没日志」—— 那等于删了却查不到谁删的。
func archiveTikuQuestion(user *entity.User, tikuID int32, reason string) (*entity.Tiku, error) {
	if user == nil {
		return nil, errors.New("缺少操作者")
	}
	db := tikuArchiveDB()
	tx := db.Begin()
	if tx == nil || tx.Error != nil {
		if tx != nil {
			return nil, tx.Error
		}
		return nil, errors.New("开启事务失败")
	}
	defer func() {
		// 已提交时 Rollback 是空操作；未提交时保证不留下半截状态。
		_ = tx.Rollback().Error
	}()

	var current entity.Tiku
	if err := tx.Model(&entity.Tiku{}).Where("id = ?", tikuID).First(&current).Error; err != nil {
		return nil, err
	}

	now := time.Now()
	snapshot, marshalErr := json.Marshal(map[string]any{
		"before": archiveTikuSnapshot(&current),
		"reason": reason,
	})
	if marshalErr != nil {
		return nil, marshalErr
	}

	// 先写日志：这一刻被归档的完整内容 + 管理员填的原因都已在事务里。
	entry := &entity.Log{
		Qid:         current.ID,
		Module:      "tiku",
		Action:      TikuActionArchive,
		UserID:      user.ID,
		Username:    user.Username,
		Description: "归档题库：" + reason,
		CreateTime:  now,
		OldAnswer:   string(snapshot),
	}
	if err := tikuArchiveLogCreate(tx, entry); err != nil {
		return nil, err
	}

	result := tx.Model(&entity.Tiku{}).Where("id = ?", tikuID).Updates(map[string]any{
		"archived_reason":     archiveReasonCode(reason),
		"archived_at":         now,
		"archived_by_user_id": user.ID,
		// deleted_at 非 NULL = 已归档；gorm.DeletedAt 会自动给所有 dao.Tiku 查询
		// 附上 `deleted_at IS NULL`，因此归档后该题在所有业务路径上「等于不存在」。
		"deleted_at": now,
	})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// 行在事务内被别人改掉（重复删除/并发恢复），按「已归档或不存在」处理。
		return nil, gorm.ErrRecordNotFound
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &current, nil
}

// ArchivedTikuItem 是归档列表的一行（含归档元数据）。
type ArchivedTikuItem struct {
	ID               int32      `json:"id"`
	Question         string     `json:"question"`
	Type             int32      `json:"type"`
	Options          string     `json:"options"`
	Answer           string     `json:"answer"`
	Hash             string     `json:"hash"`
	CourseName       string     `json:"courseName"`
	Extra            string     `json:"extra"`
	SourceName       string     `json:"sourceName"`
	ArchivedReason   string     `json:"archivedReason"`
	ArchivedAt       *time.Time `json:"archivedAt"`
	ArchivedByUserID int32      `json:"archivedByUserId"`
	DeletedAt        *time.Time `json:"deletedAt"`
}

// ArchivedTikuListResponse 是归档列表的分页响应。
type ArchivedTikuListResponse struct {
	PageNo   int                `json:"pageNo"`
	PageSize int                `json:"pageSize"`
	Total    int64              `json:"total"`
	Items    []ArchivedTikuItem `json:"items"`
}

// archivedTikuSelect 逐列列出（含 COALESCE 兜底 NULL），不用 SELECT *：
// 归档行可能由更早的迁移写入、archived_reason 为 NULL，扫进非指针字段会报错。
const archivedTikuSelect = "id, question, type, options, answer, hash, course_name, extra, source_name, " +
	"COALESCE(archived_reason, '') AS archived_reason, archived_at, " +
	"COALESCE(archived_by_user_id, 0) AS archived_by_user_id, deleted_at"

// parseArchivedTikuTimeRange 解析时间筛选参数（接受 YYYY-MM-DD 或 RFC3339）。
// 返回值为 nil 表示「该边界未指定」，不是错误。
func parseArchivedTikuTimeRange(raw string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		value := parsed
		return &value, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", trimmed, time.Local)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// ListArchivedTikus 返回已归档题库列表（管理员归档查看视图）。
//
// ⚠️ 必须走 Unscoped/裸表查询：归档行的 deleted_at 非 NULL，
// 任何走 dao.Tiku 默认作用域的查询都永远查不到它们（total 恒为 0）。
// 全仓只有这个视图允许看到归档行（判定口径见 77-*.md §11.3）。
func ListArchivedTikus(c *gin.Context) {
	pageNo, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("pageNo", "1")))
	if err != nil || pageNo < 1 {
		pageNo = 1
	}
	pageSize, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("pageSize", "20")))
	if err != nil || pageSize < 1 {
		pageSize = 20
	}
	if pageSize > archivedTikuMaxPageSize {
		pageSize = archivedTikuMaxPageSize
	}

	reason := strings.TrimSpace(c.Query("reason"))
	keyword := strings.TrimSpace(c.Query("keyword"))
	archivedFrom, err := parseArchivedTikuTimeRange(c.Query("archivedFrom"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "archivedFrom 时间格式错误，应为 YYYY-MM-DD"})
		return
	}
	archivedTo, err := parseArchivedTikuTimeRange(c.Query("archivedTo"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "archivedTo 时间格式错误，应为 YYYY-MM-DD"})
		return
	}

	base := tikuArchiveDB().Table(entity.TableNameTiku).Where("deleted_at IS NOT NULL")
	if reason != "" {
		base = base.Where("archived_reason = ?", reason)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		base = base.Where("question LIKE ? OR course_name LIKE ? OR extra LIKE ?", like, like, like)
	}
	if archivedFrom != nil {
		base = base.Where("COALESCE(archived_at, deleted_at) >= ?", *archivedFrom)
	}
	if archivedTo != nil {
		// 只给日期时按「当天结束」包含式过滤，符合运营直觉。
		if archivedTo.Hour() == 0 && archivedTo.Minute() == 0 && archivedTo.Second() == 0 {
			*archivedTo = archivedTo.Add(24 * time.Hour)
		}
		base = base.Where("COALESCE(archived_at, deleted_at) < ?", *archivedTo)
	}

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		logger.SysError("读取归档题库总数失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "服务器错误"})
		return
	}

	items := make([]ArchivedTikuItem, 0)
	if total > 0 {
		err := base.Select(archivedTikuSelect).
			Order("COALESCE(archived_at, deleted_at) DESC, id DESC").
			Offset((pageNo - 1) * pageSize).
			Limit(pageSize).
			Find(&items).Error
		if err != nil {
			logger.SysError("读取归档题库列表失败: " + logger.RedactSecrets(err.Error()))
			c.JSON(http.StatusInternalServerError, gin.H{"message": "服务器错误"})
			return
		}
	}

	c.JSON(http.StatusOK, ArchivedTikuListResponse{
		PageNo:   pageNo,
		PageSize: pageSize,
		Total:    total,
		Items:    items,
	})
}

// RestoreArchivedTiku 把已归档题目恢复为正常题目。
//
// ⚠️ 定位必须用 Unscoped/裸表查询：默认作用域查不到 deleted_at 非 NULL 的行，
// 那样「恢复」会永远 404。
func RestoreArchivedTiku(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	currentUser, _ := c.Get("user")
	user, _ := currentUser.(*entity.User)
	if user == nil || user.ID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "未登录"})
		return
	}

	restored, err := restoreArchivedTikuQuestion(user, int32(id), strings.TrimSpace(c.Query("reason")))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"message": "归档题目不存在"})
			return
		}
		logger.SysError("恢复归档题库失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "服务器错误"})
		return
	}

	recordAccountAudit(c, user, "tiku", TikuActionRestore, "恢复归档题库", map[string]any{
		"tikuId": restored.ID,
		"ok":     true,
	})
	// 恢复等于「题库里重新出现这道题」，之前基于「没有这道题」建立的搜题缓存必须作废。
	invalidateSearchResultCaches("restore_question")

	c.JSON(http.StatusOK, gin.H{
		"message": "恢复成功",
		"id":      restored.ID,
	})
}

// restoreArchivedTikuQuestion 在单个事务内清空归档四列并写审计日志。
func restoreArchivedTikuQuestion(user *entity.User, tikuID int32, reason string) (*entity.Tiku, error) {
	if user == nil {
		return nil, errors.New("缺少操作者")
	}
	db := tikuArchiveDB()
	tx := db.Begin()
	if tx == nil || tx.Error != nil {
		if tx != nil {
			return nil, tx.Error
		}
		return nil, errors.New("开启事务失败")
	}
	defer func() {
		_ = tx.Rollback().Error
	}()

	var current entity.Tiku
	// ⚠️ 必须 Unscoped：entity.Tiku 带 gorm.DeletedAt，GORM 会给每次查询自动附加
	// `deleted_at IS NULL`，与这里手写的 `deleted_at IS NOT NULL` 冲突 → 恒 record not found，
	// 表现为「恢复功能完全不可用」。Count 不受影响（Count 不走软删除过滤），
	// 所以只看 Count 会误判为正常。
	err := tx.Unscoped().Table(entity.TableNameTiku).
		Where("id = ? AND deleted_at IS NOT NULL", tikuID).Take(&current).Error
	if err != nil {
		return nil, err
	}

	now := time.Now()
	snapshot, marshalErr := json.Marshal(map[string]any{
		"before": archiveTikuSnapshot(&current),
		"reason": reason,
	})
	if marshalErr != nil {
		return nil, marshalErr
	}

	entry := &entity.Log{
		Qid:         current.ID,
		Module:      "tiku",
		Action:      TikuActionRestore,
		UserID:      user.ID,
		Username:    user.Username,
		Description: "恢复归档题库",
		CreateTime:  now,
		OldAnswer:   string(snapshot),
	}
	if err := tikuArchiveLogCreate(tx, entry); err != nil {
		return nil, err
	}

	result := tx.Table(entity.TableNameTiku).Where("id = ?", tikuID).Updates(map[string]any{
		"deleted_at":          nil,
		"archived_reason":     nil,
		"archived_at":         nil,
		"archived_by_user_id": nil,
	})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	return &current, nil
}

// reviveArchivedTikuForImport 处理「导入的题与某条已归档题同键（hash + course_name）」。
//
// 为什么必须处理：唯一索引 hash_course_idx 建在 (hash, course_name) 上，
// 归档行仍然占着它。按「归档 = 不存在」的口径，导入应当新建一条，
// 但直接 Create 会撞唯一键 → 整个导入 500。这里复用该行、覆盖内容、清空归档标记，
// 对操作者而言等价于「新建了一条」（id 复用是实现细节，会在返回值里体现）。
//
// 返回 true 表示命中了归档行并已复活；false 表示没有归档同键行，调用方正常 Create。
func reviveArchivedTikuForImport(item *entity.Tiku) (bool, error) {
	if item == nil {
		return false, nil
	}
	db := tikuArchiveDB()
	var existing struct {
		ID int32
	}
	err := db.Table(entity.TableNameTiku).
		Select("id").
		Where("hash = ? AND course_name = ? AND deleted_at IS NOT NULL", item.Hash, item.CourseName).
		Take(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}

	updates := map[string]any{
		"question":            item.Question,
		"type":                item.Type,
		"options":             item.Options,
		"answer":              item.Answer,
		"hash":                item.Hash,
		"course_name":         item.CourseName,
		"extra":               item.Extra,
		"source_name":         item.SourceName,
		"deleted_at":          nil,
		"archived_reason":     nil,
		"archived_at":         nil,
		"archived_by_user_id": nil,
	}
	if err := db.Table(entity.TableNameTiku).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
		return false, err
	}
	item.ID = existing.ID
	return true, nil
}

// archivedTikuCount 返回已归档题目数量（供统计口径核对与测试使用）。
func archivedTikuCount() (int64, error) {
	var total int64
	err := tikuArchiveDB().Table(entity.TableNameTiku).Where("deleted_at IS NOT NULL").Count(&total).Error
	return total, err
}

// archivedReasonRuneLength 返回原因的有效长度（去空格后的字符数）。
func archivedReasonRuneLength(reason string) int {
	return utf8.RuneCountInString(strings.TrimSpace(reason))
}
