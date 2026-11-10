package service

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// clawbackDBSeq 保证每个用例（含表驱动里的每一格）拿到独立的内存库。
//
// `file:xxx?mode=memory&cache=shared` 在同一个测试进程内同名库是**共享**的，
// 而表驱动用例的 t.Name() 完全相同 —— 不加序号时序第一格之后的用例会撞主键
// （实测：TestClawbackBalanceMatrix 第二格起报 UNIQUE constraint failed: users.id）。
var clawbackDBSeq atomic.Int64

// newClawbackTestDB 建一个带 users / billing_records / user_binding_records /
// idempotency_records / system_settings 的内存库。
//
// DSN 用 pkg/util.SQLiteConcurrentDSN（与生产 sqliteProductionDSN 共用同一份定义，
// 见 task_doc/47-并发测试SQLite-DSN必须与生产一致.md）：少了 _txlock=immediate +
// busy_timeout(5000)，并发用例的落败方会全被 "database table is locked" 挡下，
// 断言照样绿但测的是锁行为而不是业务语义。
func newClawbackTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := util.SQLiteConcurrentDSN(
		"file:clawback-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
			"-" + strconv.FormatInt(clawbackDBSeq.Add(1), 10) + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&entity.User{},
		&entity.BillingRecord{},
		&entity.UserBindingRecord{},
		&entity.IdempotencyRecord{},
		&entity.SystemSetting{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// 与生产同款：单连接串行化写事务。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// setClawbackSwitch 写追缴开关（默认关闭，测试里显式打开）。
func setClawbackSwitch(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()
	if err := SetRegistrationBonusClawbackEnabledWithDB(db, enabled); err != nil {
		t.Fatalf("set clawback switch: %v", err)
	}
}

// seedClawbackUser 种一个带指定两桶余额的用户。
func seedClawbackUser(t *testing.T, db *gorm.DB, id int32, name string, balance, gift float64) entity.User {
	t.Helper()
	user := entity.User{
		ID: id, Username: name, Password: "123456", Role: "user",
		Status: "enabled", RegisterSource: "admin", Balance: balance, GiftBalance: gift,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

// seedRegistrationBonus 写一条「该用户当初拿到的新用户赠送」账单。
func seedRegistrationBonus(t *testing.T, db *gorm.DB, userID int32, amount float64) {
	t.Helper()
	record := entity.BillingRecord{
		UserID: userID, Type: registrationBonusBillingRecordType, Provider: "system",
		Amount: amount, BalanceBefore: 0, BalanceAfter: 0,
		RequestID: "registration_bonus_" + itoa(userID), Remark: "新用户赠送",
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create registration bonus: %v", err)
	}
}

// seedReleasedBinding 写一条 released 绑定记录（情形 2/4 的触发源）。
func seedReleasedBinding(t *testing.T, db *gorm.DB, ownerID int32, kind, value, releaseReason string) entity.UserBindingRecord {
	t.Helper()
	hash := BindingValueHash(value)
	now := time.Now()
	record := entity.UserBindingRecord{
		UserID: ownerID, UsernameSnapshot: "owner", Kind: kind, Value: value,
		ValueHash: hash, State: entity.BindingStateReleased, ActiveKey: nil,
		Source: entity.BindingSourceUserBind, ReleasedAt: &now,
		ReleaseReason: releaseReason, ReleaseSource: entity.BindingReleaseSourceUserUnbind,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create released binding: %v", err)
	}
	return record
}

func itoa(value int32) string {
	return strconv.FormatInt(int64(value), 10)
}

// ===== ① 四情形真值表（44-*.md §2 / §5.5 权威编号） =====

// 情形 1 / 3（注册侧）：D 用全新值 BBBB+CCCC 注册，无论开 history_* 还是 current_*，
// 都**匹配不到** → 照常赠送。这不是 bug，是设计（44 §2 情形 3 的必然性）。
func TestBindingLookupFreshValuesNeverMatch(t *testing.T) {
	db := newClawbackTestDB(t)
	// A 换绑释放 BB/CC（history），注销释放 BBB/CCC（closure）。
	seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)
	seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000002", entity.BindingReleaseReasonClosure)

	lookup, err := LookupBindingHistoryWithDB(db, "13900000099", "wechat-brand-new")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if lookup.Any() {
		t.Fatalf("brand-new values must not match any released record, got %+v", lookup)
	}
}

// 情形 1 与情形 2 的**核心断言**：同一组历史记录里，rebind 与 closure 必须落进
// **两个不相交的集合**。若实现把两者混在一起（缺 release_reason 二分类的典型后果），
// 四个开关就退化成一个 —— 这条用例会同时变红。
func TestBindingLookupSeparatesRebindFromClosure(t *testing.T) {
	db := newClawbackTestDB(t)
	// BB 是换绑释放的值（history），BBB 是注销释放的值（closure）。
	seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)
	seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000002", entity.BindingReleaseReasonClosure)
	// admin 释放必须计入 history（用户 2026-11-09 定案，44 §5.1）。
	seedReleasedBinding(t, db, 1, entity.BindingKindWechat, "wx-admin-released", entity.BindingReleaseReasonAdmin)
	seedReleasedBinding(t, db, 1, entity.BindingKindWechat, "wx-closure-released", entity.BindingReleaseReasonClosure)

	// ① BB（rebind）→ 只命中 historyPhone，**不命中** currentPhone。
	historyOnly, err := LookupBindingHistoryWithDB(db, "13800000001", "")
	if err != nil {
		t.Fatalf("lookup rebind: %v", err)
	}
	if !historyOnly.HistoryPhone || historyOnly.CurrentPhone {
		t.Fatalf("rebind 值必须只落 history、不落 current，got %+v", historyOnly)
	}

	// ② BBB（closure）→ 只命中 currentPhone，**不命中** historyPhone。
	currentOnly, err := LookupBindingHistoryWithDB(db, "13800000002", "")
	if err != nil {
		t.Fatalf("lookup closure: %v", err)
	}
	if currentOnly.HistoryPhone || !currentOnly.CurrentPhone {
		t.Fatalf("closure 值必须只落 current、不落 history，got %+v", currentOnly)
	}

	// ③ admin 释放的微信 → historyWechat；closure 释放的微信 → currentWechat。
	wechatCases := []struct {
		value       string
		wantHistory bool
		wantCurrent bool
		description string
	}{
		{"wx-admin-released", true, false, "admin 释放归历史绑定（用户定案）"},
		{"wx-closure-released", false, true, "closure 释放归当前绑定"},
	}
	for _, tc := range wechatCases {
		got, lookupErr := LookupBindingHistoryWithDB(db, "", tc.value)
		if lookupErr != nil {
			t.Fatalf("lookup %s: %v", tc.description, lookupErr)
		}
		if got.HistoryWechat != tc.wantHistory || got.CurrentWechat != tc.wantCurrent {
			t.Fatalf("%s: history=%v current=%v, want %v/%v",
				tc.description, got.HistoryWechat, got.CurrentWechat, tc.wantHistory, tc.wantCurrent)
		}
	}

	// ④ 关键反证：两个集合**不相等**。若二者恒等，说明二分类失效（四开关退化成一个）。
	if historyOnly.HistoryPhone == historyOnly.CurrentPhone &&
		currentOnly.HistoryPhone == currentOnly.CurrentPhone {
		t.Fatal("history 与 current 命中了同一个集合 —— release_reason 二分类失效（四开关退化成一个）")
	}
}

// 四情形 × 四开关的真值表逐格断言（44 §2 / §5.5）。
func TestRegistrationBonusFourSwitchTruthTable(t *testing.T) {
	// lookup 形状由上游单元固定：手机号命中 history、微信命中 closure。
	lookup := BindingLookup{HistoryPhone: true, CurrentWechat: true}
	cases := []struct {
		name     string
		switches RegistrationBonusSwitches
		want     bool
	}{
		{"四个全关（默认）→ 照常赠送", RegistrationBonusSwitches{}, false},
		{"只开 history_phone 且命中 → 不赠送", RegistrationBonusSwitches{HistoryPhone: true}, true},
		{"只开 history_wechat（未命中）→ 赠送", RegistrationBonusSwitches{HistoryWechat: true}, false},
		{"只开 current_phone（未命中）→ 赠送", RegistrationBonusSwitches{CurrentPhone: true}, false},
		{"只开 current_wechat 且命中 → 不赠送", RegistrationBonusSwitches{CurrentWechat: true}, true},
		{"四个全开 → 不赠送", RegistrationBonusSwitches{
			HistoryPhone: true, HistoryWechat: true, CurrentPhone: true, CurrentWechat: true}, true},
	}
	for _, tc := range cases {
		decision := EvaluateRegistrationBonus(tc.switches, lookup)
		if decision.Suppressed != tc.want {
			t.Fatalf("%s: suppressed=%v, want %v", tc.name, decision.Suppressed, tc.want)
		}
	}

	// 命中多项时 remark 必须写**全部**命中项（42-*.md §3.2），顺序固定。
	all := EvaluateRegistrationBonus(RegistrationBonusSwitches{
		HistoryPhone: true, HistoryWechat: true, CurrentPhone: true, CurrentWechat: true,
	}, BindingLookup{HistoryPhone: true, HistoryWechat: true, CurrentPhone: true, CurrentWechat: true})
	if len(all.Reasons) != 4 {
		t.Fatalf("四项全命中必须写 4 条原因，got %d: %v", len(all.Reasons), all.Reasons)
	}
	remark := RegistrationBonusDecisionRemark(all)
	if strings.Count(remark, "；") != 3 {
		t.Fatalf("四项原因应用「；」连接，got %q", remark)
	}
	if code := RegistrationBonusSuppressionReasonCode(all); code != "history_phone,history_wechat,current_phone,current_wechat" {
		t.Fatalf("原因代码顺序必须固定，got %q", code)
	}
}

// 开关关闭时**不查表**（默认配置的零成本路径），并且缺绑定表时一律按未命中。
func TestResolveRegistrationBonusDecisionShortCircuits(t *testing.T) {
	db := newClawbackTestDB(t)
	seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)

	// ① 四开关全关 → 不抑制（即使表里有命中记录）。
	decision, err := ResolveRegistrationBonusDecisionWithDB(db, "13800000001", "")
	if err != nil || decision.Suppressed {
		t.Fatalf("全关时必须照常赠送，got %+v err=%v", decision, err)
	}

	// ② 打开 history_phone → 同一输入改判为不赠送。
	if err := SetRegistrationBonusSwitchWithDB(db, HistoryPhoneNoBonusSettingKey, true); err != nil {
		t.Fatalf("set switch: %v", err)
	}
	decision, err = ResolveRegistrationBonusDecisionWithDB(db, "13800000001", "")
	if err != nil || !decision.Suppressed {
		t.Fatalf("命中 history_phone 必须不赠送，got %+v err=%v", decision, err)
	}

	// ③ 非法键名必须报错（静默忽略会让运营以为打开了拦截）。
	if err := SetRegistrationBonusSwitchWithDB(db, "history_phone_no_bonuss", true); !errors.Is(err, ErrInvalidRegistrationBonusSwitch) {
		t.Fatalf("未知键名必须报错，got %v", err)
	}
}

// ===== ② 追缴：金额 / 扣减顺序 / 不变量 =====

// 情形 2 的完整链路：D 换绑到 BB（rebind 释放过的值）→ 追缴当初拿到的新用户赠送。
func TestClawbackOnRebindTakesGiftThenPrincipal(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	// D 当初拿到 ¥2 赠送，现在 gift=2 balance=0。
	seedClawbackUser(t, db, 2, "user-d", 0, 2)
	seedRegistrationBonus(t, db, 2, 2)
	// BB 是 A 换绑释放的值。
	binding := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)

	result, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &binding)
	if err != nil {
		t.Fatalf("clawback: %v", err)
	}
	if !result.Executed {
		t.Fatal("expected the clawback to execute")
	}
	if !nearEqual(result.Amount, 2) || !nearEqual(result.FromGift, 2) || !nearEqual(result.FromBalance, 0) {
		t.Fatalf("应全额从赠金扣：amount=%v gift=%v balance=%v", result.Amount, result.FromGift, result.FromBalance)
	}
	if !nearEqual(result.GiftAfter, 0) || !nearEqual(result.BalanceAfter, 0) {
		t.Fatalf("两桶应为 0/0，got gift=%v balance=%v", result.GiftAfter, result.BalanceAfter)
	}

	// 流水必须存在且可读（44 §3.4）。
	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND type = ?", 2, RegistrationBonusClawbackBillingRecordType).
		First(&record).Error; err != nil {
		t.Fatalf("追缴流水缺失：%v", err)
	}
	if !nearEqual(record.Amount, 2) {
		t.Fatalf("流水金额应为应追缴额 2，got %v", record.Amount)
	}
	if record.RequestID != RegistrationBonusClawbackRequestID(2) {
		t.Fatalf("幂等键应为 %s，got %s", RegistrationBonusClawbackRequestID(2), record.RequestID)
	}
	for _, want := range []string{"用户换绑到", "追回新用户赠送", "其中赠金扣", "本金扣"} {
		if !strings.Contains(record.Remark, want) {
			t.Fatalf("remark 必须含 %q，got %q", want, record.Remark)
		}
	}
	if !strings.Contains(record.UnitPriceSnapshot, "bindingRecordId=") {
		t.Fatalf("快照必须留触发绑定记录 id，got %q", record.UnitPriceSnapshot)
	}
}

