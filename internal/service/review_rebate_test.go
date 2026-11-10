package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// newReviewRebateTestDB 建一个独立的内存 SQLite（按 t.Name() 隔离）。
func newReviewRebateTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:review-rebate-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&entity.User{},
		&entity.SystemSetting{},
		&entity.BillingRecord{},
		&entity.ReviewRebateProgram{},
		&entity.ReviewRebateRule{},
		&entity.ReviewRebateClaim{},
		&entity.ReviewRebateAward{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// seedReviewRebateUser 建一个账户。
func seedReviewRebateUser(t *testing.T, db *gorm.DB, id int32, username string, role string) entity.User {
	t.Helper()

	user := entity.User{
		ID:             id,
		Username:       username,
		Password:       "123456",
		Role:           role,
		Status:         "enabled",
		RegisterSource: "public",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user
}

// seedReviewRebateProgram 建一个活动。
func seedReviewRebateProgram(t *testing.T, db *gorm.DB, enabled bool, maxClaims int, maxAmount float64, minConfidence float64) entity.ReviewRebateProgram {
	t.Helper()

	program := entity.ReviewRebateProgram{
		Name:               "评价返现",
		StoreURL:           "https://example.invalid/store",
		ExtensionID:        "extension-fixture",
		Enabled:            enabled,
		MaxClaimsPerUser:   maxClaims,
		MaxAmountPerUser:   maxAmount,
		MinAgentConfidence: minConfidence,
		PolicyVersion:      ReviewRebatePolicyVersion,
	}
	if err := db.Create(&program).Error; err != nil {
		t.Fatalf("create program: %v", err)
	}
	return program
}

// seedReviewRebateRule 建一条规则。
func seedReviewRebateRule(t *testing.T, db *gorm.DB, rule entity.ReviewRebateRule) entity.ReviewRebateRule {
	t.Helper()

	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	return rule
}

// setReviewRebateSwitch 写入总开关（与设置页写入路径一致）。
func setReviewRebateSwitch(t *testing.T, db *gorm.DB, enabled bool) {
	t.Helper()

	value := "false"
	if enabled {
		value = "true"
	}
	if err := saveSystemSettingValue(db, ReviewRebateEnabledSettingKey, value, "启用评价返现（默认关闭）"); err != nil {
		t.Fatalf("save review rebate switch: %v", err)
	}
}

// reviewRebateFixtureDigest 生成一个可复现的 64 位十六进制摘要。
func reviewRebateFixtureDigest(seed string) string {
	// 用固定前缀 + 补齐，保证长度是 64（服务层会校验长度）。
	base := "0123456789abcdef"
	filled := seed
	for len(filled) < 64 {
		filled += base
	}
	return filled[:64]
}

// Test 12：设置默认值 —— enabled=false、max_claims=3、max_amount=5.04、min_confidence=0.6。
func TestReviewRebateSettingDefaults(t *testing.T) {
	db := newReviewRebateTestDB(t)

	if got := GetReviewRebateEnabledWithDB(db); got != false {
		t.Fatalf("default enabled = %v, want false", got)
	}
	if got := GetReviewRebateMaxClaimsPerUserWithDB(db); got != 3 {
		t.Fatalf("default max claims = %d, want 3", got)
	}
	if got := GetReviewRebateMaxAmountPerUserWithDB(db); got != 5.04 {
		t.Fatalf("default max amount = %v, want 5.04", got)
	}
	if got := GetReviewRebateMinAgentConfidenceWithDB(db); got != 0.6 {
		t.Fatalf("default min confidence = %v, want 0.6", got)
	}

	// 非法值必须回落默认（enabled 必须 false）。
	for _, invalid := range []string{"not-a-bool", "", "2"} {
		if err := saveSystemSettingValue(db, ReviewRebateEnabledSettingKey, invalid, ""); err != nil {
			t.Fatalf("save invalid switch: %v", err)
		}
		if got := GetReviewRebateEnabledWithDB(db); got != false {
			t.Fatalf("invalid enabled %q must fall back to false, got %v", invalid, got)
		}
	}
	if err := saveSystemSettingValue(db, ReviewRebateMaxClaimsPerUserSettingKey, "-3", ""); err != nil {
		t.Fatalf("save invalid claims: %v", err)
	}
	if got := GetReviewRebateMaxClaimsPerUserWithDB(db); got != 3 {
		t.Fatalf("negative max claims must fall back to 3, got %d", got)
	}
	if err := saveSystemSettingValue(db, ReviewRebateMaxAmountPerUserSettingKey, "-1", ""); err != nil {
		t.Fatalf("save invalid amount: %v", err)
	}
	if got := GetReviewRebateMaxAmountPerUserWithDB(db); got != 5.04 {
		t.Fatalf("negative max amount must fall back to 5.04, got %v", got)
	}
	if err := saveSystemSettingValue(db, ReviewRebateMinAgentConfidenceSettingKey, "1.5", ""); err != nil {
		t.Fatalf("save invalid confidence: %v", err)
	}
	if got := GetReviewRebateMinAgentConfidenceWithDB(db); got != 0.6 {
		t.Fatalf("out-of-range confidence must fall back to 0.6, got %v", got)
	}

	// 合法的 0 必须被尊重（0 次上限 = 不允许提交，不是「未配置」）。
	if err := saveSystemSettingValue(db, ReviewRebateMaxClaimsPerUserSettingKey, "0", ""); err != nil {
		t.Fatalf("save zero claims: %v", err)
	}
	if got := GetReviewRebateMaxClaimsPerUserWithDB(db); got != 0 {
		t.Fatalf("explicit zero max claims must be respected, got %d", got)
	}
}

// Test 1a：开关关闭 → 提交被拒，规则表不生效。
func TestReviewRebateDisabledRejectsSubmitAndRules(t *testing.T) {
	db := newReviewRebateTestDB(t)
	user := seedReviewRebateUser(t, db, 11, "rr-disabled-user", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 1, MinContentScore: 1, MaxContentScore: 1,
		Amount: 0.001, Priority: 10, Enabled: true,
	})
	// 总开关保持默认 false（不写任何设置项）。

	_, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("aa"),
	})
	if !errors.Is(err, ErrReviewRebateDisabled) {
		t.Fatalf("disabled switch must reject submit, got %v", err)
	}
	if _, err := GetReviewRebateProgramWithDB(db); !errors.Is(err, ErrReviewRebateDisabled) {
		t.Fatalf("disabled switch must reject program read, got %v", err)
	}
	var count int64
	if err := db.Model(&entity.ReviewRebateClaim{}).Count(&count).Error; err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if count != 0 {
		t.Fatalf("disabled switch must not create claims, got %d", count)
	}

	// 活动行被禁用（总开关打开）→ 同样拒绝。
	setReviewRebateSwitch(t, db, true)
	if _, err := UpdateReviewRebateProgramWithDB(db, program.ID, ReviewRebateProgramUpdate{
		Enabled: boolPointer(false),
	}); err != nil {
		t.Fatalf("disable program: %v", err)
	}
	_, err = CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("ab"),
	})
	if !errors.Is(err, ErrReviewRebateDisabled) {
		t.Fatalf("disabled program must reject submit, got %v", err)
	}
}

