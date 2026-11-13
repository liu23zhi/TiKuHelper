package service

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 本文件钉死封禁判定的**双读**语义（121 决策 6，本项最容易漏的点）。
//
// # 三条核心判据为什么必须各是一个独立用例
//
//	① users.status='banned' 且**无** user_bans 记录 → 封禁
//	    抓的缺陷：只读 user_bans（存量封禁全部静默解封，上线即生效且不报错）
//	② users.status='enabled' 且 user_bans 有**未过期**记录 → 封禁
//	    抓的缺陷：只读 users.status（user_bans 写了完全没效果，表现「封禁点了没反应」）
//	③ users.status='enabled' 且 user_bans 只有**已过期**记录 → 不封禁
//	    抓的缺陷：读 user_bans 但漏掉到期条件（临时封禁到期后永远解不开）
//
// 三条分成三个 Test 而不是一个表驱动：变异验证要能看出「红的到底是哪一条」，
// 糊在一个用例里只能看到「某个子测试失败」，定位信息会差一层。
// 每个用例内部都**先断言前置条件**（状态值 / 记录数 / 到期方向），
// 否则数据造错时用例会以「判定结果不符」的假象失败，指向错误的方向。

// userBanDBSeq 保证每个用例拿到独立的内存库。
//
// `file:xxx?mode=memory&cache=shared` 同名库在同一进程内是共享的，
// t.Name() 在子测试间又可能重复 —— 不加序号会撞主键（与 admin_grant_test 同款坑）。
var userBanDBSeq atomic.Int64

