package service

import (
	"net/url"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// newRechargePromotionTestDB 在充值测试库的基础上补上赠送账本表。
//
// 单独一个 helper 而不是改 newRechargeOrderTestDB：后者被既有用例当作
// 「最小充值 schema」在用，加表会掩盖「表缺失时降级」这条路径。
func newRechargePromotionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newRechargeOrderTestDB(t)
	if err := db.AutoMigrate(&entity.RechargePromotionGrant{}); err != nil {
		t.Fatalf("migrate recharge promotion grants: %v", err)
	}
	return db
}

// setRechargePromotionEnabled 直接写开关值（nil / 非法值走另一个 helper）。
func setRechargePromotionEnabled(t *testing.T, db *gorm.DB, value string) {
	t.Helper()
	if err := saveSystemSettingValue(db, RechargePromotionEnabledSettingKey, value, "启用充值阶梯赠送（默认关闭）"); err != nil {
		t.Fatalf("set %s=%q: %v", RechargePromotionEnabledSettingKey, value, err)
	}
}

// seedRechargePromotionOrder 建一个待支付订单并返回可直接提交的已签名回调参数。
func seedRechargePromotionOrder(t *testing.T, db *gorm.DB, userID int32, orderNo string, amount float64) (entity.RechargeOrder, url.Values) {
	t.Helper()
	order := entity.RechargeOrder{
		UserID:                    userID,
		OrderNo:                   orderNo,
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "支付宝",
		Amount:                    amount,
		Status:                    rechargeOrderStatusPending,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create recharge order: %v", err)
	}
	_, privateKey := rechargePromotionTestKeys(t, db)

	values := url.Values{}
	values.Set("pid", "10001")
	values.Set("type", "alipay")
	values.Set("out_trade_no", order.OrderNo)
	values.Set("trade_no", "trade_"+orderNo)
	values.Set("trade_status", "TRADE_SUCCESS")
	values.Set("money", formatRechargeAmount(amount))
	signature, err := signRechargeValues(values, privateKey)
	if err != nil {
		t.Fatalf("sign notify values: %v", err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)
	return order, values
}

// rechargePromotionTestKeys 保证每个测试库只生成一次密钥对并把支付配置写好。
func rechargePromotionTestKeys(t *testing.T, db *gorm.DB) (string, string) {
	t.Helper()
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)
	return publicKey, privateKey
}

// loadRechargePromotionWallet 读回钱包三件套：balance / gift_balance / 账单与赠送行数。
func loadRechargePromotionWallet(t *testing.T, db *gorm.DB, userID int32) (entity.User, []entity.BillingRecord, []entity.RechargePromotionGrant) {
	t.Helper()
	var user entity.User
	if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	var records []entity.BillingRecord
	if err := db.Where("user_id = ?", userID).Order("id").Find(&records).Error; err != nil {
		t.Fatalf("load billing records: %v", err)
	}
	var grants []entity.RechargePromotionGrant
	if err := db.Where("user_id = ?", userID).Order("id").Find(&grants).Error; err != nil {
		t.Fatalf("load promotion grants: %v", err)
	}
	return user, records, grants
}