func boolPointer(value bool) *bool { return &value }

func intPointer(value int) *int { return &value }

func floatPointer(value float64) *float64 { return &value }

// Test 2：规则匹配 —— 1星+1分→0.001、5星+10分→1.00、重叠区间由 priority 决断、都不命中→nil。
func TestReviewRebateMatchRule(t *testing.T) {
	low := entity.ReviewRebateRule{
		ID: 1, MinStars: 1, MaxStars: 1, MinContentScore: 1, MaxContentScore: 1,
		Amount: 0.001, Priority: 10, Enabled: true,
	}
	high := entity.ReviewRebateRule{
		ID: 2, MinStars: 5, MaxStars: 5, MinContentScore: 10, MaxContentScore: 10,
		Amount: 1, Priority: 20, Enabled: true,
	}
	// 与 high 重叠但优先级更低：5星+8~10分 → 0.8。
	overlapLow := entity.ReviewRebateRule{
		ID: 3, MinStars: 5, MaxStars: 5, MinContentScore: 8, MaxContentScore: 10,
		Amount: 0.8, Priority: 15, Enabled: true,
	}
	// 与 high 重叠且优先级更高：同样的 5星+10分 但金额 2.00。
	overlapHigh := entity.ReviewRebateRule{
		ID: 4, MinStars: 5, MaxStars: 5, MinContentScore: 10, MaxContentScore: 10,
		Amount: 2, Priority: 30, Enabled: true,
	}
	disabled := entity.ReviewRebateRule{
		ID: 5, MinStars: 3, MaxStars: 3, MinContentScore: 3, MaxContentScore: 3,
		Amount: 9, Priority: 999, Enabled: false,
	}
	rules := []entity.ReviewRebateRule{low, high, overlapLow, overlapHigh, disabled}

	// 必测 1：1星+1分 → 0.001
	matched := MatchReviewRebateRule(rules, 1, 1)
	if matched == nil || matched.Amount != 0.001 {
		t.Fatalf("1 star + 1 score must hit the 0.001 rule, got %+v", matched)
	}

	// 必测 2：5星+10分 → priority 最高的重叠规则（2.00），而不是 1.00。
	matched = MatchReviewRebateRule(rules, 5, 10)
	if matched == nil || matched.Amount != 2 {
		t.Fatalf("5 stars + 10 score must be decided by highest priority, got %+v", matched)
	}
	// 去掉最高优先级那条，就应该是 1.00（priority 20 > 15）。
	matched = MatchReviewRebateRule([]entity.ReviewRebateRule{low, high, overlapLow}, 5, 10)
	if matched == nil || matched.Amount != 1 {
		t.Fatalf("5 stars + 10 score must hit the 1.00 rule when it has the top priority, got %+v", matched)
	}
	// 5星+9分：落在 8~10 的重叠区间 → 0.8。
	matched = MatchReviewRebateRule(rules, 5, 9)
	if matched == nil || matched.Amount != 0.8 {
		t.Fatalf("5 stars + 9 score must hit the 0.8 overlap rule, got %+v", matched)
	}

	// 必测 3（重叠由 priority 决断，且停用规则不参与）：
	// 3星+3分 只有 disabled 命中 → nil。
	if matched := MatchReviewRebateRule(rules, 3, 3); matched != nil {
		t.Fatalf("disabled rule must never match, got %+v", matched)
	}

	// 必测 4：都不命中 → nil。
	if matched := MatchReviewRebateRule(rules, 2, 5); matched != nil {
		t.Fatalf("no matching rule must return nil, got %+v", matched)
	}
	if matched := MatchReviewRebateRule(nil, 5, 10); matched != nil {
		t.Fatalf("empty rule table must return nil, got %+v", matched)
	}
}

