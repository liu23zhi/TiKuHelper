package controller

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func newScriptRuntimeBillingFixture(t *testing.T, mode string, balance float64) (*gorm.DB, entity.User, entity.AccessToken, entity.ScriptRunSession) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:runtime-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.UserScript{}, &entity.ScriptRunSession{},
		&entity.ScriptEscrow{}, &entity.ScriptEscrowEntry{}, &entity.ScriptSubsidizedUsage{}, &entity.BillingRecord{},
		&entity.SearchAccountConfig{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	user := entity.User{ID: 101, Username: "fixture-script-owner", Role: "user", Status: "enabled", Balance: balance}
	token := entity.AccessToken{ID: 102, UserID: user.ID, ScriptID: 103, Name: "fixture-script-key",
		TokenHash: strings.Repeat("c", 64), Status: "enabled", TotalQuotaLimit: 1}
	script := entity.UserScript{ID: token.ScriptID, UserID: user.ID, AccessTokenID: token.ID,
		BillingMode: mode, BaseDomain: "example.com", ShortCode: "fixture-runtime", Enabled: true}
	now := time.Now()
	session := entity.ScriptRunSession{ID: 104, ScriptID: script.ID, UserID: user.ID, AccessTokenID: token.ID,
		TokenHash: strings.Repeat("a", 64), IdentityHash: strings.Repeat("b", 64), LeaseSlot: &script.ID,
		SourceZoneHash: strings.Repeat("d", 64), LastHeartbeatAt: &now, ActivatedAt: now, ExpiresAt: now.Add(time.Hour)}
	for _, item := range []any{&user, &token, &script, &session} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, user, token, session
}

