package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 归档用户名释放 + 恢复冲突改名（task_doc/106 Q4、task_doc/76 §3.4）的钉子。
//
// # 这组用例守的是什么
//
// 用户拍板的语义是「归档释放用户名（可被新用户注册）/ 恢复时若被占则随机新名」。
// 这条语义最容易被三种「看起来对」的实现架空：
//
//  1. **什么都不做也算通过** —— users.username 没有唯一索引，`db.Create` 一个同名
//     用户可以无条件成功。若拿「创建成功」当「已释放」的判据，用例永远绿。
//     真实闸门是应用层检查（controller/user.go:608
//     `dao.User.Where(dao.User.Username.Eq(username)).First()`），
//     所以本文件用**同一条查询**判「能不能注册」（registrationAllowsUsername）。
//  2. **冲突时直接拒绝恢复** —— 用户明确要求改名而不是拒绝（106 §3 Q4）。
//  3. **重试无上限 / 耗尽后半改** —— 耗尽必须返回可判定的错误，且不留半改状态。
//
// 每个断言前都有对照组（例如「归档前同名注册必须被拒」），
// 避免「样本本身就不冲突」导致断言恒真。

// newUsernameReleaseTestDB 建一个带 users / 归档四列 / logs 的内存库。
//
// 库名带 t.Name()：`file:xxx?mode=memory&cache=shared` 在同一进程内同名库共享，
// 跨用例会互相污染。
//
// DSN 走 util.SQLiteConcurrentDSN（与生产 registry.sqliteProductionDSN 同源），
// 不手写并发参数 —— 手写的那份一旦生产改了就是「测试测的是另一套锁行为」。
func newUsernameReleaseTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := util.SQLiteConcurrentDSN(
		"file:username-release-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.Log{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	// ⚠️ UserArchive 必须**单独一次** AutoMigrate，不能和 User 放同一个调用。
	//
	// 实测（本 worktree，glebarez/sqlite）：把两者放进同一个
	// `AutoMigrate(&entity.User{}, &entity.UserArchive{}, &entity.Log{})` 调用后，
	// users 表只剩 5 列（id / deleted_at / archived_reason / archived_at /
	// archived_by_user_id）—— UserArchive 是 users 的**第二个**模型声明，
	// GORM 按表名去重后建出了它的精简版，插入用户时报
	// 「table users has no column named username」，而错误信息完全不提 AutoMigrate。
	//
	// controller 的 newUserControllerTestDB 早就是分两次调用（user_test.go:38-46），
	// 这里对齐同一做法。
	if err := db.AutoMigrate(&entity.UserArchive{}); err != nil {
		t.Fatalf("migrate user archive columns: %v", err)
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

// registrationAllowsUsername 复刻注册路径的用户名闸门（controller/user.go:608）。
//
// 必须是**同一条查询**：`entity.User` 带 gorm.DeletedAt，因此归档行
// （deleted_at 非空）自动不参与判定。这也是「归档即释放」的实现基础。
//
// ⚠️ 不能用「insert 一个同名用户成功了」当判据：users.username 无唯一索引，
// 那种断言在任何实现下都成立（假钉子第一形态）。
func registrationAllowsUsername(t *testing.T, db *gorm.DB, username string) bool {
	t.Helper()
	var existing entity.User
	err := db.Session(&gorm.Session{NewDB: true}).
		Where("username = ?", strings.TrimSpace(username)).Take(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true
	}
	if err != nil {
		t.Fatalf("probe username %q for registration: %v", username, err)
	}
	return false
}

// seedReleaseUser 建一个活跃账号。
func seedReleaseUser(t *testing.T, db *gorm.DB, id int32, username string) *entity.User {
	t.Helper()
	user := &entity.User{
		ID: id, Username: username, Password: "synthetic-pw-hash", Nickname: username + "-nick",
		Role: "user", Status: "enabled", RegisterSource: "public", Balance: 1, GiftBalance: 0.5,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user
}

// archiveThroughRealPath 走**真实归档路径**（ArchiveUserInTx + 绑定释放）。
//
// 不手写 SQL：手写会让「归档到底改了哪几列、有没有顺带释放用户名」在测试里
// 自成一套，实现改了用例照样绿。
func archiveThroughRealPath(t *testing.T, db *gorm.DB, user entity.User, reason string) UsernameRelease {
	t.Helper()
	var release UsernameRelease
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ReleaseAllBindingsForArchiveInTx(tx, user.ID, reason, 999); err != nil {
			return err
		}
		var err error
		release, err = ArchiveUserInTxWithRelease(tx, user, reason, 999)
		return err
	}); err != nil {
		t.Fatalf("archive user %s through real path: %v", user.Username, err)
	}
	return release
}

// releaseAuditRows 取「用户名释放」写出的 logs 行。
func releaseAuditRows(t *testing.T, db *gorm.DB) []entity.Log {
	t.Helper()
	var rows []entity.Log
	if err := db.Where("module = ? AND action = ?", usernameReleaseAuditModule, usernameReleaseAuditAction).
		Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("read username release logs: %v", err)
	}
	return rows
}

