package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ensureRechargePromotionTierSchema 建充值阶梯赠送的可配置档位表。
//
// **只建表，不写初始数据** —— 这是 Q75-2 可配置化的关键设计：
// 表空时服务层回退到 service.DefaultRechargePromotionTiers（六档常量），
// 所以
//   - 上线不需要数据迁移，也不需要「先把常量搬进表」的一次性动作；
//   - 任何既有发放行为在表空时与常量表逐条一致（可回退、可安全灰度）；
//   - 管理员一旦保存，表就不再为空，此后以表为准（不会出现「表里三条
//     常量里六条」这种混算）。
//
// 结构性失败返回错误（外层 FATAL）；与开关解耦，表照建。
func ensureRechargePromotionTierSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := db.AutoMigrate(&entity.RechargePromotionTierRule{}); err != nil {
		return fmt.Errorf("create recharge_promotion_tiers fail: %w", err)
	}
	return nil
}
