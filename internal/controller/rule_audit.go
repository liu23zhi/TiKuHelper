package controller

// 规则类（黑名单规则 / 评价规则 / 支付方式）删除的审计实现。
//
// 权威依据：用户对 Q77 的拍板（task_doc/45-用户待确认问题清单.md §11.4 与 §11.8）：
//
//	Q77-2 **否**，但是要在日志完整记录。  → 规则类仍做**物理删除**，但删除前必须留完整快照
//	Q77-6 **C**，可以下拉选择，也可以自己填写。→ 删除必须填原因，后端独立校验
//
// 三条不可协商的性质（少一条这套机制就等于没有）：
//
//  1. **快照必须在 DELETE 之前取**。删完之后那一行已经查不到了 ——
//     这正是「先删后记」这个错误写法唯一能骗过自己的地方：日志行确实写进去了，
//     但 OldAnswer 里是一个空对象。
//  2. **写日志与物理删除在同一个事务里**。任一失败整体回滚：
//     接受「日志多一条但没删」，绝不接受「删了但没日志」。
//  3. **快照按字段白名单显式构造**，不做 `json.Marshal(整结构体)`。
//     整结构体会把主键与派生列（value_hash / value_masked）一起带进去，
//     而且以后实体新增字段会**静默**改变日志格式（历史行与新行形状不一致）。
//
// ⚠️ 为什么不再额外调 recordAccountAudit：本仓的 `recordAccountAudit`
// （account_audit.go:15）最终就是往 **同一张 `logs` 表**写一行
// （service/account_audit.go `WriteAccountAuditWithDB` 里 `db.Create(&entity.Log{...})`）。
// 再调一次只会让同一个删除动作在 LogList 里出现两行、其中一行还被白名单过滤成空壳。
// 因此这里写的这一行**同时承担两个通道的职责**：Description 给人看，
// OldAnswer 里的完整快照给追溯/重建用；而且快照字段是白名单式的，凭据类字段进不来。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// ruleDeleteModuleBlacklist 是 logs.module 的取值（黑名单规则）。
	ruleDeleteModuleBlacklist = "blacklist"
	// ruleDeleteModuleReviewRule 是 logs.module 的取值（评价返现规则）。
	ruleDeleteModuleReviewRule = "review_rule"
	// ruleDeleteModulePaymentMethod 是 logs.module 的取值（支付方式）。
	ruleDeleteModulePaymentMethod = "payment_method"

	// minRuleDeleteReasonRunes 是删除原因去空白后的最小字符数（Q77-6）。
	minRuleDeleteReasonRunes = 2

	// ruleDeleteReasonRequiredMessage 是缺原因时的 400 文案（与 45-*.md §11.8 一致）。
	ruleDeleteReasonRequiredMessage = "请填写删除原因"
)

// requireRuleDeleteReason 读取并校验删除原因（query 参数 `reason`）。
//
// **前端下拉框上的 required 不算数**：管理员直接 curl 这个接口必须同样被拒，
// 所以校验放在服务端入口，任何一条删除路由都绕不过去。
func requireRuleDeleteReason(c *gin.Context) (string, bool) {
	reason := strings.TrimSpace(c.Query("reason"))
	if len([]rune(reason)) < minRuleDeleteReasonRunes {
		c.JSON(http.StatusBadRequest, gin.H{"message": ruleDeleteReasonRequiredMessage})
		return "", false
	}
	return reason, true
}

