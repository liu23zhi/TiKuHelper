package service

// 设备标识冲突检测与会话上报/心跳的回归测试（141 §4 / §5.4 / §6.2）。
//
// # 每条用例对应哪条判据，以及它能抓住哪种实现错误
//
//	S5    TestDeviceConflictRejectsSecondInstanceOfSameDeviceID
//	      —— 同 device_id + 不同 instance 的**另一账号**首次登记 ⇒ 冲突。
//	      变异「去掉 instance 比对」→ 本条红（判成放行）。
//
//	S6    TestDeviceConflictAllowsSameInstanceReconnect
//	      —— 同 device_id + 同 instance ⇒ 放行（对照组）。
//	      变异「instance 必须不同才放行」→ 本条红。
//
//	红线 1 TestDeviceConflictOnlyOnFirstRegistration
//	      —— 已登记过的设备换 instance 再来 ⇒ **放行**。
//	      变异「每次都判」→ 本条红。这是 141 §12 第 2 条点名的最易写错处：
//	      写反后用户每次重开浏览器都被判冲突 → 换 ID → 槽位耗尽，且无任何报错。
//
//	S9    TestDeviceSessionCountExpiresAfterHeartbeatTimeout
//	      —— 心跳超 3 分钟 ⇒ 读到的会话数为 0，但**库里仍是原值**（不写回）。
//	      变异「去掉超时判定」→ 本条红。
//
// 另有边界：count 越界、归属/撤销/不存在三态同一错误、空 instance 不判冲突、
// 历史行空 instance 不构成冲突证据。
//
// ## 期望值一律写字面量
//
// 不引用 DeviceConflictCode / DeviceSessionMaxCount / DeviceSessionHeartbeatTTL
// 等被测常量：期望值与被测实现共享常量时，常量改成本身会同步漂移，断言恒真
// （本仓已因「期望值引用被测常量」交付过假钉子，见 t150）。

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ensureDeviceConflictColumns 补上 141 §3.1 的两列 + 本批次的心跳列（原生 SQL）。
//
// ## 为什么用 ALTER 而不是 AutoMigrate 实体
//
// client_instance 与 session_count 属于 C1 的独占范围（entity 文件由它改），
// 本批次不碰那个文件 —— 加了字段就会与 C1 的改动冲突。这里显式 ALTER 出
// 「C1 迁移跑完之后」的库状态，正是本批次在生产上的目标形态。
//
// session_reported_at 是本批次的心跳列（141 §5.4），当前没有任何迁移建它，
// 所以生产上**也**可能缺这一列 —— 代码对此有降级（退回 last_seen_at），
// 另有一条用例专门覆盖缺列态。
func ensureDeviceConflictColumns(t *testing.T, db *gorm.DB, withReportedAt bool) {
	t.Helper()
	ddls := []string{
		"client_instance VARCHAR(64) NOT NULL DEFAULT ''",
		"session_count INTEGER NOT NULL DEFAULT 0",
	}
	if withReportedAt {
		ddls = append(ddls, "session_reported_at DATETIME NULL")
	}
	for _, ddl := range ddls {
		column := fieldName(ddl)
		if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			continue
		}
		if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding +
			" ADD COLUMN " + ddl).Error; err != nil {
			t.Fatalf("add binding column %s: %v", column, err)
		}
	}
}

// fieldName 取 DDL 片段的第一段（列名）。
func fieldName(ddl string) string {
	for i, r := range ddl {
		if r == ' ' {
			return ddl[:i]
		}
	}
	return ddl
}

// newDeviceConflictTestDB 建一个带冲突/会话所需全部列的最小库。
//
// 复用 device_pool_test.go 的 newDevicePoolTestDB（同包、同一套 users/level 夹具），
// 只在其上补 141 的三列 —— 不复制一份建库逻辑，避免两处口径漂移。
func newDeviceConflictTestDB(t *testing.T, withReportedAt bool) *gorm.DB {
	t.Helper()
	db := newDevicePoolTestDB(t)
	ensureDeviceConflictColumns(t, db, withReportedAt)
	return db
}

