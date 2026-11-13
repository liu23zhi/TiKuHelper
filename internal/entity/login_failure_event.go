package entity

import "time"

const TableNameLoginFailureEvent = "login_failure_events"

// LoginFailureEvent 是一次**登录失败**的审计流水（R9 credential_stuffing 的数据源）。
//
// # 为什么需要这张表（与 login_attempts 的分工）
//
// `login_attempts` 是**计数器**：每个 `(scope, subject_key)` 只有一行，失败时
// `fail_count += 1`。它回答的是「这个主体失败了几次」（**防爆破**）。
//
// 本表是**事件流水**：每次失败一行，保留「何时、哪个 IP、试了哪个账号名」。
// 它回答的是「这个 IP 试了多少个**不同账号**」（**防撞库**）。
//
// 两者的数据形态根本不同，不能合并：
//   - 限流判定在每个登录请求上跑，必须是 O(1) 的单行 UPDATE；
//     若改成「插流水 + 聚合查询」，每个登录请求都要扫表。
//   - 失败语义不同：`login_attempts` 写失败 = 锁定失效 = 安全洞（必须重试）；
//     本表写失败只是少一条审计（旁路，见 service.RecordLoginFailureEventWithDB）。
//
// ⚠️ 会重复记同一件事（一次失败既计数又插流水），但那是**有意的**：
// ①「这个主体又失败一次」是状态，②「某时刻某 IP 用某账号名失败一次」是事件。
//
// # R9 与 R2 的分界线
//
// 撞库的定义就是「用一个密码列表去试大量不同账号」，其中**绝大多数账号名可能
// 根本不存在**。所以 `UserID = 0`（账号不存在）的行**必须与密码错误同等计入**。
// 若只统计真实存在的账号，检测的就不是撞库，而是「对已知账号的暴力破解」（R2 范畴）。
type LoginFailureEvent struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// CreatedAt 是**失败发生时刻**，由写入方传入（不用 DB now）——
	// 便于测试控制时间窗与事后回放。
	CreatedAt time.Time `gorm:"column:created_at;not null;index:idx_login_failure_ip_time,priority:2" json:"createdAt"`
	// ClientIP 是来源 IP。空串的行**不写**（对 R9 无意义，无法按 IP 聚合）。
	ClientIP string `gorm:"column:client_ip;type:varchar(64);not null;index:idx_login_failure_ip_time,priority:1" json:"clientIp"`
	// UserID 是**匹配到的真实用户 id**；0 表示账号不存在。
	//
	// 0 不是「未知」而是「攻击者输入的账号名在库里查不到」—— 这是撞库的典型特征，
	// 所以它必须被正常计入统计（见上方「R9 与 R2 的分界线」）。
	UserID int32 `gorm:"column:user_id;type:int(11);not null;default:0" json:"userId"`
	// Username 是**攻击者声称的账号名**（或手机号），不可信。
	//
	// ⚠️ 刻意**不建任何索引**：撞库时攻击者输入的账号名是任意长度、任意字符集的
	// 随机串，建索引会（a）在长度维度被撑爆（varchar(255)），（b）选择性极差
	// （几乎每个值都独一无二）—— 收益为零，只有维护成本。
	// 它只作普通列存储，供人工排查时 SELECT 出来看。
	Username string `gorm:"column:username;type:varchar(255);not null;default:''" json:"username"`
	// Scene 是登录场景：password / sms / admin_password。
	Scene string `gorm:"column:scene;type:varchar(32);not null;default:''" json:"scene"`
	// Reason 是失败原因码：user_not_found / bad_password / account_disabled /
	// phone_not_bound / not_admin / captcha_failed。
	//
	// 存原因码便于排查（「密码错」可能是记错，「账号不存在」是撞库特征），
	// 但 **R9 的计数不区分原因**（所有失败同等计入）。
	Reason string `gorm:"column:reason;type:varchar(64);not null;default:''" json:"reason"`
	// Locked 表示**本次失败是否触发了锁定**（即返回了 429）。
	//
	// ⚠️ 用 `default:false` 而不是 `default:true`：本仓踩过 GORM 的
	// 「bool + default:true 会把写入的 false 静默替换成 true」零值陷阱
	// （见 entity/login_attempt.go 的注释）。`default:false` 时零值与默认值一致，
	// 不存在该风险 —— 实测 Create(true)/Create(false)/UpdateColumn(false) 三者均正确。
	Locked bool `gorm:"column:locked;not null;default:false" json:"locked"`
}

func (*LoginFailureEvent) TableName() string { return TableNameLoginFailureEvent }

// 登录失败原因码（写入 LoginFailureEvent.Reason）。
const (
	// LoginFailureReasonUserNotFound 账号名在库里查不到 —— **撞库的最典型特征**。
	LoginFailureReasonUserNotFound = "user_not_found"
	// LoginFailureReasonBadPassword 账号存在但密码错误。
	LoginFailureReasonBadPassword = "bad_password"
	// LoginFailureReasonAccountDisabled 账号被禁用。
	LoginFailureReasonAccountDisabled = "account_disabled"
	// LoginFailureReasonPhoneNotBound 手机号未绑定任何账号（短信登录）。
	LoginFailureReasonPhoneNotBound = "phone_not_bound"
	// LoginFailureReasonNotAdmin 非管理员尝试使用管理员入口。
	LoginFailureReasonNotAdmin = "not_admin"
	// LoginFailureReasonCaptchaFailed 验证码错误（含短信验证码）。
	LoginFailureReasonCaptchaFailed = "captcha_failed"
	// LoginFailureReasonOther 未归类的失败（保持可追溯，不静默丢）。
	LoginFailureReasonOther = "other"
)

// 登录场景（写入 LoginFailureEvent.Scene）。
const (
	LoginFailureScenePassword      = "password"
	LoginFailureSceneSMS           = "sms"
	LoginFailureSceneAdminPassword = "admin_password"
)
