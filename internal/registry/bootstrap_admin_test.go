package registry

import (
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newBootstrapAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(quietSQLiteOpen("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// quietSQLiteOpen 用生产同款 Dialector（含不再逐列重建的包装）打开 SQLite。
func quietSQLiteOpen(dsn string) gorm.Dialector {
	return sqliteQuietDialector{Dialector: sqlite.Open(dsn)}
}

// countTableInserts 统计对某张表的 INSERT 次数。
func countTableInserts(db *gorm.DB, table string) *int32 {
	var count int32
	_ = db.Callback().Create().After("gorm:create").Register("test:count_"+table, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == table {
			atomic.AddInt32(&count, 1)
		}
	})
	return &count
}

// 不存在管理员时必须创建。
func TestBootstrapAdminAccountCreatesWhenMissing(t *testing.T) {
	db := newBootstrapAdminTestDB(t)

	bootstrapAdminAccount(db, "hashed-password")

	var admin entity.User
	if err := db.Where("id = ?", 1).First(&admin).Error; err != nil {
		t.Fatalf("expected the bootstrap admin to exist: %v", err)
	}
	if admin.Username != "admin" || admin.Password != "hashed-password" || admin.Role != "admin" {
		t.Fatalf("unexpected bootstrap admin: %+v", admin)
	}
}

// 回归：管理员已存在时不能再插一次（否则每次启动都撞 users.id 唯一键并刷 ERROR）。
func TestBootstrapAdminAccountDoesNotInsertWhenAdminExists(t *testing.T) {
	db := newBootstrapAdminTestDB(t)
	if err := db.Create(&entity.User{
		ID: 1, Username: "admin", Password: "keep-me", Role: "admin", Status: "enabled",
	}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	inserts := countTableInserts(db, entity.TableNameUser)
	bootstrapAdminAccount(db, "hashed-password")

	if got := atomic.LoadInt32(inserts); got != 0 {
		t.Fatalf("expected no INSERT into users when the admin exists, got %d", got)
	}
	var admin entity.User
	if err := db.Where("id = ?", 1).First(&admin).Error; err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if admin.Password != "keep-me" {
		t.Fatalf("existing password must not be touched, got %q", admin.Password)
	}
}

// 明文默认密码必须升级成哈希。
func TestBootstrapAdminAccountUpgradesLegacyPlaintextPassword(t *testing.T) {
	db := newBootstrapAdminTestDB(t)
	if err := db.Create(&entity.User{
		ID: 1, Username: "admin", Password: "123456", Role: "admin", Status: "enabled",
	}).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	bootstrapAdminAccount(db, "hashed-password")

	var admin entity.User
	if err := db.Where("id = ?", 1).First(&admin).Error; err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if admin.Password != "hashed-password" {
		t.Fatalf("expected the plaintext default password to be upgraded, got %q", admin.Password)
	}
}
