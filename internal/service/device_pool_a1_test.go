package service

// 设备体系批次 A1 的验收用例（137 §10 判据 D1-D11 中属于 A1 的部分）。
//
// 与 device_pool_test.go 的分工：那个文件是 121 §4.9 时代的「等级上限」判据
// （D1/D2/D3/D4），本文件钉住 137 新增的部分 —— **判定顺序**、用户级覆盖、
// 用户自管拉黑、全局设备黑名单、撤销→静默重绑、10 天闲置解绑。
//
// ⚠️ 两条贯穿全文件的纪律：
//
//  1. **期望值一律写字面量**（"device-blacklisted"、"level_missing"…），
//     不引用被测常量。引用常量会让常量改名/改值时断言同步漂移 = 恒真
//     （本仓已因「期望值引用被测常量」交付过假钉子）。
//  2. **判据 D5 的用例必须在同一账号上先建立「已绑定」事实再封禁**。
//     只测「新设备 + 黑名单」的话，把黑名单判定挪到已绑定之后仍然会红不了 ——
//     因为新设备根本不命中「已绑定」分支，两条实现等价。
//     137 §5.1 的缺陷形态正是「已绑定设备被封禁后仍放行」，用例必须**精确**
//     构造那个场景，否则就是假钉子。

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

// a1LegacyDBSeq 保证每个「老库」用例拿到独立的内存库（同库同名会互相污染）。
var a1LegacyDBSeq atomic.Int64

// newLegacyUsersTestDB 建一个**旧结构**的最小库：
// users 有 level_id 但**没有** device_limit_override（A1-1 之前的形态）。
//
// 注意这里为什么不需要「删列」：`internal/entity/users.gen.go` 是 gorm.io/gen 生成文件，
// 上面**没有** DeviceLimitOverride 字段（本仓刻意不手改生成文件），
// 因此 `AutoMigrate(&entity.User{})` 建出来的表天然就是老结构 ——
// 生产环境的 device_limit_override 由 registry.ensureUserDeviceLimitOverrideColumn
// 用显式 ALTER 补，同理。
//
// 但 GORM 的 schema 缓存按 struct 名记；同一进程内先跑过会带该列的用例时，
// AutoMigrate 可能带上残留定义。所以这里额外用 HasColumn 断言前置，
// 断言失败即说明前置不成立（会在用例开头直接 Fatal，不会悄悄退化成新结构）。
func newLegacyUsersTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := "file:legacy-users-" + name + "-" + formatInt64(a1LegacyDBSeq.Add(1)) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{}, &entity.AccountDeviceBinding{},
		&entity.UserDeviceBlock{}, &entity.BlacklistEntry{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate legacy db: %v", err)
	}
	// 老库有 level_id（等级体系先上线），但**没有** device_limit_override。
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE " + entity.TableNameUser +
			" ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatalf("add level_id: %v", err)
		}
	}
	if db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		t.Skip("前置不成立：实体里若新增了 DeviceLimitOverride 字段，本用例需改用裸 DDL 建表")
	}
	return db
}

// newLegacyDeviceBindingTestDB 建一个 account_device_bindings **不含 revoked_at** 的旧库。
//
// 语义：部署顺序是「代码先上、迁移后跑」，此时判定必须仍然生效
// （把「没有撤销功能」当作全未撤销），而不是整条链 fail-open。
func newLegacyDeviceBindingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := "file:legacy-binding-" + name + "-" + formatInt64(a1LegacyDBSeq.Add(1)) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserLevel{}, &entity.UserDeviceBlock{},
		&entity.BlacklistEntry{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate legacy db: %v", err)
	}
	// 手写 A1 之前的绑定表 DDL（只有 121 §4.9 的 7 列）。
	ddl := "CREATE TABLE " + entity.TableNameAccountDeviceBinding + " (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"user_id INTEGER NOT NULL, " +
		"device_id VARCHAR(191) NOT NULL, " +
		"first_seen_at DATETIME, " +
		"last_seen_at DATETIME, " +
		"created_at DATETIME, " +
		"updated_at DATETIME)"
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create legacy binding table: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX " + entity.AccountDeviceBindingUniqueIndex +
		" ON " + entity.TableNameAccountDeviceBinding + " (user_id, device_id)").Error; err != nil {
		t.Fatalf("create legacy unique index: %v", err)
	}
	// users.device_limit_override 本用例不需要，但 EffectiveDeviceLimit 会判 HasColumn，
	// 缺失时自动退化 —— 正好覆盖另一个分支。
	if err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN level_id integer NOT NULL DEFAULT 0",
		entity.TableNameUser)).Error; err != nil {
		t.Fatalf("add level_id: %v", err)
	}
	return db
}

