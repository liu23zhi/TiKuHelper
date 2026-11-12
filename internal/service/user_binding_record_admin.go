package service

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件是「绑定历史」的管理端读写：列表 / 详情 / 作废 / 手工补录。
//
// 权威依据：task_doc/70 §3.4（需求 5a）、§3.6（需求 6）、§3.7（需求 7）。
// 判定侧的读写仍在 user_binding_record.go，两处的隔离约定是：
// **业务判定只读 active/released，管理端视图可以读写 voided。**
//
// # 为什么「删除」是作废而不是 DELETE
//
// user_binding_records 不是纯审计日志，它是 19 号（二次注册判定）与 44 号（换绑追缴）
// 的**判定数据源**。物理删一行 = 静默改变判定结果（70 §3.7 两条直接后果）：
//   - 删掉「某号曾属于 A（rebind 释放）」→ B 再绑该号被当成首次注册 → 可能重复发赠送；
//   - 凭空插一条 → 伪造追缴依据。
//
// 所以采用 §3.7.1 方案 A：新增 state='voided'，判定查询统一排除，留痕三列
// （void_reason / voided_by_user_id / voided_at）。

// BindingReasonMinLength 是「作废 / 补录」理由的最小长度（去空格后按字符数）。
//
// 与题库归档同一口径（task_doc/77 §11.8）：不可逆或改变判定的操作必须有理由，
// 前端 required 不算数，服务端独立校验。
const BindingReasonMinLength = 2

// 绑定历史列表的单页上限。
const bindingListMaxPageSize = 100

// ErrBindingAlreadyVoided 表示该行已经是作废态（重复作废）。
var ErrBindingAlreadyVoided = errors.New("该绑定记录已作废")

// ErrBindingActiveNotVoidable 表示该行仍是 active。
//
// 活跃绑定不能直接作废：users.phone / users.wechat_id 上还写着这个值，
// 而唯一索引 (kind, active_key) 因为 active_key 置 NULL 被放开 ——
// 结果是「同一个手机号可以被另一个账号重新绑上」，而原账号的资料里还显示着它。
// 正确路径是先走解绑 / 换绑（ReleaseBindingInTx），再作废那条 released 行。
var ErrBindingActiveNotVoidable = errors.New("活跃绑定不能作废，请先解绑或换绑释放")

// MaskBindingDisplayValue 返回绑定值的展示形态（脱敏）。
//
// 口径（70 §3.4 / 红线 10）：
//   - 手机号：138****0000（后四位可见，复用 MaskPhone，与账户审计同一口径）；
//   - 微信 OpenID：前 6 + **** + 后 4（OpenID 不是手机号，MaskPhone 会原样返回，
//     必须单独处理，否则完整 OpenID 会落到页面上）。
//
// ⚠️ 这是**唯一**的实现点：用户管理弹窗（A1）、侧边栏两页（A2）、归档账号回看
// 都调它，不要再各自写一份 —— 三份脱敏一旦口径不同，最宽的那份就是真泄漏。
func MaskBindingDisplayValue(kind, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(kind), entity.BindingKindPhone) {
		return MaskPhone(trimmed)
	}
	runes := []rune(trimmed)
	if len(runes) <= 10 {
		// 太短的值按「全掩码」处理：保留首尾会把值本身暴露掉。
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:6]) + "****" + string(runes[len(runes)-4:])
}

// BindingRecordView 是绑定记录的对外视图（脱敏后）。
//
// 只读视图里**没有** Value / ValueHash 字段：明文值与检索哈希都不该出接口
// （70 红线 11：绑定查询只走 value_hash 精确匹配，明文不进查询计划与日志）。
type BindingRecordView struct {
	ID               int64      `json:"id"`
	UserID           int32      `json:"userId"`
	UsernameSnapshot string     `json:"usernameSnapshot"`
	Kind             string     `json:"kind"`
	ValueMasked      string     `json:"valueMasked"`
	State            string     `json:"state"`
	Source           string     `json:"source"`
	BoundAt          *time.Time `json:"boundAt"`
	ReleasedAt       *time.Time `json:"releasedAt"`
	ReleaseReason    string     `json:"releaseReason"`
	ReleaseSource    string     `json:"releaseSource"`
	OperatorUserID   int32      `json:"operatorUserId"`
	VoidReason       string     `json:"voidReason"`
	VoidedByUserID   int32      `json:"voidedByUserId"`
	VoidedAt         *time.Time `json:"voidedAt"`
	CreatedAt        time.Time  `json:"createdAt"`
}