// 余额矩阵：两桶 5 种初值 × 追缴额 {小于/等于/大于} 两桶合计，逐格断言 §5.3 形态。
//
// 核心不变量：
//   - I1：gift_balance 永不为负；
//   - I3：balance<0 ⇒ gift_balance==0；
//   - 追缴**总能全额执行**（FromGift+FromBalance == 应追缴额），不存在「扣不到」。
func TestClawbackBalanceMatrix(t *testing.T) {
	type balanceCase struct{ balance, gift float64 }
	buckets := []balanceCase{
		{-1, 0}, {0, 0}, {0, 5}, {5, 0}, {5, 3},
	}
	amounts := []float64{1, 3, 8, 10} // 覆盖「小于/等于/大于」两桶合计的各个边界
	for _, bucket := range buckets {
		for _, amount := range amounts {
			name := "bal=" + balanceMoneyText(bucket.balance) + "/gift=" + balanceMoneyText(bucket.gift) +
				"/claw=" + balanceMoneyText(amount)
			db := newClawbackTestDB(t)
			setClawbackSwitch(t, db, true)
			seedClawbackUser(t, db, 2, "user-d", bucket.balance, bucket.gift)
			seedRegistrationBonus(t, db, 2, amount)
			binding := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001",
				entity.BindingReleaseReasonRebind)

			result, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &binding)
			if err != nil {
				t.Fatalf("%s: clawback: %v", name, err)
			}
			if !result.Executed {
				t.Fatalf("%s: expected execution (principal may go negative, clawback always applies)", name)
			}
			// 全额执行：两桶合计扣减额 == 应追缴额。
			if total := util.RoundMoney(result.FromGift + result.FromBalance); !nearEqual(total, amount) {
				t.Fatalf("%s: 追缴必须全额执行，扣减合计 %v != 应追缴 %v", name, total, amount)
			}
			// 先扣赠金：扣减额不超过原赠金。
			if result.FromGift > bucket.gift+1e-9 {
				t.Fatalf("%s: 赠金扣减 %v 超过原赠金 %v", name, result.FromGift, bucket.gift)
			}
			// I1：赠金下限 0。
			if result.GiftAfter < -1e-9 {
				t.Fatalf("%s: I1 violated，赠金变负 %v", name, result.GiftAfter)
			}
			// I3：欠款状态下不得同时留有赠金。
			if result.BalanceAfter < -1e-9 && result.GiftAfter > 1e-9 {
				t.Fatalf("%s: I3 violated，balance=%v 同时 gift=%v", name, result.BalanceAfter, result.GiftAfter)
			}
			// 落库值与返回值一致（不能只改内存）。
			var stored entity.User
			if err := db.First(&stored, 2).Error; err != nil {
				t.Fatalf("%s: reload user: %v", name, err)
			}
			if !nearEqual(stored.Balance, result.BalanceAfter) || !nearEqual(stored.GiftBalance, result.GiftAfter) {
				t.Fatalf("%s: 落库 %v/%v 与返回 %v/%v 不一致",
					name, stored.Balance, stored.GiftBalance, result.BalanceAfter, result.GiftAfter)
			}
		}
	}
}

