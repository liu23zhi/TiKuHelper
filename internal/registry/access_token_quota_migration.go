package registry

import (
	"errors"
	"fmt"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"strings"
)

const accessTokenQuotaLimitRepairedKey = "access_token_quota_limit_repaired_v2"

var ErrAccessTokenQuotaLimitDataInvalid = errors.New("historical access token quota limit invalid; manual recovery required; original data preserved")

func ensureAccessTokenQuotaLimitData(db *gorm.DB) error {
	_, err := inspectAccessTokenQuotaLimitData(db)
	return err
}

// The returned hold applies only to an observed, explicitly disabled invalid
// historical quota. No rows or migration markers are written here.
func inspectAccessTokenQuotaLimitData(db *gorm.DB) (bool, error) {
	if db == nil {
		return false, nil
	}
	if !db.Migrator().HasTable(entity.TableNameAccessToken) {
		if err := db.Exec("SELECT 1").Error; err != nil {
			return false, fmt.Errorf("inspect access token metadata: %w", err)
		}
		return false, nil
	}
	columns, err := db.Migrator().ColumnTypes(entity.TableNameAccessToken)
	if err != nil {
		return false, fmt.Errorf("inspect access token columns: %w", err)
	}
	hasQuota, hasStatus := false, false
	for _, column := range columns {
		switch strings.ToLower(column.Name()) {
		case "total_quota_limit":
			hasQuota = true
		case "status":
			hasStatus = true
		}
	}
	if !hasQuota {
		return false, nil
	}
	invalid := func() *gorm.DB {
		return db.Table(entity.TableNameAccessToken).Where("total_quota_limit < ? OR total_quota_limit >= ?", 0, entity.AccessTokenQuotaLimitMaxExclusive)
	}
	active := invalid()
	if hasStatus {
		active = active.Where("LOWER(TRIM(COALESCE(status, ''))) <> ?", "disabled")
	}
	var rows []struct{ ID int32 }
	if err := active.Select("id").Limit(1).Find(&rows).Error; err != nil {
		return false, fmt.Errorf("inspect access token quota limits: %w", err)
	}
	if len(rows) > 0 {
		return false, ErrAccessTokenQuotaLimitDataInvalid
	}
	if !hasStatus {
		return false, nil
	}
	var disabledCount int64
	if err := invalid().Where("LOWER(TRIM(COALESCE(status, ''))) = ?", "disabled").Count(&disabledCount).Error; err != nil {
		return false, fmt.Errorf("inspect disabled access token quotas: %w", err)
	}
	if disabledCount > 0 {
		logger.SysWarn(fmt.Sprintf("disabled_invalid_quota_preserved count=%d; token schema held; correct quota before enabling; stored limits and state unchanged", disabledCount))
		return true, nil
	}
	return false, nil
}
