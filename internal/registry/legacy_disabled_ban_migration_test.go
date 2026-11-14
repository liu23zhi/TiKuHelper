package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 历史禁用账号（users.status='disabled'）迁移成封禁记录（user_bans）。
//
// # 四条验收判据（任务书 ①–④）
//
//	① 迁移后每个 disabled 用户都有对应 ban 行
//	② 重复执行不产生重复行（幂等）
//	③ 正常用户不被误迁移
//	④ 原 users.status 未被改写
//
// # 每条判据「如果实现坏了会不会红」
//
//	① 把 status 过滤条件写错（比如漏掉 disabled）→ 红
//	② 去掉查重 / 去掉标记 → 第二次跑插入翻倍 → 红
//	③ 把 `= 'disabled'` 写成 `<> 'enabled'` → enabled 用户被扫进来 → 红
//	④ 迁移里顺手把 status 改成 banned → 红（这是任务书红线，必须钉死）
//
// 另外补两条边界：
//	⑤ 迁移出来的行是**永久封禁**（expires_at IS NULL）且 BannedBy=0（系统）
//	   —— 设成临时会让这批账号到点自动解封，是行为变更
//	⑥ 迁移动了 status='disabled' 以外的东西吗：closure_pending / banned / 空串
//	   都不得被迁移（空串在 isUserEnabled 里算「正常」，不能当成禁用）

// newLegacyDisabledBanTestDB 建一个含 users / user_bans / system_settings 的库。
func newLegacyDisabledBanTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db := newUserBansMigrationTestDB(t)
	if err := db.AutoMigrate(
		&entity.User{},
		&entity.UserBan{},
		&entity.SystemSetting{},
	); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	return db
}

// seedLegacyUser 插一个指定 status 的用户。
func seedLegacyUser(t *testing.T, db *gorm.DB, id int32, username, status string) entity.User {
	t.Helper()

	user := entity.User{
		ID:       id,
		Username: username,
		Password: "x",
		Role:     "user",
		Status:   status,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s(status=%q): %v", username, status, err)
	}
	return user
}

// banRowForUser 取某用户由本迁移插入的行（按来源前缀精确匹配）。
func banRowForUser(t *testing.T, db *gorm.DB, userID int32) *entity.UserBan {
	t.Helper()

	var rows []entity.UserBan
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("user_id = ?", userID).
		Where("reason = ?", legacyDisabledBanSource).
		Find(&rows).Error; err != nil {
		t.Fatalf("read ban rows for user %d: %v", userID, err)
	}
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// ① 迁移后每个 disabled 用户都有对应 ban 行。
func TestLegacyDisabledUsersGetBanRows(t *testing.T) {
	db := newLegacyDisabledBanTestDB(t)
	legacy := seedLegacyUser(t, db, 101, "legacy-disabled", "disabled")

	inserted, err := EnsureLegacyDisabledUsersMigratedToBan(db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("首次迁移应新增 1 条，实际 %d", inserted)
	}

	row := banRowForUser(t, db, legacy.ID)
	if row == nil {
		t.Fatal("disabled 用户必须有对应的封禁记录")
	}
	if row.UserID != legacy.ID {
		t.Fatalf("ban.user_id = %d, want %d", row.UserID, legacy.ID)
	}
	// 永久封禁：disabled 的既有语义就是「一直禁用直到管理员手动改」，
	// 迁成临时会让这批账号到点自动解封 —— 那是没人要求过的行为变更。
	if row.ExpiresAt != nil {
		t.Fatalf("迁移出来的封禁必须是永久的（expires_at IS NULL），实际 %v", *row.ExpiresAt)
	}
	// BannedBy=0 = 系统归因：迁移不是任何管理员的操作。
	if row.BannedBy != 0 {
		t.Fatalf("迁移行的 banned_by 必须是 0（系统），实际 %d", row.BannedBy)
	}
}

// ② 重复执行不产生重复行（幂等）。
//
// 这是任务书要求做变异验证的那一条。变异形态：把「幂等标记检查」删掉 →
// 第二次跑会再插一遍 → 行数翻倍 → 本用例红。
func TestLegacyDisabledBanMigrationIsIdempotent(t *testing.T) {
	db := newLegacyDisabledBanTestDB(t)
	seedLegacyUser(t, db, 201, "idem-a", "disabled")
	seedLegacyUser(t, db, 202, "idem-b", "disabled")

	first, err := EnsureLegacyDisabledUsersMigratedToBan(db)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first != 2 {
		t.Fatalf("首次应迁移 2 条，实际 %d", first)
	}

	// 第二次：标记已写，应当整体短路。
	second, err := EnsureLegacyDisabledUsersMigratedToBan(db)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second != 0 {
		t.Fatalf("第二次迁移不应新增任何行，实际 %d", second)
	}

	rows, err := legacyDisabledBanRows(db)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Fatalf("两次执行后总行数必须仍是 2，实际 %d（幂等失效）", rows)
	}

	// 第三次：显式再把标记删掉，模拟「标记写失败 / 迁移中途崩溃」后重跑。
	// 此时靠**查重**兜底（不能靠标记），仍然不得产生重复行。
	// 这一层是「唯一键双保险」的等价物：user_bans 没有唯一约束（也不该有，
	// 同一用户解封后再封禁是合法历史），所以查重是按来源精确判的。
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where(map[string]any{"key": legacyDisabledBanMigrationKey}).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("drop idempotency flag: %v", err)
	}
	third, err := EnsureLegacyDisabledUsersMigratedToBan(db)
	if err != nil {
		t.Fatalf("third run (no flag): %v", err)
	}
	if third != 0 {
		t.Fatalf("标记被删后重跑仍不得新增（应靠查重挡住），实际 %d", third)
	}
	rowsAfterThird, err := legacyDisabledBanRows(db)
	if err != nil {
		t.Fatalf("count rows after third: %v", err)
	}
	if rowsAfterThird != 2 {
		t.Fatalf("标记丢失后重跑不得产生重复行，实际总数 %d", rowsAfterThird)
	}
}

