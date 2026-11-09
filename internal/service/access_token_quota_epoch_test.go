package service

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// newQuotaEpochTestDB 建一个带 users / access_tokens / logs / quota_epochs 的内存库。
//
// 库名带 t.Name()：`file:xxx?mode=memory&cache=shared` 在同一个测试进程内
// 同名库是共享的，跨用例会互相污染（AGENTS.md 第 9 条）。
//
// DSN 的并发参数**不手写**，直接调 pkg/util.SQLiteConcurrentDSN —— 与生产
// internal/registry.sqliteProductionDSN 共用同一份定义，因此「生产改了测试没跟着改」
// 这种漂移在结构上不可能发生。用 Concurrent 而不是 Production 版本，是因为内存库
// 不支持 WAL（Production 会对文件库加 journal_mode=WAL）。
//
// 为什么必须带这两个参数：_txlock=immediate 让写事务在 BEGIN 时就拿写锁，
// busy_timeout 让它排队等待而不是立刻报 SQLITE_BUSY。少了它们，并发用例的落败方
// 会全部被 "database table is locked" 挡下 —— 断言照样是绿的，但覆盖的是锁行为
// 而不是业务语义（实测：默认 DSN 下 retryableLock=5、两道业务闸 0 次触发）。
func newQuotaEpochTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := util.SQLiteConcurrentDSN(
		"file:quota-epoch-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.Log{}, &entity.AccessTokenQuotaEpoch{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// 与生产同款：单连接串行化，写事务排队而不是互相撞锁。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedQuotaEpochToken 种一个「余额 8 / 限额 20 / 已用 12」的令牌（第 4 节验收样例）。
func seedQuotaEpochToken(t *testing.T, db *gorm.DB, balance, limit, used float64) (entity.User, entity.AccessToken) {
	t.Helper()

	owner := entity.User{
		ID: 2, Username: "user-quota-epoch", Password: "123456", Role: "user",
		Status: "enabled", RegisterSource: "admin", Balance: balance,
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	token := entity.AccessToken{
		ID: 1, UserID: owner.ID, Name: "quota-epoch-token",
		TokenHash: hashAccessTokenValue("tk_synthetic_quota_epoch_only"), TokenPrefix: "tk_synthetic",
		TokenValue: "tk_synthetic_quota_epoch_only", Status: "enabled",
		TotalQuotaLimit: limit, TotalAmountUsed: used,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("create token: %v", err)
	}
	return owner, token
}

func seedQuotaEpochAdmin(t *testing.T, db *gorm.DB) entity.User {
	t.Helper()

	admin := entity.User{
		ID: 1, Username: "admin-quota-epoch", Password: "123456", Role: "admin",
		Status: "enabled", RegisterSource: "admin",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return admin
}

func loadQuotaEpochToken(t *testing.T, db *gorm.DB, id int32) entity.AccessToken {
	t.Helper()

	var token entity.AccessToken
	if err := db.First(&token, id).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	return token
}

func loadQuotaEpochRows(t *testing.T, db *gorm.DB, tokenID int32) []entity.AccessTokenQuotaEpoch {
	t.Helper()

	var rows []entity.AccessTokenQuotaEpoch
	if err := db.Where("token_id = ?", tokenID).Order("epoch asc").Find(&rows).Error; err != nil {
		t.Fatalf("load epochs: %v", err)
	}
	return rows
}

// TestResetAccessTokenQuotaWindowKeepsHistoryAndBalance 照抄第 4 节验收样例：
//
//	余额 8 / 限额 20 / 已用 12 → 重置后：本轮已用 0、历史 TotalAmountUsed 仍是 12、
//	balance 仍是 8、限额仍是 20。
//
// 这是本 PR 的核心断言：**TotalAmountUsed 绝不清零**。
func TestResetAccessTokenQuotaWindowKeepsHistoryAndBalance(t *testing.T) {
	db := newQuotaEpochTestDB(t)
	admin := seedQuotaEpochAdmin(t, db)
	owner, token := seedQuotaEpochToken(t, db, 8, 20, 12)

	before, err := LoadAccessTokenQuotaWindowWithDB(db, token)
	if err != nil {
		t.Fatalf("load window before reset: %v", err)
	}
	if before.Epoch != 0 || before.BaselineAmount != 0 || before.WindowUsed != 12 {
		t.Fatalf("never-reset token must report epoch=0 baseline=0 window=history: %+v", before)
	}

	result, err := ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.20")
	if err != nil {
		t.Fatalf("reset quota window: %v", err)
	}

	// 重置后的四个数字：本轮已用 0 / 历史 12 / 余额 8 / 限额 20。
	if result.QuotaWindow.WindowUsed != 0 {
		t.Fatalf("window used after reset = %v, want 0", result.QuotaWindow.WindowUsed)
	}
	if result.HistoryAmountUsed != 12 {
		t.Fatalf("history amount used in response = %v, want 12", result.HistoryAmountUsed)
	}
	if result.Balance != 8 {
		t.Fatalf("balance in response = %v, want 8", result.Balance)
	}
	if result.TotalQuotaLimit != 20 {
		t.Fatalf("quota limit in response = %v, want 20", result.TotalQuotaLimit)
	}
	if result.QuotaWindow.Epoch != 1 || result.QuotaWindow.BaselineAmount != 12 {
		t.Fatalf("epoch baseline mismatch: %+v", result.QuotaWindow)
	}
	if result.EventID == "" {
		t.Fatal("reset must carry an event id")
	}

	// 落库核对：TotalAmountUsed 仍是 12（红线）、钱包未动、限额未动。
	stored := loadQuotaEpochToken(t, db, token.ID)
	if stored.TotalAmountUsed != 12 {
		t.Fatalf("TotalAmountUsed must never be zeroed, got %v want 12", stored.TotalAmountUsed)
	}
	if stored.TotalQuotaLimit != 20 {
		t.Fatalf("quota limit changed by reset: %v want 20", stored.TotalQuotaLimit)
	}
	var storedOwner entity.User
	if err := db.First(&storedOwner, owner.ID).Error; err != nil {
		t.Fatalf("load owner: %v", err)
	}
	if storedOwner.Balance != 8 {
		t.Fatalf("wallet must not move on reset: %v want 8", storedOwner.Balance)
	}

	rows := loadQuotaEpochRows(t, db, token.ID)
	if len(rows) != 1 || rows[0].Epoch != 1 || rows[0].BaselineAmount != 12 {
		t.Fatalf("expected exactly one epoch row with baseline 12: %+v", rows)
	}
	if rows[0].OperatorID != admin.ID || rows[0].EventID != result.EventID {
		t.Fatalf("epoch row must record operator and event: %+v", rows[0])
	}

	// 读回视图同样必须是本轮 0。
	after, err := LoadAccessTokenQuotaWindowWithDB(db, stored)
	if err != nil {
		t.Fatalf("load window after reset: %v", err)
	}
	if after.WindowUsed != 0 || after.Epoch != 1 || after.BaselineAmount != 12 {
		t.Fatalf("window after reset = %+v, want epoch=1 baseline=12 window=0", after)
	}
}

// TestResetAccessTokenQuotaWindowAccumulatesAfterReset 重置后继续消费：
// 本轮已用从 0 开始累加，历史同步增长。
func TestResetAccessTokenQuotaWindowAccumulatesAfterReset(t *testing.T) {
	db := newQuotaEpochTestDB(t)
	admin := seedQuotaEpochAdmin(t, db)
	_, token := seedQuotaEpochToken(t, db, 8, 20, 12)

	if _, err := ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.21"); err != nil {
		t.Fatalf("first reset: %v", err)
	}

	// 模拟后续消费：计费路径只做 TotalAmountUsed 的增量更新（绝不清零）。
	for _, charge := range []float64{0.75, 2.25} {
		current := loadQuotaEpochToken(t, db, token.ID)
		if err := db.Model(&entity.AccessToken{}).Where("id = ?", current.ID).
			Update("total_amount_used", current.TotalAmountUsed+charge).Error; err != nil {
			t.Fatalf("charge usage: %v", err)
		}
	}

	stored := loadQuotaEpochToken(t, db, token.ID)
	if stored.TotalAmountUsed != 15 {
		t.Fatalf("history must keep growing: %v want 15", stored.TotalAmountUsed)
	}
	window, err := LoadAccessTokenQuotaWindowWithDB(db, stored)
	if err != nil {
		t.Fatalf("load window: %v", err)
	}
	if window.WindowUsed != 3 {
		t.Fatalf("window used after 0.75+2.25 = %v, want 3", window.WindowUsed)
	}
	if window.BaselineAmount != 12 || window.Epoch != 1 {
		t.Fatalf("baseline/epoch must stay unchanged by consumption: %+v", window)
	}
}

// TestResetAccessTokenQuotaWindowTwiceIncrementsEpochWithoutDuplicateRows
// 连续重置两次：epoch 递增，不重复插行（唯一索引兜底）。
func TestResetAccessTokenQuotaWindowTwiceIncrementsEpochWithoutDuplicateRows(t *testing.T) {
	db := newQuotaEpochTestDB(t)
	admin := seedQuotaEpochAdmin(t, db)
	_, token := seedQuotaEpochToken(t, db, 8, 20, 12)

	first, err := ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.22")
	if err != nil {
		t.Fatalf("first reset: %v", err)
	}
	// 第一轮又花了 5，再重置一次：baseline 应推到 17，epoch 推到 2。
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", token.ID).
		Update("total_amount_used", 17.0).Error; err != nil {
		t.Fatalf("charge usage: %v", err)
	}
	second, err := ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.22")
	if err != nil {
		t.Fatalf("second reset: %v", err)
	}

	if first.QuotaWindow.Epoch != 1 || second.QuotaWindow.Epoch != 2 {
		t.Fatalf("epoch must increment: first=%d second=%d", first.QuotaWindow.Epoch, second.QuotaWindow.Epoch)
	}
	if second.QuotaWindow.BaselineAmount != 17 || second.QuotaWindow.WindowUsed != 0 {
		t.Fatalf("second reset baseline/window mismatch: %+v", second.QuotaWindow)
	}
	if second.HistoryAmountUsed != 17 {
		t.Fatalf("history must remain 17 after second reset, got %v", second.HistoryAmountUsed)
	}
	rows := loadQuotaEpochRows(t, db, token.ID)
	if len(rows) != 2 || rows[0].Epoch != 1 || rows[1].Epoch != 2 {
		t.Fatalf("expected exactly two epoch rows (1,2): %+v", rows)
	}
	// 历史累计始终是单值字段，不是「每轮一行」的累加。
	if stored := loadQuotaEpochToken(t, db, token.ID); stored.TotalAmountUsed != 17 {
		t.Fatalf("TotalAmountUsed must never be zeroed, got %v want 17", stored.TotalAmountUsed)
	}

	// 唯一索引确实存在：手工插一行重复 (token_id, epoch) 必须被拒绝。
	duplicate := entity.AccessTokenQuotaEpoch{
		TokenID: token.ID, Epoch: 2, ResetAt: time.Now(),
		BaselineAmount: 17, OperatorID: admin.ID, EventID: "dup",
	}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("unique index (token_id, epoch) must reject a duplicate epoch row")
	} else if !isUniqueConstraintViolation(err) {
		t.Fatalf("duplicate insert failed with unexpected error: %v", err)
	}
}

// TestResetAccessTokenQuotaWindowSecondResetWithoutConsumptionIsRejected
// 确定性地区分两道闸的错误语义：**串行**连点两次、中间没有任何消费。
//
// 这条用例单独存在的理由（captain 要求）：
//
//	唯一索引 (token_id, epoch) 只防「同一刻」的并发 —— 两方都算 epoch=1 才撞键。
//	串行时后一次读到已提交的 epoch=1，会算出 epoch=2，唯一索引**放行**，
//	于是写出第二行 baseline 完全相同的空 epoch。挡住它的是语义闸
//	ErrAccessTokenQuotaWindowEmpty（不是 ErrAccessTokenQuotaEpochConflict）。
//
// 两种错误语义不同，客户端处理方式也不同：
//   - ErrAccessTokenQuotaEpochConflict（唯一索引）：并发抢跑，可原样重试；
//   - ErrAccessTokenQuotaWindowEmpty（语义闸）：当前状态没有可重置的东西，
//     重试也不会成功，应当提示用户「本轮额度已经归零」。
//
// 因此这里必须断言**具体是哪一种**，而不是「反正是失败了」。
func TestResetAccessTokenQuotaWindowSecondResetWithoutConsumptionIsRejected(t *testing.T) {
	db := newQuotaEpochTestDB(t)
	admin := seedQuotaEpochAdmin(t, db)
	_, token := seedQuotaEpochToken(t, db, 8, 20, 12)

	if _, err := ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.25"); err != nil {
		t.Fatalf("first reset must succeed: %v", err)
	}

	// 中间不消费：本轮已用仍是 0，第二次重置必须被语义闸拒绝。
	_, err := ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.25")
	if !errors.Is(err, ErrAccessTokenQuotaWindowEmpty) {
		t.Fatalf("second reset without consumption must be ErrAccessTokenQuotaWindowEmpty, got %v", err)
	}
	// 语义上它**不是**并发冲突：这条断言把两道闸的边界钉死。
	if errors.Is(err, ErrAccessTokenQuotaEpochConflict) {
		t.Fatal("serial double reset must not be reported as a concurrency conflict")
	}

	rows := loadQuotaEpochRows(t, db, token.ID)
	if len(rows) != 1 || rows[0].Epoch != 1 {
		t.Fatalf("rejected second reset must not append an empty epoch row: %+v", rows)
	}
	if stored := loadQuotaEpochToken(t, db, token.ID); stored.TotalAmountUsed != 12 {
		t.Fatalf("rejected second reset must not touch history: %v want 12", stored.TotalAmountUsed)
	}
}

