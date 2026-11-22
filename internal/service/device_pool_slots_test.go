package service

// 设备 ID 体系第二批（141 §2）：计槽公式 used = Σ max(1, session_count)。
//
// 与既有 device_pool_test.go 分开成文件：那个文件守的是 137 的判定顺序与
// fail-open 策略，本文件只守**一个式子**。141 §2 明确写了「不要再想别的表达方式」，
// 所以这里每条用例都在钉同一件事的不同侧面 —— 写法必须让「换个式子」必然变红。
//
// ## 为什么四条场景必须分开断言（S1–S4 各一条）
//
// 公式里 max(1, ·) 同时满足 R4（普通浏览器占 1）、R5（exe 未开会话占 1）、
// R6（3 会话占 3）、R7（全关回落 1）。其中 R4 与 R7 的 session_count **都是 0**，
// 一个「忘了 max」的实现会让它们在**同一个**方向出错，看起来像一条用例；
// 但一个「把 max 恒为 1」的实现只错 R6。两者是不同的实现错误，必须各自有钉子：
//
//	去掉 max(1,·)      ⇒ S1 / S2 / S4 红，S3 仍绿
//	max(1,·) 恒为 1    ⇒ S3 红，S1 / S2 / S4 仍绿
//	session_count 取历史峰值 ⇒ S4 红（其余绿）
//
// 所以四条缺一不可 —— 只有 S3 或只有 S1 都会漏掉另一类实现错误。
//
// ## 期望值一律写字面量
//
// 不引用 DevicePoolMinSlotWeight 等常量：那会让期望值与被测实现共享同一常量，
// 常量漂移时两侧同步漂移 ⇒ 断言恒真（本仓已因此交付过假钉子）。

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// seedDeviceSlotBinding 插一行绑定，显式给 session_count / 撤销状态。
//
// 用 Create 而不是裸 SQL：entity 现在带 client_instance / session_count 两列，
// AutoMigrate 已建好，Create 会把零值一并写入 —— 这正是生产写入路径的形态。
func seedDeviceSlotBinding(t *testing.T, db *gorm.DB, userID int32, deviceID string,
	sessionCount int32, revoked bool) {
	t.Helper()
	row := entity.AccountDeviceBinding{
		UserID:         userID,
		DeviceID:       deviceID,
		ClientInstance: "instance-" + deviceID,
		SessionCount:   sessionCount,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create binding %s: %v", deviceID, err)
	}
	if revoked {
		if err := db.Table(entity.TableNameAccountDeviceBinding).
			Where("id = ?", row.ID).
			Updates(map[string]any{
				"revoked_at":     "2026-11-15 00:00:00",
				"revoked_reason": entity.DeviceBindingRevokeReasonUserRemoved,
			}).Error; err != nil {
			t.Fatalf("revoke %s: %v", deviceID, err)
		}
	}
}

// setDeviceSlotSessionCount 模拟 exe 上报当前会话数（C2-3 的写入口，本批次只读口径）。
func setDeviceSlotSessionCount(t *testing.T, db *gorm.DB, userID int32, deviceID string, count int32) {
	t.Helper()
	result := db.Table(entity.TableNameAccountDeviceBinding).
		Where("user_id = ? AND device_id = ?", userID, deviceID).
		UpdateColumn("session_count", count)
	if result.Error != nil {
		t.Fatalf("set session_count %s=%d: %v", deviceID, count, result.Error)
	}
	if result.RowsAffected == 0 {
		t.Fatalf("set session_count %s=%d 没有命中任何行（前置不成立）", deviceID, count)
	}
}

func mustCountDevices(t *testing.T, db *gorm.DB, userID int32) int {
	t.Helper()
	used, err := CountDevices(db, userID)
	if err != nil {
		t.Fatalf("CountDevices(%d): %v", userID, err)
	}
	return used
}

// ─────────────────────── S1（R4）：普通浏览器贡献 1 ───────────────────────

// S1：普通浏览器 session_count 恒 0，必须贡献 **1** 个槽位（R4「绑定即占 1」）。
//
// 变异「把 CountDevices 改成直接累加 session_count」⇒ 本用例红（期望 1 实际 0）。
// 这是最危险的一类回退：上限 3 的用户能绑无限台普通浏览器，而且没有任何报错。
func TestDeviceSlotsNormalBrowserContributesOne(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 4001, 0)

	seedDeviceSlotBinding(t, db, 4001, "chrome-profile-a", 0, false)

	if used := mustCountDevices(t, db, 4001); used != 1 {
		t.Fatalf("普通浏览器（session_count=0）必须贡献 1 个槽位（R4），实际 used=%d。\n"+
			"若这里是 0，说明计槽退化成「直接累加 session_count」——"+
			"普通浏览器会永远不占槽，设备上限对它们彻底失效", used)
	}

	// 两台普通浏览器 ⇒ 2（证明是逐行累加，不是「有设备就算 1」）。
	seedDeviceSlotBinding(t, db, 4001, "edge-profile-b", 0, false)
	if used := mustCountDevices(t, db, 4001); used != 2 {
		t.Fatalf("两台普通浏览器必须占 2 个槽位，实际 used=%d", used)
	}
}