// readBindingClientInstance 直接读库里的 client_instance（绕过实体字段）。
func readBindingClientInstance(t *testing.T, db *gorm.DB, userID int32, deviceID string) string {
	t.Helper()
	var row struct {
		ClientInstance string `gorm:"column:client_instance"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("client_instance AS client_instance").
		Where("user_id = ? AND device_id = ?", userID, deviceID).
		Take(&row).Error; err != nil {
		t.Fatalf("read client_instance(%d,%s): %v", userID, deviceID, err)
	}
	return row.ClientInstance
}

// readBindingSessionCount 直接读库里的 session_count / 心跳列。
func readBindingSessionState(t *testing.T, db *gorm.DB, userID int32, deviceID string,
	withReportedAt bool) (count int, lastSeen *time.Time, reportedAt *time.Time) {
	t.Helper()
	columns := "session_count AS session_count, last_seen_at AS last_seen_at"
	if withReportedAt {
		columns += ", session_reported_at AS session_reported_at"
	}
	var row struct {
		SessionCount     int        `gorm:"column:session_count"`
		LastSeenAt       *time.Time `gorm:"column:last_seen_at"`
		SessionReportedA *time.Time `gorm:"column:session_reported_at"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).Select(columns).
		Where("user_id = ? AND device_id = ?", userID, deviceID).Take(&row).Error; err != nil {
		t.Fatalf("read session state(%d,%s): %v", userID, deviceID, err)
	}
	return row.SessionCount, row.LastSeenAt, row.SessionReportedA
}

// ─────────────────────────── C2-1 / S5 ───────────────────────────

// TestDeviceConflictRejectsSecondInstanceOfSameDeviceID 是 S5：跨账号的镜像复制。
//
// 场景（141 §4.3）：同一个 device_id 被两个不同账号先后登记，instance 不同 ⇒
// 后到者必须拿到冲突结论，且**一行都没写**（否则它的槽位被自己第一次请求吃掉，
// 「重新生成后重试一次」的第二次请求会失败在额度上，而不是冲突上）。
func TestDeviceConflictRejectsSecondInstanceOfSameDeviceID(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolLevel(t, db, 1, 0) // device_limit=0 ⇒ 不限，排除额度干扰
	seedDevicePoolUser(t, db, 3101, 0)
	seedDevicePoolUser(t, db, 3102, 0)

	const deviceID = "mirror-device-d1"
	const instanceA = "instance-a"
	const instanceB = "instance-b"

	first := mustRegisterDeviceWithInstance(t, db, 3101, deviceID, instanceA)
	if !first.Allowed {
		t.Fatalf("首个账号首次登记必须放行，实际被拒：reason=%s", first.Reason)
	}

	second := mustRegisterDeviceWithInstance(t, db, 3102, deviceID, instanceB)
	if second.Allowed {
		t.Fatalf("同 device_id + 不同 instance 的第二个账号必须判冲突（S5），实际放行：reason=%s",
			second.Reason)
	}
	if got, want := second.Reason, "device-id-conflict"; got != want {
		t.Fatalf("冲突原因必须是 %q，实际 %q", want, got)
	}
	// 144 §6.3 的两个协议字段都要有：客户端靠 code 决定是否重新生成。
	if got, want := DeviceConflictCode, "DEVICE_ID_CONFLICT"; got != want {
		t.Fatalf("冲突 code 必须是 %q（客户端据它重新生成 device_id），实际 %q", want, got)
	}
	if second.Message == "" {
		t.Fatal("冲突结论必须带用户文案（141 §6.3）")
	}

	// 关键：冲突**不占槽位** —— 第二个账号在库里不该有任何行。
	var rows int64
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("user_id = ?", 3102).Count(&rows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("被判冲突的设备不得落库，实际写了 %d 行（会把槽位吃掉）", rows)
	}
	// 首个账号的行保持完好且记下当时的实例（跨账号判定赖以工作的锚点）。
	if got := readBindingClientInstance(t, db, 3101, deviceID); got != instanceA {
		t.Fatalf("首个账号的 client_instance 应为 %q，实际 %q", instanceA, got)
	}
}

// ─────────────────────────── C2-2 / S6 ───────────────────────────