// §5.3 的定案样例：gift=2 balance=1，追缴 10 → gift=0 balance=−7（赠金 0、余额为负）。
func TestClawbackOvershootLeavesGiftZeroAndNegativePrincipal(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	seedClawbackUser(t, db, 2, "user-d", 1, 2)
	seedRegistrationBonus(t, db, 2, 10)
	binding := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)

	result, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &binding)
	if err != nil {
		t.Fatalf("clawback: %v", err)
	}
	if !nearEqual(result.GiftAfter, 0) {
		t.Fatalf("赠金必须扣到 0，got %v", result.GiftAfter)
	}
	if !nearEqual(result.BalanceAfter, -7) {
		t.Fatalf("余额必须为 −7（欠款留存），got %v", result.BalanceAfter)
	}
	if !strings.Contains(mustClawbackRemark(t, db, 2), "本金可为负") {
		t.Fatalf("扣到本金时 remark 必须解释欠款，got %q", mustClawbackRemark(t, db, 2))
	}
}

// 情形 4：closure 释放的值**同样**触发追缴（44 §5.2「覆盖 current_* 命中的值」）。
func TestClawbackCoversClosureReleasedValues(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	seedClawbackUser(t, db, 2, "user-d", 0, 3)
	seedRegistrationBonus(t, db, 2, 3)
	binding := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000002", entity.BindingReleaseReasonClosure)

	result, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &binding)
	if err != nil || !result.Executed {
		t.Fatalf("closure 释放的值必须同样触发追缴，got %+v err=%v", result, err)
	}
	if remark := mustClawbackRemark(t, db, 2); !strings.Contains(remark, "原账号注销时释放") {
		t.Fatalf("remark 必须写明是注销释放，got %q", remark)
	}
}

