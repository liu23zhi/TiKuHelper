package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 管理员预生成一次性脚本激活码的管理接口。
//
// 与 script_entitlement.go 的分工：
//   - script_entitlement.go 是**用户侧**（自购、兑换自己的码、查价）；
//   - 本文件是**管理员侧**（预生成、列表、改期/禁用、看使用记录）。
//
// 四张路由全部挂 controller.Auth + middleware.RequireAdmin（见 router.go），
// 且每个 handler 内部再判一次 requireAdmin —— 这是本仓惯例（纵深防御）。
// 注意：二次判权会让「路由少挂中间件」的行为测试测不出来，所以另有一条
// 读 router.go 源码的断言（TestScriptActivationCodeRoutesMountRequireAdmin）。

// 详情里返回全码，列表里只返回后 4 位（entity.Mask）：
// 列表是常驻屏幕、可能被投屏共享的视图；核对码的场景走详情。
type adminScriptActivationCodeListItem struct {
	ID             int64      `json:"id"`
	CodeMask       string     `json:"codeMask"`
	Status         string     `json:"status"`
	Remark         string     `json:"remark"`
	CreatedBy      int32      `json:"createdBy"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	UsedAt         *time.Time `json:"usedAt,omitempty"`
	UsedBy         *int32     `json:"usedBy,omitempty"`
	UsedByUsername string     `json:"usedByUsername,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

type adminScriptActivationCodeCreateInput struct {
	Count     int        `json:"count"`
	Remark    string     `json:"remark"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

type adminScriptActivationCodeUpdateInput struct {
	Remark      *string    `json:"remark"`
	Status      *string    `json:"status"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	ClearExpiry bool       `json:"clearExpiry"`
}

// ScriptActivationCodeCreate 批量生成激活码。
func ScriptActivationCodeCreate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	var input adminScriptActivationCodeCreateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求格式不正确"})
		return
	}
	if input.Count < 1 || input.Count > service.MaxAdminScriptActivationCodeBatch {
		// 上限校验放在服务层之前给出**明确**文案：调用方需要知道上限是多少。
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "单次批量生成数量必须在 1 到 " + strconv.Itoa(service.MaxAdminScriptActivationCodeBatch) + " 之间",
		})
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请重新登录"})
		return
	}
	codes, err := service.GenerateAdminScriptActivationCodes(
		scriptDB(), operator.ID, input.Count, input.Remark, input.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, service.ErrAdminScriptActivationCodeBatchRange) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "生成激活码失败"})
		return
	}

	// 审计里**不记码本身**（审计日志会被更多角色看到，而码是可直接兑换的凭据）。
	auditAdminScriptActivationCode(c, 1, "生成脚本激活码", map[string]any{
		"count":  len(codes),
		"remark": strings.TrimSpace(input.Remark),
	})

	// 生成响应必须回明文码：这是码的唯一一次明文批量输出（管理员要发给用户）。
	payload := make([]gin.H, 0, len(codes))
	for _, code := range codes {
		payload = append(payload, gin.H{
			"id":        code.ID,
			"code":      code.Code,
			"status":    code.Status,
			"remark":    code.Remark,
			"expiresAt": code.ExpiresAt,
			"createdAt": code.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": payload, "count": len(payload)})
}

// ScriptActivationCodeList 分页列表（码只露后 4 位）。
func ScriptActivationCodeList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	usedBy, _ := strconv.Atoi(c.DefaultQuery("usedBy", "0"))

	result, err := service.ListAdminScriptActivationCodes(scriptDB(), service.AdminScriptActivationCodeListQuery{
		Status:   strings.TrimSpace(c.Query("status")),
		UsedBy:   int32(usedBy),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取激活码列表失败"})
		return
	}

	now := time.Now().UTC()
	usernames := resolveAdminScriptActivationCodeUsernames(scriptDB(), result.Items)
	items := make([]adminScriptActivationCodeListItem, 0, len(result.Items))
	for _, code := range result.Items {
		item := adminScriptActivationCodeListItem{
			ID:        code.ID,
			CodeMask:  code.Mask(),
			Status:    projectAdminScriptActivationCodeStatusForResponse(code, now),
			Remark:    code.Remark,
			CreatedBy: code.CreatedBy,
			ExpiresAt: code.ExpiresAt,
			UsedAt:    code.UsedAt,
			UsedBy:    code.UsedBy,
			CreatedAt: code.CreatedAt,
		}
		if code.UsedBy != nil {
			item.UsedByUsername = usernames[*code.UsedBy]
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"items":    items,
		"total":    result.Total,
		"page":     result.Page,
		"pageSize": result.PageSize,
	})
}

// ScriptActivationCodeDetail 详情：全码 + 使用记录。
func ScriptActivationCodeDetail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "激活码 ID 不正确"})
		return
	}
	code, err := service.GetAdminScriptActivationCodeByID(scriptDB(), id)
	if err != nil {
		if errors.Is(err, service.ErrAdminScriptActivationCodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"message": "激活码不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取激活码失败"})
		return
	}
	usages, err := service.ListAdminScriptActivationCodeUsages(scriptDB(), code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取使用记录失败"})
		return
	}

	// 详情是一次显全码的入口（客服核对用）。它会被记进审计，便于追溯谁查过。
	auditAdminScriptActivationCode(c, 2, "查看脚本激活码详情", map[string]any{"id": id})

	now := time.Now().UTC()
	c.JSON(http.StatusOK, gin.H{
		"code": gin.H{
			"id":        code.ID,
			"code":      code.Code,
			"status":    projectAdminScriptActivationCodeStatusForResponse(code, now),
			"remark":    code.Remark,
			"createdBy": code.CreatedBy,
			"expiresAt": code.ExpiresAt,
			"usedAt":    code.UsedAt,
			"usedBy":    code.UsedBy,
			"createdAt": code.CreatedAt,
		},
		"usages":   usages,
		"hasUsage": code.UsedAt != nil,
	})
}