// Test 3：规则命中后由 Agent 打分写入候选金额（不发放）。
func TestReviewRebateScoreWritesCandidateWithoutAwarding(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 21, "rr-score-user", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	// 任意星数/内容分都命中 0.5 的兜底规则，这样 stub 的输出与规则无关。
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 0.5, Priority: 1, Enabled: true,
	})
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("b0"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}

	scored, err := ScoreReviewRebateClaimWithDB(db, claim.ID, nil)
	if err != nil {
		t.Fatalf("score claim: %v", err)
	}
	if scored.Status != entity.ReviewRebateStatusScored {
		t.Fatalf("status = %q, want scored: %+v", scored.Status, scored)
	}
	if scored.AwardAmount != 0.5 || scored.MatchedRuleID == 0 {
		t.Fatalf("candidate amount/rule not written: %+v", scored)
	}
	if scored.AgentStars < 1 || scored.AgentStars > 5 || scored.AgentContentScore < 1 || scored.AgentContentScore > 10 {
		t.Fatalf("agent output out of range: %+v", scored)
	}
	// 打分**不发放**：钱包不动、没有 award 行。
	var awards int64
	if err := db.Model(&entity.ReviewRebateAward{}).Count(&awards).Error; err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 0 {
		t.Fatalf("scoring must not create awards, got %d", awards)
	}
	var wallet entity.User
	if err := db.Where("id = ?", user.ID).First(&wallet).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if wallet.Balance != 0 || wallet.GiftBalance != 0 {
		t.Fatalf("scoring must not touch the wallet, got balance=%v gift=%v", wallet.Balance, wallet.GiftBalance)
	}
}

// Test 6：Agent confidence 低于门槛 → 强制 pending_review，不自动发放。
func TestReviewRebateScoreLowConfidenceForcesPendingReview(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 31, "rr-lowconf-user", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("c0"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}

	// 注入一个低置信度打分器（真实模型在「证据不足」时就是这样返回的）。
	lowConfidence := ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		return ReviewRebateScore{Stars: 5, ContentScore: 10, Confidence: 0.2, Rationale: "证据不足"}, nil
	})
	scored, err := ScoreReviewRebateClaimWithDB(db, claim.ID, lowConfidence)
	if err != nil {
		t.Fatalf("score claim: %v", err)
	}
	if scored.Status != entity.ReviewRebateStatusPendingReview {
		t.Fatalf("low confidence must force pending_review, got %q", scored.Status)
	}
	// 关键：低置信度时连候选金额都不算，避免「点一下批准就发」。
	if scored.AwardAmount != 0 || scored.MatchedRuleID != 0 {
		t.Fatalf("low confidence must not compute a candidate amount, got %+v", scored)
	}
	// 也不能自动发放。
	var awards int64
	if err := db.Model(&entity.ReviewRebateAward{}).Count(&awards).Error; err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 0 {
		t.Fatalf("low confidence must not award, got %d awards", awards)
	}
}

// ReviewRebateScorerFunc 让测试可以注入一个函数式打分器。
type ReviewRebateScorerFunc func(claim entity.ReviewRebateClaim) (ReviewRebateScore, error)

func (f ReviewRebateScorerFunc) Score(claim entity.ReviewRebateClaim) (ReviewRebateScore, error) {
	return f(claim)
}

// Test 3b：规则都不命中 → below_threshold + 金额 0。
func TestReviewRebateScoreBelowThreshold(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 41, "rr-threshold-user", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	// 只有「1星+1分」一条规则。
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 1, MinContentScore: 1, MaxContentScore: 1,
		Amount: 0.001, Priority: 10, Enabled: true,
	})
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("d0"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	// 强制打分器给 5星+10分，规则表里没有 → below_threshold。
	scorer := ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		return ReviewRebateScore{Stars: 5, ContentScore: 10, Confidence: 0.95, Rationale: "ok"}, nil
	})
	scored, err := ScoreReviewRebateClaimWithDB(db, claim.ID, scorer)
	if err != nil {
		t.Fatalf("score claim: %v", err)
	}
	if scored.Status != entity.ReviewRebateStatusBelowThreshold {
		t.Fatalf("status = %q, want below_threshold", scored.Status)
	}
	if scored.AwardAmount != 0 {
		t.Fatalf("below_threshold amount must be 0, got %v", scored.AwardAmount)
	}
}

// Test 4：超次数上限 → ErrReviewRebateLimitReached。
func TestReviewRebateClaimLimitReachedByCount(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 51, "rr-count-user", "user")
	admin := seedReviewRebateUser(t, db, 52, "rr-count-admin", "admin")
	program := seedReviewRebateProgram(t, db, true, 1, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})

	first, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("e0"),
	})
	if err != nil {
		t.Fatalf("first claim must succeed: %v", err)
	}
	if _, err := ScoreReviewRebateClaimWithDB(db, first.ID, nil); err != nil {
		t.Fatalf("score first: %v", err)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, admin, first.ID, true, "ok"); err != nil {
		t.Fatalf("approve first: %v", err)
	}

	// 上限 1 次：第二单必须被拒。
	_, err = CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("e1"),
	})
	if !errors.Is(err, ErrReviewRebateLimitReached) {
		t.Fatalf("count limit must be enforced, got %v", err)
	}
}