// ===== ③ 幂等 =====

// 同一用户换绑两次（先 BB 再 CC）→ **只追缴一次**（44 §3.5 t2 / §5.6）。
func TestClawbackIsIdempotentPerUser(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	seedClawbackUser(t, db, 2, "user-d", 5, 5)
	seedRegistrationBonus(t, db, 2, 4)

	first := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)
	result, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &first)
	if err != nil || !result.Executed {
		t.Fatalf("第一次追缴应执行，got %+v err=%v", result, err)
	}
	afterFirst := readWallet(t, db, 2)

	// 再换绑到 CC（另一条 released 记录）→ 幂等命中，不再扣。
	second := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000003", entity.BindingReleaseReasonRebind)
	again, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &second)
	if err != nil {
		t.Fatalf("第二次追缴不应报错，got %v", err)
	}
	if again.Executed || !again.AlreadyClawedBack {
		t.Fatalf("第二次必须幂等命中，got executed=%v already=%v", again.Executed, again.AlreadyClawedBack)
	}
	if after := readWallet(t, db, 2); !nearEqual(after.Balance, afterFirst.Balance) || !nearEqual(after.GiftBalance, afterFirst.GiftBalance) {
		t.Fatalf("幂等命中不得改动钱包：%+v → %+v", afterFirst, after)
	}
	var count int64
	db.Model(&entity.BillingRecord{}).Where("user_id = ? AND type = ?", 2, RegistrationBonusClawbackBillingRecordType).Count(&count)
	if count != 1 {
		t.Fatalf("只应有一条追缴流水，got %d", count)
	}
}