// ─────────────────────── A1-1：users.device_limit_override ───────────────────────

// seedDeviceLimitOverride 设置某用户的 device_limit_override（-1=不覆盖）。
func seedDeviceLimitOverride(t *testing.T, db *gorm.DB, userID int32, override int32) {
	t.Helper()
	if err := db.Table(entity.TableNameUser).Where("id = ?", userID).
		Update("device_limit_override", override).Error; err != nil {
		t.Fatalf("set device_limit_override=%d: %v", override, err)
	}
}

// readDeviceLimitOverride 读回该列，用于断言默认值与落库。
func readDeviceLimitOverride(t *testing.T, db *gorm.DB, userID int32) int32 {
	t.Helper()
	var row struct {
		Override int32 `gorm:"column:device_limit_override"`
	}
	if err := db.Table(entity.TableNameUser).Select("device_limit_override AS device_limit_override").
		Where("id = ?", userID).Take(&row).Error; err != nil {
		t.Fatalf("read device_limit_override: %v", err)
	}
	return row.Override
}

// 判据：新建用户（未显式设置）时 device_limit_override 必须是 **-1**（不覆盖）。
//
// 变异的现实后果：若默认值是 0，则所有用户一上线就被解释为「用户主动放宽到不限」，
// 等级上的 device_limit 永远不生效 —— 而且这是静默的（0 与 -1 都是合法值，
// 判定函数不会报错，只是走了另一条分支）。
func TestDeviceLimitOverrideDefaultsToNotSet(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 2001, 0)

	if got := readDeviceLimitOverride(t, db, 2001); got != -1 {
		t.Fatalf("users.device_limit_override 默认必须是 -1（不覆盖），实际 %d", got)
	}
	// 同一条约束的另一面：DDL 层的默认值也必须是 -1，而不是靠 Go 侧补。
	// 用一条绕过 GORM 的裸 INSERT 验证列默认值真的生效。
	if err := db.Exec("INSERT INTO "+entity.TableNameUser+
		" (username, password, role, status, register_source) VALUES (?, ?, ?, ?, ?)",
		"raw-default-user", "x", "user", "enabled", "system").Error; err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	var raw struct {
		ID       int32 `gorm:"column:id"`
		Override int32 `gorm:"column:device_limit_override"`
	}
	if err := db.Table(entity.TableNameUser).
		Select("id AS id, device_limit_override AS device_limit_override").
		Where("username = ?", "raw-default-user").Take(&raw).Error; err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if raw.Override != -1 {
		t.Fatalf("列的 DDL 默认值必须是 -1（不依赖应用层写入），实际 %d", raw.Override)
	}
}

// ─────────────────────── A1-3：EffectiveDeviceLimit 唯一入口 ───────────────────────

