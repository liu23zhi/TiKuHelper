package service

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// newReferralTestDB 建一个独立的内存 SQLite（按 t.Name() 隔离，见 AGENTS.md 第 9 条）。
func newReferralTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:referral-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&entity.User{},
		&entity.SystemSetting{},
		&entity.RechargeOrder{},
		&entity.PaymentEvent{},
		&entity.BillingRecord{},
		&entity.PaymentMethod{},
		&entity.IdempotencyRecord{},
		&entity.Log{},
		&entity.ReferralInvite{},
		&entity.ReferralRebate{},
		&entity.TicketRefund{},
		&entity.TicketRefundLedger{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// seedReferralUser 建一个账户；invitedBy 为 0 表示无归因。
func seedReferralUser(t *testing.T, db *gorm.DB, id int32, username string, invitedBy int32) entity.User {
	t.Helper()

	user := entity.User{
		ID:              id,
		Username:        username,
		Password:        "123456",
		Role:            "user",
		Status:          "enabled",
		RegisterSource:  "public",
		InvitedByUserID: invitedBy,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user
}

// setReferralSettings 写入总开关与比例（直接落 system_settings，与设置页写入路径一致）。
func setReferralSettings(t *testing.T, db *gorm.DB, enabled bool, rate float64) {
	t.Helper()

	if err := saveSystemSettingValue(db, ReferralRebateEnabledSettingKey, boolString(enabled), "启用邀请码与充值返佣（默认关闭）"); err != nil {
		t.Fatalf("save enabled setting: %v", err)
	}
	if err := saveSystemSettingValue(db, ReferralRebateRateSettingKey, formatReferralRate(rate), "返佣比例（0–1，不含 1）"); err != nil {
		t.Fatalf("save rate setting: %v", err)
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func formatReferralRate(rate float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.8f", rate), "0"), ".")
}

// seedReferralPaidOrder 造一笔已支付充值订单。
func seedReferralPaidOrder(t *testing.T, db *gorm.DB, orderNo string, userID int32, amount float64) entity.RechargeOrder {
	t.Helper()

	paidAt := time.Now()
	order := entity.RechargeOrder{
		UserID:                    userID,
		OrderNo:                   orderNo,
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "支付宝",
		Amount:                    amount,
		Status:                    rechargeOrderStatusPaid,
		PaidAt:                    &paidAt,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create recharge order: %v", err)
	}
	return order
}

// Test 1a：总开关关闭时注册不生成邀请码。
func TestReferralRegisterSkipsInviteWhenDisabled(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, false, 0.1)

	user := seedReferralUser(t, db, 11, "referral-disabled-user", 0)
	invite, err := RegisterReferralInviteForNewUser(db, user.ID)
	if err != nil {
		t.Fatalf("disabled path must not error: %v", err)
	}
	if invite.ID != 0 || invite.Code != "" {
		t.Fatalf("disabled switch must not create an invite code: %+v", invite)
	}

	var count int64
	if err := db.Model(&entity.ReferralInvite{}).Count(&count).Error; err != nil {
		t.Fatalf("count invites: %v", err)
	}
	if count != 0 {
		t.Fatalf("disabled switch must not persist any invite row, got %d", count)
	}
	if got := GetReferralRebateEnabledWithDB(db); got {
		t.Fatalf("switch should read false, got %t", got)
	}
}

// Test 1b：总开关关闭时支付回调不产生返佣。
func TestReferralPaymentNotifySkipsRebateWhenDisabled(t *testing.T) {
	db := newReferralTestDB(t)
	inviter := seedReferralUser(t, db, 21, "referral-off-inviter", 0)
	invited := seedReferralUser(t, db, 22, "referral-off-invited", inviter.ID)
	order := seedReferralPaidOrder(t, db, "rcg_referral_off", invited.ID, 1688)

	setReferralSettings(t, db, false, 0.1)

	rebate, err := CreateReferralRebateInTx(db, order)
	if err != nil {
		t.Fatalf("disabled switch must not error: %v", err)
	}
	if rebate != nil {
		t.Fatalf("disabled switch must not create a rebate: %+v", rebate)
	}
	var count int64
	if err := db.Model(&entity.ReferralRebate{}).Count(&count).Error; err != nil {
		t.Fatalf("count rebates: %v", err)
	}
	if count != 0 {
		t.Fatalf("disabled switch must not persist any rebate, got %d", count)
	}
}

// Test 2a：开关开启时注册生成唯一邀请码。
func TestReferralRegisterGeneratesUniqueInviteCode(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.1)

	first := seedReferralUser(t, db, 31, "referral-code-a", 0)
	second := seedReferralUser(t, db, 32, "referral-code-b", 0)

	inviteA, err := RegisterReferralInviteForNewUser(db, first.ID)
	if err != nil {
		t.Fatalf("generate invite for first: %v", err)
	}
	inviteB, err := RegisterReferralInviteForNewUser(db, second.ID)
	if err != nil {
		t.Fatalf("generate invite for second: %v", err)
	}
	if len(inviteA.Code) != referralCodeLength || len(inviteB.Code) != referralCodeLength {
		t.Fatalf("invite code must be %d chars: %q %q", referralCodeLength, inviteA.Code, inviteB.Code)
	}
	if inviteA.Code == inviteB.Code {
		t.Fatalf("two accounts got the same invite code %q", inviteA.Code)
	}
	for _, r := range inviteA.Code + inviteB.Code {
		if !strings.ContainsRune(referralCodeAlphabet, r) {
			t.Fatalf("invite code contains a confusing char %q", r)
		}
		if strings.ContainsAny(string(r), "0O1IL") {
			t.Fatalf("invite code must exclude 0/O/1/I/L, got %q", r)
		}
	}

	// 幂等：再次调用返回同一个码，绝不重新生成（已分享出去的码不能失效）。
	again, err := EnsureReferralInviteWithDB(db, first.ID)
	if err != nil {
		t.Fatalf("re-read invite: %v", err)
	}
	if again.Code != inviteA.Code {
		t.Fatalf("invite code must be stable: %q -> %q", inviteA.Code, again.Code)
	}
}

// Test 2b：用邀请码注册能绑定 invited_by_user_id。
func TestReferralBindInviterOnRegister(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.25)

	inviter := seedReferralUser(t, db, 41, "referral-inviter", 0)
	invite, err := RegisterReferralInviteForNewUser(db, inviter.ID)
	if err != nil {
		t.Fatalf("generate inviter code: %v", err)
	}

	invited := seedReferralUser(t, db, 42, "referral-invited", 0)
	resolved, err := ResolveReferralInviterWithDB(db, "  "+strings.ToLower(invite.Code)+"  ")
	if err != nil {
		t.Fatalf("resolve invite code (case/space tolerant): %v", err)
	}
	if resolved.UserID != inviter.ID {
		t.Fatalf("resolved inviter = %d, want %d", resolved.UserID, inviter.ID)
	}
	if err := BindReferralInviterInTx(db, &invited, resolved); err != nil {
		t.Fatalf("bind inviter: %v", err)
	}

	var stored entity.User
	if err := db.Where("id = ?", invited.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload invited user: %v", err)
	}
	if stored.InvitedByUserID != inviter.ID {
		t.Fatalf("invited_by_user_id = %d, want %d", stored.InvitedByUserID, inviter.ID)
	}
	if stored.InvitedAt == nil {
		t.Fatal("invited_at must be written")
	}

	// 按账户计：同一受邀人再有 Key/设备也不新增归因行。
	var inviteCount int64
	if err := db.Model(&entity.ReferralInvite{}).Count(&inviteCount).Error; err != nil {
		t.Fatalf("count invites: %v", err)
	}
	if inviteCount != 1 {
		t.Fatalf("only the inviter should own an invite code, got %d rows", inviteCount)
	}
}

// Test 3a：无效邀请码 → ErrReferralCodeInvalid。
func TestReferralRejectsUnknownInviteCode(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.1)
	seedReferralUser(t, db, 51, "referral-unknown-code", 0)

	if _, err := ResolveReferralInviterWithDB(db, "ZZZZZZZZ"); !errors.Is(err, ErrReferralCodeInvalid) {
		t.Fatalf("unknown code must be rejected, got %v", err)
	}
	if _, err := ResolveReferralInviterWithDB(db, "   "); !errors.Is(err, ErrReferralCodeInvalid) {
		t.Fatalf("blank code must be rejected, got %v", err)
	}
}