// ─────────────────────── S2（R5/R7）：exe 无会话也贡献 1 ───────────────────────

// S2：exe 一个浏览器会话都没开（session_count=0）时，exe 宿主自己占 **1**（R5）；
// 全部关闭后同样回落到 **1**（R7）。
//
// 与 S1 是同一个 0 值的两种业务解释 —— 141 §2 要求它们**同解**，所以这里
// 用「同一个 device_id 从 0 开始」直接覆盖两次：先无会话，再上报归 0。
//
// 变异「直接累加 session_count」⇒ 本用例两次断言都红（期望 1 实际 0）。
func TestDeviceSlotsExeWithoutSessionsContributesOne(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 4002, 0)

	// R5：exe 启动、从不点「新会话」。
	seedDeviceSlotBinding(t, db, 4002, "exe-host-r5", 0, false)
	if used := mustCountDevices(t, db, 4002); used != 1 {
		t.Fatalf("exe 启动但未开会话必须占 1 个槽位（R5），实际 used=%d。"+
			"若为 0，exe 就等于「不占槽」，用户能无限多开 exe", used)
	}

	// R6：开 3 个会话 ⇒ 占 3。
	setDeviceSlotSessionCount(t, db, 4002, "exe-host-r5", 3)
	if used := mustCountDevices(t, db, 4002); used != 3 {
		t.Fatalf("exe 开 3 个会话必须占 3 个槽位（R6），实际 used=%d", used)
	}

	// R7：全部关闭、上报归 0 ⇒ 回落到 1（那一个是 exe 宿主自己）。
	setDeviceSlotSessionCount(t, db, 4002, "exe-host-r5", 0)
	if used := mustCountDevices(t, db, 4002); used != 1 {
		t.Fatalf("exe 全关后必须回落到 1 个槽位（R7：max(1,0)=1），实际 used=%d。"+
			"若为 0，说明「全关」没有回落而是直接释放，与 R5 的「exe 常驻占 1」矛盾；"+
			"若为 3，说明 session_count 被当成了历史峰值", used)
	}
}

// ─────────────────────── S3（R6）：3 会话占 3 ───────────────────────

// S3：exe 开 3 个会话必须贡献 **3**，而不是被 max 压成 1。
//
// 变异「把 max(1,n) 改成恒为 1」⇒ 本用例红（期望 3 实际 1）。
// 这条是唯一能抓住「max 写错方向」的用例：S1/S2 的输入都是 0，
// 恒为 1 的实现在它们上面完全正确。
func TestDeviceSlotsThreeSessionsContributeThree(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 4003, 0)

	seedDeviceSlotBinding(t, db, 4003, "exe-host-r6", 3, false)
	if used := mustCountDevices(t, db, 4003); used != 3 {
		t.Fatalf("exe 开 3 个会话必须占 3 个槽位（R6），实际 used=%d。"+
			"若为 1，说明 max(1,·) 退化成了恒等 1 —— 那会让 exe 的会话数完全不参与计槽，"+
			"上限 3 的用户能开无限个会话", used)
	}

	// 4 会话 ⇒ 4（上界不是 3，公式不含 clamp）。
	setDeviceSlotSessionCount(t, db, 4003, "exe-host-r6", 4)
	if used := mustCountDevices(t, db, 4003); used != 4 {
		t.Fatalf("exe 开 4 个会话必须占 4 个槽位（141 §2 的表格），实际 used=%d", used)
	}

	// 1 会话 ⇒ 1（与「0 会话」的 1 同值，但语义不同：这是真的 1 个会话）。
	setDeviceSlotSessionCount(t, db, 4003, "exe-host-r6", 1)
	if used := mustCountDevices(t, db, 4003); used != 1 {
		t.Fatalf("exe 开 1 个会话必须占 1 个槽位，实际 used=%d", used)
	}
}

// ─────────────────────── S4：归 0 必须真的回落到 1，不是「记得曾经是 3」 ───────────────────────

