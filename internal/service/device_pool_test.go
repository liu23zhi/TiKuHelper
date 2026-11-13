package service

// 设备池上限判定的回归测试（121 §4.9 / 107 Q15-Q17）。
//
// 五条判据（任务书）+ 两条自加的边界：
//   ① device_limit=2 的第 3 台设备被拒
//   ② device_limit=0 的第 100 台设备放行（变异重点：把 0 当「禁止」→ 必红）
//   ③ 表不存在时全部放行（fail-open 变异：改成 fail-closed → 用例红）
//   ④ 同设备重复登记幂等（不占新槽）
//   ⑤ 无历史回填 —— 在 registry 侧做源码级断言
//
// 全部走真实函数 + 真实 SQLite 库，不手搓 struct 自比。
//
// ⚠️ 本文件的期望值一律写**字面量**，不引用 DevicePoolReason* 常量：
// 若期望值与被测实现共享同一个常量，常量改成任何值时期望值会同步漂移，
// 断言就恒真了（本仓已因「期望值引用被测常量」交付过假钉子）。

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var devicePoolTestDBSeq atomic.Int64

// newDevicePoolTestDB 建一个带等级体系 + 设备池完整 schema 的最小库。
//
// 刻意**不用** registry.ensure*：service 包不能 import registry（registry → service，
// 反向 import 会循环）。这里手写等价的两步（AutoMigrate + level_id 的显式 ALTER），
// 好处是单测能独立验证「schema 就绪时 service 行为正确」，
// 而 schema 本身的建立由 registry 包的测试负责。
func newDevicePoolTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := "file:device-pool-" + name + "-" + formatInt64(devicePoolTestDBSeq.Add(1)) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{}, &entity.AccountDeviceBinding{},
		&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// users.level_id 走显式 ALTER（生成文件里没有该字段，见 registry 的注释）。
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser + " ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatalf("add level_id: %v", err)
		}
	}
	return db
}