// newUserBanTestDB 建一个带 users + user_bans + system_settings 的内存库。
//
// DSN 走 util.SQLiteConcurrentDSN：与生产 sqliteProductionDSN 共用同一份定义
// （`_txlock=immediate` + `busy_timeout(5000)`）。用默认 DSN 时，
// 凡「开事务再写另一张表」的路径会报 `database table is locked` ——
// 那测的是 SQLite 的默认锁行为，不是被测语义（见 task_doc/47）。
func newUserBanTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := util.SQLiteConcurrentDSN(
		"file:user-ban-" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
			"-" + strconv.FormatInt(userBanDBSeq.Add(1), 10) + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserBan{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// seedUserBanUser 造一个指定 status 的用户并返回 id。
func seedUserBanUser(t *testing.T, db *gorm.DB, username, status string) int32 {
	t.Helper()
	user := entity.User{Username: username, Status: status, Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user.ID
}

// reloadUserStatus 从库里读回 status（不信任内存里的 struct）。
func reloadUserStatus(t *testing.T, db *gorm.DB, userID int32) string {
	t.Helper()
	var status string
	if err := db.Model(&entity.User{}).Where("id = ?", userID).Pluck("status", &status).Error; err != nil {
		t.Fatalf("reload status of %d: %v", userID, err)
	}
	return status
}

// 判据 ①：users.status='banned' 且**没有** user_bans 记录 → 判定为封禁。
//
// 这条覆盖的是**存量封禁**：迁移不回填 user_bans 行（回填造不出 reason/banned_by，
// 只会产生假审计记录），因此「只读 user_bans」的实现会让上线瞬间静默解封所有人。
func TestUserBanDoubleReadStatusBannedWithoutUserBansRow(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "legacy-banned", "banned")

	// 前置条件：确实一条 user_bans 记录都没有。造错数据会让本用例以假象失败。
	var banRows int64
	if err := db.Model(&entity.UserBan{}).Count(&banRows).Error; err != nil {
		t.Fatalf("count user_bans: %v", err)
	}
	if banRows != 0 {
		t.Fatalf("precondition: expected zero user_bans rows, got %d", banRows)
	}
	if got := reloadUserStatus(t, db, userID); got != "banned" {
		t.Fatalf("precondition: users.status = %q, want \"banned\"", got)
	}

	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if !banned {
		t.Fatal("users.status='banned' with no user_bans row MUST be judged as banned (legacy bans must not be silently lifted)")
	}
}

// 判据 ②：users.status='enabled' 但 user_bans 有**未过期**记录 → 也判定为封禁。
//
// 这条覆盖的是「新封禁只写了记录表」的形态。只读 users.status 的实现会让
// user_bans 完全无效 —— 而它的表现是「管理页提示封禁成功，用户照常使用」。
func TestUserBanDoubleReadUnexpiredRowWithEnabledStatus(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "row-only", "enabled")
	now := time.Now()
	row := entity.UserBan{
		UserID:    userID,
		Reason:    "未过期封禁",
		BannedAt:  now.Add(-time.Hour),
		ExpiresAt: ptrTime(now.Add(time.Hour)),
		BannedBy:  1,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create ban row: %v", err)
	}
	// 前置条件：状态是 enabled、记录未过期。
	if got := reloadUserStatus(t, db, userID); got != "enabled" {
		t.Fatalf("precondition: users.status = %q, want \"enabled\"", got)
	}
	if !row.ExpiresAt.After(now) {
		t.Fatal("precondition: the ban row must still be unexpired")
	}

	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if !banned {
		t.Fatal("an unexpired user_bans row MUST be judged as banned even when users.status='enabled'")
	}

	// 永不过期（ExpiresAt 为空）同样算封禁 —— 用户 2026-11-10 定案两种并存。
	permanentID := seedUserBanUser(t, db, "permanent", "enabled")
	if err := db.Create(&entity.UserBan{UserID: permanentID, Reason: "永久封禁", BannedAt: now, BannedBy: 1}).Error; err != nil {
		t.Fatalf("create permanent ban row: %v", err)
	}
	banned, err = IsBanned(db, permanentID)
	if err != nil {
		t.Fatalf("IsBanned (permanent): %v", err)
	}
	if !banned {
		t.Fatal("a NULL expires_at row MUST be judged as banned (NULL = permanent)")
	}
}

// 判据 ③：users.status='enabled' 且 user_bans 只有**已过期**记录 → 不判定为封禁。
//
// 这条覆盖的是「到期自动解除」。漏掉到期条件的实现会让临时封禁变成永久封禁，
// 而且没有任何报错 —— 用户会一直收到「已被封禁」。
//
// 同时钉住边界：expires_at == now 视为**已到期**（严格大于），
// 因为 Unban 正是把 expires_at 置为 now。
func TestUserBanDoubleReadExpiredRowIsNotBanned(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "expired", "enabled")
	now := time.Now()
	row := entity.UserBan{
		UserID:    userID,
		Reason:    "已过期封禁",
		BannedAt:  now.Add(-2 * time.Hour),
		ExpiresAt: ptrTime(now.Add(-time.Minute)),
		BannedBy:  1,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create ban row: %v", err)
	}
	// 前置条件：记录确实存在（否则「不封禁」是空断言 —— 没有记录本来就该不封禁）。
	var banRows int64
	if err := db.Model(&entity.UserBan{}).Count(&banRows).Error; err != nil {
		t.Fatalf("count user_bans: %v", err)
	}
	if banRows != 1 {
		t.Fatalf("precondition: expected exactly one user_bans row, got %d", banRows)
	}
	if row.ExpiresAt.After(now) {
		t.Fatal("precondition: the ban row must already be expired")
	}

	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if banned {
		t.Fatal("an expired user_bans row MUST NOT be judged as banned (temporary bans must auto-lift)")
	}

	// 边界：expires_at 恰好等于 now → 已到期（严格大于）。
	boundaryID := seedUserBanUser(t, db, "boundary", "enabled")
	if err := db.Create(&entity.UserBan{UserID: boundaryID, Reason: "边界", BannedAt: now, ExpiresAt: ptrTime(now), BannedBy: 1}).Error; err != nil {
		t.Fatalf("create boundary row: %v", err)
	}
	banned, err = IsBannedAt(db, boundaryID, now)
	if err != nil {
		t.Fatalf("IsBannedAt (boundary): %v", err)
	}
	if banned {
		t.Fatal("expires_at == now MUST be treated as expired (the comparison is strictly greater-than)")
	}
}