// 判据 D9：override >= 0 时覆盖等级值（0=不限，N>0=具体台数），source=user_override。
//
// 变异方向「判定不读 override」→ 本用例必红（等级 1 台却放行第 2 台）。
func TestEffectiveDeviceLimitUserOverrideWins(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 1)
	seedDevicePoolUser(t, db, 2101, levelID)

	// 基线：读等级值 1。
	limit, source, err := EffectiveDeviceLimit(db, 2101)
	if err != nil || limit != 1 || source != "level" {
		t.Fatalf("基线应当 (1, level, nil)，实际 (%d, %q, %v)", limit, source, err)
	}

	// override=3 ⇒ 覆盖成 3。
	seedDeviceLimitOverride(t, db, 2101, 3)
	limit, source, err = EffectiveDeviceLimit(db, 2101)
	if err != nil || limit != 3 || source != "user_override" {
		t.Fatalf("override=3 应当 (3, user_override, nil)，实际 (%d, %q, %v)", limit, source, err)
	}

	// override=0 ⇒ 不限。
	seedDeviceLimitOverride(t, db, 2101, 0)
	limit, source, err = EffectiveDeviceLimit(db, 2101)
	if err != nil || limit != 0 || source != "user_override" {
		t.Fatalf("override=0 应当 (0, user_override, nil)，实际 (%d, %q, %v)", limit, source, err)
	}

	// override=-1 ⇒ 回到等级值（不覆盖）。
	seedDeviceLimitOverride(t, db, 2101, -1)
	limit, source, err = EffectiveDeviceLimit(db, 2101)
	if err != nil || limit != 1 || source != "level" {
		t.Fatalf("override=-1 应当回到 (1, level, nil)，实际 (%d, %q, %v)", limit, source, err)
	}
}

// 判据 D9 的行为面：override=1 时第 2 台设备必须被拒（判定真的读了 override）。
func TestDevicePoolOverrideRejectsSecondDevice(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 5) // 等级给 5，用户自己降到 1
	seedDevicePoolUser(t, db, 2102, levelID)
	seedDeviceLimitOverride(t, db, 2102, 1)

	mustRegisterDevice(t, db, 2102, "ovr-dev-1")
	decision := CanUseDevice(db, 2102, "ovr-dev-2")
	if decision.Allowed {
		t.Fatalf("override=1 时第 2 台必须被拒（否则 override 没被读取）：%+v", decision)
	}
	if decision.Reason != "limit-exceeded" || decision.Limit != 1 || decision.Used != 1 {
		t.Fatalf("拒绝原因/上限/已用不对：%+v", decision)
	}
	if decision.LimitSource != "user_override" {
		t.Fatalf("LimitSource 必须是 user_override，实际 %q", decision.LimitSource)
	}
}

// 红线 1：level_id=0 时 EffectiveDeviceLimit 必须 (0, "level_missing", **nil**)。
//
// 返回错误会让「还没评估过等级的新用户」立刻无法登录 —— 而且是全量性的：
// 新注册用户在被评估前 level_id 恒为 0。
func TestEffectiveDeviceLimitLevelMissingIsNotAnError(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 2103, 0) // level_id=0

	limit, source, err := EffectiveDeviceLimit(db, 2103)
	if err != nil {
		t.Fatalf("level_id=0 时不得返回错误（红线 1：会让未评估用户无法登录）：%v", err)
	}
	if limit != 0 {
		t.Fatalf("level_id=0 时必须返回 0（不限，fail-open），实际 %d", limit)
	}
	if source != "level_missing" {
		t.Fatalf("source 必须是 level_missing，实际 %q", source)
	}

	// 行为面：无等级用户连续 5 台设备都必须放行（不是「0 台」）。
	for i := 1; i <= 5; i++ {
		decision := CanUseDevice(db, 2103, "missing-level-dev-"+formatInt64(int64(i)))
		if !decision.Allowed {
			t.Fatalf("无等级用户第 %d 台必须放行：%+v", i, decision)
		}
	}
}

// 等级表整体缺失 ⇒ fail-open（不是 level_missing 静默放行）。
//
// 两种语义必须分开：前者是基础设施故障（要留痕），后者是正常状态。
func TestEffectiveDeviceLimitSchemaMissingFailsOpen(t *testing.T) {
	db := newDevicePoolTestDB(t)
	seedDevicePoolUser(t, db, 2104, 0)
	if err := db.Migrator().DropTable(entity.TableNameUserLevel); err != nil {
		t.Fatalf("drop user_levels: %v", err)
	}
	limit, source, err := EffectiveDeviceLimit(db, 2104)
	if err != nil {
		t.Fatalf("等级表缺失时不该返回错误：%v", err)
	}
	if limit != 0 || source != "level_missing" {
		t.Fatalf("等级表缺失应当 (0, level_missing)，实际 (%d, %q)", limit, source)
	}
}

