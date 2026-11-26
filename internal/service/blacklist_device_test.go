package service

import (
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 设备粒度黑名单 + 设备管理员端服务层（137 §3.4 / §5.1 / §8.2）的钉子。
//
// # 为什么这些用例必须跑真库
//
// 本文件的被测对象几乎全是「两条写路径必须同时成立」的形态：
// 封禁 = 写黑名单 + 撤销绑定（D12）；解绑 = 只写绑定不写黑名单。
// 用 mock 只能证明「我们调用了一个函数」，证明不了「那个函数改的是同一批行」。
// 所以一律真 sqlite（newBlacklistTestDB 同款隔离命名）+ 真 GORM 语句。

// newDeviceAdminTestDB 在黑名单测试库上补建绑定表（含 A1-2 的五列）。
//
// ⚠️ 为什么要手工 ALTER：`entity.AccountDeviceBinding` 目前**只声明了**
// user_id / device_id / first_seen_at / last_seen_at 四列（A1-2 尚未落地）。
// 生产由 registry 的迁移补列，测试库没有任何迁移入口，因此这里显式补上 ——
// 这是「模拟 A1-2 已落地」的最小手段。
//
// 不补的话，封禁会走 RevocationWarning 分支（撤销未生效），
// 于是 D12 的断言变成「什么都没做也绿」—— 那正是本仓反复记过的假钉子形态。
func newDeviceAdminTestDB(t *testing.T, withRevocationColumns bool) *gorm.DB {
	t.Helper()

	db := newBlacklistTestDB(t)
	if err := db.AutoMigrate(&entity.AccountDeviceBinding{}); err != nil {
		t.Fatalf("migrate account_device_bindings: %v", err)
	}
	if withRevocationColumns {
		// 列类型与 137 §3.2 一致（revoked_at 用 DATETIME NULL / revoked_reason 用 VARCHAR NOT NULL DEFAULT ''）。
		//
		// ⚠️ 必须逐列判断「是否已存在」再补（2026-11-15 合入 A1 后修）：
		// `entity.AccountDeviceBinding` 在 A1 落地后**本身就带这 5 个字段**，
		// AutoMigrate 会把它们一并建出来。此处再无条件 ALTER 会报
		// `duplicate column name: owner_kind`，导致 A3 的 16 条用例全红。
		//
		// 保留这段补列逻辑而不删掉：A3 的用例需要能在**没有 A1 实体字段**的库上
		// 也跑通（`withRevocationColumns=true` 就是为「列齐备」这个前提服务的）。
		// 判断用 HasColumn 而不是吞错 —— 吞掉 ALTER 错误会让「真的没加上列」
		// 变成静默通过，那正是「测试保护了不存在的事实」的变体。
		for _, col := range []struct{ name, ddl string }{
			{"owner_kind", "ALTER TABLE account_device_bindings ADD COLUMN owner_kind VARCHAR(16) NOT NULL DEFAULT 'login'"},
			{"token_id", "ALTER TABLE account_device_bindings ADD COLUMN token_id INTEGER NOT NULL DEFAULT 0"},
			{"device_label", "ALTER TABLE account_device_bindings ADD COLUMN device_label VARCHAR(64) NOT NULL DEFAULT ''"},
			{"revoked_at", "ALTER TABLE account_device_bindings ADD COLUMN revoked_at DATETIME NULL"},
			{"revoked_reason", "ALTER TABLE account_device_bindings ADD COLUMN revoked_reason VARCHAR(32) NOT NULL DEFAULT ''"},
		} {
			if db.Migrator().HasColumn(&entity.AccountDeviceBinding{}, col.name) {
				continue
			}
			if err := db.Exec(col.ddl).Error; err != nil {
				t.Fatalf("add column (%s): %v", col.ddl, err)
			}
		}
	}
	return db
}

// seedDeviceBinding 落一行绑定。revoked 为真时写撤销列（仅在补过列时可用）。
func seedDeviceBinding(t *testing.T, db *gorm.DB, userID int32, deviceID string, revoked bool) entity.AccountDeviceBinding {
	t.Helper()

	now := time.Now().UTC()
	row := entity.AccountDeviceBinding{
		UserID:      userID,
		DeviceID:    deviceID,
		FirstSeenAt: now.Add(-2 * time.Hour),
		LastSeenAt:  now.Add(-time.Minute),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	if revoked {
		if err := db.Model(&entity.AccountDeviceBinding{}).Where("id = ?", row.ID).
			Updates(map[string]any{
				"revoked_at":     now.Add(-30 * time.Minute),
				"revoked_reason": "user_removed",
			}).Error; err != nil {
			t.Fatalf("mark binding revoked: %v", err)
		}
	}
	return row
}

// countDeviceEntries 数某个 device 的黑名单规则条数。
func countDeviceEntries(t *testing.T, db *gorm.DB, deviceID string) int64 {
	t.Helper()

	var count int64
	if err := db.Model(&entity.BlacklistEntry{}).
		Where("kind = ? AND value_hash = ?", BlacklistKindDevice, BlacklistPatternHashHex(deviceID)).
		Count(&count).Error; err != nil {
		t.Fatalf("count device entries: %v", err)
	}
	return count
}

// reloadDeviceSnapshot 加载一份快照并打开总开关（开关关闭时快照里没有规则）。
func reloadDeviceSnapshot(t *testing.T, db *gorm.DB) *BlacklistSnapshot {
	t.Helper()

	setBlacklistEnabled(t, db, "true")
	snapshot, err := LoadBlacklistSnapshot(db, time.Now())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if !snapshot.Enabled {
		t.Fatalf("snapshot.Enabled 必须为 true —— 否则后面的「命中」断言毫无意义")
	}
	return snapshot
}

// ---------------------------------------------------------------------------
// 归一化 / 掩码
// ---------------------------------------------------------------------------

func TestNormalizeBlacklistDeviceIDTrimsAndTruncates(t *testing.T) {
	if got := NormalizeBlacklistDeviceID("  exe:a1b2  "); got != "exe:a1b2" {
		t.Fatalf("必须去首尾空白，got %q", got)
	}
	// 不转小写：两台指纹只差大小写是两台不同设备（与 wechat OpenID 同一取舍）。
	if got := NormalizeBlacklistDeviceID("EXE:A1B2"); got != "EXE:A1B2" {
		t.Fatalf("不得转小写（设备指纹大小写敏感），got %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := NormalizeBlacklistDeviceID(long); len([]rune(got)) != 191 {
		t.Fatalf("必须截断到 191 字符（与 device_id varchar(191) 对齐），got %d", len([]rune(got)))
	}
	if got := NormalizeBlacklistDeviceID("   "); got != "" {
		t.Fatalf("全空白必须归一化成空串，got %q", got)
	}
}

func TestMaskBlacklistDeviceIDKeepsBothEnds(t *testing.T) {
	masked := MaskBlacklistDeviceID("exe:a1b2c3d4e5f6")
	if !strings.HasPrefix(masked, "exe:a1b2") {
		t.Fatalf("掩码必须保留前 8 位（运营要能核对是哪台设备），got %q", masked)
	}
	if !strings.HasSuffix(masked, "e5f6") {
		t.Fatalf("掩码必须保留末 4 位（同前缀设备靠它区分），got %q", masked)
	}
	if strings.Contains(masked, "c3d4") {
		t.Fatalf("掩码不得泄漏中段，got %q", masked)
	}
	if got := MaskBlacklistDeviceID("abc"); got != "a***" {
		t.Fatalf("短 ID 只留首字符，got %q", got)
	}
	if got := MaskBlacklistDeviceID("  "); got != "***" {
		t.Fatalf("空 ID 掩码为 ***，got %q", got)
	}
}

// ---------------------------------------------------------------------------
// A3-1：kind=device 进缓存 + MatchDevice
// ---------------------------------------------------------------------------

func TestBlacklistDeviceBanIsMatchedFromCache(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)

	result, err := BanDevice(db, DeviceBanRequest{
		DeviceID:  "  exe:a1b2c3d4  ", // 故意带空白：写入侧必须归一化
		Reason:    "工作室批量刷题",
		CreatedBy: 9101,
	})
	if err != nil {
		t.Fatalf("ban device: %v", err)
	}
	if result.DeviceID != "exe:a1b2c3d4" {
		t.Fatalf("落库的 deviceId 必须归一化（去空白），got %q", result.DeviceID)
	}
	if result.EntryID <= 0 {
		t.Fatalf("必须返回新建规则的 entryId，got %d", result.EntryID)
	}
	if !result.Permanent || result.ExpiresAt != nil {
		t.Fatalf("expiresAt 缺省必须是永久封禁（ExpiresAt=nil, Permanent=true），got %+v", result)
	}

	snapshot := reloadDeviceSnapshot(t, db)
	match, hit := snapshot.MatchDevice("exe:a1b2c3d4")
	if !hit {
		t.Fatalf("封禁后必须命中；快照规则数=%d（若为 0，说明 device 分支没进缓存）", snapshot.RuleCount)
	}
	if match.Kind != BlacklistKindDevice {
		t.Fatalf("命中结果的 kind 必须是 device，got %q", match.Kind)
	}
	if match.EntryID != result.EntryID {
		t.Fatalf("命中结果必须回指规则 entryId=%d，got %d", result.EntryID, match.EntryID)
	}
	if match.Reason != "工作室批量刷题" {
		t.Fatalf("命中结果必须带 reason（供被拦用户理解），got %q", match.Reason)
	}
	// 归一化在判定侧也必须生效：带空白的输入同样命中。
	if _, hit := snapshot.MatchDevice("   exe:a1b2c3d4 \t"); !hit {
		t.Fatalf("判定侧必须同样做归一化（去空白），否则「封禁成功但从不命中」")
	}
	// 未封禁的设备不得命中。
	if _, hit := snapshot.MatchDevice("exe:ffffffff"); hit {
		t.Fatalf("未封禁设备不得命中")
	}
	// 空 device_id 永不命中：否则 sha256("") 这一项会封掉所有无指纹请求。
	if _, hit := snapshot.MatchDevice("   "); hit {
		t.Fatalf("空 device_id 不得命中")
	}
}

func TestBlacklistDeviceCacheSkipsDisabledAndExpiredEntries(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)

	// ① 停用：直接落一条 enabled=false 的 device 规则（走 seedBlacklistEntry 的两步写法）。
	disabled := seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind:      BlacklistKindDevice,
		Pattern:   "exe:disabled0001",
		MatchType: BlacklistMatchExact,
		Reason:    "已停用",
		Enabled:   false,
	})
	// ② 已过期。
	past := time.Now().Add(-time.Hour).UTC()
	seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind:      BlacklistKindDevice,
		Pattern:   "exe:expired00001",
		MatchType: BlacklistMatchExact,
		Reason:    "已过期",
		Enabled:   true,
		ExpiresAt: &past,
	})
	// ③ 对照组：生效中的一条。没有它，「三条都不命中」这种全错实现也会绿。
	future := time.Now().Add(time.Hour).UTC()
	active := seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind:      BlacklistKindDevice,
		Pattern:   "exe:active000001",
		MatchType: BlacklistMatchExact,
		Reason:    "生效中",
		Enabled:   true,
		ExpiresAt: &future,
	})

	snapshot := reloadDeviceSnapshot(t, db)

	if _, hit := snapshot.MatchDevice("exe:disabled0001"); hit {
		t.Fatalf("Enabled=false 的 device 规则不得进缓存（规则 %d 仍被命中）", disabled.ID)
	}
	if _, hit := snapshot.MatchDevice("exe:expired00001"); hit {
		t.Fatalf("ExpiresAt 已过期的 device 规则不得进缓存（过期判定必须生效）")
	}
	if _, hit := snapshot.MatchDevice("exe:active000001"); !hit {
		t.Fatalf("生效中的 device 规则必须命中（规则 %d）—— 否则本条用例的对照组失效", active.ID)
	}
	// 规则计数必须只含那一条：若 device 分支忘了从 RuleCount 里排除，
	// 下面的数字会变成 3，能立刻暴露「停用/过期被收进缓存」。
	if snapshot.RuleCount != 1 {
		t.Fatalf("RuleCount 必须只统计生效中的规则，期望 1，实际 %d", snapshot.RuleCount)
	}
}

