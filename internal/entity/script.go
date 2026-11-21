package entity

import "time"

const (
	TableNameScriptVersion       = "script_versions"
	TableNameUserScript          = "user_scripts"
	TableNameScriptExchangeToken = "script_exchange_tokens"
	TableNameScriptRunSession    = "script_run_sessions"
	ScriptBillingModeEscrow      = "escrow"
	ScriptBillingModeActivation  = "activation_code"

	// ScriptRevokeReasonHeartbeatTimeout 表示托管会话因心跳中断被撤销。
	//
	// 这是三种原因里**唯一需要主动报警**的一种：用户没有做任何操作，
	// 是客户端停止发心跳导致令牌失效。用户看到「因为你电脑上的客户端
	// 停止响应，本次答题令牌已撤销」才知道要去看客户端是否崩溃/断网，
	// 而不是以为系统无故踢人。
	ScriptRevokeReasonHeartbeatTimeout = "heartbeat_timeout"
	// ScriptRevokeReasonUserClosed 表示用户自己关闭了脚本。
	// 这是用户主动行为，通知区**不应**报警，只作为历史记录保留。
	ScriptRevokeReasonUserClosed = "user_closed"
	// ScriptRevokeReasonAdminRevoked 表示管理员强制撤销。
	// 与 user_closed 分开是因为用户需要知道「不是我自己关的」。
	ScriptRevokeReasonAdminRevoked = "admin_revoked"
)

// ScriptVersion is an administrator-uploaded, reusable executable release.
type ScriptVersion struct {
	ID                  int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Version             string    `gorm:"column:version;type:varchar(64);not null;uniqueIndex" json:"version"`
	Filename            string    `gorm:"column:filename;type:varchar(255);not null" json:"filename"`
	SHA256              string    `gorm:"column:sha256;type:char(64);not null;index" json:"sha256"`
	SizeBytes           int64     `gorm:"column:size_bytes;not null" json:"sizeBytes"`
	Data                []byte    `gorm:"column:data;type:longblob;not null" json:"-"`
	IsCurrent           bool      `gorm:"column:is_current;not null;default:false;index" json:"isCurrent"`
	NativeLeaseProtocol bool      `gorm:"column:native_lease_protocol;not null;default:false;index" json:"nativeLeaseProtocol"`
	CreatedBy           int32     `gorm:"column:created_by;not null;index" json:"createdBy"`
	CreatedAt           time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*ScriptVersion) TableName() string { return TableNameScriptVersion }

