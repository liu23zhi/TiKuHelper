package service

// 充值阶梯赠送：可配置档位（Q75-2，94-*.md §2 C2）。
//
// 设计要点 —— **「表不存在」与「表里没有档位」是两件完全不同的事，必须分开**：
//
//   - **表不存在**（首次上线、还没迁移的库）：回退到常量表 DefaultRechargePromotionTiers。
//     这是「可安全上线」的全部依据 —— 上线不需要数据迁移、不需要「把六档常量搬进表」
//     的一次性动作，且发放行为与改动前逐条一致。**新库首启不能变成「上线即停发」**。
//
//   - **表存在、查询成功、但没有任何可用档位**（空表 / 全部停用 / 行全非法）：
//     返回**空列表 = 不赠送**。需求口径（task_doc/123）：
//     「充值赠送没有任何档位就是不开启充值赠送功能，不要回退到系统默认六档」。
//     若这里回退常量，「管理员把表清空」这个动作就会静默变成「恢复系统默认六档」，
//     与管理员意图**正好相反**，而且界面上看不出任何异常。
//
//   - 管理员一旦保存，表就不再为空，此后**只读表**（不会出现「表里三条 + 常量里六条」
//     混算的情形 —— 那种混算会让「我明明删掉了 888 这档」静默失效）。
//
// 「同门槛不允许不同比例」（Q75-2）：同一个门槛出现两个不同比例时，
// 「按门槛选档」的语义就不确定了（同一笔金额有两个候选）。保存时逐条比对，
// 冲突必须报出**到底是哪两档**，否则管理员只能一条条试。

import (
	"errors"
	"fmt"
	"sort"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

var (
	// ErrRechargePromotionTierThresholdConflict 同一门槛配了不同比例。
	ErrRechargePromotionTierThresholdConflict = errors.New("同一门槛不允许配置不同赠送比例")
	// ErrRechargePromotionTierThresholdInvalid 门槛或比例非法。
	ErrRechargePromotionTierThresholdInvalid = errors.New("档位门槛与比例必须是正数")
	// ErrRechargePromotionTierSchemaUnavailable 档位表还没迁移好。
	ErrRechargePromotionTierSchemaUnavailable = errors.New("充值阶梯档位表尚未就绪")
	// ErrRechargePromotionTierDuplicateThreshold 提交的列表内部就有重复门槛。
	ErrRechargePromotionTierDuplicateThreshold = errors.New("提交的档位里存在重复门槛")
)

// rechargePromotionTierSchemaAvailable 判断档位表是否已就绪。
//
// 表不存在时所有读取都回退到常量表 —— 这是「可安全上线」的全部依据，
// 也是单测里最小 schema 场景不炸的原因。
func rechargePromotionTierSchemaAvailable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	return db.Migrator().HasTable(entity.TableNameRechargePromotionTierRule)
}

// rechargePromotionTierSource 是「档位表给的是什么」的三态。
//
// 为什么必须三态而不是 bool：这三种情况的**正确行为完全不同**，合并成 ok=false
// 就只能取其中一种，另一种必然是错的。
//
//	表不存在   → 回退常量（新库首启不能停发）
//	表存在且空 → 不赠送（用户清空 = 关闭赠送功能）
//	表存在有档 → 只读表
type rechargePromotionTierSource int

const (
	// rechargePromotionTierSourceSchemaMissing 表不存在：未迁移 / 首次上线。
	// 只有这一态回退 DefaultRechargePromotionTiers。
	rechargePromotionTierSourceSchemaMissing rechargePromotionTierSource = iota
	// rechargePromotionTierSourceTable 表存在且有可用档位。
	rechargePromotionTierSourceTable
	// rechargePromotionTierSourceEmpty 表存在、查询成功，但没有任何可用档位
	// （空表 / 全部停用 / 行全非法）。语义 = 不开启充值赠送，返回空列表。
	rechargePromotionTierSourceEmpty
)