// TestDeviceConflictAllowsSameInstanceReconnect 是 S6 对照组：同 device_id + 同 instance。
//
// 这是「镜像两半都在跑，但其中一半是同一实例的重连」的情形，141 §4.2 明确正常。
// 变异「instance 必须不同才放行」会让本条红 —— 那条变异会把所有正常重连判成冲突。
func TestDeviceConflictAllowsSameInstanceReconnect(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 3201, 0)
	seedDevicePoolUser(t, db, 3202, 0)

	const deviceID = "mirror-device-d2"
	const instance = "same-instance"

	if d := mustRegisterDeviceWithInstance(t, db, 3201, deviceID, instance); !d.Allowed {
		t.Fatalf("首个账号登记必须放行，实际被拒：reason=%s", d.Reason)
	}
	// 同一 instance 的第二个账号：放行。
	//
	// ⚠️ 它放行的**理由**是「实例相同 = 同一实例重连」，不是「槽位还有空」——
	// 所以这里断的是 reason，而不是只断 Allowed（后者在「不判冲突」的实现下也绿）。
	d := mustRegisterDeviceWithInstance(t, db, 3202, deviceID, instance)
	if !d.Allowed {
		t.Fatalf("同 device_id + 同 instance 必须放行（S6 对照组），实际被拒：reason=%s", d.Reason)
	}
	if d.Reason == "device-id-conflict" {
		t.Fatalf("同实例不得报冲突，实际 reason=%q", d.Reason)
	}
}

// ───────────────────── 红线 1：只判首次登记 ─────────────────────

// TestDeviceConflictOnlyOnFirstRegistration 是「每次重开浏览器不能判冲突」的钉子。
//
// ## 为什么这条是本任务最重要的用例
//
// 141 §4.3 的补丁要求 client_instance **每次浏览器启动重新生成**。因此：
//
//	同一账号、同一 device_id、第二次启动 ⇒ instance 必然不同
//
// 若实现「每次都判」，这条路径会判冲突 → 客户端换 device_id → 旧槽位不释放、
// 新槽位再占一个 → 上限很快被打满，而全程没有任何报错（141 §12 第 2 条）。
//
// 变异方向：把 DecideDeviceRegistration 第 ① 步的「已有活跃行 → 放行」改成
// 「已有活跃行也要比 instance」→ 本条红。
func TestDeviceConflictOnlyOnFirstRegistration(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 3301, 0)

	const deviceID = "reopen-device-d3"

	if d := mustRegisterDeviceWithInstance(t, db, 3301, deviceID, "boot-1"); !d.Allowed {
		t.Fatalf("首次登记必须放行，实际被拒：reason=%s", d.Reason)
	}
	// 第二次启动：换了 instance，必须**放行**（只判首次）。
	if d := mustRegisterDeviceWithInstance(t, db, 3301, deviceID, "boot-2"); !d.Allowed {
		t.Fatalf("同一账号重开浏览器（instance 变了）必须放行 —— 判成冲突会让用户每次重开都换 ID，"+
			"槽位迅速耗尽（141 §12 第 2 条）。实际被拒：reason=%s", d.Reason)
	}
	// 第三次、第四次同样放行（连续重开不能有累积效应）。
	for _, instance := range []string{"boot-3", "boot-4"} {
		if d := mustRegisterDeviceWithInstance(t, db, 3301, deviceID, instance); !d.Allowed {
			t.Fatalf("第 %s 次重开也必须放行，实际被拒：reason=%s", instance, d.Reason)
		}
	}
	// 且全程只有一行（重开不新增槽位）。
	var rows int64
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("user_id = ? AND device_id = ?", 3301, deviceID).Count(&rows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("重开浏览器不得新增绑定行，实际 %d 行", rows)
	}
	// 库里的 instance 跟随最新一次登记（占用判定反映现状，不是陈旧值）。
	if got := readBindingClientInstance(t, db, 3301, deviceID); got != "boot-4" {
		t.Fatalf("client_instance 应更新为最近一次登记值 boot-4，实际 %q", got)
	}
}

// ─────────────────── 边界：空 instance / 历史行 ───────────────────

