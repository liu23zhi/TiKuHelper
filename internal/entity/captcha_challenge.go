package entity

import "time"

const TableNameCaptchaChallenge = "captcha_challenges"

type CaptchaChallenge struct {
	ID          int32      `gorm:"primaryKey;autoIncrement" json:"id"`
	ChallengeID string     `gorm:"column:challenge_id;type:varchar(64);not null;uniqueIndex" json:"challengeId"`
	Scene       string     `gorm:"column:scene;type:varchar(64);not null;index" json:"scene"`
	AnswerHash  string     `gorm:"column:answer_hash;type:char(64);not null" json:"-"`
	Status      string     `gorm:"column:status;type:varchar(32);not null;index" json:"status"`
	RequestIP   string     `gorm:"column:request_ip;type:varchar(64)" json:"requestIp"`
	VerifiedAt  *time.Time `gorm:"column:verified_at" json:"verifiedAt"`
	ExpiresAt   time.Time  `gorm:"column:expires_at;not null;index" json:"expiresAt"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*CaptchaChallenge) TableName() string {
	return TableNameCaptchaChallenge
}