// Test 3b：自邀（同一账户用自己的码）→ ErrReferralSelfInvite。
func TestReferralRejectsSelfInvite(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.1)

	user := seedReferralUser(t, db, 61, "referral-self", 0)
	invite, err := RegisterReferralInviteForNewUser(db, user.ID)
	if err != nil {
		t.Fatalf("generate own invite: %v", err)
	}
	if err := BindReferralInviterInTx(db, &user, invite); !errors.Is(err, ErrReferralSelfInvite) {
		t.Fatalf("self invite must be rejected, got %v", err)
	}

	var stored entity.User
	if err := db.Where("id = ?", user.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored.InvitedByUserID != 0 {
		t.Fatalf("self invite must not write attribution, got %d", stored.InvitedByUserID)
	}
}

// Test 4：支付回调重复触发 → 只有一条 ReferralRebate（唯一索引生效）。
func TestReferralRebateIsUniquePerOrder(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.1)
	inviter := seedReferralUser(t, db, 71, "referral-dedupe-inviter", 0)
	invited := seedReferralUser(t, db, 72, "referral-dedupe-invited", inviter.ID)
	order := seedReferralPaidOrder(t, db, "rcg_referral_dedupe", invited.ID, 100)

	first, err := CreateReferralRebateInTx(db, order)
	if err != nil {
		t.Fatalf("first rebate: %v", err)
	}
	if first == nil || first.ID == 0 {
		t.Fatal("first rebate must be created")
	}
	second, err := CreateReferralRebateInTx(db, order)
	if err != nil {
		t.Fatalf("duplicate rebate must not error: %v", err)
	}
	if second == nil || second.ID != first.ID {
		t.Fatalf("duplicate must return the existing row, got %+v", second)
	}

	var count int64
	if err := db.Model(&entity.ReferralRebate{}).Where("recharge_order_id = ?", order.ID).Count(&count).Error; err != nil {
		t.Fatalf("count rebates: %v", err)
	}
	if count != 1 {
		t.Fatalf("one order must have exactly one rebate, got %d", count)
	}
}