// TestSelectRechargePromotionTierUsesHighestThresholdNotAboveAmount 钉住档位表与选档规则。
//
// 规则：取**不高于本笔实付金额的最高门槛**，整笔按该档比例赠送；
// 不插值、不累进、不叠加；低于 8.8 不参与赠送。
func TestSelectRechargePromotionTierUsesHighestThresholdNotAboveAmount(t *testing.T) {
	cases := []struct {
		amount      float64
		wantMatched bool
		wantThresh  float64
		wantRate    float64
		wantGift    float64
	}{
		// 低于最低门槛：可以充值，但不参与赠送。
		{amount: 8.79, wantMatched: false},
		{amount: 1, wantMatched: false},
		{amount: 0, wantMatched: false},
		// 六档边界与档内取值（整笔按档比例，不插值）。
		{amount: 8.8, wantMatched: true, wantThresh: 8.8, wantRate: 0.10, wantGift: 0.88},
		{amount: 12.35, wantMatched: true, wantThresh: 8.8, wantRate: 0.10, wantGift: 1.235},
		{amount: 27.99, wantMatched: true, wantThresh: 8.8, wantRate: 0.10, wantGift: 2.799},
		{amount: 28, wantMatched: true, wantThresh: 28, wantRate: 0.25, wantGift: 7},
		{amount: 87.99, wantMatched: true, wantThresh: 28, wantRate: 0.25, wantGift: 21.9975},
		{amount: 88, wantMatched: true, wantThresh: 88, wantRate: 0.50, wantGift: 44},
		{amount: 288, wantMatched: true, wantThresh: 288, wantRate: 1.00, wantGift: 288},
		{amount: 888, wantMatched: true, wantThresh: 888, wantRate: 1.50, wantGift: 1332},
		// 文档基准：1688 充值赠 3376；返佣基数仍然是 1688（见 referralRebateBaseAmount）。
		{amount: 1688, wantMatched: true, wantThresh: 1688, wantRate: 2.00, wantGift: 3376},
		// 超档沿用最高档，不硬封顶（04 稿列了两个分支，这里取「沿用 200%」）。
		{amount: 2000, wantMatched: true, wantThresh: 1688, wantRate: 2.00, wantGift: 4000},
	}
	for _, test := range cases {
		tier, matched := SelectRechargePromotionTier(test.amount)
		if matched != test.wantMatched {
			t.Fatalf("amount %v: matched = %v, want %v", test.amount, matched, test.wantMatched)
		}
		if !matched {
			if gift := ComputeRechargePromotionGiftAmount(test.amount); gift != 0 {
				t.Fatalf("amount %v: below-threshold gift = %v, want 0", test.amount, gift)
			}
			continue
		}
		if tier.Threshold != test.wantThresh || tier.Rate != test.wantRate {
			t.Fatalf("amount %v: tier = %+v, want threshold %v rate %v", test.amount, tier, test.wantThresh, test.wantRate)
		}
		if gift := ComputeRechargePromotionGiftAmount(test.amount); gift != test.wantGift {
			t.Fatalf("amount %v: gift = %v, want %v", test.amount, gift, test.wantGift)
		}
	}
}

// TestRechargePromotionDisabledCreditsPaidAmountOnly 覆盖必做测试 1：
// 开关关闭时充值 → 无赠金、无赠送流水、balance 变化 == 实付。
func TestRechargePromotionDisabledCreditsPaidAmountOnly(t *testing.T) {
	db := newRechargePromotionTestDB(t)
	user := seedRechargeOrderUser(t, db, 201, "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 3.5).Error; err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	setRechargePromotionEnabled(t, db, "false")

	// 1688 是最诱人的一档（赠送 3376）。关闭时必须一分钱都不送。
	order, values := seedRechargePromotionOrder(t, db, user.ID, "rcg_promo_off", 1688)
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("notify: acknowledged=%v err=%v", acknowledged, err)
	}

	updated, records, grants := loadRechargePromotionWallet(t, db, user.ID)
	if updated.Balance != 1691.5 {
		t.Fatalf("balance = %v, want 3.5+1688 = 1691.5 (paid amount only)", updated.Balance)
	}
	if updated.GiftBalance != 0 {
		t.Fatalf("gift_balance = %v, want 0 when switch is off", updated.GiftBalance)
	}
	if len(grants) != 0 {
		t.Fatalf("expected no promotion grant when switch is off, got %+v", grants)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 billing record (recharge), got %d: %+v", len(records), records)
	}
	if records[0].Type != rechargeBillingRecordType || records[0].Amount != order.Amount {
		t.Fatalf("unexpected billing record: %+v", records[0])
	}
	for _, record := range records {
		if record.Type == rechargePromotionBillingRecordType {
			t.Fatalf("switch off must not write a promotion billing record: %+v", record)
		}
	}
}

