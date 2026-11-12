package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

// 绑定历史的管理端接口（需求 5a / 6 / 7，task_doc/70 §3.4 §3.6 §3.7）。
//
// # 一条贯穿全文件的红线
//
// `user_binding_records` 不是纯审计日志，它是 19 号（二次注册判定）与
// 44 号（换绑追缴）的**判定数据源**。所以：
//   - 读接口一律**脱敏**（手机号 138****0000、OpenID 前 6 + **** + 后 4）；
//   - 筛选只走 `value_hash` 精确匹配，不做明文 LIKE（明文会进 SQL 与慢日志）；
//   - 「删除」= `state='voided'`，**不物理删除**（70 §3.7.1 方案 A）；
//   - 「添加」只写 `released` 行，绝不插 `active`（会把值从真正持有它的账号手里抢走）；
//   - 两个写动作都**理由必填**并写双通道审计。
//
// # Action 编号登记（logs.action 是 tinyint(4)，写库后不可重排）
//
// Module = "binding"（与 account / tiku / blacklist 各自独立的号段）：
//
//	1 作废绑定历史
//	2 手工补录绑定历史
const (
	bindingLogModule = "binding"

	BindingActionVoid     int32 = 1
	BindingActionBackfill int32 = 2
)

// bindingLogSnapshot 生成写进 logs.old_answer 的快照 JSON。
//
// ⚠️ 必须是脱敏快照（service.BindingAuditSnapshot 已按此实现）：
// 审计日志长期保留、管理员可查，完整手机号 / OpenID 没有留在那里的必要。
func bindingLogSnapshot(snapshot map[string]any, reason string) string {
	payload := map[string]any{"before": snapshot, "reason": strings.TrimSpace(reason)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// recordBindingAudit 写一条绑定历史变更的双通道审计（logs + AccountAudit）。
//
// 为什么两个通道都写：logs 表是 LogList 页的数据源（运营日常查阅），
// AccountAudit 是结构化事件流（合规追溯）。二者语义不同，不得只写一条（77 §3.2.1）。
func recordBindingAudit(c *gin.Context, operator *entity.User, action int32, description string, snapshot map[string]any, reason string, result map[string]any) {
	if operator == nil {
		return
	}
	log := entity.Log{
		Qid:         0, // 非题库操作
		Module:      bindingLogModule,
		Action:      action,
		UserID:      operator.ID,
		Username:    operator.Username,
		Description: description,
		OldAnswer:   bindingLogSnapshot(snapshot, reason),
		ClientIP:    strings.TrimSpace(c.ClientIP()),
		CreateTime:  time.Now(),
	}
	if err := dao.Log.Create(&log); err != nil {
		// 审计写失败只记日志，不回滚业务结果：变更已经发生且可查，
		// 让「日志写不进去」变成「操作失败」会造成更难解释的状态。
		logger.SysError("写绑定历史操作日志失败: " + logger.RedactSecrets(err.Error()))
	}
	if result == nil {
		result = map[string]any{}
	}
	if reason != "" {
		result["reason"] = strings.TrimSpace(reason)
	}
	recordAccountAudit(c, operator, bindingLogModule, action, description, result)
}

// bindingPageFromQuery 解析分页参数（page 从 1 开始，默认 1 / 20）。
func bindingPageFromQuery(c *gin.Context) (int, int) {
	page, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("page", "1")))
	if err != nil || page < 1 {
		page = 1
	}
	pageSize, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("pageSize", "20")))
	if err != nil || pageSize < 1 {
		pageSize = 20
	}
	return page, pageSize
}

// adminUserView 是详情弹窗「账号基本信息」区块（70 §3.4）。
//
// ⚠️ 显式挑字段，**不返回 *entity.User**：整结构体会把 password hash
// 与 login_session_id 一起带出接口，而这两个字段能直接冒充登录。
type adminUserView struct {
	ID             int32      `json:"id"`
	Username       string     `json:"username"`
	Nickname       string     `json:"nickname"`
	Role           string     `json:"role"`
	Status         string     `json:"status"`
	Balance        float64    `json:"balance"`
	GiftBalance    float64    `json:"giftBalance"`
	RegisterSource string     `json:"registerSource"`
	CreatedAt      time.Time  `json:"createdAt"`
	LastLoginAt    *time.Time `json:"lastLoginAt"`
	LastLoginIP    string     `json:"lastLoginIp"`
	PhoneMasked    string     `json:"phoneMasked"`
	WechatMasked   string     `json:"wechatMasked"`
}