// Test 5：超金额上限 → ErrReviewRebateLimitReached。
func TestReviewRebateClaimLimitReachedByAmount(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 61, "rr-amount-user", "user")
	admin := seedReviewRebateUser(t, db, 62, "rr-amount-admin", "admin")
	// 次数给足，金额上限 1.00 —— 由金额而不是次数卡住。
	program := seedReviewRebateProgram(t, db, true, 10, 1, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})

	first, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("f0"),
	})
	if err != nil {
		t.Fatalf("first claim must succeed: %v", err)
	}
	if _, err := ScoreReviewRebateClaimWithDB(db, first.ID, nil); err != nil {
		t.Fatalf("score first: %v", err)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, admin, first.ID, true, "ok"); err != nil {
		t.Fatalf("approve first: %v", err)
	}

	// 已发 1.00 == 上限 1.00 → 第二单被拒。
	_, err = CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("f1"),
	})
	if !errors.Is(err, ErrReviewRebateLimitReached) {
		t.Fatalf("amount limit must be enforced, got %v", err)
	}
}

// 活动换版（新建 program 行）**不能**重置已领次数（16-*.md §1.2「换版不重置已领次数」）。
func TestReviewRebateQuotaSurvivesProgramVersionUpgrade(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 151, "rr-version-user", "user")
	admin := seedReviewRebateUser(t, db, 152, "rr-version-admin", "admin")

	// 旧版活动：上限 1 次。
	oldProgram := seedReviewRebateProgram(t, db, true, 1, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: oldProgram.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: oldProgram.ID, UserID: user.ID, ScreenshotSHA256: reviewRebateFixtureDigest("a1"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, nil); err != nil {
		t.Fatalf("score: %v", err)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, admin, claim.ID, true, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// 换版：停用旧活动、启用新活动（上限同样 1 次）。
	if _, err := UpdateReviewRebateProgramWithDB(db, oldProgram.ID, ReviewRebateProgramUpdate{
		Enabled: boolPointer(false),
	}); err != nil {
		t.Fatalf("disable old program: %v", err)
	}
	newProgram := seedReviewRebateProgram(t, db, true, 1, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: newProgram.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})

	// 新版下再交一单必须被次数上限拒绝：额度按账号跨活动累计。
	if _, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: newProgram.ID, UserID: user.ID, ScreenshotSHA256: reviewRebateFixtureDigest("a2"),
	}); !errors.Is(err, ErrReviewRebateLimitReached) {
		t.Fatalf("program upgrade must not reset the per-user quota, got %v", err)
	}

	// 视图里的剩余额度也必须按跨活动口径算（新版页面显示剩余 0）。
	view, err := GetReviewRebateProgramViewWithDB(db, user.ID)
	if err != nil {
		t.Fatalf("program view: %v", err)
	}
	if view.Program.ID != newProgram.ID {
		t.Fatalf("view must show the newest enabled program, got %d", view.Program.ID)
	}
	if view.Usage.ClaimedCount != 1 || view.Usage.RemainingClaims != 0 || view.Usage.RemainingAmount != 4.04 {
		t.Fatalf("usage must accumulate across programs: %+v", view.Usage)
	}
}

// 并发：剩最后 1 次额度时，两个并发提交只能成功一笔（16-*.md §1.4 的并发要求）。
//
// 这条用例钉住的是「锁用户行」这个实现选择：如果退回「对 claim 行 FOR UPDATE」，
// 「一条 approved 都没有」时锁的是空集合，两个事务都会通过检查 → 测试变红。
func TestReviewRebateConcurrentClaimRespectsQuota(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 161, "rr-concurrent-user", "user")
	admin := seedReviewRebateUser(t, db, 162, "rr-concurrent-admin", "admin")
	// 上限 1 次：先烧掉，让剩余额度只剩 1。
	program := seedReviewRebateProgram(t, db, true, 1, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Priority: 1, Enabled: true,
	})
	warmup, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: user.ID, ScreenshotSHA256: reviewRebateFixtureDigest("b1"),
	})
	if err != nil {
		t.Fatalf("warmup claim: %v", err)
	}
	if _, err := ScoreReviewRebateClaimWithDB(db, warmup.ID, nil); err != nil {
		t.Fatalf("warmup score: %v", err)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, admin, warmup.ID, true, "ok"); err != nil {
		t.Fatalf("warmup approve: %v", err)
	}

	// 现在额度已满，再开 8 个 goroutine 抢 —— 全部必须被拒。
	const workers = 8
	errs := make([]error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(slot int) {
			defer wait.Done()
			_, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
				ProgramID:        program.ID,
				UserID:           user.ID,
				ScreenshotSHA256: reviewRebateFixtureDigest(fmt.Sprintf("c%d", slot)),
			})
			errs[slot] = err
		}(index)
	}
	wait.Wait()

	for index, err := range errs {
		if !errors.Is(err, ErrReviewRebateLimitReached) {
			t.Fatalf("worker %d: expected limit reached, got %v", index, err)
		}
	}
	var total int64
	if err := db.Model(&entity.ReviewRebateClaim{}).Where("user_id = ?", user.ID).Count(&total).Error; err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if total != 1 {
		t.Fatalf("only the warmup claim may exist, got %d rows", total)
	}
}

// Test 5b：同账号同 canonical_claim_id 重复 → ErrReviewRebateDuplicate。
func TestReviewRebateDuplicateClaimRejected(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 71, "rr-dup-user", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	digest := reviewRebateFixtureDigest("99")
	input := ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: digest,
		ClaimedStars:     5,
		ClaimedReviewURL: "https://example.invalid/review/1",
	}
	if _, err := CreateReviewRebateClaimWithDB(db, input); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// 完全相同的内容 → duplicate。
	if _, err := CreateReviewRebateClaimWithDB(db, input); !errors.Is(err, ErrReviewRebateDuplicate) {
		t.Fatalf("identical claim must be duplicate, got %v", err)
	}
	// 链接尾斜杠/大小写差异归一化后仍然是同一条 → duplicate。
	normalized := input
	normalized.ClaimedReviewURL = "https://EXAMPLE.invalid/review/1/"
	if _, err := CreateReviewRebateClaimWithDB(db, normalized); !errors.Is(err, ErrReviewRebateDuplicate) {
		t.Fatalf("normalized claim must be duplicate, got %v", err)
	}
	// 换一张截图 → 新单（唯一键不同）。
	other := input
	other.ScreenshotSHA256 = reviewRebateFixtureDigest("98")
	if _, err := CreateReviewRebateClaimWithDB(db, other); err != nil {
		t.Fatalf("different screenshot must be a new claim, got %v", err)
	}
	var count int64
	if err := db.Model(&entity.ReviewRebateClaim{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected exactly 2 persisted claims, got %d", count)
	}
}