func TestBlacklistDeviceEmptyPatternIsSkipped(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)

	// 手工插一条空白 pattern 的 device 规则（模拟脏数据/导入错误）。
	blank := seedBlacklistEntry(t, db, entity.BlacklistEntry{
		Kind:      BlacklistKindDevice,
		Pattern:   "   ",
		MatchType: BlacklistMatchExact,
		Reason:    "脏数据",
		Enabled:   true,
	})

	snapshot := reloadDeviceSnapshot(t, db)
	if snapshot.SkippedCount != 1 {
		t.Fatalf("空白 device 规则必须被跳过并计入 SkippedCount，期望 1，实际 %d", snapshot.SkippedCount)
	}
	if _, hit := snapshot.MatchDevice(""); hit {
		t.Fatalf("规则 %d 的空白 pattern 不得让空 device_id 命中", blank.ID)
	}
	if snapshot.RuleCount != 0 {
		t.Fatalf("RuleCount 必须为 0（唯一一条被跳过），实际 %d", snapshot.RuleCount)
	}
}

// ---------------------------------------------------------------------------
// A3-4：封禁必须同时撤销全部在用绑定（D12）
// ---------------------------------------------------------------------------

func TestBanDeviceRevokesAllActiveBindings(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)

	// 同一台设备被两个账号用过（工作室的典型形态），另有一行本来就已撤销。
	first := seedDeviceBinding(t, db, 9201, "exe:shared000001", false)
	second := seedDeviceBinding(t, db, 9202, "exe:shared000001", false)
	alreadyRevoked := seedDeviceBinding(t, db, 9203, "exe:shared000001", true)
	// 对照：另一台设备的在用绑定**不得**被牵连。
	other := seedDeviceBinding(t, db, 9201, "exe:other0000001", false)

	result, err := BanDevice(db, DeviceBanRequest{
		DeviceID: "exe:shared000001",
		Reason:   "工作室批量刷题",
	})
	if err != nil {
		t.Fatalf("ban device: %v", err)
	}
	if !result.RevocationApplied {
		t.Fatalf("撤销列齐备时必须真的执行撤销，got RevocationApplied=false（warning=%q）", result.RevocationWarning)
	}
	if result.RevokedBindings != 2 {
		t.Fatalf("必须撤销该 device 的**全部**在用绑定（2 行），实际 %d —— 只撤销部分会让某个账号仍在线", result.RevokedBindings)
	}

	load := func(id int64) entity.AccountDeviceBinding {
		t.Helper()
		var row entity.AccountDeviceBinding
		if err := db.Where("id = ?", id).Take(&row).Error; err != nil {
			t.Fatalf("reload binding %d: %v", id, err)
		}
		return row
	}

	for _, pair := range []struct {
		name string
		id   int64
	}{
		{"第一台在用绑定", first.ID},
		{"第二台在用绑定", second.ID},
	} {
		var revokedAt *time.Time
		var reason string
		if err := db.Raw("SELECT revoked_at, revoked_reason FROM account_device_bindings WHERE id = ?", pair.id).
			Row().Scan(&revokedAt, &reason); err != nil {
			t.Fatalf("read revocation of %s: %v", pair.name, err)
		}
		if revokedAt == nil {
			t.Fatalf("%s（id=%d）必须被写 revoked_at —— D12：封禁只写黑名单不撤销绑定", pair.name, pair.id)
		}
		if reason != DeviceBindingRevokedReasonBanned {
			t.Fatalf("%s 的 revoked_reason 必须是 device_banned，got %q", pair.name, reason)
		}
	}

	// 已撤销的行不得被覆盖（首次撤销时间戳是申诉证据）。
	revokedRow := load(alreadyRevoked.ID)
	if revokedRow.DeviceID != "exe:shared000001" {
		t.Fatalf("绑定行内容不得变，got %+v", revokedRow)
	}
	var existingReason string
	if err := db.Raw("SELECT revoked_reason FROM account_device_bindings WHERE id = ?", alreadyRevoked.ID).
		Row().Scan(&existingReason); err != nil {
		t.Fatalf("read existing revocation: %v", err)
	}
	if existingReason != "user_removed" {
		t.Fatalf("已撤销行的原因不得被覆盖成 device_banned（那是另一件事的事实），got %q", existingReason)
	}

	// 对照：别的设备不受影响。
	var otherRevokedAt *time.Time
	if err := db.Raw("SELECT revoked_at FROM account_device_bindings WHERE id = ?", other.ID).
		Row().Scan(&otherRevokedAt); err != nil {
		t.Fatalf("read other binding: %v", err)
	}
	if otherRevokedAt != nil {
		t.Fatalf("封禁只作用于目标 device，不得撤销其它设备（id=%d）", other.ID)
	}

	// 黑名单侧：一条规则 + 缓存命中。
	if got := countDeviceEntries(t, db, "exe:shared000001"); got != 1 {
		t.Fatalf("必须只有一条 device 规则（复用 blacklist_entries，不新建表），实际 %d", got)
	}
	snapshot := reloadDeviceSnapshot(t, db)
	if _, hit := snapshot.MatchDevice("exe:shared000001"); !hit {
		t.Fatalf("封禁后缓冲必须命中")
	}
}

