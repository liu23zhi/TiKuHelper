package service

// 等级体系（Q75-3 / Q75-6 / Q75-7，94-*.md §2 C1）。
//
// 三件事：
//  1. 判定：按「累计充值 + 累计消费」两个维度取**最高达标级**；
//  2. 落点：users.level_id（走显式 SQL，见下）；
//  3. 流水：user_level_histories，幂等键是「from→to + trigger」。
//
// ⚠️ users.level_id 的读写一律不用 entity.User 字段，而是 Select/Updates 直接点名列：
// internal/entity/users.gen.go 是 gorm.io/gen 生成文件（DO NOT EDIT），本轮不动它
// （加列在 registry/user_level_migration.go 里走显式 ALTER）。因此 entity.User 上
// 没有 LevelID 字段，只能按列名读写。**不要**为了「更优雅」去改生成文件 —— 下次
// gen 会把改动覆盖掉，而且会让「数据库有列、实体没字段」的偏差被藏起来。

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrUserLevelNotFound 等级记录不存在。
	ErrUserLevelNotFound = errors.New("等级不存在")
	// ErrUserLevelNameRequired 等级名不能为空。
	ErrUserLevelNameRequired = errors.New("等级名称不能为空")
	// ErrUserLevelRankConflict 两个等级用了同一个 rank。
	// 必须报出冲突的名字 —— 只说「重复了」管理员不知道改哪一条。
	ErrUserLevelRankConflict = errors.New("等级排序号已被占用")
	// ErrUserLevelThresholdInvalid 门槛为负。
	ErrUserLevelThresholdInvalid = errors.New("门槛金额不能为负数")
	// ErrUserLevelSchemaUnavailable 等级表还没迁移好。
	ErrUserLevelSchemaUnavailable = errors.New("等级表尚未就绪")
)

// rank 0 保留给「无等级」：历史用户 level_id 落 0，表示还没评估过。
// 因此配置的等级必须 rank >= 1（否则「取最高达标级」会与「无等级」混淆）。
const userLevelMinRank = 1

// userLevelRefundCompletedStatus 是「退款真的发生了」的终态。
//
// 只认 succeeded：pending_review / frozen / processing 都还没出钱，
// unknown 是「不知道出没出」，把它算成已退款会让等级提前掉下来。
//
// ⚠️ 为什么需要这张表：**退款不会把 recharge_orders.status 改成 refunded**。
// 实测全仓没有任何非测试代码写 `status = refunded`（rechargeOrderStatusRefunded
// 只出现在常量定义与测试里）；ticket_refund 流程只在 wallet 上扣款并在
// ticket_refunds / ticket_refund_ledgers 留账本，订单状态保持 paid。
// 所以「累计充值 = status=paid 之和」在退款后**不会自动回落** ——
// 必须显式减去已成功的退款额，Q75-6 的退款降级才真的成立。
const userLevelRefundCompletedStatus = "succeeded"

// ResolveUserLevel 从等级表里挑出「同时满足两个门槛」的最高级。
//
// 三条规则在这里一次体现：
//   - 跳级（Q75-3）：不要求逐级经过，直接返回 rank 最大的达标级；
//   - 无上限（Q75-7）：没有固定档数、也没有「封顶级」分支，传多少级就看多少级；
//   - 停用级不参与：Enabled=false 既不参与晋级，也不会被降到（调用方据此回落）。
//
// 返回 ok=false 表示没有任何级别达标（等级表为空，或全部被停用）。
func ResolveUserLevel(levels []entity.UserLevel, rechargeTotal, consumptionTotal float64) (entity.UserLevel, bool) {
	recharge := util.RoundMoney(rechargeTotal)
	consumption := util.RoundMoney(consumptionTotal)
	var best entity.UserLevel
	found := false
	for _, level := range levels {
		if !level.Enabled {
			continue
		}
		if level.Rank < userLevelMinRank {
			// rank < 1 是脏数据（与「无等级」冲突），跳过而不是让判定结果不确定。
			continue
		}
		if recharge < util.RoundMoney(level.MinRechargeTotal) {
			continue
		}
		if consumption < util.RoundMoney(level.MinConsumptionTotal) {
			continue
		}
		if !found || level.Rank > best.Rank {
			best, found = level, true
		}
	}
	return best, found
}

