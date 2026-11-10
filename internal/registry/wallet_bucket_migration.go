package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// walletBucketSplitKey 标记「双桶钱包拆账」已完成。
	walletBucketSplitKey = "wallet_bucket_split_v1"

	// walletBucketSplitGiftZeroKey 标记「存量赠金清零」已完成（用户 2026-11-09 选方案 A）。
	walletBucketSplitGiftZeroKey = "wallet_bucket_gift_zero_v1"
)

// ensureWalletBucketSplitSchema 把旧模型的「单一余额」拆成双桶语义。
//
// 背景（用户 2026-11-09 两次口径修正）：
//   - 旧模型：balance = 总可用（本金 + 赠金），gift_balance 只是**只增不减的来源标记**；
//   - 新模型：balance = **仅充值本金**（可退），gift_balance = **赠送金额**（不可退但可消费），
//     消费优先扣赠金。
//
// 存量行的 balance 里混着历史赠金，不清零的话新模型消费会**先扣这笔虚高的赠金** ——
// 等于凭空多给用户一笔可消费额度，而退款时又退不走它。所以必须处理。
//
// **口径：用户 2026-11-09 已拍板选方案 A（保守）** ——
// 不做按账单重建的精确拆账，而是把存量 `gift_balance` **全部清零**，`balance` 保持不变。
// 代价（用户已知并接受）：存量用户的历史赠金仍混在 balance 里，退款时会被一起退走。
//
// 为什么不做精确拆账：旧账单没有「本次从哪个桶扣」的字段，已消耗赠金无法还原，
// 只能得到区间 [max(0, G−C), G]；任何取值都要在「平台亏」与「用户投诉」之间取舍，
// 而用户选择用零风险换这部分精度（详见 45 号文档的未来可选方案）。
//
// 纪律：
//   - 结构性失败返回错误；**数据类操作失败只记日志**，不阻断启动；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动重试；
//   - 不做任何 DROP TABLE。
func ensureWalletBucketSplitSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return nil
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "gift_balance") {
		// 列还没建出来（ensureReviewRebateSchema 排在前面，正常不会走到这里）。
		// 直接返回，下次启动重试。
		return nil
	}
	if systemSettingFlagIsSet(db, walletBucketSplitGiftZeroKey) {
		return nil
	}
	if err := zeroLegacyGiftBalance(db); err != nil {
		logger.SysLog(fmt.Sprintf("存量赠金清零失败（下次启动会重试）：%v", err))
		return nil
	}
	if err := setSystemSettingFlag(db, walletBucketSplitGiftZeroKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入双桶拆账标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// zeroLegacyGiftBalance 把旧模型累加出来的 gift_balance 清零。
//
// 判据只看「还有非零值」：新模型下 gift_balance 是真实可用余额，但**这条迁移只在
// 首次升级时跑一次**（有 system_settings 标记），之后新产生的赠金不会被它碰到。
//
// 为什么要清零而不是保留：旧模型的 gift_balance 是「累计发放额」，只增不减，
// 里面包含**早已被消费掉**的部分。保留它 = 让用户凭一个历史计数器再花一次钱。
//
// 对账方法（迁移前后 `balance + gift_balance` 的差 = 被清零的赠金总额）：
//
//	-- 迁移前
//	SELECT SUM(balance) AS b, SUM(gift_balance) AS g, SUM(balance + gift_balance) AS total FROM users;
//	-- 迁移后（total 应当减少 g_before，b 不变）
//	SELECT SUM(balance) AS b, SUM(gift_balance) AS g, SUM(balance + gift_balance) AS total FROM users;
//
// 注意：清零是**一次性降额**，不满足「总和守恒」——这是用户明确接受的口径
// （历史赠金不再可用），不是 bug。守恒要求只适用于迁移之后的日常操作。
func zeroLegacyGiftBalance(db *gorm.DB) error {
	var cleared struct {
		RowCount int64   `gorm:"column:row_count"`
		GiftSum  float64 `gorm:"column:gift_sum"`
	}
	// 先统计（只为日志与对账留证），再清零。
	if err := db.Model(&entity.User{}).Where("gift_balance <> 0").
		Select("COUNT(*) AS row_count, COALESCE(SUM(gift_balance), 0) AS gift_sum").
		Scan(&cleared).Error; err != nil {
		return err
	}
	if cleared.RowCount == 0 {
		return nil
	}
	if err := db.Model(&entity.User{}).Where("gift_balance <> 0").Update("gift_balance", 0).Error; err != nil {
		return err
	}
	logger.SysLog(fmt.Sprintf(
		"双桶钱包拆账：已把 %d 个账户的存量 gift_balance 清零（合计 %v）。旧模型下该列是「累计发放额」而非可用余额，保留会让用户重复消费；balance（充值本金）未改动。对账：迁移前后 balance 之和应完全相同，balance+gift_balance 之和应减少该金额",
		cleared.RowCount, cleared.GiftSum))
	return nil
}