func TestBanDeviceIsIdempotentAndRepeatedCallRevokesNothing(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	binding := seedDeviceBinding(t, db, 9301, "exe:repeat000001", false)

	first, err := BanDevice(db, DeviceBanRequest{DeviceID: "exe:repeat000001", Reason: "第一次"})
	if err != nil {
		t.Fatalf("first ban: %v", err)
	}
	if first.RevokedBindings != 1 {
		t.Fatalf("首次封禁应撤销 1 行，实际 %d", first.RevokedBindings)
	}
	if first.AlreadyBanned {
		t.Fatalf("首次封禁不能报 AlreadyBanned")
	}

	later := time.Now().Add(2 * time.Hour).UTC()
	second, err := BanDevice(db, DeviceBanRequest{
		DeviceID:  "exe:repeat000001",
		Reason:    "第二次",
		ExpiresAt: &later,
	})
	if err != nil {
		t.Fatalf("second ban: %v", err)
	}
	if second.EntryID != first.EntryID {
		t.Fatalf("重复封禁必须复用同一行，entryId 首次=%d 第二次=%d", first.EntryID, second.EntryID)
	}
	if !second.AlreadyBanned {
		t.Fatalf("第二次封禁必须标记 AlreadyBanned（页面要据此显示「更新」而不是「新增」）")
	}
	if second.RevokedBindings != 0 {
		t.Fatalf("第二次封禁不得再撤销任何行（revoked_at IS NULL 判据），实际 %d", second.RevokedBindings)
	}
	// 到期时间与原因必须被更新（否则「延长封禁」不生效）。
	if second.ExpiresAt == nil || !second.ExpiresAt.After(time.Now()) {
		t.Fatalf("第二次封禁必须把 expiresAt 更新为新的未来时间，got %v", second.ExpiresAt)
	}
	if second.Reason != "第二次" {
		t.Fatalf("原因必须更新，got %q", second.Reason)
	}
	if got := countDeviceEntries(t, db, "exe:repeat000001"); got != 1 {
		t.Fatalf("重复封禁不得产生第二行，实际 %d", got)
	}
	// 首次撤销的时间戳不许被第二次覆盖。
	var revokedAt time.Time
	if err := db.Raw("SELECT revoked_at FROM account_device_bindings WHERE id = ?", binding.ID).
		Row().Scan(&revokedAt); err != nil {
		t.Fatalf("read revoked_at: %v", err)
	}
	if revokedAt.IsZero() {
		t.Fatalf("绑定必须已撤销")
	}
	if revokedAt.After(time.Now()) {
		t.Fatalf("撤销时间戳被覆盖了（应保持首次封禁时刻），got %v", revokedAt)
	}
}

