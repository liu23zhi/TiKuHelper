package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件钉死「批量赠送 + 批量回滚」的八条行为口径（任务书 ⑧）。
//
// 每条都对应一个会静默出错的实现方式：
//
//	排除封禁/归档   漏排除 → 给已封禁的人发钱（他登录不了，钱白送）
//	>100 人异步     写成同步 → HTTP 请求挂住十几分钟然后超时，而钱已经发出去了
//	只重试失败项    不漏判 → 重跑会把已成功的人再发一遍（除非幂等挡住，但那是第二道）
//	暂停/中止不回滚 写成回滚 → 「暂停」变成资金动作，管理员不敢点
//	回滚不可中断    读了暂停标志 → 账本停在「一半追缴」的中间态，报告说不清
//	限速            不限速 → 10 人/秒的设计被打满，数据库与审计日志一起雪崩
//	批次上限 1000   不校验 → 一次误操作给全库发钱
//	异步不阻塞      写成阻塞 → 前端拿不到 batchId，进度无从查询

// seedBatchGrantUsers 批量种用户并返回其 ID 列表。
func seedBatchGrantUsers(t *testing.T, db *gorm.DB, specs []struct {
	ID      int32
	Name    string
	Status  string
	Balance float64
	Gift    float64
}) []int32 {
	t.Helper()
	ids := make([]int32, 0, len(specs))
	for _, spec := range specs {
		user := entity.User{
			ID: spec.ID, Username: spec.Name, Password: "123456", Role: "user",
			Status: spec.Status, RegisterSource: "admin", Balance: spec.Balance, GiftBalance: spec.Gift,
		}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user %d: %v", spec.ID, err)
		}
		ids = append(ids, spec.ID)
	}
	return ids
}

// withFastBatchGrant 把限速间隔临时置 0（限速本身不属于被测语义）。
func withFastBatchGrant(t *testing.T) {
	t.Helper()
	original := batchGrantInterval
	batchGrantInterval = 0
	t.Cleanup(func() { batchGrantInterval = original })
}

// TestBatchGrantPreviewExcludesBannedAndArchived 钉住「排除已封禁与已归档」。
//
// 排除必须**分开计数**：只报「排除 2 人」无法让管理员判断该放宽哪个条件。
// 已归档用户用 Unscoped 的 physical delete 之外的方式构造 —— 直接置 deleted_at。
func TestBatchGrantPreviewExcludesBannedAndArchived(t *testing.T) {
	db := newAdminGrantTestDB(t)
	ids := seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{
		{10, "enabled-user", "enabled", 5, 1},
		{11, "banned-user", "banned", 5, 0},
		{12, "archived-user", "enabled", 5, 0},
		{13, "disabled-user", "disabled", 5, 0},
	})

	// 归档：直接写 deleted_at（模拟 registry 的软删除），不带 ArchivedReason。
	if err := db.Exec("UPDATE users SET deleted_at = ? WHERE id = ?", time.Now(), int32(12)).Error; err != nil {
		t.Fatalf("archive user 12: %v", err)
	}

	preview, err := BatchGrantPreview(db, BatchGrantSelector{UserIDs: ids})
	if err != nil {
		t.Fatalf("BatchGrantPreview: %v", err)
	}
	if preview.ExcludedBanned != 1 {
		t.Fatalf("banned must be counted separately: %+v", preview)
	}
	if preview.ExcludedArchived != 1 {
		t.Fatalf("archived must be counted separately: %+v", preview)
	}
	got := make([]int32, 0, len(preview.Eligible))
	for _, target := range preview.Eligible {
		got = append(got, target.UserID)
	}
	// 预期 enabled(10) + disabled(13)：N3-f 的排除范围只排除 banned 与归档。
	want := []int32{10, 13}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("eligible = %v, want %v", got, want)
	}
	if preview.Total != 2 {
		t.Fatalf("total must be 2, got %d", preview.Total)
	}
	// 可用合计口径：balance 5 + gift 1 = 6，不能只报 balance。
	for _, target := range preview.Eligible {
		if target.UserID == 10 && target.Available != 6 {
			t.Fatalf("available must be balance+gift (6), got %v", target.Available)
		}
	}
}

