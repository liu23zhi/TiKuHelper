package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件是 `user_binding_records` 的**唯一读写落点**。
//
// 它同时服务三件事，改动时这三条都要一起想：
//  1. `18-账号注销与保留区.md`：注销/换绑时把原绑定值搬进来（released 态），保留区回看；
//  2. `19-二次注册判定与绑定记录.md`：注册时按 value_hash 判「这个值以前属于谁」；
//  3. `44-二次注册四情形与追缴定案.md`：换绑时用同一份判定触发追缴。
//
// 字段语义（尤其是 release_reason 二分类 vs release_source 操作来源）见
// `internal/entity/user_binding_record.go` 的类型注释，那里有完整的原因说明。

// BindingValueHash 返回绑定值的检索哈希：sha256(规范化值) 的 hex。
//
// 规范化在这里**只做 TrimSpace**，不做大小写折叠：
//   - 手机号在调用前已由 NormalizeMainlandPhoneInput 归一（digits-only）；
//   - 微信 OpenID 大小写敏感，折叠会把两个不同的号判成同一个（44-*.md 追缴错人）。
func BindingValueHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

// bindingNormalizeKind 只接受 phone / wechat，其余一律视为非法。
func bindingNormalizeKind(kind string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(kind))
	if normalized != entity.BindingKindPhone && normalized != entity.BindingKindWechat {
		return "", fmt.Errorf("绑定类型只能是 phone 或 wechat，收到 %q", kind)
	}
	return normalized, nil
}

// bindingNormalizeReleaseReason 把判定二分类收敛到只允许的三个值。
//
// 空串表示「这条记录没有被释放」（只对 active 行合法）。非法值直接报错而不是
// 静默写成空串 —— 写错一个二分类会让 19 号四个开关**静默**退化，不能吞。
func bindingNormalizeReleaseReason(reason string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(reason)); normalized {
	case "", entity.BindingReleaseReasonRebind, entity.BindingReleaseReasonClosure, entity.BindingReleaseReasonAdmin:
		return normalized, nil
	default:
		return "", fmt.Errorf("释放二分类只能是 rebind / closure / admin，收到 %q", reason)
	}
}

// RecordBindingActiveWithDB 记录一条**活跃**绑定（注册、绑定、抢号之后调用）。
//
// 语义：
//   - 同一个 (kind, value_hash) 已有活跃行 → 幂等返回，不重复插入；
//   - 已存在同一用户的同名活跃行（值变了）→ 由调用方先用
//     ReleaseBindingInTx 释放旧的，本函数只管新增；
//   - ActiveKey = ValueHash，占住 (kind, active_key) 唯一索引。
//
// 唯一索引冲突（并发下另一个事务先插了同值）按**已存在**处理：调用方要的结果
// 「这个值现在属于这个用户」在那种情形下已经由对方满足了，报错只会制造噪声。
func RecordBindingActiveWithDB(tx *gorm.DB, record entity.UserBindingRecord) (entity.UserBindingRecord, error) {
	if tx == nil {
		return record, fmt.Errorf("数据库不可用")
	}
	kind, err := bindingNormalizeKind(record.Kind)
	if err != nil {
		return record, err
	}
	value := strings.TrimSpace(record.Value)
	if value == "" {
		return record, fmt.Errorf("绑定值不能为空")
	}
	if record.UserID <= 0 {
		return record, fmt.Errorf("绑定记录缺少用户")
	}
	valueHash := BindingValueHash(value)
	if record.ValueHash != "" && record.ValueHash != valueHash {
		// 传进来的哈希与值不一致 → 判定会与展示错位，必须拒绝而不是二选一。
		return record, fmt.Errorf("绑定值哈希与绑定值不一致")
	}

	existing, err := findActiveBindingByValue(tx, kind, valueHash)
	if err != nil {
		return record, err
	}
	if existing != nil {
		return *existing, nil
	}

	record.Kind = kind
	record.Value = value
	record.ValueHash = valueHash
	record.State = entity.BindingStateActive
	record.ActiveKey = &valueHash
	record.ReleaseReason = ""
	record.ReleaseSource = ""
	record.ReleasedAt = nil
	record.UsernameSnapshot = strings.TrimSpace(record.UsernameSnapshot)
	record.Source = strings.ToLower(strings.TrimSpace(record.Source))
	if record.BoundAt == nil {
		now := time.Now()
		record.BoundAt = &now
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
		// 唯一索引兜底：并发插入同值。回读一次，读到就按已存在处理。
		if again, findErr := findActiveBindingByValue(tx, kind, valueHash); findErr == nil && again != nil {
			return *again, nil
		}
		return record, err
	}
	return record, nil
}

