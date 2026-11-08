package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newReferralMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:referral-migration-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 旧库（users 无归因列、无邀请/返佣表）必须被补齐，且历史用户按「无归因」保留。
func TestEnsureReferralSchemaAddsTablesAndColumns(t *testing.T) {
	db := newReferralMigrationTestDB(t)

	// 模拟加列之前的老 users 表（故意不带 invited_by_user_id / invited_at）。
	if err := db.Exec(`CREATE TABLE users (
		id integer PRIMARY KEY AUTOINCREMENT,
		username varchar(255),
		password varchar(255),
		role varchar(32) NOT NULL DEFAULT 'user',
		status varchar(32) NOT NULL DEFAULT 'enabled',
		balance decimal(18,6) NOT NULL DEFAULT 0,
		register_source varchar(32) NOT NULL DEFAULT 'legacy',
		created_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy users: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (username, password) VALUES ('legacy-user', '123456')`).Error; err != nil {
		t.Fatalf("seed legacy user: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}

	if err := ensureReferralSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if !db.Migrator().HasColumn(entity.TableNameUser, "invited_by_user_id") {
		t.Fatal("expected users.invited_by_user_id to be added")
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "invited_at") {
		t.Fatal("expected users.invited_at to be added")
	}
	if !db.Migrator().HasTable(entity.TableNameReferralInvite) {
		t.Fatal("expected referral_invites table")
	}
	if !db.Migrator().HasTable(entity.TableNameReferralRebate) {
		t.Fatal("expected referral_rebates table")
	}

	// 历史用户按「无归因」保留：不猜测历史邀请关系。
	var legacy entity.User
	if err := db.Where("username = ?", "legacy-user").First(&legacy).Error; err != nil {
		t.Fatalf("load legacy user: %v", err)
	}
	if legacy.InvitedByUserID != 0 || legacy.InvitedAt != nil {
		t.Fatalf("legacy user must stay unattributed, got %d / %v", legacy.InvitedByUserID, legacy.InvitedAt)
	}

	// 只跑一次：标记写入后第二次直接返回（结构已存在，行为相同）。
	if !systemSettingFlagIsSet(db, referralSchemaReadyKey) {
		t.Fatal("expected the migration flag to be written")
	}
	if err := ensureReferralSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// 唯一索引必须生效：一单一条返佣、一账户一个邀请码。
func TestReferralSchemaUniqueIndexes(t *testing.T) {
	db := newReferralMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if err := ensureReferralSchema(db); err != nil {
		t.Fatalf("migrate referral: %v", err)
	}

	invite := entity.ReferralInvite{UserID: 1, Code: "ABCDEFGH"}
	if err := db.Create(&invite).Error; err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if err := db.Create(&entity.ReferralInvite{UserID: 2, Code: "ABCDEFGH"}).Error; err == nil {
		t.Fatal("duplicate invite code must be rejected by the unique index")
	}
	if err := db.Create(&entity.ReferralInvite{UserID: 1, Code: "JKLMNPQR"}).Error; err == nil {
		t.Fatal("one account must own at most one invite code")
	}

	rebate := entity.ReferralRebate{
		InviterUserID: 1, InvitedUserID: 2, RechargeOrderID: 77,
		OrderNo: "rcg_unique", BaseAmount: 100, Rate: 0.1, Amount: 10,
		Status: entity.ReferralRebateStatusPending, PolicyVersion: "v1",
	}
	if err := db.Create(&rebate).Error; err != nil {
		t.Fatalf("create rebate: %v", err)
	}
	duplicate := rebate
	duplicate.ID = 0
	duplicate.RechargeOrderID = 77
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate recharge_order_id must be rejected by the unique index")
	}
}

// users 表还不存在时（极端空库）直接返回，不报错 —— 交给 AutoMigrate(User) 处理。
func TestEnsureReferralSchemaSkipsWhenUsersMissing(t *testing.T) {
	db := newReferralMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := ensureReferralSchema(db); err != nil {
		t.Fatalf("expected no error when users is missing, got %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameReferralInvite) {
		t.Fatal("must not create referral tables before users exists")
	}
}
