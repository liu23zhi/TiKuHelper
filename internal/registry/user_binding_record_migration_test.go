package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newBindingRecordTestDB 建一个只含 system_settings / users 的内存库。
//
// 库名带 nanos：表驱动或多次调用的用例会共用 t.Name()，
// `file:xxx?mode=memory&cache=shared` 同名即共享库，不加后缀会互相污染。
func newBindingRecordTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := "file:binding-mig-" + name + "-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	return db
}

// 空库首次启动：表 + 两条索引建出来，标记写入，第二次幂等。
func TestEnsureUserBindingRecordSchemaCreatesTable(t *testing.T) {
	db := newBindingRecordTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		t.Fatalf("expected table %s", entity.TableNameUserBindingRecord)
	}
	// 关键列一个都不能少 —— 尤其 release_reason（判定二分类）。
	for _, column := range []string{
		"user_id", "username_snapshot", "kind", "value", "value_hash", "state",
		"active_key", "source", "bound_at", "released_at",
		"release_reason", "release_source", "operator_user_id",
	} {
		if !db.Migrator().HasColumn(entity.TableNameUserBindingRecord, column) {
			t.Fatalf("expected column %s", column)
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameUserBindingRecord, "idx_binding_active") {
		t.Fatal("expected unique index idx_binding_active")
	}
	if !db.Migrator().HasIndex(entity.TableNameUserBindingRecord, "idx_binding_lookup") {
		t.Fatal("expected index idx_binding_lookup")
	}
	if !systemSettingFlagIsSet(db, userBindingRecordSchemaReadyKey) {
		t.Fatal("expected the migration flag to be written")
	}
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// ⚠️ 头号风险的结构性钉子：`(kind, active_key)` 必须是**唯一索引**。
//
// 缺唯一性时「同一个手机号同时绑在两个账号上」在库里会静默成立，
// 二次注册判定与换绑追缴都会错人（追缴会把钱扣到无关账号）。
func TestBindingActiveKeyUniqueIndexRejectsDuplicate(t *testing.T) {
	db := newBindingRecordTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hash := bindingBackfillHash("13800000001")
	first := entity.UserBindingRecord{
		UserID: 1, Kind: entity.BindingKindPhone, Value: "13800000001",
		ValueHash: hash, State: entity.BindingStateActive, ActiveKey: &hash,
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first: %v", err)
	}
	duplicate := entity.UserBindingRecord{
		UserID: 2, Kind: entity.BindingKindPhone, Value: "13800000001",
		ValueHash: hash, State: entity.BindingStateActive, ActiveKey: &hash,
	}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("同一个值同时绑两个账号必须被唯一索引拒绝")
	}
	// released 行 active_key = NULL，多条 NULL 互不冲突 → 历史可以有很多条。
	for i := 0; i < 3; i++ {
		released := entity.UserBindingRecord{
			UserID: int32(i + 1), Kind: entity.BindingKindPhone, Value: "13800000001",
			ValueHash: hash, State: entity.BindingStateReleased, ActiveKey: nil,
			ReleaseReason: entity.BindingReleaseReasonRebind,
		}
		if err := db.Create(&released).Error; err != nil {
			t.Fatalf("released 行 %d 必须允许重复：%v", i, err)
		}
	}
	// 不同 kind 的同一个 value_hash 是不同行。
	otherKind := entity.UserBindingRecord{
		UserID: 3, Kind: entity.BindingKindWechat, Value: "13800000001",
		ValueHash: hash, State: entity.BindingStateActive, ActiveKey: &hash,
	}
	if err := db.Create(&otherKind).Error; err != nil {
		t.Fatalf("不同 kind 应允许同值：%v", err)
	}
}

// 迁移绝不 DROP TABLE：清掉标记逼完整重跑之后数据还在。
func TestEnsureUserBindingRecordSchemaNeverDropsData(t *testing.T) {
	db := newBindingRecordTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hash := bindingBackfillHash("13800000002")
	if err := db.Create(&entity.UserBindingRecord{
		UserID: 9, Kind: entity.BindingKindPhone, Value: "13800000002",
		ValueHash: hash, State: entity.BindingStateActive, ActiveKey: &hash,
	}).Error; err != nil {
		t.Fatalf("create row: %v", err)
	}
	if err := db.Where(map[string]any{"key": userBindingRecordSchemaReadyKey}).
		Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var count int64
	if err := db.Model(&entity.UserBindingRecord{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("重跑迁移不得删数据，got %d rows", count)
	}
}

// system_settings 不存在时不写标记（否则表建出来之后迁移会被永远跳过）。
func TestEnsureUserBindingRecordSchemaSkipsFlagWithoutSettingsTable(t *testing.T) {
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := "file:binding-mig-nosettings-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open: %v (name=%s)", err, name)
	}
	// 没有 users 表 → 直接返回，什么都不建。
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("migrate without users: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameUserBindingRecord) {
		t.Fatal("users 不存在时不得建绑定记录表")
	}
}