// TestUsernameReleaseArchivedNameBecomesRegistrable 是 Q4 的**核心用例**：
// 归档后原用户名必须能被新账号注册（同一条应用层查询从「拒绝」翻转为「允许」）。
func TestUsernameReleaseArchivedNameBecomesRegistrable(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 101, "zhangsan")

	// 对照组：归档**之前**同名注册必须被拒 —— 否则下面的「翻转为允许」
	// 不能证明归档起了作用（样本可能本来就不冲突）。
	if registrationAllowsUsername(t, db, "zhangsan") {
		t.Fatal("control failed: an active account must block same-name registration")
	}
	if released, err := UsernameReleasedForRegistration(db, "zhangsan"); err != nil || released {
		t.Fatalf("before archive UsernameReleasedForRegistration must be false, got %v (err=%v)", released, err)
	}

	release := archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonUserClosure)
	if !release.Released {
		t.Fatalf("archive must report the username as released, got %+v", release)
	}
	if release.OccupiedByUserID != 0 {
		t.Fatalf("no other active account should hold the name, got occupiedBy=%d", release.OccupiedByUserID)
	}

	// 核心断言：注册闸门翻转。
	if !registrationAllowsUsername(t, db, "zhangsan") {
		t.Fatal("archived account must stop occupying the username so a new user can register it")
	}
	if released, err := UsernameReleasedForRegistration(db, "zhangsan"); err != nil || !released {
		t.Fatalf("after archive UsernameReleasedForRegistration must be true, got %v (err=%v)", released, err)
	}

	// 归档行本身必须仍在、且**保留原用户名**：恢复要靠它判断冲突；
	// 一旦归档时改名，「这个账号原来叫什么」就只剩日志可查。
	var archived entity.User
	if err := db.Unscoped().Where("id = ?", 101).Take(&archived).Error; err != nil {
		t.Fatalf("archived row must still exist: %v", err)
	}
	if archived.Username != "zhangsan" {
		t.Fatalf("archive must keep the original username for restore, got %q", archived.Username)
	}
	if !archived.DeletedAt.Valid {
		t.Fatal("archived row must carry a non-null deleted_at")
	}

	// 释放动作必须可追溯（用既有 logs 表，不新建表）。
	rows := releaseAuditRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 username-release audit row, got %d", len(rows))
	}
	if rows[0].UserID != 999 {
		t.Fatalf("release log user_id must be the operator 999, got %d", rows[0].UserID)
	}
	if !strings.Contains(rows[0].Description, "zhangsan") {
		t.Fatalf("release log must name the released username, got %q", rows[0].Description)
	}
	// Request 走 accountAuditFields 白名单：白名单外的键会被静默丢弃，
	// 因此这里断言的是「关键字段真的落地了」而不是「代码写了 map」。
	if !strings.Contains(rows[0].OldAnswer, `"username":"zhangsan"`) {
		t.Fatalf("release log snapshot must carry username, got %s", rows[0].OldAnswer)
	}
	if !strings.Contains(rows[0].OldAnswer, `"targetUserId":101`) {
		t.Fatalf("release log snapshot must carry targetUserId, got %s", rows[0].OldAnswer)
	}
}

