package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	accessTokenQuotaLimitRepairedKey = "access_token_quota_limit_repaired"
	accessTokenQuotaRepairBatchSize  = 500
)

// ensureAccessTokenQuotaLimitData repairs historical quota values that cannot fit
// decimal(18,6). Such rows become 0, which the UI defines as unlimited. The
// repair is idempotent, bounded to 500 IDs per batch, and retried on later starts
// unless the system_settings marker is written successfully.
func ensureAccessTokenQuotaLimitData(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(entity.TableNameAccessToken) ||
		!db.Migrator().HasColumn(entity.TableNameAccessToken, "total_quota_limit") {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameSystemSetting) || systemSettingFlagIsSet(db, accessTokenQuotaLimitRepairedKey) {
		return nil
	}

	if err := safeRepairAccessTokenQuotaLimits(db); err != nil {
		logger.SysLog(fmt.Sprintf("访问令牌总额度历史数据修复失败（不影响启动，将保留在下次启动重试）：%v", err))
		return err
	}
	if err := setSystemSettingFlag(db, accessTokenQuotaLimitRepairedKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入访问令牌总额度修复标记失败（下次启动会重新检查）：%v", err))
	}
	return nil
}

func safeRepairAccessTokenQuotaLimits(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return repairAccessTokenQuotaLimits(db)
}

type accessTokenQuotaRepairRow struct {
	ID int32 `gorm:"column:id"`
}

// repairAccessTokenQuotaLimits uses a fixed-size ID page so memory remains
// bounded regardless of the access_tokens table size.
func repairAccessTokenQuotaLimits(db *gorm.DB) error {
	for {
		var rows []accessTokenQuotaRepairRow
		if err := db.Table(entity.TableNameAccessToken).
			Select("id").
			Where("total_quota_limit >= ? OR total_quota_limit <= ?",
				entity.AccessTokenQuotaLimitMaxExclusive, -entity.AccessTokenQuotaLimitMaxExclusive).
			Order("id asc").
			Limit(accessTokenQuotaRepairBatchSize).
			Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}

		ids := make([]int32, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		result := db.Table(entity.TableNameAccessToken).
			Where("id IN ? AND (total_quota_limit >= ? OR total_quota_limit <= ?)", ids,
				entity.AccessTokenQuotaLimitMaxExclusive, -entity.AccessTokenQuotaLimitMaxExclusive).
			Update("total_quota_limit", 0)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("访问令牌总额度修复未更新待修复记录")
		}
		if len(rows) < accessTokenQuotaRepairBatchSize {
			return nil
		}
	}
}