// bindingRecordView 把实体转成脱敏视图。
func bindingRecordView(row entity.UserBindingRecord) BindingRecordView {
	return BindingRecordView{
		ID:               row.ID,
		UserID:           row.UserID,
		UsernameSnapshot: row.UsernameSnapshot,
		Kind:             row.Kind,
		ValueMasked:      MaskBindingDisplayValue(row.Kind, row.Value),
		State:            row.State,
		Source:           row.Source,
		BoundAt:          row.BoundAt,
		ReleasedAt:       row.ReleasedAt,
		ReleaseReason:    row.ReleaseReason,
		ReleaseSource:    row.ReleaseSource,
		OperatorUserID:   row.OperatorUserID,
		VoidReason:       row.VoidReason,
		VoidedByUserID:   row.VoidedByUserID,
		VoidedAt:         row.VoidedAt,
		CreatedAt:        row.CreatedAt,
	}
}

// bindingTableReady 判断绑定表是否可用（老库/精简单测库可能没有）。
func bindingTableReady(db *gorm.DB) bool {
	return db != nil && db.Migrator().HasTable(entity.TableNameUserBindingRecord)
}

// normalizeBindingPage 收敛分页参数（page 从 1 开始）。
func normalizeBindingPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > bindingListMaxPageSize {
		pageSize = bindingListMaxPageSize
	}
	return page, pageSize
}

// ListUserBindingRecordsWithDB 返回某个用户的**全部**绑定历史（含 voided），分页。
//
// 管理端详情视图要回答「这个号以前绑过什么、怎么释放的、哪条被作废了」，
// 因此这里**不过滤** voided —— 这是与业务判定查询的关键区别。
// 作废行带状态与作废理由，读的人能自己判断它算不算数。
func ListUserBindingRecordsWithDB(db *gorm.DB, userID int32, page, pageSize int) ([]BindingRecordView, int64, error) {
	views := make([]BindingRecordView, 0, pageSize)
	if !bindingTableReady(db) || userID <= 0 {
		return views, 0, nil
	}
	page, pageSize = normalizeBindingPage(page, pageSize)

	base := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Where("user_id = ?", userID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return views, 0, nil
	}
	var rows []entity.UserBindingRecord
	if err := base.Order("COALESCE(bound_at, created_at) DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	for _, row := range rows {
		views = append(views, bindingRecordView(row))
	}
	return views, total, nil
}

// BindingListFilter 是侧边栏历史绑定页的筛选条件（70 §3.6）。
type BindingListFilter struct {
	// Kind 必填：phone / wechat（两个页面共用一个接口）。
	Kind string
	// State 可选：active / released / voided；空 = 全部。
	State string
	// ReleaseReason 可选：rebind / closure / admin。
	ReleaseReason string
	// Value 可选：**精确匹配**（内部转 value_hash），不做明文模糊搜索。
	Value string
	// UserID 可选：按用户过滤。
	UserID   int32
	Page     int
	PageSize int
}

// ListBindingRecordsWithDB 返回全站绑定历史（管理端侧边栏两页共用）。
//
// ⚠️ 明文值筛选走 value_hash **精确匹配**（70 红线 11）：
// LIKE '%明文%' 会把明文塞进 SQL 与慢查询日志，而这张表存的是真实手机号 / OpenID。
func ListBindingRecordsWithDB(db *gorm.DB, filter BindingListFilter) ([]BindingRecordView, int64, error) {
	views := make([]BindingRecordView, 0, 20)
	if !bindingTableReady(db) {
		return views, 0, nil
	}
	kind, err := bindingNormalizeKind(filter.Kind)
	if err != nil {
		return nil, 0, err
	}
	page, pageSize := normalizeBindingPage(filter.Page, filter.PageSize)

	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Where("kind = ?", kind)
	if state := strings.ToLower(strings.TrimSpace(filter.State)); state != "" {
		query = query.Where("state = ?", state)
	}
	if reason, reasonErr := bindingNormalizeReleaseReason(filter.ReleaseReason); reasonErr != nil {
		return nil, 0, reasonErr
	} else if reason != "" {
		query = query.Where("release_reason = ?", reason)
	}
	if value := strings.TrimSpace(filter.Value); value != "" {
		query = query.Where("value_hash = ?", BindingValueHash(value))
	}
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return views, 0, nil
	}
	var rows []entity.UserBindingRecord
	if err := query.Order("COALESCE(bound_at, created_at) DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	for _, row := range rows {
		views = append(views, bindingRecordView(row))
	}
	return views, total, nil
}