// loadConfiguredRechargePromotionTiers 读表并转成选档用的 RechargePromotionTier。
//
// 返回值第二项是三态（见 rechargePromotionTierSource），**不是 bool**：
//   - SchemaMissing：表不存在 → 调用方回退常量；
//   - Empty：表在但无可用档位 → 调用方返回空列表（不赠送），**不回退**；
//   - Table：正常读表。
//
// err != nil 时调用方仍回退常量：发放链路不能因为配置表抖动就停发赠金。
//
// 停用档位被排除在选档之外，但保留行（便于临时停用再恢复）。
func loadConfiguredRechargePromotionTiers(db *gorm.DB) ([]RechargePromotionTier, rechargePromotionTierSource, error) {
	if !rechargePromotionTierSchemaAvailable(db) {
		return nil, rechargePromotionTierSourceSchemaMissing, nil
	}
	var rows []entity.RechargePromotionTierRule
	if err := userLevelSession(db).Model(&entity.RechargePromotionTierRule{}).
		Where("enabled = ?", true).
		Order("threshold asc, sort_order asc").Find(&rows).Error; err != nil {
		return nil, rechargePromotionTierSourceEmpty, err
	}
	tiers := make([]RechargePromotionTier, 0, len(rows))
	for _, row := range rows {
		threshold := util.RoundMoneyToDecimals(row.Threshold, util.RechargePrecision)
		if threshold <= 0 || row.Rate <= 0 {
			continue
		}
		tiers = append(tiers, RechargePromotionTier{Threshold: threshold, Rate: row.Rate})
	}
	if len(tiers) == 0 {
		// 表存在但一条可用档位都没有 → Empty（不是 SchemaMissing）。
		return nil, rechargePromotionTierSourceEmpty, nil
	}
	return tiers, rechargePromotionTierSourceTable, nil
}

// defaultRechargePromotionTiersCopy 返回常量六档的副本（调用方可能改动切片）。
func defaultRechargePromotionTiersCopy() []RechargePromotionTier {
	fallback := make([]RechargePromotionTier, len(DefaultRechargePromotionTiers))
	copy(fallback, DefaultRechargePromotionTiers)
	return fallback
}

// RechargePromotionTiersWithDB 返回**当前生效**的档位表。
//
// 三分支（与 loadConfiguredRechargePromotionTiers 的三态一一对应）：
//   - 表存在且有可用档位 → 只读表；
//   - 表不存在（未迁移）→ 回退 DefaultRechargePromotionTiers（新库首启不能停发）；
//   - 表存在但无可用档位（空表 / 全部停用）→ **返回空列表 = 不赠送**。
//     需求口径（task_doc/123）：「充值赠送没有任何档位就是不开启充值赠送功能，
//     不要回退到系统默认六档」。这里回退常量会让「清空表」静默变成
//     「恢复系统默认六档」，与管理员意图正好相反。
//
// 这是选档与管理端展示共用的唯一入口 —— 不要在别处直接读 DefaultRechargePromotionTiers
// 或直接查表，否则两处口径会分别漂移。
func RechargePromotionTiersWithDB(db *gorm.DB) []RechargePromotionTier {
	tiers, source, err := loadConfiguredRechargePromotionTiers(db)
	if err != nil {
		// 读表失败也回退：发放链路不能因为配置表抖动就停发赠金。
		return defaultRechargePromotionTiersCopy()
	}
	switch source {
	case rechargePromotionTierSourceTable:
		return tiers
	case rechargePromotionTierSourceSchemaMissing:
		return defaultRechargePromotionTiersCopy()
	default: // rechargePromotionTierSourceEmpty
		// 非 nil 空切片：管理端 JSON 里是 [] 而不是 null，前端可直接 .length。
		return []RechargePromotionTier{}
	}
}

