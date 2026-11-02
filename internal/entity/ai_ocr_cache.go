package entity

import "time"

const TableNameAIOCRCache = "ai_ocr_cache_entries"

// AIOCRCache stores the latest successful OCR result for one exact image URL.
// The URL and image bytes are deliberately not persisted.
type AIOCRCache struct {
	URLHash     string    `gorm:"column:url_hash;type:char(64);primaryKey" json:"urlHash"`
	ImageSHA256 string    `gorm:"column:image_sha256;type:char(64);not null" json:"imageSha256"`
	ContentType string    `gorm:"column:content_type;type:varchar(128);not null;default:''" json:"contentType"`
	OCRText     string    `gorm:"column:ocr_text;type:text;not null" json:"ocrText"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AIOCRCache) TableName() string {
	return TableNameAIOCRCache
}
