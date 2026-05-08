package entity

import "time"

const TableNameSystemSetting = "system_settings"

type SystemSetting struct {
	ID        int32     `gorm:"primaryKey;autoIncrement" json:"id"`
	Key       string    `gorm:"column:key;type:varchar(128);uniqueIndex;not null" json:"key"`
	Value     string    `gorm:"column:value;type:text" json:"value"`
	Remark    string    `gorm:"column:remark;type:varchar(255)" json:"remark"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*SystemSetting) TableName() string {
	return TableNameSystemSetting
}
