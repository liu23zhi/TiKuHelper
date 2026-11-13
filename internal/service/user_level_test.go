package service

// 等级体系与充值阶梯档位的回归测试（94-*.md §2 C1 / C2）。
//
// 判据一律是「如果实现坏了，这条会不会红」，并配合两次变异验证：
//   ① 把升级判定的门槛比较符号反过来 → TestResolveUserLevel* 必须红；
//   ② 把「同门槛不同比例」的校验短路掉  → TestRechargePromotionTierRejects* 必须红。
//
// 全部走真实函数 + 真实 SQLite 库，不手搓 struct 自比。

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// newUserLevelTestDB 建一个带等级体系完整 schema 的最小库。
//
// 刻意**不用** registry.ensureUserLevelSchema：service 包不能 import registry
// （registry → service，反向 import 会形成循环）。这里手写等价的三步，
// 好处是单测能独立验证「service 在 schema 就绪时行为正确」，
// 而 schema 本身的建立由 registry 包的测试负责。
func newUserLevelTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:user-level-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{}, &entity.UserLevelHistory{},
		&entity.BillingRecord{}, &entity.RechargeOrder{}, &entity.RechargePromotionTierRule{}, &entity.SystemSetting{}, &entity.RechargePromotionGrant{}, &entity.TicketRefund{}); err != nil {
		t.Fatal(err)
	}
	// users.level_id 走显式 ALTER（生成文件里没有该字段，见 registry 的注释）。
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser + " ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// seedUserLevel / seedLevel 造一个用户 / 一个等级。返回 ID。
func seedLevelUser(t *testing.T, db *gorm.DB, id int32) {
	t.Helper()
	user := entity.User{Username: "level-fixture-" + itoa32(id), Role: "user"}
	user.ID = id
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
}

func mustCreateLevel(t *testing.T, db *gorm.DB, level entity.UserLevel) entity.UserLevel {
	t.Helper()
	if err := db.Create(&level).Error; err != nil {
		t.Fatal(err)
	}
	return level
}

// ─────────────────────── 判定规则 ───────────────────────

// TestResolveUserLevelSkipsLevelsAndPicksHighest 覆盖「允许跳级」（Q75-3）与
// 「无上限」（Q75-7）：一次达标直接拿到最高达标级，不要求逐级经过，
// 且等级数量没有固定档数。
func TestResolveUserLevelSkipsLevelsAndPicksHighest(t *testing.T) {
	// 20 个等级 —— 用来证明「无上限」：没有第 5 档、第 10 档的硬编码天花板。
	levels := make([]entity.UserLevel, 0, 20)
	for rank := int32(1); rank <= 20; rank++ {
		levels = append(levels, entity.UserLevel{
			ID: rank, Rank: rank, Enabled: true,
			MinRechargeTotal:    float64(rank) * 100,
			MinConsumptionTotal: float64(rank) * 10,
		})
	}
	// 充值 2000 / 消费 200 → 同时满足 rank 20 的两个门槛 → **直接跳到 20**。
	got, ok := ResolveUserLevel(levels, 2000, 200)
	if !ok || got.Rank != 20 {
		t.Fatalf("expected to skip straight to rank 20, got %+v ok=%v", got, ok)
	}
	// 只满足充值维度：rank 20 要求消费 200，消费 50 时只到 rank 5（充值 500 且消费 50）。
	got, ok = ResolveUserLevel(levels, 2000, 50)
	if !ok || got.Rank != 5 {
		t.Fatalf("consumption dimension must gate the level, got rank %d ok=%v", got.Rank, ok)
	}
	// 一个都不满足 → ok=false（不是「落到最低级」）。
	if _, ok := ResolveUserLevel(levels, 0, 0); ok {
		t.Fatal("no level meets a 0/0 threshold here: rank 1 needs 100/10")
	}
}

