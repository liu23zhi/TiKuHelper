package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func newQuietSQLiteTransactionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqliteQuietDialector{Dialector: sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared")}, &gorm.Config{PrepareStmt: true})
	if err != nil {
		t.Fatalf("open production SQLite dialector: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestQuietSQLiteNestedTransactionRollsBackOnlyInnerChanges(t *testing.T) {
	db := newQuietSQLiteTransactionTestDB(t)
	if err := db.Create(&entity.User{ID: 1, Username: "nested", Password: "hash", Nickname: "original"}).Error; err != nil {
		t.Fatal(err)
	}
	innerErr := errors.New("reject inner changes")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entity.User{}).Where("id = ?", 1).Update("nickname", "outer").Error; err != nil {
			return err
		}
		err := tx.Transaction(func(nested *gorm.DB) error {
			if err := nested.Model(&entity.User{}).Where("id = ?", 1).Update("nickname", "inner").Error; err != nil {
				return err
			}
			return innerErr
		})
		if !errors.Is(err, innerErr) {
			return err
		}
		var user entity.User
		if err := tx.First(&user, 1).Error; err != nil {
			return err
		}
		if user.Nickname != "outer" {
			t.Fatalf("inner rollback changed the outer transaction: %q", user.Nickname)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("production SQLite wrapper must support nested savepoints: %v", err)
	}
	var stored entity.User
	if err := db.First(&stored, 1).Error; err != nil || stored.Nickname != "outer" {
		t.Fatalf("outer transaction did not commit: user=%+v err=%v", stored, err)
	}
}

func TestQuietSQLiteUserEditCommitsBindingsAndProfileAtomically(t *testing.T) {
	db := newQuietSQLiteTransactionTestDB(t)
	if err := db.Create(&entity.User{ID: 3, Username: "edit-user", Password: "hash", Nickname: "old", Balance: 12.345678}).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := service.UpdateUserPhoneWithDB(tx, 3, "13800000000", false); err != nil {
			return err
		}
		if _, err := service.UpdateUserWechatBindingWithDB(tx, 3, "openid-edited", false); err != nil {
			return err
		}
		return tx.Model(&entity.User{}).Where("id = ?", 3).Update("nickname", "updated").Error
	})
	if err != nil {
		t.Fatalf("user editing transaction with production driver failed: %v", err)
	}
	var stored entity.User
	if err := db.First(&stored, 3).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Nickname != "updated" || stored.Phone == nil || *stored.Phone != "13800000000" || stored.PhoneBoundAt == nil || stored.WechatID == nil || *stored.WechatID != "openid-edited" || stored.WechatBoundAt == nil || stored.Balance != 12.345678 {
		t.Fatalf("user edit lost fields or changed balance: %+v", stored)
	}
}

func TestQuietSQLiteUserEditRollsBackPhoneWhenWechatIsOccupied(t *testing.T) {
	db := newQuietSQLiteTransactionTestDB(t)
	occupiedID := "openid-occupied"
	if err := db.Create(&[]entity.User{
		{ID: 3, Username: "target", Password: "hash", Nickname: "original"},
		{ID: 4, Username: "owner", Password: "hash", WechatID: &occupiedID},
	}).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := service.UpdateUserPhoneWithDB(tx, 3, "13800000000", false); err != nil {
			return err
		}
		_, err := service.UpdateUserWechatBindingWithDB(tx, 3, occupiedID, false)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "该微信已绑定其他账号") {
		t.Fatalf("expected occupied WeChat rejection, got %v", err)
	}
	var stored entity.User
	if err := db.First(&stored, 3).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Phone != nil || stored.PhoneBoundAt != nil || stored.WechatID != nil || stored.Nickname != "original" {
		t.Fatalf("failed user edit partially persisted: %+v", stored)
	}
}