// CurrentActiveBindingsWithDB 返回某用户当前的活跃绑定（详情弹窗「当前绑定」区块）。
func CurrentActiveBindingsWithDB(db *gorm.DB, userID int32) ([]BindingRecordView, error) {
	views := make([]BindingRecordView, 0, 2)
	if !bindingTableReady(db) || userID <= 0 {
		return views, nil
	}
	var rows []entity.UserBindingRecord
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("user_id = ? AND state = ? AND state != 'voided'", userID, entity.BindingStateActive).
		Order("kind asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		views = append(views, bindingRecordView(row))
	}
	return views, nil
}

// BindingAuditSnapshot 生成写进审计日志的行快照（显式挑字段）。
//
// ⚠️ 用脱敏值，不落明文：审计日志长期保留且管理员可查，
// 完整手机号 / OpenID 没有留在那里的必要（70 红线 10）。
func BindingAuditSnapshot(row entity.UserBindingRecord) map[string]any {
	return map[string]any{
		"recordId":       row.ID,
		"userId":         row.UserID,
		"username":       row.UsernameSnapshot,
		"kind":           row.Kind,
		"valueMasked":    MaskBindingDisplayValue(row.Kind, row.Value),
		"state":          row.State,
		"source":         row.Source,
		"releaseReason":  row.ReleaseReason,
		"releaseSource":  row.ReleaseSource,
		"boundAt":        row.BoundAt,
		"releasedAt":     row.ReleasedAt,
		"operatorUserId": row.OperatorUserID,
		"voidReason":     row.VoidReason,
		"voidedByUserId": row.VoidedByUserID,
	}
}

