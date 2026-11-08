package entity

import "time"

// ScriptSubsidizedUsage is internal accounting for executed activation-code
// calls. It is deliberately separate from the user's balance and statement.
// A request is recorded at most once even if persistence is retried.
type ScriptSubsidizedUsage struct {
	ID            int64     `gorm:"primaryKey;autoIncrement" json:"-"`
	ScriptID      int32     `gorm:"column:script_id;not null;uniqueIndex:idx_script_subsidized_request,priority:1" json:"-"`
	RequestID     string    `gorm:"column:request_id;type:varchar(64);not null;uniqueIndex:idx_script_subsidized_request,priority:2" json:"-"`
	UserID        int32     `gorm:"column:user_id;not null;index" json:"-"`
	AccessTokenID int32     `gorm:"column:access_token_id;not null" json:"-"`
	CostMicros    int64     `gorm:"column:cost_micros;not null" json:"-"`
	CostLinesJSON string    `gorm:"column:cost_lines_json;type:text;not null" json:"-"`
	UsageJSON     string    `gorm:"column:usage_json;type:text;not null" json:"-"`
	CreatedAt     time.Time `json:"-"`
}

func (*ScriptSubsidizedUsage) TableName() string { return "script_subsidized_usage" }