// TestRechargePromotionEnabledGrantsByTier 覆盖必做测试 2：
// 开关开启时充值 → 按档位赠送、有流水、balance 与 gift_balance 同步增。
func TestRechargePromotionEnabledGrantsByTier(t *testing.T) {
	db := newRechargePromotionTestDB(t)
	user := seedRechargeOrderUser(t, db, 202, "user")
	setRechargePromotionEnabled(t, db, "true")

	order, values := seedRechargePromotionOrder(t, db, user.ID, "rcg_promo_on", 1688)
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("notify: acknowledged=%v err=%v", acknowledged, err)
	}

	updated, records, grants := loadRechargePromotionWallet(t, db, user.ID)
	const wantGift = 3376.0
	if updated.Balance != 1688+wantGift {
		t.Fatalf("balance = %v, want %v (paid + gift)", updated.Balance, 1688+wantGift)
	}
	if updated.GiftBalance != wantGift {
		t.Fatalf("gift_balance = %v, want %v", updated.GiftBalance, wantGift)
	}
	if len(grants) != 1 {
		t.Fatalf("expected 1 promotion grant, got %d: %+v", len(grants), grants)
	}
	grant := grants[0]
	if grant.RechargeOrderID != order.ID || grant.UserID != user.ID || grant.PaidAmount != 1688 ||
		grant.Threshold != 1688 || grant.Rate != 2 || grant.GiftAmount != wantGift ||
		grant.PolicyVersion != RechargePromotionPolicyVersion {
		t.Fatalf("unexpected grant snapshot: %+v", grant)
	}
	// 返佣基数只认实付本金：赠金不能被算进 BaseAmount（见 referralRebateBaseAmount）。
	if base := referralRebateBaseAmount(order); base != 1688 {
		t.Fatalf("referral base = %v, want 1688 (gift must not enter the base)", base)
	}

	// 两条流水：充值（实付）+ 赠送（赠金），且赠送行金额/类型/汇总归类都对。
	var rechargeRow, promotionRow *entity.BillingRecord
	for i := range records {
		switch records[i].Type {
		case rechargeBillingRecordType:
			rechargeRow = &records[i]
		case rechargePromotionBillingRecordType:
			promotionRow = &records[i]
		default:
			t.Fatalf("unexpected billing type %q", records[i].Type)
		}
	}
	if rechargeRow == nil || rechargeRow.Amount != 1688 {
		t.Fatalf("missing/incorrect recharge row: %+v", records)
	}
	if promotionRow == nil {
		t.Fatalf("missing promotion billing row: %+v", records)
	}
	if promotionRow.Amount != wantGift || promotionRow.Provider != "system" || promotionRow.RelatedOrderID != order.ID {
		t.Fatalf("unexpected promotion row: %+v", promotionRow)
	}
	// 钱包快照必须首尾相接：充值行 after == 赠送行 before，赠送行 after == 最终余额。
	if rechargeRow.BalanceAfter != promotionRow.BalanceBefore {
		t.Fatalf("wallet snapshots not contiguous: recharge after %v, promotion before %v",
			rechargeRow.BalanceAfter, promotionRow.BalanceBefore)
	}
	if promotionRow.BalanceAfter != updated.Balance {
		t.Fatalf("promotion after = %v, want final balance %v", promotionRow.BalanceAfter, updated.Balance)
	}

	// 账单展示归类：赠送是真收入，且不能被算作消费。
	decorated := decorateBillingRecord(BillingRecordListItem{
		Type: rechargePromotionBillingRecordType, Provider: promotionRow.Provider,
		Amount: promotionRow.Amount, Remark: promotionRow.Remark,
	})
	if decorated.TypeLabel != "充值赠送" || decorated.Direction != "income" {
		t.Fatalf("unexpected decoration: %+v", decorated)
	}
	if decorated.Subject != promotionRow.Remark {
		t.Fatalf("subject = %q, want remark %q", decorated.Subject, promotionRow.Remark)
	}
}