// SortUserLevelsByRank 按 rank 升序排序（判定与展示共用同一顺序）。
func SortUserLevelsByRank(levels []entity.UserLevel) {
	sort.SliceStable(levels, func(i, j int) bool { return levels[i].Rank < levels[j].Rank })
}

// userLevelSession 清掉继承的模型（同 rechargePromotionSession 的成因）。
func userLevelSession(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

// userLevelSchemaAvailable 判断等级表是否已就绪。
//
// 存在意义与 rechargePromotionSchemaAvailable 一致：等级评估挂在充值/退款这类
// **既有核心链路**上，表没迁移好时必须降级为「本次不评估」，绝不阻断入账或退款。
func userLevelSchemaAvailable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameUserLevel) {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return false
	}
	return db.Migrator().HasColumn(entity.TableNameUser, "level_id")
}

// ─────────────────────── 口径计算 ───────────────────────

// ComputeUserLevelTotalsWithDB is the same replay used by the personal and
// admin summaries; no wallet initial balance or guessed legacy attribution.
func ComputeUserLevelTotalsWithDB(db *gorm.DB, userID int32) (UserLevelTotals, error) {
	if db == nil || userID <= 0 {
		return UserLevelTotals{}, ErrLedgerInput
	}
	var p effectiveProjection
	read := func(tx *gorm.DB) error { var err error; p, err = replayEffectiveLedger(tx, userID); return err }
	if _, inTx := db.Statement.ConnPool.(gorm.TxCommitter); inTx {
		err := read(db)
		return p.Totals, err
	}
	err := userLevelSession(db).Transaction(read)
	return p.Totals, err
}

