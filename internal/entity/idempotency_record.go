package entity

import "time"

const TableNameIdempotencyRecord = "idempotency_records"

type IdempotencyRecord struct {
	ID           int64     `gorm:"primaryKey;autoIncrement"`
	ScopeHash    string    `gorm:"column:scope_hash;type:char(64);not null;uniqueIndex:idx_idempotency_identity"`
	Operation    string    `gorm:"column:operation;type:varchar(64);not null;uniqueIndex:idx_idempotency_identity"`
	KeyHash      string    `gorm:"column:key_hash;type:char(64);not null;uniqueIndex:idx_idempotency_identity"`
	RequestHash  string    `gorm:"column:request_hash;type:char(64);not null"`
	Status       string    `gorm:"column:status;type:varchar(16);not null"`
	HTTPStatus   int       `gorm:"column:http_status;not null;default:0"`
	ResponseJSON string    `gorm:"column:response_json;type:longtext"`
	ExpiresAt    time.Time `gorm:"column:expires_at;index;not null"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

func (*IdempotencyRecord) TableName() string {
	return TableNameIdempotencyRecord
}