// VoidBindingRecordWithDB 把一条绑定历史置为作废（需求 7 的「删除」）。
//
// 三条不可协商的性质：
//  1. **不物理删除**（70 §3.7.1 方案 A）：判定数据源必须留痕；
//  2. **active_key 必须同时置 NULL**（红线 8）：否则该行继续占着
//     (kind, active_key) 唯一索引，别人再绑同一个值会撞唯一约束，
//     报错文案与手机号毫无字面关系，极难排查；
//  3. **只改 state 不够**：所有判定查询都排除 voided（见 user_binding_record.go
//     里 4 处 `state != 'voided'`），否则「作废了但判定上还在」。
//
// 返回作废前的快照（供调用方写审计），以及作废后的行。
func VoidBindingRecordWithDB(db *gorm.DB, recordID int64, operatorUserID int32, reason string) (map[string]any, entity.UserBindingRecord, error) {
	var after entity.UserBindingRecord
	if !bindingTableReady(db) {
		return nil, after, fmt.Errorf("绑定记录表不存在")
	}
	if recordID <= 0 {
		return nil, after, fmt.Errorf("绑定记录 ID 非法")
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) < BindingReasonMinLength {
		return nil, after, fmt.Errorf("请填写作废理由（至少 %d 个字符）", BindingReasonMinLength)
	}

	var before entity.UserBindingRecord
	now := time.Now()
	err := db.Transaction(func(tx *gorm.DB) error {
		handle := tx.Session(&gorm.Session{NewDB: true})
		if err := handle.Where("id = ?", recordID).First(&before).Error; err != nil {
			return err
		}
		switch before.State {
		case entity.BindingStateVoided:
			return ErrBindingAlreadyVoided
		case entity.BindingStateActive:
			return ErrBindingActiveNotVoidable
		}
		updated := handle.Model(&entity.UserBindingRecord{}).
			Where("id = ? AND state = ?", recordID, before.State).
			Updates(map[string]any{
				"state":             entity.BindingStateVoided,
				"active_key":        nil,
				"void_reason":       reason,
				"voided_by_user_id": operatorUserID,
				"voided_at":         now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrBindingStateChanged
		}
		after = before
		after.State = entity.BindingStateVoided
		after.ActiveKey = nil
		after.VoidReason = reason
		after.VoidedByUserID = operatorUserID
		after.VoidedAt = &now
		return nil
	})
	if err != nil {
		return nil, after, err
	}
	return BindingAuditSnapshot(before), after, nil
}

// BindingBackfillInput 是管理员手工补录一条绑定历史的入参（需求 7 后半句）。
type BindingBackfillInput struct {
	UserID int32
	Kind   string
	Value  string
	// ReleaseReason 必填：rebind / closure / admin —— 补录的就是「已释放」的历史。
	ReleaseReason string
	// ReleaseSource 可选，缺省 admin_manual_backfill 对应的来源口径。
	ReleaseSource string
	BoundAt       *time.Time
	ReleasedAt    *time.Time
	// Reason 是管理员填写的补录理由（写审计，必填）。
	Reason string
}

// BackfillBindingHistoryWithDB 手工补录一条**已释放**的绑定历史。
//
// ⚠️ 红线 9（70）：绝不能插 active 行 —— 那会把某个值从真正持有它的账号手里抢走
// （占住 (kind, active_key) 唯一索引）。因此本函数**只写 released 行**。
//
// ⚠️ 记为 source='admin_manual_backfill'（红线 70 §3.7.2 建议）：
// 手输的值可能并不属于该账号，事后必须能把「人工补录」与「真实换绑」分开统计。
//
// 已知并被接受的代价：补录一条 closure 释放的历史，会让该值在二次注册判定里
// 命中 current_*（= 可能触发对下一个绑该值的人的追缴）。这正是 94 号 §3.3
// 额外-1 标记的风险点，处置方式是**理由必填 + 审计留痕 + UI 二次确认**，
// 而不是把它做成静默操作。
func BackfillBindingHistoryWithDB(db *gorm.DB, input BindingBackfillInput) (entity.UserBindingRecord, error) {
	var record entity.UserBindingRecord
	if !bindingTableReady(db) {
		return record, fmt.Errorf("绑定记录表不存在")
	}
	kind, err := bindingNormalizeKind(input.Kind)
	if err != nil {
		return record, err
	}
	reason, err := bindingNormalizeReleaseReason(input.ReleaseReason)
	if err != nil {
		return record, err
	}
	if reason == "" {
		return record, fmt.Errorf("补录历史必须给出释放二分类（rebind / closure / admin）")
	}
	if input.UserID <= 0 {
		return record, fmt.Errorf("补录历史缺少用户")
	}
	value := strings.TrimSpace(input.Value)
	if value == "" {
		return record, fmt.Errorf("绑定值不能为空")
	}
	if strings.EqualFold(kind, entity.BindingKindPhone) {
		normalized, normalizeErr := NormalizeMainlandPhoneInput(value)
		if normalizeErr != nil {
			return record, normalizeErr
		}
		value = normalized
	}
	remark := strings.TrimSpace(input.Reason)
	if utf8.RuneCountInString(remark) < BindingReasonMinLength {
		return record, fmt.Errorf("请填写补录理由（至少 %d 个字符）", BindingReasonMinLength)
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return record, fmt.Errorf("用户表不存在")
	}
	var user entity.User
	if err := db.Session(&gorm.Session{NewDB: true}).Unscoped().
		Where("id = ?", input.UserID).First(&user).Error; err != nil {
		return record, fmt.Errorf("用户不存在: %w", err)
	}

	valueHash := BindingValueHash(value)
	// 同一用户同值的活跃行存在时不再补录：那是登记错误，不是历史。
	var activeCount int64
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBindingRecord{}).
		Where("user_id = ? AND kind = ? AND value_hash = ? AND state = ?", input.UserID, kind, valueHash, entity.BindingStateActive).
		Count(&activeCount).Error; err != nil {
		return record, err
	}
	if activeCount > 0 {
		return record, fmt.Errorf("该用户当前正绑着这个值，无需补录历史")
	}

	now := time.Now()
	boundAt := input.BoundAt
	releasedAt := input.ReleasedAt
	if releasedAt == nil {
		releasedAt = &now
	}
	source := strings.TrimSpace(input.ReleaseSource)
	if source == "" {
		source = entity.BindingSourceAdminManualBackfill
	}
	record = entity.UserBindingRecord{
		UserID:           input.UserID,
		UsernameSnapshot: user.Username,
		Kind:             kind,
		Value:            value,
		ValueHash:        valueHash,
		State:            entity.BindingStateReleased,
		// released / voided 行都必须 active_key = NULL（红线 8 / 70 §3.7.1 第 1 条）。
		ActiveKey:      nil,
		Source:         source,
		BoundAt:        boundAt,
		ReleasedAt:     releasedAt,
		ReleaseReason:  reason,
		ReleaseSource:  entity.BindingReleaseSourceManualBackfill,
		OperatorUserID: 0,
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
		return record, err
	}
	return record, nil
}