// TestDeviceConflictSkipsWhenInstanceMissing 覆盖「没有可比对的东西就不判」。
//
// 两个来源都必须跳过：
//   - 本次请求没带 instance（旧客户端 / 扩展还没接 X-Device-Instance）；
//   - 库里已有行是 141 之前登记的历史行（client_instance 默认 ''）。
//
// 不跳过的话，上线瞬间所有老设备都会把别的账号顶掉（'' != UUID ⇒ 冲突）。
func TestDeviceConflictSkipsWhenInstanceMissing(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 3401, 0)
	seedDevicePoolUser(t, db, 3402, 0)

	const deviceID = "legacy-device-d4"

	// ① 本次不带 instance：整体不判冲突（且不写 client_instance，保持 ''）。
	if d := mustRegisterDeviceWithInstance(t, db, 3401, deviceID, ""); !d.Allowed {
		t.Fatalf("不带 instance 的登记必须放行，实际被拒：reason=%s", d.Reason)
	}
	if got := readBindingClientInstance(t, db, 3401, deviceID); got != "" {
		t.Fatalf("不带 instance 时不得写入非空 client_instance，实际 %q", got)
	}
	// ② 另一账号带 instance 来登记同一 device_id：占用集合里只有 ''，
	//    而 '' 表示「不知道」，不构成冲突证据 ⇒ 放行。
	d := mustRegisterDeviceWithInstance(t, db, 3402, deviceID, "fresh-instance")
	if !d.Allowed {
		t.Fatalf("历史空 instance 不构成冲突证据（'' 与 UUID 比较必然不等），实际被拒：reason=%s", d.Reason)
	}
}

// TestDeviceConflictIgnoresRevokedRows 覆盖「撤销 = 释放」。
//
// 撤销的行不占槽位（137 §3.2 的唯一口径），因此也不该继续占据该 device_id 的
// 实例声明 —— 否则用户移除设备后，别人永远接手不了这台设备的标识（成了永久污染）。
func TestDeviceConflictIgnoresRevokedRows(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 3501, 0)
	seedDevicePoolUser(t, db, 3502, 0)

	const deviceID = "revoked-device-d5"

	if d := mustRegisterDeviceWithInstance(t, db, 3501, deviceID, "owner-1"); !d.Allowed {
		t.Fatalf("首个账号登记必须放行，实际被拒：reason=%s", d.Reason)
	}
	if _, err := RevokeDeviceBinding(db, 3501, deviceID, entity.DeviceBindingRevokeReasonUserRemoved); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// 撤销后，另一个实例来登记同一 device_id：占用集合里已无活跃行 ⇒ 放行。
	if d := mustRegisterDeviceWithInstance(t, db, 3502, deviceID, "owner-2"); !d.Allowed {
		t.Fatalf("撤销的行不占实例声明（撤销=释放），实际被拒：reason=%s", d.Reason)
	}
}

// TestDeviceConflictWithoutColumnFailsOpen 覆盖「client_instance 列还没迁移」。
//
// 列缺失时不能报错也不能判冲突：设备池是权益限制不是安全边界
// （device_pool.go 文件头第 2 条），因为少一列就让所有登录失败是不可接受的代价。
func TestDeviceConflictWithoutColumnFailsOpen(t *testing.T) {
	db := newDevicePoolTestDB(t) // 不补 141 的列
	// ⚠️ 2026-11-15 合入 C1 后修：C1 的迁移会把 client_instance 列一并建出来
	// （它调用 ensureDevicePoolBindingSchema，spec 已含该列）。所以「不补列」再也
	// 构造不出缺列态，必须显式 DROP。
	//
	// 前置断言不能省：若哪天 DropColumn 静默失效，本用例会拿「列齐备」的库去测
	// 「缺列」分支 —— 那是自证式通过。
	if err := db.Migrator().DropColumn(&entity.AccountDeviceBinding{}, "client_instance"); err != nil {
		t.Fatalf("drop column client_instance: %v", err)
	}
	if db.Migrator().HasColumn(&entity.AccountDeviceBinding{}, "client_instance") {
		t.Fatalf("前置失效：client_instance 仍未删除，本用例测不到「缺列」分支")
	}
	seedDevicePoolUser(t, db, 3601, 0)
	seedDevicePoolUser(t, db, 3602, 0)

	const deviceID = "premig-device-d6"

	if d := mustRegisterDeviceWithInstance(t, db, 3601, deviceID, "inst-1"); !d.Allowed {
		t.Fatalf("列缺失时首次登记必须放行，实际被拒：reason=%s", d.Reason)
	}
	// 没有 client_instance 列 ⇒ 无法证明占用 ⇒ 一律放行（fail-open）。
	if d := mustRegisterDeviceWithInstance(t, db, 3602, deviceID, "inst-2"); !d.Allowed {
		t.Fatalf("client_instance 列缺失时必须 fail-open 放行，实际被拒：reason=%s", d.Reason)
	}
}