// 并发：两个 goroutine 同时触发同一用户 → 只有一个真正扣款，落败原因按类型分计。
func TestClawbackConcurrentTriggerSucceedsOnce(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	seedClawbackUser(t, db, 2, "user-d", 0, 10)
	seedRegistrationBonus(t, db, 2, 5)
	binding := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)
	bindingCopy := binding

	type outcome struct {
		executed bool
		already  bool
		err      error
	}
	const workers = 2
	results := make([]outcome, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			res, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &bindingCopy)
			results[index] = outcome{executed: res.Executed, already: res.AlreadyClawedBack, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	// 落败原因按类型分计 —— 糊成一个 rejected 会把「被锁挡下」和「业务幂等命中」
	// 混成同一个数字，而这正是 task_doc/47 明确要求避免的（锁冲突会掩盖业务闸是否真的触发）。
	var executed, alreadyClawed, lockErrors, otherErrors int
	for _, res := range results {
		switch {
		case res.err != nil && isSQLiteRetryableLockError(res.err):
			lockErrors++
		case res.err != nil:
			otherErrors++
		case res.executed:
			executed++
		case res.already:
			alreadyClawed++
		}
	}
	if otherErrors != 0 {
		t.Fatalf("出现非锁类错误 %d 次：%v", otherErrors, results)
	}
	if lockErrors != 0 {
		t.Fatalf("出现 %d 次锁等待失败 —— DSN 未与生产对齐（应为 _txlock=immediate + busy_timeout）", lockErrors)
	}
	if executed != 1 {
		t.Fatalf("只能有一次真正扣款，got executed=%d already=%d", executed, alreadyClawed)
	}
	if alreadyClawed != workers-1 {
		t.Fatalf("另一个并发请求必须走幂等命中，got already=%d", alreadyClawed)
	}
	// 钱包只被扣一次。
	wallet := readWallet(t, db, 2)
	if !nearEqual(wallet.GiftBalance, 5) {
		t.Fatalf("并发只应扣一次（10−5=5），got gift=%v", wallet.GiftBalance)
	}
}