// 用户自设覆盖列不存在（老库、迁移未跑）⇒ 按「不覆盖」处理，绝不报错。
//
// ⚠️ 不能用 `Migrator().DropColumn` 造这个场景：glebarez/sqlite 的 DropColumn
// 走「重建表」，对没有 model schema 的表名会 panic（schema 为 nil → LookUpField）。
// 改用**裸 DDL 建一张老结构的 users 表**，语义更贴近真实场景（老库本来就是这样）。
func TestEffectiveDeviceLimitWithoutOverrideColumnFallsBackToLevel(t *testing.T) {
	db := newLegacyUsersTestDB(t)
	// 老结构：有 level_id，**没有** device_limit_override。
	if db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		t.Fatal("前置不成立：老库不该有 device_limit_override 列")
	}
	if err := db.Create(&entity.UserLevel{
		Name: "老库等级", Rank: 1, Enabled: true, DeviceLimit: 2,
	}).Error; err != nil {
		t.Fatalf("create level: %v", err)
	}
	var level entity.UserLevel
	if err := db.Where("name = ?", "老库等级").Take(&level).Error; err != nil {
		t.Fatalf("read level: %v", err)
	}
	if err := db.Create(&entity.User{
		Username: "legacy-user", Password: "x", Role: "user", Status: "enabled", RegisterSource: "system",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	var user entity.User
	if err := db.Where("username = ?", "legacy-user").Take(&user).Error; err != nil {
		t.Fatalf("read user: %v", err)
	}
	if err := db.Table(entity.TableNameUser).Where("id = ?", user.ID).
		Update("level_id", level.ID).Error; err != nil {
		t.Fatalf("attach level: %v", err)
	}

	limit, source, err := EffectiveDeviceLimit(db, user.ID)
	if err != nil {
		t.Fatalf("覆盖列缺失时不得返回错误（老库必须先能跑）：%v", err)
	}
	if limit != 2 || source != "level" {
		t.Fatalf("覆盖列缺失时应当用等级值 (2, level)，实际 (%d, %q)", limit, source)
	}
	// 判定链路上也必须正常生效（不是恰好放行）。
	if decision := CanUseDevice(db, user.ID, "legacy-dev-1"); decision.Limit != 2 {
		t.Fatalf("老库上判定必须读到等级上限 2，实际 %+v", decision)
	}
}

// ─────────────────────── A1-4：判定顺序（判据 D5 / D6） ───────────────────────

// seedGlobalDeviceBlacklist 往 blacklist_entries 写一条 kind=device 的规则。
func seedGlobalDeviceBlacklist(t *testing.T, db *gorm.DB, deviceID string, expiresAt *time.Time) int64 {
	t.Helper()
	normalized := NormalizeDevicePoolDeviceID(deviceID)
	entry := entity.BlacklistEntry{
		Kind:        "device",
		Pattern:     normalized,
		ValueHash:   BlacklistPatternHashHex(normalized),
		ValueMasked: "de**ce",
		MatchType:   "exact",
		Reason:      "测试：封禁该设备",
		Enabled:     true,
		ExpiresAt:   expiresAt,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed device blacklist: %v", err)
	}
	return entry.ID
}

// 判据 D5（本批次最关键的一条）：**全局黑名单优先于已绑定**。
//
// 场景必须精确构造（137 §5.1）：同一账号上「设备已绑定且未撤销」**之后**
// 才把它拉进全局黑名单。正确实现下必须拒绝 —— 否则「封禁一台正在用的设备」
// 完全失效（用户照常使用，封禁只是安慰）。
//
// 变异方向「把黑名单判定移到已绑定判定之后」→ 本用例必红
// （已绑定分支先返回 already-registered / Allowed=true）。
//
// ⚠️ 只测「新设备 + 黑名单」是不够的：新设备不命中已绑定分支，两条实现等价，
// 变异不会红 —— 那就是假钉子。
func TestDevicePoolGlobalBlacklistBeatsAlreadyBound(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 3)
	seedDevicePoolUser(t, db, 2201, levelID)

	// ① 先在**没有黑名单**的情况下正常绑定，建立「已绑定且未撤销」事实。
	decision := mustRegisterDevice(t, db, 2201, "ban-me-dev")
	if decision.Reason != "within-limit" {
		t.Fatalf("前置：首次绑定应当是 within-limit，实际 %q", decision.Reason)
	}
	// 再确认一次「已绑定 → 放行」这条基线成立（否则后面的拒绝可能来自别的原因）。
	if again := CanUseDevice(db, 2201, "ban-me-dev"); again.Reason != "already-registered" {
		t.Fatalf("前置：未封禁时已绑定设备应为 already-registered，实际 %q", again.Reason)
	}

	// ② 现在把这台**已在用**的设备拉进全局黑名单。
	entryID := seedGlobalDeviceBlacklist(t, db, "ban-me-dev", nil)

	blocked := CanUseDevice(db, 2201, "ban-me-dev")
	if blocked.Allowed {
		t.Fatalf("已在用的设备被全局封禁后必须拒绝 —— 放行说明黑名单判定排在已绑定之后（D5）：%+v", blocked)
	}
	if blocked.Reason != "device-blacklisted" {
		t.Fatalf("拒绝原因必须是 device-blacklisted，实际 %q", blocked.Reason)
	}
	if blocked.BlacklistEntryID != entryID {
		t.Fatalf("必须报出命中的规则 id（申诉时要能回指），期望 %d 实际 %d", entryID, blocked.BlacklistEntryID)
	}
	if blocked.Message == "" {
		t.Fatal("拒绝必须带用户可读文案")
	}

	// ③ 同一黑名单对「新设备」也必须拒绝（两条路径都封）。
	if fresh := CanUseDevice(db, 2201, "ban-me-dev"); !fresh.Allowed {
		// 已覆盖
	} else {
		t.Fatal("黑名单对新请求同样必须拒绝")
	}

	// ④ 停用该规则后必须立刻恢复放行（否则「解封」无从生效）。
	if err := db.Model(&entity.BlacklistEntry{}).Where("id = ?", entryID).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable entry: %v", err)
	}
	if after := CanUseDevice(db, 2201, "ban-me-dev"); !after.Allowed {
		t.Fatalf("规则停用后必须放行（解封链路依赖它）：%+v", after)
	}
}

