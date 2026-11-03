package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const aiOCRCacheIDsMigrationKey = "ai_ocr_cache_ids_backfilled"

// ensureAIOCRCacheIDs backfills stable display IDs without rebuilding or
// dropping the existing URLHash-keyed cache table. Rows are read in ascending
// update order in batches; the sequence table provides IDs for both legacy and
// future rows. Memory stays bounded to one batch.
func ensureAIOCRCacheIDs(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("OCR 缓存 ID 迁移异常: %v", recovered)
		}
	}()
	if !db.Migrator().HasTable(&entity.AIOCRCache{}) || !db.Migrator().HasTable(&entity.AIOCRCacheSequence{}) {
		return fmt.Errorf("OCR 缓存或 ID 序列表不存在")
	}
	if !db.Migrator().HasColumn(&entity.AIOCRCache{}, "id") {
		return fmt.Errorf("OCR 缓存 ID 列不存在")
	}
	if systemSettingFlagIsSet(db, aiOCRCacheIDsMigrationKey) {
		return nil
	}

	const batchSize = 500
	for {
		batch := make([]entity.AIOCRCache, 0, batchSize)
		if err := db.Where("id IS NULL").Order("updated_at ASC, url_hash ASC").Limit(batchSize).Find(&batch).Error; err != nil {
			return fmt.Errorf("读取待补 ID 的 OCR 缓存失败: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			for i := range batch {
				sequence := entity.AIOCRCacheSequence{}
				if err := tx.Create(&sequence).Error; err != nil {
					return fmt.Errorf("分配 OCR 缓存 ID 失败: %w", err)
				}
				if sequence.ID == 0 {
					return fmt.Errorf("OCR 缓存 ID 序列未返回编号")
				}
				if err := tx.Model(&entity.AIOCRCache{}).
					Where("url_hash = ? AND id IS NULL", batch[i].URLHash).
					Update("id", sequence.ID).Error; err != nil {
					return fmt.Errorf("回填 OCR 缓存 ID 失败: %w", err)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if err := setSystemSettingFlag(db, aiOCRCacheIDsMigrationKey); err != nil {
		return fmt.Errorf("写入 OCR 缓存 ID 迁移标记失败: %w", err)
	}
	return nil
}
