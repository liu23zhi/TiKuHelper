package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestWechatSuccessfulPollingIssuesAndAuditsOnlyOneLogin(t *testing.T) {
	db := newWechatLoginTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	wechatID := "audit-openid"
	user := entity.User{Username: "wechat-audit", Password: "private-password", Status: "enabled", WechatID: &wechatID}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		_, _ = fmt.Fprintf(w, `{"success":true,"data":{"status":"success","wechat_user":{"openid":%q}}}`, wechatID)
	}))
	t.Cleanup(server.Close)
	store := NewWechatLoginSessionStore()
	store.Create("private-login-token", time.Now(), time.Now().Add(time.Minute))
	input := WechatLoginStatusInput{Client: WechatLoginClient{BaseURL: server.URL, Client: server.Client()}, SessionStore: store, LoginToken: "private-login-token", RequestIP: "127.0.0.1"}
	first, err := CheckWechatLoginStatusWithDB(db, input)
	if err != nil || first.Status != WechatLoginStatusSuccess || first.JWT == "" {
		t.Fatalf("unexpected login result: %+v err=%v", first, err)
	}
	var stored entity.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	firstLoginTime := *stored.LastLoginAt
	for i := 0; i < 3; i++ {
		result, err := CheckWechatLoginStatusWithDB(db, input)
		if err != nil || result != first {
			t.Fatalf("successful polling must reuse its completed result: %+v err=%v", result, err)
		}
	}
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.LastLoginAt.Equal(firstLoginTime) || upstreamCalls.Load() != 1 {
		t.Fatalf("cached polls must not create another authentication: last=%v first=%v upstream=%d", stored.LastLoginAt, firstLoginTime, upstreamCalls.Load())
	}
	var logs []entity.Log
	if err := db.Where("module = ? AND action = ?", "auth", 10).Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].UserID != user.ID || !strings.Contains(logs[0].OldAnswer, `"scene":"wechat"`) {
		t.Fatalf("expected a single attributed WeChat login audit: %+v", logs)
	}
	for _, secret := range []string{user.Password, input.LoginToken, first.JWT, wechatID} {
		if strings.Contains(logs[0].Description+logs[0].OldAnswer+logs[0].NewAnswer, secret) {
			t.Fatalf("WeChat authentication secret %q leaked into audit", secret)
		}
	}
}

func TestWechatConcurrentSuccessfulPollingAuditsOnlyOneLogin(t *testing.T) {
	db := newWechatLoginTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	wechatID := "parallel-openid"
	user := entity.User{Username: "parallel-wechat", Password: "private-password", Status: "enabled", WechatID: &wechatID}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"success":true,"data":{"status":"success","wechat_user":{"openid":%q}}}`, wechatID)
	}))
	t.Cleanup(server.Close)
	store := NewWechatLoginSessionStore()
	store.Create("concurrent-login-token", time.Now(), time.Now().Add(time.Minute))
	input := WechatLoginStatusInput{Client: WechatLoginClient{BaseURL: server.URL, Client: server.Client()}, SessionStore: store, LoginToken: "concurrent-login-token"}
	var group sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := CheckWechatLoginStatusWithDB(db, input)
			errors <- err
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&entity.Log{}).Where("module = ? AND action = ?", "auth", 10).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("concurrent polls duplicated login audit: count=%d err=%v", count, err)
	}
}

func TestWechatLoginAuditFailureDoesNotFailAuthentication(t *testing.T) {
	db := newWechatLoginTestDB(t)
	user := entity.User{Username: "audit-unavailable", Password: "private-password", Status: "enabled"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	jwt, err := issueWechatJWTWithDB(db, &user, "127.0.0.1")
	if err != nil || jwt == "" || user.LastLoginAt == nil {
		t.Fatalf("audit failure must not undo completed authentication: jwt=%t err=%v", jwt != "", err)
	}
}

func TestWechatFailedLoginMetadataUpdateDoesNotAuditSuccess(t *testing.T) {
	db := newWechatLoginTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	user := entity.User{Username: "update-failure", Password: "private-password", Status: "enabled"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("fail_login_update", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameUser {
			tx.AddError(fmt.Errorf("test update failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := issueWechatJWTWithDB(db, &user, "127.0.0.1"); err == nil {
		t.Fatal("failed metadata update must fail authentication")
	}
	var count int64
	if err := db.Model(&entity.Log{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed login must not produce success audit: count=%d err=%v", count, err)
	}
}