// ──────────────────── C2-4 / S9：心跳超时 ────────────────────

// TestDeviceSessionCountExpiresAfterHeartbeatTimeout 是 S9。
//
// 场景（141 §5.4）：exe 崩溃 → 库里的 session_count 停在崩溃前的 3（比如 3），
// 而实际一个会话都没有。心跳超过 3 分钟没刷新 ⇒ **读取时**必须视为 0。
//
// ## 两个断言缺一不可
//
//  1. 读取结果为 0（S9 的核心：去掉超时判定 → 本条红）；
//  2. 库里**仍是 3**（「视为 0」不是「写回 0」：写回会与 C1 的 CountDevices 抢
//     同一列的写入权，表现为计数忽大忽小且无法归因）。
//
// 断言 2 也是「不要把超时做成定时清理任务」的钉子：清理任务会真的把 3 写成 0。
func TestDeviceSessionCountExpiresAfterHeartbeatTimeout(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 3701, 0)

	const deviceID = "crash-device-d7"
	if d := mustRegisterDeviceWithInstance(t, db, 3701, deviceID, "inst-7"); !d.Allowed {
		t.Fatalf("登记必须放行：reason=%s", d.Reason)
	}

	// 基准时刻与真实校验时钟**同源**（不硬编码具体日期：那种用例写完当天绿、
	// 几小时后自己变红，本仓踩过）。
	now := time.Now().UTC().Truncate(time.Second)

	// ① 新鲜心跳：上报 3 个会话，心跳就在此刻 ⇒ 读到 3。
	if err := ReportDeviceSessionCount(db, 3701, deviceID, 3, now); err != nil {
		t.Fatalf("report fresh: %v", err)
	}
	fresh, err := EffectiveDeviceSessionCount(db, 3701, deviceID, now)
	if err != nil {
		t.Fatalf("read fresh: %v", err)
	}
	if fresh.Count != 3 {
		t.Fatalf("心跳新鲜时应读到 3 个会话，实际 %d（stored=%d fresh=%v）",
			fresh.Count, fresh.Stored, fresh.Fresh)
	}
	if !fresh.Fresh {
		t.Fatal("心跳新鲜时 Fresh 必须为 true（否则调用方无法区分「0 个会话」与「心跳过期」）")
	}

	// ② 超时：3 分 1 秒没上报 ⇒ 读到时视为 0（S9）。
	stale := now.Add(3*time.Minute + time.Second)
	expired, err := EffectiveDeviceSessionCount(db, 3701, deviceID, stale)
	if err != nil {
		t.Fatalf("read stale: %v", err)
	}
	if expired.Count != 0 {
		t.Fatalf("心跳超 3 分钟必须视为 0 个会话（S9：exe 崩溃后不虚高），实际 %d", expired.Count)
	}
	if expired.Stored != 3 {
		t.Fatalf("Stored 必须如实反映库里的原始值 3（排障需要），实际 %d", expired.Stored)
	}
	if expired.Fresh {
		t.Fatal("心跳过期时 Fresh 必须为 false")
	}

	// ③ 库里仍是 3 —— 超时是「读时视为 0」，不是写回 0。
	stored, _, _ := readBindingSessionState(t, db, 3701, deviceID, true)
	if stored != 3 {
		t.Fatalf("心跳超时不得把 session_count 写回 0（会与 CountDevices 抢写入权），"+
			"库里应为 3，实际 %d", stored)
	}

	// ④ 边界：恰好 3 分钟**不算**超时（严格大于才失效，允许漏报 2 次）。
	edge, err := EffectiveDeviceSessionCount(db, 3701, deviceID, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("read edge: %v", err)
	}
	if edge.Count != 3 {
		t.Fatalf("恰好 3 分钟应仍算新鲜（阈值是严格大于），实际 %d", edge.Count)
	}
}