// 存量封禁（决策 6 选 A：不回填）—— status=banned 且**完全没有 user_bans 行** → 仍然封禁。
//
// ⚠️ 这条断言的对象在 2026-11-14 被收窄过。原文是
// 「status=banned + 只有过期行 → 仍然封禁」，注释理由是「证明两路互相独立，
// 不是 user_bans 到期就把 status 也视为解除这种耦合实现」。
//
// 但那个版本把**一个真实生产缺陷当成了期望值**：
// 实测 `Ban(expiresAt=now+1h)` 之后 `IsBannedAt(now+2h)` 仍返回 true ——
// 即「临时封禁」实际是永久的。根因是 users.status 只在 Ban/Unban 两处被写，
// **自然到期没有任何复位机制**（全仓无到期清理任务）。
// 而 user_ban.go:384 的注释自己写着「提前置到期……与『到期自然失效』同一条路径」——
// 说明设计意图里**本来就有**自然失效这条路径，只是从未实现。
//
// 所以现在把两类场景分开：
//
//	存量（无 user_bans 行）      → 按 status 判封禁  ← 本条，保「两路独立」的意图
//	临时封禁到期（有行且全过期） → 视为已解除        ← TestUserBanTemporaryBanExpiresNaturally
//
// 这样既没有「status 被 user_bans 耦合」的问题（没有行时不看 user_bans），
// 也没有「临时封禁永久化」的缺陷。
func TestUserBanStatusWinsWhenNoBanRowsAtAll(t *testing.T) {
	db := newUserBanTestDB(t)
	// 决策 6 选 A 的存量用户：只有 status，没有任何 user_bans 行。
	userID := seedUserBanUser(t, db, "legacy-banned", "banned")

	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if !banned {
		t.Fatal("存量封禁（status=banned 且无任何 user_bans 行）必须判封禁 —— " +
			"决策 6 选 A 不回填，这批用户只有 status 这一个信息源")
	}
}

// 临时封禁**自然到期**后必须解除 —— 这是 2026-11-14 修掉的真实缺陷的回归钉。
//
// 修前实测：Ban(expiresAt=now+1h) → IsBannedAt(now+2h) == true，users.status 仍是 banned。
// 运营设 1 小时临时封禁 = 实际永久，界面上写着到期时间，判定永不到期，且不报错。
func TestUserBanTemporaryBanExpiresNaturally(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "temp-ban", "enabled")
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)

	if _, err := Ban(db, UserBanRequest{
		UserID: userID, Reason: "临时封禁 1 小时", ExpiresAt: &expires, OperatorUserID: 1,
	}); err != nil {
		t.Fatalf("Ban: %v", err)
	}

	// 封禁期间：必须 true（防止实现无条件返回 false）
	during, err := IsBannedAt(db, userID, now)
	if err != nil {
		t.Fatalf("IsBannedAt(during): %v", err)
	}
	if !during {
		t.Fatal("封禁期间必须判封禁")
	}

	// 到期后：必须 false。这一条就是修前不成立的那条。
	after, err := IsBannedAt(db, userID, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("IsBannedAt(after): %v", err)
	}
	if after {
		t.Fatal("临时封禁在到期 2 小时后仍判封禁 —— 「临时封禁」实际是永久的，这是真实生产缺陷")
	}

	// user_bans 行必须保留（到期不等于删记录，审计要能查到当初封过）。
	var rows []entity.UserBan
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("到期后 user_bans 行必须保留（审计需要），期望 1 行，实际 %d", len(rows))
	}
}

// 永久封禁（expires_at IS NULL）不受到期逻辑影响 —— 对照组。
func TestUserBanPermanentBanNeverExpires(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "perm-ban", "enabled")
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

	if _, err := Ban(db, UserBanRequest{
		UserID: userID, Reason: "永久封禁", ExpiresAt: nil, OperatorUserID: 1,
	}); err != nil {
		t.Fatalf("Ban: %v", err)
	}

	// 十年后仍然是封禁。
	after, err := IsBannedAt(db, userID, now.AddDate(10, 0, 0))
	if err != nil {
		t.Fatalf("IsBannedAt: %v", err)
	}
	if !after {
		t.Fatal("永久封禁（expires_at IS NULL）十年后仍必须判封禁")
	}
}

// 未封禁的干净用户必须判为未封禁（防止实现写死 true）。
func TestUserBanCleanUserIsNotBanned(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "clean", "enabled")

	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if banned {
		t.Fatal("a user with status='enabled' and no ban rows MUST NOT be judged as banned")
	}
}

