package entity

import "time"

const (
	// TableNameAccountDeviceBinding 是「账号—设备」绑定表（121 §4.9 / 107 Q15-Q17）。
	//
	// 它是设备池上限（user_levels.device_limit）的**唯一数据源**：
	// 判定「这台设备还能不能给这个账号用」时，只看这张表里该账号已登记的设备数。
	TableNameAccountDeviceBinding = "account_device_bindings"

	// AccountDeviceBindingUniqueIndex 是 (user_id, device_id) 的唯一索引名。
	//
	// ⚠️ 这条唯一索引是本表的**幂等锚点**，不是可选优化：
	// 同一个账号重复登记同一台设备时，靠它保证只有一行、且不额外占用一个槽位。
	// 缺了它，「同一设备登录 10 次」会写 10 行，设备数虚高到 10，
	// 上限 2 的等级会立刻被自己正常使用打满 —— 而且不报任何错。
	AccountDeviceBindingUniqueIndex = "uk_account_device_binding_user_device"
)

// 绑定行的 owner_kind 取值（137 §3.2）。
//
//	login —— 该行由登录/换绑路径登记（当前唯一生产写入口）
//	token —— 由访问令牌（扩展 X-Device-Id）路径登记，供后续批次使用
const (
	DeviceBindingOwnerKindLogin = "login"
	DeviceBindingOwnerKindToken = "token"
)

// 绑定撤销原因（137 §3.2）。**行不删**，只写 revoked_at + revoked_reason：
// 删行会导致「这台设备什么时候被谁移除」永远查不到，而封禁申诉需要这个。
//
//	user_removed    用户自己在「我的设备」移除
//	admin_unbound   管理员强制解绑
//	idle_10d        10 天未活跃被后台任务自动撤销（137 §7）
//	device_banned   设备被封禁后连带撤销旧绑定（137 §2 决定 2）
const (
	DeviceBindingRevokeReasonUserRemoved  = "user_removed"
	DeviceBindingRevokeReasonAdminUnbound = "admin_unbound"
	DeviceBindingRevokeReasonIdle10d      = "idle_10d"
	DeviceBindingRevokeReasonDeviceBanned = "device_banned"
)