// findActiveBindingByValue 按 (kind, value_hash, state='active') 找活跃绑定。
//
// `state != 'voided'` 是显式冗余：本查询已经用 state='active' 收窄，
// 作废行本来就不在其中。写上它是为了 **grep 可验证**——
// 判定查询与「作废」的隔离必须是能一眼查到的约定，而不是「因为另一处写了 active」。
func findActiveBindingByValue(tx *gorm.DB, kind, valueHash string) (*entity.UserBindingRecord, error) {
	var rows []entity.UserBindingRecord
	if err := tx.Session(&gorm.Session{NewDB: true}).
		Where("kind = ? AND value_hash = ? AND state = ? AND state != 'voided'", kind, valueHash, entity.BindingStateActive).
		Order("id asc").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// ReleaseBindingInTx 把某个用户某类绑定的活跃行翻成 released，并返回被释放的行。
//
// ⚠️ `state` 与 `active_key` 必须**同一条 UPDATE 里一起翻转**（18-*.md §4.2 的漏洞）：
// 只改 state 不置 NULL，那条行会继续占着 (kind, active_key) 唯一索引，
// 别人再绑同一个值就撞唯一约束，报错文案与手机号毫无字面关系。
//
// 返回 nil 表示该用户此类没有活跃行（例如本来就没绑过），调用方按「无旧值」继续。
//
// releaseReason 是**二分类**（rebind / closure / admin），releaseSource 是**操作来源**
// （user_unbind / admin_clear / force_reassign / account_close / admin_delete）。
func ReleaseBindingInTx(tx *gorm.DB, userID int32, kind, releaseReason, releaseSource string, operatorUserID int32) (*entity.UserBindingRecord, error) {
	if tx == nil {
		return nil, fmt.Errorf("数据库不可用")
	}
	kind, err := bindingNormalizeKind(kind)
	if err != nil {
		return nil, err
	}
	reason, err := bindingNormalizeReleaseReason(releaseReason)
	if err != nil {
		return nil, err
	}
	if reason == "" {
		// released 行必须有二分类，否则 19 号四个开关会把它当「无法判定」而两边都不命中。
		return nil, fmt.Errorf("释放绑定必须给出二分类（rebind / closure / admin）")
	}
	var rows []entity.UserBindingRecord
	if err := tx.Session(&gorm.Session{NewDB: true}).
		Where("user_id = ? AND kind = ? AND state = ? AND state != 'voided'", userID, kind, entity.BindingStateActive).
		Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	now := time.Now()
	// 正常只会有一行；历史脏数据可能有多行，全部翻转（不去猜哪一行是对的）。
	for i := range rows {
		updated := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
			Where("id = ? AND state = ?", rows[i].ID, entity.BindingStateActive).
			Updates(map[string]any{
				"state":            entity.BindingStateReleased,
				"active_key":       nil,
				"released_at":      now,
				"release_reason":   reason,
				"release_source":   strings.ToLower(strings.TrimSpace(releaseSource)),
				"operator_user_id": operatorUserID,
			})
		if updated.Error != nil {
			return nil, updated.Error
		}
		if updated.RowsAffected != 1 {
			return nil, ErrBindingStateChanged
		}
	}
	released := rows[0]
	released.State = entity.BindingStateReleased
	released.ActiveKey = nil
	released.ReleasedAt = &now
	released.ReleaseReason = reason
	released.ReleaseSource = strings.ToLower(strings.TrimSpace(releaseSource))
	released.OperatorUserID = operatorUserID
	return &released, nil
}

// ErrBindingStateChanged 表示释放时活跃行已被别的事务翻转（并发换绑）。
var ErrBindingStateChanged = fmt.Errorf("绑定状态已变化，请刷新后重试")

// BindingLookup 是一次注册/换绑判定需要的四个布尔值（44-*.md §1.2 的判定分流）。
//
// 四个值是**互不重叠的集合**，这是四开关不退化成一个的前提：
//
//	HistoryPhone  ← state=released AND release_reason IN ('rebind','admin')
//	CurrentPhone  ← state=released AND release_reason = 'closure'
//
// 同一行**只会**落进其中一边，绝不会两边都命中。若实现把它们写成同一个查询，
// 情形 2/4（current_* 命中注销释放的值）就永远不触发，而且不报错。
type BindingLookup struct {
	HistoryPhone  bool
	HistoryWechat bool
	CurrentPhone  bool
	CurrentWechat bool
}

// AnyHistory 表示手机号或微信命中「历史绑定」。
func (lookup BindingLookup) AnyHistory() bool {
	return lookup.HistoryPhone || lookup.HistoryWechat
}

// AnyCurrent 表示手机号或微信命中「当前绑定（注销释放）」。
func (lookup BindingLookup) AnyCurrent() bool {
	return lookup.CurrentPhone || lookup.CurrentWechat
}

// Any 表示任一维度命中（换绑追缴的触发条件，44-*.md §5.2）。
func (lookup BindingLookup) Any() bool {
	return lookup.AnyHistory() || lookup.AnyCurrent()
}

// bindingHistoryReleaseReasons 是归入 history_* 的二分类集合。
//
// `admin` 也在这里 —— 用户 2026-11-09 定案「管理员手工释放归为历史绑定」（44-*.md §5.1）。
var bindingHistoryReleaseReasons = []string{
	entity.BindingReleaseReasonRebind,
	entity.BindingReleaseReasonAdmin,
}

// LookupBindingHistoryWithDB 判断一组绑定值是否命中历史/当前绑定。
//
// phone / wechat 传空串表示本次没有携带该维度（不查询、恒 false）。
// 四个条件在最外层是 OR 关系（19-*.md §3.3 / 44-*.md §2），这里只负责取四个布尔值，
// 与开关的组合由调用方决定 —— 这样开关全关时可以完全不调本函数（零查询）。
//
// 查询次数：每个非空维度 1 次带索引的 EXISTS（索引 idx_binding_lookup）。
func LookupBindingHistoryWithDB(db *gorm.DB, phone, wechat string) (BindingLookup, error) {
	var lookup BindingLookup
	if db == nil {
		return lookup, nil
	}
	if !db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		// 表还没建出来（极端空库首次启动）：一律按「未命中」，即照常赠送。
		// 这与「开关默认关闭」同向：功能没就绪不该悄悄扣掉用户的钱。
		return lookup, nil
	}
	phone = strings.TrimSpace(phone)
	if phone != "" {
		history, current, err := lookupBindingByValue(db, entity.BindingKindPhone, BindingValueHash(phone))
		if err != nil {
			return lookup, err
		}
		lookup.HistoryPhone, lookup.CurrentPhone = history, current
	}
	wechat = strings.TrimSpace(wechat)
	if wechat != "" {
		history, current, err := lookupBindingByValue(db, entity.BindingKindWechat, BindingValueHash(wechat))
		if err != nil {
			return lookup, err
		}
		lookup.HistoryWechat, lookup.CurrentWechat = history, current
	}
	return lookup, nil
}

