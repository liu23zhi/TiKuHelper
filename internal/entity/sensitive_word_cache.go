package entity

import "time"

const TableNameSensitiveWordCache = "sensitive_word_cache"

// SensitiveWordCache 按文本哈希缓存敏感词检测结果，避免同一文本重复外呼。
//
// 参考 ai_ocr_cache.go 的模式（hash 主键、只缓存结果不缓存原文）：
// 原文可能很长且可能是题库内容，缓存只需要「这段文本的检测结论」，
// 因此主键是 sha256(text) 的 hex，列里不出现原文。
//
// 失败也会落行（Error 非空），由 AttemptCount 限制重试次数：
// 上游抖动不能被固化成「永远失败」，但也不能无限重试浪费 API 额度。
type SensitiveWordCache struct {
	// TextHash 是 sha256(profile + 送检文本) 的 hex，长度恒为 64。
	TextHash string `gorm:"column:text_hash;type:char(64);primaryKey"`
	// Sensitive 是「上游判定命中」的结论，与 logs.sensitive_flag 同语义。
	Sensitive bool `gorm:"column:sensitive;not null;default:false"`
	// Words 是命中词 JSON 数组，未命中时为 "[]"。
	Words string `gorm:"column:words;type:text"`
	// MaskedText 是上游返回的屏蔽后文本，只作参考，从不替换原文。
	MaskedText string `gorm:"column:masked_text;type:text"`
	// APIStatus 是上游 status 原值（forbidden / clean / …），供排障比对。
	APIStatus string `gorm:"column:api_status;type:varchar(32)"`
	// Error 非空表示「这次检测失败」，此时 Sensitive 必须为 false（失败不等于命中）。
	Error string `gorm:"column:error;type:text"`
	// AttemptCount 是本行累计失败次数；达到上限后不再重试，直接按失败返回。
	AttemptCount int `gorm:"column:attempt_count;not null;default:0"`
	// CheckedAt 是最近一次检测（成功或失败）的时间。
	CheckedAt time.Time `gorm:"column:checked_at;autoUpdateTime"`
}

func (*SensitiveWordCache) TableName() string { return TableNameSensitiveWordCache }