func formatInt64(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// seedDevicePoolUser 造一个用户并把它挂到指定等级（levelID=0 表示无等级）。
func seedDevicePoolUser(t *testing.T, db *gorm.DB, userID int32, levelID int32) {
	t.Helper()
	user := entity.User{Username: "device-pool-fixture-" + formatInt64(int64(userID)), Role: "user"}
	user.ID = userID
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if levelID > 0 {
		if err := db.Table(entity.TableNameUser).Where("id = ?", userID).
			Update("level_id", levelID).Error; err != nil {
			t.Fatalf("attach level: %v", err)
		}
	}
}

// seedDevicePoolLevel 造一个等级，返回它的 ID。
func seedDevicePoolLevel(t *testing.T, db *gorm.DB, rank int32, deviceLimit int32) int32 {
	t.Helper()
	level := entity.UserLevel{
		Name:        "设备池档-" + formatInt64(int64(rank)),
		Rank:        rank,
		Enabled:     true,
		DeviceLimit: deviceLimit,
	}
	if err := db.Create(&level).Error; err != nil {
		t.Fatalf("create level: %v", err)
	}
	return level.ID
}

// RegisterDevice 的薄封装：断言放行并吞掉错误（正常路径不该有错）。
func mustRegisterDevice(t *testing.T, db *gorm.DB, userID int32, deviceID string) DevicePoolDecision {
	t.Helper()
	decision, err := RegisterDevice(db, userID, deviceID)
	if err != nil {
		t.Fatalf("register device %s: %v", deviceID, err)
	}
	if !decision.Allowed {
		t.Fatalf("register device %s 应当放行，实际被拒：%+v", deviceID, decision)
	}
	return decision
}

// ─────────────────────── 判据 ① ───────────────────────

// 判据 ①：device_limit=2 时第 3 台新设备被拒，前两台放行。
//
// 断言的是**具体哪一台**被拒（前两台 Allowed=true、第三台 false）。
// 只断言「有一台被拒」的话，把上限错写成 1 也会绿。
func TestDevicePoolRejectsThirdDeviceWhenLimitTwo(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 2)
	seedDevicePoolUser(t, db, 101, levelID)

	for _, deviceID := range []string{"dev-1", "dev-2"} {
		decision := mustRegisterDevice(t, db, 101, deviceID)
		if decision.Reason != "within-limit" {
			t.Fatalf("device %s 应当是 within-limit，实际 %q", deviceID, decision.Reason)
		}
		if decision.Limit != 2 {
			t.Fatalf("生效上限必须是 2，实际 %d", decision.Limit)
		}
		if count, err := CountDevices(db, 101); err != nil || count != iDeviceIDCount(deviceID) {
			t.Fatalf("已登记设备数不对：count=%d err=%v", count, err)
		}
	}

	decision := CanUseDevice(db, 101, "dev-3")
	if decision.Allowed {
		t.Fatalf("上限 2 的第 3 台设备必须被拒，实际放行：%+v", decision)
	}
	if decision.Reason != "limit-exceeded" {
		t.Fatalf("拒绝原因必须是 limit-exceeded，实际 %q", decision.Reason)
	}
	if decision.Used != 2 {
		t.Fatalf("拒绝时应报出已用 2 台，实际 %d", decision.Used)
	}
	if !decision.Enforced {
		t.Fatal("上限档被拒时 Enforced 必须为 true")
	}

	// 被拒的设备**不得**被登记（否则一次拒绝也会占槽，用户越试越满）。
	if count, err := CountDevices(db, 101); err != nil || count != 2 {
		t.Fatalf("被拒的第 3 台设备不该落库：count=%d err=%v", count, err)
	}
	if err := db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "device_id"); !err {
		t.Fatal("前置：绑定表必须存在")
	}
	var thirdCount int64
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("user_id = ? AND device_id = ?", 101, "dev-3").Count(&thirdCount).Error; err != nil {
		t.Fatalf("count dev-3: %v", err)
	}
	if thirdCount != 0 {
		t.Fatalf("被拒设备不该落库，实际 %d 行", thirdCount)
	}
}

// iDeviceIDCount 把 "dev-N" 映射成「登记到这一步为止应该有 N 台」。
func iDeviceIDCount(deviceID string) int {
	switch deviceID {
	case "dev-1":
		return 1
	case "dev-2":
		return 2
	default:
		return -1
	}
}

// ─────────────────────── 判据 ② ───────────────────────

// 判据 ②：device_limit=0 ⇒ **不限**，第 100 台设备也必须放行。
//
// 这是本任务最关键的一条（不可协商点 ①）。变异形态：把 0 当成「禁止」或
// 「上限 0 台」，前 1 台起就会红 —— 而这条用例直接跑满 100 台，
// 任何「0 = 某个具体台数」的解释都会在第 1 台或第 N 台红。
func TestDevicePoolUnlimitedWhenLimitIsZero(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 0)
	seedDevicePoolUser(t, db, 202, levelID)

	for i := 1; i <= 100; i++ {
		deviceID := "unlimited-dev-" + formatInt64(int64(i))
		decision := mustRegisterDevice(t, db, 202, deviceID)
		if decision.Reason != "unlimited" {
			t.Fatalf("第 %d 台设备应当是 unlimited，实际 %q", i, decision.Reason)
		}
		if decision.Limit != 0 {
			t.Fatalf("不限档的 Limit 必须原样报出 0，实际 %d", decision.Limit)
		}
		if decision.Enforced {
			t.Fatalf("不限档不做上限比较，Enforced 必须为 false（第 %d 台）", i)
		}
	}

	if count, err := CountDevices(db, 202); err != nil || count != 100 {
		t.Fatalf("100 台设备都应登记成功：count=%d err=%v", count, err)
	}
	// 不限档**不做计数**：判定走的是「先于计数返回」的短路，
	// 因此 Used 是 DevicePoolUsedUnknown 而不是 0。这是刻意设计：
	// 把「没统计」写成 0 会让日志里分不清「不限」和「真的还没设备」。
	decision := CanUseDevice(db, 202, "unlimited-dev-101")
	if decision.Used != -1 {
		t.Fatalf("不限档不该统计已用数（Used 应为 -1），实际 %d", decision.Used)
	}
}