// S4：session_count 是**当前**活跃会话数，不是历史峰值。
//
// 场景：3 会话 → 上报 0 → 必须回到 1。若实现把 session_count 当「历史最大值」
// （例如写入口只增不减、或 CountDevices 取 MAX(历史)），这里会仍然是 3。
//
// 与 S2 的 R7 单条不同：本用例先制造一个**非零历史**，再归 0 ——
// 只有这样才能区分「默认值就是 0 所以恰好对」与「真的回落」。
func TestDeviceSlotsSessionCountIsCurrentNotPeak(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 4004, 0)

	seedDeviceSlotBinding(t, db, 4004, "exe-host-peak", 3, false)
	if used := mustCountDevices(t, db, 4004); used != 3 {
		t.Fatalf("前置：3 会话应占 3，实际 used=%d", used)
	}

	// 全部关闭，exe 上报 count=0。
	setDeviceSlotSessionCount(t, db, 4004, "exe-host-peak", 0)
	if used := mustCountDevices(t, db, 4004); used != 1 {
		t.Fatalf("上报 count=0 后必须回落到 1（session_count 是当前值不是历史峰值），实际 used=%d。"+
			"若仍为 3，说明 session_count 被当成峰值保存或读取 —— "+
			"那会让用户每次开完会话就永久少 2 个槽位，且没有任何报错", used)
	}
	// 再确认库里真的写了 0（排除「CountDevices 内部记住了峰值」这一种解释）。
	var stored struct {
		SessionCount int32 `gorm:"column:session_count"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("session_count AS session_count").
		Where("user_id = ? AND device_id = ?", 4004, "exe-host-peak").Take(&stored).Error; err != nil {
		t.Fatalf("read session_count: %v", err)
	}
	if stored.SessionCount != 0 {
		t.Fatalf("上报归 0 后库里必须真的是 0，实际 %d", stored.SessionCount)
	}
}

// ─────────────────────── 撤销的行不占槽位 ───────────────────────

// 撤销的行无论 session_count 多大都不占槽（137 §3.2 + 141 §2 的「已绑定且未撤销」）。
//
// 这条同时钉两件事：
//   - revoked_at IS NULL 过滤**必须保留**在公式内侧（丢掉它 ⇒ 用例红）；
//   - 撤销不会把 session_count 清零（数据保留以便审计），所以「撤销后仍算 3」
//     这种错必须能被抓出来。
func TestDeviceSlotsRevokedBindingNeverCounts(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 4005, 0)

	seedDeviceSlotBinding(t, db, 4005, "live-dev", 0, false)
	seedDeviceSlotBinding(t, db, 4005, "revoked-dev", 3, true)

	if used := mustCountDevices(t, db, 4005); used != 1 {
		t.Fatalf("已撤销的行不占槽位：期望 1（只有 live-dev），实际 used=%d。"+
			"若为 4，说明撤销过滤丢了 —— 用户移除设备后槽位永不释放", used)
	}
	// 撤销行仍在表里（不删行）—— 这是 CountAllDevices 的口径。
	if all, err := CountAllDevices(db, 4005); err != nil || all != 2 {
		t.Fatalf("撤销后行必须仍在（不删行）：all=%d err=%v", all, err)
	}
}

// ─────────────────────── 混合场景：累加而不是计数 ───────────────────────

// 多行混合时必须**逐行取 max(1,·) 再累加**，而不是「行数」或「会话总数」。
//
// 构造：普通浏览器(0) + exe 2 会话(2) ⇒ 期望 3。
//
//	行数口径       ⇒ 2（错）
//	会话总和口径   ⇒ 2（错，且普通浏览器贡献 0）
//	正确公式       ⇒ 1 + 2 = 3
//
// 三种解释在本用例上给出三个不同数字，所以它能一次排除两种错误实现。
func TestDeviceSlotsMixedUserSumsPerRowMax(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 4006, 0)

	seedDeviceSlotBinding(t, db, 4006, "browser-profile", 0, false) // → 1
	seedDeviceSlotBinding(t, db, 4006, "exe-host", 2, false)        // → 2

	if used := mustCountDevices(t, db, 4006); used != 3 {
		t.Fatalf("普通浏览器(1) + exe 2 会话(2) 必须是 3，实际 used=%d。\n"+
			"2 说明退化成「行数」或「会话总和」；两种都会让普通浏览器不占槽", used)
	}
	// CountAllDevices 是行数口径（2），与槽位口径（3）刻意不同。
	if all, err := CountAllDevices(db, 4006); err != nil || all != 2 {
		t.Fatalf("CountAllDevices 是行数口径（不套 max(1,·)）：期望 2，实际 all=%d err=%v", all, err)
	}
}

// ─────────────────────── 与 CanUseDevice 的接线 ───────────────────────

// 计槽口径必须真的接到上限判定上：exe 开 3 个会话把上限 3 打满后，新会话被拒。
//
// 这是「实现层」与「接线层」两个作用域里的后者（本仓踩过只测前者的坑）。
// 变异「CountDevices 改回统计行数」⇒ 本用例红（used=1，第 2 台放行）。
func TestDeviceSlotsFeedIntoLimitDecision(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 3)
	seedDevicePoolUser(t, db, 4007, levelID)

	// exe 宿主 1 行、3 个会话 ⇒ 占 3 个槽位即已满。
	seedDeviceSlotBinding(t, db, 4007, "exe-host-full", 3, false)

	decision := CanUseDevice(db, 4007, "exe-host-full-2")
	if decision.Allowed {
		t.Fatalf("上限 3 已被 exe 的 3 个会话占满时，新的第 4 个槽必须被拒：%+v", decision)
	}
	if decision.Reason != "limit-exceeded" {
		t.Fatalf("拒绝原因必须是 limit-exceeded，实际 %q", decision.Reason)
	}
	if decision.Used != 3 {
		t.Fatalf("判定里报出的已用必须是 3（槽位口径），实际 %d", decision.Used)
	}
	if !decision.Enforced {
		t.Fatal("上限档被拒时 Enforced 必须为 true")
	}

	// 关掉全部会话 ⇒ 回落到 1 ⇒ 下一台新设备可以进来。
	setDeviceSlotSessionCount(t, db, 4007, "exe-host-full", 0)
	after := CanUseDevice(db, 4007, "exe-host-full-2")
	if !after.Allowed {
		t.Fatalf("exe 全关后应回落到 1 个槽位，新设备必须放行：%+v", after)
	}
	if after.Used != 1 {
		t.Fatalf("回落后的已用应为 1，实际 %d", after.Used)
	}
}

// ─────────────────────── 列未就绪时的降级口径 ───────────────────────

// session_count 列还没补（部署顺序「代码先上、迁移后跑」）时，
// 计槽必须退化为「每行算 1」—— 它恰好等于 max(1, 0)，与迁移完成后的口径一致。
//
// 为什么要钉这条：若退化路径改为「报错」或「算 0」，部署窗口期内
// CanUseDevice 会 fail-open（上限全失效）或全站误拒，而两种都只在
// 「先上代码后跑迁移」的那几分钟里出现 —— 单测不覆盖就永远发现不了。
func TestDeviceSlotsWithoutSessionCountColumnFallsBackToOnePerRow(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 4008, 0)
	seedDeviceSlotBinding(t, db, 4008, "pre-migration-a", 0, false)
	seedDeviceSlotBinding(t, db, 4008, "pre-migration-b", 0, false)

	// 把列删掉，精确复现「迁移未跑」的库。
	if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding +
		" DROP COLUMN session_count").Error; err != nil {
		t.Fatalf("drop session_count: %v", err)
	}
	if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "session_count") {
		t.Fatal("前置不成立：session_count 列应当已被删除")
	}

	used, err := CountDevices(db, 4008)
	if err != nil {
		t.Fatalf("列缺失时不得报错（否则 CanUseDevice 会 fail-open）：%v", err)
	}
	if used != 2 {
		t.Fatalf("列缺失时每行应算 1（等价 max(1,0)），期望 2 实际 %d", used)
	}
}

// 表缺失时 CountDevices 仍然返回 ErrDevicePoolSchemaUnavailable（既有契约不变）。
//
// 这条是回归钉：本批次把实现从 Count 改成 Find+累加，容易顺手把
// 「表未就绪」的错误语义丢掉 —— 那会让 device-quota 端点把故障显示成 used=0。
func TestDeviceSlotsMissingTableStillReturnsSchemaError(t *testing.T) {
	db := newDevicePoolTestDB(t)
	if err := db.Migrator().DropTable(entity.TableNameAccountDeviceBinding); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := CountDevices(db, 4009); err == nil {
		t.Fatal("表缺失时 CountDevices 必须返回 ErrDevicePoolSchemaUnavailable（不能假装 0）")
	}
}

// Exercise the real host reporting path rather than manufacturing session_count
// in SQL. A missing binding cannot be created by a report, and all closed
// browsers leave the single already-bound host, not zero slots.
func TestNativeHostReportedSessionsUseThreeThenOneSlot(t *testing.T) {
	db := newDevicePoolTestDB(t)
	const userID = 42001
	seedDevicePoolUser(t, db, userID, 0)
	now := time.Now()
	if err := ReportDeviceSessionCount(db, userID, "new-host", 3, now); !errors.Is(err, ErrDeviceSessionBindingNotFound) {
		t.Fatal("session report created or authorized an unbound host")
	}
	seedDeviceSlotBinding(t, db, userID, "bound-host", 0, false)
	for _, c := range []struct{ reported, want int }{{3, 3}, {3, 3}, {0, 1}} {
		if err := ReportDeviceSessionCount(db, userID, "bound-host", c.reported, now); err != nil {
			t.Fatal(err)
		}
		if got := mustCountDevices(t, db, userID); got != c.want {
			t.Fatalf("after report %d: %d slots, expected %d", c.reported, got, c.want)
		}
	}
}