// TestUsernameReleaseKeepsNameBlockedWhenAnotherActiveAccountHoldsIt 覆盖历史同名数据：
// 归档前就已经有另一个活跃账号用同一个名字时，释放对该名字**没有**额外效果，
// 必须如实上报（Released=false），不能因为「归档成功」就宣称已释放。
func TestUsernameReleaseKeepsNameBlockedWhenAnotherActiveAccountHoldsIt(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 111, "duplicated")
	seedReleaseUser(t, db, 112, "duplicated") // 历史遗留的同名活跃账号

	release := archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonAdminDelete)
	if release.Released {
		t.Fatalf("name is still held by an active account, must not report released: %+v", release)
	}
	if release.OccupiedByUserID != 112 {
		t.Fatalf("expected occupiedBy=112, got %d", release.OccupiedByUserID)
	}
	if registrationAllowsUsername(t, db, "duplicated") {
		t.Fatal("an active holder must keep blocking the name even after another account is archived")
	}
	rows := releaseAuditRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("expected 1 release audit row, got %d", len(rows))
	}
	if !strings.Contains(rows[0].Description, "112") {
		t.Fatalf("audit must name the active holder so the state is explainable later, got %q", rows[0].Description)
	}
}

// TestUsernameReleaseEmptyUsernameSkipsAudit 空用户名不该在日志里留噪声。
func TestUsernameReleaseEmptyUsernameSkipsAudit(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 121, "")
	release := archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonAdminDelete)
	if !release.Released {
		t.Fatalf("empty username needs no release, got %+v", release)
	}
	if rows := releaseAuditRows(t, db); len(rows) != 0 {
		t.Fatalf("empty username must not write a release log, got %d rows", len(rows))
	}
}

// TestUsernameReleaseResolveKeepsOriginalWhenFree 「用户名空着（无冲突）」→ 保留原名。
func TestUsernameReleaseResolveKeepsOriginalWhenFree(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 131, "lisi")
	archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonUserClosure)

	decision, err := ResolveRestoreUsernameWithDB(db, 131, "lisi")
	if err != nil {
		t.Fatalf("resolve must succeed when the name is free: %v", err)
	}
	if decision.Final != "lisi" || decision.Renamed {
		t.Fatalf("free name must be kept as-is, got %+v", decision)
	}
	if decision.OccupiedByUserID != 0 || decision.Attempts != 0 {
		t.Fatalf("free name must not run the rename loop, got %+v", decision)
	}
	// 保留原名 ⇒ 调用方无需写 username 列，但必须能写 deleted_at=nil。
	if decision.Final != decision.Archived {
		t.Fatalf("kept name must equal the archived name, got %q vs %q", decision.Final, decision.Archived)
	}
}

// TestUsernameReleaseResolveKeepsBlankWhenArchivedNameBlank 归档行用户名为空时
// **不动** username 列：凭空造一个名字会让用户看到一个从未属于他的账号名，
// 同时其余字段仍必须能恢复（不能因为一列脏了就拒绝整条恢复）。
func TestUsernameReleaseResolveKeepsBlankWhenArchivedNameBlank(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 141, "")
	archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonAdminDelete)
	// 造一个「名字被占」的语境：如果实现凭空生成名字，这里会走到改名分支。
	seedReleaseUser(t, db, 142, "user_restored_1")

	decision, err := ResolveRestoreUsernameWithDB(db, 141, "")
	if err != nil {
		t.Fatalf("blank archived name must not fail the resolve: %v", err)
	}
	if decision.Final != "" {
		t.Fatalf("blank archived name must leave the username column untouched (Final empty), got %q", decision.Final)
	}
	if decision.Renamed {
		t.Fatalf("blank archived name must not be reported as renamed, got %+v", decision)
	}
}