// 管理端「当前生效来源」的取值。与 RechargePromotionTiersWithDB 的三分支同源。
const (
	// rechargePromotionTiersSourceTable 用表。
	rechargePromotionTiersSourceTable = "table"
	// rechargePromotionTiersSourceDefault 表不存在，回退常量六档。
	rechargePromotionTiersSourceDefault = "default"
	// rechargePromotionTiersSourceEmpty 表存在但为空 → 不赠送。
	// 必须与前两者区分：此时「正在使用系统默认六档」这句话是**错的**。
	rechargePromotionTiersSourceEmpty = "empty"
)

// rechargePromotionTiersSourceWithDB 回答「这次档位从哪来」，
// 只用于管理端把当前生效来源显示给管理员（避免「我改了表但没生效」的疑惑）。
func rechargePromotionTiersSourceWithDB(db *gorm.DB) string {
	_, source, err := loadConfiguredRechargePromotionTiers(db)
	if err != nil {
		return rechargePromotionTiersSourceDefault
	}
	switch source {
	case rechargePromotionTierSourceTable:
		return rechargePromotionTiersSourceTable
	case rechargePromotionTierSourceSchemaMissing:
		return rechargePromotionTiersSourceDefault
	default:
		return rechargePromotionTiersSourceEmpty
	}
}

// selectRechargePromotionTierFrom 按「不高于实付金额的最高门槛」在**给定档位表**里选档。
//
// 与 SelectRechargePromotionTier 的区别只在于档位来源；选档规则一字不改
// （取不高于本笔金额的最高门槛，整笔按该档比例）。
func selectRechargePromotionTierFrom(tiers []RechargePromotionTier, paidAmount float64) (RechargePromotionTier, bool) {
	paid := util.RoundMoneyToDecimals(paidAmount, util.RechargePrecision)
	if paid <= 0 {
		return RechargePromotionTier{}, false
	}
	var selected RechargePromotionTier
	matched := false
	for _, tier := range tiers {
		threshold := util.RoundMoneyToDecimals(tier.Threshold, util.RechargePrecision)
		if threshold <= 0 || tier.Rate <= 0 {
			continue
		}
		if paid >= threshold && (!matched || threshold > selected.Threshold) {
			selected = RechargePromotionTier{Threshold: threshold, Rate: tier.Rate}
			matched = true
		}
	}
	return selected, matched
}

// SelectRechargePromotionTierWithDB 是发放链路使用的选档入口（读表 + 回退）。
func SelectRechargePromotionTierWithDB(db *gorm.DB, paidAmount float64) (RechargePromotionTier, bool) {
	return selectRechargePromotionTierFrom(RechargePromotionTiersWithDB(db), paidAmount)
}

// ─────────────────────── 管理端读写 ───────────────────────

// RechargePromotionTierRuleList 是管理端列表的返回结构。
//
// 同时下发 rows 与 effective/source：管理员需要知道「表现在是空的，实际在用常量」，
// 否则会以为「我明明保存了 3 档，怎么还是六档在发」。
type RechargePromotionTierRuleList struct {
	Rows      []entity.RechargePromotionTierRule `json:"rows"`
	Effective []RechargePromotionTier            `json:"effective"`
	Source    string                             `json:"source"`
}

// ListRechargePromotionTierRulesWithDB 返回管理端列表。
func ListRechargePromotionTierRulesWithDB(db *gorm.DB) (RechargePromotionTierRuleList, error) {
	if db == nil {
		return RechargePromotionTierRuleList{}, ErrRechargePromotionTierSchemaUnavailable
	}
	result := RechargePromotionTierRuleList{
		Rows:      []entity.RechargePromotionTierRule{},
		Effective: RechargePromotionTiersWithDB(db),
		Source:    rechargePromotionTiersSourceWithDB(db),
	}
	if !rechargePromotionTierSchemaAvailable(db) {
		return result, nil
	}
	var rows []entity.RechargePromotionTierRule
	if err := userLevelSession(db).Model(&entity.RechargePromotionTierRule{}).
		Order("threshold asc, sort_order asc").Find(&rows).Error; err != nil {
		return RechargePromotionTierRuleList{}, err
	}
	if rows != nil {
		result.Rows = rows
	}
	return result, nil
}