// archiveRuleDeleteInTx 在同一事务内先写完整快照日志，再执行物理删除。
//
// 顺序不可颠倒：删除后记录就查不到了（快照只会剩一个空对象）。
//
// 写入内容：
//   - Module = module（blacklist / review_rule / payment_method）
//   - Action = RuleActionDelete（61）
//   - UserID / Username = **操作者**（不是被删对象；Q77-5 的口径是「谁执行的」）
//   - Description = `删除<对象> <标识>，原因：<reason>`
//   - OldAnswer = `{"before": <完整快照>, "reason": "<reason>"}`（可据此重建规则）
//   - NewAnswer 留空：这一行没有「修改后」的值，留空同时让它与审计行可区分
func archiveRuleDeleteInTx(tx *gorm.DB, module, target string, snapshot map[string]any,
	operator *entity.User, reason, clientIP string) error {
	if tx == nil {
		return errors.New("删除快照日志写入失败：数据库不可用")
	}
	if snapshot == nil {
		// 空快照等于没有留痕，按错误处理：宁可整笔删除回滚。
		return errors.New("删除快照日志写入失败：快照为空")
	}
	payload, err := json.Marshal(map[string]any{
		"before": snapshot,
		"reason": reason,
	})
	if err != nil {
		return fmt.Errorf("序列化删除快照失败：%w", err)
	}

	entry := entity.Log{
		Module:      strings.TrimSpace(module),
		Action:      RuleActionDelete,
		Description: truncateAuditDescription(strings.TrimSpace("删除" + target + "，原因：" + reason)),
		Qid:         0,
		OldAnswer:   string(payload),
		NewAnswer:   "",
		ClientIP:    strings.TrimSpace(clientIP),
		CreateTime:  time.Now().In(logger.ShanghaiLocation()),
	}
	if operator != nil {
		entry.UserID = operator.ID
		entry.Username = strings.TrimSpace(operator.Username)
	}
	if entry.Username == "" {
		entry.Username = "anonymous"
	}

	// 同一个事务句柄上刚查过别的 model（BlacklistEntry / ReviewRebateRule / PaymentMethod），
	// Statement 里还挂着那份 schema。不 NewDB 就直接 Create(&entity.Log{})，
	// GORM 会拿前一个 model 的字段表去 reflect 这条 Log（本仓实测会
	// `panic: reflect: Field index out of range`）。NewDB 清 schema、保留同一连接与事务。
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&entry).Error; err != nil {
		return fmt.Errorf("写入删除快照日志失败：%w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 三类规则的快照字段清单（写死，不用整结构体序列化）
// ---------------------------------------------------------------------------

// blacklistRuleSnapshot 记黑名单规则**全部业务字段**。
//
// 不入快照：
//   - id：重建时由数据库重新分配，写进去只会误导（也不能用它来「复活」同一行）；
//   - value_hash / value_masked：都是 pattern 的派生物，重建时由 service 重新算，
//     存进来等于存了两份可能不一致的副本；
//   - updated_at：删除时点没有意义。
func blacklistRuleSnapshot(entry entity.BlacklistEntry) map[string]any {
	snapshot := map[string]any{
		"kind":      entry.Kind,
		"pattern":   entry.Pattern,
		"matchType": entry.MatchType,
		"reason":    entry.Reason,
		"enabled":   entry.Enabled,
		"createdBy": entry.CreatedBy,
		"createdAt": formatRuleSnapshotTime(entry.CreatedAt),
		"expiresAt": nil,
	}
	// 过期时间必须区分「永久（null）」与「某时刻」，否则重建时会凭空变成永久规则。
	if entry.ExpiresAt != nil {
		snapshot["expiresAt"] = formatRuleSnapshotTime(*entry.ExpiresAt)
	}
	return snapshot
}

// reviewRebateRuleSnapshot 记评价返现规则**全部业务字段**。
//
// programId 必须进快照：规则脱离活动就没有意义，重建时要能挂回同一个活动。
func reviewRebateRuleSnapshot(rule entity.ReviewRebateRule) map[string]any {
	return map[string]any{
		"programId":       rule.ProgramID,
		"minStars":        rule.MinStars,
		"maxStars":        rule.MaxStars,
		"minContentScore": rule.MinContentScore,
		"maxContentScore": rule.MaxContentScore,
		"amount":          rule.Amount,
		"priority":        rule.Priority,
		"enabled":         rule.Enabled,
		"remark":          rule.Remark,
		"createdAt":       formatRuleSnapshotTime(rule.CreatedAt),
	}
}

// paymentMethodSnapshot 记支付方式**全部业务字段**。
//
// iconSvg 也进快照：它是运营手填的图标内容，删了就找不回来（不是可再生的资源路径）。
func paymentMethodSnapshot(method entity.PaymentMethod) map[string]any {
	return map[string]any{
		"name":      method.Name,
		"iconSvg":   method.IconSVG,
		"epayType":  method.EPayType,
		"enabled":   method.Enabled,
		"sort":      method.Sort,
		"remark":    method.Remark,
		"createdAt": formatRuleSnapshotTime(method.CreatedAt),
	}
}

// formatRuleSnapshotTime 把时间统一成 UTC RFC3339（秒级），保证日志行可比、可解析。
// 零值返回空串：`0001-01-01T00:00:00Z` 容易被误读成「真的发生过」。
func formatRuleSnapshotTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// 三类的「查 → 写快照 → 物理删除」入口（都在同一个事务内）
// ---------------------------------------------------------------------------

// deleteBlacklistEntryWithSnapshot 删除一条黑名单规则，并在同一事务内先写完整快照。
//
// 物理删除语句仍然只在 service.DeleteBlacklistEntryWithDB 里出现一次，
// 这里只是在同一个事务里**先**补一条快照 —— 不复制删除逻辑，避免出现第二条
// 「不带快照的删除路径」。
func deleteBlacklistEntryWithSnapshot(db *gorm.DB, id int64, operator entity.User,
	reason, clientIP string) (entity.BlacklistEntry, error) {
	var entry entity.BlacklistEntry
	if db == nil {
		return entry, service.ErrBlacklistCacheNoDatabase
	}
	if id <= 0 {
		return entry, service.ErrBlacklistEntryNotFound
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if !tx.Migrator().HasTable(entity.TableNameBlacklistEntry) {
			return service.ErrBlacklistEntryNotFound
		}
		// 1) 先查：快照必须在 DELETE 之前取。
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).Take(&entry).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return service.ErrBlacklistEntryNotFound
			}
			return fmt.Errorf("读取黑名单规则失败：%w", err)
		}
		// 2) 再记：同一事务内写快照日志。
		target := "黑名单规则 " + entry.ValueMasked
		if strings.TrimSpace(entry.ValueMasked) == "" {
			target = "黑名单规则 " + entry.Pattern
		}
		if err := archiveRuleDeleteInTx(tx, ruleDeleteModuleBlacklist, target,
			blacklistRuleSnapshot(entry), &operator, reason, clientIP); err != nil {
			return err
		}
		// 3) 最后删：失败则连同日志一起回滚。
		_, err := service.DeleteBlacklistEntryWithDB(tx.Session(&gorm.Session{NewDB: true}), id)
		return err
	})
	if err != nil {
		return entity.BlacklistEntry{}, err
	}
	return entry, nil
}

