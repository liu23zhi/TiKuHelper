package controller

// 等级体系与充值阶梯档位的管理接口（94-*.md §2 C1 / C2）。
//
// 本文件只做 HTTP 适配：鉴权、参数解析、错误分类、响应包装。
// 业务读写在 service/user_level.go 与 service/recharge_promotion_tier.go。
//
// 全部 Auth + RequireAdmin，且 handler 内先跑 requireAdmin：
// 路由层挡住未登录者，handler 层再挡一次「已登录但非管理员」——
// 与本仓其它管理接口（ip_segment.go / referral.go）口径一致。

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// userLevelDB 返回管理面使用的数据库句柄（低优先级会话）。
func userLevelDB(c *gin.Context) *gorm.DB {
	return withDatabasePriority(resolveSearchPersistenceDB(), databaseRequestContext(c), dbqueue.PriorityLow)
}

// ─────────────────────── 等级 ───────────────────────

// UserLevelList 列出全部等级（含停用级）。
//
//	GET /adapter-service/admin/user-levels
func UserLevelList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	levels, err := service.ListUserLevelsWithDB(userLevelDB(c))
	if err != nil {
		writeUserLevelFailure(c, err, "获取等级列表失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": levels})
}

// UserLevelCreate 新增等级。
//
//	POST /adapter-service/admin/user-levels
func UserLevelCreate(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input service.UserLevelInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "等级参数错误"})
		return
	}
	level, err := service.CreateUserLevelWithDB(userLevelDB(c), input)
	if err != nil {
		writeUserLevelFailure(c, err, "新增等级失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": level})
}

// UserLevelUpdate 编辑等级。
//
//	PUT /adapter-service/admin/user-levels/:id
func UserLevelUpdate(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	id, ok := parseUserLevelID(c)
	if !ok {
		return
	}
	var input service.UserLevelInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "等级参数错误"})
		return
	}
	level, err := service.UpdateUserLevelWithDB(userLevelDB(c), id, input)
	if err != nil {
		writeUserLevelFailure(c, err, "保存等级失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": level})
}

// UserLevelDelete 删除等级。
//
//	DELETE /adapter-service/admin/user-levels/:id
//
// 删除会让仍指向该等级的用户回落为「无等级」并留一行 manual 流水
// （见 service.DeleteUserLevelWithDB 的注释）。
func UserLevelDelete(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	id, ok := parseUserLevelID(c)
	if !ok {
		return
	}
	if err := service.DeleteUserLevelWithDB(userLevelDB(c), id); err != nil {
		writeUserLevelFailure(c, err, "删除等级失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// UserLevelReevaluate 手动重评一批用户的等级（管理员改门槛后触发）。
//
//	POST /adapter-service/admin/user-levels/reevaluate
//	{"limit":200}
func UserLevelReevaluate(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input struct {
		Limit int `json:"limit"`
	}
	// 允许空 body（等价 limit=默认值）。
	_ = c.ShouldBindJSON(&input)
	processed, changed, err := service.ReevaluateAllUserLevelsWithDB(
		userLevelDB(c), input.Limit, "manual")
	if err != nil {
		writeUserLevelFailure(c, err, "重新评估等级失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"processed": processed, "changed": changed})
}

// UserLevelCurrent 返回**当前登录用户**自己的等级摘要（个人中心用，非管理员专属）。
//
//	GET /adapter-service/user/level
func UserLevelCurrent(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	summary, err := service.GetUserLevelSummaryWithDB(userLevelDB(c), operator.ID)
	if err != nil {
		writeUserLevelFailure(c, err, "获取等级信息失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summary})
}

// parseUserLevelID 解析路径参数 :id。非法时直接写 400 并返回 false。
func parseUserLevelID(c *gin.Context) (int32, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	id, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "等级 ID 不合法"})
		return 0, false
	}
	return int32(id), true
}

// classifyUserLevelError 把 service 层错误映射成 HTTP 状态码。
func classifyUserLevelError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	switch {
	case errors.Is(err, service.ErrUserLevelSchemaUnavailable),
		errors.Is(err, service.ErrRechargePromotionTierSchemaUnavailable):
		// 表还没建好：既不是参数错也不是「记录不存在」，用 503 让前端提示「数据表未就绪」。
		return http.StatusServiceUnavailable
	case errors.Is(err, service.ErrUserLevelNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrUserLevelNameRequired),
		errors.Is(err, service.ErrUserLevelRankConflict),
		errors.Is(err, service.ErrUserLevelThresholdInvalid):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrRechargePromotionTierThresholdConflict),
		errors.Is(err, service.ErrRechargePromotionTierThresholdInvalid),
		errors.Is(err, service.ErrRechargePromotionTierDuplicateThreshold):
		// 冲突/非法档位是**管理员可修正的输入错误**，必须是 400 而不是 500，
		// 否则前端会当成系统故障、不给管理员改的机会。
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// writeUserLevelFailure 输出错误响应。
//
// 参数类 / 未找到类 / schema 未就绪这几类直接把 service 文案下发 ——
// 它们都是固定中文，且「冲突的是哪两档」「排序号被谁占了」这些信息只有
// service 层知道，捂着只会让管理员反复试。其余用固定兜底文案。
func writeUserLevelFailure(c *gin.Context, err error, fallback string) {
	status := classifyUserLevelError(err)
	message := fallback
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusServiceUnavailable:
		message = err.Error()
	}
	c.JSON(status, gin.H{"message": message})
}

// ─────────────────────── 充值阶梯档位 ───────────────────────

// RechargePromotionTierList 返回档位表与当前生效来源。
//
//	GET /adapter-service/admin/recharge-promotion-tiers
func RechargePromotionTierList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	result, err := service.ListRechargePromotionTierRulesWithDB(userLevelDB(c))
	if err != nil {
		writeUserLevelFailure(c, err, "获取充值阶梯档位失败")
		return
	}
	c.JSON(http.StatusOK, result)
}

// RechargePromotionTierReplace 整体替换档位表。
//
//	PUT /adapter-service/admin/recharge-promotion-tiers
//	{"rows":[{"threshold":8.8,"rate":0.1,"enabled":true}]}
//
// 空数组 = 清空表 = 回退到系统默认六档（明确定义的行为，不是误操作）。
func RechargePromotionTierReplace(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input struct {
		Rows []service.RechargePromotionTierInput `json:"rows"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "档位参数错误"})
		return
	}
	result, err := service.ReplaceRechargePromotionTierRulesWithDB(userLevelDB(c), input.Rows)
	if err != nil {
		writeUserLevelFailure(c, err, "保存充值阶梯档位失败")
		return
	}
	c.JSON(http.StatusOK, result)
}

// RechargePromotionTierPreview 按提交中但尚未保存的档位试算一笔金额的赠金。
//
//	POST /adapter-service/admin/recharge-promotion-tiers/preview
//	{"paidAmount":100,"rows":[...]}
//
// 保存前的试算是刻意保留的能力：管理员调档位时最常问「充 100 送多少」，
// 没有它就只能「保存 → 去充值 → 发现算错 → 再改」。
func RechargePromotionTierPreview(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input struct {
		PaidAmount float64                              `json:"paidAmount"`
		Rows       []service.RechargePromotionTierInput `json:"rows"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "试算参数错误"})
		return
	}
	tier, gift, matched, err := service.PreviewRechargePromotionGift(input.Rows, input.PaidAmount)
	if err != nil {
		writeUserLevelFailure(c, err, "试算失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"matched":   matched,
		"threshold": tier.Threshold,
		"rate":      tier.Rate,
		"gift":      gift,
	})
}