func TestScriptRuntimePaidSettlementScalesAllStandardLinesAndRunsOnce(t *testing.T) {
	db, user, token, session := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeEscrow, 0.000003)
	escrow := entity.ScriptEscrow{ScriptID: token.ScriptID, UserID: user.ID, AvailableMicros: 2, Revision: 1}
	if err := db.Create(&escrow).Error; err != nil {
		t.Fatal(err)
	}
	admin := entity.User{Username: "fixture-script-admin", Role: "admin", Status: "enabled"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	policy := service.ScriptEscrowPolicy{TargetMicros: 80_000_000, MinimumMicros: 60_000_000, MultiplierMicros: 1_500_000}
	if err := service.UpdateScriptEscrowPolicyWithDB(db, admin.ID, policy); err != nil {
		t.Fatal(err)
	}
	ctx, err := loadAIFallbackBillingContextWithDB(db, user.ID, token.ID, session)
	if err != nil || ctx.ScriptBilling.mode != entity.ScriptBillingModeEscrow || ctx.ScriptBilling.multiplierMicros != 1_500_000 {
		t.Fatalf("paid lease admission/snapshot: %+v %v", ctx, err)
	}
	// A policy change and lease revocation after execution cannot change the
	// selected rate or discard the already executed call.
	policy.MultiplierMicros = 2_000_000
	if err := service.UpdateScriptEscrowPolicyWithDB(db, admin.ID, policy); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&session).Update("revoked_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&token).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	ctx.RequestID = "fixture-paid-request"
	charges := []searchBillingCharge{
		{Provider: "local", Amount: 0.000001, Snapshot: "provider=local"},
		{Provider: "ai", Amount: 0.123456, Snapshot: "provider=ai", TotalTokens: 123},
	}
	for i := 0; i < 2; i++ {
		if err := persistSearchBillingRecordsWithDB(db, ctx, charges); err != nil {
			t.Fatalf("settlement attempt %d: %v", i, err)
		}
	}
	var bills []entity.BillingRecord
	if err := db.Order("id").Find(&bills).Error; err != nil || len(bills) != 2 {
		t.Fatalf("bills=%+v, %v", bills, err)
	}
	if bills[0].Amount != 0.000002 || bills[0].BalanceAfter != 0.000003 ||
		bills[1].Amount != 0.185184 || bills[1].BalanceAfter != -0.185181 || bills[1].TotalTokens != 123 {
		t.Fatalf("unexpected exact per-line amounts and wallet snapshots: %+v", bills)
	}
	for _, item := range []any{&user, &token, &escrow} {
		if err := db.Take(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	if user.Balance != -0.185181 || token.TotalAmountUsed != 0.185186 || escrow.AvailableMicros != 0 {
		t.Fatalf("wallet=%v, key=%v, escrow=%v", user.Balance, token.TotalAmountUsed, escrow.AvailableMicros)
	}
	charges[0].Amount = 0.000003
	if err := persistSearchBillingRecordsWithDB(db, ctx, charges); !errors.Is(err, errSearchBillingRetryConflict) {
		t.Fatalf("changed paid retry not rejected: %v", err)
	}
}

func TestScriptRuntimePaidSettlementRollsBackOnBillingInsertFailure(t *testing.T) {
	db, user, token, session := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeEscrow, 2)
	row := entity.ScriptEscrow{ScriptID: token.ScriptID, UserID: user.ID, AvailableMicros: 3_000_000, Revision: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	ctx, err := loadAIFallbackBillingContextWithDB(db, user.ID, token.ID, session)
	if err != nil {
		t.Fatal(err)
	}
	ctx.RequestID = "fixture-atomic-failure"
	if err := db.Model(&token).Update("total_quota_limit", 0).Error; err != nil {
		t.Fatal(err)
	}
	fixtureErr := errors.New("synthetic billing insert failure")
	if err := db.Callback().Create().Before("gorm:create").Register("fixture:fail-billing", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameBillingRecord {
			tx.AddError(fixtureErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := persistSearchBillingRecordsWithDB(db, ctx, []searchBillingCharge{{Provider: "ai", Amount: 5.000001}}); !errors.Is(err, fixtureErr) {
		t.Fatalf("billing failure not propagated: %v", err)
	}
	for _, item := range []any{&user, &token, &row} {
		if err := db.Take(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	var entries int64
	if err := db.Model(&entity.ScriptEscrowEntry{}).Count(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if user.Balance != 2 || token.TotalAmountUsed != 0 || row.AvailableMicros != 3_000_000 || entries != 0 {
		t.Fatalf("partial settlement: wallet=%v key=%v escrow=%v journal=%v", user.Balance, token.TotalAmountUsed, row.AvailableMicros, entries)
	}
}

func TestScriptRuntimeActivationKeepsInternalCostWithoutDebitingUser(t *testing.T) {
	db, user, token, session := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeActivation, -0.000001)
	if _, err := loadAIFallbackBillingContextWithDB(db, user.ID, token.ID, entity.ScriptRunSession{}); err == nil {
		t.Fatal("unverified dedicated Key cannot receive a subsidy")
	}
	legacy := session
	legacy.LeaseSlot = nil
	if _, _, err := checkResolvedScriptSpendingAdmission(db, user, token, legacy); err == nil {
		t.Fatal("legacy launch cannot receive a subsidy")
	}
	ctx, err := loadAIFallbackBillingContextWithDB(db, user.ID, token.ID, session)
	if err != nil || ctx.ScriptBilling.mode != entity.ScriptBillingModeActivation {
		t.Fatalf("verified zero mode at negative balance: %+v %v", ctx, err)
	}
	if err := db.Model(&session).Update("revoked_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	ctx.RequestID = "fixture-free-request"
	ctx.captureScriptExecutionUsage(search.AISearchUsage{
		InputTokens: 100, OutputTokens: 23, TotalTokens: 123,
		ModelCalls:   []search.AIModelCallUsage{{Model: "fixture-model"}},
		WebReadCalls: []search.AIWebReadCallUsage{{Status: "failed"}},
	})
	charges := []searchBillingCharge{
		{Provider: "ai", ModelName: "fixture-model", Amount: 0.012345, TotalTokens: 123, Snapshot: "question=never-retain-this-value"},
		{Provider: "ai_search", Amount: 0.000001},
	}
	for i := 0; i < 2; i++ {
		if err := persistSearchBillingRecordsWithDB(db, ctx, charges); err != nil {
			t.Fatalf("activation cost attempt %d: %v", i, err)
		}
	}
	var usage []entity.ScriptSubsidizedUsage
	if err := db.Find(&usage).Error; err != nil || len(usage) != 1 {
		t.Fatalf("internal usage=%+v, %v", usage, err)
	}
	if usage[0].CostMicros != 12_346 || strings.Contains(usage[0].CostLinesJSON, "never-retain-this-value") {
		t.Fatalf("internal cost missing or leaked user question: %+v", usage)
	}
	var counts scriptInternalExecutionUsage
	if err := json.Unmarshal([]byte(usage[0].UsageJSON), &counts); err != nil || counts.WebReadCalls != 1 || counts.TotalTokens != 123 {
		t.Fatalf("failed tool/model execution missing: %+v, %v", counts, err)
	}
	for _, item := range []any{&user, &token} {
		if err := db.Take(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	var bills int64
	if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
		t.Fatal(err)
	}
	if user.Balance != -0.000001 || token.TotalAmountUsed != 0 || bills != 0 {
		t.Fatalf("subsidy wrote wallet, key or statement: wallet=%v key=%v bills=%v", user.Balance, token.TotalAmountUsed, bills)
	}
	ctx.ScriptExecutionUsage.WebReadCalls++
	if err := persistSearchBillingRecordsWithDB(db, ctx, charges); !errors.Is(err, errSearchBillingRetryConflict) {
		t.Fatalf("changed activation retry not rejected: %v", err)
	}
}

func TestScriptRuntimeManagedModeDoesNotOverrideNormalTariff(t *testing.T) {
	db, _, _, _ := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeEscrow, 1)
	if err := db.Create(&entity.SearchAccountConfig{Provider: searchBillingProviderLocal, FlatPrice: 0.123456}).Error; err != nil {
		t.Fatal(err)
	}
	resolved := resolvedSearchRequestContext{
		ScriptID: 1, ScriptHadNormalAnswer: true, SearchMode: searchModeNormal,
		ScriptTariff:  &scriptAnswerTariff{enabled: true, amount: 5},
		ScriptBilling: searchScriptBillingPolicy{mode: entity.ScriptBillingModeEscrow, multiplierMicros: 1_500_000},
	}
	charges, _, err := resolveCompletedSearchBillingCharges(db, resolved, "local", model.SearchRequest{}, search.AISearchUsage{}, true, true)
	if err != nil || len(charges) != 1 || charges[0].Provider != searchBillingProviderLocal || charges[0].Amount != 0.123456 {
		t.Fatalf("managed mode replaced standard tariff: %+v %v", charges, err)
	}
}

func TestScriptRuntimePaidAdmissionUsesEscrowButDeniesNewDebt(t *testing.T) {
	db, user, token, session := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeEscrow, 0)
	row := entity.ScriptEscrow{ScriptID: token.ScriptID, UserID: user.ID, AvailableMicros: 1, Revision: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := checkResolvedScriptSpendingAdmission(db, user, token, session); err != nil {
		t.Fatalf("zero wallet with available escrow was rejected: %v", err)
	}
	user.Balance = -0.000001
	if _, _, err := checkResolvedScriptSpendingAdmission(db, user, token, session); !errors.Is(err, service.ErrInsufficientSearchBalance) {
		t.Fatalf("negative wallet started new work: %v", err)
	}
}

// 双桶模型下脚本链路的准入判据：**本金 0 + 赠金 > 0 必须放行**。
//
// 这条钉住的是一个真实的行为变化：旧判据只看 `Balance`，`balance=0 && escrow=0`
// 会被拒 —— 但新模型下用户还有赠金可花，拒绝就是「有钱不让用」。
// 本用例同时固定「合计为 0 且无托管仍拒绝」（不能因为改成合计就把零额度放过去）。
func TestScriptRuntimeAdmissionCountsGiftBucket(t *testing.T) {
	db, user, token, session := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeEscrow, 0)
	// 托管可用额为 0：这样「放行/拒绝」只由钱包合计决定，不被托管额掩盖。
	row := entity.ScriptEscrow{ScriptID: token.ScriptID, UserID: user.ID, AvailableMicros: 0, Revision: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	// 无本金、无赠金、无托管 → 拒绝（合计 0）。
	if _, _, err := checkResolvedScriptSpendingAdmission(db, user, token, session); !errors.Is(err, service.ErrInsufficientSearchBalance) {
		t.Fatalf("empty wallet with no escrow must be rejected, got %v", err)
	}

	// 只有赠金 → 必须放行（旧判据在这里会误拒）。
	user.GiftBalance = 1
	if _, _, err := checkResolvedScriptSpendingAdmission(db, user, token, session); err != nil {
		t.Fatalf("gift-only wallet must be admitted, got %v", err)
	}

	// 本金为负但合计仍为正 → 放行（规则 3 下这种状态不该出现，但判据必须与合计一致）。
	user.Balance = -0.5
	user.GiftBalance = 1
	if _, _, err := checkResolvedScriptSpendingAdmission(db, user, token, session); err != nil {
		t.Fatalf("negative principal offset by gift must be admitted, got %v", err)
	}

	// 合计为负 → 拒绝（欠款）。
	user.Balance = -2
	user.GiftBalance = 1
	if _, _, err := checkResolvedScriptSpendingAdmission(db, user, token, session); !errors.Is(err, service.ErrInsufficientSearchBalance) {
		t.Fatalf("net debt must be rejected, got %v", err)
	}
}

func TestScriptRuntimeActivationRecordsExecutedToolWithoutPricedLine(t *testing.T) {
	db, user, token, session := newScriptRuntimeBillingFixture(t, entity.ScriptBillingModeActivation, 0)
	ctx, err := loadAIFallbackBillingContextWithDB(db, user.ID, token.ID, session)
	if err != nil {
		t.Fatal(err)
	}
	ctx.RequestID = "fixture-unpriced-tool"
	ctx.captureScriptExecutionUsage(search.AISearchUsage{OCRCalls: []search.AIOCRCallUsage{{Status: "failed"}}})
	if err := persistSearchBillingRecordsWithDB(db, ctx, nil); err != nil {
		t.Fatal(err)
	}
	var usage entity.ScriptSubsidizedUsage
	if err := db.Take(&usage).Error; err != nil || usage.CostMicros != 0 ||
		usage.CostLinesJSON != "[]" || usage.UsageJSON != `{"ocrCalls":1}` {
		t.Fatalf("executed tool must be counted even without a billable line: %+v, %v", usage, err)
	}
}