// UserScript is a user's instruction bound to a specific executable version.
type UserScript struct {
	ID                    int32   `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID                int32   `gorm:"column:user_id;not null;index" json:"userId"`
	VersionID             int32   `gorm:"column:version_id;not null;index" json:"versionId"`
	Name                  string  `gorm:"column:name;type:varchar(128);not null" json:"name"`
	Instruction           string  `gorm:"column:instruction;type:text;not null" json:"instruction"`
	Parameters            string  `gorm:"column:parameters;type:text;not null" json:"parameters"`
	Description           string  `gorm:"column:description;type:varchar(255);not null;default:''" json:"description"`
	BaseDomain            string  `gorm:"column:base_domain;type:varchar(255);not null;default:''" json:"baseDomain"`
	Scheme                string  `gorm:"column:scheme;type:varchar(16);not null;default:https" json:"scheme"`
	UseWildcardDomain     bool    `gorm:"column:use_wildcard_domain;not null;default:false" json:"useWildcardDomain"`
	Enabled               bool    `gorm:"column:enabled;not null;index" json:"enabled"`
	BillingMode           string  `gorm:"column:billing_mode;type:varchar(24);not null;default:''" json:"billingMode"`
	AutoTopUp             bool    `gorm:"column:auto_top_up;not null;default:false" json:"autoTopUp"`
	RunDurationMinutes    int     `gorm:"column:run_duration_minutes;not null;default:0" json:"runDurationMinutes"`
	NotifyOnDenied        bool    `gorm:"column:notify_on_denied;not null;default:false" json:"notifyOnDenied"`
	BillingEnabled        bool    `gorm:"column:billing_enabled;not null" json:"billingEnabled"`
	UseTikuPrice          bool    `gorm:"column:use_tiku_price;not null" json:"useTikuPrice"`
	AnswerPriceMultiplier float64 `gorm:"column:answer_price_multiplier;type:decimal(18,6);not null;default:0" json:"answerPriceMultiplier"`
	// AnswerPrice is retained for backwards-compatible reads of pre-multiplier
	// rows. New scripts use AnswerPriceMultiplier against the current tiku price.
	AnswerPrice float64 `gorm:"column:answer_price;type:decimal(18,6);not null;default:0" json:"answerPrice"`
	ShortCode   string  `gorm:"column:short_code;type:char(16);not null;uniqueIndex" json:"shortCode"`
	// Legacy scripts have no locator and continue using their original address.
	LauncherLocator *string `gorm:"column:launcher_locator;type:char(36);uniqueIndex:ux_user_scripts_launcher_locator" json:"-"`
	// Old locators were public identifiers. Only newly issued/rotated locators
	// may authorize a launch without the legacy header capability.
	LauncherBearerEnabled bool    `gorm:"column:launcher_bearer_enabled;not null;default:false" json:"-"`
	AccessTokenID         int32   `gorm:"column:access_token_id;not null;index" json:"accessTokenId"`
	AccessKey             string  `gorm:"column:access_key;type:varchar(128);not null" json:"-"`
	ReservedFee           float64 `gorm:"column:reserved_fee;type:decimal(18,6);not null;default:10" json:"reservedFee"`
	// Full and short entry budgets are independent; legacy full commands remain
	// enabled with no time/count limit until the owner explicitly configures them.
	LauncherDisabled   bool       `gorm:"column:launcher_disabled;not null;default:false" json:"-"`
	LauncherGeneration int64      `gorm:"column:launcher_generation;not null;default:0" json:"-"`
	LauncherStartsAt   *time.Time `gorm:"column:launcher_starts_at" json:"-"`
	LauncherExpiresAt  *time.Time `gorm:"column:launcher_expires_at" json:"-"`
	LauncherWindows    string     `gorm:"column:launcher_windows;type:text" json:"-"`
	LauncherMaxUses    int64      `gorm:"column:launcher_max_uses;not null;default:0" json:"-"`
	LauncherUsedCount  int64      `gorm:"column:launcher_used_count;not null;default:0" json:"-"`
	// NULL frees the short address after exhaustion/revocation.
	ShortEntryAddress    string     `gorm:"column:short_entry_address;type:varchar(512);not null;default:''" json:"-"`
	ShortEntryAddressKey *string    `gorm:"column:short_entry_address_key;type:char(64);uniqueIndex:ux_user_scripts_short_entry" json:"-"`
	ShortEntryExpiresAt  *time.Time `gorm:"column:short_entry_expires_at;index" json:"-"`
	ShortEntryUsedAt     *time.Time `gorm:"column:short_entry_used_at" json:"-"`
	ShortEntryStartsAt   *time.Time `gorm:"column:short_entry_starts_at" json:"-"`
	ShortEntryWindows    string     `gorm:"column:short_entry_windows;type:text" json:"-"`
	ShortEntryMaxUses    int64      `gorm:"column:short_entry_max_uses;not null;default:0" json:"-"`
	ShortEntryUsedCount  int64      `gorm:"column:short_entry_used_count;not null;default:0" json:"-"`
	ShortEntryGeneration int64      `gorm:"column:short_entry_generation;not null;default:0" json:"-"`
	EscrowClosingAt      *time.Time `gorm:"column:escrow_closing_at;index" json:"escrowClosingAt,omitempty"`
	EscrowWorkTracked    bool       `gorm:"column:escrow_work_tracked;not null;default:false" json:"-"`
	DeletedAt            *time.Time `gorm:"column:deleted_at;index" json:"deletedAt,omitempty"`
	CreatedAt            time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt            time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*UserScript) TableName() string { return TableNameUserScript }

// ScriptExchangeToken is a short-lived, single-use capability for a script.
// Only a SHA-256 digest is persisted; the plaintext token is returned once.
type ScriptExchangeToken struct {
	ID                      int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	ScriptID                int32      `gorm:"column:script_id;not null;index" json:"scriptId"`
	VersionID               int32      `gorm:"column:version_id;not null;default:0;index" json:"-"`
	Purpose                 string     `gorm:"column:purpose;type:varchar(16);not null;default:exchange;index" json:"-"`
	LaunchAddress           string     `gorm:"column:launch_address;type:varchar(512);not null;default:''" json:"-"`
	AuthorizationKind       string     `gorm:"column:authorization_kind;type:varchar(8);not null;default:''" json:"-"`
	AuthorizationGeneration int64      `gorm:"column:authorization_generation;not null;default:0" json:"-"`
	TokenHash               string     `gorm:"column:token_hash;type:char(64);not null;uniqueIndex" json:"-"`
	ExpiresAt               time.Time  `gorm:"column:expires_at;not null;index" json:"expiresAt"`
	UsedAt                  *time.Time `gorm:"column:used_at;index" json:"usedAt,omitempty"`
	CreatedAt               time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*ScriptExchangeToken) TableName() string { return TableNameScriptExchangeToken }

// ScriptRunSession is the short-lived runtime capability issued after an
// exchange ticket is redeemed. Only token and client identity digests are
// persisted; plaintext secrets never reach the database.
type ScriptRunSession struct {
	ID            int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	ScriptID      int32  `gorm:"column:script_id;not null;index" json:"scriptId"`
	UserID        int32  `gorm:"column:user_id;not null;index" json:"userId"`
	AccessTokenID int32  `gorm:"column:access_token_id;not null;index" json:"accessTokenId"`
	TokenHash     string `gorm:"column:token_hash;type:char(64);not null;uniqueIndex" json:"-"`
	IdentityHash  string `gorm:"column:identity_hash;type:char(64);not null" json:"-"`
	// LeaseSlot is unique while a managed run owns the script. NULL preserves
	// retired and historical sessions without making the old rows exclusive.
	LeaseSlot       *int32     `gorm:"column:lease_slot;uniqueIndex" json:"-"`
	SourceZoneHash  string     `gorm:"column:source_zone_hash;type:char(64);not null;default:''" json:"-"`
	LastHeartbeatAt *time.Time `gorm:"column:last_heartbeat_at" json:"-"`
	ActivatedAt     time.Time  `gorm:"column:activated_at;not null;index" json:"activatedAt"`
	ExpiresAt       time.Time  `gorm:"column:expires_at;not null;index" json:"expiresAt"`
	RevokedAt       *time.Time `gorm:"column:revoked_at;index" json:"revokedAt,omitempty"`
	// RevokedReason 记录**为什么**被撤销，供「我的脚本」通知区区分处置动作。
	//
	// 空串 = 没有撤销（正常到寿或仍在运行）。这与 RevokedAt 非空但原因为空
	// 是不同的状态：后者是历史数据（本列加入前撤销的会话），前端应显示
	// 「已撤销」而不猜原因。
	//
	// 取值（见 ScriptRevokeReason* 常量）：
	//   heartbeat_timeout → 托管会话心跳中断，被后来者抢占。**要报警**：
	//                       用户没主动做任何事，是客户端出了问题。
	//   user_closed       → 用户自己关闭脚本。**不报警**。
	//   admin_revoked     → 管理员强制撤销。**要提示**，但措辞与前者不同。
	//
	// ⚠️ 「到期」不是撤销，不得写本列。到寿是正常结束，每次都报警会让
	// 通知区变成噪音，真有异常时用户已经学会忽略它了。
	RevokedReason string    `gorm:"column:revoked_reason;type:varchar(32);not null;default:''" json:"revokedReason,omitempty"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*ScriptRunSession) TableName() string { return TableNameScriptRunSession }