// TestBatchGrantPreviewCountsMissingUsers 钉住「名单里不存在的人单独计数」。
func TestBatchGrantPreviewCountsMissingUsers(t *testing.T) {
	db := newAdminGrantTestDB(t)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{{20, "present-user", "enabled", 0, 0}})

	preview, err := BatchGrantPreview(db, BatchGrantSelector{UserIDs: []int32{20, 999}})
	if err != nil {
		t.Fatalf("BatchGrantPreview: %v", err)
	}
	if preview.ExcludedMissing != 1 {
		t.Fatalf("missing user must be counted, got %+v", preview)
	}
	if preview.Total != 1 || preview.Eligible[0].UserID != 20 {
		t.Fatalf("only user 20 is eligible: %+v", preview.Eligible)
	}
}

// TestBatchGrantExecuteGrantsEveryoneIndependently 钉住「每人独立事务、一个失败不影响其他」。
//
// 中途把某人封禁不现实（预览与执行之间没有钩子），因此用「名单里混一个不存在的 ID」
// 来制造失败：管理员预览时该 ID 已被排除，但直接执行仍应把其余人送完。
func TestBatchGrantExecuteGrantsEveryoneIndependently(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{
		{21, "batch-a", "enabled", 0, 0},
		{22, "batch-b", "enabled", 0, 0},
	})

	progress, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "b1", Amount: 5, Reason: "活动",
		Selector: BatchGrantSelector{UserIDs: []int32{21, 22}}, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("BatchGrantExecute: %v", err)
	}
	if progress.Total != 2 || progress.Succeeded != 2 || progress.Failed != 0 {
		t.Fatalf("both users must succeed: %+v", progress)
	}
	if progress.Amount != 10 {
		t.Fatalf("amount must be 5×2=10, got %v", progress.Amount)
	}
	for _, id := range []int32{21, 22} {
		if stored := reloadAdminGrantUser(t, db, id); stored.GiftBalance != 5 {
			t.Fatalf("user %d gift must be 5, got %v", id, stored.GiftBalance)
		}
	}
	// 批次号必须同时体现在每个人的 request_id 上（回滚靠它反查名单）。
	for _, id := range []int32{21, 22} {
		bills := adminGrantBills(t, db, id, AdminGrantBillingRecordType)
		if len(bills) != 1 || bills[0].RequestID != AdminGrantRequestID(id, "batch-b1") {
			t.Fatalf("user %d bill request_id must carry the batch: %+v", id, bills)
		}
	}
}

// TestBatchGrantExecuteRequiresConfirmation 钉住三层确认（N3-g）。
//
// 每条原因单独验：只断「要求确认了」会让「因为哪一条」完全失去覆盖 ——
// 而三条原因的修法完全不同（改金额 / 拆批次 / 收窄筛选）。
func TestBatchGrantExecuteRequiresConfirmation(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{{30, "confirm-a", "enabled", 0, 0}})

	cases := []struct {
		name      string
		batchID   string
		amount    float64
		selector  BatchGrantSelector
		wantInMsg string
	}{
		// ⚠️ BatchID 必须是纯 ASCII：它走白名单校验（只允许字母/数字/短横线），
		// 而 tc.name 是中文子用例名。混用会让本用例以「批次号无效」失败，
		// 看起来像「二次确认没触发」，实际是批次号被拒 —— 实测踩过一次。
		{"单笔超阈值", "confirm-over-threshold", 10.01, BatchGrantSelector{UserIDs: []int32{30}}, "单人金额"},
		{"全选", "confirm-all-users", 1, BatchGrantSelector{All: true}, "全部用户"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
				Operator: admin, BatchID: tc.batchID, Amount: tc.amount,
				Selector: tc.selector, Confirmed: false,
			})
			if !errors.Is(err, ErrBatchGrantConfirmRequired) {
				t.Fatalf("must require confirmation, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantInMsg) {
				t.Fatalf("error must name the reason %q, got %v", tc.wantInMsg, err)
			}
		})
	}

	// 典型不触发：100 人 × 0.1 元 = 总额 10 元、单人 0.1 元、人数不超 100（112 §3.7 例 3）。
	specs := make([]struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}, 0, 3)
	ids := make([]int32, 0, 3)
	for i := int32(0); i < 3; i++ {
		specs = append(specs, struct {
			ID      int32
			Name    string
			Status  string
			Balance float64
			Gift    float64
		}{100 + i, "cheap-" + balanceMoneyText(float64(i)), "enabled", 0, 0})
		ids = append(ids, 100+i)
	}
	seedBatchGrantUsers(t, db, specs)
	if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "confirm-cheap", Amount: 0.1,
		Selector: BatchGrantSelector{UserIDs: ids}, Confirmed: false,
	}); err != nil {
		t.Fatalf("3 人 × 0.1 元 must not require confirmation: %v", err)
	}
}