func TestBanDeviceWithoutRevocationColumnsStillWritesBlacklist(t *testing.T) {
	// 「A1-2 尚未落地的库」这个场景，在 A1 合入后**不能再靠「不补列」构造** ——
	// `entity.AccountDeviceBinding` 已带那 5 个字段，AutoMigrate 一定会建出来。
	// 必须显式 DROP 掉撤销列，才是真的「老库缺列」。
	db := newDeviceAdminTestDB(t, false)
	for _, col := range []string{"revoked_at", "revoked_reason"} {
		if err := db.Migrator().DropColumn(&entity.AccountDeviceBinding{}, col); err != nil {
			t.Fatalf("drop column %s: %v", col, err)
		}
	}
	// 前置断言：确认真的缺列了。缺了这层判断，若哪天 DropColumn 静默失效，
	// 本用例会拿「列齐备」的库去测「缺列」分支 —— 那正是自证式通过。
	if db.Migrator().HasColumn(&entity.AccountDeviceBinding{}, "revoked_at") {
		t.Fatalf("前置失效：revoked_at 仍未删除，本用例测不到「缺列」分支")
	}

	result, err := BanDevice(db, DeviceBanRequest{DeviceID: "exe:nocolumn0001", Reason: "结构未就绪"})
	if err != nil {
		t.Fatalf("缺撤销列时封禁本身必须成功（黑名单判定同步生效），err=%v", err)
	}
	if result.RevocationApplied {
		t.Fatalf("缺列时不得声称已撤销（那会让 D12 被空实现骗过），got RevocationApplied=true")
	}
	if result.RevocationWarning == "" {
		t.Fatalf("缺列时必须返回可展示的警告，否则运营会以为设备已被踢下线")
	}
	if result.RevokedBindings != 0 {
		t.Fatalf("缺列时撤销行数必须为 0，实际 %d", result.RevokedBindings)
	}
	if got := countDeviceEntries(t, db, "exe:nocolumn0001"); got != 1 {
		t.Fatalf("黑名单规则必须照写，实际 %d 条", got)
	}
	snapshot := reloadDeviceSnapshot(t, db)
	if _, hit := snapshot.MatchDevice("exe:nocolumn0001"); !hit {
		t.Fatalf("缺撤销列时黑名单仍必须命中（这是封禁的第二道、同步的保险）")
	}
}