// TestUsernameReleaseResolveRenamesOnConflictAndStaysValid 冲突 → 生成随机新名，
// 且新名必须满足**既有用户名校验规则**（util.ValidateRestrictedName：中文/英文/
// 数字/下划线/@，长度 ≤ util.UsernameMaxLength rune）。
func TestUsernameReleaseResolveRenamesOnConflictAndStaysValid(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 151, "wangwu")
	archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonUserClosure)
	// 归档释放后别人注册了同名（这就是 Q4 说「恢复时冲突必然发生」的场景）。
	seedReleaseUser(t, db, 152, "wangwu")

	decision, err := ResolveRestoreUsernameWithDB(db, 151, "wangwu")
	if err != nil {
		t.Fatalf("conflict must be resolved by renaming, not by failing: %v", err)
	}
	if !decision.Renamed {
		t.Fatalf("decision must be flagged as renamed, got %+v", decision)
	}
	if decision.OccupiedByUserID != 152 {
		t.Fatalf("expected occupiedBy=152, got %d", decision.OccupiedByUserID)
	}
	if decision.Final == "wangwu" {
		t.Fatal("renamed result must differ from the occupied name")
	}
	// 必须符合既有用户名校验规则（与注册 / 改资料走同一个函数）。
	if err := ValidateUsernameCandidate(decision.Final); err != nil {
		t.Fatalf("generated name %q must pass the existing username rules: %v", decision.Final, err)
	}
	if err := util.ValidateRestrictedName(decision.Final, util.UsernameMaxLength, "用户名"); err != nil {
		t.Fatalf("generated name %q rejects the raw util rule: %v", decision.Final, err)
	}
	if !strings.HasPrefix(decision.Final, "wangwu"+UsernameRestoredMark) {
		t.Fatalf("generated name must be 原名_restored_N, got %q", decision.Final)
	}
	// 新名必须在库里确实可用（不是「生成就算」）。
	if occupied, err := UsernameOccupiedBy(db, decision.Final, 0); err != nil || occupied != 0 {
		t.Fatalf("generated name must be free in the DB, occupiedBy=%d err=%v", occupied, err)
	}
	if registrationAllowsUsername(t, db, decision.Final) != true {
		t.Fatal("generated name must be registrable")
	}
}

// TestUsernameReleaseResolveRetriesWhenCandidateCollides 候选名撞库必须重试，
// 而不是一次撞上就放弃。
//
// 用**注入**确定性地制造「第一次撞上」：把包级生成器换成「第 1 次返回一个已知
// 被占用的名字、之后回落到真实生成器」。若不注入，序号递增恰好不冲突，
// 「有没有重试」这件事根本测不出来。
func TestUsernameReleaseResolveRetriesWhenCandidateCollides(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 161, "zhaoliu")
	archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonUserClosure)
	seedReleaseUser(t, db, 162, "zhaoliu")
	// 故意让「第 1 个候选名」被别人占着 —— 真实候选名是 zhaoliu_restored_1。
	seedReleaseUser(t, db, 163, "zhaoliu_restored_1")

	original := usernameReleaseCandidate
	defer func() { usernameReleaseCandidate = original }()
	calls := 0
	usernameReleaseCandidate = func(name string, attempt int) string {
		calls++
		if attempt == 1 {
			// 第一次故意返回被占用的候选名（正是真实候选名，语义等价于「撞库」）。
			return "zhaoliu_restored_1"
		}
		return original(name, attempt)
	}

	decision, err := ResolveRestoreUsernameWithDB(db, 161, "zhaoliu")
	if err != nil {
		t.Fatalf("collision must be retried and then succeed: %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected at least 2 candidate attempts, got %d", calls)
	}
	if decision.Attempts != 2 {
		t.Fatalf("expected the second candidate to be accepted, attempts=%d", decision.Attempts)
	}
	if decision.Final != "zhaoliu_restored_2" {
		t.Fatalf("expected zhaoliu_restored_2 after skipping the occupied candidate, got %q", decision.Final)
	}
	if err := ValidateUsernameCandidate(decision.Final); err != nil {
		t.Fatalf("retried candidate must stay valid: %v", err)
	}
}