// ③ 正常用户不被误迁移。
func TestLegacyDisabledBanMigrationSkipsNonDisabledUsers(t *testing.T) {
	db := newLegacyDisabledBanTestDB(t)

	enabled := seedLegacyUser(t, db, 301, "normal", "enabled")
	closure := seedLegacyUser(t, db, 302, "closure", "closure_pending")
	alreadyBanned := seedLegacyUser(t, db, 303, "already-banned", "banned")
	blank := seedLegacyUser(t, db, 304, "blank-status", "")
	disabled := seedLegacyUser(t, db, 305, "really-disabled", "disabled")

	if _, err := EnsureLegacyDisabledUsersMigratedToBan(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 只有真正 disabled 的那一个被迁移。
	for _, user := range []entity.User{enabled, closure, alreadyBanned, blank} {
		if row := banRowForUser(t, db, user.ID); row != nil {
			t.Fatalf("status=%q 的用户（%s）不得被迁移，却出现了封禁行 id=%d",
				user.Status, user.Username, row.ID)
		}
	}
	if banRowForUser(t, db, disabled.ID) == nil {
		t.Fatal("真正 disabled 的用户必须被迁移")
	}

	// 补充：banned 用户已有封禁语义，迁移既不该给它插行，也不该动它的 status。
	var reloaded entity.User
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", alreadyBanned.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload banned user: %v", err)
	}
	if reloaded.Status != "banned" {
		t.Fatalf("已封禁用户的 status 不得被迁移改动，实际 %q", reloaded.Status)
	}
}