// Test 5：返佣基数 = 订单实付，不含赠金（1688 赠 3376 → 基数 1688，不是 5064）。
func TestReferralRebateBaseExcludesBonus(t *testing.T) {
	db := newReferralTestDB(t)
	// 10% 比例让断言能同时区分「基数 1688」与「到账 5064」两种口径。
	setReferralSettings(t, db, true, 0.1)
	inviter := seedReferralUser(t, db, 81, "referral-base-inviter", 0)
	invited := seedReferralUser(t, db, 82, "referral-base-invited", inviter.ID)

	// 1688 档：实付 1688，赠 3376，到账 5064。
	const paidPrincipal = 1688.0
	const bonus = 3376.0
	order := seedReferralPaidOrder(t, db, "rcg_referral_1688", invited.ID, paidPrincipal)
	// 到账总额进钱包（含赠金），返佣基数不能跟着变大。
	if err := db.Model(&entity.User{}).Where("id = ?", invited.ID).
		Update("balance", paidPrincipal+bonus).Error; err != nil {
		t.Fatalf("seed wallet: %v", err)
	}

	rebate, err := CreateReferralRebateInTx(db, order)
	if err != nil {
		t.Fatalf("create rebate: %v", err)
	}
	if rebate == nil {
		t.Fatal("rebate must be created for an invited payer")
	}
	if rebate.BaseAmount != paidPrincipal {
		t.Fatalf("base amount = %v, want %v (bonus must be excluded)", rebate.BaseAmount, paidPrincipal)
	}
	if rebate.BaseAmount == paidPrincipal+bonus {
		t.Fatalf("base amount must not include the %v bonus", bonus)
	}
	if rebate.Amount != 168.8 {
		t.Fatalf("rebate amount = %v, want 168.8 (1688 * 0.1)", rebate.Amount)
	}
	if rebate.Rate != 0.1 {
		t.Fatalf("rate snapshot = %v, want 0.1", rebate.Rate)
	}
	if rebate.Status != entity.ReferralRebateStatusPending {
		t.Fatalf("new rebate status = %q, want pending", rebate.Status)
	}
	if rebate.PolicyVersion != ReferralRebatePolicyVersion {
		t.Fatalf("policy version = %q, want %q", rebate.PolicyVersion, ReferralRebatePolicyVersion)
	}

	// 返佣不写 users.balance：钱包只应包含本金 + 赠金，不含返佣。
	var stored entity.User
	if err := db.Where("id = ?", invited.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload invited: %v", err)
	}
	if stored.Balance != paidPrincipal+bonus {
		t.Fatalf("rebate must not touch users.balance, got %v", stored.Balance)
	}
	var inviterStored entity.User
	if err := db.Where("id = ?", inviter.ID).First(&inviterStored).Error; err != nil {
		t.Fatalf("reload inviter: %v", err)
	}
	if inviterStored.Balance != 0 {
		t.Fatalf("inviter balance must stay 0 (rebate is a separate ledger), got %v", inviterStored.Balance)
	}
}