// AccountDeviceBinding 是一行「某账号用过某设备」的记录。
//
// ## 为什么需要这张表（121 §4.9）
//
// 设备池上限是**等级权益**的一部分（user_levels.device_limit），但要判定「还能不能加新设备」
// 就必须有「已经用了哪些设备」的落地数据。users 表上加一个计数器做不到 —— 计数器
// 无法判断「这台设备是不是已经算过了」，于是同一台设备每次登录都会被计一次。
//
// ## 无历史回填（121 §4.9「数据回填：无」）
//
// 上线之前的历史请求**没有任何设备指纹**（那时还没采），所以本表从上线那一刻起
// 从零开始累积。这不是偷懒：伪造历史行等于凭空决定「某个用户已被用了 3 台设备」，
// 会把存量用户直接锁在上限之外，而这种误判无法事后自证。
//
// 由此推出一个必然结果：**上线初期所有用户的实际可用设备数 = 上限**（而不是上限减去历史），
// 文档里必须这么写，否则运营会以为「他明明只用过 1 台，为什么说满了」。
//
// ## 字段语义
//
//   - UserID：账号（users.id）。不级联删除：账号注销后设备记录仍要能解释历史判定。
//   - DeviceID：客户端上报的设备指纹（归一化后）。**不存明文之外的东西**：
//     它本身就是不可读的随机串，没有脱敏需求；也不做哈希，因为运营排查时
//     需要能直接比对「用户说的那台设备」。
//   - FirstSeenAt：该账号第一次用这台设备的时间。**只写一次**，后续登记不改它。
//   - LastSeenAt：最近一次使用时间。每次登记都刷新 —— 它是「10 天闲置解绑」的唯一数据源。
//   - CreatedAt / UpdatedAt：行级时间戳（本仓所有业务表的统一约定）。
//
// ## 批次 A1 新增的 5 列（137 §3.2）
//
//   - OwnerKind / TokenID：这行是谁登记的（login / token）。默认为 login，
//     让所有既有行与既有写入口（登录路径）语义不变。
//   - DeviceLabel：展示用设备名（「Chrome on Windows #a1b2」），由前端/扩展上报。
//   - RevokedAt / RevokedReason：**撤销而非删除**。
//     `RevokedAt IS NULL` 才算占槽位 —— 这一条是「已用台数」统计的口径定义，
//     写反了会让「用户移除设备后槽位不释放」或「撤销的行仍占槽」。
//
// ## 第二批（141）新增的 2 列（§3.1）
//
//   - ClientInstance：这次运行的实例标识，只用于冲突检测（R2/R3）。
//     **不进唯一索引**（141 §3.2）：exe 三个会话共用一个 device_id，
//     加进索引会变成三行，与 max(1,session_count) 的口径直接打架。
//   - SessionCount：exe 当前活跃会话数（R6/R7）。普通浏览器恒 0。
type AccountDeviceBinding struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// UserID 关联 users.id。唯一索引的第 1 列。
	UserID int32 `gorm:"column:user_id;type:int(11);not null;uniqueIndex:uk_account_device_binding_user_device,priority:1" json:"userId"`
	// DeviceID 是归一化后的设备指纹（去首尾空白、截断到 191）。
	//
	// 191 而不是 255：MySQL 的 utf8mb4 下索引前缀上限是 767 字节（老版本），
	// 191*4=764 是安全值。与 user_binding_records.value 的取舍一致。
	DeviceID string `gorm:"column:device_id;type:varchar(191);not null;uniqueIndex:uk_account_device_binding_user_device,priority:2" json:"deviceId"`
	// FirstSeenAt 是首次见到该设备的时间。留空时由 GORM 自动填当前时间，
	// 但服务层一律显式赋值 —— 「首次」的语义不能依赖 ORM 的零值推断。
	FirstSeenAt time.Time `gorm:"column:first_seen_at;autoCreateTime" json:"firstSeenAt"`
	// LastSeenAt 是最近一次登记该设备的时间。
	//
	// 刻意**不加** autoUpdateTime：它是业务值（每次登记都显式写 now），
	// 不是「行被改过」的元信息。两者混用会让「只改了备注也算使用过」这类
	// 误读在数据上无法分辨。
	LastSeenAt time.Time `gorm:"column:last_seen_at" json:"lastSeenAt"`
	// OwnerKind 是登记来源（login / token），默认 login 以保持既有行语义不变。
	OwnerKind string `gorm:"column:owner_kind;type:varchar(16);not null;default:'login'" json:"ownerKind"`
	// TokenID 是登记时使用的访问令牌 id（0 = 不适用/未知）。
	TokenID int32 `gorm:"column:token_id;type:int;not null;default:0" json:"tokenId"`
	// DeviceLabel 是给人看的设备名（前端/扩展上报，可空）。
	DeviceLabel string `gorm:"column:device_label;type:varchar(64);not null;default:''" json:"deviceLabel"`
	// FirstIP is recorded only when this binding is created. Existing rows stay
	// unknown after migration: their most recent IP cannot establish first use.
	FirstIP string `gorm:"column:first_ip;type:varchar(45);not null;default:''" json:"firstIp"`
	// LastIP 是这台设备**最近一次**被该账号使用时的来源 IP（归一化后的字面量）。
	//
	// ## 为什么是单列「最近一次」而不是历史
	//
	// 用户要看的是「这台设备现在从哪儿连进来的」——用于识别陌生设备、判断是不是
	// 自己在别的机器上登录过。历史 IP 序列属于风控留痕（login_attempts /
	// billing_records 已经在记），在设备行上再存一份会让本表从「绑定关系」
	// 变成「行为日志」，而那张表的每一行都参与槽位计数（口径会被日志量污染）。
	//
	// ## 为什么是 varchar(45) 而不是 INET
	//
	// 45 = IPv6 文本最长长度（含 `::ffff:` 映射形式）。INET 只有 PostgreSQL 有，
	// MySQL 用 VARBINARY(16)、SQLite 完全没有类型 —— 三方言统一成文本，
	// 归一化交给 service.NormalizeDeviceClientIP（net.ParseIP 收敛），
	// 与 blacklist / location 那两套 IP 体系的取舍一致。
	//
	// ## 为什么 default ''（空串）而不是 NULL
	//
	// 空串 = 「没采到/老行」，与 NULL 语义相同但少一层指针判定；且本列**不参与
	// 任何判定**（只是展示），不需要区分「未知」与「空」。既有行补列后自然为 ''。
	LastIP string `gorm:"column:last_ip;type:varchar(45);not null;default:''" json:"lastIp"`
	// ClientInstance 是「这次运行」的实例标识（141 §3.1 / R3）。
	//
	// 语义**只在冲突检测语境里**成立：同 device_id + 不同 client_instance ⇒ 冲突（R2）。
	// ⚠️ 绝不要用本列的行数去数 exe 的浏览器会话 —— exe 的三个会话共用一个
	// device_id、各自一个 instance，按行数会把「一个 device_id 下多个 instance」
	// 误判成冲突（141 §3.1 的显式警告）。会话数走 SessionCount。
	ClientInstance string `gorm:"column:client_instance;type:varchar(64);not null;default:''" json:"clientInstance"`
	// SessionCount 是 exe 当前**活跃**浏览器会话数（141 §3.1 / R6 / R7）。
	//
	// 它不是历史峰值：全关后必须能回到 0（R7 的「回落到 1 个」靠 max(1,0)=1 实现）。
	// 统计口径见 service.CountDevices：used = Σ max(1, session_count)。
	//
	// 默认 0 同时覆盖两种设备：普通浏览器（从不写本列）与「exe 启动但没开会话」，
	// 两者都靠 max(1,0)=1 贡献 1 个槽位（R4 / R5 / R7 同解）。
	SessionCount int32 `gorm:"column:session_count;type:int;not null;default:0" json:"sessionCount"`
	// SessionReportedAt 是最近一次 **exe 会话数上报** 的时间（141 §5.4 的心跳时间戳）。
	//
	// ⚠️ 它与 LastSeenAt 必须分开，这是本列存在的**唯一理由**：
	//
	//	last_seen_at        —— 任何请求都会刷新（搜题、状态页…）
	//	session_reported_at —— 只有 POST /user/device-sessions 会刷新
	//
	// 心跳超时的语义是「exe 最近一次自报会话数是多久以前」。若复用 last_seen_at，
	// 用户在 exe 里正常搜题（刷新 last_seen_at）但 exe 的上报线程已死时，
	// 服务端会以为会话数仍然新鲜，session_count 就永远不归零 → 崩溃后槽位虚高。
	//
	// **指针类型是刻意的**：历史行没有这个值（NULL），与「上报过但时间恰好零值」
	// 必须可区分。NULL 时 service 层退回 last_seen_at（见 EffectiveDeviceSessionCount
	// 的缺列/缺值降级），因此本列的迁移是**纯增强**，不是硬依赖。
	SessionReportedAt *time.Time `gorm:"column:session_reported_at" json:"sessionReportedAt,omitempty"`
	// RevokedAt 非 nil ⇒ 该行已撤销，**不占槽位**。
	// 指针类型是刻意的：NULL 与「零值时间」必须可区分，否则每条历史行都会被当成已撤销。
	RevokedAt *time.Time `gorm:"column:revoked_at" json:"revokedAt,omitempty"`
	// RevokedReason 取值见 DeviceBindingRevokeReason*；未撤销时为空串。
	RevokedReason string    `gorm:"column:revoked_reason;type:varchar(32);not null;default:''" json:"revokedReason"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt     time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AccountDeviceBinding) TableName() string { return TableNameAccountDeviceBinding }
