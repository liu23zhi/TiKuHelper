package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestUserSessionSchemaUpgradesLegacyUsers(t *testing.T) {
	db, err := gorm.Open(sqliteQuietDialector{Dialector: sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared")}, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&legacyUserWithoutPhone{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&legacyUserWithoutPhone{Username: "legacy-session-user", Password: "password"}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.AutoMigrate(&entity.User{}); err != nil {
			t.Fatal(err)
		}
	}
	var user entity.User
	if err := db.First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Username != "legacy-session-user" || user.Password != "password" || user.LoginSessionID != "" {
		t.Fatalf("legacy user changed: %+v", user)
	}
	if !db.Migrator().HasColumn(&entity.User{}, "login_session_id") {
		t.Fatal("login session column missing")
	}
}
