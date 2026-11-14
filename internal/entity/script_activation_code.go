package entity

import "time"

const TableNameAdminScriptActivationCode = "admin_script_activation_codes"

// 激活码状态。用字符串而非 bool，是因为「已使用 / 已过期 / 已禁用 / 未使用」
// 四种状态在工单排查时语义完全不同，用两个布尔字段表达会漏组合。
const (
	ScriptActivationCodeStatusUnused   = "unused"
	ScriptActivationCodeStatusUsed     = "used"
	ScriptActivationCodeStatusDisabled = "disabled"
	// ScriptActivationCodeStatusExpired 不是库里的持久状态：过期是按
	// ExpiresAt 与当前时间实时算出来的投影（见 service 层的 projectStatus），
	// 避免靠定时任务刷状态时出现「到期了但还没被扫到」的窗口。
	ScriptActivationCodeStatusExpired = "expired"
)

// AdminScriptActivationCode 是**管理员预生成**的一次性脚本激活码。
//
// 与既有的 ScriptActivationEntitlement 的区别（这是本表存在的理由）：
//   - ScriptActivationEntitlement 是**用户自购**产生的：码由
//     deriveScriptActivationCode(signingKey, userID, nonce) 用 HMAC 派生，
//     库里只存 code_hash，**明文码永不落库**，因此管理员既不能预先批量生成，
//     也无法事后查出「这个码是谁的、用了没」。用户只能看到自己买到的那个码。
//   - 本表是**管理员预生成**的：码由管理员侧随机生成并**明文保存**，因为客服
//     处理工单时必须能把码念给用户 / 在后台核对用户报上来的码。这不是安全
//     倒退 —— 激活码本来就是发给用户的凭据，管理员后台是它唯一的管理入口，
//     且接口全部 RequireAdmin。
//
// 一次性语义靠 **Code 唯一索引 + 条件更新（WHERE used_at IS NULL）** 保证，
// 不靠「先查后写」：并发兑换时只有一个事务能拿到 RowsAffected==1。
type AdminScriptActivationCode struct {
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// Code 是明文激活码，唯一索引同时承担「防重复生成」与「兑换幂等」。
	Code string `gorm:"column:code;type:varchar(64);not null;uniqueIndex:idx_admin_script_activation_code" json:"code"`
	// Status 只持久化 unused / used / disabled；expired 是读取时按 ExpiresAt 投影的。
	Status string `gorm:"column:status;type:varchar(16);not null;default:unused;index:idx_admin_script_activation_status" json:"status"`
	// Remark 是用途/备注，客服记录「发给哪个工单用」。
	Remark string `gorm:"column:remark;type:varchar(255);not null;default:''" json:"remark"`
	// CreatedBy 是生成该码的管理员。
	CreatedBy int32 `gorm:"column:created_by;not null;index" json:"createdBy"`
	// ExpiresAt 为空表示永不过期（NULL 语义：不设限，而不是「立即过期」）。
	ExpiresAt *time.Time `gorm:"column:expires_at;index" json:"expiresAt,omitempty"`
	// UsedAt / UsedBy 是兑换时刻与兑换人；一次性语义的唯一判据是 UsedAt IS NULL。
	UsedAt *time.Time `gorm:"column:used_at;index" json:"usedAt,omitempty"`
	UsedBy *int32     `gorm:"column:used_by;index" json:"usedBy,omitempty"`
	// UsedScriptID 记录兑换时绑定到的脚本（兑换流程允许先不绑脚本，故可空）。
	UsedScriptID *int32 `gorm:"column:used_script_id;index" json:"usedScriptId,omitempty"`
	// UsedEntitlementID 关到既有兑换链路的产物，让客服能从码追到实际会话。
	UsedEntitlementID *int64    `gorm:"column:used_entitlement_id;index" json:"usedEntitlementId,omitempty"`
	CreatedAt         time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt         time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AdminScriptActivationCode) TableName() string { return TableNameAdminScriptActivationCode }

// Mask 返回只露后 4 位的码，用于列表展示。
//
// 列表默认不显全码：一个管理员可能同时打开列表页和别人的屏幕共享，
// 而激活码就是可直接兑换的凭据。详情接口才返回全码（客服核对时需要）。
func (code AdminScriptActivationCode) Mask() string {
	const visible = 4
	if len(code.Code) <= visible {
		return "****"
	}
	return "****" + code.Code[len(code.Code)-visible:]
}