// Test 7：批准 → 写 award + 更新 balance 与 gift_balance + 写 billing_record。
func TestReviewRebateApproveCreditsWalletAndBilling(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 81, "rr-approve-user", "user")
	admin := seedReviewRebateUser(t, db, 82, "rr-approve-admin", "admin")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 5, MaxStars: 5, MinContentScore: 10, MaxContentScore: 10,
		Amount: 1, Priority: 20, Enabled: true,
	})
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("70"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	scorer := ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		return ReviewRebateScore{Stars: 5, ContentScore: 10, Confidence: 0.95, Rationale: "一致"}, nil
	})
	scored, err := ScoreReviewRebateClaimWithDB(db, claim.ID, scorer)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if scored.AwardAmount != 1 {
		t.Fatalf("candidate amount = %v, want 1", scored.AwardAmount)
	}

	decided, err := DecideReviewRebateClaimWithDB(db, admin, claim.ID, true, "内容一致，予以发放")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if decided.Status != entity.ReviewRebateStatusApproved || decided.ReviewedByUserID != admin.ID || decided.ReviewedAt == nil {
		t.Fatalf("approve must record reviewer/time: %+v", decided)
	}

	// 1) award 一行。
	var awards []entity.ReviewRebateAward
	if err := db.Where("claim_id = ?", claim.ID).Find(&awards).Error; err != nil {
		t.Fatalf("load awards: %v", err)
	}
	if len(awards) != 1 || awards[0].Amount != 1 || awards[0].GiftSource != entity.ReviewRebateGiftSource ||
		awards[0].PolicyVersion != program.PolicyVersion || awards[0].UserID != user.ID {
		t.Fatalf("award row wrong: %+v", awards)
	}

	// 2) 钱包：balance 与 gift_balance 同步增加。
	var wallet entity.User
	if err := db.Where("id = ?", user.ID).First(&wallet).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if wallet.GiftBalance != 1 {
		t.Fatalf("review rebate must credit the gift bucket with 1, got %v", wallet.GiftBalance)
	}
	// 双桶模型：返现进赠送余额（可消费、不可退），可退本金不动。
	if wallet.Balance != 0 {
		t.Fatalf("review rebate must not enter the refundable principal, got %v", wallet.Balance)
	}

	// 3) billing_records 一条 type=review_rebate。
	var records []entity.BillingRecord
	if err := db.Where("user_id = ? AND type = ?", user.ID, ReviewRebateBillingRecordType).Find(&records).Error; err != nil {
		t.Fatalf("load billing records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 review_rebate billing record, got %d", len(records))
	}
	// 本金桶没动（前后都是 0），赠送桶 0 → 1 写进快照。
	if records[0].Amount != 1 || records[0].BalanceBefore != 0 || records[0].BalanceAfter != 0 {
		t.Fatalf("billing record amounts wrong: %+v", records[0])
	}
	if after := ExtractBillingSnapshotField(records[0].UnitPriceSnapshot, "giftBalanceAfter"); after != "1" {
		t.Fatalf("snapshot giftBalanceAfter = %q, want 1", after)
	}
	if direction := buildBillingRecordDirection(records[0].Type); direction != "income" {
		t.Fatalf("review_rebate must be income direction, got %q", direction)
	}
	if label := buildBillingRecordTypeLabel(records[0].Type); label != "评价返现" {
		t.Fatalf("review_rebate label = %q, want 评价返现", label)
	}
}

// Test 8：重复批准 → 只有一条 award（唯一索引），钱包只加一次。
func TestReviewRebateDoubleApproveKeepsSingleAward(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 91, "rr-double-user", "user")
	admin := seedReviewRebateUser(t, db, 92, "rr-double-admin", "admin")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 0.5, Priority: 1, Enabled: true,
	})
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("60"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, nil); err != nil {
		t.Fatalf("score: %v", err)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, admin, claim.ID, true, "第一次"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	// 第二次批准是幂等返回（不是 500），但不会再发钱。
	second, err := DecideReviewRebateClaimWithDB(db, admin, claim.ID, true, "第二次")
	if err != nil {
		t.Fatalf("second approve must be idempotent, got %v", err)
	}
	if second.Status != entity.ReviewRebateStatusApproved {
		t.Fatalf("second approve status = %q, want approved", second.Status)
	}

	var awards int64
	if err := db.Model(&entity.ReviewRebateAward{}).Where("claim_id = ?", claim.ID).Count(&awards).Error; err != nil {
		t.Fatalf("count awards: %v", err)
	}
	if awards != 1 {
		t.Fatalf("duplicate approve must keep exactly 1 award, got %d", awards)
	}
	var wallet entity.User
	if err := db.Where("id = ?", user.ID).First(&wallet).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if wallet.GiftBalance != 0.5 {
		t.Fatalf("duplicate approve must credit the gift bucket only once, got %v", wallet.GiftBalance)
	}
	if wallet.Balance != 0 {
		t.Fatalf("review rebate must not enter the refundable principal, got %v", wallet.Balance)
	}
	var records int64
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ? AND type = ?", user.ID, ReviewRebateBillingRecordType).
		Count(&records).Error; err != nil {
		t.Fatalf("count billing: %v", err)
	}
	if records != 1 {
		t.Fatalf("duplicate approve must keep exactly 1 billing record, got %d", records)
	}

	// 直接撞唯一索引也必须失败（证明约束真的在库上，不只靠代码分支）。
	duplicate := entity.ReviewRebateAward{
		ClaimID: claim.ID, UserID: user.ID, Amount: 0.5, GiftSource: entity.ReviewRebateGiftSource,
	}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate claim_id must be rejected by the unique index")
	}
	// 已批准的不能再打分（否则金额会被改写）。
	if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, nil); !errors.Is(err, ErrReviewRebateClaimState) {
		t.Fatalf("scoring an approved claim must be refused, got %v", err)
	}
}