// RechargePromotionTierInput 是一条提交上来的档位。
type RechargePromotionTierInput struct {
	Threshold float64 `json:"threshold"`
	Rate      float64 `json:"rate"`
	Enabled   *bool   `json:"enabled"`
	SortOrder int32   `json:"sortOrder"`
}

// normalizeRechargePromotionTierInputs 归一化 + 校验一批档位。
//
// 三件事：
//  1. 门槛/比例必须是正数（门槛 0 在选档里被 `threshold <= 0` 跳过，
//     允许它存在等于让管理员配一条「永不生效」的档，必须当场拒）；
//  2. **同一门槛不允许不同比例**（Q75-2）—— 报出冲突的两个门槛值；
//  3. 同一门槛比例相同时去重（幂等，不算错误）。
func normalizeRechargePromotionTierInputs(inputs []RechargePromotionTierInput) ([]RechargePromotionTierInput, error) {
	normalized := make([]RechargePromotionTierInput, 0, len(inputs))
	// key 是「门槛的 2 位字符串」，值是该门槛已见过的比例。
	seen := make(map[string]float64, len(inputs))
	for index, input := range inputs {
		threshold := util.RoundMoneyToDecimals(input.Threshold, util.RechargePrecision)
		rate := util.RoundMoneyToDecimals(input.Rate, 6)
		if threshold <= 0 || rate <= 0 {
			return nil, fmt.Errorf("%w：第 %d 档（门槛 %s，比例 %s）", ErrRechargePromotionTierThresholdInvalid,
				index+1, formatPromotionMoney(threshold, util.RechargePrecision), formatPromotionMoney(rate, 6))
		}
		key := formatPromotionMoney(threshold, util.RechargePrecision)
		if existingRate, ok := seen[key]; ok {
			if util.RoundMoneyToDecimals(existingRate, 6) != rate {
				return nil, fmt.Errorf("%w：门槛 %s 同时配了 %s%% 与 %s%%",
					ErrRechargePromotionTierThresholdConflict, key,
					formatPromotionMoney(existingRate*100, 2), formatPromotionMoney(rate*100, 2))
			}
			// 同门槛同比例：静默去重，保留先出现的那条。
			continue
		}
		seen[key] = rate
		normalized = append(normalized, RechargePromotionTierInput{
			Threshold: threshold,
			Rate:      rate,
			Enabled:   input.Enabled,
			SortOrder: input.SortOrder,
		})
	}
	if len(normalized) > 0 {
		sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].Threshold < normalized[j].Threshold })
	}
	return normalized, nil
}

// ReplaceRechargePromotionTierRulesWithDB 用提交的列表**整体替换**档位表。
//
// 为什么是整体替换而不是逐条增删改：档位是一张「阶梯」，它不是独立记录 ——
// 逐条改会出现「删掉 888 又加回 888 但比例不同」这种中间态，而中间态在选档
// 语义上是不合法的（同门槛不同比例）。整体替换让「校验通过」与「生效」是同一瞬间。
//
// 提交空列表 = 清空表 = **关闭充值赠送**（返回空档位，不再赠送）。
// 这是**明确定义的行为**（不是误操作）：充值赠送没有任何档位就是不开启该功能，
// 绝不回退到系统默认六档 —— 否则「清空」会静默变成「恢复默认六档」，
// 与管理员意图正好相反。管理端会显式提示「清空后将不再赠送」。
func ReplaceRechargePromotionTierRulesWithDB(db *gorm.DB, inputs []RechargePromotionTierInput) (RechargePromotionTierRuleList, error) {
	if db == nil || !rechargePromotionTierSchemaAvailable(db) {
		return RechargePromotionTierRuleList{}, ErrRechargePromotionTierSchemaUnavailable
	}
	normalized, err := normalizeRechargePromotionTierInputs(inputs)
	if err != nil {
		return RechargePromotionTierRuleList{}, err
	}
	err = userLevelSession(db).Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("1 = 1").Delete(&entity.RechargePromotionTierRule{}).Error; err != nil {
			return err
		}
		if len(normalized) == 0 {
			return nil
		}
		rows := make([]entity.RechargePromotionTierRule, 0, len(normalized))
		for _, input := range normalized {
			enabled := true
			if input.Enabled != nil {
				enabled = *input.Enabled
			}
			rows = append(rows, entity.RechargePromotionTierRule{
				Threshold: input.Threshold,
				Rate:      input.Rate,
				Enabled:   enabled,
				SortOrder: input.SortOrder,
			})
		}
		return tx.Session(&gorm.Session{NewDB: true}).Create(&rows).Error
	})
	if err != nil {
		return RechargePromotionTierRuleList{}, err
	}
	return ListRechargePromotionTierRulesWithDB(db)
}