// TestResolveUserLevelThresholdBoundaryIsInclusive 钉住「门槛用 >=」。
//
// 这是变异①会打红的那条：把 `>=` 改成 `<`（或把比较方向反过来）时，
// 恰好等于门槛的用户会被判为不达标，下面三个恰好在门槛上的断言全红。
func TestResolveUserLevelThresholdBoundaryIsInclusive(t *testing.T) {
	level := entity.UserLevel{ID: 7, Rank: 3, Enabled: true, MinRechargeTotal: 500, MinConsumptionTotal: 100}
	levels := []entity.UserLevel{
		{ID: 1, Rank: 1, Enabled: true},
		level,
	}
	cases := []struct {
		recharge, consumption float64
		wantRank              int32
	}{
		{500, 100, 3},    // 恰好等于门槛 → 达标
		{499.99, 100, 1}, // 充值差 0.01 → 不达标
		{500, 99.99, 1},  // 消费差 0.01 → 不达标
		{5000, 1000, 3},  // 远超门槛 → 仍是 3（没有更高级）
	}
	for _, c := range cases {
		got, ok := ResolveUserLevel(levels, c.recharge, c.consumption)
		if !ok || got.Rank != c.wantRank {
			t.Fatalf("recharge=%v consumption=%v: got rank %d ok=%v, want rank %d",
				c.recharge, c.consumption, got.Rank, ok, c.wantRank)
		}
	}
}

// TestResolveUserLevelIgnoresDisabledLevels 停用级既不参与晋级也不参与降级。
func TestResolveUserLevelIgnoresDisabledLevels(t *testing.T) {
	levels := []entity.UserLevel{
		{ID: 1, Rank: 1, Enabled: true},
		{ID: 2, Rank: 2, Enabled: false, MinRechargeTotal: 100},
		{ID: 3, Rank: 3, Enabled: true, MinRechargeTotal: 500},
	}
	// 充值 300：rank 2 被停用，rank 3 的 500 没到 → 落 rank 1。
	got, ok := ResolveUserLevel(levels, 300, 0)
	if !ok || got.ID != 1 {
		t.Fatalf("disabled rank 2 must be skipped, got %+v ok=%v", got, ok)
	}
	got, ok = ResolveUserLevel(levels, 600, 0)
	if !ok || got.ID != 3 {
		t.Fatalf("enabled rank 3 should win, got %+v ok=%v", got, ok)
	}
}

// ─────────────────────── 口径计算 ───────────────────────

// TestComputeUserLevelTotalsExcludesIncomeAndRefundedOrders 钉住两个口径：
//   - 累计充值只算 status=paid（退款后 status 变 refunded → 自动降级，Q75-6 的充值侧）；
//   - 累计消费只算 expense 方向的流水（六类收入不算消费）。
func TestComputeUserLevelTotalsExcludesIncomeAndRefundedOrders(t *testing.T) {
	db := newUserLevelTestDB(t)
	seedLevelUser(t, db, 21)

	orders := []entity.RechargeOrder{
		{UserID: 21, OrderNo: "paid-1", PaymentProvider: "fixture", Amount: 100, Status: rechargeOrderStatusPaid},
		{UserID: 21, OrderNo: "paid-2", PaymentProvider: "fixture", Amount: 50, Status: rechargeOrderStatusPaid},
		{UserID: 21, OrderNo: "refunded-1", PaymentProvider: "fixture", Amount: 300, Status: rechargeOrderStatusRefunded},
		{UserID: 21, OrderNo: "pending-1", PaymentProvider: "fixture", Amount: 999, Status: rechargeOrderStatusPending},
	}
	for _, order := range orders {
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
	}
	bills := []entity.BillingRecord{
		{UserID: 21, Type: normalSearchBillingRecordType, Amount: 5},
		{UserID: 21, Type: normalSearchBillingRecordType, Amount: 7.5},
		{UserID: 21, Type: rechargeBillingRecordType, Amount: 100},                // 收入，不算消费
		{UserID: 21, Type: rechargePromotionBillingRecordType, Amount: 20},        // 收入，不算消费
		{UserID: 21, Type: RegistrationBonusClawbackBillingRecordType, Amount: 3}, // 支出，算消费
	}
	for _, bill := range bills {
		if err := db.Create(&bill).Error; err != nil {
			t.Fatal(err)
		}
	}

	totals, err := ComputeUserLevelTotalsWithDB(db, 21)
	if err != nil {
		t.Fatal(err)
	}
	if totals.RechargeTotal != 150 {
		t.Fatalf("recharge total = %v, want 150 (only status=paid)", totals.RechargeTotal)
	}
	if totals.ConsumptionTotal != 15.5 {
		t.Fatalf("consumption total = %v, want 15.5 (5 + 7.5 + 3, income excluded)", totals.ConsumptionTotal)
	}
}

// ─────────────────────── 评估 / 幂等 / 升降级 ───────────────────────