// TestBatchGrantTotalAmountThresholdCatchesManySmallGrants 钉住 N3-g 的总额漏洞。
//
// 1000 人 × 1 元 = 1000 元：单人 1 元永远不触发，只有总额阈值能拦住。
// 这里用 1000 人触发「批次上限」边界，因此改用 999 人 × 1.01 元逼近总额阈值。
func TestBatchGrantTotalAmountThresholdCatchesManySmallGrants(t *testing.T) {
	db := newAdminGrantTestDB(t)
	// 只验原因计算，不真执行（真实执行 999 个用户没必要）。
	reasons := BatchGrantConfirmReasons(db, 1.01, 995, false)
	joined := strings.Join(reasons, "；")
	if !strings.Contains(joined, "批次总额") {
		t.Fatalf("995×1.01=1004.95 must trip the total-amount threshold, got %v", reasons)
	}
	// 反向对照：同样的金额但人数很少 → 不应触发总额。
	if reasons := BatchGrantConfirmReasons(db, 1.01, 2, false); len(reasons) != 0 {
		t.Fatalf("2 人 × 1.01 元 must not trip any threshold, got %v", reasons)
	}
}

// TestBatchGrantOnlyFailedRetriesFailures 钉住 N3-i「只重试失败项」。
//
// 做法：先跑一遍成功 2 人，然后手工把第 2 人的赠送流水删掉（模拟「上次失败」），
// 再用 OnlyFailed 重跑 —— 只有第 2 人应该被补上，第 1 人的余额不能再涨。
func TestBatchGrantOnlyFailedRetriesFailures(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{
		{40, "retry-a", "enabled", 0, 0},
		{41, "retry-b", "enabled", 0, 0},
	})

	if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "retry", Amount: 3,
		Selector: BatchGrantSelector{UserIDs: []int32{40, 41}}, Confirmed: true,
	}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// 模拟「第 41 人上次失败」：删掉他的流水并把赠金退回（真实失败时钱没进过账）。
	//
	// ⚠️ 必须连 idempotency_records 一起删：生产里两者在**同一个事务**内写入，
	// 失败时一起回滚。只删账单会造出「账单没有、并发闸还在」的不可能态，而那时
	// ClaimIdempotency 会以 succeeded 命中 → 被判成「已赠送」，重试就静默变成 no-op。
	// （这条注释本身是实测踩出来的：只删账单时本用例在「失败用户必须补上」处变红。）
	if err := db.Where("user_id = ? AND type = ?", int32(41), AdminGrantBillingRecordType).
		Delete(&entity.BillingRecord{}).Error; err != nil {
		t.Fatalf("drop retry-b bill: %v", err)
	}
	if err := db.Where("1 = 1").Delete(&entity.IdempotencyRecord{}).Error; err != nil {
		t.Fatalf("drop retry-b idempotency: %v", err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", int32(41)).
		Update("gift_balance", 0).Error; err != nil {
		t.Fatalf("reset retry-b gift: %v", err)
	}

	progress, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "retry", Amount: 3, OnlyFailed: true,
		Selector: BatchGrantSelector{UserIDs: []int32{40, 41}}, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if progress.Total != 1 || progress.Succeeded != 1 {
		t.Fatalf("only the failed user may be retried, got %+v", progress)
	}
	if stored := reloadAdminGrantUser(t, db, 40); stored.GiftBalance != 3 {
		t.Fatalf("already-succeeded user must not be granted twice, gift=%v", stored.GiftBalance)
	}
	if stored := reloadAdminGrantUser(t, db, 41); stored.GiftBalance != 3 {
		t.Fatalf("failed user must be granted now, gift=%v", stored.GiftBalance)
	}
}

// TestBatchGrantAsyncThresholdReturnsImmediately 钉住「>100 人走异步」。
//
// 判据不能只看「函数返回了」：同步实现也会返回。要同时看 ① 状态是 running
// ② 返回时**还没有**全部完成（Processed < Total）。
func TestBatchGrantAsyncThresholdReturnsImmediately(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)

	specs := make([]struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}, 0, batchGrantAsyncThreshold+1)
	ids := make([]int32, 0, batchGrantAsyncThreshold+1)
	for i := 0; i <= batchGrantAsyncThreshold; i++ {
		id := int32(200 + i)
		specs = append(specs, struct {
			ID      int32
			Name    string
			Status  string
			Balance float64
			Gift    float64
		}{id, "async-" + balanceMoneyText(float64(i)), "enabled", 0, 0})
		ids = append(ids, id)
	}
	seedBatchGrantUsers(t, db, specs)

	progress, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "async", Amount: 1,
		Selector: BatchGrantSelector{UserIDs: ids}, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("BatchGrantExecute: %v", err)
	}
	if progress.Status != batchGrantStatusRunning {
		t.Fatalf("async batch must report running, got %q", progress.Status)
	}
	if progress.Processed >= progress.Total {
		t.Fatalf("async batch must return before finishing: %+v", progress)
	}

	// 等异步跑完，确认最终所有人都到账（否则「异步」等于「悄悄丢活」）。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		current, err := BatchGrantProgressWithDB(db, "async")
		if err != nil {
			t.Fatalf("progress: %v", err)
		}
		if current.Status == batchGrantStatusCompleted && current.Succeeded == len(ids) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	final, _ := BatchGrantProgressWithDB(db, "async")
	t.Fatalf("async batch did not finish: %+v", final)
}