// rechargePromotionTiersFromInput 校验提交的档位并同时返回选档用的切片。
// 供「保存后立刻试算」这类只读校验用（例如管理端预览某笔金额赠多少）。
func rechargePromotionTiersFromInput(inputs []RechargePromotionTierInput) ([]RechargePromotionTier, error) {
	normalized, err := normalizeRechargePromotionTierInputs(inputs)
	if err != nil {
		return nil, err
	}
	tiers := make([]RechargePromotionTier, 0, len(normalized))
	for _, input := range normalized {
		if input.Enabled != nil && !*input.Enabled {
			continue
		}
		tiers = append(tiers, RechargePromotionTier{Threshold: input.Threshold, Rate: input.Rate})
	}
	return tiers, nil
}

// PreviewRechargePromotionGift 按**提交中但尚未保存**的档位试算一笔金额的赠金。
//
// 管理端用来在保存前回答「充 100 送多少」，避免「保存 → 发现算错 → 再改」的往返。
// 与真正发放共用 selectRechargePromotionTierFrom，不存在两套算法。
func PreviewRechargePromotionGift(inputs []RechargePromotionTierInput, paidAmount float64) (RechargePromotionTier, float64, bool, error) {
	tiers, err := rechargePromotionTiersFromInput(inputs)
	if err != nil {
		return RechargePromotionTier{}, 0, false, err
	}
	if len(tiers) == 0 {
		return RechargePromotionTier{}, 0, false, nil
	}
	tier, ok := selectRechargePromotionTierFrom(tiers, paidAmount)
	if !ok {
		return RechargePromotionTier{}, 0, false, nil
	}
	paid := util.RoundMoneyToDecimals(paidAmount, util.RechargePrecision)
	return tier, util.RoundMoney(paid * tier.Rate), true, nil
}

// describeRechargePromotionTierConflict 生成人类可读的冲突说明（管理端错误提示用）。
//
// 与 normalizeRechargePromotionTierInputs 的错误文案保持一致口径，
// 但这里只做「给定两档是否冲突」的判定，便于单测直接驱动。
func describeRechargePromotionTierConflict(a, b entity.RechargePromotionTierRule) string {
	if util.RoundMoneyToDecimals(a.Threshold, util.RechargePrecision) != util.RoundMoneyToDecimals(b.Threshold, util.RechargePrecision) {
		return ""
	}
	if util.RoundMoneyToDecimals(a.Rate, 6) == util.RoundMoneyToDecimals(b.Rate, 6) {
		return ""
	}
	return fmt.Sprintf("%v：门槛 %s 同时配了 %s%% 与 %s%%",
		ErrRechargePromotionTierThresholdConflict,
		formatPromotionMoney(a.Threshold, util.RechargePrecision),
		formatPromotionMoney(a.Rate*100, 2),
		formatPromotionMoney(b.Rate*100, 2))
}