// Test 6：退款后 ReferralRebate.Status == reversed（已 settled 的也照样冲回）。
func TestReferralRebateReversedOnRefund(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.2)
	inviter := seedReferralUser(t, db, 91, "referral-refund-inviter", 0)
	invited := seedReferralUser(t, db, 92, "referral-refund-invited", inviter.ID)
	order := seedReferralPaidOrder(t, db, "rcg_referral_refund", invited.ID, 288)

	rebate, err := CreateReferralRebateInTx(db, order)
	if err != nil || rebate == nil {
		t.Fatalf("create rebate: %+v err=%v", rebate, err)
	}
	// 先结算再退款：已 settled 的记录也必须能冲回。
	admin := entity.User{ID: 90, Username: "referral-refund-admin", Role: "admin", Status: "enabled"}
	settled, err := SettleReferralRebateWithDB(db, admin, rebate.ID)
	if err != nil {
		t.Fatalf("settle rebate: %v", err)
	}
	if settled.Status != entity.ReferralRebateStatusSettled || settled.SettledAt == nil {
		t.Fatalf("settle must mark status/time: %+v", settled)
	}

	reversed, err := ReverseReferralRebateForOrderInTx(db, order.ID, "原订单退款，返佣已冲回")
	if err != nil {
		t.Fatalf("reverse rebate: %v", err)
	}
	if reversed == nil || reversed.Status != entity.ReferralRebateStatusReversed {
		t.Fatalf("rebate must be reversed, got %+v", reversed)
	}
	if reversed.ReversedAt == nil {
		t.Fatal("reversed_at must be written")
	}

	var stored entity.ReferralRebate
	if err := db.Where("id = ?", rebate.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload rebate: %v", err)
	}
	if stored.Status != entity.ReferralRebateStatusReversed || stored.ReversedAt == nil {
		t.Fatalf("persisted rebate must be reversed: %+v", stored)
	}
	// 冲回只改状态，不动任何钱包。
	var inviterStored entity.User
	if err := db.Where("id = ?", inviter.ID).First(&inviterStored).Error; err != nil {
		t.Fatalf("reload inviter: %v", err)
	}
	if inviterStored.Balance != 0 {
		t.Fatalf("reversal must not write inviter balance, got %v", inviterStored.Balance)
	}

	// 幂等：再冲一次不报错、不覆盖首次时间戳。
	firstReversedAt := *stored.ReversedAt
	again, err := ReverseReferralRebateForOrderInTx(db, order.ID, "")
	if err != nil {
		t.Fatalf("idempotent reversal: %v", err)
	}
	if again == nil || again.Status != entity.ReferralRebateStatusReversed {
		t.Fatalf("second reversal must stay reversed: %+v", again)
	}
	var afterSecond entity.ReferralRebate
	if err := db.Where("id = ?", rebate.ID).First(&afterSecond).Error; err != nil {
		t.Fatalf("reload rebate: %v", err)
	}
	if afterSecond.ReversedAt == nil || !afterSecond.ReversedAt.Equal(firstReversedAt) {
		t.Fatalf("reversal timestamp must be stable: %v -> %v", firstReversedAt, afterSecond.ReversedAt)
	}

	// reversed 记录不允许再被结算。
	if _, err := SettleReferralRebateWithDB(db, admin, rebate.ID); !errors.Is(err, ErrReferralRebateState) {
		t.Fatalf("settling a reversed rebate must conflict, got %v", err)
	}
}

