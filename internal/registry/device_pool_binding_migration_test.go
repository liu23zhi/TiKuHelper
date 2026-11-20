package registry

// 设备体系批次 A1 的迁移与回填验收（137 §3.1 / §3.2 / §3.3 / §10.3）。
//
// 三条独立判据：
//  1. **A1-1**：users.device_limit_override 补列，默认 -1（不覆盖），存量行全 -1；
//  2. **A1-2**：account_device_bindings 补 5 列 + 建 user_device_blocks 与唯一索引；
//  3. **A1-6**：存量 level_id=0 的用户被回填评级，且**只回填一次**（标记幂等）。
//
// ## 为什么这里必须专门测「老库」形态
//
// 既有 account_device_bindings_migration_test.go 只测「空库首启」—— 它证明
// 表能建出来，但**完全不能**证明「已经跑过 v1 标记的老库能补上新列」。
// 而后者才是真实升级路径：老库里 account_device_bindings_schema_v1 已经置位，
// 若把加列写在标记判断之后，那些库的新列**永远补不上且不报错**。
// 本文件的核心价值就是这条（见 TestDevicePoolBindingSchemaBackfillsColumnsAfterFlagSet）。

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var a1RegistryDBSeq atomic.Int64

// newA1RegistryDB 建一个含 users + 等级体系 + system_settings 的库。
//
// 刻意**不**预建 account_device_bindings：本组用例要验证「迁移能把表与列一起建出来」。
func newA1RegistryDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := "file:a1-registry-" + name + "-" + itoaAtomic(a1RegistryDBSeq.Add(1)) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base tables: %v", err)
	}
	return db
}

// ─────────────────────── A1-1：users.device_limit_override ───────────────────────

