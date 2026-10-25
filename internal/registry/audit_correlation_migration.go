package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const auditCorrelationMigrationKey = "audit_correlation_schema_v1"

// This structural migration never scans or guesses ownership of historical
// logs. Existing rows retain empty correlation evidence for safe rollback.
func ensureAuditCorrelationSchema(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("操作日志关联字段迁移异常: %v", recovered)
		}
	}()
	if systemSettingFlagIsSet(db, auditCorrelationMigrationKey) {
		return nil
	}
	for _, column := range []struct {
		model any
		field string
		index string
	}{
		{&entity.Log{}, "CorrelationID", "idx_logs_correlation_id"},
		{&entity.SMSVerificationCode{}, "CaptchaChallengeID", "idx_sms_verification_codes_captcha_challenge_id"},
	} {
		if !db.Migrator().HasTable(column.model) {
			return fmt.Errorf("关联字段对应数据表不存在: %T", column.model)
		}
		if !db.Migrator().HasColumn(column.model, column.field) {
			if err := db.Migrator().AddColumn(column.model, column.field); err != nil {
				return fmt.Errorf("新增关联字段 %s 失败: %w", column.field, err)
			}
		}
		if !db.Migrator().HasIndex(column.model, column.index) {
			if err := db.Migrator().CreateIndex(column.model, column.field); err != nil {
				return fmt.Errorf("新增关联索引 %s 失败: %w", column.index, err)
			}
		}
	}
	return setSystemSettingFlag(db, auditCorrelationMigrationKey)
}