// TestDeviceSessionCountZeroIsNotExpired 覆盖 count=0 的正常态（exe 全部会话关闭）。
//
// 0 与「心跳过期」读出来都是 0，但 Fresh 不同 —— 这就是 Fresh 字段存在的理由。
func TestDeviceSessionCountZeroIsNotExpired(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 3801, 0)

	const deviceID = "closed-device-d8"
	if d := mustRegisterDeviceWithInstance(t, db, 3801, deviceID, "inst-8"); !d.Allowed {
		t.Fatalf("登记必须放行：reason=%s", d.Reason)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := ReportDeviceSessionCount(db, 3801, deviceID, 0, now); err != nil {
		t.Fatalf("report 0: %v", err)
	}
	result, err := EffectiveDeviceSessionCount(db, 3801, deviceID, now)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("上报 0 后应读到 0，实际 %d", result.Count)
	}
	if result.Fresh {
		t.Fatal("count=0 时 Fresh 必须为 false（Fresh 的含义是「有活着的会话」）")
	}
	if !result.ColumnsReady {
		t.Fatal("列就绪时 ColumnsReady 必须为 true —— 否则调用方分不清「0 个会话」与「列还没迁移」")
	}
}

// TestDeviceSessionCountFallsBackToLastSeenWithoutReportedColumn 覆盖缺 session_reported_at 列。
//
// 141 §5.4 允许「新增 session_reported_at 列**或**复用现有 last_seen_at」。
// 当前没有任何迁移建这一列，因此实现必须在缺列时退回 last_seen_at ——
// 而 ReportDeviceSessionCount 每次上报都会刷新 last_seen_at，所以那句「该行最后
// 一次被上报」在 last_seen_at 上成立。缺列时若直接判「无法证明 → 0」，
// S9 在**所有**现有库上都会把活着的会话判死（配额永远按 1 算）。
func TestDeviceSessionCountFallsBackToLastSeenWithoutReportedColumn(t *testing.T) {
	db := newDeviceConflictTestDB(t, false) // 不建 session_reported_at
	seedDevicePoolUser(t, db, 3901, 0)

	const deviceID = "fallback-device-d9"
	if d := mustRegisterDeviceWithInstance(t, db, 3901, deviceID, "inst-9"); !d.Allowed {
		t.Fatalf("登记必须放行：reason=%s", d.Reason)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := ReportDeviceSessionCount(db, 3901, deviceID, 2, now); err != nil {
		t.Fatalf("report: %v", err)
	}
	fresh, err := EffectiveDeviceSessionCount(db, 3901, deviceID, now)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if fresh.Count != 2 {
		t.Fatalf("缺 session_reported_at 列时应退回 last_seen_at，读到 2 个会话，实际 %d", fresh.Count)
	}
	// 超时判定同样要生效（降级不等于关掉 S9）。
	stale, err := EffectiveDeviceSessionCount(db, 3901, deviceID, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("read stale: %v", err)
	}
	if stale.Count != 0 {
		t.Fatalf("退回 last_seen_at 后超时判定仍必须生效（S9），实际 %d", stale.Count)
	}
}

// ────────────────── C2-3 的服务端部分：上报语义 ──────────────────

// TestReportDeviceSessionCountRejectsForeignAndRevoked 覆盖 404 的三态同一口径。
//
// 不存在 / 不属于本账号 / 已撤销 必须返回**同一个** error（ErrDeviceSessionBindingNotFound）。
// 分开返回等于告诉攻击者「这个 deviceId 存在但不属于你」（141 §6.2 / device_user.go 红线 1）。
func TestReportDeviceSessionCountRejectsForeignAndRevoked(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 4001, 0)
	seedDevicePoolUser(t, db, 4002, 0)

	const deviceID = "owned-device-d10"
	if d := mustRegisterDeviceWithInstance(t, db, 4001, deviceID, "inst-10"); !d.Allowed {
		t.Fatalf("登记必须放行：reason=%s", d.Reason)
	}
	now := time.Now().UTC()

	// ① 别的账号：拒绝（同一错误）。
	if err := ReportDeviceSessionCount(db, 4002, deviceID, 1, now); err != ErrDeviceSessionBindingNotFound {
		t.Fatalf("别的账号上报必须返回 ErrDeviceSessionBindingNotFound，实际 %v", err)
	}
	// ② 根本不存在的 device_id：同一错误。
	if err := ReportDeviceSessionCount(db, 4001, "never-registered", 1, now); err != ErrDeviceSessionBindingNotFound {
		t.Fatalf("不存在的 device_id 必须返回同一个错误（不泄露存在性），实际 %v", err)
	}
	// ③ 已撤销：同一错误。
	if _, err := RevokeDeviceBinding(db, 4001, deviceID, entity.DeviceBindingRevokeReasonUserRemoved); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := ReportDeviceSessionCount(db, 4001, deviceID, 1, now); err != ErrDeviceSessionBindingNotFound {
		t.Fatalf("已撤销的设备上报必须返回同一个错误，实际 %v", err)
	}
}

