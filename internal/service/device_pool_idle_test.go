package service

// 10 天闲置自动解绑的验收用例（137 §7，判据 D10 / D11）。
//
// ⚠️ 时间基准的口径（本仓踩过的坑）：用例一律用**显式传入的 now**，
// 绝不用真实 time.Now() 与硬编码时刻比较 —— 那种写法写完当天绿、
// 几小时后自己变红，看起来像随机失败。
// RunDeviceIdleRevokeOnce 的签名刻意接受 now 参数，就是为了让这条边界可测。

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// idleTestNow 是一个固定的「当前时刻」基准。
// 它只作为**同一函数调用的输入**，不参与任何「是否在未来」的判断，
// 因此不存在「硬编码时刻过期」问题。
var idleTestNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// seedBindingWithLastSeen 直接写入一条绑定行，指定 last_seen_at 与撤销状态。
func seedBindingWithLastSeen(t *testing.T, db *gorm.DB, userID int32, deviceID string, lastSeen time.Time, revoked bool, reason string) {
	t.Helper()
	row := entity.AccountDeviceBinding{
		UserID:      userID,
		DeviceID:    deviceID,
		FirstSeenAt: lastSeen,
		LastSeenAt:  lastSeen,
		OwnerKind:   entity.DeviceBindingOwnerKindLogin,
	}
	if revoked {
		at := lastSeen
		row.RevokedAt = &at
		row.RevokedReason = reason
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed binding %s: %v", deviceID, err)
	}
}