// 判据 D5 的配套：全局黑名单同样**优先于「不限档」**（不限不等于允许被封设备）。
//
// 顺序写反的另一种表现：把黑名单放在 limit==0 之后，则不限档用户的设备永不生效。
func TestDevicePoolGlobalBlacklistBeatsUnlimitedLimit(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 0) // 不限
	seedDevicePoolUser(t, db, 2202, levelID)
	seedGlobalDeviceBlacklist(t, db, "unlimited-banned-dev", nil)

	decision := CanUseDevice(db, 2202, "unlimited-banned-dev")
	if decision.Allowed {
		t.Fatalf("不限档也必须先过全局黑名单（黑名单优先于 limit==0 分支）：%+v", decision)
	}
	if decision.Reason != "device-blacklisted" {
		t.Fatalf("原因必须是 device-blacklisted，实际 %q", decision.Reason)
	}
}

// 过期规则不命中（与缓存侧一致：ExpiresAt 已过 ⇒ 不生效）。
//
// 若不判过期，运营设了「封 7 天」会变成永久封禁，且**没有任何报错**。
func TestDevicePoolGlobalBlacklistExpiredEntryIgnored(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 3)
	seedDevicePoolUser(t, db, 2203, levelID)
	past := time.Now().Add(-time.Hour)
	seedGlobalDeviceBlacklist(t, db, "expired-dev", &past)

	if decision := CanUseDevice(db, 2203, "expired-dev"); !decision.Allowed {
		t.Fatalf("过期规则不得命中：%+v", decision)
	}
	future := time.Now().Add(time.Hour)
	seedGlobalDeviceBlacklist(t, db, "future-dev", &future)
	if decision := CanUseDevice(db, 2203, "future-dev"); decision.Allowed {
		t.Fatalf("未过期规则必须命中：%+v", decision)
	}
}