// TestUsernameReleaseResolveExhaustionLeavesNoPartialChange 重试耗尽 → 返回可判定
// 的错误（ErrUsernameReleaseExhausted），且**不留半改状态**。
//
// 「不留半改」的判据是：ResolveRestoreUsernameWithDB 是纯判定，失败后数据库
// 逐列未变。用逐列快照对比而不是「再读一次看 username」——
// 后者在「改了名又改回来」这类实现下会假绿。
//
// ⚠️ 本用例**刻意用字面量 10 而不是 UsernameReleaseMaxAttempts**，理由是被
// 第 4 类假钉子（默认值安全项）咬过一次：原版用常量做种子循环与断言，于是
// 把常量从 10 改成 1 时，种子只占 1 个候选名、断言也只期望 1 次尝试 ——
// 用例恒绿，常量大小完全没有被约束（实测 cap=1 变异时本用例不变红）。
//
// 用字面量的代价：改上限时必须同步改这里。这是**故意**的 ——
// 上限是产品决策，改它应当触发一次显式的测试审视，而不是静默跟随。
func TestUsernameReleaseResolveExhaustionLeavesNoPartialChange(t *testing.T) {
	// pinnedAttemptCap 是「本用例假设的重试上限」。
	//
	// 与实现里的 UsernameReleaseMaxAttempts 解耦：常量被改小时本用例会红
	// （因为种子按 10 个候选名铺满，而实现只试更少次数就提前返回），
	// 常量被改大时本用例也会红（因为第 11 个候选名没人占，实现会成功）。
	// 两个方向的漂移都能被抓住。
	const pinnedAttemptCap = 10
	if UsernameReleaseMaxAttempts != pinnedAttemptCap {
		t.Fatalf("retry cap changed to %d (test pins %d): update this case deliberately, "+
			"do not let a default-value change slip through",
			UsernameReleaseMaxAttempts, pinnedAttemptCap)
	}

	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 171, "sunqi")
	archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonUserClosure)
	seedReleaseUser(t, db, 172, "sunqi")
	// 占满 1..pinnedAttemptCap 全部候选名（用字面量，见上方注释）。
	for attempt := 1; attempt <= pinnedAttemptCap; attempt++ {
		seedReleaseUser(t, db, int32(1000+attempt), fmt.Sprintf("sunqi_restored_%d", attempt))
	}
	// 对照：候选名上限内的名字全部被占 ⇒ 若实现只试 1 次就放弃，
	// 下面断言 err 也会成立，但它无法证明「真的试满了」——
	// 因此另外断言 Attempts 等于上限。
	before := archivedRowSnapshot(t, db, 171)

	decision, err := ResolveRestoreUsernameWithDB(db, 171, "sunqi")
	if !errors.Is(err, ErrUsernameReleaseExhausted) {
		t.Fatalf("expected ErrUsernameReleaseExhausted, got err=%v decision=%+v", err, decision)
	}
	if decision.Attempts != pinnedAttemptCap {
		t.Fatalf("expected %d attempts before giving up, got %d", pinnedAttemptCap, decision.Attempts)
	}
	if decision.Final != "" {
		t.Fatalf("failed resolve must not hand out a candidate name, got %q", decision.Final)
	}
	if decision.Renamed {
		t.Fatalf("failed resolve must not claim a rename, got %+v", decision)
	}
	after := archivedRowSnapshot(t, db, 171)
	if before != after {
		t.Fatalf("failed resolve must not touch the row:\nbefore=%s\nafter =%s", before, after)
	}
	// 账号仍在归档状态，且原用户名仍是 sunqi（恢复可重试）。
	if !strings.Contains(after, "deleted=true") {
		t.Fatalf("account must stay archived after a failed restore, got %s", after)
	}
	if !strings.Contains(after, "username=sunqi") {
		t.Fatalf("username must be unchanged after a failed restore, got %s", after)
	}
}

// archivedRowSnapshot 把归档行拍成可比对的字符串（逐列，不含时间戳精度噪声）。
func archivedRowSnapshot(t *testing.T, db *gorm.DB, userID int32) string {
	t.Helper()
	var row entity.UserArchive
	if err := db.Unscoped().Where("id = ?", userID).Take(&row).Error; err != nil {
		t.Fatalf("read archive row %d: %v", userID, err)
	}
	var user entity.User
	if err := db.Unscoped().Select("id", "username", "status").Where("id = ?", userID).Take(&user).Error; err != nil {
		t.Fatalf("read user row %d: %v", userID, err)
	}
	return fmt.Sprintf("deleted=%v reason=%q by=%d username=%s status=%s",
		row.DeletedAt.Valid, row.ArchivedReason, row.ArchivedByUserID, user.Username, user.Status)
}

