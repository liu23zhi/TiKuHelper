package entity

import "time"

const TableNameSearchAccountConfig = "search_account_configs"

// SearchAccountConfig mapped from table <search_account_configs>
type SearchAccountConfig struct {
	ID         int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Provider   string    `gorm:"column:provider;type:varchar(64);uniqueIndex;not null" json:"provider"`
	TokenKey   string    `gorm:"column:token_key;type:varchar(128);not null" json:"token_key"`
	TokenValue string    `gorm:"column:token_value;type:text" json:"token_value"`
	Enabled    bool      `gorm:"column:enabled;not null;default:false" json:"enabled"`
	Remark     string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	FlatPrice  float64   `gorm:"column:flat_price;type:decimal(18,8);not null;default:0" json:"flatPrice"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName SearchAccountConfig's table name
func (*SearchAccountConfig) TableName() string {
	return TableNameSearchAccountConfig
}