// Test 11：表未迁移时降级不 panic。
func TestReviewRebateSchemaUnavailableDegrades(t *testing.T) {
	// 只建 users/system_settings，不建评价返现四张表。
	db, err := gorm.Open(sqlite.Open("file:review-rebate-no-schema-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}

	if ReviewRebateSchemaAvailable(db) {
		t.Fatal("schema must be reported unavailable before migration")
	}
	// 开关打开也不能绕过结构检查（顺序是「先开关后结构」，但结构缺失必须降级而不是 panic）。
	setReviewRebateSwitch(t, db, true)

	if _, err := GetReviewRebateProgramWithDB(db); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("program read must degrade, got %v", err)
	}
	if _, err := GetReviewRebateProgramViewWithDB(db, 1); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("program view must degrade, got %v", err)
	}
	if _, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: 1, UserID: 1, ScreenshotSHA256: reviewRebateFixtureDigest("zz"),
	}); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("claim create must degrade, got %v", err)
	}
	if _, err := ListReviewRebateRulesWithDB(db, 1); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("rule list must degrade, got %v", err)
	}
	if _, err := ListReviewRebateClaimsWithDB(db, ReviewRebateClaimListQuery{}, 1, 20); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("claim list must degrade, got %v", err)
	}
	if _, err := ScoreReviewRebateClaimWithDB(db, 1, nil); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("score must degrade, got %v", err)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, entity.User{ID: 1}, 1, true, ""); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("decision must degrade, got %v", err)
	}
	if err := DeleteReviewRebateRuleWithDB(db, 1); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("rule delete must degrade, got %v", err)
	}
	// nil 数据库也不能 panic。
	if ReviewRebateSchemaAvailable(nil) {
		t.Fatal("nil db must report schema unavailable")
	}
	if _, err := ListReviewRebateRulesWithDB(db, 0); !errors.Is(err, ErrReviewRebateSchemaUnavailable) {
		t.Fatalf("rule list with nil programId must degrade, got %v", err)
	}
}

// 规则校验：MinStars > MaxStars、负金额必须被拒。
func TestReviewRebateRuleValidation(t *testing.T) {
	db := newReviewRebateTestDB(t)
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)

	if _, err := CreateReviewRebateRuleWithDB(db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 4, MaxStars: 2, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Enabled: true,
	}); !errors.Is(err, ErrReviewRebateInvalidInput) {
		t.Fatalf("min>max stars must be rejected, got %v", err)
	}
	if _, err := CreateReviewRebateRuleWithDB(db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 8, MaxContentScore: 3,
		Amount: 1, Enabled: true,
	}); !errors.Is(err, ErrReviewRebateInvalidInput) {
		t.Fatalf("min>max content score must be rejected, got %v", err)
	}
	if _, err := CreateReviewRebateRuleWithDB(db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: -1, Enabled: true,
	}); !errors.Is(err, ErrInvalidReviewRebateAmount) {
		t.Fatalf("negative amount must be rejected, got %v", err)
	}
	if _, err := CreateReviewRebateRuleWithDB(db, entity.ReviewRebateRule{
		ProgramID: 999, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Enabled: true,
	}); !errors.Is(err, ErrReviewRebateProgramNotFound) {
		t.Fatalf("unknown program must be rejected, got %v", err)
	}

	// 部分更新也要跑整条校验：只改 MinStars 造成下限大于上限必须被拒。
	rule := seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 5, MinContentScore: 1, MaxContentScore: 10,
		Amount: 1, Enabled: true,
	})
	if _, err := UpdateReviewRebateRuleWithDB(db, rule.ID, ReviewRebateRuleUpdate{
		MinStars: intPointer(9),
	}); !errors.Is(err, ErrReviewRebateInvalidInput) {
		t.Fatalf("cross-field violation on partial update must be rejected, got %v", err)
	}

	// CRUD 闭环：更新成功、删除成功、删除不存在 → NotFound。
	updated, err := UpdateReviewRebateRuleWithDB(db, rule.ID, ReviewRebateRuleUpdate{
		Amount:   floatPointer(0.001),
		Priority: intPointer(10),
	})
	if err != nil {
		t.Fatalf("update rule: %v", err)
	}
	if updated.Amount != 0.001 || updated.Priority != 10 {
		t.Fatalf("update not applied: %+v", updated)
	}
	if err := DeleteReviewRebateRuleWithDB(db, rule.ID); err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	if err := DeleteReviewRebateRuleWithDB(db, rule.ID); !errors.Is(err, ErrReviewRebateRuleNotFound) {
		t.Fatalf("delete missing rule = %v, want not found", err)
	}
}

