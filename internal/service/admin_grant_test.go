package service

import (
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 本文件钉死「管理员赠送 + 追缴式回滚」的五条资金守恒判据（任务书「必写的资金守恒测试」）。
//
// # 为什么每一条都必须存在
//
// 这五条各自对应一类**会静默产生资损**的实现错误：
//
//	① 赠送先抵欠款    写错成 gift += amount → 欠款用户的赠金越攒越多（欠的钱永远还不掉）
//	② 回滚只扣赠金    写错成 gear 到 0 就停 → 已消费部分永久追不回（应扣 10 只扣 7）
//	③ 差额必须留痕    写错成静默吞掉 → 对账时凭空少一笔，且查不出是谁
//	④ 本金可为负      写错成 clamp 到 0 → 追缴变成「扣不动」，欠款机制整体失效
//	⑤ 金额 0 不写流水 写错成照写 → 大量 amount=0 的垃圾流水淹没账单
//
// 每条断言都要求在**真实库**上读回两桶值与流水行，不做 struct 自比。

// adminGrantDBSeq 保证每个用例拿到独立的内存库。
//
// `file:xxx?mode=memory&cache=shared` 同名库在同一进程内是**共享**的，表驱动用例的
// t.Name() 又可能重复 —— 不加序号会撞主键（与 registration_clawback_test.go 同款坑）。
var adminGrantDBSeq atomic.Int64

// newAdminGrantTestDB 建一个带赠送/回滚所需全部表的内存库。
//
// DSN 走 util.SQLiteConcurrentDSN：与生产 sqliteProductionDSN 共用同一份定义
// （`_txlock=immediate` + `busy_timeout(5000)`）。用默认 DSN 时，凡是开事务再嵌套
// 写另一张表的路径会报 `database table is locked` —— 那是测出了 SQLite 的默认锁行为，
// 不是被测语义。见 task_doc/47。
func newAdminGrantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := util.SQLiteConcurrentDSN(
		"file:admin-grant-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
			"-" + strconv.FormatInt(adminGrantDBSeq.Add(1), 10) + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&entity.User{},
		&entity.BillingRecord{},
		&entity.IdempotencyRecord{},
		&entity.SystemSetting{},
		&entity.Log{},
		&entity.UserLevel{},
		&entity.UserLevelHistory{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	// ⚠️ entity.UserArchive 与 entity.User 指向同一张 users 表，但只声明归档四列。
	// 把它放进同一个 AutoMigrate 列表会**先于** User 建出只有 5 列的 users 表
	// （实测报 `table users has no column named username`）。
	// 生产环境这四列由 registry 的显式 ALTER 补，测试库这里用同样的方式补。
	for _, column := range []string{
		"archived_reason varchar(32) NOT NULL DEFAULT ''",
		"archived_at datetime",
		"archived_by_user_id int NOT NULL DEFAULT 0",
	} {
		if err := db.Exec("ALTER TABLE users ADD COLUMN " + column).Error; err != nil {
			t.Fatalf("add archived column %q: %v", column, err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedAdminGrantAdmin 种一个管理员（赠送的 operator）。
func seedAdminGrantAdmin(t *testing.T, db *gorm.DB) entity.User {
	t.Helper()
	admin := entity.User{
		ID: 1, Username: "grant-admin", Password: "123456", Role: "admin",
		Status: "enabled", RegisterSource: "admin",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return admin
}

// seedAdminGrantUser 种一个带指定两桶余额与状态的用户。
func seedAdminGrantUser(t *testing.T, db *gorm.DB, id int32, name string, balance, gift float64) entity.User {
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

// reloadAdminGrantUser 从库里读回用户（**必须**读库，不能用入参 struct 自比）。
func reloadAdminGrantUser(t *testing.T, db *gorm.DB, id int32) entity.User {
	t.Helper()
	var user entity.User
	if err := db.First(&user, id).Error; err != nil {
		t.Fatalf("reload user %d: %v", id, err)
	}
	return user
}

// adminGrantBills 读回某用户的指定类型流水（按 id 升序）。
func adminGrantBills(t *testing.T, db *gorm.DB, userID int32, recordType string) []entity.BillingRecord {
	t.Helper()
	var records []entity.BillingRecord
	if err := db.Where("user_id = ? AND type = ?", userID, recordType).Order("id asc").Find(&records).Error; err != nil {
		t.Fatalf("load %s bills: %v", recordType, err)
	}
	return records
}

// ---------------------------------------------------------------------------
// ① 赠送入账：先抵欠款，再进赠金桶
// ---------------------------------------------------------------------------

// TestAdminGrantOffsetsDebtBeforeGiftBucket 是任务书的第一条：欠款 1 元获赠 2 元 → 0 / 1。
//
// 43-*.md 算例 B 的原始断言。若实现写成 `gift += amount`，结果是 balance=-1 / gift=2：
// 用户欠的钱永远还不掉，而赠金却在涨 —— 这是双桶模型下最容易犯、也最难在界面上看出的错。
func TestAdminGrantOffsetsDebtBeforeGiftBucket(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	user := seedAdminGrantUser(t, db, 2, "debt-user", -1, 0)

	result, err := AdminGrant(db, AdminGrantRequest{
		Operator: admin, UserID: user.ID, Amount: 2, Reason: "补偿", Anchor: "single-test-debt",
	})
	if err != nil {
		t.Fatalf("AdminGrant: %v", err)
	}

	stored := reloadAdminGrantUser(t, db, user.ID)
	if stored.Balance != 0 {
		t.Fatalf("balance must be offset to 0 (欠款先抵), got %v", stored.Balance)
	}
	if stored.GiftBalance != 1 {
		t.Fatalf("gift_balance must be 1 (2-1 抵欠款后剩余), got %v", stored.GiftBalance)
	}
	// 返回值也必须与库一致 —— 只对库断言会让「返回给前端的金额错了」漏过。
	if result.BalanceAfter != 0 || result.GiftAfter != 1 || result.DebtOffset != 1 {
		t.Fatalf("result mismatch: %+v", result)
	}

	bills := adminGrantBills(t, db, user.ID, AdminGrantBillingRecordType)
	if len(bills) != 1 {
		t.Fatalf("expected exactly 1 admin_grant bill, got %d", len(bills))
	}
	if bills[0].Amount != 2 || bills[0].BalanceBefore != -1 || bills[0].BalanceAfter != 0 {
		t.Fatalf("bill must record amount=2 and -1 → 0: %+v", bills[0])
	}
	giftBefore, giftAfter, ok := parseGiftSnapshot(bills[0].UnitPriceSnapshot)
	if !ok || giftBefore != 0 || giftAfter != 1 {
		t.Fatalf("bill snapshot must carry gift 0 → 1, got ok=%t before=%v after=%v (%s)",
			ok, giftBefore, giftAfter, bills[0].UnitPriceSnapshot)
	}
}

// TestAdminGrantRejectsNonPositiveAmount 钉住金额校验（0 / 负数都拒绝且不落任何流水）。
func TestAdminGrantRejectsNonPositiveAmount(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	user := seedAdminGrantUser(t, db, 2, "amount-user", 5, 0)

	for _, amount := range []float64{0, -1, -0.000001} {
		if _, err := AdminGrant(db, AdminGrantRequest{
			Operator: admin, UserID: user.ID, Amount: amount, Anchor: "single-amount-" + balanceMoneyText(amount),
		}); !errors.Is(err, ErrInvalidAdminGrant) {
			t.Fatalf("amount %v must be rejected, got %v", amount, err)
		}
	}
	if bills := adminGrantBills(t, db, user.ID, AdminGrantBillingRecordType); len(bills) != 0 {
		t.Fatalf("rejected amounts must not write bills, got %d", len(bills))
	}
	if stored := reloadAdminGrantUser(t, db, user.ID); stored.Balance != 5 || stored.GiftBalance != 0 {
		t.Fatalf("rejected amounts must not touch wallet: %+v", stored)
	}
}

// TestAdminGrantRequiresConfirmationAboveThreshold 钉住 ⑨：单笔超阈值必须二次确认。
func TestAdminGrantRequiresConfirmationAboveThreshold(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	user := seedAdminGrantUser(t, db, 2, "confirm-user", 0, 0)

	// 阈值缺省 10，恰好 10 元**不**触发（严格大于）。
	if _, err := AdminGrant(db, AdminGrantRequest{Operator: admin, UserID: user.ID, Amount: 10, Anchor: "single-eq"}); err != nil {
		t.Fatalf("amount == threshold must not require confirmation: %v", err)
	}
	if _, err := AdminGrant(db, AdminGrantRequest{Operator: admin, UserID: user.ID, Amount: 10.01, Anchor: "single-gt"}); !errors.Is(err, ErrAdminGrantConfirmRequired) {
		t.Fatalf("amount > threshold must require confirmation, got %v", err)
	}
	if stored := reloadAdminGrantUser(t, db, user.ID); stored.GiftBalance != 10 {
		t.Fatalf("unconfirmed grant must not touch wallet, got gift=%v", stored.GiftBalance)
	}
	// 确认后放行。
	if _, err := AdminGrant(db, AdminGrantRequest{
		Operator: admin, UserID: user.ID, Amount: 10.01, Anchor: "single-gt", Confirmed: true,
	}); err != nil {
		t.Fatalf("confirmed grant must pass: %v", err)
	}
	if stored := reloadAdminGrantUser(t, db, user.ID); stored.GiftBalance != 20.01 {
		t.Fatalf("confirmed grant must land, got gift=%v", stored.GiftBalance)
	}
}

// ---------------------------------------------------------------------------
// ② 回滚：赠金实扣 < 应扣，差额转本金 + 两笔流水金额分别正确
// ---------------------------------------------------------------------------

// TestAdminGrantReversalRecordsShortfallAndBothBills 是任务书第二条 + 第五条。
//
// 场景（118-*.md §5.5）：赠 10、花了 3（赠金剩 7）、本金 50。
// 期望：赠金扣 7 → 0；差额 3 从本金扣 → 47；两笔流水 7.00 / 3.00，且 remark 里写明差额。
func TestAdminGrantReversalRecordsShortfallAndBothBills(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	user := seedAdminGrantUser(t, db, 2, "shortfall-user", 50, 7)

	result, err := AdminGrantReverse(db, AdminGrantReverseRequest{
		Operator: admin, UserID: user.ID, Amount: 10, Anchor: "batch-shortfall", Reason: "撤销误发",
	})
	if err != nil {
		t.Fatalf("AdminGrantReverse: %v", err)
	}

	stored := reloadAdminGrantUser(t, db, user.ID)
	if stored.GiftBalance != 0 {
		t.Fatalf("gift must be drained to 0, got %v", stored.GiftBalance)
	}
	if stored.Balance != 47 {
		t.Fatalf("principal must absorb the 3 shortfall (50 → 47), got %v", stored.Balance)
	}
	// 「实扣 < 应扣」必须在返回值里体现，不能对外假装扣满。
	if result.FromGift != 7 || result.FromBalance != 3 || result.Shortfall != 3 {
		t.Fatalf("shortfall split wrong: %+v", result)
	}

	bills := adminGrantBills(t, db, user.ID, AdminGrantReversalBillingRecordType)
	if len(bills) != 2 {
		t.Fatalf("expected 2 reversal bills (gift + principal), got %d", len(bills))
	}
	byRequestID := make(map[string]entity.BillingRecord, len(bills))
	for _, bill := range bills {
		byRequestID[bill.RequestID] = bill
	}
	giftBill, ok := byRequestID[AdminGrantReversalGiftRequestID(user.ID, "batch-shortfall")]
	if !ok {
		t.Fatalf("missing gift bill, request ids: %v", mapKeys(byRequestID))
	}
	if giftBill.Amount != 7 {
		t.Fatalf("gift bill amount must be the actual 7, got %v", giftBill.Amount)
	}
	principalBill, ok := byRequestID[AdminGrantReversalPrincipalRequestID(user.ID, "batch-shortfall")]
	if !ok {
		t.Fatalf("missing principal bill, request ids: %v", mapKeys(byRequestID))
	}
	if principalBill.Amount != 3 {
		t.Fatalf("principal bill amount must be the shortfall 3, got %v", principalBill.Amount)
	}
	// ④ 差额留痕：remark 必须同时出现「应扣」「实扣」与差额，否则对账时看不出少了多少。
	if !strings.Contains(giftBill.Remark, "应扣") || !strings.Contains(giftBill.Remark, "实扣") {
		t.Fatalf("gift bill remark must state 应扣/实扣: %q", giftBill.Remark)
	}
	if !strings.Contains(giftBill.Remark, "差额") || !strings.Contains(giftBill.Remark, "3") {
		t.Fatalf("gift bill remark must record the ¥3 shortfall: %q", giftBill.Remark)
	}
}

// TestAdminGrantReversalGiftBucketNeverNegative 是「赠金下限 0」的钉子（变异 A 的目标）。
//
// 赠金只有 7、应追缴 10：赠金必须停在 0，绝不能变成 -3（② 关键约束）。
func TestAdminGrantReversalGiftBucketNeverNegative(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	user := seedAdminGrantUser(t, db, 3, "floor-user", 0, 7)

	result, err := AdminGrantReverse(db, AdminGrantReverseRequest{
		Operator: admin, UserID: user.ID, Amount: 10, Anchor: "batch-floor",
	})
	if err != nil {
		t.Fatalf("AdminGrantReverse: %v", err)
	}
	stored := reloadAdminGrantUser(t, db, user.ID)
	if stored.GiftBalance != 0 {
		t.Fatalf("gift_balance must floor at 0 (never negative), got %v", stored.GiftBalance)
	}
	if result.FromGift != 7 {
		t.Fatalf("fromGift must be capped by the bucket (7), got %v", result.FromGift)
	}
}

// ---------------------------------------------------------------------------
// ③ 回滚到负本金：balance 变负，没被 clamp
// ---------------------------------------------------------------------------

// TestAdminGrantReversalDrivesPrincipalNegative 钉住「本金不 clamp」（117-*.md 算例 3）。
//
// 赠金 0、本金 0、应追缴 10 → balance = -10（欠款）。若实现 clamp 到 0，
// 追缴就变成「扣不动」，而用户照样用服务 —— 这是追缴机制整体失效的形态。
func TestAdminGrantReversalDrivesPrincipalNegative(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	user := seedAdminGrantUser(t, db, 4, "negative-user", 0, 0)

	result, err := AdminGrantReverse(db, AdminGrantReverseRequest{
		Operator: admin, UserID: user.ID, Amount: 10, Anchor: "batch-negative",
	})
	if err != nil {
		t.Fatalf("AdminGrantReverse: %v", err)
	}
	stored := reloadAdminGrantUser(t, db, user.ID)
	if stored.Balance != -10 {
		t.Fatalf("principal must go negative (no clamp), got %v", stored.Balance)
	}
	if stored.GiftBalance != 0 {
		t.Fatalf("gift must stay 0, got %v", stored.GiftBalance)
	}
	if result.FromBalance != 10 || result.FromGift != 0 {
		t.Fatalf("all 10 must come from principal: %+v", result)
	}

	bills := adminGrantBills(t, db, user.ID, AdminGrantReversalBillingRecordType)
	if len(bills) != 1 {
		t.Fatalf("gift part is 0 → only the principal bill may exist, got %d bills", len(bills))
	}
	if bills[0].RequestID != AdminGrantReversalPrincipalRequestID(user.ID, "batch-negative") {
		t.Fatalf("only the principal bill may be written, got %q", bills[0].RequestID)
	}
	if bills[0].BalanceAfter != -10 || !strings.Contains(bills[0].Remark, "欠款") {
		t.Fatalf("principal bill must show the debt: %+v", bills[0])
	}
}

// ---------------------------------------------------------------------------
// ④ 同一请求跑两次：第二次 no-op
// ---------------------------------------------------------------------------

// TestAdminGrantIsIdempotentAcrossReplay 钉住 ⑥：幂等锚点是 billing_records.request_id。
//
// 重复跑两次必须① 余额不变 ② 流水仍只有一行 ③ 第二次明确标记 AlreadyGranted。
// 回滚侧同理：两笔都判重之后整笔跳过。
func TestAdminGrantIsIdempotentAcrossReplay(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	user := seedAdminGrantUser(t, db, 5, "replay-user", 0, 0)
	request := AdminGrantRequest{Operator: admin, UserID: user.ID, Amount: 5, Anchor: "batch-replay"}

	first, err := AdminGrant(db, request)
	if err != nil {
		t.Fatalf("first grant: %v", err)
	}
	second, err := AdminGrant(db, request)
	if err != nil {
		t.Fatalf("replayed grant: %v", err)
	}
	if !second.AlreadyGranted || first.AlreadyGranted {
		t.Fatalf("second call must be a no-op replay: first=%+v second=%+v", first, second)
	}
	stored := reloadAdminGrantUser(t, db, user.ID)
	if stored.GiftBalance != 5 {
		t.Fatalf("replay must not add money again, gift=%v", stored.GiftBalance)
	}
	if bills := adminGrantBills(t, db, user.ID, AdminGrantBillingRecordType); len(bills) != 1 {
		t.Fatalf("replay must not write a second bill, got %d", len(bills))
	}

	// 回滚同样幂等：跑两次，两笔流水各仍只有一行，余额不再变化。
	reverseRequest := AdminGrantReverseRequest{Operator: admin, UserID: user.ID, Amount: 5, Anchor: "batch-replay"}
	if _, err := AdminGrantReverse(db, reverseRequest); err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	replayed, err := AdminGrantReverse(db, reverseRequest)
	if err != nil {
		t.Fatalf("replayed reversal: %v", err)
	}
	if !replayed.Skipped {
		t.Fatalf("second reversal must be skipped: %+v", replayed)
	}
	stored = reloadAdminGrantUser(t, db, user.ID)
	if stored.GiftBalance != 0 || stored.Balance != 0 {
		t.Fatalf("replayed reversal must not deduct again: %+v", stored)
	}
	reversals := adminGrantBills(t, db, user.ID, AdminGrantReversalBillingRecordType)
	if len(reversals) != 1 {
		t.Fatalf("only the gift bill exists (principal part is 0), got %d", len(reversals))
	}
}

// ---------------------------------------------------------------------------
// ⑤ 金额为 0 的那笔不写流水
// ---------------------------------------------------------------------------

// TestAdminGrantReversalSkipsZeroAmountBill 钉住 118-*.md §5.4 的三态。
//
//	一分没花（赠金 10）→ 只写赠金笔 10.00，本金笔不写
//	花完了  （赠金 0） → 只写本金笔 10.00，赠金笔不写
func TestAdminGrantReversalSkipsZeroAmountBill(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)

	// 情形 1：赠金充足 → 只有赠金笔。
	untouched := seedAdminGrantUser(t, db, 6, "untouched-user", 20, 10)
	if _, err := AdminGrantReverse(db, AdminGrantReverseRequest{
		Operator: admin, UserID: untouched.ID, Amount: 10, Anchor: "batch-untouched",
	}); err != nil {
		t.Fatalf("reversal (untouched): %v", err)
	}
	untouchedBills := adminGrantBills(t, db, untouched.ID, AdminGrantReversalBillingRecordType)
	if len(untouchedBills) != 1 || untouchedBills[0].Amount != 10 {
		t.Fatalf("untouched user: only one gift bill of 10, got %+v", untouchedBills)
	}
	if untouchedBills[0].RequestID != AdminGrantReversalGiftRequestID(untouched.ID, "batch-untouched") {
		t.Fatalf("untouched user: must be the gift bill, got %q", untouchedBills[0].RequestID)
	}
	if stored := reloadAdminGrantUser(t, db, untouched.ID); stored.Balance != 20 || stored.GiftBalance != 0 {
		t.Fatalf("untouched user: principal must stay 20, gift 0, got %+v", stored)
	}

	// 情形 2：赠金已花完 → 只有本金笔。
	spent := seedAdminGrantUser(t, db, 7, "spent-user", 20, 0)
	if _, err := AdminGrantReverse(db, AdminGrantReverseRequest{
		Operator: admin, UserID: spent.ID, Amount: 10, Anchor: "batch-spent",
	}); err != nil {
		t.Fatalf("reversal (spent): %v", err)
	}
	spentBills := adminGrantBills(t, db, spent.ID, AdminGrantReversalBillingRecordType)
	if len(spentBills) != 1 || spentBills[0].Amount != 10 {
		t.Fatalf("spent user: only one principal bill of 10, got %+v", spentBills)
	}
	if spentBills[0].RequestID != AdminGrantReversalPrincipalRequestID(spent.ID, "batch-spent") {
		t.Fatalf("spent user: must be the principal bill, got %q", spentBills[0].RequestID)
	}
	if stored := reloadAdminGrantUser(t, db, spent.ID); stored.Balance != 10 {
		t.Fatalf("spent user: principal 20 → 10, got %v", stored.Balance)
	}
}

// ---------------------------------------------------------------------------
// ⑤ 追缴不删除、不改小原流水
// ---------------------------------------------------------------------------

// TestAdminGrantReversalKeepsOriginalGrantRow 钉住关键约束 ⑤。
//
// 回滚必须是**独立的反向流水**：原 admin_grant 行的金额、前后余额一字不动，
// 否则账本无法对账（改历史数据后「为什么余额是 X」永久失去答案）。
func TestAdminGrantReversalKeepsOriginalGrantRow(t *testing.T) {
	db := newAdminGrantTestDB(t)
	admin := seedAdminGrantAdmin(t, db)
	user := seedAdminGrantUser(t, db, 8, "immutable-user", 0, 0)

	if _, err := AdminGrant(db, AdminGrantRequest{
		Operator: admin, UserID: user.ID, Amount: 10, Anchor: "batch-immutable",
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	before := adminGrantBills(t, db, user.ID, AdminGrantBillingRecordType)
	if len(before) != 1 {
		t.Fatalf("expected 1 grant bill, got %d", len(before))
	}

	if _, err := AdminGrantReverse(db, AdminGrantReverseRequest{
		Operator: admin, UserID: user.ID, Amount: 10, Anchor: "batch-immutable",
	}); err != nil {
		t.Fatalf("reversal: %v", err)
	}

	after := adminGrantBills(t, db, user.ID, AdminGrantBillingRecordType)
	if len(after) != 1 {
		t.Fatalf("original grant row must survive (not deleted), got %d", len(after))
	}
	if after[0].ID != before[0].ID || after[0].Amount != 10 ||
		after[0].BalanceBefore != before[0].BalanceBefore || after[0].BalanceAfter != before[0].BalanceAfter {
		t.Fatalf("original grant row must be byte-identical:\nbefore=%+v\nafter=%+v", before[0], after[0])
	}
}

// ---------------------------------------------------------------------------
// 纯函数：planAdminGrantReversal 的两桶分配
// ---------------------------------------------------------------------------

// TestPlanAdminGrantReversalMatrix 是回滚分配算法的表驱动钉子。
//
// 先钉纯函数的好处：上层的库级用例失败时，能立刻区分「分配算错了」还是「写库写错了」。
func TestPlanAdminGrantReversalMatrix(t *testing.T) {
	cases := []struct {
		name                                               string
		balance, gift, amount                              float64
		wantGift, wantBalance, wantGiftAfter, wantBalAfter float64
	}{
		{"赠金充足", 50, 10, 10, 10, 0, 0, 50},
		{"赠金部分", 50, 7, 10, 7, 3, 0, 47},
		{"赠金为零", 0, 0, 10, 0, 10, 0, -10},
		{"本金也为负", -5, 4, 10, 4, 6, 0, -11},
		{"应扣为 0", 8, 3, 0, 0, 0, 3, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fromGift, fromBalance, giftAfter, balanceAfter := planAdminGrantReversal(tc.balance, tc.gift, tc.amount)
			if fromGift != tc.wantGift || fromBalance != tc.wantBalance {
				t.Fatalf("split %v/%v, want %v/%v", fromGift, fromBalance, tc.wantGift, tc.wantBalance)
			}
			if giftAfter != tc.wantGiftAfter || balanceAfter != tc.wantBalAfter {
				t.Fatalf("buckets %v/%v, want %v/%v", giftAfter, balanceAfter, tc.wantGiftAfter, tc.wantBalAfter)
			}
			// 守恒：两桶实扣 = 应扣（追缴不设上限，差额总能落到本金）。
			if util.RoundMoney(fromGift+fromBalance) != util.RoundMoney(tc.amount) {
				t.Fatalf("追缴必须全额执行：%v+%v != %v", fromGift, fromBalance, tc.amount)
			}
			if giftAfter < 0 {
				t.Fatalf("gift bucket must never go negative, got %v", giftAfter)
			}
		})
	}
}

// mapKeys 只用于失败信息里打印 request_id 集合。
func mapKeys(m map[string]entity.BillingRecord) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
