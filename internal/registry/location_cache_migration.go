package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const locationCacheEntriesMigrationKey = "location_cache_entries_migrated"

// ensureLocationCacheEntriesSchema creates the source-aware cache table and
// copies legacy rows from location_cache exactly once. The old table remains
// untouched so SQLite never has to rewrite its primary key.
func ensureLocationCacheEntriesSchema(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("归属地缓存迁移异常: %v", recovered)
		}
	}()
	if !db.Migrator().HasTable(entity.TableNameLocationCache) {
		return fmt.Errorf("归属地缓存新表不存在")
	}
	if systemSettingFlagIsSet(db, locationCacheEntriesMigrationKey) {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameLegacyLocationCache) {
		return setSystemSettingFlag(db, locationCacheEntriesMigrationKey)
	}

	const batchSize = 500
	for offset := 0; ; offset += batchSize {
		legacyRows := make([]entity.LegacyLocationCache, 0, batchSize)
		if queryErr := db.Order("kind ASC, lookup_key ASC").Limit(batchSize).Offset(offset).Find(&legacyRows).Error; queryErr != nil {
			return fmt.Errorf("读取旧归属地缓存失败: %w", queryErr)
		}
		if len(legacyRows) == 0 {
			break
		}
		entries := make([]entity.LocationCache, 0, len(legacyRows))
		for _, legacy := range legacyRows {
			source := ""
			if legacy.Kind == entity.LocationKindIP {
				source = entity.LocationSourceLegacy
			}
			entries = append(entries, entity.LocationCache{
				Kind: legacy.Kind, LookupKey: legacy.LookupKey, Source: source,
				Address: legacy.Address, RawJSON: legacy.RawJSON,
				FetchedAt: legacy.FetchedAt, UpdatedAt: legacy.UpdatedAt,
			})
		}
		if createErr := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&entries).Error; createErr != nil {
			return fmt.Errorf("写入新归属地缓存失败: %w", createErr)
		}
		if len(legacyRows) < batchSize {
			break
		}
	}
	if err := setSystemSettingFlag(db, locationCacheEntriesMigrationKey); err != nil {
		return fmt.Errorf("写入归属地缓存迁移标记失败: %w", err)
	}
	logger.SysLog("旧归属地缓存迁移完成，历史 IP 记录标记为来源未记录，原始数据已保留")
	return nil
}