// deleteReviewRebateRuleWithSnapshot 删除一条评价返现规则，并在同一事务内先写完整快照。
func deleteReviewRebateRuleWithSnapshot(db *gorm.DB, id int32, operator entity.User,
	reason, clientIP string) error {
	if db == nil {
		return service.ErrReviewRebateSchemaUnavailable
	}
	if id <= 0 {
		return service.ErrReviewRebateRuleNotFound
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if !service.ReviewRebateSchemaAvailable(tx) {
			return service.ErrReviewRebateSchemaUnavailable
		}
		var rule entity.ReviewRebateRule
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).Take(&rule).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return service.ErrReviewRebateRuleNotFound
			}
			return fmt.Errorf("读取评价返现规则失败：%w", err)
		}
		target := fmt.Sprintf("评价返现规则 #%d", rule.ID)
		if err := archiveRuleDeleteInTx(tx, ruleDeleteModuleReviewRule, target,
			reviewRebateRuleSnapshot(rule), &operator, reason, clientIP); err != nil {
			return err
		}
		return service.DeleteReviewRebateRuleWithDB(tx.Session(&gorm.Session{NewDB: true}), id)
	})
}

// deletePaymentMethodWithSnapshot 删除一个支付方式，并在同一事务内先写完整快照。
func deletePaymentMethodWithSnapshot(db *gorm.DB, paymentMethodID int32, operator entity.User,
	reason, clientIP string) error {
	if db == nil {
		return service.ErrPaymentMethodNotFound
	}
	if paymentMethodID <= 0 {
		return service.ErrPaymentMethodNotFound
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var method entity.PaymentMethod
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", paymentMethodID).First(&method).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return service.ErrPaymentMethodNotFound
			}
			return fmt.Errorf("读取支付方式失败：%w", err)
		}
		target := fmt.Sprintf("支付方式 %s", method.Name)
		if err := archiveRuleDeleteInTx(tx, ruleDeleteModulePaymentMethod, target,
			paymentMethodSnapshot(method), &operator, reason, clientIP); err != nil {
			return err
		}
		return service.DeletePaymentMethodWithDB(tx.Session(&gorm.Session{NewDB: true}), paymentMethodID)
	})
}
