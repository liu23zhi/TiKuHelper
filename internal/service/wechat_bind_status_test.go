package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestWechatBindingStatusDoesNotIssueLoginCredentials(t *testing.T) {
	for _, cachedLogin := range []bool{false, true} {
		name := "fresh-scan"
		if cachedLogin {
			name = "completed-login"
		}
		t.Run(name, func(t *testing.T) {
			db := newWechatLoginTestDB(t)
			wechatID := "bound-wechat"
			user := entity.User{Username: "bound-user", Password: "password-hash", Status: "enabled", WechatID: &wechatID, LoginSessionID: "active-session"}
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"status": "success", "wechat_user": map[string]any{"openid": wechatID}}})
			}))
			t.Cleanup(server.Close)
			sessions := NewWechatLoginSessionStore()
			sessions.Create("scan-token", time.Now(), time.Now().Add(time.Minute))
			if cachedLogin {
				_, err := sessions.completeLogin("scan-token", func() (WechatLoginStatusResult, error) {
					return WechatLoginStatusResult{Status: WechatLoginStatusSuccess, JWT: "cached-jwt"}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			result, err := CheckWechatLoginStatusWithDB(db, WechatLoginStatusInput{
				Client: WechatLoginClient{BaseURL: server.URL, Client: server.Client()}, SessionStore: sessions,
				LoginToken: "scan-token", BindOnly: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != WechatLoginStatusSuccess || result.JWT != "" || result.BindToken != "" {
				t.Fatalf("binding status returned login credentials: %+v", result)
			}
			var stored entity.User
			if err := db.First(&stored, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.LoginSessionID != user.LoginSessionID || stored.LastLoginAt != nil {
				t.Fatalf("binding status changed login state: %+v", stored)
			}
		})
	}
}

func TestWechatBindingStatusReturnsUsableTokenForUnboundWechat(t *testing.T) {
	db := newWechatLoginTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"status": "success", "wechat_user": map[string]any{"openid": "unbound-wechat"}}})
	}))
	t.Cleanup(server.Close)
	sessions := NewWechatLoginSessionStore()
	sessions.Create("scan-token", time.Now(), time.Now().Add(time.Minute))
	bindings := NewWechatBindTokenStore()
	result, err := CheckWechatLoginStatusWithDB(db, WechatLoginStatusInput{
		Client: WechatLoginClient{BaseURL: server.URL, Client: server.Client()}, SessionStore: sessions,
		Store: bindings, LoginToken: "scan-token", BindOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != WechatLoginStatusNeedBind || result.JWT != "" || result.BindToken == "" {
		t.Fatalf("binding status = %+v", result)
	}
	wechatID, err := bindings.Resolve(result.BindToken, time.Now())
	if err != nil || wechatID != "unbound-wechat" {
		t.Fatalf("binding token did not resolve: %q %v", wechatID, err)
	}
}
