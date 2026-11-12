package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 「赠送记录」四类来源聚合（B2 / Q75-1）的行为契约。
//
// 核心判据不是「SQL 拼对了」，而是：**四类都必须出现、金额合计必须等于四类之和、
// 且漏掉任何一类都会被抓到**。为此每个用例都显式造齐四类账单行。

// seedGiftRecordBilling 直接落一条账单行（四类来源共用）。
//
// 为什么不调各自的业务函数：那要满足充值/评价/注册的完整前置条件，
// 会让「聚合查询是否正确」这个待测点被前置条件淹没。
// 本文件测的是聚合层，输入就是账单行本身。
func seedGiftRecordBilling(t *testing.T, db *gorm.DB, userID int32, recordType string, amount float64, createdAt time.Time) {
	t.Helper()

	record := entity.BillingRecord{
		UserID:   userID,
		Type:     recordType,
		Provider: "system",
		Amount:   amount,
		// request_id 必须唯一：同类型同金额的多条种子行不能互相覆盖。
		RequestID: fmt.Sprintf("%s_%d_%s", recordType, userID, createdAt.Format("20060102150405.000000000")),
		CreatedAt: createdAt,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("seed billing %s: %v", recordType, err)
	}
}

// TestGiftRecordsAggregatesFourSources 四类赠送来源必须全部出现在列表与汇总里。
//
// ⚠️ 四类的账单类型在种子与断言里都**写死字符串字面量**，不用常 量表：
// 用同一定量做种子和断言是自指的 —— 把常量改坏（比如把充值赠金那一类写成
// "recharge_promotion_DISABLED"）时，种子和断言会同步变化，测试照样全绿。
// 实测过这个陷阱：第一版用常量，变异「改掉一类来源」后 ok 0.9s 全绿。
// 这里的字面量必须与 billing_record.go / recharge_promotion.go 里的真实值一致。
func TestGiftRecordsAggregatesFourSources(t *testing.T) {
	db := newReferralTestDB(t)
	user := seedReferralUser(t, db, 501, "gift-four-sources", 0)

	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	// 四类各一条，金额刻意都不同：任何一类被漏掉或算错，合计立刻不等。
	seedGiftRecordBilling(t, db, user.ID, "registration_bonus", 10, base)
	seedGiftRecordBilling(t, db, user.ID, "review_rebate", 20, base.Add(time.Hour))
	seedGiftRecordBilling(t, db, user.ID, "referral_rebate", 30, base.Add(2*time.Hour))
	seedGiftRecordBilling(t, db, user.ID, "recharge_promotion", 40, base.Add(3*time.Hour))

	result, err := ListGiftRecordsWithDB(db, GiftRecordListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list gift records: %v", err)
	}

	if result.Total != 4 {
		t.Fatalf("four sources must all be listed, got total=%d", result.Total)
	}
	if len(result.Data) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(result.Data))
	}
	// 合计必须等于 10+20+30+40，漏一类就会是 90/80/… 而不是 100。
	const wantTotal = 100.0
	if result.Summary.TotalAmount != wantTotal {
		t.Fatalf("summary total must be %v, got %v", wantTotal, result.Summary.TotalAmount)
	}
	// 汇总必须**四类都出现**（没记录的补 0），否则前端图例会随筛选抖动。
	if len(result.Summary.BySource) != 4 {
		t.Fatalf("summary must list all four sources, got %d", len(result.Summary.BySource))
	}
	amountBySource := make(map[string]float64, 4)
	for _, item := range result.Summary.BySource {
		amountBySource[item.Source] = item.Amount
		if item.Label == "" {
			t.Fatalf("source %q must have a chinese label", item.Source)
		}
	}
	// 键同样用字面量：漏掉的那一类不会出现在这里，取值 0 ⇒ 立刻红。
	for source, want := range map[string]float64{
		"registration_bonus": 10,
		"review_rebate":      20,
		"referral_rebate":    30,
		"recharge_promotion": 40,
	} {
		if got := amountBySource[source]; got != want {
			t.Fatalf("source %q amount must be %v, got %v", source, want, got)
		}
	}

	// 每条都要有中文来源名：界面不能显示 referral_rebate 这种机器标识。
	for _, item := range result.Data {
		if item.SourceLabel == "" || item.SourceLabel == item.Source {
			t.Fatalf("row must carry a chinese source label, got %q for %q", item.SourceLabel, item.Source)
		}
	}
}

