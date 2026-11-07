package entity

import "time"

const (
	TableNameScriptVersion       = "script_versions"
	TableNameUserScript          = "user_scripts"
	TableNameScriptExchangeToken = "script_exchange_tokens"
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
	ID                int32      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID            int32      `gorm:"column:user_id;not null;index" json:"userId"`
	VersionID         int32      `gorm:"column:version_id;not null;index" json:"versionId"`
	Name              string     `gorm:"column:name;type:varchar(128);not null" json:"name"`
	Instruction       string     `gorm:"column:instruction;type:text;not null" json:"instruction"`
	Parameters        string     `gorm:"column:parameters;type:text;not null" json:"parameters"`
	Description       string     `gorm:"column:description;type:varchar(255);not null;default:''" json:"description"`
	BaseDomain        string     `gorm:"column:base_domain;type:varchar(255);not null;default:''" json:"baseDomain"`
	Scheme            string     `gorm:"column:scheme;type:varchar(16);not null;default:https" json:"scheme"`
	UseWildcardDomain bool       `gorm:"column:use_wildcard_domain;not null;default:false" json:"useWildcardDomain"`
	Enabled           bool       `gorm:"column:enabled;not null;index" json:"enabled"`
	BillingEnabled    bool       `gorm:"column:billing_enabled;not null" json:"billingEnabled"`
	UseTikuPrice      bool       `gorm:"column:use_tiku_price;not null" json:"useTikuPrice"`
	AnswerPrice       float64    `gorm:"column:answer_price;type:decimal(18,6);not null;default:0" json:"answerPrice"`
	ShortCode         string     `gorm:"column:short_code;type:char(16);not null;uniqueIndex" json:"shortCode"`
	AccessTokenID     int32      `gorm:"column:access_token_id;not null;index" json:"accessTokenId"`
	AccessKey         string     `gorm:"column:access_key;type:varchar(128);not null" json:"-"`
	ReservedFee       float64    `gorm:"column:reserved_fee;type:decimal(18,6);not null;default:10" json:"reservedFee"`
	DeletedAt         *time.Time `gorm:"column:deleted_at;index" json:"deletedAt,omitempty"`
	CreatedAt         time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*UserScript) TableName() string { return TableNameUserScript }

// ScriptExchangeToken is a short-lived, single-use capability for a script.
// Only a SHA-256 digest is persisted; the plaintext token is returned once.
type ScriptExchangeToken struct {
	ID        int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	ScriptID  int32      `gorm:"column:script_id;not null;index" json:"scriptId"`
	TokenHash string     `gorm:"column:token_hash;type:char(64);not null;uniqueIndex" json:"-"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null;index" json:"expiresAt"`
	UsedAt    *time.Time `gorm:"column:used_at;index" json:"usedAt,omitempty"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*ScriptExchangeToken) TableName() string { return TableNameScriptExchangeToken }