// TestRechargePromotionRepeatCallbackGrantsOnce 覆盖必做测试 3：
// 同一订单重复回调 → 只赠送一次（金额、流水、赠送账本都不翻倍）。
func TestRechargePromotionRepeatCallbackGrantsOnce(t *testing.T) {
	db := newRechargePromotionTestDB(t)
	user := seedRechargeOrderUser(t, db, 203, "user")
	setRechargePromotionEnabled(t, db, "true")

	_, values := seedRechargePromotionOrder(t, db, user.ID, "rcg_promo_repeat", 88)
	for round := 0; round < 3; round++ {
		if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
			t.Fatalf("notify round %d: acknowledged=%v err=%v", round, acknowledged, err)
		}
	}

	updated, records, grants := loadRechargePromotionWallet(t, db, user.ID)
	if updated.Balance != 88+44 {
		t.Fatalf("balance = %v, want 132 after repeated callbacks", updated.Balance)
	}
	if updated.GiftBalance != 44 {
		t.Fatalf("gift_balance = %v, want 44 after repeated callbacks", updated.GiftBalance)
	}
	if len(grants) != 1 {
		t.Fatalf("repeated callbacks must keep 1 grant, got %d: %+v", len(grants), grants)
	}
	promotionRows := 0
	for _, record := range records {
		if record.Type == rechargePromotionBillingRecordType {
			promotionRows++
		}
	}
	if promotionRows != 1 {
		t.Fatalf("repeated callbacks must keep 1 promotion billing row, got %d: %+v", promotionRows, records)
	}
}

// TestRechargePromotionUnreadableSettingFallsBackToDisabled 覆盖必做测试 6：
// 设置读不到（缺表 / 缺行 / 非法值）→ 按关闭处理，不 panic、不赠送。
func TestRechargePromotionUnreadableSettingFallsBackToDisabled(t *testing.T) {
	t.Run("missing_row", func(t *testing.T) {
		db := newRechargePromotionTestDB(t)
		user := seedRechargeOrderUser(t, db, 204, "user")
		// 不写任何 system_settings 行。
		if GetRechargePromotionEnabledWithDB(db) {
			t.Fatal("missing setting row must read as disabled")
		}
		_, values := seedRechargePromotionOrder(t, db, user.ID, "rcg_promo_norow", 288)
		if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
			t.Fatalf("notify: acknowledged=%v err=%v", acknowledged, err)
		}
		updated, records, grants := loadRechargePromotionWallet(t, db, user.ID)
		if updated.Balance != 288 || updated.GiftBalance != 0 || len(grants) != 0 {
			t.Fatalf("missing row must not grant: balance=%v gift=%v grants=%+v", updated.Balance, updated.GiftBalance, grants)
		}
		if len(records) != 1 {
			t.Fatalf("expected only the recharge row, got %+v", records)
		}
	})

	t.Run("invalid_value", func(t *testing.T) {
		db := newRechargePromotionTestDB(t)
		user := seedRechargeOrderUser(t, db, 205, "user")
		// strconv.ParseBool 不接受 "yes"/"1 "之外的写法；非法值一律按关闭。
		setRechargePromotionEnabled(t, db, "yes")
		if GetRechargePromotionEnabledWithDB(db) {
			t.Fatal("invalid setting value must read as disabled")
		}
		_, values := seedRechargePromotionOrder(t, db, user.ID, "rcg_promo_invalid", 888)
		if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
			t.Fatalf("notify: acknowledged=%v err=%v", acknowledged, err)
		}
		updated, _, grants := loadRechargePromotionWallet(t, db, user.ID)
		if updated.Balance != 888 || updated.GiftBalance != 0 || len(grants) != 0 {
			t.Fatalf("invalid value must not grant: balance=%v gift=%v grants=%+v", updated.Balance, updated.GiftBalance, grants)
		}
	})

	t.Run("missing_system_settings_table", func(t *testing.T) {
		// 用一个只有 users/recharge_orders 的库，system_settings 表根本不存在。
		db := newRechargePromotionTestDB(t)
		if err := db.Migrator().DropTable(&entity.SystemSetting{}); err != nil {
			t.Fatalf("drop system_settings: %v", err)
		}
		user := seedRechargeOrderUser(t, db, 206, "user")
		if GetRechargePromotionEnabledWithDB(db) {
			t.Fatal("missing system_settings table must read as disabled")
		}
		// 表缺失时结账仍要走完（赠送降级），但支付配置也读不到，因此这里只断言
		// 「开关读不出来时降级为关闭、不 panic」，并且钱包不受影响。
		order := entity.RechargeOrder{
			UserID: user.ID, OrderNo: "rcg_promo_notable", PaymentProvider: DefaultPaymentProvider,
			PaymentMethodNameSnapshot: "支付宝", Amount: 28, Status: rechargeOrderStatusPending,
		}
		if err := db.Create(&order).Error; err != nil {
			t.Fatalf("create order: %v", err)
		}
		grant, err := GrantRechargePromotionInTx(db, order, 0)
		if err != nil {
			t.Fatalf("grant with missing settings must degrade, not error: %v", err)
		}
		if grant != nil {
			t.Fatalf("missing settings must not grant, got %+v", grant)
		}
		if updated, _, grants := loadRechargePromotionWallet(t, db, user.ID); updated.Balance != 0 || len(grants) != 0 {
			t.Fatalf("missing settings must leave wallet untouched: balance=%v grants=%+v", updated.Balance, grants)
		}
	})

	t.Run("nil_db", func(t *testing.T) {
		if GetRechargePromotionEnabledWithDB(nil) {
			t.Fatal("nil db must read as disabled")
		}
		grant, err := GrantRechargePromotionInTx(nil, entity.RechargeOrder{ID: 1, UserID: 1, Amount: 88}, 0)
		if err != nil || grant != nil {
			t.Fatalf("nil tx must degrade silently: grant=%+v err=%v", grant, err)
		}
	})
}