// lookupBindingByValue 返回某个值在 released 行列上的 (history, current) 两个布尔。
//
// 两次独立 EXISTS：它们查询的 release_reason 集合**互斥**，所以同一条记录
// 只可能让其中一个是 true —— 这正是「两种释放必须分开记录」的直接体现。
func lookupBindingByValue(db *gorm.DB, kind, valueHash string) (history bool, current bool, err error) {
	history, err = bindingReleasedExists(db, kind, valueHash, bindingHistoryReleaseReasons)
	if err != nil {
		return false, false, err
	}
	current, err = bindingReleasedExists(db, kind, valueHash, []string{entity.BindingReleaseReasonClosure})
	if err != nil {
		return false, false, err
	}
	return history, current, nil
}

func bindingReleasedExists(db *gorm.DB, kind, valueHash string, reasons []string) (bool, error) {
	var count int64
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Where("kind = ? AND value_hash = ? AND state = ? AND state != 'voided' AND release_reason IN ?",
			kind, valueHash, entity.BindingStateReleased, reasons).
		Limit(1).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// FindReleasedBindingByValueWithDB 返回某个值最近的一条 released 绑定记录。
//
// 换绑追缴要用它拿到 **bindingRecordID**（幂等键的一部分）以及展示用的脱敏值来源。
// 只认 released 行：活跃行意味着「别人正在用」，那不是「曾获赠过的旧值」。
func FindReleasedBindingByValueWithDB(db *gorm.DB, kind, value string) (*entity.UserBindingRecord, error) {
	kind, err := bindingNormalizeKind(kind)
	if err != nil {
		return nil, err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if !db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return nil, nil
	}
	var rows []entity.UserBindingRecord
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("kind = ? AND value_hash = ? AND state = ? AND state != 'voided'", kind, BindingValueHash(value), entity.BindingStateReleased).
		Order("id desc").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// MaskBindingValue 返回用于账单 remark 的脱敏值（44-*.md §3.4 要求写明「换绑到哪个值」）。
//
// 手机号走既有 MaskPhone；微信 OpenID 不是手机号，MaskPhone 会原样返回，
// 因此这里对微信单独做首尾保留的掩码 —— 账单是用户可见的，不能直接落完整 OpenID。
func MaskBindingValue(kind, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(kind), entity.BindingKindPhone) {
		return MaskPhone(trimmed)
	}
	runes := []rune(trimmed)
	if len(runes) <= 6 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:3]) + "****" + string(runes[len(runes)-3:])
}