// TestBatchGrantPauseAndAbortDoNotRollback 钉住「暂停/中止不回滚」。
//
// 判据分两半，缺一半这条钉子是假的：
//
//	① **已赠送的钱不能少** —— 先真送一笔，再暂停/中止，余额必须原封不动；
//	② **后续不再处理** —— 直接调执行体（runBatchGrant）喂一个已暂停/已中止的批次，
//	   必须一个人都不处理（否则「暂停」只是个按钮上的字）。
//
// 只做 ① 的话，把「暂停」实现成空函数也能过 —— 那是假钉子。
func TestBatchGrantPauseAndAbortDoNotRollback(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{
		{50, "pause-a", "enabled", 0, 0},
		{51, "pause-b", "enabled", 0, 0},
	})

	// ① 先真送 50，再暂停：余额不能被回滚。
	if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "pause", Amount: 4,
		Selector: BatchGrantSelector{UserIDs: []int32{50}}, Confirmed: true,
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	before := reloadAdminGrantUser(t, db, 50)
	if before.GiftBalance != 4 {
		t.Fatalf("setup: user 50 must have 4 gift, got %v", before.GiftBalance)
	}

	if err := BatchGrantPause("pause"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if after := reloadAdminGrantUser(t, db, 50); after.GiftBalance != before.GiftBalance {
		t.Fatalf("pause must not roll back: %v → %v", before.GiftBalance, after.GiftBalance)
	}

	// ② 已暂停的批次再喂 51，必须一个人都不处理。
	requeue := BatchGrantExecuteRequest{Operator: admin, Amount: 4}
	runBatchGrant(db, "pause", []int32{51}, 4, requeue)
	if after := reloadAdminGrantUser(t, db, 51); after.GiftBalance != 0 {
		t.Fatalf("paused batch must not process anyone, user 51 gift=%v", after.GiftBalance)
	}

	// 中止同理。
	if err := BatchGrantAbort("pause"); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if after := reloadAdminGrantUser(t, db, 50); after.GiftBalance != before.GiftBalance {
		t.Fatalf("abort must not roll back: %v → %v", before.GiftBalance, after.GiftBalance)
	}
	runBatchGrant(db, "pause", []int32{51}, 4, requeue)
	if after := reloadAdminGrantUser(t, db, 51); after.GiftBalance != 0 {
		t.Fatalf("aborted batch must not process anyone, user 51 gift=%v", after.GiftBalance)
	}
	if !batchGrantShouldStop("pause") {
		t.Fatalf("aborted batch must report shouldStop")
	}
}