// ===== ④ 开关默认关闭 / 零副作用 =====

func TestClawbackSwitchDefaultOffHasNoSideEffects(t *testing.T) {
	db := newClawbackTestDB(t)
	// 注意：**不调用** setClawbackSwitch，用默认值。
	seedClawbackUser(t, db, 2, "user-d", 3, 7)
	seedRegistrationBonus(t, db, 2, 5)
	binding := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)
	before := readWallet(t, db, 2)

	if GetRegistrationBonusClawbackEnabledWithDB(db) {
		t.Fatal("追缴开关默认必须为 false")
	}
	result, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &binding)
	if err != nil {
		t.Fatalf("开关关闭时不应报错，got %v", err)
	}
	if result.Executed || result.AlreadyClawedBack {
		t.Fatalf("开关关闭时不得执行，got %+v", result)
	}
	if after := readWallet(t, db, 2); !nearEqual(after.Balance, before.Balance) || !nearEqual(after.GiftBalance, before.GiftBalance) {
		t.Fatalf("开关关闭时两桶不得变化：%+v → %+v", before, after)
	}
	var count int64
	db.Model(&entity.BillingRecord{}).Where("type IN ?", []string{
		RegistrationBonusClawbackBillingRecordType, registrationBonusRevokedBillingRecordType}).Count(&count)
	if count != 0 {
		t.Fatalf("开关关闭时不得写任何追缴/扣回流水，got %d", count)
	}
	// 换绑接点也必须零查询：ClawbackTriggerBindingForRebindWithDB 直接返回 nil。
	trigger, err := ClawbackTriggerBindingForRebindWithDB(db, entity.BindingKindPhone, "13800000001")
	if err != nil || trigger != nil {
		t.Fatalf("开关关闭时不应取触发记录，got %+v err=%v", trigger, err)
	}
}

