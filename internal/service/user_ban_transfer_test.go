package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestUserBanImportPreviewMatchesIDAndRechecksIdentityAtomically(t *testing.T) {
	db := managementTransferDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.UserBan{}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []entity.User{{ID: 401, Username: "actual-one", Nickname: "目标一"}, {ID: 402, Username: "actual-two"}} {
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	content := []byte(`[{"user_id":401,"username":"must-not-be-used","reason":"测试","expires_at":"2030-01-01T00:00:00Z"},{"user_id":402,"reason":"测试二"}]`)
	targets, err := PreviewUserBanImportWithDB(db, "json", content, now)
	if err != nil || len(targets) != 2 || targets[0].Username != "actual-one" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	var count int64
	db.Model(&entity.UserBan{}).Count(&count)
	if count != 0 {
		t.Fatal("preview wrote bans")
	}
	db.Model(&entity.User{}).Where("id = ?", 402).Update("username", "changed-user")
	if _, err := ApplyUserBanImportWithDB(db, targets, 999, now); err == nil {
		t.Fatal("changed target accepted")
	}
	db.Model(&entity.UserBan{}).Count(&count)
	if count != 0 {
		t.Fatal("failed confirmation partially wrote bans")
	}
	targets, err = PreviewUserBanImportWithDB(db, "json", content, now)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := ApplyUserBanImportWithDB(db, targets, 999, now); err != nil || n != 2 {
		t.Fatalf("applied=%d err=%v", n, err)
	}
	for _, id := range []int32{401, 402} {
		banned, err := IsBannedAt(db, id, now)
		if err != nil || !banned {
			t.Fatalf("%d banned=%v err=%v", id, banned, err)
		}
	}
	output, err := ExportDataWithDB(db, ExportDataOptions{Resource: "user_bans", Format: "json"})
	if err != nil || output.Rows != 2 || !strings.Contains(string(output.Content), "actual-one") {
		t.Fatalf("export=%+v err=%v", output, err)
	}
}

func TestUserBanImportRejectsMissingIDUnknownUsersDuplicateAndPastTime(t *testing.T) {
	db := managementTransferDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.UserBan{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.User{ID: 1, Username: "named-target"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`[{"username":"named-target","reason":"x"}]`,
		`[{"user_id":999,"username":"named-target","reason":"x"}]`,
		`[{"user_id":1,"reason":""}]`,
		`[{"user_id":1,"reason":"x","expires_at":"2000-01-01T00:00:00Z"}]`,
		`[{"user_id":1,"reason":"x"},{"user_id":1,"reason":"y"}]`,
	} {
		if _, err := PreviewUserBanImportWithDB(db, "json", []byte(input), time.Now()); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestUserBanImportRollsBackEveryAccountOnWriteFailure(t *testing.T) {
	db := managementTransferDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.UserBan{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int32{401, 402} {
		if err := db.Create(&entity.User{ID: id, Username: fmt.Sprintf("target-%d", id)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	targets, err := PreviewUserBanImportWithDB(db, "csv", []byte("user_id,reason\n401,test\n402,test\n"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_second_ban BEFORE INSERT ON user_bans WHEN NEW.user_id = 402 BEGIN SELECT RAISE(FAIL, 'synthetic write failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyUserBanImportWithDB(db, targets, 999, time.Now()); err == nil {
		t.Fatal("write failure accepted")
	}
	var count int64
	db.Model(&entity.UserBan{}).Count(&count)
	if count != 0 {
		t.Fatalf("partial bans persisted: %d", count)
	}
	var users []entity.User
	db.Find(&users)
	for _, user := range users {
		if user.Status != "enabled" {
			t.Fatalf("partial status persisted: %+v", user)
		}
	}
}
