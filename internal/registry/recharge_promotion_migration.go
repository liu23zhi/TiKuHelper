package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// rechargePromotionSchemaReadyKey 标记「充值阶梯赠送账本表已就绪」。
	rechargePromotionSchemaReadyKey = "recharge_promotion_schema_ready"
)

// ensureRechargePromotionSchema 建立充值阶梯赠送所需的表结构。
//
// 覆盖：
//   - recharge_promotion_grants（赠送发放账本，recharge_order_id 唯一 → 一单一条）
//
// 只建结构，不做任何数据回填：历史充值单是否「本该赠送」无法判定
// （开关当时可能是关的、档位当时可能不同），事后补赠等于凭空造钱。
//
// 纪律：
//   - 结构性建表失败 → 返回错误，让启动 FATAL 看见；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动幂等重跑；
//   - 外层 recover 兜底，脏数据不能让服务反复崩溃重启；
//   - 不做任何 DROP TABLE。
//
// 注意：本迁移与开关**解耦**。开关默认 false，但表照建 —— 这样运营打开开关时
// 结构已经就位，不需要为「打开开关」再安排一次重启迁移。
func ensureRechargePromotionSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if systemSettingFlagIsSet(db, rechargePromotionSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureRechargePromotionSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// 什么都没建（极端空库首次启动，users 都还没有）：绝不能写标记，
		// 否则 users 建出来之后本迁移会被永久跳过。
		return nil
	}
	if err := setSystemSettingFlag(db, rechargePromotionSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入充值赠送迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureRechargePromotionSchema 在外层兜一层 recover。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsureRechargePromotionSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyRechargePromotionSchema(db)
}

func applyRechargePromotionSchema(db *gorm.DB) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameUser) {
		// users 还没建出来：交给 AutoMigrate(User) 处理，下次启动 users 就存在了。
		return false, nil
	}
	if err := db.AutoMigrate(&entity.RechargePromotionGrant{}); err != nil {
		return false, fmt.Errorf("create %s fail: %w", entity.TableNameRechargePromotionGrant, err)
	}
	// gift_balance 由 ensureReviewRebateSchema 负责补列；这里只做一次只读自检并记日志，
	// 不重复 ALTER（同一列两处维护会打架）。缺失时赠金发放会在 service 层降级为
	// 「本次不发放赠金」，绝不让充值回调失败。
	if !db.Migrator().HasColumn(entity.TableNameUser, "gift_balance") {
		logger.SysLog("users.gift_balance 尚未就绪（由 ensureReviewRebateSchema 负责补列）；充值赠金将在该列就绪后才发放")
	}
	return true, nil
}