// TestRechargePromotionGrantDegradesWhenLedgerTableMissing 钉住「缺表按关闭/降级」：
// 赠送账本表还没迁移过时，充值照样入账，只是不赠送（绝不阻断支付）。
func TestRechargePromotionGrantDegradesWhenLedgerTableMissing(t *testing.T) {
	db := newRechargeOrderTestDB(t) // 故意不建 recharge_promotion_grants
	user := seedRechargeOrderUser(t, db, 207, "user")
	setRechargePromotionEnabled(t, db, "true")

	_, values := seedRechargePromotionOrder(t, db, user.ID, "rcg_promo_nogranttable", 88)
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("notify must still settle without the grant table: acknowledged=%v err=%v", acknowledged, err)
	}
	var updated entity.User
	if err := db.Where("id = ?", user.ID).First(&updated).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if updated.Balance != 88 || updated.GiftBalance != 0 {
		t.Fatalf("missing grant table must credit paid amount only: balance=%v gift=%v", updated.Balance, updated.GiftBalance)
	}
	var count int64
	if err := db.Model(&entity.BillingRecord{}).Where("type = ?", rechargePromotionBillingRecordType).Count(&count).Error; err != nil {
		t.Fatalf("count promotion rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("missing grant table must not write promotion rows, got %d", count)
	}
}

// TestRechargePromotionBelowThresholdGrantsNothing 钉住「低于 8.8 可充值但不赠送」。
func TestRechargePromotionBelowThresholdGrantsNothing(t *testing.T) {
	db := newRechargePromotionTestDB(t)
	user := seedRechargeOrderUser(t, db, 208, "user")
	setRechargePromotionEnabled(t, db, "true")

	_, values := seedRechargePromotionOrder(t, db, user.ID, "rcg_promo_small", 5)
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("notify: acknowledged=%v err=%v", acknowledged, err)
	}
	updated, records, grants := loadRechargePromotionWallet(t, db, user.ID)
	if updated.Balance != 5 || updated.GiftBalance != 0 || len(grants) != 0 {
		t.Fatalf("below-threshold recharge must not grant: balance=%v gift=%v grants=%+v",
			updated.Balance, updated.GiftBalance, grants)
	}
	if len(records) != 1 || records[0].Type != rechargeBillingRecordType {
		t.Fatalf("below-threshold recharge must only write the recharge row: %+v", records)
	}
}