// readBindingRevokeState 读回某行的撤销状态。
func readBindingRevokeState(t *testing.T, db *gorm.DB, userID int32, deviceID string) (revokedAt *time.Time, reason string) {
	t.Helper()
	var row struct {
		RevokedAt     *time.Time `gorm:"column:revoked_at"`
		RevokedReason string     `gorm:"column:revoked_reason"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("revoked_at AS revoked_at, revoked_reason AS revoked_reason").
		Where("user_id = ? AND device_id = ?", userID, deviceID).Take(&row).Error; err != nil {
		t.Fatalf("read binding %s: %v", deviceID, err)
	}
	return row.RevokedAt, row.RevokedReason
}

// 判据 D10：last_seen_at 早于 now-240h 的行被撤销，原因 idle_10d。
//
// 变异方向「阈值 240h 改成 240 分钟」→ 本用例必红：
// 240 分钟 = 4 小时，于是「9 天前活跃」的行不再被撤销、「2 小时前活跃」的行反而被撤销，
// 两条断言同时失败（见下方两个对照组，它们保证用例不是只测了一个方向）。
func TestDeviceIdleRevokeMarksStaleBindings(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 3001, 0)

	// stale：10 天零 1 小时前活跃 ⇒ 必须撤销。
	stale := idleTestNow.Add(-(DeviceIdleRevokeAfter + time.Hour))
	seedBindingWithLastSeen(t, db, 3001, "idle-stale", stale, false, "")
	// fresh：9 天前活跃 ⇒ 不撤销（10 天还没到）。
	fresh := idleTestNow.Add(-(9 * 24 * time.Hour))
	seedBindingWithLastSeen(t, db, 3001, "idle-fresh", fresh, false, "")
	// boundary：恰好 240 小时的**下一毫秒**更早（即 last_seen = cutoff - 1s）⇒ 撤销；
	// 恰好等于 cutoff ⇒ 按 `<` 不撤销（严格小于）。
	justBefore := idleTestNow.Add(-DeviceIdleRevokeAfter).Add(-time.Second)
	seedBindingWithLastSeen(t, db, 3001, "idle-just-before", justBefore, false, "")

	processed, err := RunDeviceIdleRevokeOnce(db, idleTestNow)
	if err != nil {
		t.Fatalf("闲置解绑不得返回错误：%v", err)
	}
	if processed != 2 {
		t.Fatalf("本轮应撤销 2 行（stale + just-before），实际 %d", processed)
	}

	// stale 被撤销且原因是 idle_10d。
	revokedAt, reason := readBindingRevokeState(t, db, 3001, "idle-stale")
	if revokedAt == nil {
		t.Fatal("10 天未活跃的绑定必须被撤销")
	}
	if reason != "idle_10d" {
		t.Fatalf("撤销原因必须是 idle_10d（稳定字符串，前端/审计依赖它），实际 %q", reason)
	}
	if !revokedAt.Equal(idleTestNow) {
		t.Fatalf("revoked_at 必须写成传入的 now（可测性依赖它），期望 %v 实际 %v", idleTestNow, revokedAt)
	}

	// fresh 不动 —— 这条是对照组：没有它，「无条件撤销所有行」也能通过上面的断言。
	if revokedAt, _ := readBindingRevokeState(t, db, 3001, "idle-fresh"); revokedAt != nil {
		t.Fatalf("9 天前活跃的行不该被撤销（阈值是 240h 不是更小），实际 revoked_at=%v", revokedAt)
	}

	// 行不删（137 §3.2：撤销而非删除）。
	if all, err := CountAllDevices(db, 3001); err != nil || all != 3 {
		t.Fatalf("撤销不删行：期望仍 3 行，实际 %d（err=%v）", all, err)
	}
	// 槽位口径随之变化：3 行里只有 fresh 仍占槽。
	if used, err := CountDevices(db, 3001); err != nil || used != 1 {
		t.Fatalf("撤销后占槽数应为 1（只有 fresh），实际 %d（err=%v）", used, err)
	}
}

// 判据 D10 的另一半：阈值必须是 240 小时 —— 「4 小时前活跃」的行**不许**被撤销。
//
// 这条专门抓「240h 写成 240 分钟」这个变异：240 分钟 = 4 小时，
// 于是 `idle-4h` 会落入 cutoff 而被撤销，本用例立刻红。
func TestDeviceIdleRevokeKeepsRecentlyActiveBindings(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 3002, 0)

	for i, age := range []time.Duration{
		0,
		time.Minute,
		4 * time.Hour,       // ← 变异「240 分钟」会把它错杀
		23 * time.Hour,      // ← 变异「按天取整」也可能错杀
		24 * time.Hour,      // 1 天
		239 * time.Hour,     // 差 1 小时到 10 天，仍不该解绑
	} {
		seedBindingWithLastSeen(t, db, 3002, "recent-"+formatInt64(int64(i)), idleTestNow.Add(-age), false, "")
	}

	processed, err := RunDeviceIdleRevokeOnce(db, idleTestNow)
	if err != nil {
		t.Fatalf("闲置解绑不得返回错误：%v", err)
	}
	if processed != 0 {
		t.Fatalf("最近活跃的 6 台设备一台都不该被撤销，实际处理 %d 行（阈值被改小了？）", processed)
	}
	if used, err := CountDevices(db, 3002); err != nil || used != 6 {
		t.Fatalf("6 台仍应全部占槽：used=%d err=%v", used, err)
	}
}

// 幂等（137 §7 判据）：第二次跑同一批数据返回 0。
func TestDeviceIdleRevokeIsIdempotent(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 3003, 0)
	seedBindingWithLastSeen(t, db, 3003, "idem-a", idleTestNow.Add(-300*time.Hour), false, "")
	seedBindingWithLastSeen(t, db, 3003, "idem-b", idleTestNow.Add(-400*time.Hour), false, "")

	first, err := RunDeviceIdleRevokeOnce(db, idleTestNow)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first != 2 {
		t.Fatalf("首轮应处理 2 行，实际 %d", first)
	}
	second, err := RunDeviceIdleRevokeOnce(db, idleTestNow)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second != 0 {
		t.Fatalf("第二轮必须返回 0（幂等锚点是 revoked_at IS NULL），实际 %d", second)
	}
	// 第三轮同样 0，且时间戳没被推进（若条件里漏了 revoked_at IS NULL，
	// 第二轮就会把 revoked_at 写成更晚的值 —— 表现为「明明 10 天前就该解绑，
	// 时间却一直变新」）。
	third, err := RunDeviceIdleRevokeOnce(db, idleTestNow.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if third != 0 {
		t.Fatalf("第三轮（时间推进 48h）必须仍返回 0，实际 %d", third)
	}
	revokedAt, _ := readBindingRevokeState(t, db, 3003, "idem-a")
	if revokedAt == nil || !revokedAt.Equal(idleTestNow) {
		t.Fatalf("revoked_at 不该被后续轮次改写：期望 %v 实际 %v", idleTestNow, revokedAt)
	}
}

// 已撤销的行（任何原因）一律不重复处理：user_removed 的痕迹不该被改成 idle_10d。
//
// 若条件里只有 last_seen_at，用户手动移除的记录会被后台任务覆盖成 idle_10d，
// 于是「用户自己移除的」与「系统自动解绑的」在审计上无法区分。
func TestDeviceIdleRevokeSkipsAlreadyRevokedRows(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 3004, 0)
	old := idleTestNow.Add(-500 * time.Hour)
	seedBindingWithLastSeen(t, db, 3004, "user-removed", old, true, entity.DeviceBindingRevokeReasonUserRemoved)

	processed, err := RunDeviceIdleRevokeOnce(db, idleTestNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if processed != 0 {
		t.Fatalf("已撤销的行不得重复处理，实际 %d", processed)
	}
	_, reason := readBindingRevokeState(t, db, 3004, "user-removed")
	if reason != "user_removed" {
		t.Fatalf("既有撤销原因不得被覆盖成 idle_10d，实际 %q", reason)
	}
}

// 分批 500（137 §7）：600 行候选必须一次调用全部处理完（循环到扫空）。
//
// 这条防的是「只处理第一批就返回」—— 那种实现在数据量大时表现为
// 「10 天解绑永远只解绑前 500 台」，且不报错。
func TestDeviceIdleRevokeProcessesAllBatches(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 3005, 0)
	old := idleTestNow.Add(-500 * time.Hour)

	const total = 600
	rows := make([]entity.AccountDeviceBinding, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, entity.AccountDeviceBinding{
			UserID:      3005,
			DeviceID:    "batch-dev-" + formatInt64(int64(i)),
			FirstSeenAt: old,
			LastSeenAt:  old,
			OwnerKind:   entity.DeviceBindingOwnerKindLogin,
		})
	}
	if err := db.CreateInBatches(&rows, 100).Error; err != nil {
		t.Fatalf("seed batch rows: %v", err)
	}

	processed, err := RunDeviceIdleRevokeOnce(db, idleTestNow)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if processed != total {
		t.Fatalf("%d 行候选必须全部处理（跨批次），实际 %d", total, processed)
	}
}

// 空表 / 表缺失 / nil db 一律 (0, nil)，绝不 panic 或报错。
//
// 后台 worker 反复重试一个必然失败的任务会把日志刷满，并掩盖真正的故障。
func TestDeviceIdleRevokeNilAndMissingSchemaAreSafe(t *testing.T) {
	if processed, err := RunDeviceIdleRevokeOnce(nil, idleTestNow); processed != 0 || err != nil {
		t.Fatalf("nil db 应当 (0,nil)，实际 (%d,%v)", processed, err)
	}

	db := newDevicePoolTestDB(t)
	if err := db.Migrator().DropTable(entity.TableNameAccountDeviceBinding); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if processed, err := RunDeviceIdleRevokeOnce(db, idleTestNow); processed != 0 || err != nil {
		t.Fatalf("表缺失应当 (0,nil)（fail-open），实际 (%d,%v)", processed, err)
	}
}

// 零值 now 必须被替换成真实时间，而不是拿零值去算 cutoff
// （零值会让 cutoff 变成 -240h，即 1753 年，所有行都会被扫成候选）。
func TestDeviceIdleRevokeZeroNowFallsBackToRealTime(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 3006, 0)
	// 刚刚活跃：无论 now 用什么，都不该被撤销。
	seedBindingWithLastSeen(t, db, 3006, "just-active", time.Now().UTC(), false, "")

	processed, err := RunDeviceIdleRevokeOnce(db, time.Time{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if processed != 0 {
		t.Fatalf("零值 now 必须回退到真实时间，刚活跃的行不该被撤销，实际 %d", processed)
	}
}
