package entity

import "time"

const (
	TableNameScriptVersion       = "script_versions"
	TableNameUserScript          = "user_scripts"
	TableNameScriptExchangeToken = "script_exchange_tokens"
	TableNameScriptRunSession    = "script_run_sessions"
	ScriptBillingModeEscrow      = "escrow"
	ScriptBillingModeActivation  = "activation_code"
)

// ScriptVersion is an administrator-uploaded, reusable executable release.
type ScriptVersion struct {
	ID        int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Version   string    `gorm:"column:version;type:varchar(64);not null;uniqueIndex" json:"version"`
	Filename  string    `gorm:"column:filename;type:varchar(255);not null" json:"filename"`
	SHA256    string    `gorm:"column:sha256;type:char(64);not null;index" json:"sha256"`
	SizeBytes int64     `gorm:"column:size_bytes;not null" json:"sizeBytes"`
	Data      []byte    `gorm:"column:data;type:longblob;not null" json:"-"`
	IsCurrent bool      `gorm:"column:is_current;not null;default:false;index" json:"isCurrent"`
	CreatedBy int32     `gorm:"column:created_by;not null;index" json:"createdBy"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
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
	AnswerPrice   float64    `gorm:"column:answer_price;type:decimal(18,6);not null;default:0" json:"answerPrice"`
	ShortCode     string     `gorm:"column:short_code;type:char(16);not null;uniqueIndex" json:"shortCode"`
	AccessTokenID int32      `gorm:"column:access_token_id;not null;index" json:"accessTokenId"`
	AccessKey     string     `gorm:"column:access_key;type:varchar(128);not null" json:"-"`
	ReservedFee   float64    `gorm:"column:reserved_fee;type:decimal(18,6);not null;default:10" json:"reservedFee"`
	DeletedAt     *time.Time `gorm:"column:deleted_at;index" json:"deletedAt,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*UserScript) TableName() string { return TableNameUserScript }

// ScriptExchangeToken is a short-lived, single-use capability for a script.
// Only a SHA-256 digest is persisted; the plaintext token is returned once.
type ScriptExchangeToken struct {
	ID        int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	ScriptID  int32      `gorm:"column:script_id;not null;index" json:"scriptId"`
	VersionID int32      `gorm:"column:version_id;not null;default:0;index" json:"-"`
	Purpose   string     `gorm:"column:purpose;type:varchar(16);not null;default:exchange;index" json:"-"`
	TokenHash string     `gorm:"column:token_hash;type:char(64);not null;uniqueIndex" json:"-"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null;index" json:"expiresAt"`
	UsedAt    *time.Time `gorm:"column:used_at;index" json:"usedAt,omitempty"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
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
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*ScriptRunSession) TableName() string { return TableNameScriptRunSession }