// user_bans 表不存在（迁移未登记）：降级为只读 users.status，不报错、不 fail-closed。
//
// 判据：status=banned 仍判封禁（存量封禁不受迁移缺失影响），
// status=enabled 判未封禁（不能因为表缺失就把全站判成封禁）。
func TestUserBanDegradesToStatusWhenTableMissing(t *testing.T) {
	db := newUserBanTestDB(t)
	if err := db.Migrator().DropTable(entity.TableNameUserBan); err != nil {
		t.Fatalf("drop user_bans: %v", err)
	}
	bannedID := seedUserBanUser(t, db, "no-table-banned", "banned")
	enabledID := seedUserBanUser(t, db, "no-table-enabled", "enabled")

	if !db.Migrator().HasTable(entity.TableNameUserBan) {
		// 前置条件成立。
	} else {
		t.Fatal("precondition: user_bans must be gone")
	}

	banned, err := IsBanned(db, bannedID)
	if err != nil {
		t.Fatalf("IsBanned (banned): %v", err)
	}
	if !banned {
		t.Fatal("without user_bans, users.status='banned' must still be judged as banned")
	}
	banned, err = IsBanned(db, enabledID)
	if err != nil {
		t.Fatalf("IsBanned (enabled): %v", err)
	}
	if banned {
		t.Fatal("without user_bans, an enabled user must NOT be judged as banned (degrade must not fail closed)")
	}
}

// 无效 userID：不查库、不报错、判未封禁（匿名请求会走到这里）。
func TestUserBanInvalidUserIDIsNotBanned(t *testing.T) {
	db := newUserBanTestDB(t)
	for _, userID := range []int32{0, -1} {
		banned, err := IsBanned(db, userID)
		if err != nil {
			t.Fatalf("IsBanned(%d): %v", userID, err)
		}
		if banned {
			t.Fatalf("IsBanned(%d) must be false", userID)
		}
	}
}

// nil db 必须返回错误而不是 panic，也不是「未封禁」。
//
// 判据：调用方不能把「读不到」当成「没封禁」—— 那会让准入判定在库挂掉时静默放行。
func TestUserBanNilDBReturnsError(t *testing.T) {
	var db *gorm.DB
	banned, err := IsBanned(db, 1)
	if err == nil {
		t.Fatal("nil db must return an error, not a silent false")
	}
	if banned {
		t.Fatal("nil db must not report banned")
	}
	if !errors.Is(err, ErrUserBanDBUnavailable) {
		t.Fatalf("expected ErrUserBanDBUnavailable, got %v", err)
	}
}

// Ban 必须**两处都写**：user_bans 一行 + users.status='banned'。
//
// 只写一处时，双读判定仍能正确工作（这正是双读的价值），但运营界面会矛盾：
// 用户列表显示「正常」而判定说封禁，或反之。因此这条独立断言两处。
func TestUserBanBanWritesBothPlaces(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "to-ban", "enabled")
	future := time.Now().Add(2 * time.Hour)

	record, err := Ban(db, UserBanRequest{UserID: userID, Reason: "违规刷单", ExpiresAt: &future, OperatorUserID: 9})
	if err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if record.ID == 0 {
		t.Fatal("Ban must return the persisted row (with ID)")
	}
	if got := reloadUserStatus(t, db, userID); got != "banned" {
		t.Fatalf("users.status = %q, want \"banned\" (Ban must write both places)", got)
	}

	var rows []entity.UserBan
	if err := db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		t.Fatalf("load ban rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one user_bans row, got %d", len(rows))
	}
	// 原因 / 操作者 / 到期时间必须是**字面值**，不能拿 req 回比（自我指涉）。
	if rows[0].Reason != "违规刷单" {
		t.Fatalf("reason = %q, want \"违规刷单\"", rows[0].Reason)
	}
	if rows[0].BannedBy != 9 {
		t.Fatalf("banned_by = %d, want 9", rows[0].BannedBy)
	}
	if rows[0].ExpiresAt == nil || !rows[0].ExpiresAt.Equal(future) {
		t.Fatalf("expires_at = %v, want %v", rows[0].ExpiresAt, future)
	}

	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if !banned {
		t.Fatal("the user must be banned right after Ban")
	}
}

