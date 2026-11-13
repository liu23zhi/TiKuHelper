package service

// 充值阶梯档位的「空表语义」回归钉子（T-30）。
//
// 这一组用例存在的**唯一理由**是钉住下面这条区分 —— 历史上这两种情况被合并成
// 同一个 ok=false，于是「管理员清空档位表」被静默解释成「恢复系统默认六档」，
// 与管理员意图正好相反（需求口径见 task_doc/123）。
//
//	① 表存在、有可用档位   → 只读表
//	② 表存在、无可用档位   → **空列表 = 不赠送**（空表 / 全部停用 / 行全非法）
//	③ 表**不存在**         → 回退常量六档（新库首启不能停发）
//
// 判据④（变异）不在测试里，而由外部变异验证完成：把 ②③ 重新合并成一个分支后，
// 至少 ① 或 ③ 必有一条变红。

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// newRechargeTierTestDB 造一个**已迁移档位表**的库（表存在但为空）。
//
// 复用 newUserLevelTestDB 而不是另建一套：后者已经迁移了
// RechargePromotionTierRule / RechargePromotionGrant / SystemSetting / BillingRecord，
// 正是端到端验证「空表 → 不赠金」所需的全部 schema。
func newRechargeTierTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newUserLevelTestDB(t)
	if !db.Migrator().HasTable(entity.TableNameRechargePromotionTierRule) {
		t.Fatalf("fixture must migrate %s", entity.TableNameRechargePromotionTierRule)
	}
	return db
}

// dropRechargeTierTable 把档位表删掉，模拟「首次上线、还没跑迁移的库」。
//
// ⚠️ 这是与「表空」**唯一**的区别，而本任务的全部价值就在于这两种情况不能再混淆。
// 因此它必须是独立 helper，而不是靠 flag 在同一个 db 上切换 —— 那种写法会让
// 用例本身也把两种情况揉在一起。
func dropRechargeTierTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Migrator().DropTable(entity.TableNameRechargePromotionTierRule); err != nil {
		t.Fatalf("drop %s: %v", entity.TableNameRechargePromotionTierRule, err)
	}
	if db.Migrator().HasTable(entity.TableNameRechargePromotionTierRule) {
		t.Fatalf("%s must be gone after drop (otherwise this fixture does not simulate an unmigrated db)",
			entity.TableNameRechargePromotionTierRule)
	}
}

// rechargeTierWallet 读回钱包与赠送账本行数（判断「到底赠没赠」）。
func rechargeTierWallet(t *testing.T, db *gorm.DB, userID int32) (float64, float64, int64) {
	t.Helper()
	var user entity.User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatalf("read user %d: %v", userID, err)
	}
	var grants int64
	if err := db.Model(&entity.RechargePromotionGrant{}).Where("user_id = ?", userID).Count(&grants).Error; err != nil {
		t.Fatalf("count grants: %v", err)
	}
	return user.Balance, user.GiftBalance, grants
}