// CurrentUserLevelIDWithDB 读 users.level_id（0 = 无等级 / 未评估）。
func CurrentUserLevelIDWithDB(db *gorm.DB, userID int32) (int32, error) {
	if db == nil || userID <= 0 {
		return 0, nil
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		return 0, nil
	}
	var row struct {
		LevelID int32
	}
	if err := userLevelSession(db).Table(entity.TableNameUser).
		Select("level_id AS level_id").
		Where("id = ?", userID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return row.LevelID, nil
}

// ─────────────────────── 评估与落库 ───────────────────────

// UserLevelChangeResult 是一次评估的结论。
type UserLevelChangeResult struct {
	Evaluated   bool    `json:"evaluated"`
	Changed     bool    `json:"changed"`
	FromLevelID int32   `json:"fromLevelId"`
	ToLevelID   int32   `json:"toLevelId"`
	HistoryID   int32   `json:"historyId"`
	RankMoved   int32   `json:"rankMoved"`
	RechargeSum float64 `json:"rechargeTotal"`
	ConsumeSum  float64 `json:"consumptionTotal"`
}

// EvaluateUserLevelWithDB 重新评估并落库某用户的等级。
//
// 调用时机（三处触发器）：
//   - 充值到账后（trigger=recharge）；
//   - 消费记账后（trigger=consumption）；
//   - 退款后（trigger=refund）。
//
// 幂等：等级没变（from == to）直接返回，不写任何流水。等级变了则写一行
// user_level_histories，靠 event_key 的唯一索引挡住重复 —— 同一用户
// 同一 from→to 同一 trigger 只落一行，并发/重试都不会写出双份。
//
// **不允许因为评估失败影响调用方**：所有早退路径都返回 (result, nil)，
// 只把真正的数据库错误往上抛；但调用方（充值/退款事务）应当把本函数视为
// 可降级步骤，见各自注释。
func EvaluateUserLevelWithDB(db *gorm.DB, userID int32, trigger string) (UserLevelChangeResult, error) {
	if !userLevelSchemaAvailable(db) {
		return UserLevelChangeResult{}, nil
	}
	if userID <= 0 {
		return UserLevelChangeResult{}, nil
	}

	levels, err := readEffectiveLevelDefinitions(userLevelSession(db).Model(&entity.UserLevel{}))
	if err != nil {
		return UserLevelChangeResult{}, err
	}
	if len(levels) == 0 {
		// 表还没配任何等级：不评估，也不把用户降到 0（否则「清空等级表」会
		// 一次性把所有用户打成无等级，这是个不可逆的误操作放大）。
		return UserLevelChangeResult{}, nil
	}
	SortUserLevelsByRank(levels)

	totals, err := ComputeUserLevelTotalsWithDB(db, userID)
	if err != nil {
		return UserLevelChangeResult{}, err
	}
	result := UserLevelChangeResult{
		Evaluated:   true,
		RechargeSum: totals.RechargeTotal,
		ConsumeSum:  totals.ConsumptionTotal,
	}

	level, matched := resolveEffectiveLevel(levels, totals)
	targetID := int32(0)
	if matched {
		targetID = level.ID
	}

	current, err := CurrentUserLevelIDWithDB(db, userID)
	if err != nil {
		return UserLevelChangeResult{}, err
	}
	// Never turn an unexplained historical row into an automatic demotion.
	// Missing financial source tables and ambiguous legacy settlements preserve the existing entitlement.
	if totals.RequiresReview() {
		result.Evaluated = false
		result.FromLevelID = current
		result.ToLevelID = current
		return result, nil
	}
	return persistUserLevelFacts(db, userID, current, targetID, trigger, totals, levels, buildUserLevelEventKey(userID, current, targetID, trigger))
}

// Called with the exact entitlement decision and source totals. Rebuild supplies
// the locked, digest-approved rows directly; it never recalculates a second plan.
func persistUserLevelFacts(db *gorm.DB, userID, current, targetID int32, trigger string, totals UserLevelTotals, levels []entity.UserLevel, key string) (UserLevelChangeResult, error) {
	result := UserLevelChangeResult{Evaluated: true, FromLevelID: current, ToLevelID: targetID, RechargeSum: totals.RechargeTotal, ConsumeSum: totals.ConsumptionTotal}
	if current == targetID {
		return result, nil
	}

	// 先写流水（幂等锚点），再改 users.level_id。
	// 顺序与 GrantRechargePromotionInTx 一致：唯一键冲突时要能在
	// **没有动过用户行**的前提下返回，否则就得补偿写回。
	history := effectiveHistoryWrite{
		UserID:        userID,
		FromLevelID:   current,
		ToLevelID:     targetID,
		Trigger:       normalizeUserLevelTrigger(trigger),
		EventKey:      key,
		RechargeTotal: totals.Exact.Recharge, RechargeExact: totals.Exact.Recharge,
		ConsumptionTotal: totals.Exact.Consumption, ConsumptionExact: totals.Exact.Consumption,
		Remark: buildUserLevelHistoryRemark(current, targetID, levels),
	}
	ins := userLevelSession(db).Table(entity.TableNameUserLevelHistory).Clauses(clause.OnConflict{DoNothing: true}).Create(&history)
	if ins.Error != nil {
		return result, ins.Error
	}
	result.Changed = ins.RowsAffected > 0
	if result.Changed {
		result.HistoryID = history.ID
	}

	// 即使这次流水被唯一键挡掉（重复触发），level_id 也要写成目标值：
	// 上一次可能只写了流水而没改到用户行（例如进程在两步之间退出）。
	updated := userLevelSession(db).Table(entity.TableNameUser).Where("id = ? AND level_id = ?", userID, current).Update("level_id", targetID)
	if updated.Error != nil {
		return result, updated.Error
	}
	if updated.RowsAffected != 1 {
		return result, ErrLedgerStale
	}

	return result, nil
}

// normalizeUserLevelTrigger 把外部传入的 trigger 收敛到已知取值。
func normalizeUserLevelTrigger(trigger string) string {
	switch strings.TrimSpace(trigger) {
	case entity.UserLevelTriggerRecharge:
		return entity.UserLevelTriggerRecharge
	case entity.UserLevelTriggerConsumption:
		return entity.UserLevelTriggerConsumption
	case entity.UserLevelTriggerRefund:
		return entity.UserLevelTriggerRefund
	case entity.UserLevelTriggerReconcile:
		return entity.UserLevelTriggerReconcile
	case entity.UserLevelTriggerRegister:
		// 注册触发的首次评级（137 §10.3 / A1-6）。
		// ⚠️ 漏掉这一条会让 trigger 被静默收敛成 manual —— 于是「首次评级为 X」
		// 与「管理员手改」在流水里完全同形，事后无法区分这两种完全不同的来源。
		return entity.UserLevelTriggerRegister
	default:
		return entity.UserLevelTriggerManual
	}
}

// buildUserLevelEventKey 生成幂等键：level:<userID>:<from>-<to>:<trigger>。
//
// 只放业务主体与**判定结论**，不放时间戳、不放调用 id ——
// 否则每次调用都生成新键，幂等就等于没做。
func buildUserLevelEventKey(userID, fromLevelID, toLevelID int32, trigger string) string {
	return fmt.Sprintf("level:%d:%d-%d:%s", userID, fromLevelID, toLevelID, normalizeUserLevelTrigger(trigger))
}

// buildUserLevelHistoryRemark 写一条人能读懂的变更说明（含等级名）。
func buildUserLevelHistoryRemark(fromLevelID, toLevelID int32, levels []entity.UserLevel) string {
	name := func(id int32) string {
		if id <= 0 {
			return "无等级"
		}
		for _, level := range levels {
			if level.ID == id {
				return level.Name
			}
		}
		return fmt.Sprintf("已删除等级#%d", id)
	}
	from, to := name(fromLevelID), name(toLevelID)
	if toLevelID > fromLevelID && fromLevelID != 0 {
		return from + " → " + to + "（升级）"
	}
	if fromLevelID == 0 {
		return "首次评级为 " + to
	}
	return from + " → " + to + "（降级或调整）"
}

// ─────────────────────── 管理端 CRUD ───────────────────────

// ListUserLevelsWithDB 返回全部等级（按 rank 升序），含停用级。
//
// 管理端要看到停用级才能重新启用，因此这里不过滤 Enabled。
func ListUserLevelsWithDB(db *gorm.DB) ([]entity.UserLevel, error) {
	if db == nil {
		return nil, ErrUserLevelSchemaUnavailable
	}
	if !userLevelSchemaAvailable(db) {
		return nil, ErrUserLevelSchemaUnavailable
	}
	levels, err := readEffectiveLevelDefinitions(userLevelSession(db).Model(&entity.UserLevel{}))
	if err != nil {
		return nil, err
	}
	SortUserLevelsByRank(levels)
	if levels == nil {
		levels = []entity.UserLevel{}
	}
	return levels, nil
}

// UserLevelInput 是新增/编辑等级的入参。
//
// DeviceLimit 是 C3 的契约占位：本轮不消费，但要能编辑并随等级下发，
// 这样 C3 落地时不需要再改一次表结构或再加一次管理端表单。
type UserLevelInput struct {
	Name                string  `json:"name"`
	Rank                int32   `json:"rank"`
	MinRechargeTotal    float64 `json:"minRechargeTotal"`
	MinConsumptionTotal float64 `json:"minConsumptionTotal"`
	DeviceLimit         int32   `json:"deviceLimit"`
	Enabled             *bool   `json:"enabled"`
	Description         string  `json:"description"`
}

// validateUserLevelInput 校验入参并把 Name / Description 归一化。
func validateUserLevelInput(input *UserLevelInput) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return ErrUserLevelNameRequired
	}
	if len([]rune(input.Name)) > 64 {
		input.Name = string([]rune(input.Name)[:64])
	}
	input.Description = strings.TrimSpace(input.Description)
	if len([]rune(input.Description)) > 255 {
		input.Description = string([]rune(input.Description)[:255])
	}
	if input.Rank < userLevelMinRank {
		return ErrUserLevelRankConflict
	}
	if input.MinRechargeTotal < 0 || input.MinConsumptionTotal < 0 {
		return ErrUserLevelThresholdInvalid
	}
	if input.DeviceLimit < 0 {
		input.DeviceLimit = 0
	}
	input.MinRechargeTotal = util.RoundMoney(input.MinRechargeTotal)
	input.MinConsumptionTotal = util.RoundMoney(input.MinConsumptionTotal)
	return nil
}