// Test 8：设置项默认值 referral_rebate_enabled=false、referral_rebate_rate=0。
func TestReferralSettingsDefaults(t *testing.T) {
	db := newReferralTestDB(t)

	detail, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if detail.ReferralRebateEnabled {
		t.Fatalf("referralRebateEnabled default must be false, got %t", detail.ReferralRebateEnabled)
	}
	if detail.ReferralRebateRate != 0 {
		t.Fatalf("referralRebateRate default must be 0, got %v", detail.ReferralRebateRate)
	}
	if GetReferralRebateEnabledWithDB(db) {
		t.Fatal("switch getter default must be false")
	}
	if GetReferralRebateRateWithDB(db) != 0 {
		t.Fatal("rate getter default must be 0")
	}

	// 非法持久值同样回落：读不到就不要发钱。
	if err := saveSystemSettingValue(db, ReferralRebateEnabledSettingKey, "not-a-bool", ""); err != nil {
		t.Fatalf("seed invalid bool: %v", err)
	}
	if GetReferralRebateEnabledWithDB(db) {
		t.Fatal("invalid persisted bool must read as false")
	}
	for _, invalid := range []string{"-0.1", "1", "2", "NaN", "abc"} {
		if err := saveSystemSettingValue(db, ReferralRebateRateSettingKey, invalid, ""); err != nil {
			t.Fatalf("seed invalid rate %q: %v", invalid, err)
		}
		if got := GetReferralRebateRateWithDB(db); got != 0 {
			t.Fatalf("invalid rate %q must read as 0, got %v", invalid, got)
		}
	}

	// 写入语义：nil = 保持现值；越界 = 报错。
	enabled := true
	rate := 0.15
	updated, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled:       true,
		EPayVersion:           DefaultEPayVersion,
		CaptchaProvider:       DefaultCaptchaProvider,
		ReferralRebateEnabled: &enabled,
		ReferralRebateRate:    &rate,
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if !updated.ReferralRebateEnabled || updated.ReferralRebateRate != 0.15 {
		t.Fatalf("settings not persisted: %+v", updated)
	}
	// 省略这两个字段（nil）必须保持现值，不能被静默关掉。
	kept, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled: true,
		EPayVersion:     DefaultEPayVersion,
		CaptchaProvider: DefaultCaptchaProvider,
	})
	if err != nil {
		t.Fatalf("update settings without referral fields: %v", err)
	}
	if !kept.ReferralRebateEnabled || kept.ReferralRebateRate != 0.15 {
		t.Fatalf("omitted fields must keep current values: %+v", kept)
	}
	outOfRange := 1.0
	if _, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled:    true,
		EPayVersion:        DefaultEPayVersion,
		CaptchaProvider:    DefaultCaptchaProvider,
		ReferralRebateRate: &outOfRange,
	}); !errors.Is(err, ErrInvalidReferralRebateRate) {
		t.Fatalf("rate >= 1 must be rejected, got %v", err)
	}
}

// 补充：完整的充值回调链路（含签名）也要在开关开启时产生一条返佣。
func TestReferralRebateCreatedByRechargePaymentNotify(t *testing.T) {
	db := newReferralTestDB(t)
	inviter := seedReferralUser(t, db, 101, "referral-notify-inviter", 0)
	invited := seedReferralUser(t, db, 102, "referral-notify-invited", inviter.ID)

	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)
	setReferralSettings(t, db, true, 0.1)

	order := entity.RechargeOrder{
		UserID:                    invited.ID,
		OrderNo:                   "rcg_referral_notify",
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "支付宝",
		Amount:                    1688,
		Status:                    rechargeOrderStatusPending,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	values := url.Values{}
	values.Set("pid", "10001")
	values.Set("type", "alipay")
	values.Set("out_trade_no", order.OrderNo)
	values.Set("trade_no", "trade_referral_notify")
	values.Set("trade_status", "TRADE_SUCCESS")
	values.Set("money", "1688.00")
	signature, err := signRechargeValues(values, privateKey)
	if err != nil {
		t.Fatalf("sign notify: %v", err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)

	for i := 0; i < 2; i++ {
		acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values)
		if err != nil || !acknowledged {
			t.Fatalf("notify #%d: ack=%t err=%v", i+1, acknowledged, err)
		}
	}

	var rebates []entity.ReferralRebate
	if err := db.Where("recharge_order_id = ?", order.ID).Find(&rebates).Error; err != nil {
		t.Fatalf("load rebates: %v", err)
	}
	if len(rebates) != 1 {
		t.Fatalf("repeated notify must keep exactly one rebate, got %d", len(rebates))
	}
	if rebates[0].BaseAmount != 1688 || rebates[0].Amount != 168.8 || rebates[0].InviterUserID != inviter.ID {
		t.Fatalf("unexpected rebate from notify: %+v", rebates[0])
	}
}