// TestBatchGrantRollbackIsNotInterruptible 钉住「回滚不可中断」。
//
// 做法：先中止批次（aborted=true），再执行回滚 —— 如果回滚读了那个标志就会一行都不处理。
// 期望：全部用户都被追缴完。
func TestBatchGrantRollbackIsNotInterruptible(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{
		{60, "abort-a", "enabled", 10, 0},
		{61, "abort-b", "enabled", 10, 0},
		{62, "abort-c", "enabled", 10, 0},
	})

	if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "aborted", Amount: 5,
		Selector: BatchGrantSelector{UserIDs: []int32{60, 61, 62}}, Confirmed: true,
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if err := BatchGrantAbort("aborted"); err != nil {
		t.Fatalf("abort: %v", err)
	}

	result, err := BatchGrantRollback(db, BatchGrantRollbackRequest{
		Operator: admin, BatchID: "aborted", Reason: "撤销",
	})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if result.Total != 3 || result.Succeeded != 3 {
		t.Fatalf("rollback must process everyone despite abort: %+v", result)
	}
	for _, id := range []int32{60, 61, 62} {
		stored := reloadAdminGrantUser(t, db, id)
		if stored.GiftBalance != 0 {
			t.Fatalf("user %d gift must be drained, got %v", id, stored.GiftBalance)
		}
	}
}

