package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 绑定值明文揭示（单条，管理员专属，必留审计）。
//
// # 为什么需要这个接口
//
// 绑定历史的所有读接口一律脱敏（`valueMasked`：138****1111 / OpenID 前 6 + **** + 后 4），
// 原始值**根本没有下发**（70 §3.4 / 红线 10）。这是有意的：绑定值是账号身份凭据，
// 列表页只需要「能不能认出来」。
//
// 但客服核验场景确实需要完整值（用户来电说「我这个号绑的是 138 多少」，
// 只给 138****1111 无法核对）。用户 2026-11-15 拍板的方案是：
// **加一个「显示完整值」按钮并记审计日志，而不是默认明文。**
//
// 因此本接口的形态被三条约束钉死：
//
//  1. **单条**。URL 带 binding record id，返回值是一个对象，没有任何批量形态
//     （不存在 `?ids=` / 列表返回明文的可能）。
//  2. **必须 RequireAdmin**（路由级）+ handler 内 `isAdminUser` 二次校验
//     （纵深防御：监控/重构把中间件摘掉时行为断言仍能挡住）。
//  3. **每次揭示都写审计**，且审计里**只有脱敏值**。明文绝不进日志、绝不进
//     Description、绝不进 logs.old_answer / new_answer。
//
// # 为什么用 binding record id 而不是 archivedUserId
//
// 任务书给的是 `/admin/archived-users/:id/bindings/:bindingId/reveal`，
// 但实测两个读入口服务的是**不同**人群，而按钮要加在「活跃用户」那个：
//
//	GET /admin/users/:id/bindings      → 用户管理页「查看」弹窗（**活跃账号**，
//	                                     service.ListUserBindingRecordsWithDB）
//	GET /admin/archived-users/:id      → 归档详情（archived_user.go:309 的
//	                                     maskArchivedBindingValue）
//
// 若把揭示路由挂在 archived-users 前缀下，用户管理页那个弹窗根本调不通
// （账号不是归档态），前端按钮就成死的。user_binding_records.id 是全局唯一
// 主键，两条读路径命中同一张表，所以用 `/admin/bindings/:id/reveal` 一处服务两边
// —— 与同族 `POST /admin/bindings/:id/void` 的 id 语义完全一致。
//
// # Action 编号（logs.action 是 tinyint，写库后不可重排）
//
//	62 查看绑定值明文（本文件）
//
// 已登记占用：0-3 通用 CRUD、4-9 各模块内部、10-16 认证类、50-58 账号注销与
// 保留区、59-60 题库归档/恢复、61 规则类删除快照（rule_action.go）。
// rule_action.go 的注释写明「新增编号只能从 62 往后取」，故取 62。
const (
	// AccountActionBindingReveal 是「管理员查看绑定值明文」的 logs.action。
	AccountActionBindingReveal int32 = 62

	// bindingRevealLogModule 与账户域其余高危操作同模块（account_log.go）。
	//
	// ⚠️ 已知展示层缺口（如实记录，本次不改）：web/src/utils/logListHelpers.mjs 的
	// LOG_MODULE_OPTIONS 里**没有** account 这一项，后端 buildLogProject 也只对
	// "auth" 返回「账号」。所以这条审计在 LogList 页的「模块」列显示为原始值
	// `account`、project 列显示 `-`。补标的动作会动到 logTableLayout.test.mjs
	// 里 `LOG_MODULE_OPTIONS.slice(-3)` 的断言（它钉死了末尾三项），属于另一个
	// 改动面，留给日志展示的负责人统一处理。
	bindingRevealLogModule = accountLogModule
)

// bindingRevealView 是 reveal 接口的响应体（**单条**）。
//
// 同时给 value 与 valueMasked：前者是本次揭示的目标，后者让前端/日志能
// 用同一个对象做「显示的是不是原来那一行」的自校验，也避免前端自己再算一遍脱敏。
type bindingRevealView struct {
	ID               int64      `json:"id"`
	UserID           int32      `json:"userId"`
	UsernameSnapshot string     `json:"usernameSnapshot"`
	Kind             string     `json:"kind"`
	Value            string     `json:"value"`
	ValueMasked      string     `json:"valueMasked"`
	State            string     `json:"state"`
	ReleaseReason    string     `json:"releaseReason"`
	ReleaseSource    string     `json:"releaseSource"`
	BoundAt          *time.Time `json:"boundAt,omitempty"`
	ReleasedAt       *time.Time `json:"releasedAt,omitempty"`
	RevealedAt       time.Time  `json:"revealedAt"`
}