// buildAdminUserView 把用户实体转成脱敏详情视图。
func buildAdminUserView(user entity.User) adminUserView {
	view := adminUserView{
		ID:             user.ID,
		Username:       user.Username,
		Nickname:       user.Nickname,
		Role:           user.Role,
		Status:         user.Status,
		Balance:        user.Balance,
		GiftBalance:    user.GiftBalance,
		RegisterSource: user.RegisterSource,
		CreatedAt:      user.CreatedAt,
		LastLoginAt:    user.LastLoginAt,
		LastLoginIP:    strings.TrimSpace(user.LastLoginIP),
	}
	if user.Phone != nil {
		view.PhoneMasked = service.MaskBindingDisplayValue(entity.BindingKindPhone, *user.Phone)
	}
	if user.WechatID != nil {
		view.WechatMasked = service.MaskBindingDisplayValue(entity.BindingKindWechat, *user.WechatID)
	}
	return view
}

// GetAdminUserBindings 返回某用户的账号详情 + 当前绑定 + 绑定历史（需求 5a）。
//
// GET /adapter-service/admin/users/:id/bindings?page=&pageSize=
//
// ⚠️ 不复用 `/user/info`：那是「本人」接口，塞别人的绑定历史会形成越权面
// （任何登录用户改个 id 就能读别人的绑定）。
func GetAdminUserBindings(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	user, err := dao.User.Where(dao.User.ID.Eq(int32(id))).First()
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}

	page, pageSize := bindingPageFromQuery(c)
	db := dao.User.UnderlyingDB()
	bindings, total, err := service.ListUserBindingRecordsWithDB(db, user.ID, page, pageSize)
	if err != nil {
		logger.SysError("读取用户绑定历史失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取绑定历史失败"})
		return
	}
	current, err := service.CurrentActiveBindingsWithDB(db, user.ID)
	if err != nil {
		logger.SysError("读取用户当前绑定失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取当前绑定失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user":            buildAdminUserView(*user),
		"currentBindings": current,
		"bindings": gin.H{
			"page":     page,
			"pageSize": pageSize,
			"total":    total,
			"items":    bindings,
		},
	})
}

// ListAdminBindings 返回全站绑定历史（侧边栏「微信历史绑定 / 手机号历史绑定」共用）。
//
// GET /adapter-service/admin/bindings?kind=phone|wechat&state=&releaseReason=&value=&userId=&page=&pageSize=
//
// `value` 是**精确匹配**（内部转 value_hash）：明文模糊搜索会把真实手机号 / OpenID
// 塞进 SQL 与慢查询日志（70 红线 11）。
func ListAdminBindings(c *gin.Context) {
	kind := strings.TrimSpace(c.Query("kind"))
	if kind == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "必须指定 kind（phone 或 wechat）"})
		return
	}
	userID, _ := strconv.Atoi(strings.TrimSpace(c.Query("userId")))
	page, pageSize := bindingPageFromQuery(c)

	items, total, err := service.ListBindingRecordsWithDB(dao.User.UnderlyingDB(), service.BindingListFilter{
		Kind:          kind,
		State:         c.Query("state"),
		ReleaseReason: c.Query("releaseReason"),
		Value:         c.Query("value"),
		UserID:        int32(userID),
		Page:          page,
		PageSize:      pageSize,
	})
	if err != nil {
		if strings.Contains(err.Error(), "绑定类型只能是") || strings.Contains(err.Error(), "释放二分类只能是") {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		logger.SysError("读取绑定历史列表失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取绑定历史失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"kind":     strings.ToLower(strings.TrimSpace(kind)),
		"page":     page,
		"pageSize": pageSize,
		"total":    total,
		"items":    items,
	})
}