// TestEvaluateUserLevelUpgradesAndWritesOneHistoryRow 覆盖升级 + 幂等：
// 同一状态重复评估只写一行流水，等级值不变。
func TestEvaluateUserLevelUpgradesAndWritesOneHistoryRow(t *testing.T) {
	db := newUserLevelTestDB(t)
	seedLevelUser(t, db, 22)
	basic := mustCreateLevel(t, db, entity.UserLevel{Name: "基础", Rank: 1, Enabled: true})
	senior := mustCreateLevel(t, db, entity.UserLevel{Name: "资深", Rank: 3, Enabled: true, MinRechargeTotal: 500, MinConsumptionTotal: 100})

	// 先造到「够 rank 3」的账单。
	if err := db.Create(&entity.RechargeOrder{UserID: 22, OrderNo: "o-22", PaymentProvider: "fixture", Amount: 600, Status: rechargeOrderStatusPaid}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.BillingRecord{UserID: 22, Type: normalSearchBillingRecordType, Amount: 120}).Error; err != nil {
		t.Fatal(err)
	}

	first, err := EvaluateUserLevelWithDB(db, 22, entity.UserLevelTriggerRecharge)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Evaluated || !first.Changed || first.ToLevelID != senior.ID || first.FromLevelID != 0 {
		t.Fatalf("first evaluation should jump 0 -> %d, got %+v", senior.ID, first)
	}
	levelID, err := CurrentUserLevelIDWithDB(db, 22)
	if err != nil || levelID != senior.ID {
		t.Fatalf("users.level_id = %d err=%v, want %d", levelID, err, senior.ID)
	}

	// 重复评估：不写第二行，Changed=false。
	second, err := EvaluateUserLevelWithDB(db, 22, entity.UserLevelTriggerRecharge)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Fatalf("repeat evaluation must not write another history row: %+v", second)
	}
	var historyCount int64
	if err := db.Model(&entity.UserLevelHistory{}).Where("user_id = ?", 22).Count(&historyCount).Error; err != nil {
		t.Fatal(err)
	}
	if historyCount != 1 {
		t.Fatalf("history rows = %d, want 1 (idempotent by event_key)", historyCount)
	}
	_ = basic
}

