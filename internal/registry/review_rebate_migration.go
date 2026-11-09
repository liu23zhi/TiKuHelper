package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// reviewRebateSchemaReadyKey 标记「评价返现四张表与 users.gift_balance 已就绪」。
	reviewRebateSchemaReadyKey = "review_rebate_schema_ready"
)

// ensureReviewRebateSchema 建立评价返现所需的表结构。
//
// 覆盖（16-评价返现与邀请返现-设计定稿.md §1.6）：
//   - review_rebate_programs（活动配置，换版不重置已领次数）
//   - review_rebate_rules（规则表：(星数区间, 内容分区间) → 金额，靠 priority 决断）
//   - review_rebate_claims（用户提交，唯一键 (program_id,user_id,canonical_claim_id)）
//   - review_rebate_awards（发放流水，claim_id 唯一 → 一单一条）
//   - users.gift_balance（赠金来源标记桶）
//
// **赠金口径**（写在这里，因为这一列就是它唯一的物理落点）：
//
//	赠金计入 users.balance —— 用户可以直接消费，不新增第二个可用钱包，
//	避免「余额不够但赠金够」这种两段扣款带来的歧义。
//	赠金**不计入可退本金 / 净实付 / 返佣基数**：退款与邀请返佣只认充值实付本金，
//	否则「充值 1688 赠 3376 再退款」就能把赠金套成现金。
//	users.gift_balance 因此是**只增不减**的来源标记列（消费不扣这里），
//	用于对账与「这些钱是送的」的展示；将来若要判「赠金不可退」，判据已经在那里。
//
// 纪律（AGENTS.md 第 11 / 17 条）：
//   - 结构性建表/建列失败 → 返回错误，让启动 FATAL 看见；
//   - 本迁移没有数据回填；历史 users 行按 gift_balance=0 保留（不猜测历史赠金）；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动幂等重跑；
//   - 不做任何 DROP TABLE。
func ensureReviewRebateSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// 新增列**不受一次性标记约束**：标记只保证建表不复跑，而后来新增的列
	// 在已经跑过标记的老库上永远等不到 AutoMigrate。HasColumn 是幂等的，
	// 代价只有一次元数据查询，因此放在标记判断之前每次都核一次。
	if db.Migrator().HasTable(entity.TableNameUser) {
		if err := ensureUserGiftBalanceColumn(db); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(entity.TableNameReviewRebateClaim) {
		if err := ensureReviewRebateClaimScorerColumn(db); err != nil {
			return err
		}
	}
	if systemSettingFlagIsSet(db, reviewRebateSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureReviewRebateSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// 这一次**什么都没建**（users 表还不存在，极端空库首次启动）。
		// 绝不能在这种情况下写标记：否则 users 建出来之后，
		// 标记会让本迁移永远被跳过，评价返现的表就再也不会被创建了。
		return nil
	}
	if err := setSystemSettingFlag(db, reviewRebateSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入评价返现迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureReviewRebateSchema 在外层兜一层 recover，脏数据不能让服务反复崩溃。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsureReviewRebateSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyReviewRebateSchema(db)
}

func applyReviewRebateSchema(db *gorm.DB) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameUser) {
		// users 还没建出来（极端空库首次启动）：交给 AutoMigrate(User) 处理，
		// 这里直接返回「未执行」，下次启动 users 就存在了。
		return false, nil
	}
	if err := ensureUserGiftBalanceColumn(db); err != nil {
		return false, err
	}
	if err := db.AutoMigrate(&entity.ReviewRebateProgram{}); err != nil {
		return false, fmt.Errorf("create review_rebate_programs fail: %w", err)
	}
	if err := db.AutoMigrate(&entity.ReviewRebateRule{}); err != nil {
		return false, fmt.Errorf("create review_rebate_rules fail: %w", err)
	}
	if err := db.AutoMigrate(&entity.ReviewRebateClaim{}); err != nil {
		return false, fmt.Errorf("create review_rebate_claims fail: %w", err)
	}
	if err := ensureReviewRebateClaimScorerColumn(db); err != nil {
		return false, fmt.Errorf("add review_rebate_claims.agent_scorer fail: %w", err)
	}
	if err := db.AutoMigrate(&entity.ReviewRebateAward{}); err != nil {
		return false, fmt.Errorf("create review_rebate_awards fail: %w", err)
	}
	return true, nil
}

// ensureUserGiftBalanceColumn 为 users 补上赠金标记列。
//
// SQLite 走原生 ALTER（与 ensureUserReferralAttributionColumns 同构：sqlite 驱动的
// AddColumn 支持不如其它驱动稳定）。列必须 NOT NULL DEFAULT 0，历史行按
// 「从未拿过赠金」处理 —— 不做历史回填，也不根据历史账单倒推赠金来源
// （历史 registration_bonus 行可以推，但推断出来的值一旦写进钱包口径就再难纠正，
// 收益小于风险；对账用 billing_records 的 type 汇总即可）。
func ensureUserGiftBalanceColumn(db *gorm.DB) error {
	if db.Migrator().HasColumn(entity.TableNameUser, "gift_balance") {
		return nil
	}
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN gift_balance real NOT NULL DEFAULT 0").Error; err != nil {
			return err
		}
	} else if err := db.Migrator().AddColumn(&entity.User{}, "GiftBalance"); err != nil {
		return err
	}
	logger.SysLog("已为 users 补上 gift_balance 列（赠金来源标记，默认 0）")
	return nil
}

// ensureReviewRebateClaimScorerColumn 为 review_rebate_claims 补上 agent_scorer 列。
//
// 背景：`agent_scorer` 是「打分器真实实现」落地时新增的列，它区分
// agent（模型看过截图）与 stub（按截图摘要派生）。老库里这张表已经存在，
// 一次性迁移标记也已写过，AutoMigrate 不会再跑 —— 必须在这里单独补。
//
// 历史行按空字符串保留（**不推测**它们是谁打的）：空 = 无法归因，
// 管理端按「未标注来源」处理，不会把它误当成模型结论。
func ensureReviewRebateClaimScorerColumn(db *gorm.DB) error {
	if db.Migrator().HasColumn(entity.TableNameReviewRebateClaim, "agent_scorer") {
		return nil
	}
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		// SQLite 走原生 ALTER（与 gift_balance 同因：驱动的 AddColumn 支持不如 MySQL 稳）。
		if err := db.Exec("ALTER TABLE " + entity.TableNameReviewRebateClaim +
			" ADD COLUMN agent_scorer varchar(16) NOT NULL DEFAULT ''").Error; err != nil {
			return err
		}
	} else if err := db.Migrator().AddColumn(&entity.ReviewRebateClaim{}, "AgentScorer"); err != nil {
		return err
	}
	logger.SysLog("已为 review_rebate_claims 补上 agent_scorer 列（打分来源标记，默认空）")
	return nil
}
