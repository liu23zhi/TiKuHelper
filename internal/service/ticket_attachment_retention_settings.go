package service

import (
	"fmt"
	"gorm.io/gorm"
	"strconv"
)

func UpdateTicketImageRetentionSettingsWithDB(db *gorm.DB, input TicketAttachmentRetentionSettings) (TicketAttachmentRetentionSettings, error) {
	if db == nil {
		return TicketAttachmentRetentionSettings{}, ErrTicketAttachmentRetentionNoDatabase
	}
	if input.Mode != "time" && input.Mode != "size" && input.Mode != "both" {
		return TicketAttachmentRetentionSettings{}, fmt.Errorf("清理策略必须为按时间、按大小或同时启用")
	}
	if input.MaxAgeDays < 1 || input.MaxAgeDays > MaxTicketAttachmentRetentionAgeDays || input.MaxTotalGiB < 1 || input.MaxTotalGiB > MaxTicketAttachmentRetentionTotalGiB || input.CleanupIntervalMinutes < 1 || input.CleanupIntervalMinutes > MaxTicketAttachmentRetentionCleanupInterval {
		return TicketAttachmentRetentionSettings{}, fmt.Errorf("保留天数、大小或检查间隔不在允许范围内")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, setting := range []struct{ key, value string }{
			{TicketAttachmentRetentionModeSettingKey, input.Mode},
			{TicketAttachmentRetentionMaxAgeDaysSettingKey, strconv.Itoa(input.MaxAgeDays)},
			{TicketAttachmentRetentionMaxTotalGiBSettingKey, strconv.FormatInt(input.MaxTotalGiB, 10)},
			{TicketAttachmentRetentionCleanupIntervalMinutesSettingKey, strconv.Itoa(input.CleanupIntervalMinutes)},
		} {
			if err := saveSystemSettingValue(tx, setting.key, setting.value, "工单图片保留策略"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TicketAttachmentRetentionSettings{}, err
	}
	return GetTicketAttachmentRetentionSettingsWithDB(db), nil
}