// TestUsernameReleaseResolveIgnoresSelfWhenUnscoped 排除自身：
// 占用判定必须排除被恢复账号自己，否则任何走 Unscoped 的实现都会「被自己占用」。
func TestUsernameReleaseResolveIgnoresSelfWhenUnscoped(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 181, "selfholder")
	archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonAdminDelete)

	// 直接验证 selfID 排除语义：把自身 ID 传进去后，即便库里有同名行也不该算占用。
	occupied, err := UsernameOccupiedBy(db, "selfholder", 181)
	if err != nil {
		t.Fatalf("probe occupancy: %v", err)
	}
	if occupied != 0 {
		t.Fatalf("self must not count as an occupant, got %d", occupied)
	}
	// 对照：selfID=0 时归档行仍不可见（软删除过滤），所以也是 0 ——
	// 这条对照证明「释放」来自软删除过滤而不是 selfID 排除的副作用。
	occupiedNoSelf, err := UsernameOccupiedBy(db, "selfholder", 0)
	if err != nil {
		t.Fatalf("probe occupancy without self: %v", err)
	}
	if occupiedNoSelf != 0 {
		t.Fatalf("archived row must not be visible to occupancy probes, got %d", occupiedNoSelf)
	}
}

// TestUsernameReleaseBuildRestoredNameStaysValid 生成规则必须对
// 「历史脏数据」也产出合法名：不符字符集的原名、超长原名、空原名。
//
// 为什么需要这组：用户名曾是**无校验**写入的（users 加校验之前的历史行），
// 直接拼接会产出「生成时合法、注册时非法」的名字 —— 那种名字一旦写进库，
// 用户改资料 / 再登录都会被校验挡下。
func TestUsernameReleaseBuildRestoredNameStaysValid(t *testing.T) {
	cases := []struct {
		name     string
		original string
	}{
		{"既有合法字符", "中文A9_@"},
		{"带连字符的历史名", "zhang-san"},
		{"带空格的历史名", "zhang san"},
		{"带点的历史名", "a.b.c"},
		{"超长原名", strings.Repeat("a", 255)},
		{"超长中文原名", strings.Repeat("张", 255)},
		{"空原名", ""},
		{"只有空白", "   "},
		{"只有非法字符", "---"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for attempt := 1; attempt <= UsernameReleaseMaxAttempts; attempt++ {
				candidate := BuildRestoredUsername(testCase.original, attempt)
				if utf8.RuneCountInString(candidate) > util.UsernameMaxLength {
					t.Fatalf("candidate %q exceeds %d runes (%d)", candidate, util.UsernameMaxLength,
						utf8.RuneCountInString(candidate))
				}
				if err := util.ValidateRestrictedName(candidate, util.UsernameMaxLength, "用户名"); err != nil {
					t.Fatalf("candidate %q violates the existing username rule: %v", candidate, err)
				}
				// 后缀必须保留：截掉后缀就变成「随便一个名字」，客服无法口述。
				wantSuffix := fmt.Sprintf("%s%d", UsernameRestoredMark, attempt)
				if !strings.HasSuffix(candidate, wantSuffix) {
					t.Fatalf("candidate %q must keep suffix %q", candidate, wantSuffix)
				}
			}
		})
	}
}

// TestUsernameReleaseBuildRestoredNameAttemptLowerBound attempt < 1 归一到 1（防御调用方传 0）：
// 否则会产出「原名_restored_0」这种没人预期过的序号。
func TestUsernameReleaseBuildRestoredNameAttemptLowerBound(t *testing.T) {
	if got := BuildRestoredUsername("zhangsan", 0); got != "zhangsan_restored_1" {
		t.Fatalf("attempt 0 must normalize to 1, got %q", got)
	}
	if got := BuildRestoredUsername("zhangsan", -5); got != "zhangsan_restored_1" {
		t.Fatalf("negative attempt must normalize to 1, got %q", got)
	}
}

// TestUsernameReleaseNextAvailableReportsAttempts 重试次数要能被上层看到
// （耗尽时用来区分「试满了」与「候选名非法」）。
func TestUsernameReleaseNextAvailableReportsAttempts(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	seedReleaseUser(t, db, 191, "retryuser")
	seedReleaseUser(t, db, 192, "retryuser_restored_1")
	seedReleaseUser(t, db, 193, "retryuser_restored_2")

	candidate, attempts, err := NextAvailableRestoredUsernameWithDB(db, "retryuser", 0)
	if err != nil {
		t.Fatalf("expected a free candidate: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts (two occupied), got %d", attempts)
	}
	if candidate != "retryuser_restored_3" {
		t.Fatalf("expected retryuser_restored_3, got %q", candidate)
	}
}