func TestBanDeviceRejectsEmptyDeviceIDAndReason(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)

	if _, err := BanDevice(db, DeviceBanRequest{DeviceID: "   ", Reason: "x"}); err == nil {
		t.Fatalf("空 device_id 必须报错（不能让一条脏规则封掉全站）")
	}
	if _, err := BanDevice(db, DeviceBanRequest{DeviceID: "exe:ok0000000001", Reason: "   "}); err == nil {
		t.Fatalf("空 reason 必须报错（审计里出现「无原因」等于没有审计）")
	}
	if got := countDeviceEntries(t, db, "exe:ok0000000001"); got != 0 {
		t.Fatalf("被拒的封禁不得留下任何规则，实际 %d 条", got)
	}
}

func TestUnbanDeviceRemovesRuleFromCache(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	if _, err := BanDevice(db, DeviceBanRequest{DeviceID: "exe:unban0000001", Reason: "误封"}); err != nil {
		t.Fatalf("ban: %v", err)
	}
	if _, hit := reloadDeviceSnapshot(t, db).MatchDevice("exe:unban0000001"); !hit {
		t.Fatalf("前提不成立：封禁后应命中")
	}

	result, err := UnbanDevice(db, "  exe:unban0000001 ")
	if err != nil {
		t.Fatalf("unban: %v", err)
	}
	if result.Deleted != 1 {
		t.Fatalf("必须删掉 1 条规则，实际 %d", result.Deleted)
	}
	if result.DeviceID != "exe:unban0000001" {
		t.Fatalf("返回的 deviceId 必须归一化，got %q", result.DeviceID)
	}
	if _, hit := reloadDeviceSnapshot(t, db).MatchDevice("exe:unban0000001"); hit {
		t.Fatalf("解封后不得再命中")
	}

	// 幂等：再解一次不报错、删 0 条。
	again, err := UnbanDevice(db, "exe:unban0000001")
	if err != nil {
		t.Fatalf("second unban must not fail: %v", err)
	}
	if again.Deleted != 0 {
		t.Fatalf("重复解封必须删 0 条，实际 %d", again.Deleted)
	}
}

// ---------------------------------------------------------------------------
// A3-3：强制解绑只断绑定，不封设备
// ---------------------------------------------------------------------------