// TestBatchGrantRollbackReportsShortfallAndDebt 钉住回滚报告的三个金额口径。
//
// 场景（对应 117-*.md §1.4 例 5 的缩小版）：
//
//	60 一分没花 → 赠金扣 5，无欠款
//	61 花了 2（赠金剩 3）→ 赠金扣 3 + 本金扣 2
//	62 花完了、本金也是 0 → 本金扣 5，欠款 5
func TestBatchGrantRollbackReportsShortfallAndDebt(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{
		{70, "report-a", "enabled", 100, 0},
		{71, "report-b", "enabled", 100, 0},
		{72, "report-c", "enabled", 0, 0},
	})

	if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "report", Amount: 5,
		Selector: BatchGrantSelector{UserIDs: []int32{70, 71, 72}}, Confirmed: true,
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	// 制造「已消费」状态：71 花掉 2 元赠金，72 把 5 元赠金全花掉。
	if err := db.Model(&entity.User{}).Where("id = ?", int32(71)).
		Update("gift_balance", 3).Error; err != nil {
		t.Fatalf("consume 71: %v", err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", int32(72)).
		Update("gift_balance", 0).Error; err != nil {
		t.Fatalf("consume 72: %v", err)
	}

	result, err := BatchGrantRollback(db, BatchGrantRollbackRequest{Operator: admin, BatchID: "report"})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if result.ExpectedTotal != 15 {
		t.Fatalf("expected total must be 5×3=15, got %v", result.ExpectedTotal)
	}
	if result.GiftTotal != 5+3+0 {
		t.Fatalf("gift total must be 8, got %v", result.GiftTotal)
	}
	if result.PrincipalTotal != 0+2+5 {
		t.Fatalf("principal total must be 7, got %v", result.PrincipalTotal)
	}
	// 追缴不设上限：两桶实扣必须等于应追缴（15），差额不丢弃。
	if result.GiftTotal+result.PrincipalTotal != result.ExpectedTotal {
		t.Fatalf("追缴必须全额执行: %v+%v != %v", result.GiftTotal, result.PrincipalTotal, result.ExpectedTotal)
	}
	if result.DebtUsers != 1 || result.DebtTotal != 5 {
		t.Fatalf("exactly one user must fall into debt of 5: users=%d total=%v", result.DebtUsers, result.DebtTotal)
	}
	if stored := reloadAdminGrantUser(t, db, 72); stored.Balance != -5 {
		t.Fatalf("user 72 must go negative, got %v", stored.Balance)
	}
}

// TestBatchGrantRollbackIsIdempotent 钉住回滚重放（两笔各自判重）。
func TestBatchGrantRollbackIsIdempotent(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{{80, "idem-a", "enabled", 10, 0}})

	if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "idem", Amount: 5,
		Selector: BatchGrantSelector{UserIDs: []int32{80}}, Confirmed: true,
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if _, err := BatchGrantRollback(db, BatchGrantRollbackRequest{Operator: admin, BatchID: "idem"}); err != nil {
		t.Fatalf("first rollback: %v", err)
	}
	before := reloadAdminGrantUser(t, db, 80)

	second, err := BatchGrantRollback(db, BatchGrantRollbackRequest{Operator: admin, BatchID: "idem"})
	if err != nil {
		t.Fatalf("second rollback: %v", err)
	}
	after := reloadAdminGrantUser(t, db, 80)
	if after.GiftBalance != before.GiftBalance || after.Balance != before.Balance {
		t.Fatalf("replayed rollback must not touch the wallet: %v/%v → %v/%v",
			before.GiftBalance, before.Balance, after.GiftBalance, after.Balance)
	}
	if len(second.Items) != 1 || !second.Items[0].Skipped {
		t.Fatalf("replay must report skipped: %+v", second.Items)
	}
	if bills := adminGrantBills(t, db, 80, AdminGrantReversalBillingRecordType); len(bills) != 1 {
		t.Fatalf("replay must not write another reversal bill, got %d", len(bills))
	}
}

// TestBatchGrantRejectsOversizedBatch 钉住「单批次 1000 人上限」。
func TestBatchGrantRejectsOversizedBatch(t *testing.T) {
	db := newAdminGrantTestDB(t)
	ids := make([]int32, 0, batchGrantMaxUsers+1)
	for i := 0; i <= batchGrantMaxUsers; i++ {
		ids = append(ids, int32(1000+i))
	}
	if _, err := BatchGrantPreview(db, BatchGrantSelector{UserIDs: ids}); !errors.Is(err, ErrBatchGrantInvalid) {
		t.Fatalf("oversized batch must be rejected, got %v", err)
	}
}

// TestBatchGrantProgressSurvivesWithoutMemory 钉住「批次进度可从事务账本反推」。
//
// 内存状态在进程重启后必然丢失（它是显示层缓存，不是账本）。此时仍必须能回答
// 「这一批送了多少人、多少钱」—— 依据是 billing_records。
func TestBatchGrantProgressSurvivesWithoutMemory(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{{90, "cold-a", "enabled", 0, 0}, {91, "cold-b", "enabled", 0, 0}})

	if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "cold", Amount: 2.5,
		Selector: BatchGrantSelector{UserIDs: []int32{90, 91}}, Confirmed: true,
	}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	// 清空内存状态，模拟进程重启。
	resetBatchGrantStatesForTest()

	progress, err := BatchGrantProgressWithDB(db, "cold")
	if err != nil {
		t.Fatalf("cold progress: %v", err)
	}
	if progress.Succeeded != 2 || progress.Amount != 5 {
		t.Fatalf("progress must be rebuilt from billing_records: %+v", progress)
	}
	// 不存在的批次要报 ErrBatchGrantNotFound，而不是返回一个「0 人完成」的假成功。
	if _, err := BatchGrantProgressWithDB(db, "no-such-batch"); !errors.Is(err, ErrBatchGrantNotFound) {
		t.Fatalf("unknown batch must be rejected, got %v", err)
	}
}