// releaseBindingForUser 释放某用户某类绑定的活跃行，并在失败时**只记日志不阻断**？
// —— 不，这里选择**返回错误并回滚**：绑定记录的完整性是二次注册判定与追缴的前提，
// 静默丢掉一条 released 记录等于让「情形 1/2/4」永久失效，属于必须让人看见的结构性故障。
//
// 调用方必须**先**调本函数再清 `users.phone` / `users.wechat_id`：清完就取不到原值了。
func releaseBindingForUser(tx *gorm.DB, userID int32, kind, releaseReason, releaseSource string,
	user entity.User, operatorUserID int32) error {
	if tx == nil || userID <= 0 {
		return nil
	}
	// 老库/精简测试库可能还没建这张表：此时无历史可记，直接放过（不能因此让换绑失败）。
	if !tx.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return nil
	}
	released, err := ReleaseBindingInTx(tx, userID, kind, releaseReason, releaseSource, operatorUserID)
	if err != nil {
		return err
	}
	if released == nil {
		// 该用户此类没有活跃行（从未绑定过，或历史代码没有写过绑定记录）。
		// 仍补写一条 released 行：值从 users 上取，保证历史不丢 —— 这正是
		// 「上线前的历史无法回填，上线后每一次释放都要留痕」的落地方式。
		value := ""
		var boundAt *time.Time
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case entity.BindingKindPhone:
			if user.Phone != nil {
				value = *user.Phone
			}
			boundAt = user.PhoneBoundAt
		case entity.BindingKindWechat:
			if user.WechatID != nil {
				value = *user.WechatID
			}
			boundAt = user.WechatBoundAt
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		now := time.Now()
		hash := BindingValueHash(value)
		record := entity.UserBindingRecord{
			UserID:           userID,
			UsernameSnapshot: user.Username,
			Kind:             strings.ToLower(strings.TrimSpace(kind)),
			Value:            value,
			ValueHash:        hash,
			State:            entity.BindingStateReleased,
			// released 行必须 active_key = NULL，否则继续占着 (kind, active_key) 唯一索引。
			ActiveKey:      nil,
			Source:         entity.BindingSourceUserBind,
			BoundAt:        boundAt,
			ReleasedAt:     &now,
			ReleaseReason:  releaseReason,
			ReleaseSource:  releaseSource,
			OperatorUserID: operatorUserID,
		}
		return tx.Session(&gorm.Session{NewDB: true}).Create(&record).Error
	}
	return nil
}

// recordActivePhoneBinding 在 users.phone 更新成功后写入/刷新 active 绑定记录。
//
// 幂等：同值已有 active 行时 RecordBindingActiveWithDB 直接返回既有行。
func recordActivePhoneBinding(tx *gorm.DB, user entity.User, phone string, boundAt time.Time) error {
	return recordActiveBinding(tx, user, entity.BindingKindPhone, phone, boundAt, entity.BindingSourceUserBind)
}

// recordActiveWechatBinding 同 recordActivePhoneBinding，微信维度。
func recordActiveWechatBinding(tx *gorm.DB, user entity.User, wechatID string, boundAt time.Time) error {
	return recordActiveBinding(tx, user, entity.BindingKindWechat, wechatID, boundAt, entity.BindingSourceWechatRegister)
}

func recordActiveBinding(tx *gorm.DB, user entity.User, kind, value string, boundAt time.Time, source string) error {
	if tx == nil || user.ID <= 0 || strings.TrimSpace(value) == "" {
		return nil
	}
	if !tx.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		return nil
	}
	when := boundAt
	_, err := RecordBindingActiveWithDB(tx, entity.UserBindingRecord{
		UserID:           user.ID,
		UsernameSnapshot: user.Username,
		Kind:             kind,
		Value:            value,
		Source:           source,
		BoundAt:          &when,
	})
	return err
}
