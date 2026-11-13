package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// sensitiveImageCacheSchemaKey 标记「敏感图片检测缓存表已就绪」（121-*.md §4.1）。
	sensitiveImageCacheSchemaKey = "sensitive_image_cache_schema_v1"
	// sensitiveImageQuotaSchemaKey 标记「每人每日额度表已就绪」（121-*.md §4.2）。
	sensitiveImageQuotaSchemaKey = "sensitive_image_daily_quota_schema_v1"
)

// EnsureSensitiveImageSchema 建立敏感图片检测所需的**两张**新表：
//
//   - sensitive_image_cache_entries（判定缓存，主键 ImageSHA256）
//   - sensitive_image_daily_quotas（每人每日额度，主键 (user_id, quota_date)）
//
// # 为什么导出
//
// 本函数要挂进启动迁移链（internal/registry/db.go），而 db.go 在本次改动里
// **只读不可写**（另一个分支正在改它），因此这里导出供调用方接线；
// 依赖顺序见 121-*.md §4.2：`ensureSensitiveImageDailyQuotaSchema` 必须排在
// `db.AutoMigrate(&entity.User{})` 之后。本函数自身用 HasTable 前置检查
// 兜住这个依赖（见下），即使接线位置不理想也不会建出错表。
//
// # 纪律（照 121-*.md §2）
//
//  1. **建表失败绝不写标记**。先建表、成功才写标记。反过来的后果是表永远建不出来，
//     而标记说「已完成」，功能静默 100% 失效且没有任何报错（§2.1 的原文）。
//  2. **两张表各自独立的 FLAG**。它们的前置依赖与失败域完全不同（缓存表无依赖、
//     额度表依赖 users），合并成一个标记会让「其中一张建失败」把另一张也拖成
//     永不重跑。
//  3. **绝不 DROP TABLE**。缓存与额度都是可再生的，但没有理由主动删数据。
//  4. 失败策略 FATAL 由调用方（runStartupMigration）决定：缺表则检测功能整体不可用，
//     这里只如实返回 error。
//
// 幂等：两次运行不报错、不重复建表（标记 + HasTable 双保险）。
func EnsureSensitiveImageSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := ensureSensitiveImageCacheSchema(db); err != nil {
		return err
	}
	return ensureSensitiveImageDailyQuotaSchema(db)
}

// ensureSensitiveImageCacheSchema 建缓存表（纯新表，无前置依赖）。
func ensureSensitiveImageCacheSchema(db *gorm.DB) error {
	if systemSettingFlagIsSet(db, sensitiveImageCacheSchemaKey) && db.Migrator().HasTable(entity.TableNameSensitiveImageCache) {
		return nil
	}
	if err := db.AutoMigrate(&entity.SensitiveImageCacheEntry{}); err != nil {
		// 不写标记：AutoMigrate 失败时表可能只建了一半，下次启动必须重跑。
		return fmt.Errorf("create sensitive_image_cache_entries fail: %w", err)
	}
	if !db.Migrator().HasTable(entity.TableNameSensitiveImageCache) {
		// AutoMigrate 返回 nil 但表不存在（方言兼容层吞错时的兜底）：同样不写标记。
		return fmt.Errorf("sensitive_image_cache_entries 未创建成功")
	}
	if err := setSystemSettingFlag(db, sensitiveImageCacheSchemaKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的（§2 第 6 条）。
		logger.SysLog(fmt.Sprintf("写入敏感图片缓存迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// ensureSensitiveImageDailyQuotaSchema 建每人每日额度表。
//
// 前置依赖 users（121-*.md §4.2 的顺序依赖清单）：额度表的 user_id 语义上指向
// users.id。这里**不建外键约束**（与仓内其它表一致），但表存在性仍是接线正确性的
// 晴雨表 —— 如果 users 还没有，说明本函数被调用得太早。
//
// 依赖不满足时**返回 nil 且不写标记**：这不是「做完了」，而是「还没轮到」。
// 若在这里写标记，users 建好之后本迁移会被永远跳过，额度表将永远不存在
// （与 §2.1 「建表失败不写标记」同一条纪律，只是失败原因换成了顺序）。
func ensureSensitiveImageDailyQuotaSchema(db *gorm.DB) error {
	if systemSettingFlagIsSet(db, sensitiveImageQuotaSchemaKey) && db.Migrator().HasTable(entity.TableNameSensitiveImageQuota) {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		logger.SysLog("users 表尚不存在，跳过 sensitive_image_daily_quotas 建表（下次启动会重跑）")
		return nil
	}
	if err := db.AutoMigrate(&entity.SensitiveImageDailyQuota{}); err != nil {
		return fmt.Errorf("create sensitive_image_daily_quotas fail: %w", err)
	}
	if !db.Migrator().HasTable(entity.TableNameSensitiveImageQuota) {
		return fmt.Errorf("sensitive_image_daily_quotas 未创建成功")
	}
	if err := setSystemSettingFlag(db, sensitiveImageQuotaSchemaKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入每人每日额度迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}