// TestGiftRecordsExcludesNonGiftTypes 聚合只含四类赠送，负向调整（冲回/追缴）不得混入。
//
// 边界刻意写死：把冲回混进「赠送记录」会让合计看起来比用户实际拿到的手感金额更少。
// 冲回在「账单明细」页可见，不在本页。
func TestGiftRecordsExcludesNonGiftTypes(t *testing.T) {
	db := newReferralTestDB(t)
	user := seedReferralUser(t, db, 502, "gift-boundary", 0)

	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local)
	seedGiftRecordBilling(t, db, user.ID, "referral_rebate", 30, base)
	// 三类必须被排除的负向/无关行。
	seedGiftRecordBilling(t, db, user.ID, "referral_rebate_reversal", -30, base.Add(time.Hour))
	seedGiftRecordBilling(t, db, user.ID, "registration_bonus_clawback", -10, base.Add(2*time.Hour))
	seedGiftRecordBilling(t, db, user.ID, "recharge", 100, base.Add(3*time.Hour))

	result, err := ListGiftRecordsWithDB(db, GiftRecordListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list gift records: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("only the gift row must be listed, got total=%d rows=%+v", result.Total, result.Data)
	}
	if result.Summary.TotalAmount != 30 {
		t.Fatalf("summary must only count gifts (30), got %v", result.Summary.TotalAmount)
	}
}

// TestGiftRecordsPaginatesAcrossSources 并集分页必须真的跨来源切页，不能各自 limit。
//
// 判据：造 4 类共 9 条，第 1 页 5 条 + 第 2 页 4 条，两页**互不重叠、无遗漏**，
// 且第 1 页里就已经出现多个来源（证明不是「先查完 A 类再查 B 类」）。
func TestGiftRecordsPaginatesAcrossSources(t *testing.T) {
	db := newReferralTestDB(t)
	user := seedReferralUser(t, db, 503, "gift-paging", 0)

	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	sources := []string{
		GiftRecordSourceRegistrationBonus,
		GiftRecordSourceReviewRebate,
		GiftRecordSourceReferralRebate,
		GiftRecordSourceRechargePromotion,
	}
	// 交替来源造 9 条，时间递增 ⇒ 倒序时最后一条排最前。
	for index := 0; index < 9; index++ {
		seedGiftRecordBilling(t, db, user.ID, sources[index%len(sources)], float64(index+1), base.Add(time.Duration(index)*time.Hour))
	}

	first, err := ListGiftRecordsWithDB(db, GiftRecordListQuery{}, 1, 5)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	second, err := ListGiftRecordsWithDB(db, GiftRecordListQuery{}, 2, 5)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if first.Total != 9 || second.Total != 9 {
		t.Fatalf("total must be 9 on both pages, got %d / %d", first.Total, second.Total)
	}
	if len(first.Data) != 5 {
		t.Fatalf("page 1 must return 5 rows, got %d", len(first.Data))
	}
	if len(second.Data) != 4 {
		t.Fatalf("page 2 must return the remaining 4 rows, got %d", len(second.Data))
	}
	if len(first.Data) >= 1 && len(first.Data) <= 9 {
		// 第 1 页内必须已经出现多个来源：这是「UNION 后统一分页」的直接证据。
		seen := make(map[string]struct{}, 4)
		for _, item := range first.Data {
			seen[item.Source] = struct{}{}
		}
		if len(seen) < 2 {
			t.Fatalf("page 1 must span multiple sources, got only %d: %+v", len(seen), seen)
		}
	}

	// 两页的 ID 不得重叠，合起来必须恰好是 9 条。
	seenID := make(map[int32]struct{}, 9)
	combined := append(append([]GiftRecordItem{}, first.Data...), second.Data...)
	for _, item := range combined {
		if _, duplicated := seenID[item.ID]; duplicated {
			t.Fatalf("row %d appeared on both pages", item.ID)
		}
		seenID[item.ID] = struct{}{}
	}
	if len(seenID) != 9 {
		t.Fatalf("two pages must together cover all 9 rows, got %d", len(seenID))
	}

	// ⚠️ 顺序必须**倒序**（入账时间最新在前），且跨页连续。
	//
	// 这条不加就是假钉子：把 ORDER BY 从 created_at desc 改成 id asc 后，
	// 上面的「不重叠 / 共 9 条 / 页大小」全部照样成立（实测 ok 0.803s 全绿）——
	// 因为那些断言只约束集合，不约束顺序。用户看到的却是「最早的排最前」。
	for index := 1; index < len(combined); index++ {
		previous := combined[index-1]
		current := combined[index]
		if current.CreatedAt.After(previous.CreatedAt) {
			t.Fatalf("rows must be ordered newest first: row %d (%s) came after %d (%s)",
				index, current.CreatedAt.Format(time.RFC3339), index-1, previous.CreatedAt.Format(time.RFC3339))
		}
		// 同一时间戳时按 id 倒序兜底，保证分页边界稳定（否则翻页可能重复/漏行）。
		if current.CreatedAt.Equal(previous.CreatedAt) && current.ID > previous.ID {
			t.Fatalf("same-timestamp rows must be ordered by id desc, got %d then %d", previous.ID, current.ID)
		}
	}
	// 第 1 页第一条必须是全量里最晚的那条（= 最后造的第 9 条，金额 9）。
	if len(first.Data) > 0 && first.Data[0].Amount != 9 {
		t.Fatalf("page 1 must start with the newest row (amount 9), got %v", first.Data[0].Amount)
	}

	// 汇总与分页无关：两页的合计必须相同（否则「列表只剩 5 行、合计还是全量」的口径漂移）。
	if first.Summary.TotalAmount != second.Summary.TotalAmount {
		t.Fatalf("summary must not depend on the page: %v vs %v",
			first.Summary.TotalAmount, second.Summary.TotalAmount)
	}
}

