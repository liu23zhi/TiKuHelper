package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newUserProfileTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:user-profile-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func TestUpdateCurrentUserProfileWithDBUpdatesCurrentUserAndAllowsEmptyNickname(t *testing.T) {
	db := newUserProfileTestDB(t)
	if err := db.Create(&entity.User{ID: 1, Username: "old_name", Nickname: "旧昵称", Role: "user", Status: "enabled"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	updated, err := UpdateCurrentUserProfileWithDB(db, 1, UpdateUserProfileInput{
		Username: "新_user@1",
		Nickname: "",
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if updated.Username != "新_user@1" || updated.Nickname != "" {
		t.Fatalf("unexpected updated user: %+v", updated)
	}
	var stored entity.User
	if err := db.First(&stored, 1).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.Username != "新_user@1" || stored.Nickname != "" {
		t.Fatalf("unexpected stored profile: %+v", stored)
	}
}

func TestUpdateCurrentUserProfileWithDBRejectsInvalidAndOverlongValues(t *testing.T) {
	db := newUserProfileTestDB(t)
	if err := db.Create(&entity.User{ID: 1, Username: "profile", Role: "user", Status: "enabled"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if _, err := UpdateCurrentUserProfileWithDB(db, 1, UpdateUserProfileInput{Username: "bad-url/", Nickname: "昵称"}); err == nil {
		t.Fatal("expected invalid username error")
	}
	if _, err := UpdateCurrentUserProfileWithDB(db, 1, UpdateUserProfileInput{Username: "profile", Nickname: strings.Repeat("字", UserNicknameMaxLength+1)}); err == nil {
		t.Fatal("expected overlong nickname error")
	}
}

func TestUpdateCurrentUserProfileWithDBRejectsDuplicateUsername(t *testing.T) {
	db := newUserProfileTestDB(t)
	if err := db.Create(&entity.User{
		ID: 1, Username: "profile_one", Nickname: "原昵称", Role: "user", Status: "enabled",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&entity.User{ID: 2, Username: "profile_two", Role: "user", Status: "enabled"}).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	_, err := UpdateCurrentUserProfileWithDB(db, 1, UpdateUserProfileInput{Username: "profile_two", Nickname: "新昵称"})
	if !errors.Is(err, ErrUserProfileUsernameTaken) {
		t.Fatalf("expected duplicate username error, got %v", err)
	}
	var stored entity.User
	if err := db.First(&stored, 1).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.Username != "profile_one" || stored.Nickname != "原昵称" {
		t.Fatalf("duplicate update changed user: %+v", stored)
	}
}
