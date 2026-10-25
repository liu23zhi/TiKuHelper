package entity

import "time"

const TableNameSMSVerificationCode = "sms_verification_codes"

type SMSVerificationCode struct {
	ID                 int32      `gorm:"primaryKey;autoIncrement" json:"id"`
	Phone              string     `gorm:"column:phone;type:varchar(32);not null;index" json:"phone"`
	Scene              string     `gorm:"column:scene;type:varchar(32);not null;index" json:"scene"`
	CodeHash           string     `gorm:"column:code_hash;type:char(64);not null" json:"codeHash"`
	Status             string     `gorm:"column:status;type:varchar(32);not null;index" json:"status"`
	ExpiresAt          time.Time  `gorm:"column:expires_at;not null;index" json:"expiresAt"`
	ConsumedAt         *time.Time `gorm:"column:consumed_at" json:"consumedAt"`
	RequestIP          string     `gorm:"column:request_ip;type:varchar(64)" json:"requestIp"`
	CaptchaChallengeID string     `gorm:"column:captcha_challenge_id;type:varchar(192);index" json:"-"`
	CreatedAt          time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*SMSVerificationCode) TableName() string {
	return TableNameSMSVerificationCode
}