// conflictLevelName 找出占用该 rank 的等级名（排除 excludeID）。
func conflictLevelName(db *gorm.DB, rank, excludeID int32) (string, error) {
	var rows []entity.UserLevel
	if err := userLevelSession(db).Model(&entity.UserLevel{}).
		Where("level_rank = ? AND id <> ?", rank, excludeID).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Name, nil
}

// CreateUserLevelWithDB 新增一个等级。
//
// 等级数量**不设上限**（Q75-7）：这里没有任何「最多几级」的检查，
// 将来也不要加 —— 上限会立刻变成 C3 设备池的隐性档位天花板。
func CreateUserLevelWithDB(db *gorm.DB, input UserLevelInput) (entity.UserLevel, error) {
	if db == nil || !userLevelSchemaAvailable(db) {
		return entity.UserLevel{}, ErrUserLevelSchemaUnavailable
	}
	if err := validateUserLevelInput(&input); err != nil {
		return entity.UserLevel{}, err
	}
	if name, err := conflictLevelName(db, input.Rank, 0); err != nil {
		return entity.UserLevel{}, err
	} else if name != "" {
		return entity.UserLevel{}, fmt.Errorf("%w：排序号 %d 已被「%s」占用", ErrUserLevelRankConflict, input.Rank, name)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	level := entity.UserLevel{
		Name:                input.Name,
		Rank:                input.Rank,
		MinRechargeTotal:    input.MinRechargeTotal,
		MinConsumptionTotal: input.MinConsumptionTotal,
		DeviceLimit:         input.DeviceLimit,
		Enabled:             enabled,
		Description:         input.Description,
	}
	if err := userLevelSession(db).Create(&level).Error; err != nil {
		return entity.UserLevel{}, err
	}
	return level, nil
}

// UpdateUserLevelWithDB 编辑一个等级。
//
// 改门槛后**不自动重评**存量用户：门槛是配置，重评是另一件事（需要扫全表）。
// 管理端可以在保存后显式调用 RebuildEffectiveLevels 显式预览并确认重建。
func UpdateUserLevelWithDB(db *gorm.DB, id int32, input UserLevelInput) (entity.UserLevel, error) {
	if db == nil || !userLevelSchemaAvailable(db) {
		return entity.UserLevel{}, ErrUserLevelSchemaUnavailable
	}
	if id <= 0 {
		return entity.UserLevel{}, ErrUserLevelNotFound
	}
	if err := validateUserLevelInput(&input); err != nil {
		return entity.UserLevel{}, err
	}
	var existing entity.UserLevel
	if err := userLevelSession(db).Model(&entity.UserLevel{}).Where("id = ?", id).Take(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.UserLevel{}, ErrUserLevelNotFound
		}
		return entity.UserLevel{}, err
	}
	if name, err := conflictLevelName(db, input.Rank, id); err != nil {
		return entity.UserLevel{}, err
	} else if name != "" {
		return entity.UserLevel{}, fmt.Errorf("%w：排序号 %d 已被「%s」占用", ErrUserLevelRankConflict, input.Rank, name)
	}
	updates := map[string]any{
		"name":                  input.Name,
		"level_rank":            input.Rank,
		"min_recharge_total":    input.MinRechargeTotal,
		"min_consumption_total": input.MinConsumptionTotal,
		"device_limit":          input.DeviceLimit,
		"description":           input.Description,
	}
	if input.Enabled != nil {
		updates["enabled"] = *input.Enabled
	}
	if err := userLevelSession(db).Model(&entity.UserLevel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return entity.UserLevel{}, err
	}
	var fresh entity.UserLevel
	if err := userLevelSession(db).Model(&entity.UserLevel{}).Where("id = ?", id).Take(&fresh).Error; err != nil {
		return entity.UserLevel{}, err
	}
	return fresh, nil
}