// ④ 原 users.status 未被改写（任务书红线）。
func TestLegacyDisabledBanMigrationKeepsOriginalStatus(t *testing.T) {
	db := newLegacyDisabledBanTestDB(t)
	legacy := seedLegacyUser(t, db, 401, "keep-status", "disabled")

	if _, err := EnsureLegacyDisabledUsersMigratedToBan(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var reloaded entity.User
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", legacy.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	// 保留 disabled 的理由：回滚只需删掉迁移插入的行（按 reason 定位），
	// 账号立刻回到迁移前状态；若改成 banned，回滚就得猜原值。
	if reloaded.Status != "disabled" {
		t.Fatalf("迁移不得改写 users.status（回滚依赖原值），实际 %q", reloaded.Status)
	}
	// 判定仍要成立：不靠 status，靠 user_bans 那一路（hasUnexpiredUserBan）。
	// 这里直接查库验证行的到期语义，避免把 service 层的双读逻辑复制一份。
	row := banRowForUser(t, db, legacy.ID)
	if row == nil || row.ExpiresAt != nil {
		t.Fatal("status 未改也必须有一条永久封禁行，否则迁移等于没做")
	}
}

// ⑥ 大小写与首尾空格归一：历史脏数据 ' DISABLED ' 也应被识别。
func TestLegacyDisabledBanMigrationNormalizesStatus(t *testing.T) {
	db := newLegacyDisabledBanTestDB(t)
	messy := seedLegacyUser(t, db, 501, "messy-status", " DISABLED ")

	if _, err := EnsureLegacyDisabledUsersMigratedToBan(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if banRowForUser(t, db, messy.ID) == nil {
		t.Fatal("大小写/空格变形的 disabled 必须被识别（历史脏数据）")
	}
}

// ⑧ 迁移的**目的**判据：迁移后封禁判定真的成立（靠 user_bans 那一路，不靠 status）。
//
// 前面 7 条都只证明「行插进去了」。行插进去但判定读不到，等于迁移白做 ——
// 而症状是「管理页看着有记录，实际那个人还能用」，静默且难查。
//
// 这一条走真实的 service.IsBanned（双读判定的唯一实现），不是自己再写一遍
// 「查 user_bans 有没有行」—— 那样的断言会在判定逻辑改动后仍绿。
func TestLegacyDisabledMigrationMakesIsBannedTrue(t *testing.T) {
	db := newLegacyDisabledBanTestDB(t)
	legacy := seedLegacyUser(t, db, 701, "is-banned-check", "disabled")

	// 迁移前：disabled **不是** banned（isUserEnabled 拦得住，但 IsBanned 判不出来）。
	before, err := service.IsBanned(db, legacy.ID)
	if err != nil {
		t.Fatalf("IsBanned before migration: %v", err)
	}
	if before {
		t.Fatal("迁移前 disabled 账号不应被判为封禁（它是「禁用」不是「封禁」）——" +
			"若这里为 true，说明判定把 disabled 也算封禁了，本用例失去意义")
	}

	if _, err := EnsureLegacyDisabledUsersMigratedToBan(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	after, err := service.IsBanned(db, legacy.ID)
	if err != nil {
		t.Fatalf("IsBanned after migration: %v", err)
	}
	// 这是迁移的**唯一业务价值**：账号进入封禁判定 + 出现在封禁管理页。
	if !after {
		t.Fatal("迁移后必须被判为封禁（否则只是往表里插了行，判定读不到 = 迁移白做）")
	}

	// 反向对照：没被迁移的正常用户不能被误判成封禁。
	// 没有这条，「IsBanned 恒返回 true」这种坏实现也能过上一句。
	normal := seedLegacyUser(t, db, 702, "normal-still-ok", "enabled")
	if banned, err := service.IsBanned(db, normal.ID); err != nil {
		t.Fatalf("IsBanned for normal user: %v", err)
	} else if banned {
		t.Fatal("正常用户不得被判为封禁")
	}
}

// ⑨ 迁移出的行是「永久」封禁：不能到点自动解封。
//
// 判据用 service.IsBannedAt 在**远期**再判一次。若实现误写成临时封禁
// （expires_at 非空），远期那一次就会 false —— 而那是没人要求过的行为变更
// （这批人本来是被一直禁用的）。
func TestLegacyDisabledMigrationBanNeverExpires(t *testing.T) {
	db := newLegacyDisabledBanTestDB(t)
	legacy := seedLegacyUser(t, db, 703, "never-expires", "disabled")

	if _, err := EnsureLegacyDisabledUsersMigratedToBan(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	farFuture := time.Now().AddDate(50, 0, 0)
	banned, err := service.IsBannedAt(db, legacy.ID, farFuture)
	if err != nil {
		t.Fatalf("IsBannedAt(50y): %v", err)
	}
	if !banned {
		t.Fatal("迁移出的封禁必须是永久的（50 年后仍成立）；若这里 false 说明写成了临时封禁")
	}
}

// ⑦ 迁移数量写进日志（运维唯一能确认「真的跑了、跑了几条」的锚点）。
func TestLegacyDisabledBanMigrationLogsCount(t *testing.T) {
	db := newLegacyDisabledBanTestDB(t)
	seedLegacyUser(t, db, 601, "log-a", "disabled")
	seedLegacyUser(t, db, 602, "log-b", "disabled")

	buffer := userBansLogCapture(t)
	if _, err := EnsureLegacyDisabledUsersMigratedToBan(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	output := buffer.String()
	if !strings.Contains(output, "历史禁用账号迁移：本次新增 2 条封禁记录") {
		t.Fatalf("迁移必须把新增条数写进日志（运维判据），实际日志：%s", output)
	}
}