// TestEvaluateUserLevelDowngradesAfterRefund 覆盖 Q75-6。
//
// ⚠️ 关键：退款**不会**改 recharge_orders.status（实测全仓无任何非测试代码
// 写 `status=refunded`）。真实链路是 ticket_refund 在 wallet 上扣款并在
// ticket_refunds 落一条 succeeded 账本，订单仍保持 paid。
// 因此这条用例必须用「插入一条 succeeded 退款」来模拟退款，
// 用改订单状态来模拟的话，它测的是一个现实中不会发生的场景。
func TestEvaluateUserLevelDowngradesAfterRefund(t *testing.T) {
	db := newUserLevelTestDB(t)
	seedLevelUser(t, db, 23)
	basic := mustCreateLevel(t, db, entity.UserLevel{Name: "基础", Rank: 1, Enabled: true})
	senior := mustCreateLevel(t, db, entity.UserLevel{Name: "资深", Rank: 3, Enabled: true, MinRechargeTotal: 500})

	orderID := int32(1)
	order := entity.RechargeOrder{UserID: 23, OrderNo: "o-23", PaymentProvider: "fixture", Amount: 600, Status: rechargeOrderStatusPaid}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	orderID = order.ID
	up, err := EvaluateUserLevelWithDB(db, 23, entity.UserLevelTriggerRecharge)
	if err != nil || up.ToLevelID != senior.ID {
		t.Fatalf("expected upgrade to %d, got %+v err=%v", senior.ID, up, err)
	}

	// 退款 500 元（50000 分）成功：订单**仍是 paid**，只有账本变了。
	refund := entity.TicketRefund{
		UserID: 23, OrderID: &orderID, Kind: entity.TicketRefundKindOriginal,
		Status: userLevelRefundCompletedStatus, RequestedCents: 50000, ApprovedCents: 50000,
		OrderPaidCents: 60000, RefundNo: "refund-23", OrderNo: order.OrderNo,
	}
	if err := db.Create(&refund).Error; err != nil {
		t.Fatal(err)
	}
	// 订单状态必须仍是 paid —— 否则这条用例就退化成「改状态」的假场景。
	var fresh entity.RechargeOrder
	if err := db.First(&fresh, orderID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Status != rechargeOrderStatusPaid {
		t.Fatalf("precondition: order status must stay paid after refund, got %q", fresh.Status)
	}

	down, err := EvaluateUserLevelWithDB(db, 23, entity.UserLevelTriggerRefund)
	if err != nil {
		t.Fatal(err)
	}
	// 累计充值 600 - 500 = 100 → 不再满足 rank 3 的 500 → 降回 rank 1。
	if !down.Changed || down.ToLevelID != basic.ID {
		t.Fatalf("refund must downgrade to %d, got %+v (rechargeSum=%v)", basic.ID, down, down.RechargeSum)
	}
	levelID, _ := CurrentUserLevelIDWithDB(db, 23)
	if levelID != basic.ID {
		t.Fatalf("users.level_id = %d, want %d after refund", levelID, basic.ID)
	}

	// 未出钱的退款（processing）不得算作已退：等级不能再动。
	if err := db.Model(&entity.TicketRefund{}).Where("id = ?", refund.ID).Update("status", "processing").Error; err != nil {
		t.Fatal(err)
	}
	totals, err := ComputeUserLevelTotalsWithDB(db, 23)
	if err != nil {
		t.Fatal(err)
	}
	if totals.RechargeTotal != 600 {
		t.Fatalf("non-succeeded refund must not reduce recharge total, got %v", totals.RechargeTotal)
	}
}

// TestEvaluateUserLevelLeavesUsersAloneWhenTableEmpty 等级表为空时不动用户。
//
// 这条防的是「清空等级表 = 一次性把所有用户打成无等级」这种不可逆的误操作放大。
func TestEvaluateUserLevelLeavesUsersAloneWhenTableEmpty(t *testing.T) {
	db := newUserLevelTestDB(t)
	seedLevelUser(t, db, 24)
	if err := db.Table(entity.TableNameUser).Where("id = ?", 24).Update("level_id", 5).Error; err != nil {
		t.Fatal(err)
	}
	result, err := EvaluateUserLevelWithDB(db, 24, entity.UserLevelTriggerRecharge)
	if err != nil {
		t.Fatal(err)
	}
	if result.Evaluated || result.Changed {
		t.Fatalf("empty level table must not evaluate, got %+v", result)
	}
	levelID, _ := CurrentUserLevelIDWithDB(db, 24)
	if levelID != 5 {
		t.Fatalf("empty level table must not reset level_id, got %d", levelID)
	}
}

// TestUserLevelSchemaUnavailableDegradesSilently 表没好时必须静默降级，
// 绝不把错误抛给充值/退款链路。
func TestUserLevelSchemaUnavailableDegradesSilently(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:user-level-bare-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatal(err)
	}
	result, err := EvaluateUserLevelWithDB(db, 1, entity.UserLevelTriggerRecharge)
	if err != nil {
		t.Fatalf("missing schema must not error, got %v", err)
	}
	if result.Evaluated {
		t.Fatal("missing schema must not evaluate")
	}
	if _, err := ComputeUserLevelTotalsWithDB(db, 1); err != nil {
		t.Fatalf("totals must degrade gracefully, got %v", err)
	}
}

// ─────────────────────── 管理端 CRUD ───────────────────────