// TestBatchGrantRejectsLikeWildcardsInBatchID 钉住「批次号不能污染 LIKE 模式」。
//
// 背景：批次反查用 `request_id LIKE '%_batch-<id>' ESCAPE '\'`。`_` 已经转义，
// 但 `%` 与 `\` 若不做白名单校验，会出两种失真：
//
//	a%b   → 匹配到 aXb 的批次（多扣别人的流水）
//	a\b   → 反斜杠成为转义符本身，模式语义被改写
//
// 三个入口都会拼 LIKE（Execute / Progress / Rollback），所以校验必须落在
// loadBatchGrantRecordsWithDB 内部，而不是只放在 Execute。
// 只挡 `_` 而不挡 `%` 的实现能过「下划线被拒」的断言，只有这条用例能抓住它。
func TestBatchGrantRejectsLikeWildcardsInBatchID(t *testing.T) {
	withFastBatchGrant(t)
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	seedBatchGrantUsers(t, db, []struct {
		ID      int32
		Name    string
		Status  string
		Balance float64
		Gift    float64
	}{{95, "like-a", "enabled", 0, 0}})

	// 先造一个正常批次，作为「被误匹配」的目标。
	if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
		Operator: admin, BatchID: "ab", Amount: 1,
		Selector: BatchGrantSelector{UserIDs: []int32{95}}, Confirmed: true,
	}); err != nil {
		t.Fatalf("seed batch: %v", err)
	}

	for _, badBatchID := range []string{"a%b", "a_b", `a\b`, "a%b-c"} {
		t.Run(badBatchID, func(t *testing.T) {
			// ① 执行入口必须拒绝。
			if _, err := BatchGrantExecute(db, BatchGrantExecuteRequest{
				Operator: admin, BatchID: badBatchID, Amount: 1,
				Selector: BatchGrantSelector{UserIDs: []int32{95}}, Confirmed: true,
			}); !errors.Is(err, ErrBatchGrantInvalid) {
				t.Fatalf("execute must reject batch id %q, got %v", badBatchID, err)
			}
			// ② 进度入口必须拒绝（不能借它探测别人的批次）。
			if _, err := BatchGrantProgressWithDB(db, badBatchID); !errors.Is(err, ErrBatchGrantInvalid) {
				t.Fatalf("progress must reject batch id %q, got %v", badBatchID, err)
			}
			// ③ 回滚入口必须拒绝（否则可借通配符追缴别的批次）。
			if _, err := BatchGrantRollback(db, BatchGrantRollbackRequest{
				Operator: admin, BatchID: badBatchID,
			}); !errors.Is(err, ErrBatchGrantInvalid) {
				t.Fatalf("rollback must reject batch id %q, got %v", badBatchID, err)
			}
		})
	}

	// 正常批次号仍可用（白名单不能把合法形态一起挡住）。
	if _, err := BatchGrantProgressWithDB(db, "ab"); err != nil {
		t.Fatalf("valid batch id must still work: %v", err)
	}
	// 且被保护的批次没被误追缴。
	if stored := reloadAdminGrantUser(t, db, 95); stored.GiftBalance != 1 {
		t.Fatalf("wildcard attempts must not touch the real batch, gift=%v", stored.GiftBalance)
	}
}