// DeleteUserLevelWithDB 删除一个等级。
//
// 删除会让仍指向它的用户**回落**：把 users.level_id 置 0 并写一行 manual 流水，
// 而不是留下悬空的 level_id（那样前端会显示「等级已删除」或空白）。
// 回落是显式的一步 —— 删除本身不该顺手改用户等级，但留下悬空引用更糟。
func DeleteUserLevelWithDB(db *gorm.DB, id int32) error {
	if db == nil || !userLevelSchemaAvailable(db) {
		return ErrUserLevelSchemaUnavailable
	}
	if id <= 0 {
		return ErrUserLevelNotFound
	}
	var existing entity.UserLevel
	if err := userLevelSession(db).Model(&entity.UserLevel{}).Where("id = ?", id).Take(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserLevelNotFound
		}
		return err
	}
	if err := userLevelSession(db).Model(&entity.UserLevel{}).Where("id = ?", id).Delete(&entity.UserLevel{}).Error; err != nil {
		return err
	}
	// 回落：把指向被删等级的用户置 0 并留痕。
	var affected []struct {
		ID int32
	}
	if err := userLevelSession(db).Table(entity.TableNameUser).
		Select("id").Where("level_id = ?", id).Find(&affected).Error; err != nil {
		return err
	}
	if len(affected) == 0 {
		return nil
	}
	if err := userLevelSession(db).Table(entity.TableNameUser).
		Where("level_id = ?", id).Update("level_id", 0).Error; err != nil {
		return err
	}
	histories := make([]entity.UserLevelHistory, 0, len(affected))
	for _, row := range affected {
		histories = append(histories, entity.UserLevelHistory{
			UserID:      row.ID,
			FromLevelID: id,
			ToLevelID:   0,
			Trigger:     entity.UserLevelTriggerManual,
			EventKey:    buildUserLevelEventKey(row.ID, id, 0, entity.UserLevelTriggerManual),
			Remark:      "等级「" + existing.Name + "」被删除，回落为无等级",
		})
	}
	// 逐条插入而不是批量：批量在有唯一键冲突时 RocksDB/MySQL 的行为不一致
	// （MySQL 会静默跳过、SQLite 整批报错），逐条 + DoNothing 语义最稳定。
	for _, history := range histories {
		if err := userLevelSession(db).Clauses(clause.OnConflict{DoNothing: true}).Create(&history).Error; err != nil {
			return err
		}
	}
	return nil
}