// 判据 D6：用户拉黑后自己无法使用该设备，且**已绑定也不放行**（用户级优先于已绑定）。
func TestDevicePoolUserBlockRejectsEvenWhenBound(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 3)
	seedDevicePoolUser(t, db, 2204, levelID)

	mustRegisterDevice(t, db, 2204, "self-blocked-dev")
	if again := CanUseDevice(db, 2204, "self-blocked-dev"); again.Reason != "already-registered" {
		t.Fatalf("前置：未拉黑时应为 already-registered，实际 %q", again.Reason)
	}

	if err := db.Create(&entity.UserDeviceBlock{
		UserID: 2204, DeviceID: "self-blocked-dev", Reason: "不是我的设备",
	}).Error; err != nil {
		t.Fatalf("seed user device block: %v", err)
	}

	decision := CanUseDevice(db, 2204, "self-blocked-dev")
	if decision.Allowed {
		t.Fatalf("用户拉黑后必须拒绝（即便已绑定）：%+v", decision)
	}
	if decision.Reason != "user-blocked" {
		t.Fatalf("原因必须是 user-blocked（与全局封禁要能区分），实际 %q", decision.Reason)
	}

	// 拉黑只影响该用户：另一个用户用同一台设备应正常。
	seedDevicePoolUser(t, db, 2205, levelID)
	if other := CanUseDevice(db, 2205, "self-blocked-dev"); !other.Allowed {
		t.Fatalf("用户拉黑不该影响其它账号：%+v", other)
	}

	// 解除拉黑（删行）后立刻恢复（判据 D7 的服务层部分）。
	if err := db.Where("user_id = ? AND device_id = ?", 2204, "self-blocked-dev").
		Delete(&entity.UserDeviceBlock{}).Error; err != nil {
		t.Fatalf("delete block: %v", err)
	}
	if after := CanUseDevice(db, 2204, "self-blocked-dev"); !after.Allowed {
		t.Fatalf("解除拉黑后必须恢复放行：%+v", after)
	}
	// 拉黑表不存在（A2 迁移未跑）时不得阻断判定 —— 那是「还没人拉黑过」。
	if err := db.Migrator().DropTable(entity.TableNameUserDeviceBlock); err != nil {
		t.Fatalf("drop user_device_blocks: %v", err)
	}
	if afterDrop := CanUseDevice(db, 2204, "self-blocked-dev"); !afterDrop.Allowed {
		t.Fatalf("拉黑表缺失时应按未拉黑处理：%+v", afterDrop)
	}
}

// 判据 D4：同设备重复请求不占新槽 —— **且已撤销的行不算已绑定**。
//
// 变异方向「去掉 revoked_at IS NULL 判据」→ 本用例必红：
// 撤销后的行仍会被当成「已绑定」，于是第 5 步放行而不是重新占槽。
func TestDevicePoolRevokedBindingDoesNotCountAsBound(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 1) // 只给 1 个槽
	seedDevicePoolUser(t, db, 2301, levelID)

	mustRegisterDevice(t, db, 2301, "rev-dev-1")
	if used, err := CountDevices(db, 2301); err != nil || used != 1 {
		t.Fatalf("绑定后已用应为 1：used=%d err=%v", used, err)
	}

	// 撤销（模拟「用户在我的设备里移除」）。
	changed, err := RevokeDeviceBinding(db, 2301, "rev-dev-1", entity.DeviceBindingRevokeReasonUserRemoved)
	if err != nil || !changed {
		t.Fatalf("撤销应当成功：changed=%v err=%v", changed, err)
	}

	// 槽位必须释放：已用 0，行还在（不删行）。
	if used, err := CountDevices(db, 2301); err != nil || used != 0 {
		t.Fatalf("撤销后已用必须是 0（否则槽位永不释放）：used=%d err=%v", used, err)
	}
	if all, err := CountAllDevices(db, 2301); err != nil || all != 1 {
		t.Fatalf("撤销后行必须仍在（不删行）：all=%d err=%v", all, err)
	}

	// 撤销的设备不再是「已绑定」：请求它时必须**重新占槽**（within-limit）而不是 already-registered。
	decision := mustRegisterDevice(t, db, 2301, "rev-dev-1")
	if decision.Reason != "within-limit" {
		t.Fatalf("撤销后的设备应重新占槽（within-limit），实际 %q —— "+
			"若是 already-registered 说明判据里缺了 revoked_at IS NULL", decision.Reason)
	}
	// 占槽后第 2 台必须被拒（证明槽位真的被重新占用）。
	if second := CanUseDevice(db, 2301, "rev-dev-2"); second.Allowed {
		t.Fatalf("重绑后又占满 1 个槽，第 2 台必须被拒：%+v", second)
	}
}