// ─────────────────────── 判据 ③ ───────────────────────

// 判据 ③：绑定表不存在时**全部放行**（fail-open，107 Q17）。
//
// 变异形态：把 fail-open 改成 fail-closed（表缺失 ⇒ 拒绝）→ 本用例立刻红。
//
// 这条不是「优雅降级」，是必须：设备池是权益限制不是安全边界，
// fail-closed 的代价是「数据库抖一下全站登录失败」。
func TestDevicePoolFailsOpenWhenTableMissing(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 1) // 上限 1：fail-open 失败的话第 2 台必被拒
	seedDevicePoolUser(t, db, 303, levelID)

	// 制造「表不存在」：把刚建好的表删掉。
	if err := db.Migrator().DropTable(entity.TableNameAccountDeviceBinding); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		t.Fatal("前置：表必须已删掉")
	}

	// 连续 5 台都放行 —— 若 fail-closed 生效，第 2 台就会被拒。
	for i := 1; i <= 5; i++ {
		deviceID := "failopen-dev-" + formatInt64(int64(i))
		decision := CanUseDevice(db, 303, deviceID)
		if !decision.Allowed {
			t.Fatalf("表缺失时必须 fail-open 放行第 %d 台，实际被拒：%+v", i, decision)
		}
		if decision.Reason != "fail-open" {
			t.Fatalf("原因必须是 fail-open（要能与「本来就没超限」区分），实际 %q", decision.Reason)
		}
		if decision.Enforced {
			t.Fatal("fail-open 时 Enforced 必须为 false（本次没有做上限比较）")
		}
		// RegisterDevice 在表缺失时也要放行且不报错（调用方不该看到错误）。
		if _, err := RegisterDevice(db, 303, deviceID); err != nil {
			t.Fatalf("表缺失时 RegisterDevice 不该返回错误：%v", err)
		}
	}

	// CountDevices 是显式查询入口，必须如实报「表没就绪」而不是假装 0：
	// 假装 0 会让上层误判「这台用户一台设备都没用过」。
	if _, err := CountDevices(db, 303); err == nil {
		t.Fatal("CountDevices 在表缺失时必须返回错误（它不是 fail-open 入口）")
	}
}

// 判据 ③ 的配套：等级体系缺失时同样 fail-open（不是表缺失那一种）。
func TestDevicePoolFailsOpenWhenLevelSchemaMissing(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 404, 0)
	if err := db.Migrator().DropTable(entity.TableNameUserLevel); err != nil {
		t.Fatalf("drop user_levels: %v", err)
	}

	decision := CanUseDevice(db, 404, "any-device")
	if !decision.Allowed {
		t.Fatalf("等级表缺失时必须 fail-open 放行：%+v", decision)
	}
	if decision.Reason != "fail-open" {
		t.Fatalf("原因必须是 fail-open，实际 %q", decision.Reason)
	}
}

// 无等级（level_id=0）⇒ 不设上限，放行（不是「禁止」）。
//
// 这条与判据 ② 同源：本仓 rank=0 / level_id=0 都是「无」而不是「零值上限」。
func TestDevicePoolAllowsWhenUserHasNoLevel(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 505, 0)

	for i := 1; i <= 5; i++ {
		decision := mustRegisterDevice(t, db, 505, "nolevel-dev-"+formatInt64(int64(i)))
		if decision.Reason != "no-limit-configured" {
			t.Fatalf("无等级用户应当是 no-limit-configured，实际 %q", decision.Reason)
		}
		if decision.Enforced {
			t.Fatal("无等级时不做上限比较，Enforced 必须为 false")
		}
	}
}