// grantForAmount 在给定库上对一笔已支付订单跑发放链路，返回 grant 与钱包。
//
// 直接调 GrantRechargePromotionInTx 而不走 HandleRechargePaymentNotifyWithDB：
// 后者需要 RSA 密钥与签名 fixture，会把「档位来源」这条主线埋进支付细节里。
func grantForAmount(t *testing.T, db *gorm.DB, userID int32, orderNo string, amount float64) (*entity.RechargePromotionGrant, float64, int64) {
	t.Helper()
	order := entity.RechargeOrder{
		UserID:          userID,
		OrderNo:         orderNo,
		PaymentProvider: "fixture",
		Amount:          amount,
		Status:          rechargeOrderStatusPaid,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	grant, err := GrantRechargePromotionInTx(db, order, amount)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	_, giftBalance, grantRows := rechargeTierWallet(t, db, userID)
	return grant, giftBalance, grantRows
}

// TestRechargePromotionTierEmptyTableMeansNoGift 判据①：表存在但为空 → 不赠金。
//
// 这是**行为修正**的核心断言。旧实现会在这里回退常量六档（充 1688 送 3376），
// 与「没有任何档位就是不开启充值赠送」正好相反。
func TestRechargePromotionTierEmptyTableMeansNoGift(t *testing.T) {
	db := newRechargeTierTestDB(t)
	seedLevelUser(t, db, 30)
	setRechargePromotionEnabled(t, db, "true")

	// 表存在、零行。
	var rows int64
	if err := db.Model(&entity.RechargePromotionTierRule{}).Count(&rows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("fixture must start with an empty (but existing) table, got %d rows", rows)
	}

	// ①-1 档位读取本身返回空列表，**不是**常量六档。
	tiers := RechargePromotionTiersWithDB(db)
	if len(tiers) != 0 {
		t.Fatalf("empty table must yield 0 tiers (no fallback to defaults), got %d: %+v", len(tiers), tiers)
	}

	// ①-2 选档必须一个都不命中（1688 在常量表里是最诱人的一档）。
	if tier, ok := SelectRechargePromotionTierWithDB(db, 1688); ok {
		t.Fatalf("empty table must not select any tier, got %+v", tier)
	}

	// ①-3 端到端：赠金一分不发、不写赠送账本行。
	grant, giftBalance, grantRows := grantForAmount(t, db, 30, "tier-empty", 1688)
	if grant != nil {
		t.Fatalf("empty table must not grant anything, got %+v", grant)
	}
	if giftBalance != 0 {
		t.Fatalf("gift_balance = %v, want 0 (no tiers = promotion off)", giftBalance)
	}
	if grantRows != 0 {
		t.Fatalf("expected no recharge_promotion_grants row, got %d", grantRows)
	}

	// ①-4 管理端要能看出「现在是 empty 而不是 default」——
	// 若显示 default，界面会告诉管理员「正在用系统默认六档」，那是假话。
	list, err := ListRechargePromotionTierRulesWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if list.Source != "empty" {
		t.Fatalf("source = %q, want %q (table exists but has no usable tier)", list.Source, "empty")
	}
	if len(list.Effective) != 0 {
		t.Fatalf("effective = %+v, want empty", list.Effective)
	}
}

// TestRechargePromotionTierMissingTableStillGrants 判据②：表不存在 → 仍赠金。
//
// 与判据①互斥：新库首启（未迁移）不能变成「上线即停发」。
// 变异把①②重新合并成一个分支时，这条与上一条必有一条变红 —— 这正是本项的存在理由。
func TestRechargePromotionTierMissingTableStillGrants(t *testing.T) {
	db := newRechargeTierTestDB(t)
	seedLevelUser(t, db, 31)
	setRechargePromotionEnabled(t, db, "true")
	dropRechargeTierTable(t, db)

	// ②-1 表不存在 → 回退常量六档，逐条比对数值（且**不依赖** DefaultRechargePromotionTiers 顺序漂移）。
	tiers := RechargePromotionTiersWithDB(db)
	if len(tiers) != len(DefaultRechargePromotionTiers) {
		t.Fatalf("missing table must fall back to %d constants, got %d: %+v",
			len(DefaultRechargePromotionTiers), len(tiers), tiers)
	}
	for index, want := range DefaultRechargePromotionTiers {
		if tiers[index].Threshold != want.Threshold || tiers[index].Rate != want.Rate {
			t.Fatalf("fallback tier[%d] = %+v, want %+v", index, tiers[index], want)
		}
	}

	// ②-2 选档结果与常量版逐条一致（不只是切片长得一样）。
	for _, amount := range []float64{8.8, 28, 100, 288, 500, 888, 1688, 2000} {
		wantTier, wantOK := SelectRechargePromotionTier(amount)
		gotTier, gotOK := SelectRechargePromotionTierWithDB(db, amount)
		if wantOK != gotOK || wantTier != gotTier {
			t.Fatalf("amount %v: fallback %+v/%v != constant %+v/%v", amount, gotTier, gotOK, wantTier, wantOK)
		}
	}

	// ②-3 端到端：真的会赠金。1688 档常量比例 200% → 送 3376。
	grant, giftBalance, grantRows := grantForAmount(t, db, 31, "tier-missing", 1688)
	if grant == nil {
		t.Fatal("missing table must still grant (new installs must not silently stop giving)")
	}
	if grant.Threshold != 1688 || grant.Rate != 2 || grant.GiftAmount != 3376 {
		t.Fatalf("grant snapshot = %+v, want threshold 1688 rate 2 gift 3376", grant)
	}
	if giftBalance != 3376 {
		t.Fatalf("gift_balance = %v, want 3376", giftBalance)
	}
	if grantRows != 1 {
		t.Fatalf("expected 1 grant row, got %d", grantRows)
	}

	// ②-4 来源显示 default —— 此时「正在使用系统默认六档」是**真话**。
	list, err := ListRechargePromotionTierRulesWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if list.Source != "default" {
		t.Fatalf("source = %q, want %q", list.Source, "default")
	}
}

// TestRechargePromotionTierAllDisabledMeansNoGift 判据③：全部停用 → 不赠金。
//
// 与「空表」同义（都是「表在、但没有可用档位」），但走的是不同的代码路径：
// 行存在、被 enabled=false 过滤光。如果实现只判 `len(rows) == 0`（SQL 层裸行数），
// 这条会红而空表那条绿 —— 两条都必须写。
func TestRechargePromotionTierAllDisabledMeansNoGift(t *testing.T) {
	db := newRechargeTierTestDB(t)
	seedLevelUser(t, db, 32)
	setRechargePromotionEnabled(t, db, "true")

	disabled := false
	if _, err := ReplaceRechargePromotionTierRulesWithDB(db, []RechargePromotionTierInput{
		{Threshold: 10, Rate: 0.5, Enabled: &disabled},
		{Threshold: 1688, Rate: 2.0, Enabled: &disabled},
	}); err != nil {
		t.Fatal(err)
	}
	// 行确实落库了（不是空表）—— 否则这条用例与判据①重复，测不出「停用过滤」这条路径。
	var rows int64
	if err := db.Model(&entity.RechargePromotionTierRule{}).Count(&rows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Fatalf("fixture must persist 2 disabled rows, got %d", rows)
	}

	// ③-1 档位为空、选档不命中。
	if tiers := RechargePromotionTiersWithDB(db); len(tiers) != 0 {
		t.Fatalf("all-disabled table must yield 0 tiers, got %d: %+v", len(tiers), tiers)
	}
	if tier, ok := SelectRechargePromotionTierWithDB(db, 1688); ok {
		t.Fatalf("all-disabled table must not select any tier, got %+v", tier)
	}

	// ③-2 端到端不赠金。
	grant, giftBalance, grantRows := grantForAmount(t, db, 32, "tier-disabled", 1688)
	if grant != nil || giftBalance != 0 || grantRows != 0 {
		t.Fatalf("all-disabled must not grant: grant=%+v gift_balance=%v rows=%d", grant, giftBalance, grantRows)
	}

	// ③-3 来源同样是 empty（不是 default）。
	list, err := ListRechargePromotionTierRulesWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if list.Source != "empty" {
		t.Fatalf("source = %q, want %q", list.Source, "empty")
	}
}

// TestRechargePromotionTierRowsWithNonPositiveValuesCountAsEmpty 判据③的加固：
// 行**存在且启用**，但门槛/比例非法（<=0）→ 同样视为「无可用档位」。
//
// 这一态最容易漏：SQL 层 `len(rows) == 1`，只有转成 tiers 时才被过滤光。
// 若实现用「SQL 行数」而不是「过滤后的档位数」判空，这条会红。
func TestRechargePromotionTierRowsWithNonPositiveValuesCountAsEmpty(t *testing.T) {
	db := newRechargeTierTestDB(t)
	seedLevelUser(t, db, 33)
	setRechargePromotionEnabled(t, db, "true")

	// 绕过 Replace 的校验直接写非法行 —— 相当于历史脏数据/手改库。
	if err := db.Create(&entity.RechargePromotionTierRule{Threshold: 0, Rate: 0.5, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.RechargePromotionTierRule{Threshold: 100, Rate: 0, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	if tiers := RechargePromotionTiersWithDB(db); len(tiers) != 0 {
		t.Fatalf("rows with non-positive threshold/rate must count as no usable tier, got %d: %+v", len(tiers), tiers)
	}
	grant, giftBalance, grantRows := grantForAmount(t, db, 33, "tier-invalid", 1688)
	if grant != nil || giftBalance != 0 || grantRows != 0 {
		t.Fatalf("no usable tier must not grant: grant=%+v gift_balance=%v rows=%d", grant, giftBalance, grantRows)
	}
}

// TestRechargePromotionTierEmptyIsNotMissing 是本项最重要的一条「对照」用例：
// 在**同一个测试函数**里先看「表不存在 → 六档」，删表后重建成空表再看「→ 空」。
//
// 作用：任何把两态合并的实现，无论合并到哪一边，都必然在这条里红一次。
// 只写单态用例时，实现可以「永远回退常量」或「永远返回空」而各自过一半的用例。
func TestRechargePromotionTierEmptyIsNotMissing(t *testing.T) {
	db := newRechargeTierTestDB(t)
	seedLevelUser(t, db, 34)
	setRechargePromotionEnabled(t, db, "true")

	// 阶段 A：表不存在 → 六档 + 赠金。
	dropRechargeTierTable(t, db)
	if got := len(RechargePromotionTiersWithDB(db)); got != len(DefaultRechargePromotionTiers) {
		t.Fatalf("stage A (missing table): tiers = %d, want %d constants", got, len(DefaultRechargePromotionTiers))
	}
	if grant, _, _ := grantForAmount(t, db, 34, "tier-stage-a", 1688); grant == nil {
		t.Fatal("stage A (missing table): must grant")
	}

	// 阶段 B：表建回来但为空 → 空档位 + 不赠金。
	if err := db.AutoMigrate(&entity.RechargePromotionTierRule{}); err != nil {
		t.Fatalf("re-create tier table: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameRechargePromotionTierRule) {
		t.Fatal("stage B: tier table must exist again")
	}
	if got := len(RechargePromotionTiersWithDB(db)); got != 0 {
		t.Fatalf("stage B (existing empty table): tiers = %d, want 0", got)
	}
	if grant, _, _ := grantForAmount(t, db, 34, "tier-stage-b", 1688); grant != nil {
		t.Fatalf("stage B (existing empty table): must not grant, got %+v", grant)
	}

	// 阶段 C：写回一档 → 又赠金，证明不是「一旦空过就永久关死」。
	if _, err := ReplaceRechargePromotionTierRulesWithDB(db, []RechargePromotionTierInput{
		{Threshold: 100, Rate: 0.5},
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(RechargePromotionTiersWithDB(db)); got != 1 {
		t.Fatalf("stage C (one configured tier): tiers = %d, want 1", got)
	}
	if grant, _, _ := grantForAmount(t, db, 34, "tier-stage-c", 1688); grant == nil || grant.GiftAmount != 844 {
		t.Fatalf("stage C: grant = %+v, want gift 844 (1688 * 50%%)", grant)
	}
}