func TestUnbindDeviceBindingDoesNotBan(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	binding := seedDeviceBinding(t, db, 9401, "exe:unbind000001", false)

	item, err := UnbindDeviceBindingWithDB(db, binding.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if item.RevokedAt == nil {
		t.Fatalf("必须写 revoked_at")
	}
	if item.RevokedReason != DeviceBindingRevokedReasonAdminUnbound {
		t.Fatalf("revoked_reason 必须是 admin_unbound，got %q", item.RevokedReason)
	}
	if item.Status != AdminDeviceStatusRevoked {
		t.Fatalf("解绑后状态必须是 revoked，got %q", item.Status)
	}
	if item.Username != "" && item.UserID != 9401 {
		t.Fatalf("返回行必须对应原账号，got userId=%d", item.UserID)
	}

	// 红线：解绑绝不能写黑名单（那会让用户无法自救）。
	if got := countDeviceEntries(t, db, "exe:unbind000001"); got != 0 {
		t.Fatalf("强制解绑不得写黑名单规则，实际 %d 条 —— 解绑 ≠ 封禁", got)
	}
	snapshot := reloadDeviceSnapshot(t, db)
	if _, hit := snapshot.MatchDevice("exe:unbind000001"); hit {
		t.Fatalf("解绑后设备不得被判定为封禁")
	}

	// 行必须保留（撤销是既成事实，删行会让申诉无从查证）。
	var count int64
	if err := db.Model(&entity.AccountDeviceBinding{}).Where("id = ?", binding.ID).Count(&count).Error; err != nil {
		t.Fatalf("count binding: %v", err)
	}
	if count != 1 {
		t.Fatalf("解绑不得删除绑定行（137 §3.2），实际剩余 %d", count)
	}
}

func TestUnbindDeviceBindingIsIdempotentAndKeepsFirstTimestamp(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	binding := seedDeviceBinding(t, db, 9402, "exe:unbindidem01", false)

	firstAt := time.Now().Add(-time.Hour).UTC()
	if _, err := UnbindDeviceBindingWithDB(db, binding.ID, firstAt); err != nil {
		t.Fatalf("first unbind: %v", err)
	}
	// 第二次用一个明显更晚的时刻：若实现覆盖时间戳，读回的就是 later。
	later := time.Now().UTC()
	item, err := UnbindDeviceBindingWithDB(db, binding.ID, later)
	if err != nil {
		t.Fatalf("second unbind must not fail: %v", err)
	}
	if item.RevokedAt == nil {
		t.Fatalf("第二次调用仍必须返回撤销状态")
	}
	if delta := item.RevokedAt.Sub(firstAt); delta > time.Second || delta < -time.Second {
		t.Fatalf("首次撤销时间戳不得被覆盖（那是申诉证据）：期望 %v，实际 %v", firstAt, *item.RevokedAt)
	}
}

func TestUnbindDeviceBindingNotFound(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	if _, err := UnbindDeviceBindingWithDB(db, 987654321, time.Now()); err == nil {
		t.Fatalf("不存在的绑定行必须报 ErrDeviceBindingNotFound")
	} else if err != ErrDeviceBindingNotFound {
		t.Fatalf("必须是 ErrDeviceBindingNotFound（映射 404），got %v", err)
	}
	if _, err := UnbindDeviceBindingWithDB(db, 0, time.Now()); err != ErrDeviceBindingNotFound {
		t.Fatalf("id<=0 必须直接报 NotFound，got %v", err)
	}
}

func TestUnbindDeviceBindingWithoutRevocationColumnsIsUnavailable(t *testing.T) {
	// 同 TestBanDeviceWithoutRevocationColumnsStillWritesBlacklist：A1 合入后
	// 实体自带撤销列，必须显式 DROP 才能构造「老库缺列」。
	db := newDeviceAdminTestDB(t, false)
	for _, col := range []string{"revoked_at", "revoked_reason"} {
		if err := db.Migrator().DropColumn(&entity.AccountDeviceBinding{}, col); err != nil {
			t.Fatalf("drop column %s: %v", col, err)
		}
	}
	if db.Migrator().HasColumn(&entity.AccountDeviceBinding{}, "revoked_at") {
		t.Fatalf("前置失效：revoked_at 仍未删除，本用例测不到「缺列」分支")
	}
	// ⚠️ 不能调 seedDeviceBinding：它用 GORM `Create(&entity.AccountDeviceBinding{...})`，
	// 而 GORM 会按**实体结构**拼 INSERT，把已 DROP 的 revoked_at 也写进去 →
	// `table account_device_bindings has no column named revoked_at`。
	// 缺列场景必须用**原生 SQL 只写存在的列**，否则构造出来的是「插入失败」而不是「老库缺列」。
	raw := db.Exec(
		`INSERT INTO account_device_bindings (user_id, device_id, first_seen_at, last_seen_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		9403, "exe:norevoke00001", time.Now().UTC(), time.Now().UTC(), time.Now().UTC(), time.Now().UTC(),
	)
	if raw.Error != nil {
		t.Fatalf("原生插入老库形态的绑定行: %v", raw.Error)
	}
	bindingID := int64(raw.RowsAffected)
	if bindingID == 0 {
		t.Fatalf("原生插入未生效")
	}
	var binding entity.AccountDeviceBinding
	if err := db.Raw(`SELECT id FROM account_device_bindings WHERE user_id = ? AND device_id = ?`, 9403, "exe:norevoke00001").
		Scan(&binding).Error; err != nil {
		t.Fatalf("读回绑定行 id: %v", err)
	}

	_, err := UnbindDeviceBindingWithDB(db, binding.ID, time.Now())
	if err == nil {
		t.Fatalf("缺撤销列时解绑必须如实报错，不能假装成功")
	}
	if err != ErrDeviceRevocationUnavailable {
		t.Fatalf("必须是 ErrDeviceRevocationUnavailable（映射 503），got %v", err)
	}
}

// ---------------------------------------------------------------------------
// A3-2：设备列表
// ---------------------------------------------------------------------------

func seedUserForDeviceList(t *testing.T, db *gorm.DB, id int32, username string) {
	t.Helper()

	if !db.Migrator().HasTable(entity.TableNameUser) {
		if err := db.AutoMigrate(&entity.User{}); err != nil {
			t.Fatalf("migrate users: %v", err)
		}
	}
	user := entity.User{
		ID: id, Username: username, Password: "123456",
		Role: "user", Status: "enabled", RegisterSource: "public",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
}

func TestAdminDeviceListJoinsUsernameAndStatus(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	seedUserForDeviceList(t, db, 9501, "alice")
	seedUserForDeviceList(t, db, 9502, "bob")

	active := seedDeviceBinding(t, db, 9501, "exe:alice0000001", false)
	revoked := seedDeviceBinding(t, db, 9502, "exe:bob000000001", true)
	banned := seedDeviceBinding(t, db, 9501, "exe:banned000001", false)

	if _, err := BanDevice(db, DeviceBanRequest{DeviceID: "exe:banned000001", Reason: "违规"}); err != nil {
		t.Fatalf("ban: %v", err)
	}

	result, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("total 必须是 3（全部绑定行），实际 %d", result.Total)
	}
	if !result.RevocationSupported {
		t.Fatalf("撤销列齐备时 RevocationSupported 必须为 true（前端据此启用解绑按钮）")
	}

	byID := make(map[int64]AdminDeviceItem, len(result.Data))
	for _, item := range result.Data {
		byID[item.ID] = item
	}
	if item, ok := byID[active.ID]; !ok {
		t.Fatalf("列表必须包含在用绑定 id=%d", active.ID)
	} else {
		if item.Username != "alice" {
			t.Fatalf("username 必须补上（137 §8.2 要求含 username），got %q", item.Username)
		}
		if item.Status != AdminDeviceStatusActive {
			t.Fatalf("在用绑定状态必须是 active，got %q", item.Status)
		}
		if item.UserID != 9501 {
			t.Fatalf("userId 必须正确，got %d", item.UserID)
		}
		if item.LastSeenAt == nil || item.FirstSeenAt == nil {
			t.Fatalf("first_seen_at / last_seen_at 必须返回，got %+v", item)
		}
		if item.Banned {
			t.Fatalf("未封禁设备不得标 Banned")
		}
	}
	if item, ok := byID[revoked.ID]; !ok {
		t.Fatalf("列表必须包含已撤销绑定（A3-2 要求「全部设备」）")
	} else if item.Status != AdminDeviceStatusRevoked {
		t.Fatalf("已撤销绑定的状态必须是 revoked，got %q", item.Status)
	}
	if item, ok := byID[banned.ID]; !ok {
		t.Fatalf("列表必须包含被封禁设备的绑定行")
	} else {
		// 优先级：banned > active（否则运营会以为封禁没生效而重复点）。
		if item.Status != AdminDeviceStatusBanned {
			t.Fatalf("被封禁的设备（即使绑定未撤销）状态必须是 banned，got %q", item.Status)
		}
		if !item.Banned || item.BanEntryID <= 0 {
			t.Fatalf("必须回填 banEntryId，got %+v", item)
		}
		if item.BanReason != "违规" {
			t.Fatalf("必须回填 banReason，got %q", item.BanReason)
		}
		if !item.BanPermanent {
			t.Fatalf("未设到期时间时 BanPermanent 必须为 true")
		}
	}
}

func TestAdminDeviceListSearchByUsernameOrDeviceID(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	seedUserForDeviceList(t, db, 9601, "carol")
	seedDeviceBinding(t, db, 9601, "exe:carol0000001", false)
	seedDeviceBinding(t, db, 9601, "exe:zzz000000001", false)

	byName, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{Keyword: "caro"}, 1, 20)
	if err != nil {
		t.Fatalf("search by username: %v", err)
	}
	if byName.Total != 2 {
		t.Fatalf("按用户名子串应命中该账号全部 2 台设备，实际 %d", byName.Total)
	}

	byDevice, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{Keyword: "zzz"}, 1, 20)
	if err != nil {
		t.Fatalf("search by device id: %v", err)
	}
	if byDevice.Total != 1 {
		t.Fatalf("按 device_id 子串应命中 1 行，实际 %d", byDevice.Total)
	}
	if len(byDevice.Data) != 1 || byDevice.Data[0].DeviceID != "exe:zzz000000001" {
		t.Fatalf("命中行必须是那台设备，got %+v", byDevice.Data)
	}

	// 大小写不敏感（INSTR(LOWER(...))）。
	upper, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{Keyword: "ZZZ"}, 1, 20)
	if err != nil {
		t.Fatalf("search uppercase: %v", err)
	}
	if upper.Total != 1 {
		t.Fatalf("关键词必须大小写不敏感，实际 %d", upper.Total)
	}
}

func TestAdminDeviceListStatusFilter(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	seedUserForDeviceList(t, db, 9701, "dave")
	seedDeviceBinding(t, db, 9701, "exe:active000001", false)
	seedDeviceBinding(t, db, 9701, "exe:revoked00001", true)
	seedDeviceBinding(t, db, 9701, "exe:banned000001", false)
	if _, err := BanDevice(db, DeviceBanRequest{DeviceID: "exe:banned000001", Reason: "违规"}); err != nil {
		t.Fatalf("ban: %v", err)
	}

	cases := []struct {
		status string
		want   int64
		device string
	}{
		{AdminDeviceStatusActive, 1, "exe:active000001"},
		{AdminDeviceStatusRevoked, 1, "exe:revoked00001"}, // 拉黑状态优先，不重复归为已移除。
		{AdminDeviceStatusBanned, 1, "exe:banned000001"},
		{"", 3, ""},
	}
	for _, tc := range cases {
		result, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{Status: tc.status}, 1, 20)
		if err != nil {
			t.Fatalf("list status=%q: %v", tc.status, err)
		}
		if result.Total != tc.want {
			t.Fatalf("status=%q 期望 %d 行，实际 %d", tc.status, tc.want, result.Total)
		}
		if tc.device != "" {
			if len(result.Data) != 1 || result.Data[0].DeviceID != tc.device {
				t.Fatalf("status=%q 命中行必须是 %s，got %+v", tc.status, tc.device, result.Data)
			}
		}
	}

	if _, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{Status: "nonsense"}, 1, 20); err == nil {
		t.Fatalf("非法 status 必须报错（不能静默当成「全部」）")
	}
}

func TestAdminDeviceListReportsOrphanBans(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	seedUserForDeviceList(t, db, 9801, "erin")
	// 只封禁、无任何绑定行：137 §9.2 的表格里那一行 `—  已封禁  [解封]`。
	if _, err := BanDevice(db, DeviceBanRequest{DeviceID: "exe:orphan000001", Reason: "无绑定"}); err != nil {
		t.Fatalf("ban orphan: %v", err)
	}
	seedDeviceBinding(t, db, 9801, "exe:bound0000001", false)

	result, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.OrphanBans) != 1 {
		t.Fatalf("必须返回 1 条孤立封禁（否则那台设备永远无法解封），实际 %d", len(result.OrphanBans))
	}
	if result.OrphanBans[0].DeviceID != "exe:orphan000001" {
		t.Fatalf("孤立封禁的 deviceId 不对，got %q", result.OrphanBans[0].DeviceID)
	}
	if !result.OrphanBans[0].Permanent {
		t.Fatalf("未设到期时间时必须标 Permanent=true")
	}
	// 主分页不得把孤立封禁算进 total（否则分页与服务端 total 对不上）。
	if result.Total != 1 {
		t.Fatalf("total 只统计绑定行，期望 1，实际 %d", result.Total)
	}
}

func TestAdminDeviceListWithoutBindingsTableIsEmpty(t *testing.T) {
	// 完全不建 account_device_bindings：迁移没跑的库。
	db := newBlacklistTestDB(t)

	result, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("缺表时必须返回空列表而不是错误（管理页要能渲染）：%v", err)
	}
	if result.Total != 0 || len(result.Data) != 0 {
		t.Fatalf("缺表时必须是空列表，got %+v", result)
	}
	if result.RevocationSupported {
		t.Fatalf("缺表时 RevocationSupported 必须为 false")
	}
	if result.Data == nil {
		t.Fatalf("data 必须是 []，不能是 nil（前端把 null 与 [] 当两种状态）")
	}
}

// ─────────────────── A4-2：按 userId 过滤设备列表 ───────────────────

// UserID 过滤必须只返回该账号的设备。
//
// ## 为什么必须是服务端过滤（这条用例守的就是这个前提）
//
// 设备列表是分页的，前端只拿到当前页。若在客户端筛「这个用户的设备」，
// 结果会随「他的设备恰好落在第几页」而变 —— 用户在第 1 页看到 2 台、
// 翻到第 2 页又看到 1 台，而他实际有 3 台。这类错误极难被用户描述清楚。
//
// 判据用**三个用户各自的设备数**断言，而不是只断言「返回了 2 条」：
// 后者在「过滤失效、恰好总数也是 2」时会假绿。
func TestAdminDeviceListFiltersByUserID(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	seedUserForDeviceList(t, db, 9101, "alpha")
	seedUserForDeviceList(t, db, 9102, "beta")
	// alpha 有 2 台，beta 有 3 台，总数 5 —— 三个数互不相同，
	// 任何「过滤没生效」或「过滤错人」的实现在这些数字上都会暴露。
	seedDeviceBinding(t, db, 9101, "exe:alpha0000001", false)
	seedDeviceBinding(t, db, 9101, "exe:alpha0000002", false)
	seedDeviceBinding(t, db, 9102, "exe:beta00000001", false)
	seedDeviceBinding(t, db, 9102, "exe:beta00000002", false)
	seedDeviceBinding(t, db, 9102, "exe:beta00000003", false)

	alpha, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{UserID: 9101}, 1, 20)
	if err != nil {
		t.Fatalf("list alpha: %v", err)
	}
	if alpha.Total != 2 || len(alpha.Data) != 2 {
		t.Fatalf("用户 9101 应恰好 2 台设备，实际 total=%d len=%d", alpha.Total, len(alpha.Data))
	}
	for _, item := range alpha.Data {
		if item.UserID != 9101 {
			t.Fatalf("过滤后仍混入别的账号：userID=%d deviceID=%s", item.UserID, item.DeviceID)
		}
	}

	beta, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{UserID: 9102}, 1, 20)
	if err != nil {
		t.Fatalf("list beta: %v", err)
	}
	if beta.Total != 3 || len(beta.Data) != 3 {
		t.Fatalf("用户 9102 应恰好 3 台设备，实际 total=%d len=%d", beta.Total, len(beta.Data))
	}

	// 对照组：UserID=0 必须返回全部 5 台 —— 证明「过滤生效」不是靠把结果砍空。
	all, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{}, 1, 50)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if all.Total != 5 {
		t.Fatalf("不指定 userId 时必须返回全部 5 台，实际 %d", all.Total)
	}
}

// 过滤 + 关键词两个条件是 AND，不是 OR。
//
// 取 OR 的实现会让「查某用户的某台设备」变成「该用户全部设备 + 任意用户名含关键词的设备」，
// 在用户管理页上表现为「他的设备列表里冒出别人的设备」。
func TestAdminDeviceListUserIDAndKeywordAreAnded(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	seedUserForDeviceList(t, db, 9201, "gamma")
	seedUserForDeviceList(t, db, 9202, "delta")
	seedDeviceBinding(t, db, 9201, "exe:gamma0000001", false)
	seedDeviceBinding(t, db, 9201, "exe:gamma0000002", false)
	seedDeviceBinding(t, db, 9202, "exe:delta0000001", false)

	// 关键词命中 gamma 的一个 device_id，且 userID 也是 gamma ⇒ 1 条。
	got, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{
		UserID:  9201,
		Keyword: "gamma0000001",
	}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got.Total != 1 {
		t.Fatalf("userId+keyword 必须同时满足（AND），期望 1 条，实际 %d", got.Total)
	}
	if got.Total == 1 && got.Data[0].UserID != 9201 {
		t.Fatalf("命中的行必须属于 9201，实际 %d", got.Data[0].UserID)
	}

	// 关键词只匹配 delta 的设备，但 userID 限定 gamma ⇒ 0 条。
	// 写成 OR 的实现会返回 1 条（delta 那台），必红。
	empty, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{
		UserID:  9201,
		Keyword: "delta0000001",
	}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if empty.Total != 0 {
		t.Fatalf("userId=gamma + keyword=delta 必须返回 0 条（AND 语义），实际 %d 条", empty.Total)
	}
}

// 按 userId 过滤时**不得**返回全局孤儿封禁（A4-2 的界面语义）。
//
// 孤儿封禁 = 「device_id 没有任何绑定行的封禁」，它天然是跨账号的。
// 在用户管理页展示它，等于在「张三的设备」面板里列出一堆别人的封禁记录。
//
// 更糟的是「该用户一台设备都没有」与「他有一台被封禁的孤儿设备」在前端
// 会长得一模一样（都是列表空 + 一堆孤儿行），而两者该做的处置完全相反。
func TestAdminDeviceListSkipsOrphanBansWhenFilteredByUser(t *testing.T) {
	db := newDeviceAdminTestDB(t, true)
	seedUserForDeviceList(t, db, 9301, "zeta")
	// 一条全局孤儿封禁（不属于任何账号的绑定行）。
	if _, err := BanDevice(db, DeviceBanRequest{DeviceID: "exe:orphan9301xx", Reason: "无绑定"}); err != nil {
		t.Fatalf("ban orphan: %v", err)
	}

	// 该用户没有任何设备 ⇒ 列表为空。
	filtered, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{UserID: 9301}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(filtered.Data) != 0 {
		t.Fatalf("该用户没有设备，列表必须为空，实际 %d 条", len(filtered.Data))
	}
	if len(filtered.OrphanBans) != 0 {
		t.Fatalf("按 userId 过滤时不得返回全局孤儿封禁（会把别人的封禁混进这个用户的面板），实际 %d 条",
			len(filtered.OrphanBans))
	}

	// 对照组：不指定 userId 时孤儿封禁**必须**照常返回。
	// 没有这一条，「干脆永远不返回 OrphanBans」的错误实现也能让上面绿。
	all, err := ListAdminDeviceBindingsWithDB(db, AdminDeviceListQuery{}, 1, 20)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all.OrphanBans) != 1 {
		t.Fatalf("不指定 userId 时必须照常返回孤儿封禁（否则那台设备永远无法解封），实际 %d 条",
			len(all.OrphanBans))
	}
}