// ─────────────────────── 判据 ④ ───────────────────────

// 判据 ④：同一设备重复登记**幂等**（不占新槽、不覆盖 first_seen_at）。
//
// 变异形态：把 insert-on-conflict 改成无条件的 Create（或把判重挪到计数之后）
// → 同一设备登记 3 次后 count 变 3，上限 2 的用户会被自己的旧设备锁在门外，本用例红。
func TestDevicePoolRegisterIsIdempotentPerDevice(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 2)
	seedDevicePoolUser(t, db, 606, levelID)

	mustRegisterDevice(t, db, 606, "same-dev")
	var firstSeen time.Time
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("first_seen_at").Where("user_id = ? AND device_id = ?", 606, "same-dev").
		Scan(&firstSeen).Error; err != nil {
		t.Fatalf("read first_seen_at: %v", err)
	}

	// 同一设备再登记两次：都必须放行、都必须不占新槽。
	for i := 0; i < 2; i++ {
		decision := mustRegisterDevice(t, db, 606, "same-dev")
		if decision.Reason != "already-registered" {
			t.Fatalf("重复登记第 %d 次应当是 already-registered，实际 %q", i+2, decision.Reason)
		}
		if decision.Used != 1 {
			t.Fatalf("重复登记不该让已用数变成非 1，实际 %d", decision.Used)
		}
	}

	if count, err := CountDevices(db, 606); err != nil || count != 1 {
		t.Fatalf("同一设备登记 3 次后必须只有 1 行（幂等锚点）：count=%d err=%v", count, err)
	}

	var rowCount int64
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("user_id = ? AND device_id = ?", 606, "same-dev").Count(&rowCount).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("同设备必须只有 1 行，实际 %d 行", rowCount)
	}
	// FirstSeenAt 只写一次：重复登记若覆盖它，「首次见到这台设备」就丢了。
	var firstSeenAgain time.Time
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("first_seen_at").Where("user_id = ? AND device_id = ?", 606, "same-dev").
		Scan(&firstSeenAgain).Error; err != nil {
		t.Fatalf("read first_seen_at again: %v", err)
	}
	if !firstSeenAgain.Equal(firstSeen) {
		t.Fatalf("first_seen_at 必须只写一次：原 %v，重复登记后 %v", firstSeen, firstSeenAgain)
	}

	// 幂等的另一面：用满 2 台中的 1 台后，第 2 台仍应放行（槽位没被重复登记吃掉）。
	mustRegisterDevice(t, db, 606, "second-dev")
	if decision := CanUseDevice(db, 606, "third-dev"); decision.Allowed {
		t.Fatalf("两台已满时第 3 台必须被拒：%+v", decision)
	}
}

// 判据 ④ 的并发面：同一账号同一设备并发登记，最终必须只有一行。
//
// 唯一索引是这层的唯一保障：没有它，两个并发请求会各写一行，
// 设备数虚高，而上限判定会在**下一次登录**才表现出来（更难排查）。
func TestDevicePoolConcurrentRegisterSameDeviceWritesOneRow(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 5)
	seedDevicePoolUser(t, db, 707, levelID)

	const goroutines = 8
	done := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			_, err := RegisterDevice(db, 707, "race-dev")
			done <- err
		}()
	}
	for i := 0; i < goroutines; i++ {
		if err := <-done; err != nil {
			t.Fatalf("并发登记不该返回错误（撞唯一键要按幂等处理）：%v", err)
		}
	}
	if count, err := CountDevices(db, 707); err != nil || count != 1 {
		t.Fatalf("并发登记同一设备后必须只有 1 行：count=%d err=%v", count, err)
	}
}

