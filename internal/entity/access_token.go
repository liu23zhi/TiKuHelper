package entity

import "time"

const TableNameAccessToken = "access_tokens"

type AccessToken struct {
	ID              int32      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID          int32      `gorm:"column:user_id;type:int(11);not null;index" json:"userId"`
	Name            string     `gorm:"column:name;type:varchar(128);not null" json:"name"`
	TokenHash       string     `gorm:"column:token_hash;type:char(64);uniqueIndex;not null" json:"tokenHash"`
	TokenPrefix     string     `gorm:"column:token_prefix;type:varchar(32);not null" json:"tokenPrefix"`
	TokenValue      string     `gorm:"column:token_value;type:varchar(255);not null;default:''" json:"tokenValue"`
	Status          string     `gorm:"column:status;type:varchar(32);not null;default:enabled" json:"status"`
	TotalQuotaLimit float64    `gorm:"column:total_quota_limit;type:decimal(18,6)" json:"totalQuotaLimit"`
	TotalAmountUsed float64    `gorm:"column:total_amount_used;type:decimal(18,6);not null;default:0" json:"totalAmountUsed"`
	LastUsedAt      *time.Time `gorm:"column:last_used_at" json:"lastUsedAt"`
	LastUsedIP      string     `gorm:"column:last_used_ip;type:varchar(64)" json:"lastUsedIp"`
	Remark          string     `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AccessToken) TableName() string {
	return TableNameAccessToken
}