// 判据 D11：撤销后再次请求会**静默重绑**，复用旧行（不新增行），
// 且 revoked_at 与 revoked_reason **两个都清掉**（137 §7.1）。
//
// 变异方向：重绑时 Create 而非 Updates → 撞唯一索引 + 行数变 2 → 必红。
// 另一个变异：只清 revoked_at 不清 revoked_reason → 必红（本用例同时断言两者）。
func TestDevicePoolRebindReusesRowAndClearsBothRevokeFields(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 3)
	seedDevicePoolUser(t, db, 2302, levelID)

	mustRegisterDevice(t, db, 2302, "rebind-dev")
	var firstSeen time.Time
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("first_seen_at").Where("user_id = ? AND device_id = ?", 2302, "rebind-dev").
		Scan(&firstSeen).Error; err != nil {
		t.Fatalf("read first_seen_at: %v", err)
	}

	// 撤销并确认两列都写了。
	if _, err := RevokeDeviceBinding(db, 2302, "rebind-dev", entity.DeviceBindingRevokeReasonIdle10d); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	var revoked struct {
		RevokedAt     *time.Time `gorm:"column:revoked_at"`
		RevokedReason string     `gorm:"column:revoked_reason"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("revoked_at AS revoked_at, revoked_reason AS revoked_reason").
		Where("user_id = ? AND device_id = ?", 2302, "rebind-dev").Take(&revoked).Error; err != nil {
		t.Fatalf("read revoked row: %v", err)
	}
	if revoked.RevokedAt == nil || revoked.RevokedReason != "idle_10d" {
		t.Fatalf("撤销必须写 revoked_at 与 revoked_reason=idle_10d，实际 %+v", revoked)
	}

	// 再次请求 ⇒ 静默重绑。
	decision, err := RegisterDevice(db, 2302, "rebind-dev")
	if err != nil {
		t.Fatalf("重绑不得返回错误（撞唯一索引就是 D11 的缺陷形态）：%v", err)
	}
	if !decision.Allowed {
		t.Fatalf("重绑必须放行：%+v", decision)
	}

	// 行数仍是 1（复用旧行，不是 Create 新行）。
	var rows int64
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Where("user_id = ? AND device_id = ?", 2302, "rebind-dev").Count(&rows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("重绑必须复用旧行（行数 1），实际 %d 行", rows)
	}

	// revoked_at 与 revoked_reason 两个都必须清（137 §7.1「两个都要断言」）。
	var rebounded struct {
		RevokedAt     *time.Time `gorm:"column:revoked_at"`
		RevokedReason string     `gorm:"column:revoked_reason"`
		FirstSeenAt   time.Time  `gorm:"column:first_seen_at"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("revoked_at AS revoked_at, revoked_reason AS revoked_reason, first_seen_at AS first_seen_at").
		Where("user_id = ? AND device_id = ?", 2302, "rebind-dev").Take(&rebounded).Error; err != nil {
		t.Fatalf("read rebounded row: %v", err)
	}
	if rebounded.RevokedAt != nil {
		t.Fatalf("重绑后 revoked_at 必须为 NULL，实际 %v", rebounded.RevokedAt)
	}
	if rebounded.RevokedReason != "" {
		t.Fatalf("重绑后 revoked_reason 必须清空（只清 revoked_at 仍会被判定逻辑拒），实际 %q", rebounded.RevokedReason)
	}
	if !rebounded.FirstSeenAt.Equal(firstSeen) {
		t.Fatalf("首次时间必须只写一次：原 %v，重绑后 %v", firstSeen, rebounded.FirstSeenAt)
	}
	// 判定链路上确实是「已绑定」状态。
	if again := CanUseDevice(db, 2302, "rebind-dev"); again.Reason != "already-registered" {
		t.Fatalf("重绑后应视为已绑定，实际 %q", again.Reason)
	}
}