// ScriptActivationCodeUpdate 修改有效期/备注/启用状态。
func ScriptActivationCodeUpdate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "激活码 ID 不正确"})
		return
	}
	var input adminScriptActivationCodeUpdateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求格式不正确"})
		return
	}
	updated, err := service.UpdateAdminScriptActivationCode(scriptDB(), id, service.AdminScriptActivationCodeUpdate{
		Remark:      input.Remark,
		Status:      input.Status,
		ExpiresAt:   input.ExpiresAt,
		ClearExpiry: input.ClearExpiry,
	})
	switch {
	case errors.Is(err, service.ErrAdminScriptActivationCodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "激活码不存在"})
		return
	case errors.Is(err, service.ErrAdminScriptActivationCodeUsed):
		c.JSON(http.StatusConflict, gin.H{"message": "激活码已被使用，不能修改状态"})
		return
	case errors.Is(err, service.ErrAdminScriptActivationCodeInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"message": "状态必须是 unused 或 disabled"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "修改激活码失败"})
		return
	}

	auditAdminScriptActivationCode(c, 3, "修改脚本激活码", map[string]any{"id": id})
	c.JSON(http.StatusOK, gin.H{
		"code": gin.H{
			"id":        updated.ID,
			"code":      updated.Code,
			"status":    projectAdminScriptActivationCodeStatusForResponse(updated, time.Now().UTC()),
			"remark":    updated.Remark,
			"expiresAt": updated.ExpiresAt,
			"usedAt":    updated.UsedAt,
			"usedBy":    updated.UsedBy,
		},
	})
}

// projectAdminScriptActivationCodeStatusForResponse 与 service 层的投影保持一致。
//
// 直接委托给导出的 service 函数，不复制逻辑：两份实现必然漂移，而「列表显示
// 未使用、兑换却报过期」正是漂移最伤人的表现。
func projectAdminScriptActivationCodeStatusForResponse(code entity.AdminScriptActivationCode, now time.Time) string {
	return service.ProjectAdminScriptActivationCodeStatus(code, now)
}

// resolveAdminScriptActivationCodeUsernames 批量取 used_by 对应的用户名，
// 避免列表对每一行各查一次用户表。
func resolveAdminScriptActivationCodeUsernames(db *gorm.DB, codes []entity.AdminScriptActivationCode) map[int32]string {
	usernames := make(map[int32]string, len(codes))
	ids := make([]int32, 0, len(codes))
	seen := make(map[int32]struct{}, len(codes))
	for _, code := range codes {
		if code.UsedBy == nil {
			continue
		}
		if _, ok := seen[*code.UsedBy]; ok {
			continue
		}
		seen[*code.UsedBy] = struct{}{}
		ids = append(ids, *code.UsedBy)
	}
	if db == nil || len(ids) == 0 {
		return usernames
	}
	var users []entity.User
	if err := db.Session(&gorm.Session{NewDB: true}).
		Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return usernames
	}
	for _, user := range users {
		usernames[user.ID] = user.Username
	}
	return usernames
}

// auditAdminScriptActivationCode 写一条审计。
//
// 刻意**不把激活码本身写进审计字段**：码是可直接兑换的凭据，审计日志的读取
// 面（LogList 等）比激活码管理页更宽。
func auditAdminScriptActivationCode(c *gin.Context, action int32, description string, fields map[string]any) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	recordAccountAudit(c, &operator, "script_activation_code", action, description, fields)
}