// 活动配置的部分更新：nil = 保持现值。
func TestReviewRebateProgramPartialUpdate(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)

	updated, err := UpdateReviewRebateProgramWithDB(db, program.ID, ReviewRebateProgramUpdate{
		MaxClaimsPerUser: intPointer(5),
	})
	if err != nil {
		t.Fatalf("update program: %v", err)
	}
	if updated.MaxClaimsPerUser != 5 {
		t.Fatalf("max claims not updated: %+v", updated)
	}
	// 未提交的字段保持现值。
	if updated.MaxAmountPerUser != 5.04 || updated.MinAgentConfidence != 0.6 || !updated.Enabled ||
		updated.Name != program.Name || updated.ExtensionID != program.ExtensionID {
		t.Fatalf("omitted fields must keep current values: %+v", updated)
	}
	if _, err := UpdateReviewRebateProgramWithDB(db, program.ID, ReviewRebateProgramUpdate{
		MaxAmountPerUser: floatPointer(-1),
	}); !errors.Is(err, ErrInvalidReviewRebateAmount) {
		t.Fatalf("negative max amount must be rejected, got %v", err)
	}
	if _, err := UpdateReviewRebateProgramWithDB(db, program.ID, ReviewRebateProgramUpdate{
		MinAgentConfidence: floatPointer(1.5),
	}); !errors.Is(err, ErrInvalidReviewRebateConfidence) {
		t.Fatalf("out-of-range confidence must be rejected, got %v", err)
	}
	if _, err := UpdateReviewRebateProgramWithDB(db, 999, ReviewRebateProgramUpdate{}); !errors.Is(err, ErrReviewRebateProgramNotFound) {
		t.Fatalf("unknown program = %v, want not found", err)
	}
}

// 用户端视图：规则脱敏（只给启用的），并带上额度使用情况。
func TestReviewRebateProgramView(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 111, "rr-view-user", "user")
	admin := seedReviewRebateUser(t, db, 112, "rr-view-admin", "admin")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 5, MaxStars: 5, MinContentScore: 10, MaxContentScore: 10,
		Amount: 1, Priority: 20, Enabled: true, Remark: "满星好评",
	})
	seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 1, MinContentScore: 1, MaxContentScore: 1,
		Amount: 0.001, Priority: 10, Enabled: false, Remark: "已停用",
	})

	view, err := GetReviewRebateProgramViewWithDB(db, user.ID)
	if err != nil {
		t.Fatalf("program view: %v", err)
	}
	if view.AuthorshipVerified {
		t.Fatal("view must never claim authorship verification")
	}
	if strings.TrimSpace(view.Notice) == "" {
		t.Fatal("view must carry the authorship boundary notice")
	}
	if len(view.Rules) != 1 || view.Rules[0].Amount != 1 {
		t.Fatalf("view must expose only enabled rules: %+v", view.Rules)
	}
	if view.Usage.RemainingClaims != 3 || view.Usage.RemainingAmount != 5.04 {
		t.Fatalf("fresh user must have full quota: %+v", view.Usage)
	}

	// 批准一单后额度下降。
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID:        program.ID,
		UserID:           user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("aa11"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	scorer := ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		return ReviewRebateScore{Stars: 5, ContentScore: 10, Confidence: 0.99, Rationale: "一致"}, nil
	})
	if _, err := ScoreReviewRebateClaimWithDB(db, claim.ID, scorer); err != nil {
		t.Fatalf("score: %v", err)
	}
	view, err = GetReviewRebateProgramViewWithDB(db, user.ID)
	if err != nil {
		t.Fatalf("program view after submit: %v", err)
	}
	if view.Usage.PendingCount != 1 || view.Usage.ClaimedCount != 0 || view.Usage.RemainingClaims != 3 {
		t.Fatalf("pending claims must not consume quota: %+v", view.Usage)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, admin, claim.ID, true, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	view, err = GetReviewRebateProgramViewWithDB(db, user.ID)
	if err != nil {
		t.Fatalf("program view after approve: %v", err)
	}
	if view.Usage.ClaimedCount != 1 || view.Usage.ApprovedAmount != 1 {
		t.Fatalf("approved claim must consume quota: %+v", view.Usage)
	}
	if view.Usage.RemainingClaims != 2 {
		t.Fatalf("remaining claims = %d, want 2", view.Usage.RemainingClaims)
	}
	if view.Usage.RemainingAmount != 4.04 {
		t.Fatalf("remaining amount = %v, want 4.04", view.Usage.RemainingAmount)
	}
}

// 确定性占位打分器：同一摘要必须给出同一结果；摘要不可用 → confidence=0。
func TestReviewRebateDeterministicScorer(t *testing.T) {
	digest := reviewRebateFixtureDigest("5a")
	first, err := deterministicReviewRebateScore(entity.ReviewRebateClaim{ScreenshotSHA256: digest})
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	second, err := deterministicReviewRebateScore(entity.ReviewRebateClaim{ScreenshotSHA256: digest})
	if err != nil {
		t.Fatalf("score again: %v", err)
	}
	if first != second {
		t.Fatalf("deterministic scorer must be stable, got %+v vs %+v", first, second)
	}
	if first.Stars < 1 || first.Stars > 5 || first.ContentScore < 1 || first.ContentScore > 10 || first.Confidence != 1 {
		t.Fatalf("scorer output out of range: %+v", first)
	}
	if !strings.Contains(first.Rationale, "占位打分器") {
		t.Fatalf("rationale must say it is a placeholder: %q", first.Rationale)
	}

	// 摘要不可用 → 无法判定，confidence=0（走强制人工）。
	for _, broken := range []string{"", "not-hex", "abcd"} {
		got, err := deterministicReviewRebateScore(entity.ReviewRebateClaim{ScreenshotSHA256: broken})
		if err != nil {
			t.Fatalf("broken digest must not error: %v", err)
		}
		if got.Confidence != 0 || got.Stars != 0 || got.ContentScore != 0 {
			t.Fatalf("broken digest %q must force manual review, got %+v", broken, got)
		}
	}
}