// ─────────────────────── Q16 遗留出口 ───────────────────────

// device_limit < 0 ⇒ 禁止该等级用任何设备（Q16 遗留的出口）。
//
// 0 已被「不限」占用，无法表达「禁止」。这里用负数表达，且与 0 的语义
// 严格区分：0 放行第 100 台、-1 连第 1 台都拒。
func TestDevicePoolForbiddenWhenLimitNegative(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, -1)
	seedDevicePoolUser(t, db, 808, levelID)

	decision, err := RegisterDevice(db, 808, "any-dev")
	if err != nil {
		t.Fatalf("登记不该返回错误（拒绝是业务结果）：%v", err)
	}
	if decision.Allowed {
		t.Fatalf("device_limit<0 必须拒绝任何设备，实际放行：%+v", decision)
	}
	if decision.Reason != "forbidden" {
		t.Fatalf("原因必须是 forbidden（不能与 limit-exceeded 混为一谈），实际 %q", decision.Reason)
	}
	if count, err := CountDevices(db, 808); err != nil || count != 0 {
		t.Fatalf("被禁止的设备不该落库：count=%d err=%v", count, err)
	}
}

// ─────────────────────── 参数与边界 ───────────────────────

// 设备指纹为空 ⇒ fail-open（不能因为「没有指纹」把旧客户端全封）。
func TestDevicePoolEmptyDeviceIDFailsOpen(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 1)
	seedDevicePoolUser(t, db, 909, levelID)

	for _, raw := range []string{"", "   ", "\t\n"} {
		decision := CanUseDevice(db, 909, raw)
		if !decision.Allowed {
			t.Fatalf("空设备指纹（%q）必须 fail-open 放行：%+v", raw, decision)
		}
		if decision.Reason != "fail-open" {
			t.Fatalf("原因必须是 fail-open，实际 %q", decision.Reason)
		}
	}
}

// 设备指纹归一化：去首尾空白后**同一个设备**（否则多打一个空格就变成新设备占新槽）。
func TestDevicePoolNormalizesDeviceID(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 1)
	seedDevicePoolUser(t, db, 1001, levelID)

	mustRegisterDevice(t, db, 1001, "normalized-dev")
	decision := mustRegisterDevice(t, db, 1001, "  normalized-dev\t")
	if decision.Reason != "already-registered" {
		t.Fatalf("归一化后应当是 already-registered，实际 %q", decision.Reason)
	}
	if count, err := CountDevices(db, 1001); err != nil || count != 1 {
		t.Fatalf("归一化后必须只有 1 台：count=%d err=%v", count, err)
	}

	if got := NormalizeDevicePoolDeviceID("  abc  "); got != "abc" {
		t.Fatalf("NormalizeDevicePoolDeviceID 应去首尾空白，实际 %q", got)
	}
	// 不转小写：指纹大小写敏感（同 user_binding_records.value 的取舍）。
	if got := NormalizeDevicePoolDeviceID("AbC"); got != "AbC" {
		t.Fatalf("不该转小写，实际 %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := NormalizeDevicePoolDeviceID(long); len(got) != DevicePoolDeviceIDMaxLength {
		t.Fatalf("超长指纹必须截断到 %d，实际 %d", DevicePoolDeviceIDMaxLength, len(got))
	}
}