// VoidAdminBinding 作废一条绑定历史（需求 7 的「删除」）。
//
// DELETE /adapter-service/admin/bindings/:id?reason=必填
//
// 不物理删除：判定数据源删一行等于静默改变二次注册判定结果（70 §3.7）。
// 作废同时把 active_key 置 NULL（红线 8），否则该行继续占唯一索引。
func VoidAdminBinding(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	reason := strings.TrimSpace(c.Query("reason"))
	if len([]rune(reason)) < service.BindingReasonMinLength {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请填写作废理由（至少 2 个字符）"})
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "未登录"})
		return
	}

	before, after, err := service.VoidBindingRecordWithDB(dao.User.UnderlyingDB(), id, operator.ID, reason)
	switch {
	case errors.Is(err, service.ErrBindingAlreadyVoided):
		c.JSON(http.StatusConflict, gin.H{"message": "该绑定记录已作废"})
		return
	case errors.Is(err, service.ErrBindingActiveNotVoidable):
		// 活跃绑定作废会放开唯一索引而 users.phone 仍写着该值 —— 见 service 里的说明。
		c.JSON(http.StatusConflict, gin.H{"message": service.ErrBindingActiveNotVoidable.Error()})
		return
	case err != nil:
		logger.SysError("作废绑定历史失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "作废失败"})
		return
	}

	description := fmt.Sprintf("作废绑定历史 #%d（%s %s）", after.ID, after.Kind,
		service.MaskBindingValue(after.Kind, after.Value))
	recordBindingAudit(c, &operator, BindingActionVoid, description, before, reason, map[string]any{
		"recordId": after.ID,
		"userId":   after.UserID,
		"state":    after.State,
		"ok":       true,
	})
	c.JSON(http.StatusOK, gin.H{"message": "已作废", "id": after.ID, "state": after.State})
}

// CreateAdminBindingHistory 手工补录一条**已释放**的绑定历史（需求 7 后半句）。
//
// POST /adapter-service/admin/bindings
//
// body: {userId, kind, value, releaseReason, releaseSource?, boundAt?, releasedAt?, reason}
//
// ⚠️ 只写 released 行（红线 9）：插 active 会把值从真正持有它的账号手里抢走。
// ⚠️ 记为 source='admin_manual_backfill'：人工补录必须与真实换绑可区分。
func CreateAdminBindingHistory(c *gin.Context) {
	var payload struct {
		UserID        int32  `json:"userId"`
		Kind          string `json:"kind"`
		Value         string `json:"value"`
		ReleaseReason string `json:"releaseReason"`
		ReleaseSource string `json:"releaseSource"`
		BoundAt       string `json:"boundAt"`
		ReleasedAt    string `json:"releasedAt"`
		Reason        string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "未登录"})
		return
	}

	boundAt, err := parseOptionalBindingTime(payload.BoundAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "boundAt 时间格式错误"})
		return
	}
	releasedAt, err := parseOptionalBindingTime(payload.ReleasedAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "releasedAt 时间格式错误"})
		return
	}

	record, err := service.BackfillBindingHistoryWithDB(dao.User.UnderlyingDB(), service.BindingBackfillInput{
		UserID:        payload.UserID,
		Kind:          payload.Kind,
		Value:         payload.Value,
		ReleaseReason: payload.ReleaseReason,
		ReleaseSource: payload.ReleaseSource,
		BoundAt:       boundAt,
		ReleasedAt:    releasedAt,
		Reason:        payload.Reason,
	})
	if err != nil {
		if isBindingInputError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		logger.SysError("补录绑定历史失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "补录失败"})
		return
	}

	description := fmt.Sprintf("补录绑定历史 #%d（%s %s，%s 释放）",
		record.ID, record.Kind, service.MaskBindingDisplayValue(record.Kind, record.Value), record.ReleaseReason)
	recordBindingAudit(c, &operator, BindingActionBackfill, description,
		service.BindingAuditSnapshot(record), payload.Reason, map[string]any{
			"recordId": record.ID,
			"userId":   record.UserID,
			"state":    record.State,
			"source":   record.Source,
			"ok":       true,
		})
	c.JSON(http.StatusOK, gin.H{"message": "补录成功", "id": record.ID, "state": record.State, "source": record.Source})
}

// isBindingInputError 判断是否为「入参不合法」类错误（应回 400 而不是 500）。
//
// service 层的入参校验错误都是普通 fmt.Errorf，没有哨兵类型；
// 这里按错误文案前缀识别，集中在一次判断里，避免每个调用点各写一遍。
func isBindingInputError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, marker := range []string{
		"绑定类型只能是", "释放二分类只能是", "绑定值不能为空", "补录历史缺少用户",
		"补录历史必须给出释放二分类", "请填写补录理由", "该用户当前正绑着这个值",
		"用户不存在", "绑定值哈希与绑定值不一致",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// parseOptionalBindingTime 解析可选时间参数（接受 RFC3339 或 YYYY-MM-DD）。
func parseOptionalBindingTime(raw string) (*time.Time, error) {
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
