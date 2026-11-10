package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// loginAttemptSchemaReadyKey 标记「登录限流计数表已就绪」。
	loginAttemptSchemaReadyKey = "login_attempt_schema_ready"
)

// ensureLoginAttemptSchema 建立两套登录限流共用的计数表（45-*.md §0.1.4 七）。
//
// 覆盖：login_attempts(scope, subject_key, fail_count, window_start, locked_until, updated_at)
// 主键 (scope, subject_key) 即规格要求的「唯一索引 (scope, subject_key)」。
//
// 纪律：
//   - 结构性建表失败 → 返回错误，让启动 FATAL 看见；
//   - 没有数据回填，**不做任何 DROP TABLE**；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动幂等重跑；
//   - 索引补建放在标记判断之前（老库也能补上）。
//
// 与开关解耦：`login_rate_limit_enabled` 默认 true，但表照建 ——
// 关闭普通模式限流时表仍然存在（管理员随时可能重新打开）。
func ensureLoginAttemptSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		if err := ensureLoginAttemptIndexes(db); err != nil {
			return err
		}
	}
	if systemSettingFlagIsSet(db, loginAttemptSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureLoginAttemptSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// system_settings 还不存在（极端空库首次启动）：不写标记，
		// 否则表建出来之后本迁移会被永远跳过。
		return nil
	}
	if err := setSystemSettingFlag(db, loginAttemptSchemaReadyKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入登录限流迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureLoginAttemptSchema 在外层兜一层 recover：脏数据不能让服务反复崩溃。
func safeEnsureLoginAttemptSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyLoginAttemptSchema(db)
}

func applyLoginAttemptSchema(db *gorm.DB) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return false, nil
	}
	if err := db.AutoMigrate(&entity.LoginAttempt{}); err != nil {
		return false, fmt.Errorf("create login_attempts fail: %w", err)
	}
	if err := ensureLoginAttemptIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureLoginAttemptIndexes 补建辅助索引（locked_until，供「当前锁定中的账号/IP」列表查询）。
//
// SQLite 用 CREATE INDEX IF NOT EXISTS（可幂等重跑）；
// MySQL 不支持 IF NOT EXISTS，必须先查 information_schema。
func ensureLoginAttemptIndexes(db *gorm.DB) error {
	const indexName = "idx_login_attempt_locked_until"
	if db.Migrator().HasIndex(entity.TableNameLoginAttempt, indexName) {
		return nil
	}
	quoted := "`" + indexName + "`"
	table := "`" + entity.TableNameLoginAttempt + "`"
	var statement string
	switch strings.ToLower(strings.TrimSpace(db.Dialector.Name())) {
	case "sqlite":
		statement = fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (`locked_until`)", quoted, table)
	case "mysql":
		statement = fmt.Sprintf("CREATE INDEX %s ON %s (`locked_until`)", quoted, table)
	default:
		logger.SysLog(fmt.Sprintf("未知数据库方言 %q，跳过 login_attempts 辅助索引", db.Dialector.Name()))
		return nil
	}
	if err := db.Exec(statement).Error; err != nil {
		if db.Migrator().HasIndex(entity.TableNameLoginAttempt, indexName) {
			return nil
		}
		return fmt.Errorf("create index %s on %s fail: %w", indexName, entity.TableNameLoginAttempt, err)
	}
	return nil
}