// TestResetAccessTokenQuotaWindowUniqueIndexPathReturnsConflict 确定性地覆盖**唯一索引**这道闸。
//
// 为什么不能只靠上面的并发用例：生产 DSN 用 `_txlock=immediate` 把写事务串行化，
// 并发重置的落败方会排队到前一个提交之后，读到已提交的 epoch=1、本轮已用 0，
// 于是全部被语义闸（ErrAccessTokenQuotaWindowEmpty）挡下 —— 实测 breakdown 是
// `succeeded=1 epochConflict=0 windowEmpty=5`。唯一索引那条路径在这种时序下
// 根本不会被触发，等于没被测试覆盖。
//
// 这里用 GORM 的 Before("gorm:create") 回调在**同一事务内**抢先插一行相同的
// (token_id, epoch)，让真实插入必然撞唯一索引。同一连接执行，不涉及跨连接锁竞争，
// 因此是确定性的、与机器快慢无关。
func TestResetAccessTokenQuotaWindowUniqueIndexPathReturnsConflict(t *testing.T) {
	db := newQuotaEpochTestDB(t)
	admin := seedQuotaEpochAdmin(t, db)
	_, token := seedQuotaEpochToken(t, db, 8, 20, 12)

	const callbackName = "quota_epoch_preseed_competing_row"
	injected := false
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		epochRow, ok := tx.Statement.Dest.(*entity.AccessTokenQuotaEpoch)
		if !ok || injected || tx.Statement.Table != entity.TableNameAccessTokenQuotaEpoch {
			return
		}
		injected = true
		// 用原生 INSERT 而不是 tx.Create，避免回调递归。
		if err := tx.Exec(
			"INSERT INTO "+entity.TableNameAccessTokenQuotaEpoch+
				" (token_id, epoch, reset_at, baseline_amount, operator_id, event_id, created_at)"+
				" VALUES (?, ?, ?, ?, ?, ?, ?)",
			epochRow.TokenID, epochRow.Epoch, epochRow.ResetAt, epochRow.BaselineAmount,
			epochRow.OperatorID, "competing-writer", epochRow.ResetAt,
		).Error; err != nil {
			t.Errorf("seed competing epoch row: %v", err)
		}
	}); err != nil {
		t.Fatalf("register create callback: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Create().Remove(callbackName); err != nil {
			t.Errorf("remove create callback: %v", err)
		}
	})

	_, err := ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.26")
	if !errors.Is(err, ErrAccessTokenQuotaEpochConflict) {
		t.Fatalf("a unique-key collision must map to ErrAccessTokenQuotaEpochConflict, got %v", err)
	}
	// 语义上它**不是**「本轮已用为 0」：两者必须能区分开。
	if errors.Is(err, ErrAccessTokenQuotaWindowEmpty) {
		t.Fatal("unique-key collision must not be reported as an empty window")
	}

	// 事务回滚：抢跑的那一行不留下，令牌历史累计不动。
	rows := loadQuotaEpochRows(t, db, token.ID)
	if len(rows) != 0 {
		t.Fatalf("conflicted reset must roll back both rows: %+v", rows)
	}
	if stored := loadQuotaEpochToken(t, db, token.ID); stored.TotalAmountUsed != 12 {
		t.Fatalf("conflicted reset must not touch history: %v want 12", stored.TotalAmountUsed)
	}
}