// TestRechargePromotionEnabledButLedgerConflictIsSwallowed 直接验证唯一键冲突被吞掉：
// 同一订单重复调用 GrantRechargePromotionInTx 只加一次钱（AGENTS.md §26.4）。
func TestRechargePromotionEnabledButLedgerConflictIsSwallowed(t *testing.T) {
	db := newRechargePromotionTestDB(t)
	user := seedRechargeOrderUser(t, db, 209, "user")
	setRechargePromotionEnabled(t, db, "true")

	order := entity.RechargeOrder{
		UserID: user.ID, OrderNo: "rcg_promo_conflict", PaymentProvider: DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "支付宝", Amount: 288, Status: rechargeOrderStatusPaid,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 288).Error; err != nil {
		t.Fatalf("seed paid amount: %v", err)
	}

	first, err := GrantRechargePromotionInTx(db, order, 288)
	if err != nil || first == nil {
		t.Fatalf("first grant: grant=%+v err=%v", first, err)
	}
	// 第二次：模拟并发/重放，钱包不应再涨，返回既有记录（或 nil），绝不报错。
	second, err := GrantRechargePromotionInTx(db, order, 288)
	if err != nil {
		t.Fatalf("duplicate grant must be swallowed, got error: %v", err)
	}
	if second != nil && second.ID != first.ID {
		t.Fatalf("duplicate grant created a new row: first=%+v second=%+v", first, second)
	}

	updated, records, grants := loadRechargePromotionWallet(t, db, user.ID)
	if updated.Balance != 288+288 {
		t.Fatalf("balance = %v, want 576 (granted once)", updated.Balance)
	}
	if updated.GiftBalance != 288 {
		t.Fatalf("gift_balance = %v, want 288 (granted once)", updated.GiftBalance)
	}
	if len(grants) != 1 {
		t.Fatalf("expected exactly 1 grant row, got %d", len(grants))
	}
	promotionRows := 0
	for _, record := range records {
		if record.Type == rechargePromotionBillingRecordType {
			promotionRows++
		}
	}
	if promotionRows != 1 {
		t.Fatalf("expected exactly 1 promotion billing row, got %d", promotionRows)
	}
}

// TestRechargePromotionDoesNotTouchAccessTokenTotalAmountUsed 守住硬约束：
// 赠送只动钱包，绝不动 access_tokens.TotalAmountUsed（它永远不能被写成 0 或加赠金）。
func TestRechargePromotionDoesNotTouchAccessTokenTotalAmountUsed(t *testing.T) {
	db := newRechargePromotionTestDB(t)
	if err := db.AutoMigrate(&entity.AccessToken{}); err != nil {
		t.Fatalf("migrate access tokens: %v", err)
	}
	user := seedRechargeOrderUser(t, db, 210, "user")
	setRechargePromotionEnabled(t, db, "true")
	token := entity.AccessToken{
		UserID: user.ID, Name: "key", TokenHash: strings.Repeat("a", 64),
		TokenPrefix: "tk-", Status: "enabled", TotalAmountUsed: 12.5,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("create token: %v", err)
	}

	_, values := seedRechargePromotionOrder(t, db, user.ID, "rcg_promo_token", 288)
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("notify: acknowledged=%v err=%v", acknowledged, err)
	}
	var stored entity.AccessToken
	if err := db.Where("id = ?", token.ID).First(&stored).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	if stored.TotalAmountUsed != 12.5 {
		t.Fatalf("access_tokens.TotalAmountUsed = %v, want 12.5 untouched", stored.TotalAmountUsed)
	}
}
