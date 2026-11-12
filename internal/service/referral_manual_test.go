package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 管理员手动补录返佣（B1）的行为契约。
//
// 为什么这些用例必须「真的走事务 + 真的读回钱包」：
// 补录的语义是「现在给这个人补一笔钱」，判据只能是「钱包真的多了这笔、
// 本金桶没被动过、且重复提交真的没多第二笔」。断言「函数被调用了」
// 或「返回了非 nil」都测不出这些。

// TestManualReferralRebateCreditsGiftBucket 手动补录：进赠金桶，本金桶不动。
func TestManualReferralRebateCreditsGiftBucket(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.2)
	beneficiary := seedReferralUser(t, db, 401, "manual-beneficiary", 0)

	rebate, err := CreateManualReferralRebateWithDB(db, ManualReferralRebateInput{
		BeneficiaryUserID: beneficiary.ID,
		Amount:            25.5,
		Day:               "2026-11-01",
		Remark:            "客服补单",
		OperatorUsername:  "admin",
	})
	if err != nil {
		t.Fatalf("create manual rebate: %v", err)
	}

	// 手动补录是「建单 + 立即入账」，所以必须是 settled 且带 settled_at；
	// 落 pending 会让界面上多出一条还要再点结算的记录，与补录意图相反。
	if rebate.Status != entity.ReferralRebateStatusSettled || rebate.SettledAt == nil {
		t.Fatalf("manual rebate must be settled immediately: %+v", rebate)
	}
	if rebate.Source != entity.ReferralRebateSourceManual {
		t.Fatalf("source must be manual, got %q", rebate.Source)
	}
	if rebate.InviterUserID != beneficiary.ID {
		t.Fatalf("inviter must be the beneficiary, got %d", rebate.InviterUserID)
	}
	// 手动补录没有订单，也没有受邀人：这两项必须是「无」而不是一个假 ID。
	if rebate.RechargeOrderID != nil {
		t.Fatalf("manual rebate must not reference a recharge order, got %v", *rebate.RechargeOrderID)
	}
	if rebate.InvitedUserID != 0 {
		t.Fatalf("manual rebate has no invited user, got %d", rebate.InvitedUserID)
	}
	// BaseAmount/Rate 要自洽：列表上「基数 × 比例 = 金额」不能显示 0×0。
	if rebate.BaseAmount != 25.5 || rebate.Amount != 25.5 || rebate.Rate != 1 {
		t.Fatalf("manual rebate arithmetic must be self-consistent: base=%v rate=%v amount=%v",
			rebate.BaseAmount, rebate.Rate, rebate.Amount)
	}

	var stored entity.User
	if err := db.Where("id = ?", beneficiary.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	if stored.GiftBalance != 25.5 {
		t.Fatalf("manual rebate must credit gift_balance with 25.5, got %v", stored.GiftBalance)
	}
	// 返佣直接进赠金，不进可退本金 —— 否则补一笔就能立刻退款套现。
	if stored.Balance != 0 {
		t.Fatalf("manual rebate must not touch refundable principal, got balance=%v", stored.Balance)
	}

	// 账单行是「赠送记录」聚合的唯一来源，必须存在且类型正确。
	var record entity.BillingRecord
	if err := db.Where("user_id = ? AND type = ?", beneficiary.ID, ReferralRebateBillingRecordType).
		First(&record).Error; err != nil {
		t.Fatalf("manual rebate must write a billing record: %v", err)
	}
	if record.Amount != 25.5 {
		t.Fatalf("billing amount mismatch: %v", record.Amount)
	}
}

// TestManualReferralRebateOffsetsDebtFirst 欠款状态下先抵欠款，剩余才进赠金桶。
//
// 这是 43-*.md 规则 3 的直接判据：balance 为负时，赠送先补欠款。
// 若补录错误地「直接加 gift」，欠款会永远挂着，而界面上又显示「已赠送」。
func TestManualReferralRebateOffsetsDebtFirst(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.2)
	beneficiary := seedReferralUser(t, db, 402, "manual-debt-beneficiary", 0)
	// 造欠款：本金 -3。
	if err := db.Model(&entity.User{}).Where("id = ?", beneficiary.ID).
		Update("balance", -3.0).Error; err != nil {
		t.Fatalf("seed debt: %v", err)
	}

	if _, err := CreateManualReferralRebateWithDB(db, ManualReferralRebateInput{
		BeneficiaryUserID: beneficiary.ID,
		Amount:            5,
		Day:               "2026-11-02",
	}); err != nil {
		t.Fatalf("create manual rebate: %v", err)
	}

	var stored entity.User
	if err := db.Where("id = ?", beneficiary.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	// -3 + 5 = 2：先抵掉 3 元欠款，剩下 2 元才是赠金。
	if stored.Balance != 0 {
		t.Fatalf("debt must be offset to 0, got balance=%v", stored.Balance)
	}
	if stored.GiftBalance != 2 {
		t.Fatalf("remainder must land in gift_balance (2), got %v", stored.GiftBalance)
	}
}