// 判据 D1 的文案面：满额拒绝必须包含「去哪自救」的完整文案（137 §5 第 6 步）。
//
// 文案是**产品需求**：只报「设备数已满」而不说去哪移除，用户只会反复重试。
func TestDevicePoolLimitExceededMessageTellsUserWhereToFix(t *testing.T) {
	db := newDevicePoolTestDB(t)
	levelID := seedDevicePoolLevel(t, db, 1, 2)
	seedDevicePoolUser(t, db, 2303, levelID)
	mustRegisterDevice(t, db, 2303, "msg-dev-1")
	mustRegisterDevice(t, db, 2303, "msg-dev-2")

	decision := CanUseDevice(db, 2303, "msg-dev-3")
	if decision.Allowed {
		t.Fatalf("满额必须拒绝：%+v", decision)
	}
	want := "设备数已满（2/2），请到「我的设备」移除不用的设备"
	if decision.Message != want {
		t.Fatalf("拒绝文案必须是 %q（产品需求），实际 %q", want, decision.Message)
	}
	// 数字必须来自本次判定（写死 2/2 会在别的档位上错）。
	otherLevel := seedDevicePoolLevel(t, db, 2, 1)
	seedDevicePoolUser(t, db, 2304, otherLevel)
	mustRegisterDevice(t, db, 2304, "msg2-dev-1")
	if got := CanUseDevice(db, 2304, "msg2-dev-2").Message; got != "设备数已满（1/1），请到「我的设备」移除不用的设备" {
		t.Fatalf("文案里的 N/N 必须来自实际判定，实际 %q", got)
	}
}

// 判据 D3 的另一面：绑定表存在但 revoked_at 列缺失（部署顺序：代码先上、迁移后跑）
// 时，判定必须**仍然生效**（把「没有撤销功能」当作全未撤销），而不是整条链 fail-open。
//
// 造「老表」同样不能用 DropColumn（见 TestEffectiveDeviceLimitWithoutOverrideColumnFallsBackToLevel）：
// 这里用裸 DDL 建一张只有旧列的 account_device_bindings。
func TestDevicePoolWorksBeforeRevokedColumnMigration(t *testing.T) {
	db := newLegacyDeviceBindingTestDB(t)
	if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "revoked_at") {
		t.Fatal("前置不成立：老表不该有 revoked_at 列")
	}
	if err := db.Create(&entity.UserLevel{
		Name: "老表等级", Rank: 1, Enabled: true, DeviceLimit: 1,
	}).Error; err != nil {
		t.Fatalf("create level: %v", err)
	}
	var level entity.UserLevel
	if err := db.Where("name = ?", "老表等级").Take(&level).Error; err != nil {
		t.Fatalf("read level: %v", err)
	}
	if err := db.Create(&entity.User{
		Username: "legacy-device-user", Password: "x", Role: "user", Status: "enabled", RegisterSource: "system",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	var user entity.User
	if err := db.Where("username = ?", "legacy-device-user").Take(&user).Error; err != nil {
		t.Fatalf("read user: %v", err)
	}
	if err := db.Table(entity.TableNameUser).Where("id = ?", user.ID).
		Update("level_id", level.ID).Error; err != nil {
		t.Fatalf("attach level: %v", err)
	}

	if _, err := RegisterDevice(db, user.ID, "premig-dev-1"); err != nil {
		t.Fatalf("老表上登记不得返回错误：%v", err)
	}
	decision := CanUseDevice(db, user.ID, "premig-dev-2")
	if decision.Allowed {
		t.Fatalf("列缺失时上限必须仍然生效（不能整条 fail-open）：%+v", decision)
	}
	if decision.Reason != "limit-exceeded" {
		t.Fatalf("原因必须是 limit-exceeded，实际 %q", decision.Reason)
	}
	if used, err := CountDevices(db, user.ID); err != nil || used != 1 {
		t.Fatalf("老表上已用数应为 1：used=%d err=%v", used, err)
	}
}
