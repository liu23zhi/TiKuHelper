package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ensureRechargePromotionTierSchema 建充值阶梯赠送的可配置档位表。
//
// 只建表，不写初始数据。空表表示不赠送，只有管理员显式配置且启用的
// 档位参与赠送，不能因缺少配置而自动启用六档默认赠送。
//
// 结构性失败返回错误（外层 FATAL）。
func ensureRechargePromotionTierSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := db.AutoMigrate(&entity.RechargePromotionTierRule{}); err != nil {
		return fmt.Errorf("create recharge_promotion_tiers fail: %w", err)
	}
	return nil
}