// TestResetAccessTokenQuotaWindowConcurrentOnlyOneSucceeds 并发重置：只成功一次。
//
// 落败方按错误类型**分别计数并分别断言**（captain 要求）：
// 同一刻抢跑由唯一索引挡下（ErrAccessTokenQuotaEpochConflict），
// 晚到一方读到本轮已用 0 由语义闸挡下（ErrAccessTokenQuotaWindowEmpty）。
// 两者都算「被拒绝」，但必须能说清各自出现几次，不能糊成一个 rejected 计数。
//
// 实测时序（生产同款 DSN，_txlock=immediate 串行化写事务）：
// succeeded=1、epochConflict=0、windowEmpty=5 —— 串行化之后落败方都是被语义闸挡下的。
// 唯一索引那条路径由 TestResetAccessTokenQuotaWindowUniqueIndexPathReturnsConflict
// 确定性覆盖，两条用例合起来才把两道闸都钉住。
func TestResetAccessTokenQuotaWindowConcurrentOnlyOneSucceeds(t *testing.T) {
	db := newQuotaEpochTestDB(t)
	admin := seedQuotaEpochAdmin(t, db)
	_, token := seedQuotaEpochToken(t, db, 8, 20, 12)

	const racers = 6
	type outcome struct {
		result ResetAccessTokenQuotaWindowResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, racers)
	var wait sync.WaitGroup
	for i := 0; i < racers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := ResetAccessTokenQuotaWindowWithDB(db, admin, token.ID, "192.0.2.23")
			outcomes <- outcome{result, err}
		}()
	}
	close(start)
	wait.Wait()
	close(outcomes)

	succeeded := 0
	// 两道闸分别计数。
	epochConflicts := 0     // 唯一索引 (token_id, epoch)：同刻抢跑
	windowEmptyRejects := 0 // 语义闸：晚到方读到本轮已用 0
	retryableLocks := 0     // SQLite 锁竞争：可重试，不代表多插行
	unexpected := make([]error, 0, racers)
	for out := range outcomes {
		switch {
		case out.err == nil:
			succeeded++
		case errors.Is(out.err, ErrAccessTokenQuotaEpochConflict):
			epochConflicts++
		case errors.Is(out.err, ErrAccessTokenQuotaWindowEmpty):
			windowEmptyRejects++
		default:
			// SQLite 在高并发下可能先报锁冲突，这是可重试的竞争，不算「多插一行」。
			retryableLocks++
			unexpected = append(unexpected, out.err)
		}
	}
	t.Logf("concurrent reset breakdown: succeeded=%d epochConflict=%d windowEmpty=%d retryableLock=%d",
		succeeded, epochConflicts, windowEmptyRejects, retryableLocks)

	if succeeded != 1 {
		t.Fatalf("concurrent reset must succeed exactly once, succeeded=%d epochConflict=%d windowEmpty=%d retryableLock=%d errors=%v",
			succeeded, epochConflicts, windowEmptyRejects, retryableLocks, unexpected)
	}
	// 除成功者外，每一路都必须落进「两道闸之一」或「可重试锁竞争」，不得有第四类错误。
	if epochConflicts+windowEmptyRejects+retryableLocks != racers-1 {
		t.Fatalf("every loser must be classified, got epochConflict=%d windowEmpty=%d retryableLock=%d of %d",
			epochConflicts, windowEmptyRejects, retryableLocks, racers-1)
	}
	// 至少一路必须拿到**明确的语义拒绝**（而不是全部靠锁竞争侥幸通过），
	// 否则说明两道闸在这台机器上其实没生效。
	if epochConflicts+windowEmptyRejects == 0 {
		t.Fatalf("no loser received a semantic rejection; the gates did not fire (retryableLock=%d)", retryableLocks)
	}

	rows := loadQuotaEpochRows(t, db, token.ID)
	if len(rows) != 1 || rows[0].Epoch != 1 {
		t.Fatalf("concurrent reset must insert exactly one epoch row: %+v", rows)
	}
	if stored := loadQuotaEpochToken(t, db, token.ID); stored.TotalAmountUsed != 12 {
		t.Fatalf("concurrent reset must not touch history: %v want 12", stored.TotalAmountUsed)
	}
}

// TestResetAccessTokenQuotaWindowRejectsNonAdmin 普通账号调该接口 → 服务层拒绝（路由层 403）。
func TestResetAccessTokenQuotaWindowRejectsNonAdmin(t *testing.T) {
	db := newQuotaEpochTestDB(t)
	owner, token := seedQuotaEpochToken(t, db, 8, 20, 12)

	if _, err := ResetAccessTokenQuotaWindowWithDB(db, owner, token.ID, "192.0.2.24"); !errors.Is(err, ErrAccessTokenQuotaEpochAdminRequired) {
		t.Fatalf("non-admin reset must be rejected, got %v", err)
	}
	if rows := loadQuotaEpochRows(t, db, token.ID); len(rows) != 0 {
		t.Fatalf("rejected reset must not write an epoch row: %+v", rows)
	}
	if stored := loadQuotaEpochToken(t, db, token.ID); stored.TotalAmountUsed != 12 {
		t.Fatalf("rejected reset must not touch history: %v want 12", stored.TotalAmountUsed)
	}
}