// Ban 的入参校验：空原因必须拒绝（不静默补默认值），过去的时间必须拒绝。
func TestUserBanBanValidatesInput(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "validate", "enabled")
	past := time.Now().Add(-time.Minute)

	cases := []struct {
		name string
		req  UserBanRequest
	}{
		{"empty reason", UserBanRequest{UserID: userID, Reason: "   "}},
		{"expires in the past", UserBanRequest{UserID: userID, Reason: "原因", ExpiresAt: &past}},
		{"zero user id", UserBanRequest{UserID: 0, Reason: "原因"}},
		{"unknown user", UserBanRequest{UserID: 999999, Reason: "原因"}},
	}
	for _, testCase := range cases {
		_, err := Ban(db, testCase.req)
		if err == nil {
			t.Fatalf("%s: expected an error", testCase.name)
		}
	}
	// 校验失败不能留下任何副作用。
	if got := reloadUserStatus(t, db, userID); got != "enabled" {
		t.Fatalf("rejected Ban must not change users.status, got %q", got)
	}
	var banRows int64
	if err := db.Model(&entity.UserBan{}).Count(&banRows).Error; err != nil {
		t.Fatalf("count user_bans: %v", err)
	}
	if banRows != 0 {
		t.Fatalf("rejected Ban must not write user_bans rows, got %d", banRows)
	}
}

// 连续两次 Ban：更新同一行，不插第二行（幂等）。
//
// 断言「行数仍为 1」而不是「没有报错」—— 后者在插了第二行时同样成立。
func TestUserBanBanIsIdempotentForUnexpiredRow(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "twice", "enabled")
	now := time.Now()

	first, err := Ban(db, UserBanRequest{UserID: userID, Reason: "第一次", OperatorUserID: 1, Now: now})
	if err != nil {
		t.Fatalf("first Ban: %v", err)
	}
	second, err := Ban(db, UserBanRequest{UserID: userID, Reason: "第二次（改为永久）", OperatorUserID: 2, Now: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("second Ban: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("a second Ban on an unexpired ban must update the same row: %d vs %d", first.ID, second.ID)
	}
	var rows []entity.UserBan
	if err := db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		t.Fatalf("load rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one row after two Bans, got %d", len(rows))
	}
	if rows[0].Reason != "第二次（改为永久）" {
		t.Fatalf("reason = %q, want the second one", rows[0].Reason)
	}
	// 第二次没传 ExpiresAt = 改为永久：可空列必须真的被置 NULL。
	if rows[0].ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want NULL (struct-based updates would skip this)", rows[0].ExpiresAt)
	}
	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if !banned {
		t.Fatal("a permanent ban must still be in effect")
	}
}

// Unban 必须让判定立刻失效，同时**不删行**（封禁是审计事实）。
//
// 断言「记录仍在 + expires_at 已到期」，而不是「行没了」。
func TestUserBanUnbanKeepsRowAndLiftsBan(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "unban", "enabled")
	future := time.Now().Add(3 * time.Hour)
	if _, err := Ban(db, UserBanRequest{UserID: userID, Reason: "临时封禁", ExpiresAt: &future, OperatorUserID: 1}); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if banned, err := IsBanned(db, userID); err != nil || !banned {
		t.Fatalf("precondition: user must be banned before Unban (banned=%v err=%v)", banned, err)
	}

	if err := Unban(db, UserUnbanRequest{UserID: userID, OperatorUserID: 3, Reason: "申诉通过"}); err != nil {
		t.Fatalf("Unban: %v", err)
	}

	var rows []entity.UserBan
	if err := db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		t.Fatalf("load rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Unban must keep the audit row, got %d rows", len(rows))
	}
	if rows[0].ExpiresAt == nil {
		t.Fatal("Unban must set expires_at (a NULL would mean permanent, i.e. still banned)")
	}
	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned after Unban: %v", err)
	}
	if banned {
		t.Fatal("the user must not be banned after Unban")
	}
	if got := reloadUserStatus(t, db, userID); got != "enabled" {
		t.Fatalf("users.status = %q, want \"enabled\" after Unban", got)
	}
}