// 用户自填星数 0 = 未填；非法星数必须被拒。
func TestReviewRebateClaimStarsValidation(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 121, "rr-stars-user", "user")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)

	if _, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: user.ID,
		ScreenshotSHA256: reviewRebateFixtureDigest("bb"),
		ClaimedStars:     9,
	}); !errors.Is(err, ErrReviewRebateInvalidInput) {
		t.Fatalf("out-of-range claimed stars must be rejected, got %v", err)
	}
	// 摘要长度不合法也要被拒。
	if _, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: user.ID, ScreenshotSHA256: "abc",
	}); !errors.Is(err, ErrReviewRebateInvalidInput) {
		t.Fatalf("short digest must be rejected, got %v", err)
	}
	// 0 = 未填，合法。
	if _, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: user.ID, ScreenshotSHA256: reviewRebateFixtureDigest("bc"),
	}); err != nil {
		t.Fatalf("omitted claimed stars must be accepted: %v", err)
	}
}

// 列表：用户端只看自己（filter.UserID 强制），分页与总数一致。
func TestReviewRebateClaimListScoping(t *testing.T) {
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	owner := seedReviewRebateUser(t, db, 131, "rr-list-owner", "user")
	other := seedReviewRebateUser(t, db, 132, "rr-list-other", "user")

	if _, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: owner.ID, ScreenshotSHA256: reviewRebateFixtureDigest("c1"),
	}); err != nil {
		t.Fatalf("create owner claim: %v", err)
	}
	if _, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: other.ID, ScreenshotSHA256: reviewRebateFixtureDigest("c2"),
	}); err != nil {
		t.Fatalf("create other claim: %v", err)
	}

	mine, err := ListReviewRebateClaimsWithDB(db, ReviewRebateClaimListQuery{UserID: owner.ID}, 1, 20)
	if err != nil {
		t.Fatalf("list own claims: %v", err)
	}
	if mine.Total != 1 || len(mine.Data) != 1 || mine.Data[0].UserID != owner.ID {
		t.Fatalf("user scoping broken: %+v", mine)
	}
	if mine.Data[0].Username != owner.Username {
		t.Fatalf("list must join username, got %q", mine.Data[0].Username)
	}
	all, err := ListReviewRebateClaimsWithDB(db, ReviewRebateClaimListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list all claims: %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("admin total = %d, want 2", all.Total)
	}
	// 空结果必须是 []，不是 null。
	empty, err := ListReviewRebateClaimsWithDB(db, ReviewRebateClaimListQuery{UserID: 9999}, 1, 20)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if empty.Data == nil {
		t.Fatal("empty list must be an empty slice, not nil")
	}
}

// 金额 6 位精度：0.001 这类小额必须原样保留（不能被截断成 0）。
func TestReviewRebateSmallAmountPrecision(t *testing.T) {
	if util.RoundMoney(0.001) != 0.001 {
		t.Fatalf("RoundMoney(0.001) = %v", util.RoundMoney(0.001))
	}
	db := newReviewRebateTestDB(t)
	setReviewRebateSwitch(t, db, true)
	user := seedReviewRebateUser(t, db, 141, "rr-precision-user", "user")
	admin := seedReviewRebateUser(t, db, 142, "rr-precision-admin", "admin")
	program := seedReviewRebateProgram(t, db, true, 3, 5.04, 0.6)
	rule := seedReviewRebateRule(t, db, entity.ReviewRebateRule{
		ProgramID: program.ID, MinStars: 1, MaxStars: 1, MinContentScore: 1, MaxContentScore: 1,
		Amount: 0.001, Priority: 10, Enabled: true,
	})
	if rule.Amount != 0.001 {
		t.Fatalf("rule amount lost precision: %v", rule.Amount)
	}
	claim, err := CreateReviewRebateClaimWithDB(db, ReviewRebateClaimCreateInput{
		ProgramID: program.ID, UserID: user.ID, ScreenshotSHA256: reviewRebateFixtureDigest("d1"),
	})
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	scorer := ReviewRebateScorerFunc(func(entity.ReviewRebateClaim) (ReviewRebateScore, error) {
		return ReviewRebateScore{Stars: 1, ContentScore: 1, Confidence: 0.99, Rationale: "很差的评价"}, nil
	})
	scored, err := ScoreReviewRebateClaimWithDB(db, claim.ID, scorer)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if scored.AwardAmount != 0.001 {
		t.Fatalf("candidate amount = %v, want 0.001", scored.AwardAmount)
	}
	if _, err := DecideReviewRebateClaimWithDB(db, admin, claim.ID, true, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	var wallet entity.User
	if err := db.Where("id = ?", user.ID).First(&wallet).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if wallet.GiftBalance != 0.001 {
		t.Fatalf("0.001 must survive to the gift bucket: %v", wallet.GiftBalance)
	}
	if wallet.Balance != 0 {
		t.Fatalf("review rebate must not enter the refundable principal: %v", wallet.Balance)
	}
}