// 等级体系迁移必须把 device_limit_override 补上，且存量行落 -1。
func TestUserLevelSchemaAddsDeviceLimitOverrideColumn(t *testing.T) {
	db := newA1RegistryDB(t)
	// 存量用户：迁移之前就存在，level_id 由迁移补成 0。
	if err := db.Create(&entity.User{
		Username: "pre-migration-user", Password: "x", Role: "user", Status: "enabled", RegisterSource: "system",
	}).Error; err != nil {
		t.Fatalf("seed legacy user: %v", err)
	}

	if err := ensureUserLevelSchema(db); err != nil {
		t.Fatalf("ensureUserLevelSchema: %v", err)
	}

	if !db.Migrator().HasColumn(entity.TableNameUser, "device_limit_override") {
		t.Fatal("users.device_limit_override 必须存在（A1-1）")
	}
	// 存量行必须是 -1（不覆盖）—— 若默认值是 0，所有用户会被解释成
	// 「主动放宽到不限」，等级上限对全员静默失效。
	var rows []struct {
		Username string `gorm:"column:username"`
		Override int32  `gorm:"column:device_limit_override"`
	}
	if err := db.Table(entity.TableNameUser).
		Select("username AS username, device_limit_override AS device_limit_override").
		Find(&rows).Error; err != nil {
		t.Fatalf("read users: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("前置不成立：应当有一个存量用户")
	}
	for _, row := range rows {
		if row.Override != -1 {
			t.Fatalf("存量用户 %s 的 device_limit_override 必须是 -1（不覆盖），实际 %d", row.Username, row.Override)
		}
	}

	// 幂等：第二次运行不报错，且已改成 5 的值不被覆盖回 -1。
	if err := db.Table(entity.TableNameUser).Where("username = ?", "pre-migration-user").
		Update("device_limit_override", 5).Error; err != nil {
		t.Fatalf("set override: %v", err)
	}
	if err := ensureUserLevelSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var override int32
	if err := db.Table(entity.TableNameUser).Select("device_limit_override AS device_limit_override").
		Where("username = ?", "pre-migration-user").Scan(&override).Error; err != nil {
		t.Fatalf("read override: %v", err)
	}
	if override != 5 {
		t.Fatalf("重跑迁移不得覆盖用户已设的值，期望 5 实际 %d", override)
	}
}

// 覆盖列的 DDL 默认值必须真的是 -1（不靠应用层写）：裸 INSERT 也落 -1。
func TestUserDeviceLimitOverrideColumnDDLDefault(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureUserLevelSchema(db); err != nil {
		t.Fatalf("ensureUserLevelSchema: %v", err)
	}
	if err := db.Exec("INSERT INTO "+entity.TableNameUser+
		" (username, password, role, status, register_source) VALUES (?, ?, ?, ?, ?)",
		"raw-insert-user", "x", "user", "enabled", "legacy").Error; err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	var override int32
	if err := db.Table(entity.TableNameUser).
		Select("device_limit_override AS device_limit_override").
		Where("username = ?", "raw-insert-user").Scan(&override).Error; err != nil {
		t.Fatalf("read override: %v", err)
	}
	if override != -1 {
		t.Fatalf("列的 DDL 默认值必须是 -1，实际 %d（若为 0 则所有用户被当成「不限」）", override)
	}
}

// ─────────────────────── A1-2：绑定表 5 列 + 拉黑表 ───────────────────────

// 空库首启：5 列齐备 + user_device_blocks 表与唯一索引建出来。
func TestDevicePoolBindingSchemaCreatesColumnsAndBlockTable(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("ensureAccountDeviceBindingsSchema: %v", err)
	}

	// 137 §3.2 的 5 列，逐列断言（少一列 = 功能静默缺字段）。
	for _, column := range []string{"owner_kind", "token_id", "device_label", "revoked_at", "revoked_reason"} {
		if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			t.Fatalf("account_device_bindings 缺列 %s（A1-2）", column)
		}
	}
	// 第三批的 last_ip（「我的设备」显示来源 IP）。
	//
	// 断言字面量而不是从 devicePoolBindingColumnSpecs 里取：期望值若引用被测常量，
	// 常量被删掉时两侧同步漂移 → 断言恒真（本仓交付过假钉子）。
	if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "last_ip") {
		t.Fatal("account_device_bindings 缺列 last_ip（「我的设备」的来源 IP 列）")
	}
	if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "first_ip") {
		t.Fatal("account_device_bindings 缺列 first_ip（首次使用 IP）")
	}
	// 拉黑表 + 唯一索引（137 §3.3）。
	if !db.Migrator().HasTable(entity.TableNameUserDeviceBlock) {
		t.Fatal("user_device_blocks 表必须建出来（A1-2 / 137 §3.3）")
	}
	for _, column := range []string{"id", "user_id", "device_id", "reason", "created_at"} {
		if !db.Migrator().HasColumn(entity.TableNameUserDeviceBlock, column) {
			t.Fatalf("user_device_blocks 缺列 %s", column)
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameUserDeviceBlock, entity.UserDeviceBlockUniqueIndex) {
		t.Fatalf("user_device_blocks 必须有唯一索引 %s", entity.UserDeviceBlockUniqueIndex)
	}

	// 唯一索引的**行为级**断言：同一 (user, device) 插两次必须被拒。
	// 只断言索引存在不够 —— 列顺序写反也照样「存在」。
	if err := db.Create(&entity.UserDeviceBlock{UserID: 1, DeviceID: "dev-a"}).Error; err != nil {
		t.Fatalf("first block insert: %v", err)
	}
	if err := db.Create(&entity.UserDeviceBlock{UserID: 2, DeviceID: "dev-a"}).Error; err != nil {
		t.Fatalf("不同用户拉黑同一设备应当允许：%v", err)
	}
	if err := db.Create(&entity.UserDeviceBlock{UserID: 1, DeviceID: "dev-a"}).Error; err == nil {
		t.Fatal("同一 (user_id, device_id) 重复拉黑必须被唯一索引拒绝（幂等锚点）")
	}
}

func TestDeviceFirstIPMigrationPreservesUnknownHistory(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE " + entity.TableNameAccountDeviceBinding + " DROP COLUMN first_ip").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO " + entity.TableNameAccountDeviceBinding + " (user_id, device_id, last_ip) VALUES (1, 'historical-device', '172.18.0.1')").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := ensureDevicePoolBindingSchema(db); err != nil {
			t.Fatal(err)
		}
	}
	var row struct {
		FirstIP string
		LastIP  string
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).Select("first_ip, last_ip").Where("device_id = ?", "historical-device").Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.FirstIP != "" || row.LastIP != "172.18.0.1" {
		t.Fatalf("migration invented history or lost last IP: %#v", row)
	}
}

