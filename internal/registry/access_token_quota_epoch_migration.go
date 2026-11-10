package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// accessTokenQuotaEpochSchemaReadyKey 标记「access_token_quota_epochs 表与唯一索引已就绪」。
	accessTokenQuotaEpochSchemaReadyKey = "access_token_quota_epoch_schema_ready"
)

// ensureAccessTokenQuotaEpochSchema 建立访问令牌「额度周期」叠加层表（P14）。
//
// 只建**新表 + 唯一索引**，不改 access_tokens 的任何列：
// 额度周期是叠加层，total_amount_used 保持历史累计语义，**绝不清零**。
// 老库天然兼容 —— 没有 epoch 行的令牌就是「从未重置」，
// 本轮已用回落到「历史累计」，与功能上线前完全一致。
//
// 纪律：
//   - 结构性建表/建索引失败 → 返回错误，让启动 FATAL 看见；
//   - 本迁移没有数据回填（历史令牌不需要补 epoch 0 行）；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动幂等重跑；
//   - 不做任何 DROP TABLE。
//
// 复杂度：一次 AutoMigrate + 一次唯一索引自检 + 一次标记读写；不扫任何业务表。
func ensureAccessTokenQuotaEpochSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if systemSettingFlagIsSet(db, accessTokenQuotaEpochSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureAccessTokenQuotaEpochSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}
	if err := setSystemSettingFlag(db, accessTokenQuotaEpochSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入额度周期迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureAccessTokenQuotaEpochSchema 外层兜一层 recover：脏数据不能让服务反复崩溃。
func safeEnsureAccessTokenQuotaEpochSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	if err := db.AutoMigrate(&entity.AccessTokenQuotaEpoch{}); err != nil {
		return false, fmt.Errorf("create access_token_quota_epochs fail: %w", err)
	}
	// 唯一 (token_id, epoch) 是「并发重置只成功一次」的结构性保证，
	// 不能只靠 AutoMigrate 的 tag 推断 —— 显式核一次，缺了就补。
	if err := ensureAccessTokenQuotaEpochUniqueIndex(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureAccessTokenQuotaEpochUniqueIndex 保证唯一 (token_id, epoch) 索引存在。
//
// 两条分支的 SQL 写法不同（SQLite 不支持 MySQL 的 `KEY` 语法，MySQL 不认
// `IF NOT EXISTS` 在部分版本上的写法），因此分开执行；幂等性由 HasIndex 保证。
// 索引缺失时**不阻断**已有数据的读取，但重置接口会失去并发兜底，
// 所以这里当作结构性故障返回错误，让启动日志看得见。
func ensureAccessTokenQuotaEpochUniqueIndex(db *gorm.DB) error {
	const indexName = "uk_access_token_quota_epochs_token_epoch"
	if db.Migrator().HasIndex(entity.TableNameAccessTokenQuotaEpoch, indexName) {
		return nil
	}
	statement := fmt.Sprintf(
		"CREATE UNIQUE INDEX %s ON %s (token_id, epoch)",
		indexName, entity.TableNameAccessTokenQuotaEpoch,
	)
	if err := db.Exec(statement).Error; err != nil {
		return fmt.Errorf("create unique index %s fail: %w", indexName, err)
	}
	logger.SysLog("已为 access_token_quota_epochs 补上唯一索引 (token_id, epoch)")
	return nil
}