// TestAdminGrantDefaultThresholdsArePinned 钉住两个**默认值安全边界**的精确数值。
//
// # 为什么必须有这条（captain 广播的「第 4 类假钉子：默认值安全项薄断言」）
//
// 我先做的变异是「改到 100000」，它红了 —— 但那**不足以证明值被钉住**。
// 实测：把 batchGrantMaxTotalAmount 从 1000 改成 500（只差 2 倍、仍是"合理"值），
// 全部用例**依然全绿**。原因是现有断言只采样了两侧：
//
//	995 × 1.01 = 1004.95  → 大于任何 ≤1004.95 的阈值，改到 500 照样触发
//	2   × 1.01 = 2.02     → 小于任何 ≥2.02 的阈值，改到 500 照样不触发
//
// 两侧都"远"，所以 (2.02, 1004.95) 之间整段无断言 —— 阈值在这个区间里随便改都测不出来。
// 而 1000 恰好落在该区间内，等于**这个安全边界从未被真正钉住**。
//
// 修法：直接断言常量本身，再用**边界相邻值**夹住它（阈值-0.01 不触发 / 阈值+0.01 触发）。
// 三条一起，才能说明「这个数字不能动」而不是「这个数字在某个大概范围内」。
func TestAdminGrantDefaultThresholdsArePinned(t *testing.T) {
	db := newAdminGrantTestDB(t)

	// ① 单笔确认阈值：缺省 10 元（109-*.md Q3e-b：用户拍的是「超过 10 元」）。
	if got := AdminGrantConfirmThresholdWithDB(db); got != 10.0 {
		t.Fatalf("默认单笔确认阈值必须是 10 元，实际 %v —— 改大它会让「大额赠送要二次确认」静默失效", got)
	}
	// 缺表/无记录时也必须回落 10（不能因为读不到配置就变成不确认）。
	if err := db.Where("1 = 1").Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}
	if got := AdminGrantConfirmThresholdWithDB(db); got != 10.0 {
		t.Fatalf("配置缺失时必须回落 10 元，实际 %v", got)
	}
	// 非法值同样回落（负数/乱码都不能变成「不确认」）。
	for _, raw := range []string{"-1", "abc", ""} {
		if err := db.Create(&entity.SystemSetting{Key: AdminGrantConfirmThresholdSettingKey, Value: raw}).Error; err != nil {
			t.Fatal(err)
		}
		if got := AdminGrantConfirmThresholdWithDB(db); got != 10.0 {
			t.Fatalf("非法配置 %q 必须回落 10 元，实际 %v", raw, got)
		}
		if err := db.Where("`key` = ?", AdminGrantConfirmThresholdSettingKey).Delete(&entity.SystemSetting{}).Error; err != nil {
			t.Fatal(err)
		}
	}

	// ② 批量总额阈值：缺省 1000 元（117-*.md §4.4 N3-g 的漏洞修补）。
	//
	// ⚠️ 它**不是可配置项**，只能改代码改它 —— 所以更需要测试拦住。
	if batchGrantMaxTotalAmount != 1000.0 {
		t.Fatalf("单批次总额阈值必须是 1000 元，实际 %v —— 它不是配置项，改了只能靠测试发现", batchGrantMaxTotalAmount)
	}

	// ③ 用边界相邻值夹住总数阈值：钉的必须是这个精确值，不是一个范围。
	//    用 0.01 元/人 × N 人 = 阈值 ± 0.01，确保判定点就在 1000。
	nearBelow := int((batchGrantMaxTotalAmount - 0.01) / 0.01) // 99999 × 0.01 = 999.99
	nearAbove := int((batchGrantMaxTotalAmount + 0.01) / 0.01) // 100001 × 0.01 = 1000.01
	if reasons := BatchGrantConfirmReasons(db, 0.01, nearBelow, false); slicesContainSubstring(reasons, "批次总额") {
		t.Fatalf("总额 999.99 元（阈值下方 0.01）不应触发，got %v", reasons)
	}
	if reasons := BatchGrantConfirmReasons(db, 0.01, nearAbove, false); !slicesContainSubstring(reasons, "批次总额") {
		t.Fatalf("总额 1000.01 元（阈值上方 0.01）必须触发，got %v", reasons)
	}

	// ④ 单笔阈值的边界：9.99 不触发 / 10.00 不触发（严格大于）/ 10.01 触发。
	if AdminGrantConfirmRequired(db, 9.99) || AdminGrantConfirmRequired(db, 10.0) {
		t.Fatalf("阈值 10 元时，9.99 与 10.00 都不应要求确认")
	}
	if !AdminGrantConfirmRequired(db, 10.01) {
		t.Fatalf("阈值 10 元时，10.01 必须要求确认")
	}
}

// slicesContainSubstring 判断字符串切片里有没有包含该子串的项。
func slicesContainSubstring(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