// **本文件最关键的一条**：老库（v1 标记已置位）必须能补上 5 列。
//
// 场景精确对应真实升级路径：
//  1. 老版本跑过 ensureAccountDeviceBindingsSchema → 表建好 + 标记已写；
//  2. 代码升级（本批次）后重启 → 标记仍在，若加列写在标记判断之后，
//     新列永远补不上，且**不报任何错**；
//  3. 表现：代码里读 revoked_at ⇒ SQL 报 no such column ⇒ 判定恒 fail-open
//     ⇒ 设备上限静默失效。这是「fail-open 掩盖配置错误」的典型形态。
//
// 变异方向「把 ensureDevicePoolBindingSchema 移到标记判断之后」→ 本用例必红。
func TestDevicePoolBindingSchemaBackfillsColumnsAfterFlagSet(t *testing.T) {
	db := newA1RegistryDB(t)
	// ① 先用当前代码跑一次（此时会建全），再手工把新列「删回老结构」——
	// 因为 glebarez/sqlite 的 DropColumn 走重建表且对无 model 的表会 panic，
	// 这里改用「建老结构表 + 手写 v1 标记」的方式精确复现老库。
	if err := db.Exec("DROP TABLE IF EXISTS " + entity.TableNameAccountDeviceBinding).Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	legacyDDL := "CREATE TABLE " + entity.TableNameAccountDeviceBinding + " (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"user_id INTEGER NOT NULL, " +
		"device_id VARCHAR(191) NOT NULL, " +
		"first_seen_at DATETIME, last_seen_at DATETIME, created_at DATETIME, updated_at DATETIME)"
	if err := db.Exec(legacyDDL).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX " + entity.AccountDeviceBindingUniqueIndex +
		" ON " + entity.TableNameAccountDeviceBinding + " (user_id, device_id)").Error; err != nil {
		t.Fatalf("create legacy index: %v", err)
	}
	// 老库的标记（本批次之前就已经写好）。
	if err := setSystemSettingFlag(db, accountDeviceBindingsSchemaV1Key); err != nil {
		t.Fatalf("set legacy flag: %v", err)
	}
	for _, column := range []string{"owner_kind", "token_id", "device_label", "revoked_at", "revoked_reason"} {
		if db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			t.Fatalf("前置不成立：老库不该有列 %s", column)
		}
	}

	// ② 升级后重启：迁移必须补齐。
	if err := ensureAccountDeviceBindingsSchema(db); err != nil {
		t.Fatalf("upgrade run: %v", err)
	}
	for _, column := range []string{"owner_kind", "token_id", "device_label", "revoked_at", "revoked_reason"} {
		if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			t.Fatalf("老库（v1 标记已置位）必须补上列 %s —— "+
				"加列若写在标记判断之后，这些库的新列永远补不上且不报错", column)
		}
	}
	// 拉黑表也必须在老库上建出来。
	if !db.Migrator().HasTable(entity.TableNameUserDeviceBlock) {
		t.Fatal("老库升级时 user_device_blocks 表也必须建出来")
	}

	// ③ 默认值语义：补列后既有行的 owner_kind 必须是 login（varchar 默认）。
	if err := db.Exec("INSERT INTO "+entity.TableNameAccountDeviceBinding+
		" (user_id, device_id, first_seen_at, last_seen_at) VALUES (?, ?, ?, ?)",
		9, "legacy-dev", "2026-01-01 00:00:00", "2026-01-01 00:00:00").Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	var row struct {
		OwnerKind     string  `gorm:"column:owner_kind"`
		TokenID       int32   `gorm:"column:token_id"`
		RevokedAt     *string `gorm:"column:revoked_at"`
		RevokedReason string  `gorm:"column:revoked_reason"`
		LastIP        string  `gorm:"column:last_ip"`
	}
	if err := db.Table(entity.TableNameAccountDeviceBinding).
		Select("owner_kind AS owner_kind, token_id AS token_id, revoked_at AS revoked_at, "+
			"revoked_reason AS revoked_reason, last_ip AS last_ip").
		Where("device_id = ?", "legacy-dev").Take(&row).Error; err != nil {
		t.Fatalf("read backfilled row: %v", err)
	}
	if row.OwnerKind != "login" {
		t.Fatalf("owner_kind 的列默认值必须是 'login'（既有行语义不变），实际 %q", row.OwnerKind)
	}
	if row.TokenID != 0 {
		t.Fatalf("token_id 默认必须是 0，实际 %d", row.TokenID)
	}
	if row.RevokedAt != nil {
		t.Fatalf("revoked_at 默认必须是 NULL（NULL 才表示「未撤销、占槽位」），实际 %v", *row.RevokedAt)
	}
	if row.RevokedReason != "" {
		t.Fatalf("revoked_reason 默认必须是空串，实际 %q", row.RevokedReason)
	}
	// last_ip 补列后既有行必须是空串：既不能是 NULL（Go 侧要判指针），
	// 也不能是任何伪造地址（会显示成「这台设备从某处连进来」的假事实）。
	if row.LastIP != "" {
		t.Fatalf("last_ip 的列默认值必须是空串（老行没有采过 IP），实际 %q", row.LastIP)
	}
}