// TestReportDeviceSessionCountValidatesRange 覆盖 count 的闭区间 0..8。
//
// 8 来自 HOME_MAX_SESSIONS。越界必须是 400 类错误（可修正的输入错误），
// 而不是静默夹取 —— 静默夹取会让 exe 侧永远不知道自己算错了上界。
func TestReportDeviceSessionCountValidatesRange(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 4101, 0)

	const deviceID = "range-device-d11"
	if d := mustRegisterDeviceWithInstance(t, db, 4101, deviceID, "inst-11"); !d.Allowed {
		t.Fatalf("登记必须放行：reason=%s", d.Reason)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if got, want := DeviceSessionMaxCount, 8; got != want {
		t.Fatalf("会话数上界必须是 %d（HOME_MAX_SESSIONS），实际 %d", want, got)
	}
	for _, bad := range []int{-1, 9, 100} {
		if err := ReportDeviceSessionCount(db, 4101, deviceID, bad, now); err != ErrDeviceSessionCountOutOfRange {
			t.Fatalf("count=%d 必须被拒绝（0..8），实际 err=%v", bad, err)
		}
	}
	// 边界两端都合法。
	for _, ok := range []int{0, 8} {
		if err := ReportDeviceSessionCount(db, 4101, deviceID, ok, now); err != nil {
			t.Fatalf("count=%d 必须合法（闭区间 0..8），实际 err=%v", ok, err)
		}
	}
}

// TestReportDeviceSessionCountIsIdempotentAndLastWriterWins 覆盖重复/乱序上报。
//
// 协议里没有客户端时间戳（141 §6.2 只有 {deviceId, count}），所以语义必然是
// 「后到者覆盖」：同一 count 重复上报幂等，乱序到达的旧值由下一次心跳自然纠正。
// 这条不是「免测」而是**明确的行为约定** —— 若实现改成「count 只能增不能减」，
// exe 关闭会话（-1）就永远同步不下来，R7 的回落直接失效。
func TestReportDeviceSessionCountIsIdempotentAndLastWriterWins(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 4201, 0)

	const deviceID = "idem-device-d12"
	if d := mustRegisterDeviceWithInstance(t, db, 4201, deviceID, "inst-12"); !d.Allowed {
		t.Fatalf("登记必须放行：reason=%s", d.Reason)
	}
	base := time.Now().UTC().Truncate(time.Second)

	// 重复上报同一 count：幂等，不报错。
	for i := 0; i < 3; i++ {
		if err := ReportDeviceSessionCount(db, 4201, deviceID, 2, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("第 %d 次重复上报必须幂等成功，实际 %v", i+1, err)
		}
	}
	if count, _, _ := readBindingSessionState(t, db, 4201, deviceID, true); count != 2 {
		t.Fatalf("重复上报后 session_count 应为 2，实际 %d", count)
	}
	// 乱序：先写 3 再收到旧的 1 ⇒ 后到者覆盖（1）。
	if err := ReportDeviceSessionCount(db, 4201, deviceID, 3, base.Add(10*time.Second)); err != nil {
		t.Fatalf("report 3: %v", err)
	}
	if err := ReportDeviceSessionCount(db, 4201, deviceID, 1, base.Add(5*time.Second)); err != nil {
		t.Fatalf("report 1: %v", err)
	}
	if count, _, _ := readBindingSessionState(t, db, 4201, deviceID, true); count != 1 {
		t.Fatalf("后到者覆盖（无客户端时间戳可比较），应写 1，实际 %d", count)
	}
	// 回落：exe 全关上报 0（R7）。若实现改成「只能增」这里会停在 1。
	if err := ReportDeviceSessionCount(db, 4201, deviceID, 0, base.Add(11*time.Second)); err != nil {
		t.Fatalf("report 0: %v", err)
	}
	if count, _, _ := readBindingSessionState(t, db, 4201, deviceID, true); count != 0 {
		t.Fatalf("exe 全关后必须能回落到 0（R7），实际 %d", count)
	}
}