// UserLevelSummary 是「用户当前等级」的下发结构（个人中心 / 用户列表用）。
type UserLevelSummary struct {
	LevelReviewPending  bool            `json:"levelReviewPending,omitempty"`
	Ledger              UserLevelTotals `json:"ledger"`
	LevelID             int32           `json:"levelId"`
	Name                string          `json:"name"`
	Rank                int32           `json:"rank"`
	DeviceLimit         int32           `json:"deviceLimit"`
	MinRechargeTotal    float64         `json:"minRechargeTotal"`
	MinConsumptionTotal float64         `json:"minConsumptionTotal"`
	RechargeTotal       float64         `json:"rechargeTotal"`
	ConsumptionTotal    float64         `json:"consumptionTotal"`
}

// ReconcileUserLevelWithDB 在账务事务提交后重评等级。与钱包写入使用同一用户行锁，
// 避免并发消费、退款或摘要读取按旧快照把较新的评估结果覆盖掉。
func ReconcileUserLevelWithDB(db *gorm.DB, userID int32, trigger string) (UserLevelChangeResult, error) {
	var result UserLevelChangeResult
	err := withUserLevelLock(db, userID, func(tx *gorm.DB) error {
		var err error
		result, err = EvaluateUserLevelWithDB(tx, userID, trigger)
		return err
	})
	return result, err
}