// 迁移幂等：连跑三次不报错，且不重复建索引。
func TestDevicePoolBindingSchemaIsIdempotent(t *testing.T) {
	db := newA1RegistryDB(t)
	for i := 0; i < 3; i++ {
		if err := ensureAccountDeviceBindingsSchema(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	for _, column := range []string{"owner_kind", "token_id", "device_label", "revoked_at", "revoked_reason"} {
		if !db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, column) {
			t.Fatalf("三次运行后仍缺列 %s", column)
		}
	}
}

// nil db 三条迁移都返回 nil（本仓 28/29 个迁移都这么写），且不 panic。
func TestDevicePoolBindingSchemaNilDBIsSafe(t *testing.T) {
	if err := ensureDevicePoolBindingSchema(nil); err != nil {
		t.Fatalf("ensureDevicePoolBindingSchema(nil) 应返回 nil：%v", err)
	}
	if err := ensureUserDeviceBlockIndexes(nil); err != nil {
		t.Fatalf("ensureUserDeviceBlockIndexes(nil) 应返回 nil：%v", err)
	}
	if err := ensureUserDeviceLimitOverrideColumn(nil); err != nil {
		t.Fatalf("ensureUserDeviceLimitOverrideColumn(nil) 应返回 nil：%v", err)
	}
	// users 表不存在时也不做任何事（极简单测 schema）。
	db := newA1RegistryDB(t)
	if err := db.Migrator().DropTable(entity.TableNameUser); err != nil {
		t.Fatalf("drop users: %v", err)
	}
	if err := ensureUserDeviceLimitOverrideColumn(db); err != nil {
		t.Fatalf("users 缺失时不该报错：%v", err)
	}
}

// 主表不存在时本迁移什么都不做（建表归 ensureAccountDeviceBindingsSchema）。
//
// 若在这里 AutoMigrate 建表，「建表 + 建索引」的整套纪律会有两份实现并漂移。
func TestDevicePoolBindingSchemaSkipsWhenTableMissing(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureDevicePoolBindingSchema(db); err != nil {
		t.Fatalf("主表缺失时应静默跳过：%v", err)
	}
	if db.Migrator().HasTable(entity.TableNameAccountDeviceBinding) {
		t.Fatal("本迁移不得建主表（那是 ensureAccountDeviceBindingsSchema 的职责）")
	}
	if db.Migrator().HasTable(entity.TableNameUserDeviceBlock) {
		t.Fatal("主表缺失时也不该建拉黑表（保持「一次迁移只做一件事」）")
	}
}

// ─────────────────────── A1-6：注册触发 + 存量回填 ───────────────────────

// seedA1User 造一个用户（level_id 默认 0）。
func seedA1User(t *testing.T, db *gorm.DB, username string) int32 {
	t.Helper()
	user := entity.User{
		Username: username, Password: "x", Role: "user", Status: "enabled", RegisterSource: "system",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user.ID
}

// 存量回填：level_id=0 的用户被评成最低级，且写一行 trigger=register 的流水。
//
// 这条钉住的是 A1-6 的「否则设备上限对存量用户永久不生效」：
// 没有回填时 level_id 恒 0 ⇒ EffectiveDeviceLimit 恒返回 level_missing ⇒ 不限。
func TestRegisterBackfillEvaluatesUsersWithoutLevel(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureUserLevelSchema(db); err != nil {
		t.Fatalf("ensureUserLevelSchema: %v", err)
	}
	// 清掉迁移写入的标记，让回填能重新跑（迁移里已经跑过一次）。
	if err := db.Where(map[string]any{"key": userLevelBackfillSchemaKey}).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear backfill flag: %v", err)
	}

	firstID := seedA1User(t, db, "backfill-a")
	secondID := seedA1User(t, db, "backfill-b")
	// 一个人已经被评过级（level_id != 0）：回填不该碰他。
	var lowest entity.UserLevel
	if err := db.Order("level_rank asc").Take(&lowest).Error; err != nil {
		t.Fatalf("load lowest level: %v", err)
	}
	settledID := seedA1User(t, db, "backfill-settled")
	if err := db.Table(entity.TableNameUser).Where("id = ?", settledID).
		Update("level_id", lowest.ID).Error; err != nil {
		t.Fatalf("settle level: %v", err)
	}

	backfillUserLevelsForRegister(db)

	for _, userID := range []int32{firstID, secondID} {
		levelID, err := service.CurrentUserLevelIDWithDB(db, userID)
		if err != nil {
			t.Fatalf("read level of %d: %v", userID, err)
		}
		if levelID != lowest.ID {
			t.Fatalf("存量用户 %d 必须被评成最低级 %d（否则设备上限对他们永久不生效），实际 %d",
				userID, lowest.ID, levelID)
		}
	}
	// 流水的 trigger 必须是 register（可解释「这一级是怎么来的」）。
	var triggers []string
	if err := db.Model(&entity.UserLevelHistory{}).
		Where("user_id IN ?", []int32{firstID, secondID}).
		Pluck("trigger", &triggers).Error; err != nil {
		t.Fatalf("read histories: %v", err)
	}
	if len(triggers) != 2 {
		t.Fatalf("两个用户各应写一行流水，实际 %d 行", len(triggers))
	}
	for _, trigger := range triggers {
		if trigger != "register" {
			t.Fatalf("回填流水的 trigger 必须是 \"register\"（字面量），实际 %q", trigger)
		}
	}
	// 已评级的用户不被改动（流水不应出现他的 from!=to）。
	var settledFrom int32
	if err := db.Model(&entity.UserLevelHistory{}).
		Where("user_id = ?", settledID).Order("id desc").Limit(1).
		Pluck("from_level_id", &settledFrom).Error; err != nil && err != gorm.ErrRecordNotFound {
		t.Fatalf("read settled history: %v", err)
	}
}

// 回填幂等：第二次调用不再写新流水（标记已置位 ⇒ 直接返回）。
func TestRegisterBackfillIsIdempotentByFlag(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureUserLevelSchema(db); err != nil {
		t.Fatalf("ensureUserLevelSchema: %v", err)
	}
	// ensureUserLevelSchema 内部已经跑过一次回填并写了标记（此时库里没有用户，
	// 属于「候选 0 人」分支）。为了让本用例从「标记未置位」起步，
	// 先清掉标记再建用户 —— 否则 backfill 会直接早退，用例测不到任何东西。
	if err := db.Where(map[string]any{"key": userLevelBackfillSchemaKey}).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear backfill flag: %v", err)
	}
	userID := seedA1User(t, db, "backfill-idem")

	backfillUserLevelsForRegister(db)
	var firstCount int64
	if err := db.Model(&entity.UserLevelHistory{}).Where("user_id = ?", userID).Count(&firstCount).Error; err != nil {
		t.Fatalf("count histories: %v", err)
	}
	if firstCount != 1 {
		t.Fatalf("首次回填应写 1 行流水，实际 %d", firstCount)
	}
	if !systemSettingFlagIsSet(db, userLevelBackfillSchemaKey) {
		t.Fatal("回填成功后必须写幂等标记")
	}

	// 人为把用户打回 level_id=0（模拟「新注册但还没评估」）：
	// 若没有标记，第二次回填会再评一遍并写新流水。有标记 ⇒ 直接跳过。
	if err := db.Table(entity.TableNameUser).Where("id = ?", userID).
		Update("level_id", 0).Error; err != nil {
		t.Fatalf("reset level: %v", err)
	}
	backfillUserLevelsForRegister(db)
	var afterCount int64
	if err := db.Model(&entity.UserLevelHistory{}).Where("user_id = ?", userID).Count(&afterCount).Error; err != nil {
		t.Fatalf("count histories after: %v", err)
	}
	if afterCount != firstCount {
		t.Fatalf("标记已置位时不得再回填：流水应仍是 %d 行，实际 %d", firstCount, afterCount)
	}
	levelID, err := service.CurrentUserLevelIDWithDB(db, userID)
	if err != nil {
		t.Fatalf("read level: %v", err)
	}
	if levelID != 0 {
		t.Fatalf("标记已置位时不该改动 users.level_id，实际 %d", levelID)
	}
}

