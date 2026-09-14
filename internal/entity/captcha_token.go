package entity

import "time"

const TableNameCaptchaToken = "captcha_tokens"

type CaptchaToken struct {
	ID          int32      `gorm:"primaryKey;autoIncrement" json:"id"`
	Token       string     `gorm:"column:token;type:varchar(96);not null;uniqueIndex" json:"token"`
	Scene       string     `gorm:"column:scene;type:varchar(64);not null;index" json:"scene"`
	ChallengeID string     `gorm:"column:challenge_id;type:varchar(64);not null;index" json:"challengeId"`
	RequestIP   string     `gorm:"column:request_ip;type:varchar(64)" json:"requestIp"`
	Status      string     `gorm:"column:status;type:varchar(32);not null;index" json:"status"`
	ExpiresAt   time.Time  `gorm:"column:expires_at;not null;index" json:"expiresAt"`
	ConsumedAt  *time.Time `gorm:"column:consumed_at" json:"consumedAt"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*CaptchaToken) TableName() string {
	return TableNameCaptchaToken
}