// 总开关默认关闭 ⇒ 零查询直接放行（与黑名单总开关同款纪律）。
func TestDevicePoolSwitchOffAllowsEverything(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 1)
	seedDevicePoolUser(t, db, 1100, levelID)
	mustRegisterDevice(t, db, 1100, "switch-dev")

	// 开关未配置 ⇒ 默认关闭 ⇒ 上限不生效。
	for i := 0; i < 3; i++ {
		decision := CanUseDeviceWithSwitch(db, 1100, "switch-new-dev-"+formatInt64(int64(i)))
		if !decision.Allowed {
			t.Fatalf("总开关关闭时必须放行：%+v", decision)
		}
		if decision.Reason != "switch-off" {
			t.Fatalf("原因必须是 switch-off，实际 %q", decision.Reason)
		}
	}

	// 打开开关后，同样的调用必须真的开始限制（证明开关真的接进了判定）。
	if err := db.Create(&entity.SystemSetting{
		Key: DeviceLimitEnabledSettingKey, Value: "true",
		Remark: "启用账户设备池上限",
	}).Error; err != nil {
		t.Fatalf("seed switch: %v", err)
	}
	decision := CanUseDeviceWithSwitch(db, 1100, "switch-new-dev-1")
	if decision.Allowed {
		t.Fatalf("开关打开后上限 1 的第 2 台必须被拒：%+v", decision)
	}
	if decision.Reason != "limit-exceeded" {
		t.Fatalf("开关打开后原因必须是 limit-exceeded，实际 %q", decision.Reason)
	}
}

// 降级/换级**立即**改变上限（不做缓存）。
//
// 若实现里缓存了 device_limit，「管理员改了上限但行为没变」这类 bug
// 的表现是「配置看起来改了但没生效」，排查成本极高。
func TestDevicePoolLimitFollowsLevelChange(t *testing.T) {
	db := newDevicePoolTestDB(t)
	lowID := seedDevicePoolLevel(t, db, 1, 1)
	highID := seedDevicePoolLevel(t, db, 2, 3)
	seedDevicePoolUser(t, db, 1200, lowID)
	mustRegisterDevice(t, db, 1200, "lv-dev-1")
	if decision := CanUseDevice(db, 1200, "lv-dev-2"); decision.Allowed {
		t.Fatalf("上限 1 时第 2 台必须被拒：%+v", decision)
	}

	// 升级到上限 3：第 2、3 台立刻放行（不需要重启/重登）。
	if err := db.Table(entity.TableNameUser).Where("id = ?", 1200).
		Update("level_id", highID).Error; err != nil {
		t.Fatalf("upgrade level: %v", err)
	}
	mustRegisterDevice(t, db, 1200, "lv-dev-2")
	mustRegisterDevice(t, db, 1200, "lv-dev-3")
	decision := CanUseDevice(db, 1200, "lv-dev-4")
	if decision.Allowed {
		t.Fatalf("上限 3 的第 4 台必须被拒：%+v", decision)
	}
	if decision.Limit != 3 {
		t.Fatalf("换级后 Limit 必须立即变成 3，实际 %d", decision.Limit)
	}

	// 降级到上限 1：**已登记的 3 台仍然可用**（已登记优先于超限），
	// 但新的第 4 台继续被拒。这是「降级不该把用户立刻锁在门外」的取舍。
	if err := db.Table(entity.TableNameUser).Where("id = ?", 1200).
		Update("level_id", lowID).Error; err != nil {
		t.Fatalf("downgrade level: %v", err)
	}
	for _, deviceID := range []string{"lv-dev-1", "lv-dev-2", "lv-dev-3"} {
		if decision := CanUseDevice(db, 1200, deviceID); !decision.Allowed {
			t.Fatalf("降级后已登记设备 %s 仍应放行（否则用户被自己的设备锁在门外）：%+v", deviceID, decision)
		}
	}
	if decision := CanUseDevice(db, 1200, "lv-dev-4"); decision.Allowed {
		t.Fatalf("降级后新设备必须被拒：%+v", decision)
	}
}