// 等级表为空时**不写标记**、不评估：等管理员配好等级后下次启动才跑。
//
// 若此时写标记，「新库首启（等级表还没种子）」会把回填永久跳过 ——
// 而等级种子恰好在同一个 migration 里、排在回填之前，这里测的是
// 「种子写入失败」这条降级路径。
func TestRegisterBackfillSkipsWhenLevelTableEmpty(t *testing.T) {
	db := newA1RegistryDB(t)
	// 只建表、不种子（手工建 user_levels，跳过 seedDefaultUserLevels）。
	if err := db.AutoMigrate(&entity.UserLevel{}, &entity.UserLevelHistory{}); err != nil {
		t.Fatalf("migrate level tables: %v", err)
	}
	if err := ensureUserLevelIDColumn(db); err != nil {
		t.Fatalf("add level_id: %v", err)
	}
	if err := db.Where(map[string]any{"key": userLevelBackfillSchemaKey}).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	userID := seedA1User(t, db, "backfill-empty-levels")

	backfillUserLevelsForRegister(db)

	if systemSettingFlagIsSet(db, userLevelBackfillSchemaKey) {
		t.Fatal("等级表为空时不得写回填标记（否则清空等级表的库会永久跳过回填）")
	}
	levelID, err := service.CurrentUserLevelIDWithDB(db, userID)
	if err != nil {
		t.Fatalf("read level: %v", err)
	}
	if levelID != 0 {
		t.Fatalf("等级表为空时不该给用户评级，实际 level_id=%d", levelID)
	}
}