// 没有赠送记录的用户换绑到旧值 → 不追缴（无可追之款，不是错误）。
func TestClawbackSkipsWhenNoRegistrationBonusReceived(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	seedClawbackUser(t, db, 2, "user-d", 5, 5)
	binding := seedReleasedBinding(t, db, 1, entity.BindingKindPhone, "13800000001", entity.BindingReleaseReasonRebind)
	before := readWallet(t, db, 2)

	result, err := ClawbackRegistrationBonusOnRebindWithDB(db, 2, &binding)
	if err != nil {
		t.Fatalf("无赠送不应报错，got %v", err)
	}
	if result.Executed {
		t.Fatalf("无赠送时不得扣款，got %+v", result)
	}
	if after := readWallet(t, db, 2); !nearEqual(after.GiftBalance, before.GiftBalance) {
		t.Fatalf("钱包不得变化：%+v → %+v", before, after)
	}
}

// 「自己释放给自己的值」不追缴（44 号未覆盖的边界，按其意图推导）。
func TestClawbackSkipsSelfReleasedBinding(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	seedClawbackUser(t, db, 2, "user-d", 0, 4)
	seedRegistrationBonus(t, db, 2, 4)
	// D 自己换绑时释放的 BBBB 又被自己换回来。
	binding := seedReleasedBinding(t, db, 2, entity.BindingKindPhone, "13800009999", entity.BindingReleaseReasonRebind)
	before := readWallet(t, db, 2)

	result, err := clawbackAfterRebindInTx(db, 2, entity.BindingKindPhone, "13800009999")
	if err != nil {
		t.Fatalf("自释放不应报错，got %v", err)
	}
	if result.Executed {
		t.Fatal("换回自己曾绑过的值不应追缴（当年正是靠它合法拿到赠送）")
	}
	_ = binding
	if after := readWallet(t, db, 2); !nearEqual(after.GiftBalance, before.GiftBalance) {
		t.Fatalf("钱包不得变化：%+v → %+v", before, after)
	}
}

// ===== ⑤ 与换绑路径的集成：情形 2 / 4 端到端 =====

// 情形 2 端到端：A 换绑释放 BB → D 换绑到 BB → 追缴（走真实 UpdateUserPhoneWithDB）。
func TestPhoneRebindTriggersClawbackEndToEnd(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	// A 已经绑过 BB 后又换到别的号：这里直接用一次真实换绑造出 rebind 记录。
	owner := seedClawbackUser(t, db, 1, "user-a", 0, 0)
	if _, err := UpdateUserPhoneWithDB(db, owner.ID, "13800000001", false); err != nil {
		t.Fatalf("owner 首次绑定：%v", err)
	}
	if _, err := UpdateUserPhoneWithDB(db, owner.ID, "13800000009", false); err != nil {
		t.Fatalf("owner 换绑：%v", err)
	}
	// BB 现在应该是 released + rebind。
	var released entity.UserBindingRecord
	if err := db.Where("kind = ? AND value = ? AND state = ?", entity.BindingKindPhone,
		"13800000001", entity.BindingStateReleased).First(&released).Error; err != nil {
		t.Fatalf("换绑必须留下 released 记录：%v", err)
	}
	if released.ReleaseReason != entity.BindingReleaseReasonRebind {
		t.Fatalf("换绑释放的二分类必须是 rebind，got %q", released.ReleaseReason)
	}
	if released.ActiveKey != nil {
		t.Fatal("released 行的 active_key 必须为 NULL（否则继续占着唯一索引）")
	}

	// D 注册拿到赠送，之后换绑到 BB → 追缴。
	seedClawbackUser(t, db, 2, "user-d", 0, 4)
	seedRegistrationBonus(t, db, 2, 4)
	if _, err := UpdateUserPhoneWithDB(db, 2, "13800000001", false); err != nil {
		t.Fatalf("D 换绑到 BB：%v", err)
	}
	wallet := readWallet(t, db, 2)
	if !nearEqual(wallet.GiftBalance, 0) {
		t.Fatalf("换绑到历史值必须追缴掉赠金，got gift=%v", wallet.GiftBalance)
	}
	if wallet.Phone == nil || *wallet.Phone != "13800000001" {
		t.Fatalf("换绑本身必须成功，got phone=%v", wallet.Phone)
	}
}

