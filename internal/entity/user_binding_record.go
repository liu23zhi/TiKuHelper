package entity

import "time"

const TableNameUserBindingRecord = "user_binding_records"

// UserBindingRecord 记录「某个账号曾经持有过某个手机号 / 微信」的完整历史。
//
// 它是两份文档的共同数据源，**只建一张表，不要各建一份**：
//   - `18-账号注销与保留区.md` §7.2：注销归档后，原绑定值从这里回看；
//   - `19-二次注册判定与绑定记录.md` + `44-二次注册四情形与追缴定案.md`：
//     注册判定与换绑追缴都靠它回答「这个值以前属于谁、是怎么被释放的」。
//
// # 两个「释放原因」字段必须分开（44-*.md §1.2 / §5.1）
//
// ReleaseReason 是**判定二分类**（列 `release_reason`），只有三个合法值：
//
//	''        仍在生效（state='active'）
//	'rebind'  换绑释放   → 归入 19 号文档的 history_* 命中
//	'closure' 注销释放   → 归入 19 号文档的 current_* 命中
//	'admin'   管理员手工释放 → 用户 2026-11-09 定案：**计入 history_***
//
// ReleaseSource 是**操作来源**（列 `release_source`），粒度更细，服务审计与保留区展示：
//
//	'user_unbind' / 'admin_clear' / 'force_reassign' / 'account_close' / 'admin_delete'
//
// ⚠️ 命名与 `18-*.md` §7.2 草图的差异（**这不是笔误，是刻意的**）：
// 18 号文档把「操作来源」那个字段叫 `ReleaseReason`，把二分类叫 `ReleaseReasonClass`。
// 但 44 号文档（本文的**上位**文档）通篇用 `release_reason` 指代 `rebind` / `closure`
// 这个二分类，且任务书要求数据库列名就叫 `release_reason`。两者冲突时以 44 号为准，
// 因此这里：`release_reason`（列）= 二分类、`release_source`（列）= 操作来源。
// 读 18 号文档的人必须知道这层改名，否则会把两个字段对调。
//
// ⚠️ 为什么不能合成一个字段：`18-*.md` §7.2 的原设计只有操作来源，而 19 号文档的
// 四个开关（history_*/current_*）需要的是**二分类**。若只写 `state='released'` 而不分
// 二分类，四个开关会**退化成一个**（全部都算「历史」），情形 2/4（current_* 命中
// 注销释放的值）永远不触发 —— 而且**不报错、不告警**，只能靠文档对照才能发现。
type UserBindingRecord struct {
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// UserID 关联 accounts（users.id）。不级联删除：账号归档后记录仍要能查。
	UserID int32 `gorm:"column:user_id;type:int(11);not null;index:idx_binding_user,priority:1" json:"userId"`
	// UsernameSnapshot 是写入当时的用户名快照，账号改名后保留区仍能读出「当时是谁」。
	UsernameSnapshot string `gorm:"column:username_snapshot;type:varchar(255)" json:"usernameSnapshot"`
	// Kind 只有 phone / wechat 两种。
	Kind string `gorm:"column:kind;type:varchar(16);not null;index:idx_binding_user,priority:2;index:idx_binding_lookup,priority:1;uniqueIndex:idx_binding_active,priority:1" json:"kind"`
	// Value 是规范化后的原值（手机号 digits-only / 微信 OpenID）。
	Value string `gorm:"column:value;type:varchar(191);not null" json:"value"`
	// ValueHash = sha256(Value)，判定查询只走哈希，不必比对明文。
	ValueHash string `gorm:"column:value_hash;type:char(64);not null;index:idx_binding_lookup,priority:2" json:"valueHash"`
	// State 有四个取值：active / released / voided。
	//
	//	'active'   —— 当前生效的绑定（唯一索引只约束这一态）
	//	'released' —— 正常释放（换绑/注销/管理员释放），**仍参与判定**
	//	'voided'   —— 管理员作废（需求 7，task_doc/70 §3.7.1 方案 A），**不参与任何判定**
	//
	// ⚠️ 'voided' 与 'released' 的区别不是措辞：released 行是 19 号文档四个开关
	// （history_* / current_*）与 44 号追缴的判定依据；voided 行是「管理员认为这条
	// 记录不该算数」，因此**所有判定查询都必须排除它**（service/user_binding_record.go
	// 里 4 处 `state != 'voided'`）。漏掉一处就会把作废记录当成有效绑定，
	// 后果是「删了一条历史，判定上却还在」，且不报错。
	State string `gorm:"column:state;type:varchar(16);not null;default:'active';index:idx_binding_lookup,priority:3" json:"state"`
	// ActiveKey 只在 state='active' 时等于 ValueHash，released 时为 NULL。
	//
	// 唯一索引 (kind, active_key) 因此只约束「同一个值最多一个活跃绑定」，
	// released 行可以有任意多条历史。与 script_run_sessions.lease_slot 是同款 NULL 技巧。
	//
	// ⚠️ state 与 active_key 是**必须同时翻转的一对**：只改 state 不置 NULL 会继续占着
	// 唯一索引，别人再绑同一个值就撞唯一约束（报错文案与手机号毫无字面关系，极难排查）。
	ActiveKey *string `gorm:"column:active_key;type:char(64);uniqueIndex:idx_binding_active,priority:2" json:"-"`
	// Source 是这条绑定是怎么来的（写入来源，不是释放来源）。
	Source string `gorm:"column:source;type:varchar(32)" json:"source"`
	// BoundAt 是绑定时间（沿用 users.phone_bound_at / wechat_bound_at 的原值）。
	// 参与 idx_binding_user 的最后一列：保留区详情要按「用户 + 类型 + 时间倒序」出历史。
	BoundAt *time.Time `gorm:"column:bound_at;index:idx_binding_user,priority:3" json:"boundAt"`
	// ReleasedAt 只在 released 时有值。
	ReleasedAt *time.Time `gorm:"column:released_at;index" json:"releasedAt"`
	// ReleaseReason 是**判定二分类**：'' / rebind / closure / admin。见类型注释。
	ReleaseReason string `gorm:"column:release_reason;type:varchar(24);not null;default:'';index:idx_binding_lookup,priority:4" json:"releaseReason"`
	// ReleaseSource 是**操作来源**：user_unbind / admin_clear / force_reassign /
	// account_close / admin_delete。见类型注释。
	ReleaseSource string `gorm:"column:release_source;type:varchar(32);not null;default:''" json:"releaseSource"`
	// OperatorUserID 是触发本次变更的管理员（自助操作为 0）。
	OperatorUserID int32 `gorm:"column:operator_user_id;type:int(11);not null;default:0" json:"operatorUserId"`
	// 以下三列只在 state='voided' 时有值（需求 7 作废留痕，task_doc/70 §3.7.1）。
	//
	// 为什么不做物理删除：这张表是二次注册判定与追缴的数据源，
	// 删一行等于静默改变判定结果，且事后无从追查（70 §3.7 两条直接后果）。
	// 作废保留「谁在什么时候、以什么理由作废」，判定查询统一排除它。
	VoidReason     string     `gorm:"column:void_reason;type:varchar(255)" json:"voidReason"`
	VoidedByUserID int32      `gorm:"column:voided_by_user_id;type:int(11);not null;default:0" json:"voidedByUserId"`
	VoidedAt       *time.Time `gorm:"column:voided_at" json:"voidedAt"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*UserBindingRecord) TableName() string {
	return TableNameUserBindingRecord
}

// 绑定的 kind 取值。
const (
	BindingKindPhone  = "phone"
	BindingKindWechat = "wechat"
)

// 绑定的 state 取值。
const (
	BindingStateActive   = "active"
	BindingStateReleased = "released"
	// BindingStateVoided 是管理员作废（需求 7）。
	//
	// 与 released 一样**不占** (kind, active_key) 唯一索引（必须同时置 active_key=NULL），
	// 但 released 参与二次注册判定、voided **不参与** —— 见 State 字段注释。
	BindingStateVoided = "voided"
)

// ReleaseReason 的判定二分类取值（44-*.md §1.2 / §5.1）。
const (
	// BindingReleaseReasonRebind 换绑释放 → history_* 命中。
	BindingReleaseReasonRebind = "rebind"
	// BindingReleaseReasonClosure 注销释放 → current_* 命中。
	BindingReleaseReasonClosure = "closure"
	// BindingReleaseReasonAdmin 管理员手工释放 → 用户定案归入 history_*。
	BindingReleaseReasonAdmin = "admin"
)

// ReleaseSource 的操作来源取值（18-*.md §7.2 既有语义）。
const (
	BindingReleaseSourceUserUnbind    = "user_unbind"
	BindingReleaseSourceAdminClear    = "admin_clear"
	BindingReleaseSourceForceReassign = "force_reassign"
	BindingReleaseSourceAccountClose  = "account_close"
	BindingReleaseSourceAdminDelete   = "admin_delete"
	// BindingReleaseSourceManualBackfill 是管理员手工补录历史时的操作来源（需求 7）。
	//
	// 与 BindingSourceAdminManualBackfill 分开命名，是因为这里的语义是
	// 「释放是怎么发生的」而不是「这条记录是怎么来的」—— 补录的行没有真实释放动作，
	// 记成这个值等于声明「这是人工补录，不是系统换绑」。
	BindingReleaseSourceManualBackfill = "admin_manual_backfill"
)

// 绑定写入来源取值。
const (
	BindingSourceRegister           = "register"
	BindingSourceWechatRegister     = "wechat_register"
	BindingSourceUserBind           = "user_bind"
	BindingSourceAdminBind          = "admin_bind"
	BindingSourceAdminForceReassign = "admin_force_reassign"
	BindingSourceAccountClose       = "account_close"
	BindingSourceAdminDelete        = "admin_delete"
	// BindingSourceAdminManualBackfill 是管理员手工补录的历史行（需求 7，70 §3.7.2）。
	//
	// 必须与真实换绑产生的行**可区分**：手输的值可能并不属于该账号，
	// 事后统计「人工补录」与「真实历史」要能分开（70 §3.7.2 建议）。
	BindingSourceAdminManualBackfill = "admin_manual_backfill"
)
