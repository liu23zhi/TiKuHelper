package service

import (
	"errors"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func TestIssueUserLoginJWTIsAtomicOnWriteFailure(t *testing.T) {
	db := newWechatLoginTestDB(t)
	user := entity.User{Username: "session-failure", Password: "old-password", Status: "enabled", LoginSessionID: "old-session"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("metadata unavailable")
	if err := db.Callback().Update().Before("gorm:update").Register("fail_session_metadata", func(tx *gorm.DB) {
		if _, updatingMetadata := tx.Statement.Dest.(map[string]any)["last_login_at"]; updatingMetadata {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if token, err := IssueUserLoginJWTWithDB(db, &user, "127.0.0.1", "upgraded-password"); err == nil || token != "" {
		t.Fatalf("failed login returned token=%q err=%v", token, err)
	}
	var stored entity.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LoginSessionID != "old-session" || stored.Password != "old-password" || stored.LastLoginAt != nil {
		t.Fatalf("failed login changed account: %+v", stored)
	}
}

func TestIssueUserLoginJWTRotatesAndRejectsChangedCredentials(t *testing.T) {
	db := newWechatLoginTestDB(t)
	user := entity.User{Username: "session-rotation", Password: "password", Status: "enabled"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	first, err := IssueUserLoginJWTWithDB(db, &user, "127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	firstID := user.LoginSessionID
	second, err := IssueUserLoginJWTWithDB(db, &user, "127.0.0.2", "")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || firstID == "" || firstID == user.LoginSessionID {
		t.Fatal("login did not rotate session")
	}
	claims, err := util.ParseJwtWithClaims(second)
	if err != nil || claims.(jwt.MapClaims)["sid"] != user.LoginSessionID {
		t.Fatalf("token session does not match persisted session: %v", err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("password", "new-password").Error; err != nil {
		t.Fatal(err)
	}
	if token, err := IssueUserLoginJWTWithDB(db, &user, "127.0.0.3", ""); err == nil || token != "" {
		t.Fatal("stale authenticated credentials issued a session")
	}
}

func TestLogoutDoesNotRevokeNewerLogin(t *testing.T) {
	db := newWechatLoginTestDB(t)
	user := entity.User{Username: "late-logout", Password: "password", LoginSessionID: "new-session"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := RevokeUserLoginSessionWithDB(db, user.ID, "old-session"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.LoginSessionID != "new-session" {
		t.Fatal("old logout revoked new session")
	}
	if err := RevokeUserLoginSessionWithDB(db, user.ID, "new-session"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&user, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if user.LoginSessionID == "new-session" || user.LoginSessionID == "" {
		t.Fatal("logout did not revoke active session")
	}
}

func TestWechatLoginReplacesExistingSession(t *testing.T) {
	db := newWechatLoginTestDB(t)
	user := entity.User{Username: "wechat-session", Password: "password", LoginSessionID: "old-session"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token, err := issueWechatJWTWithDB(db, &user, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := util.ParseJwtWithClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	var stored entity.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LoginSessionID == "old-session" || stored.LoginSessionID == "" || claims.(jwt.MapClaims)["sid"] != stored.LoginSessionID {
		t.Fatal("wechat login did not replace active session")
	}
}

func TestConcurrentLoginsKeepExactlyOneActiveSession(t *testing.T) {
	db := newWechatLoginTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	user := entity.User{Username: "concurrent-login", Password: "password"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	const devices = 12
	tokens, failures := make(chan string, devices), make(chan error, devices)
	var group sync.WaitGroup
	for i := 0; i < devices; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			deviceUser := user
			token, err := IssueUserLoginJWTWithDB(db, &deviceUser, "127.0.0.1", "")
			if err != nil {
				failures <- err
				return
			}
			tokens <- token
		}()
	}
	group.Wait()
	close(tokens)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var stored entity.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	active := 0
	for token := range tokens {
		claims, err := util.ParseJwtWithClaims(token)
		if err != nil {
			t.Fatal(err)
		}
		if claims.(jwt.MapClaims)["sid"] == stored.LoginSessionID {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("concurrent logins left %d active sessions", active)
	}
}