// RefreshUserLevelSummaryWithDB 先修正已保存的等级，再从同一事务返回摘要。
// 存量漏评及管理员修改门槛后，用户看到的等级与累计值必须使用相同口径。
func RefreshUserLevelSummaryWithDB(db *gorm.DB, userID int32, trigger string) (UserLevelSummary, error) {
	var summary UserLevelSummary
	err := withUserLevelLock(db, userID, func(tx *gorm.DB) error {
		if _, err := EvaluateUserLevelWithDB(tx, userID, trigger); err != nil {
			return err
		}
		var err error
		summary, err = GetUserLevelSummaryWithDB(tx, userID)
		return err
	})
	return summary, err
}

func withUserLevelLock(db *gorm.DB, userID int32, apply func(*gorm.DB) error) error {
	if db == nil || userID <= 0 || !userLevelSchemaAvailable(db) {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUserLevelHistory) {
		return ErrUserLevelSchemaUnavailable
	}
	return userLevelSession(db).Transaction(func(tx *gorm.DB) error {
		var user struct{ ID int32 }
		err := tx.Table(entity.TableNameUser).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").Where("id = ?", userID).Take(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return apply(tx)
	})
}

// GetUserLevelSummaryWithDB 读取某用户的等级与两个口径值。
//
// 读不到等级（level_id=0 或等级已删）时返回零值摘要而不是错误：
// 前端据此显示「暂无等级」，不需要额外分支。
func GetUserLevelSummaryWithDB(db *gorm.DB, userID int32) (UserLevelSummary, error) {
	if db == nil || !userLevelSchemaAvailable(db) || userID <= 0 {
		return UserLevelSummary{}, nil
	}
	levelID, err := CurrentUserLevelIDWithDB(db, userID)
	if err != nil {
		return UserLevelSummary{}, err
	}
	totals, err := ComputeUserLevelTotalsWithDB(db, userID)
	if err != nil {
		return UserLevelSummary{}, err
	}
	summary := UserLevelSummary{
		LevelReviewPending: totals.RequiresReview(),
		Ledger:             totals,
		LevelID:            levelID,
		RechargeTotal:      totals.RechargeTotal,
		ConsumptionTotal:   totals.ConsumptionTotal,
	}
	if levelID <= 0 {
		return summary, nil
	}
	var level entity.UserLevel
	if err := userLevelSession(db).Model(&entity.UserLevel{}).Where("id = ?", levelID).Take(&level).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return summary, nil
		}
		return UserLevelSummary{}, err
	}
	summary.Name = level.Name
	summary.Rank = level.Rank
	summary.DeviceLimit = level.DeviceLimit
	summary.MinRechargeTotal = util.RoundMoney(level.MinRechargeTotal)
	summary.MinConsumptionTotal = util.RoundMoney(level.MinConsumptionTotal)
	return summary, nil
}

// ListUserLevelHistoriesWithDB 返回某用户的等级变更流水（新→旧）。
func ListUserLevelHistoriesWithDB(db *gorm.DB, userID int32, limit int) ([]entity.UserLevelHistory, error) {
	if db == nil || !userLevelSchemaAvailable(db) || userID <= 0 {
		return []entity.UserLevelHistory{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows []entity.UserLevelHistory
	if err := userLevelSession(db).Model(&entity.UserLevelHistory{}).
		Where("user_id = ?", userID).Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []entity.UserLevelHistory{}
	}
	return rows, nil
}