// Unban 在**两条路同时命中**时必须两路都解，且 status 复位要能被独立观测到。
//
// # 这条用例补的是什么防线（不是补实现，是补测试自身的抗退化能力）
//
// 「两路都解」原本已由 TestUserBanUnbanKeepsRowAndLiftsBan 覆盖，但那是**单点覆盖**：
// 它唯一的核心断言是 IsBanned 的返回值，而 `Unban` 复位 status 的那段（user_ban.go:386-393）
// 一旦被短路（`if false && ...`），该用例会在 IsBanned 那句先红、根本走不到后面的
// status 断言 —— 也就是说 status 复位这条路径**实际上只有「IsBanned 红」这一个间接证据**。
//
// 危险在于：将来谁把 UnbanKeepsRowAndLiftsBan 的 IsBanned 断言改弱（例如换成直接读
// status 列），status 复位就再也没有任何用例能抓住。本用例直接读列，
// 与 IsBanned 解耦 —— 抓的是「实现会坏」，也抓「测试会被改弱」。
//
// # 断言顺序是刻意的
//
// ① status 字面量 → ② expires_at → ③ IsBanned。把 status 放第一位，
// 是因为 IsBanned 是**两路的合取**：只要任一路残留它就会红，红那句无法指出是哪一路。
// 先断两路各自的直接证据，失败信息才指向具体路径，而不是退化成
// 「反正封禁还在」。这与本仓记过的「变异红在对照组前置断言、诊断信息错位」是同一类问题。
//
// # 全部断言写字面量
//
// 期望值一律写 "enabled" / "banned" / IsBanned 的布尔，**不引用 UserBanStatusEnabled 等常量**：
// 用常量比常量是自我指涉断言，常量漂移时期望值同步漂移、恒真（本仓第 4 类假钉子）。
func TestUserBanUnbanResetsStatusLiteralWhenBothPathsActive(t *testing.T) {
	db := newUserBanTestDB(t)
	// 造「两路同时命中」的状态：status 字面量 banned + 一条未过期 user_bans 行。
	userID := seedUserBanUser(t, db, "both-paths", "banned")
	startedAt := time.Now()
	row := entity.UserBan{
		UserID:    userID,
		Reason:    "两路同时命中",
		BannedAt:  startedAt.Add(-time.Hour),
		ExpiresAt: ptrTime(startedAt.Add(2 * time.Hour)),
		BannedBy:  1,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create ban row: %v", err)
	}

	// 前置条件必须显式断言：任一条不成立，本用例就退化成「只测一路」而不自知。
	if got := reloadUserStatus(t, db, userID); got != "banned" {
		t.Fatalf("precondition: users.status = %q, want \"banned\"", got)
	}
	if !row.ExpiresAt.After(startedAt) {
		t.Fatal("precondition: the ban row must be unexpired before Unban")
	}
	if banned, err := IsBanned(db, userID); err != nil || !banned {
		t.Fatalf("precondition: user must be banned before Unban (banned=%v err=%v)", banned, err)
	}

	unbanAt := time.Now()
	if err := Unban(db, UserUnbanRequest{
		UserID: userID, OperatorUserID: 4, Reason: "两路解封", Now: unbanAt,
	}); err != nil {
		t.Fatalf("Unban: %v", err)
	}

	// ① 直接读 users.status 列。这是本用例新增的防线：不经 IsBanned，
	//    因此「只解除 user_bans 行、不复位 status」的实现会在这里立刻红。
	if got := reloadUserStatus(t, db, userID); got != "enabled" {
		t.Fatalf("users.status = %q, want \"enabled\" (Unban must reset the status path too, not just the row)", got)
	}

	// ② 直接读 user_bans.expires_at 列：必须已被提前置到期，且行仍然存在（不删行）。
	var rows []entity.UserBan
	if err := db.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		t.Fatalf("load ban rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Unban must keep exactly one audit row, got %d", len(rows))
	}
	if rows[0].ExpiresAt == nil {
		t.Fatal("expires_at must not be NULL after Unban (NULL means permanent, i.e. still banned)")
	}
	if rows[0].ExpiresAt.After(unbanAt) {
		t.Fatalf("expires_at = %v, want <= %v (Unban must expire the row)", rows[0].ExpiresAt, unbanAt)
	}

	// ③ 业务判定层：两路都解之后，双读必须一起返回未封禁。
	if banned, err := IsBanned(db, userID); err != nil {
		t.Fatalf("IsBanned after Unban: %v", err)
	} else if banned {
		t.Fatal("the user must not be banned after Unban (both paths must be cleared)")
	}
}