// 情形 4 端到端：注销释放的值（closure）→ D 换绑后同样追缴（44 §5.2）。
func TestPhoneRebindTriggersClawbackForClosureValue(t *testing.T) {
	db := newClawbackTestDB(t)
	setClawbackSwitch(t, db, true)
	seedClawbackUser(t, db, 1, "user-a", 0, 0)
	// 模拟注销：先绑上，再按 closure 释放（注销路径由 releaseBindingForUser 表达）。
	if _, err := UpdateUserPhoneWithDB(db, 1, "13800000002", false); err != nil {
		t.Fatalf("owner 首次绑定：%v", err)
	}
	owner := readWallet(t, db, 1)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := releaseBindingForUser(tx, 1, entity.BindingKindPhone,
			entity.BindingReleaseReasonClosure, entity.BindingReleaseSourceAccountClose, owner, 0); err != nil {
			return err
		}
		// 注销必须同时清 users.phone（18-*.md §4.2 第 ② 步）：只翻绑定记录不清列，
		// users.phone 的唯一索引会继续挡着别人绑同一个号。
		return tx.Model(&entity.User{}).Where("id = ?", 1).
			Updates(map[string]any{"phone": nil, "phone_bound_at": nil}).Error
	}); err != nil {
		t.Fatalf("注销释放：%v", err)
	}

	seedClawbackUser(t, db, 2, "user-d", 0, 3)
	seedRegistrationBonus(t, db, 2, 3)
	if _, err := UpdateUserPhoneWithDB(db, 2, "13800000002", false); err != nil {
		t.Fatalf("D 换绑到注销释放的值：%v", err)
	}
	if wallet := readWallet(t, db, 2); !nearEqual(wallet.GiftBalance, 0) {
		t.Fatalf("closure 释放的值也必须触发追缴，got gift=%v", wallet.GiftBalance)
	}
}

// ===== 辅助 =====

func readWallet(t *testing.T, db *gorm.DB, userID int32) entity.User {
	t.Helper()
	var user entity.User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatalf("reload user %d: %v", userID, err)
	}
	return user
}

func mustClawbackRemark(t *testing.T, db *gorm.DB, userID int32) string {
	t.Helper()
	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND type = ?", userID, RegistrationBonusClawbackBillingRecordType).
		First(&record).Error; err != nil {
		t.Fatalf("追缴流水缺失：%v", err)
	}
	return record.Remark
}

// isSQLiteRetryableLockError 判定错误是否属于「可重试的锁等待失败」。
//
// 与 sqlite 驱动实际报错文案对齐：database is locked / database table is locked /
// SQLITE_BUSY / SQLITE_LOCKED。这正是诊断 DSN 是否与生产对齐的判据 ——
// 有它们在说明并发用例测的是锁行为而不是业务语义。
func isSQLiteRetryableLockError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"database is locked", "database table is locked", "sqlite_busy", "sqlite_locked"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