// TestGiftRecordsFilterBySourceAndUser 来源与受益人筛选必须生效。
func TestGiftRecordsFilterBySourceAndUser(t *testing.T) {
	db := newReferralTestDB(t)
	alice := seedReferralUser(t, db, 504, "gift-filter-alice", 0)
	bob := seedReferralUser(t, db, 505, "gift-filter-bob", 0)

	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.Local)
	seedGiftRecordBilling(t, db, alice.ID, "referral_rebate", 30, base)
	seedGiftRecordBilling(t, db, alice.ID, "review_rebate", 20, base.Add(time.Hour))
	seedGiftRecordBilling(t, db, bob.ID, "referral_rebate", 7, base.Add(2*time.Hour))

	bySource, err := ListGiftRecordsWithDB(db, GiftRecordListQuery{Source: "referral_rebate"}, 1, 20)
	if err != nil {
		t.Fatalf("filter by source: %v", err)
	}
	if bySource.Total != 2 || bySource.Summary.TotalAmount != 37 {
		t.Fatalf("source filter must match 2 rows / 37, got total=%d amount=%v",
			bySource.Total, bySource.Summary.TotalAmount)
	}
	// 过滤后的汇总里，未命中的来源必须补 0，而不是消失。
	if len(bySource.Summary.BySource) != 4 {
		t.Fatalf("summary must still list four sources, got %d", len(bySource.Summary.BySource))
	}

	byUser, err := ListGiftRecordsWithDB(db, GiftRecordListQuery{UserID: alice.ID}, 1, 20)
	if err != nil {
		t.Fatalf("filter by user: %v", err)
	}
	if byUser.Total != 2 || byUser.Summary.TotalAmount != 50 {
		t.Fatalf("user filter must match 2 rows / 50, got total=%d amount=%v",
			byUser.Total, byUser.Summary.TotalAmount)
	}
	// 用户名要带出来（管理员按人核对时看不到 ID）。
	for _, item := range byUser.Data {
		if item.Username != alice.Username {
			t.Fatalf("row must carry username %q, got %q", alice.Username, item.Username)
		}
	}
}

// TestGiftRecordsRejectsUnknownSource 未知来源值必须报错，不能静默当成「全部」。
//
// 静默回退会让管理员以为「筛选生效了、这类确实没有记录」，而实际是筛错了。
func TestGiftRecordsRejectsUnknownSource(t *testing.T) {
	db := newReferralTestDB(t)
	if _, err := ListGiftRecordsWithDB(db, GiftRecordListQuery{Source: "not-a-source"}, 1, 20); !errors.Is(err, ErrGiftRecordSourceInvalid) {
		t.Fatalf("unknown source must be rejected with ErrGiftRecordSourceInvalid, got %v", err)
	}
}

// TestGiftRecordSourceConstantsMatchBillingTypes 四类来源常量的值必须等于账单里真实的 type。
//
// 这条是上面那些「用字面量种子」的用例的配套：如果某天有人把常量值改坏
// （比如写成 "recharge_promotion_DISABLED"），上面的用例会红（因为它按真实字面量
// 取数取不到），而这条会把**根因**直接指出来 —— 常量与账单类型不一致，
// 而不是留一条「金额对不上」的间接报错。
func TestGiftRecordSourceConstantsMatchBillingTypes(t *testing.T) {
	expected := map[string]string{
		"registration_bonus": GiftRecordSourceRegistrationBonus,
		"review_rebate":      GiftRecordSourceReviewRebate,
		"referral_rebate":    GiftRecordSourceReferralRebate,
		"recharge_promotion": GiftRecordSourceRechargePromotion,
	}
	for wantLiteral, actual := range expected {
		if actual != wantLiteral {
			t.Fatalf("来源常量必须等于账单 type %q，实际 %q", wantLiteral, actual)
		}
	}
	// 四类必须互不相同，否则聚合会合并成一类。
	seen := make(map[string]struct{}, 4)
	for _, value := range expected {
		if _, duplicated := seen[value]; duplicated {
			t.Fatalf("四个来源常量不得重复，发现重复值 %q", value)
		}
		seen[value] = struct{}{}
	}
}

// TestGiftRecordSourceOptionsCoverFour 来源下拉必须是四类且顺序固定。
func TestGiftRecordSourceOptionsCoverFour(t *testing.T) {
	options := GiftRecordSourceOptions()
	if len(options) != 4 {
		t.Fatalf("must expose 4 sources, got %d", len(options))
	}
	want := []string{
		GiftRecordSourceRegistrationBonus,
		GiftRecordSourceReviewRebate,
		GiftRecordSourceReferralRebate,
		GiftRecordSourceRechargePromotion,
	}
	for index, option := range options {
		if option.Source != want[index] {
			t.Fatalf("option %d must be %q, got %q", index, want[index], option.Source)
		}
		if option.Label == "" {
			t.Fatalf("option %q must carry a label", option.Source)
		}
	}
}