// TestUsernameReleaseValidateCandidateRejectsIllegalNames 校验函数本身必须真的会拒
// （否则上面所有「合法」断言都是恒真的空断言）。
func TestUsernameReleaseValidateCandidateRejectsIllegalNames(t *testing.T) {
	illegal := []string{
		"",
		"has space",
		"has-dash",
		"has.dot",
		strings.Repeat("a", util.UsernameMaxLength+1),
	}
	for _, name := range illegal {
		if err := ValidateUsernameCandidate(name); err == nil {
			t.Fatalf("ValidateUsernameCandidate must reject %q", name)
		} else if !errors.Is(err, ErrUsernameReleaseInvalid) {
			t.Fatalf("rejection of %q must wrap ErrUsernameReleaseInvalid, got %v", name, err)
		}
	}
	for _, name := range []string{"zhangsan", "中文A9_@", "user_restored_1", strings.Repeat("a", util.UsernameMaxLength)} {
		if err := ValidateUsernameCandidate(name); err != nil {
			t.Fatalf("ValidateUsernameCandidate must accept %q: %v", name, err)
		}
	}
}

// TestUsernameReleaseArchiveRejectsUnknownReason 归档原因非法时不得有任何写入
// （释放逻辑接进归档路径后，这条闸门仍然是第一步）。
func TestUsernameReleaseArchiveRejectsUnknownReason(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 201, "reason-guard")

	err := db.Transaction(func(tx *gorm.DB) error {
		return ArchiveUserInTx(tx, *user, "bogus_reason", 999)
	})
	if err == nil {
		t.Fatal("unknown archive reason must be rejected")
	}
	var archived entity.User
	if err := db.Unscoped().Where("id = ?", 201).Take(&archived).Error; err != nil {
		t.Fatalf("read user: %v", err)
	}
	if archived.DeletedAt.Valid {
		t.Fatal("rejected archive must not mark the user as deleted")
	}
	if rows := releaseAuditRows(t, db); len(rows) != 0 {
		t.Fatalf("rejected archive must not write a release log, got %d rows", len(rows))
	}
}

// TestUsernameReleaseArchiveKeepsLegacySignature 保留旧签名：既有两个调用点
// （controller/user.go 与 controller 测试）都只关心 err。
func TestUsernameReleaseArchiveKeepsLegacySignature(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 211, "legacy-sig")
	if err := db.Transaction(func(tx *gorm.DB) error {
		return ArchiveUserInTx(tx, *user, entity.UserArchivedReasonAdminDelete, 999)
	}); err != nil {
		t.Fatalf("legacy ArchiveUserInTx must keep working: %v", err)
	}
	if !registrationAllowsUsername(t, db, "legacy-sig") {
		t.Fatal("legacy entry point must release the username too (it delegates to the WithRelease variant)")
	}
}

// TestUsernameReleaseRenameRejectsOccupiedName 生成器若返回重复名（实现写错时会
// 发生），重试仍必须收敛到唯一可用名 —— 绝不能把「与占用者同名」的名字当成可用。
func TestUsernameReleaseRenameRejectsOccupiedName(t *testing.T) {
	db := newUsernameReleaseTestDB(t)
	user := seedReleaseUser(t, db, 221, "occupycheck")
	archiveThroughRealPath(t, db, *user, entity.UserArchivedReasonUserClosure)
	seedReleaseUser(t, db, 222, "occupycheck")
	seedReleaseUser(t, db, 223, "occupycheck_restored_1")

	original := usernameReleaseCandidate
	defer func() { usernameReleaseCandidate = original }()
	// 故意让前两次都返回已知被占用的名字，第三次才回落。
	usernameReleaseCandidate = func(name string, attempt int) string {
		if attempt <= 2 {
			return "occupycheck_restored_1"
		}
		return original(name, attempt)
	}

	decision, err := ResolveRestoreUsernameWithDB(db, 221, "occupycheck")
	if err != nil {
		t.Fatalf("expected convergence on a free candidate: %v", err)
	}
	if decision.Final != "occupycheck_restored_3" {
		t.Fatalf("expected occupycheck_restored_3, got %q", decision.Final)
	}
	if decision.Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", decision.Attempts)
	}
	if occupied, err := UsernameOccupiedBy(db, decision.Final, 0); err != nil || occupied != 0 {
		t.Fatalf("final name must be free, occupiedBy=%d err=%v", occupied, err)
	}
}