// 多账号隔离：同一台设备可以被多个账号登记，互不影响。
func TestDevicePoolIsolatesUsers(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 1)
	seedDevicePoolUser(t, db, 1301, levelID)
	seedDevicePoolUser(t, db, 1302, levelID)

	mustRegisterDevice(t, db, 1301, "shared-dev")
	// 另一个账号用同一台设备：对他而言是新设备，且他自己的 1 个槽还空着 ⇒ 放行。
	decision := mustRegisterDevice(t, db, 1302, "shared-dev")
	if decision.Reason != "within-limit" {
		t.Fatalf("另一账号的同一设备应当是 within-limit，实际 %q", decision.Reason)
	}
	if decision.Used != 0 {
		t.Fatalf("另一账号登记前已用数应为 0，实际 %d", decision.Used)
	}

	if count, err := CountDevices(db, 1301); err != nil || count != 1 {
		t.Fatalf("账号 1301 仍是 1 台：count=%d err=%v", count, err)
	}
	if count, err := CountDevices(db, 1302); err != nil || count != 1 {
		t.Fatalf("账号 1302 应有 1 台：count=%d err=%v", count, err)
	}
}

// nil / 非法入参一律 fail-open，绝不 panic（这些入口在登录链路上，panic = 全站 500）。
func TestDevicePoolNilAndInvalidInputsAreSafe(t *testing.T) {
	if decision := CanUseDevice(nil, 1, "dev"); !decision.Allowed || decision.Reason != "fail-open" {
		t.Fatalf("nil db 必须 fail-open：%+v", decision)
	}
	db := newDevicePoolTestDB(t)
	for _, userID := range []int32{0, -1} {
		if decision := CanUseDevice(db, userID, "dev"); !decision.Allowed || decision.Reason != "fail-open" {
			t.Fatalf("userID=%d 必须 fail-open：%+v", userID, decision)
		}
	}
	if count, err := CountDevices(db, 0); err != nil || count != 0 {
		t.Fatalf("CountDevices(userID=0) 应当 (0,nil)：count=%d err=%v", count, err)
	}
	if count, err := CountDevices(nil, 5); err != nil || count != 0 {
		t.Fatalf("CountDevices(nil) 应当 (0,nil)：count=%d err=%v", count, err)
	}
	decision, err := RegisterDevice(nil, 1, "dev")
	if err != nil {
		t.Fatalf("RegisterDevice(nil) 不该返回错误：%v", err)
	}
	if !decision.Allowed {
		t.Fatalf("RegisterDevice(nil) 必须 fail-open：%+v", decision)
	}
}

// 说明性用例：把「已用数」打到日志里，让判据 ①②④ 的数字可复算。
//
// 这不是凑数：任务要求贴出五条判据的输出，这条把关键数字一次算清，
// 避免报告里的数字靠手抄。
func TestDevicePoolDecisionSnapshotForReport(t *testing.T) {
	db := newDevicePoolTestDB(t)
	limitTwoID := seedDevicePoolLevel(t, db, 1, 2)
	unlimitedID := seedDevicePoolLevel(t, db, 2, 0)
	seedDevicePoolUser(t, db, 1401, limitTwoID)
	seedDevicePoolUser(t, db, 1402, unlimitedID)

	mustRegisterDevice(t, db, 1401, "snap-a")
	mustRegisterDevice(t, db, 1401, "snap-b")
	third := CanUseDevice(db, 1401, "snap-c")
	unlimited := CanUseDevice(db, 1402, "snap-u")
	t.Logf("limit=2 第3台: allowed=%v reason=%s limit=%d used=%d enforced=%v",
		third.Allowed, third.Reason, third.Limit, third.Used, third.Enforced)
	t.Logf("limit=0 第1台: allowed=%v reason=%s limit=%d used=%d enforced=%v",
		unlimited.Allowed, unlimited.Reason, unlimited.Limit, unlimited.Used, unlimited.Enforced)
	if fmt.Sprintf("%v/%s", third.Allowed, third.Reason) != "false/limit-exceeded" {
		t.Fatalf("判据①快照不符：%+v", third)
	}
	if fmt.Sprintf("%v/%s", unlimited.Allowed, unlimited.Reason) != "true/unlimited" {
		t.Fatalf("判据②快照不符：%+v", unlimited)
	}
}