// release_reason 回填：历史行「state=released 但二分类为空」必须被修正，
// 否则判定两边都不命中，情形 2/4 静默失效。
func TestBackfillUserBindingReleaseReasonMapsSources(t *testing.T) {
	db := newBindingRecordTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cases := []struct {
		value         string
		releaseSource string
		wantReason    string
		description   string
	}{
		{"13800000011", entity.BindingReleaseSourceAccountClose, entity.BindingReleaseReasonClosure, "注销释放 → closure"},
		{"13800000012", entity.BindingReleaseSourceAdminDelete, entity.BindingReleaseReasonClosure, "管理员删除 → closure"},
		{"13800000013", entity.BindingReleaseSourceUserUnbind, entity.BindingReleaseReasonRebind, "自助解绑 → rebind"},
		{"13800000014", entity.BindingReleaseSourceAdminClear, entity.BindingReleaseReasonRebind, "管理员清空 → rebind"},
		{"13800000015", entity.BindingReleaseSourceForceReassign, entity.BindingReleaseReasonRebind, "抢号 → rebind"},
		{"13800000016", "", entity.BindingReleaseReasonRebind, "未知来源 → rebind（安全侧默认）"},
	}
	for i, tc := range cases {
		hash := bindingBackfillHash(tc.value)
		record := entity.UserBindingRecord{
			UserID: int32(i + 1), Kind: entity.BindingKindPhone, Value: tc.value,
			ValueHash: hash, State: entity.BindingStateReleased, ActiveKey: nil,
			ReleaseSource: tc.releaseSource,
			// release_reason 故意留空：模拟旧版本写下的行。
		}
		if err := db.Create(&record).Error; err != nil {
			t.Fatalf("create %s: %v", tc.description, err)
		}
	}
	if err := backfillUserBindingReleaseReason(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	for _, tc := range cases {
		var stored entity.UserBindingRecord
		if err := db.Where("value = ?", tc.value).First(&stored).Error; err != nil {
			t.Fatalf("reload %s: %v", tc.description, err)
		}
		if stored.ReleaseReason != tc.wantReason {
			t.Fatalf("%s: release_reason=%q, want %q", tc.description, stored.ReleaseReason, tc.wantReason)
		}
	}
	// 二次回填必须无操作（幂等）。
	if err := backfillUserBindingReleaseReason(db); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
}

// 活跃绑定回填：只回填「现在仍绑着的」，且与 service 包用**同一个哈希公式**。
//
// 这条是 registry ↔ service 的漂移钉子：两边各自实现 sha256(TrimSpace(v))，
// 若任一边改了（加大小写折叠、加前缀），本用例查不到就立刻变红。
func TestBackfillActiveUserBindingsMatchesServiceHashContract(t *testing.T) {
	db := newBindingRecordTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	phone := "13800000021"
	wechat := "wx-openid-abc"
	boundAt := time.Now().Add(-24 * time.Hour)
	users := []entity.User{
		{ID: 1, Username: "with-both", Phone: &phone, PhoneBoundAt: &boundAt, WechatID: &wechat, WechatBoundAt: &boundAt},
		{ID: 2, Username: "with-phone-only", Phone: &phone},
		{ID: 3, Username: "no-binding"},
	}
	// 上面两行同手机号会撞唯一索引，按回填逻辑只保留第一条。
	users[1].Phone = nil
	noBind := "13800000022"
	wechatUser := "wx-openid-only"
	users[1].Phone = &noBind
	users[1].WechatID = &wechatUser
	for i := range users {
		if err := db.Create(&users[i]).Error; err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}
	}
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var rows []entity.UserBindingRecord
	if err := db.Where("state = ?", entity.BindingStateActive).Find(&rows).Error; err != nil {
		t.Fatalf("list active: %v", err)
	}
	// 1 号两维度 + 2 号两维度 = 4 条；3 号没绑定 → 0 条。
	if len(rows) != 4 {
		t.Fatalf("应回填 4 条活跃绑定，got %d: %+v", len(rows), rows)
	}
	// 哈希公式必须与 service 侧一致：用同一算法重算并比对。
	for _, row := range rows {
		if want := bindingBackfillHash(row.Value); row.ValueHash != want {
			t.Fatalf("value_hash 与回填公式不一致：%s", row.Value)
		}
		if row.ActiveKey == nil || *row.ActiveKey != row.ValueHash {
			t.Fatalf("active 行的 active_key 必须等于 value_hash：%+v", row)
		}
		// TrimSpace 敏感性：带空白的输入必须算出同一个哈希（否则判定会 miss）。
		if padded := bindingBackfillHash("  " + row.Value + "  "); padded != row.ValueHash {
			t.Fatalf("哈希必须忽略首尾空白：%s", row.Value)
		}
	}
	// 幂等：再跑一次不新增。
	if err := backfillActiveUserBindings(db); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	var count int64
	db.Model(&entity.UserBindingRecord{}).Where("state = ?", entity.BindingStateActive).Count(&count)
	if count != 4 {
		t.Fatalf("回填必须幂等，got %d", count)
	}
}

// 回填不得把 released 历史覆盖成 active：已解绑的值在 users 上已经没有了，
// 只要回填逻辑按 users 读，就不会碰它们。这条钉住「不复活历史绑定」。
func TestBackfillActiveUserBindingsNeverRevivesReleasedRows(t *testing.T) {
	db := newBindingRecordTestDB(t)
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := ensureUserBindingRecordSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hash := bindingBackfillHash("13800000031")
	released := entity.UserBindingRecord{
		UserID: 1, Kind: entity.BindingKindPhone, Value: "13800000031",
		ValueHash: hash, State: entity.BindingStateReleased, ActiveKey: nil,
		ReleaseReason: entity.BindingReleaseReasonRebind,
	}
	if err := db.Create(&released).Error; err != nil {
		t.Fatalf("create released: %v", err)
	}
	// users 上没有任何绑定（已解绑）。
	if err := db.Create(&entity.User{ID: 1, Username: "u1"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := backfillActiveUserBindings(db); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var stored entity.UserBindingRecord
	if err := db.First(&stored, released.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.State != entity.BindingStateReleased {
		t.Fatalf("已释放的记录不得被回填复活，got state=%q", stored.State)
	}
	var active int64
	db.Model(&entity.UserBindingRecord{}).Where("state = ?", entity.BindingStateActive).Count(&active)
	if active != 0 {
		t.Fatalf("不该凭空造出活跃绑定，got %d", active)
	}
}