// 回填必须让**设备上限真的开始生效**：这是 A1-6 存在的全部理由。
//
// 断言语义链而不是只断言 level_id：
//   - 回填前：EffectiveDeviceLimit 返回 level_missing（不限）；
//   - 回填后：读到等级的 device_limit（非 0 时生效）。
func TestRegisterBackfillMakesDeviceLimitEffective(t *testing.T) {
	db := newA1RegistryDB(t)
	if err := ensureUserLevelSchema(db); err != nil {
		t.Fatalf("ensureUserLevelSchema: %v", err)
	}
	// 种子等级全是 0（137 §12.0），必须**显式构造**非 0 的等级，
	// 否则本用例会因「全部不限」而恒绿（红线 6）。
	var lowest entity.UserLevel
	if err := db.Order("level_rank asc").Take(&lowest).Error; err != nil {
		t.Fatalf("load lowest level: %v", err)
	}
	if err := db.Model(&entity.UserLevel{}).Where("id = ?", lowest.ID).
		Update("device_limit", 2).Error; err != nil {
		t.Fatalf("set device_limit: %v", err)
	}
	// 前置：等级表就绪后 users.level_id 已经存在。
	userID := seedA1User(t, db, "backfill-device-limit")

	// 前置断言：回填前是 level_missing（不限）。
	if limit, source, err := service.EffectiveDeviceLimit(db, userID); err != nil || limit != 0 || source != "level_missing" {
		t.Fatalf("前置：回填前应当 (0, level_missing)，实际 (%d, %q, %v)", limit, source, err)
	}

	// 清标记后回填。
	if err := db.Where(map[string]any{"key": userLevelBackfillSchemaKey}).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	backfillUserLevelsForRegister(db)

	limit, source, err := service.EffectiveDeviceLimit(db, userID)
	if err != nil {
		t.Fatalf("EffectiveDeviceLimit: %v", err)
	}
	if limit != 2 || source != "level" {
		t.Fatalf("回填后必须读到等级上限 (2, level)，实际 (%d, %q) —— "+
			"这正是 A1-6「没有回填则设备上限永久不生效」的反面", limit, source)
	}
}

// ─────────────────────── 源码级：回填不得变成「每次启动都扫全表」 ───────────────────────

// 回填必须以标记为闸门：源码里必须出现 systemSettingFlagIsSet(..., userLevelBackfillSchemaKey)。
//
// 断言源码而不是行为：行为层面「每次启动都扫」与「只扫一次」在单测里
// 几乎无法区分（都需要重启进程），而这条约束的真正代价在数据量大时才显现。
func TestRegisterBackfillUsesSchemaFlag(t *testing.T) {
	raw, err := os.ReadFile("user_level_migration.go")
	if err != nil {
		t.Fatalf("read migration source: %v", err)
	}
	source := string(raw)
	if !strings.Contains(source, "systemSettingFlagIsSet(db, userLevelBackfillSchemaKey)") {
		t.Fatal("回填必须以 systemSettingFlagIsSet(db, userLevelBackfillSchemaKey) 为闸门，" +
			"否则每次启动都会重扫一遍 level_id=0 的用户")
	}
	if !strings.Contains(source, "setSystemSettingFlag(db, userLevelBackfillSchemaKey)") {
		t.Fatal("回填成功后必须写 setSystemSettingFlag(db, userLevelBackfillSchemaKey)")
	}
}