// TestUserLevelCreateRejectsDuplicateRankWithName 排序号冲突必须报出占用者名字。
func TestUserLevelCreateRejectsDuplicateRankWithName(t *testing.T) {
	db := newUserLevelTestDB(t)
	if _, err := CreateUserLevelWithDB(db, UserLevelInput{Name: "甲", Rank: 2}); err != nil {
		t.Fatal(err)
	}
	_, err := CreateUserLevelWithDB(db, UserLevelInput{Name: "乙", Rank: 2})
	if !errors.Is(err, ErrUserLevelRankConflict) {
		t.Fatalf("expected ErrUserLevelRankConflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "甲") {
		t.Fatalf("conflict message must name the occupying level, got %q", err.Error())
	}
	// rank 0 是「无等级」的保留值，必须拒绝。
	if _, err := CreateUserLevelWithDB(db, UserLevelInput{Name: "丙", Rank: 0}); !errors.Is(err, ErrUserLevelRankConflict) {
		t.Fatalf("rank 0 must be rejected, got %v", err)
	}
	// 名称为空拒绝。
	if _, err := CreateUserLevelWithDB(db, UserLevelInput{Name: "  ", Rank: 9}); !errors.Is(err, ErrUserLevelNameRequired) {
		t.Fatalf("empty name must be rejected, got %v", err)
	}
}

// TestUserLevelDeleteFallsBackReferencingUsers 删除等级后引用它的用户回落为 0 并留痕。
func TestUserLevelDeleteFallsBackReferencingUsers(t *testing.T) {
	db := newUserLevelTestDB(t)
	seedLevelUser(t, db, 25)
	level := mustCreateLevel(t, db, entity.UserLevel{Name: "将被删", Rank: 4, Enabled: true})
	if err := db.Table(entity.TableNameUser).Where("id = ?", 25).Update("level_id", level.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := DeleteUserLevelWithDB(db, level.ID); err != nil {
		t.Fatal(err)
	}
	levelID, _ := CurrentUserLevelIDWithDB(db, 25)
	if levelID != 0 {
		t.Fatalf("users.level_id = %d, want 0 after level delete", levelID)
	}
	var histories []entity.UserLevelHistory
	if err := db.Where("user_id = ?", 25).Find(&histories).Error; err != nil {
		t.Fatal(err)
	}
	if len(histories) != 1 || histories[0].ToLevelID != 0 || !strings.Contains(histories[0].Remark, "将被删") {
		t.Fatalf("expected one fallback history naming the deleted level, got %+v", histories)
	}
	// 再删一次 → 记录不存在。
	if err := DeleteUserLevelWithDB(db, level.ID); !errors.Is(err, ErrUserLevelNotFound) {
		t.Fatalf("second delete should be ErrUserLevelNotFound, got %v", err)
	}
}

// ─────────────────────── C2：档位表 ───────────────────────

// TestRechargePromotionTiersFallBackOnlyWhenTableMissing 钉住「**只有**表不存在才回退常量」。
//
// 原用例名为 FallBackToConstantsWhenTableEmpty，把「表空」与「表不存在」写成同一件事，
// 正是 T-30 要修的反向语义。现在拆成两个阶段：
//   - 表不存在（未迁移）→ 回退常量六档（新库首启不能停发）；
//   - 表存在但为空     → 空列表 = 不赠送（清空表 = 关闭赠送，不是「恢复默认六档」）。
//
// 三态与完整端到端覆盖见 recharge_promotion_tier_test.go。
func TestRechargePromotionTiersFallBackOnlyWhenTableMissing(t *testing.T) {
	// 阶段 1：表不存在 → 回退常量，与改动前逐条一致。
	missing := newUserLevelTestDB(t)
	if err := missing.Migrator().DropTable(entity.TableNameRechargePromotionTierRule); err != nil {
		t.Fatalf("drop tier table: %v", err)
	}

	tiers := RechargePromotionTiersWithDB(missing)
	if len(tiers) != len(DefaultRechargePromotionTiers) {
		t.Fatalf("missing table must fall back to %d constants, got %d", len(DefaultRechargePromotionTiers), len(tiers))
	}
	for index, want := range DefaultRechargePromotionTiers {
		if tiers[index].Threshold != want.Threshold || tiers[index].Rate != want.Rate {
			t.Fatalf("fallback tier[%d] = %+v, want %+v", index, tiers[index], want)
		}
	}
	missingList, err := ListRechargePromotionTierRulesWithDB(missing)
	if err != nil {
		t.Fatal(err)
	}
	if missingList.Source != "default" {
		t.Fatalf("missing table: source = %q, want default", missingList.Source)
	}
	if len(missingList.Rows) != 0 {
		t.Fatalf("missing table: rows = %d, want 0", len(missingList.Rows))
	}
	// 也验证「选档结果与常量版一致」——不只是表长得一样。
	for _, amount := range []float64{8.8, 28, 100, 288, 500, 888, 1688, 2000} {
		wantTier, wantOK := SelectRechargePromotionTier(amount)
		gotTier, gotOK := SelectRechargePromotionTierWithDB(missing, amount)
		if wantOK != gotOK || wantTier != gotTier {
			t.Fatalf("amount %v: db fallback %+v/%v != constant %+v/%v", amount, gotTier, gotOK, wantTier, wantOK)
		}
	}

	// 阶段 2：表存在但为空 → **不回退**，返回空档位（不赠送）。
	empty := newUserLevelTestDB(t)
	if got := RechargePromotionTiersWithDB(empty); len(got) != 0 {
		t.Fatalf("existing empty table must yield 0 tiers (not %d constants), got %+v",
			len(DefaultRechargePromotionTiers), got)
	}
	if tier, ok := SelectRechargePromotionTierWithDB(empty, 1688); ok {
		t.Fatalf("existing empty table must not select any tier, got %+v", tier)
	}
	emptyList, err := ListRechargePromotionTierRulesWithDB(empty)
	if err != nil {
		t.Fatal(err)
	}
	// source 必须是 empty 而不是 default —— 否则界面会谎称「正在用系统默认六档」。
	if emptyList.Source != "empty" {
		t.Fatalf("existing empty table: source = %q, want empty", emptyList.Source)
	}
	if len(emptyList.Rows) != 0 || len(emptyList.Effective) != 0 {
		t.Fatalf("existing empty table: rows=%d effective=%d, want 0/0", len(emptyList.Rows), len(emptyList.Effective))
	}
}

// TestRechargePromotionTierTableOverridesConstants 表一旦有数据就只读表。
func TestRechargePromotionTierTableOverridesConstants(t *testing.T) {
	db := newUserLevelTestDB(t)
	result, err := ReplaceRechargePromotionTierRulesWithDB(db, []RechargePromotionTierInput{
		{Threshold: 50, Rate: 0.2},
		{Threshold: 200, Rate: 0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "table" || len(result.Rows) != 2 {
		t.Fatalf("after save: source=%q rows=%d, want table/2", result.Source, len(result.Rows))
	}
	// 888 这笔金额在新表里选到 200 档（50%），而常量表会给 150%。
	tier, ok := SelectRechargePromotionTierWithDB(db, 888)
	if !ok || tier.Threshold != 200 || tier.Rate != 0.5 {
		t.Fatalf("configured table must win over constants, got %+v ok=%v", tier, ok)
	}
	// 清空表 → **不赠送**（不是回退常量）。888 在常量表里是 150%，这里必须一档都不命中。
	if cleared, err := ReplaceRechargePromotionTierRulesWithDB(db, nil); err != nil {
		t.Fatal(err)
	} else if cleared.Source != "empty" {
		t.Fatalf("after clearing: source = %q, want empty", cleared.Source)
	}
	if tier, ok := SelectRechargePromotionTierWithDB(db, 888); ok {
		t.Fatalf("clearing the table must disable the promotion (no fallback to constants), got %+v", tier)
	}
}

// TestRechargePromotionTierRejectsSameThresholdDifferentRate 覆盖 Q75-2 的核心校验。
//
// 变异②（把校验短路成 `if false &&`）会把这条打红。
func TestRechargePromotionTierRejectsSameThresholdDifferentRate(t *testing.T) {
	db := newUserLevelTestDB(t)
	_, err := ReplaceRechargePromotionTierRulesWithDB(db, []RechargePromotionTierInput{
		{Threshold: 88, Rate: 0.5},
		{Threshold: 88, Rate: 0.6},
	})
	if !errors.Is(err, ErrRechargePromotionTierThresholdConflict) {
		t.Fatalf("expected ErrRechargePromotionTierThresholdConflict, got %v", err)
	}
	// 错误文案必须说清是哪两档 —— 只说「冲突」管理员只能一条条试。
	message := err.Error()
	for _, want := range []string{"88", "50.00%", "60.00%"} {
		if !strings.Contains(message, want) {
			t.Fatalf("conflict message %q must contain %q", message, want)
		}
	}
	// 冲突时**一档都不能落库**（整体替换的原子性）。
	var count int64
	if err := db.Model(&entity.RechargePromotionTierRule{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected save must not write any row, got %d", count)
	}
}

// TestRechargePromotionTierAllowsSameThresholdSameRate 同门槛同比例是幂等去重，不是错误。
func TestRechargePromotionTierAllowsSameThresholdSameRate(t *testing.T) {
	db := newUserLevelTestDB(t)
	result, err := ReplaceRechargePromotionTierRulesWithDB(db, []RechargePromotionTierInput{
		{Threshold: 88, Rate: 0.5},
		{Threshold: 88, Rate: 0.5},
	})
	if err != nil {
		t.Fatalf("same threshold + same rate must dedupe, got %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("dedupe should leave 1 row, got %d", len(result.Rows))
	}
}

// TestRechargePromotionTierRejectsNonPositiveValues 门槛/比例必须为正。
func TestRechargePromotionTierRejectsNonPositiveValues(t *testing.T) {
	db := newUserLevelTestDB(t)
	for _, bad := range [][]RechargePromotionTierInput{
		{{Threshold: 0, Rate: 0.1}},
		{{Threshold: -1, Rate: 0.1}},
		{{Threshold: 10, Rate: 0}},
		{{Threshold: 10, Rate: -0.1}},
	} {
		if _, err := ReplaceRechargePromotionTierRulesWithDB(db, bad); !errors.Is(err, ErrRechargePromotionTierThresholdInvalid) {
			t.Fatalf("input %+v: expected ErrRechargePromotionTierThresholdInvalid, got %v", bad, err)
		}
	}
}

// TestRechargePromotionTierDisabledRowIsPersistedButNotUsed 停用档保留行但不参与选档。
func TestRechargePromotionTierDisabledRowIsPersistedButNotUsed(t *testing.T) {
	db := newUserLevelTestDB(t)
	disabled := false
	if _, err := ReplaceRechargePromotionTierRulesWithDB(db, []RechargePromotionTierInput{
		{Threshold: 50, Rate: 0.1},
		{Threshold: 500, Rate: 2.0, Enabled: &disabled},
	}); err != nil {
		t.Fatal(err)
	}
	list, err := ListRechargePromotionTierRulesWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Rows) != 2 {
		t.Fatalf("disabled row must still be listed, got %d rows", len(list.Rows))
	}
	// 充 600：500 档被停用，落 50 档（10%）。
	tier, ok := SelectRechargePromotionTierWithDB(db, 600)
	if !ok || tier.Threshold != 50 || tier.Rate != 0.1 {
		t.Fatalf("disabled tier must not be selected, got %+v ok=%v", tier, ok)
	}
}

// TestRechargePromotionTierPreviewUsesSubmittedNotSavedRows 试算走提交中的档位。
func TestRechargePromotionTierPreviewUsesSubmittedNotSavedRows(t *testing.T) {
	rows := []RechargePromotionTierInput{{Threshold: 100, Rate: 0.3}}
	tier, gift, matched, err := PreviewRechargePromotionGift(rows, 250)
	if err != nil {
		t.Fatal(err)
	}
	if !matched || tier.Threshold != 100 || gift != util.RoundMoney(250*0.3) {
		t.Fatalf("preview = %+v gift=%v matched=%v", tier, gift, matched)
	}
	// 冲突的档位在试算阶段也要报错（与保存同一套校验）。
	if _, _, _, err := PreviewRechargePromotionGift([]RechargePromotionTierInput{
		{Threshold: 100, Rate: 0.3}, {Threshold: 100, Rate: 0.4},
	}, 250); !errors.Is(err, ErrRechargePromotionTierThresholdConflict) {
		t.Fatalf("preview must reuse the same validation, got %v", err)
	}
}

// TestEvaluateUserLevelGrantUsesConfiguredTier 端到端：配置过的档位必须真的影响发放。
//
// 这条把「读表」与「发放」两半接起来 —— 只测 RechargePromotionTiersWithDB
// 无法证明 GrantRechargePromotionInTx 真的走了表。
func TestEvaluateUserLevelGrantUsesConfiguredTier(t *testing.T) {
	db := newUserLevelTestDB(t)
	seedLevelUser(t, db, 26)
	setRechargePromotionEnabled(t, db, "true")
	if _, err := ReplaceRechargePromotionTierRulesWithDB(db, []RechargePromotionTierInput{
		{Threshold: 10, Rate: 1.0},
	}); err != nil {
		t.Fatal(err)
	}
	order := entity.RechargeOrder{UserID: 26, OrderNo: "tier-e2e", PaymentProvider: "fixture", Amount: 100, Status: rechargeOrderStatusPaid}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	grant, err := GrantRechargePromotionInTx(db, order, 100)
	if err != nil {
		t.Fatal(err)
	}
	if grant == nil {
		t.Fatal("expected a grant")
	}
	// 配置 100% → 赠 100（常量表 100 元只会赠 50%）。
	if grant.Rate != 1.0 || grant.Threshold != 10 || grant.GiftAmount != 100 {
		t.Fatalf("grant must use the configured tier, got %+v", grant)
	}
}
