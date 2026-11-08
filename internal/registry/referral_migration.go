package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// referralSchemaReadyKey 标记「邀请码/返佣表与 users 归因列已就绪」。
	referralSchemaReadyKey = "referral_schema_ready"
)

// ensureReferralSchema 建立邀请码与返佣所需的表结构。
//
// 覆盖：
//   - referral_invites（邀请码，user_id / code 各自唯一）
//   - referral_rebates（返佣账本，recharge_order_id 唯一保证一单一条）
//   - users.invited_by_user_id / users.invited_at（归因快照列）
//
// 纪律（AGENTS.md 第 11 / 17 条）：
//   - 结构性建表/建列失败 → 返回错误，让启动 FATAL 看见；
//   - 数据回填失败 → 只记日志、不阻断启动，下次启动重试（本迁移没有回填，
//     只建结构，但保留同样的失败分层）；
//   - 用 system_settings 标记保证只跑一次，失败不写标记；
//   - 不做任何 DROP TABLE。
func ensureReferralSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if systemSettingFlagIsSet(db, referralSchemaReadyKey) {
		return nil
	}
	if err := safeEnsureReferralSchema(db); err != nil {
		return err
	}
	if err := setSystemSettingFlag(db, referralSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入邀请返佣迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureReferralSchema 在外层兜一层 recover，脏数据不能让服务反复崩溃。
func safeEnsureReferralSchema(db *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyReferralSchema(db)
}

func applyReferralSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(entity.TableNameUser) {
		// users 还没建出来（极端空库首次启动）：交给 AutoMigrate(User) 处理，
		// 这里直接返回，下次启动 users 就存在了。
		return nil
	}

	if err := ensureUserReferralAttributionColumns(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(&entity.ReferralInvite{}); err != nil {
		return fmt.Errorf("create referral_invites fail: %w", err)
	}
	if err := db.AutoMigrate(&entity.ReferralRebate{}); err != nil {
		return fmt.Errorf("create referral_rebates fail: %w", err)
	}
	return nil
}

// ensureUserReferralAttributionColumns 为 users 补上归因两列。
//
// SQLite 走原生 ALTER（与 ensureUserPhoneBindingSchemaForDriver /
// ensureUserRegisterIPSchema 同构：sqlite 驱动的 AddColumn 支持不如其它驱动稳定）。
// 两列都必须 NOT NULL DEFAULT 0 / 可空，历史行按「无归因」处理 —— 不猜测历史归因。
func ensureUserReferralAttributionColumns(db *gorm.DB) error {
	if !db.Migrator().HasColumn(entity.TableNameUser, "invited_by_user_id") {
		if strings.EqualFold(db.Dialector.Name(), "sqlite") {
			if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
				" ADD COLUMN invited_by_user_id integer NOT NULL DEFAULT 0").Error; err != nil {
				return err
			}
		} else if err := db.Migrator().AddColumn(&entity.User{}, "InvitedByUserID"); err != nil {
			return err
		}
		logger.SysLog("已为 users 补上 invited_by_user_id 列")
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "invited_at") {
		if strings.EqualFold(db.Dialector.Name(), "sqlite") {
			if err := db.Exec("ALTER TABLE " + entity.TableNameUser + " ADD COLUMN invited_at datetime").Error; err != nil {
				return err
			}
		} else if err := db.Migrator().AddColumn(&entity.User{}, "InvitedAt"); err != nil {
			return err
		}
		logger.SysLog("已为 users 补上 invited_at 列")
	}
	// 归因查询按 invited_by_user_id 过滤（统计一级受邀人数），补一个索引。
	if !db.Migrator().HasIndex(entity.TableNameUser, "idx_users_invited_by_user_id") {
		if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_users_invited_by_user_id ON " +
			entity.TableNameUser + " (invited_by_user_id)").Error; err != nil {
			// 索引建不出来只影响统计性能，不影响正确性 —— 记日志继续。
			logger.SysLog(fmt.Sprintf("创建 users.invited_by_user_id 索引失败（不影响功能）：%v", err))
		}
	}
	return nil
}