// Unban 不得覆盖非 banned 的其它状态（closure_pending / disabled）。
//
// 无条件写 'enabled' 会把账号从注销流程里拽出来 —— 静默的状态丢失。
func TestUserBanUnbanDoesNotOverwriteOtherStatus(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "closure", "closure_pending")
	now := time.Now()
	if err := db.Create(&entity.UserBan{
		UserID: userID, Reason: "临时封禁", BannedAt: now.Add(-time.Hour),
		ExpiresAt: ptrTime(now.Add(time.Hour)), BannedBy: 1,
	}).Error; err != nil {
		t.Fatalf("create ban row: %v", err)
	}

	if err := Unban(db, UserUnbanRequest{UserID: userID, OperatorUserID: 3}); err != nil {
		t.Fatalf("Unban: %v", err)
	}
	if got := reloadUserStatus(t, db, userID); got != "closure_pending" {
		t.Fatalf("users.status = %q, want \"closure_pending\" untouched", got)
	}
	if banned, err := IsBanned(db, userID); err != nil || banned {
		t.Fatalf("the unexpired row must be expired by Unban (banned=%v err=%v)", banned, err)
	}
}

// Unban 幂等：对未封禁用户重复解封不报错、不改状态。
func TestUserBanUnbanIsIdempotent(t *testing.T) {
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "idempotent", "enabled")
	for attempt := 1; attempt <= 2; attempt++ {
		if err := Unban(db, UserUnbanRequest{UserID: userID, OperatorUserID: 1}); err != nil {
			t.Fatalf("Unban attempt %d: %v", attempt, err)
		}
	}
	if got := reloadUserStatus(t, db, userID); got != "enabled" {
		t.Fatalf("status = %q, want \"enabled\"", got)
	}
	var banRows int64
	if err := db.Model(&entity.UserBan{}).Count(&banRows).Error; err != nil {
		t.Fatalf("count user_bans: %v", err)
	}
	if banRows != 0 {
		t.Fatalf("Unban must not create rows, got %d", banRows)
	}
}

// 常量值必须与既有的 'banned' 字面量逐字节相同。
//
// ⚠️ 期望值写**字面量**，不写 controller/userStatusBanned 之类的常量：
// 拿常量比常量是自我指涉断言（两边一起漂移、恒真）。
// 本仓已有两处独立的 banned 常量（controller/closure.go:61、
// service/admin_grant.go:75），值是接口的一部分，写错会让
// 「封禁写了但准入谓词读不到」。
func TestUserBanStatusConstantsMatchLiteralValues(t *testing.T) {
	if UserBanStatusBanned != "banned" {
		t.Fatalf("UserBanStatusBanned = %q, want \"banned\"", UserBanStatusBanned)
	}
	if UserBanStatusEnabled != "enabled" {
		t.Fatalf("UserBanStatusEnabled = %q, want \"enabled\"", UserBanStatusEnabled)
	}
	// 大小写无关比较是判定契约的一部分（历史数据里出现过 'BANNED'）。
	db := newUserBanTestDB(t)
	userID := seedUserBanUser(t, db, "upper", "BANNED")
	banned, err := IsBanned(db, userID)
	if err != nil {
		t.Fatalf("IsBanned: %v", err)
	}
	if !banned {
		t.Fatal("users.status comparison must be case-insensitive ('BANNED' counts as banned)")
	}
}

// 表名常量必须是 user_bans（迁移、entity、service 三处共用这一个出处）。
func TestUserBanTableNameConstant(t *testing.T) {
	if entity.TableNameUserBan != "user_bans" {
		t.Fatalf("TableNameUserBan = %q, want \"user_bans\"", entity.TableNameUserBan)
	}
}

// 日志捕获只为确保 Ban/Unban 不因 logger 未初始化而 panic（logger 内部写 gin.DefaultWriter）。
func TestUserBanOperationsLogWithoutPanic(t *testing.T) {
	db := newUserBanTestDB(t)
	oldInfo, oldError := gin.DefaultWriter, gin.DefaultErrorWriter
	buffer := &bytes.Buffer{}
	gin.DefaultWriter, gin.DefaultErrorWriter = buffer, buffer
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = oldInfo, oldError })

	userID := seedUserBanUser(t, db, "logged", "enabled")
	if _, err := Ban(db, UserBanRequest{UserID: userID, Reason: "留痕", OperatorUserID: 1}); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if err := Unban(db, UserUnbanRequest{UserID: userID, OperatorUserID: 1, Reason: "解除"}); err != nil {
		t.Fatalf("Unban: %v", err)
	}
	if !strings.Contains(buffer.String(), "封禁") && !strings.Contains(buffer.String(), "解除") {
		t.Fatalf("expected audit log lines, got:\n%s", buffer.String())
	}
}

// ptrTime 是取 *time.Time 的小工具（多处用例需要造到期时间）。
func ptrTime(value time.Time) *time.Time {
	return &value
}