// TestReportDeviceSessionCountRefreshesHeartbeat 覆盖「上报同时刷新心跳」。
//
// 141 §6.2 的「同时刷新 last_seen_at」是 S9 的前提：心跳的时间戳就是上报时间。
// 不刷的话，所有会话在 3 分钟后都会被判死（配额永远按 1 算）。
func TestReportDeviceSessionCountRefreshesHeartbeat(t *testing.T) {
	db := newDeviceConflictTestDB(t, true)
	seedDevicePoolUser(t, db, 4301, 0)

	const deviceID = "heartbeat-device-d13"
	if d := mustRegisterDeviceWithInstance(t, db, 4301, deviceID, "inst-13"); !d.Allowed {
		t.Fatalf("登记必须放行：reason=%s", d.Reason)
	}
	// 先把心跳拨到很久以前（模拟 exe 已经停止上报一段时间）。
	old := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("user_id = ? AND device_id = ?", 4301, deviceID).
		UpdateColumns(map[string]any{
			"last_seen_at":        old,
			"session_reported_at": old,
		}).Error; err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := ReportDeviceSessionCount(db, 4301, deviceID, 4, now); err != nil {
		t.Fatalf("report: %v", err)
	}
	_, lastSeen, reportedAt := readBindingSessionState(t, db, 4301, deviceID, true)
	if lastSeen == nil || lastSeen.Sub(now) > time.Second || now.Sub(*lastSeen) > time.Second {
		t.Fatalf("上报必须刷新 last_seen_at 到 %v，实际 %v", now, lastSeen)
	}
	if reportedAt == nil || reportedAt.Sub(now) > time.Second || now.Sub(*reportedAt) > time.Second {
		t.Fatalf("上报必须刷新 session_reported_at 到 %v，实际 %v", now, reportedAt)
	}
	// 刷新后立即读：4 个会话是新鲜的（不是过期）。
	result, err := EffectiveDeviceSessionCount(db, 4301, deviceID, now)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.Count != 4 {
		t.Fatalf("心跳刚刷新应读到 4，实际 %d", result.Count)
	}
}

// mustRegisterDeviceWithInstance 登记并断言「没有写失败」（err 必须为 nil）。
//
// 与 device_pool_test.go 的 mustRegisterDevice 分工：那个不带 instance，
// 这个显式传实例标识。抽出来是为了让每条用例只表达自己的判据。
func mustRegisterDeviceWithInstance(t *testing.T, db *gorm.DB, userID int32, deviceID, instance string) DevicePoolDecision {
	t.Helper()
	decision, err := RegisterDeviceWithInstance(db, userID, deviceID, instance)
	if err != nil {
		t.Fatalf("RegisterDeviceWithInstance(%d,%s,%s): %v", userID, deviceID, instance, err)
	}
	return decision
}

// ───────────── 纯函数边界：effectiveDeviceSessionCount ─────────────

// TestEffectiveDeviceSessionCountClampsAndExpires 直接钉纯函数的四条分支。
//
// 这样即使上层（EffectiveDeviceSessionCount）的查询路径变了，心跳语义本身仍有钉子。
func TestEffectiveDeviceSessionCountClampsAndExpires(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fresh := now.Add(-30 * time.Second)
	stale := now.Add(-3*time.Minute - time.Second)

	cases := []struct {
		name      string
		stored    int
		heartbeat *time.Time
		want      int
	}{
		{"零会话", 0, &fresh, 0},
		{"负数脏数据", -5, &fresh, 0},
		{"新鲜的三会话", 3, &fresh, 3},
		{"新鲜但超界的值被夹到 8", 99, &fresh, 8},
		{"过期的三会话视为 0（S9）", 3, &stale, 0},
		{"过期且超界仍为 0", 99, &stale, 0},
		{"没有心跳时间视为 0", 3, nil, 0},
	}
	for _, tc := range cases {
		if got := effectiveDeviceSessionCount(tc.stored, tc.heartbeat, now); got != tc.want {
			t.Errorf("%s：期望 %d，实际 %d（stored=%d heartbeat=%v）",
				tc.name, tc.want, got, tc.stored, tc.heartbeat)
		}
	}
}