// TestManualReferralRebateIsIdempotent 同一 (受益人, 金额, 日期) 重复提交不重复入账。
//
// 幂等锚点必须在**账单行**上，不能只靠 referral_rebates 的唯一索引：
// manual 行的 recharge_order_id 是 NULL，而 NULL 不参与唯一性判定。
func TestManualReferralRebateIsIdempotent(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.2)
	beneficiary := seedReferralUser(t, db, 403, "manual-idem-beneficiary", 0)

	input := ManualReferralRebateInput{
		BeneficiaryUserID: beneficiary.ID,
		Amount:            12,
		Day:               "2026-11-03",
	}
	if _, err := CreateManualReferralRebateWithDB(db, input); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// 第二次必须明确报「已记过」，而不是静默返回旧记录：
	// 静默成功会让管理员以为刚记的是另一笔，接着去核对钱包。
	_, err := CreateManualReferralRebateWithDB(db, input)
	if !errors.Is(err, ErrReferralRebateDuplicate) {
		t.Fatalf("duplicate submit must be rejected with ErrReferralRebateDuplicate, got %v", err)
	}

	var stored entity.User
	if err := db.Where("id = ?", beneficiary.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	if stored.GiftBalance != 12 {
		t.Fatalf("duplicate submit must not credit twice, got gift_balance=%v", stored.GiftBalance)
	}
	var count int64
	if err := db.Model(&entity.BillingRecord{}).
		Where("user_id = ? AND type = ?", beneficiary.ID, ReferralRebateBillingRecordType).
		Count(&count).Error; err != nil {
		t.Fatalf("count billing: %v", err)
	}
	if count != 1 {
		t.Fatalf("duplicate submit must not write a second billing row, got %d", count)
	}

	// 但「同人同额、换一天」必须是**另一笔** —— 幂等键含日期正是为了这个。
	if _, err := CreateManualReferralRebateWithDB(db, ManualReferralRebateInput{
		BeneficiaryUserID: beneficiary.ID,
		Amount:            12,
		Day:               "2026-11-04",
	}); err != nil {
		t.Fatalf("same amount on another day must be allowed: %v", err)
	}
	// 「同人同日、不同金额」也必须是另一笔。
	if _, err := CreateManualReferralRebateWithDB(db, ManualReferralRebateInput{
		BeneficiaryUserID: beneficiary.ID,
		Amount:            13,
		Day:               "2026-11-03",
	}); err != nil {
		t.Fatalf("different amount on the same day must be allowed: %v", err)
	}
	if err := db.Where("id = ?", beneficiary.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	if stored.GiftBalance != 37 {
		t.Fatalf("three distinct manual rebates must all credit (12+12+13=37), got %v", stored.GiftBalance)
	}
}

// TestManualReferralRebateRejectsInvalidInput 入参校验必须明确报错，不能静默落一笔脏账。
func TestManualReferralRebateRejectsInvalidInput(t *testing.T) {
	db := newReferralTestDB(t)
	setReferralSettings(t, db, true, 0.2)
	beneficiary := seedReferralUser(t, db, 404, "manual-invalid-beneficiary", 0)

	cases := []struct {
		name  string
		input ManualReferralRebateInput
	}{
		{"金额为 0", ManualReferralRebateInput{BeneficiaryUserID: beneficiary.ID, Amount: 0}},
		{"金额为负", ManualReferralRebateInput{BeneficiaryUserID: beneficiary.ID, Amount: -5}},
		{"金额超上限", ManualReferralRebateInput{BeneficiaryUserID: beneficiary.ID, Amount: manualReferralRebateMaxAmount + 1}},
		{"日期格式错误", ManualReferralRebateInput{BeneficiaryUserID: beneficiary.ID, Amount: 1, Day: "2026/11/01"}},
		{"受益人缺失", ManualReferralRebateInput{BeneficiaryUserID: 0, Amount: 1}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := CreateManualReferralRebateWithDB(db, testCase.input)
			if !errors.Is(err, ErrReferralRebateManualInvalid) {
				t.Fatalf("must reject with ErrReferralRebateManualInvalid, got %v", err)
			}
		})
	}

	// 全部被拒之后钱包必须还是干净的（校验要先于任何写入）。
	var stored entity.User
	if err := db.Where("id = ?", beneficiary.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload beneficiary: %v", err)
	}
	if stored.GiftBalance != 0 || stored.Balance != 0 {
		t.Fatalf("rejected input must not touch the wallet: balance=%v gift=%v",
			stored.Balance, stored.GiftBalance)
	}
}

// TestManualReferralRebateRequiresEnabledAndSchema 开关关闭/结构缺失时必须明确失败。
//
// 补录要真的发钱：降级成「假装成功」会让管理员以为已入账，事后对不上账。
func TestManualReferralRebateRequiresEnabledAndSchema(t *testing.T) {
	db := newReferralTestDB(t)
	beneficiary := seedReferralUser(t, db, 405, "manual-gate-beneficiary", 0)

	// 默认关闭。
	setReferralSettings(t, db, false, 0.2)
	if _, err := CreateManualReferralRebateWithDB(db, ManualReferralRebateInput{
		BeneficiaryUserID: beneficiary.ID, Amount: 1,
	}); !errors.Is(err, ErrReferralRebateDisabled) {
		t.Fatalf("disabled switch must reject, got %v", err)
	}

	setReferralSettings(t, db, true, 0.2)
	// 结构缺失：把 referral_rebates 表删掉模拟未迁移。
	if err := db.Migrator().DropTable(&entity.ReferralRebate{}); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := CreateManualReferralRebateWithDB(db, ManualReferralRebateInput{
		BeneficiaryUserID: beneficiary.ID, Amount: 1,
	}); !errors.Is(err, ErrReferralRebateSchemaUnavailable) {
		t.Fatalf("missing schema must reject with ErrReferralRebateSchemaUnavailable, got %v", err)
	}
}