// 补充：没有被邀请过的账户充值不产生返佣。
func TestReferralRebateSkippedForUninvitedPayer(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.1)
	payer := seedReferralUser(t, db, 111, "referral-uninvited", 0)
	order := seedReferralPaidOrder(t, db, "rcg_referral_uninvited", payer.ID, 88)

	rebate, err := CreateReferralRebateInTx(db, order)
	if err != nil {
		t.Fatalf("uninvited payer must not error: %v", err)
	}
	if rebate != nil {
		t.Fatalf("uninvited payer must not create a rebate: %+v", rebate)
	}
}

// 补充：返佣列表与汇总的口径（一级计数、状态分桶、筛选共用条件）。
func TestReferralListAndSummary(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.5)
	inviter := seedReferralUser(t, db, 121, "referral-list-inviter", 0)
	first := seedReferralUser(t, db, 122, "referral-list-first", inviter.ID)
	second := seedReferralUser(t, db, 123, "referral-list-second", inviter.ID)

	orderA := seedReferralPaidOrder(t, db, "rcg_referral_list_a", first.ID, 100)
	orderB := seedReferralPaidOrder(t, db, "rcg_referral_list_b", second.ID, 200)
	if _, err := CreateReferralRebateInTx(db, orderA); err != nil {
		t.Fatalf("create rebate A: %v", err)
	}
	rebateB, err := CreateReferralRebateInTx(db, orderB)
	if err != nil {
		t.Fatalf("create rebate B: %v", err)
	}
	admin := entity.User{ID: 120, Username: "referral-list-admin", Role: "admin", Status: "enabled"}
	if _, err := SettleReferralRebateWithDB(db, admin, rebateB.ID); err != nil {
		t.Fatalf("settle rebate B: %v", err)
	}

	summary, err := GetReferralSummaryWithDB(db, inviter.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !summary.Enabled || summary.Rate != 0.5 {
		t.Fatalf("summary must expose switch/rate: %+v", summary)
	}
	if summary.InvitedCount != 2 {
		t.Fatalf("invited count = %d, want 2 (one per account, not per key)", summary.InvitedCount)
	}
	if summary.PendingAmount != 50 || summary.SettledAmount != 100 {
		t.Fatalf("amount buckets wrong: pending=%v settled=%v", summary.PendingAmount, summary.SettledAmount)
	}
	if summary.TotalBaseAmount != 300 || summary.RebateCount != 2 {
		t.Fatalf("base/count wrong: base=%v count=%d", summary.TotalBaseAmount, summary.RebateCount)
	}

	list, err := ListReferralRebatesWithDB(db, ReferralRebateListQuery{InviterUserID: inviter.ID}, 1, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Total != 2 || len(list.Data) != 2 {
		t.Fatalf("list total=%d rows=%d, want 2", list.Total, len(list.Data))
	}
	if list.Data[0].InviterUsername != inviter.Username || list.Data[0].InvitedUsername == "" {
		t.Fatalf("list must join usernames: %+v", list.Data[0])
	}
	if list.Summary.PendingAmount != 50 || list.Summary.SettledAmount != 100 {
		t.Fatalf("list summary must match list scope: %+v", list.Summary)
	}

	settledOnly, err := ListReferralRebatesWithDB(db, ReferralRebateListQuery{Status: entity.ReferralRebateStatusSettled}, 1, 10)
	if err != nil {
		t.Fatalf("list settled: %v", err)
	}
	if settledOnly.Total != 1 || settledOnly.Summary.PendingAmount != 0 || settledOnly.Summary.SettledAmount != 100 {
		t.Fatalf("filtered summary must follow filters: %+v", settledOnly.Summary)
	}
}