// loadBindingRecordForReveal 按主键取一条绑定记录。
//
// 用 Session(NewDB:true)：dao.User 的 Statement 带着 users 的 model/schema，
// 直接复用它查另一个 model 会让 GORM 用错的字段索引反射并 panic
// （见 archivedUserDB 的同类注释）。
func loadBindingRecordForReveal(db *gorm.DB, recordID int64) (entity.UserBindingRecord, error) {
	var record entity.UserBindingRecord
	if db == nil || recordID <= 0 {
		return record, gorm.ErrRecordNotFound
	}
	// 绑定表可能还没上线（空库 / 精简单测库）：按「不存在」处理而不是 500。
	if !db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return record, gorm.ErrRecordNotFound
	}
	err := db.Session(&gorm.Session{NewDB: true}).
		Where("id = ?", recordID).Take(&record).Error
	return record, err
}

// RevealBindingValue 返回**单条**绑定记录的明文值，并写审计。
//
// GET /adapter-service/admin/bindings/:id/reveal
//
// 响应：200 单条对象（见 bindingRevealView）
//
//	403 非管理员（且**不写**审计：被拒绝的请求没有「谁看了什么」可记）
//	404 该 binding record 不存在
//	500 数据库不可用 / 读取失败
func RevealBindingValue(c *gin.Context) {
	operator, _ := c.Get(`user`)
	currentUser, _ := operator.(*entity.User)
	if !isAdminUser(currentUser) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限查看绑定值"})
		return
	}
	recordID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || recordID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	db := archivedUserDB()
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库不可用"})
		return
	}
	record, err := loadBindingRecordForReveal(db, recordID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"message": "绑定记录不存在"})
		return
	}
	if err != nil {
		logger.SysError("读取绑定记录失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取绑定记录失败"})
		return
	}

	// 明文只走响应，不进任何日志字段。
	masked := maskBindingValueForReveal(record.Kind, record.Value)
	// 防缓存：明文不能被中间层/浏览器缓存住（同一个 URL 二次请求只会多一次审计）。
	c.Header("Cache-Control", "no-store")

	recordAccountReveal(c, currentUser, record, masked)
	c.JSON(http.StatusOK, gin.H{"data": bindingRevealView{
		ID:               record.ID,
		UserID:           record.UserID,
		UsernameSnapshot: record.UsernameSnapshot,
		Kind:             record.Kind,
		Value:            record.Value,
		ValueMasked:      masked,
		State:            record.State,
		ReleaseReason:    record.ReleaseReason,
		ReleaseSource:    record.ReleaseSource,
		BoundAt:          record.BoundAt,
		ReleasedAt:       record.ReleasedAt,
		RevealedAt:       time.Now(),
	}})
}

// maskBindingValueForReveal 复用**唯一**的脱敏实现点。
//
// 不要再写第三份：user_binding_record_admin.go:59 的注释已说明
// 「三份脱敏一旦口径不同，最宽的那份就是真泄漏」。
func maskBindingValueForReveal(kind, value string) string {
	return maskArchivedBindingValue(kind, value)
}

// recordAccountReveal 为一次明文揭示写双通道审计（logs 表 + AccountAudit）。
//
// # 一条不可协商的红线
//
// Description 与两个快照里**只有脱敏值**。明文参数根本没有传进来
// （调用方传的是 `masked`），所以「某天有人顺手把 record.Value 塞进快照」
// 这类回退在编译期就不可能发生 —— 这是刻意的签名设计，不是省事。
func recordAccountReveal(c *gin.Context, operator *entity.User, record entity.UserBindingRecord, masked string) {
	if operator == nil {
		return
	}
	// Description 覆盖四个事实：账号 id、绑定记录 id、类型、脱敏值。
	// 明文不在其中，也不在下面任何字段里。
	description := fmt.Sprintf("查看绑定值明文：账号 #%d 绑定 #%d（%s %s）",
		record.UserID, record.ID, record.Kind, masked)

	writeAccountOperationLog(c, operator, AccountActionBindingReveal, description, map[string]any{
		"recordId":    record.ID,
		"userId":      record.UserID,
		"kind":        record.Kind,
		"valueMasked": masked,
		"state":       record.State,
		"source":      record.Source,
	})

	// Result 只放 accountAuditFields 白名单内的键（service/account_audit.go）：
	// 白名单外的键会被**静默丢弃**，写 recordId/kind/valueMasked 会得到一份
	// 看起来写了、实际只剩空对象的审计。所以把 kind 与脱敏值合并进 remark，
	// recordId 已经在 Description 里。
	recordAccountAudit(c, operator, bindingRevealLogModule, AccountActionBindingReveal, description, map[string]any{
		"targetUserId": record.UserID,
		"username":     record.UsernameSnapshot,
		"remark":       fmt.Sprintf("bindingId=%d kind=%s valueMasked=%s", record.ID, record.Kind, masked),
	})
}
