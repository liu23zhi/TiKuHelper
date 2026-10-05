package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newWechatLoginTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:wechat-login-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func tinyPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 20, G: 120, B: 80, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestCreateWechatLoginQRCodeWithDBDownloadsQRCodeAndSendsBearerToken(t *testing.T) {
	db := newWechatLoginTestDB(t)
	qrBytes := tinyPNGBytes(t)
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/wechat/create_login_qrcode":
			authHeader = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"scene_id":       "login_scene_1",
					"qrcode_url":     "http://" + r.Host + "/qr.png",
					"login_token":    "login-token-1",
					"expire_seconds": 600,
				},
			})
		case "/qr.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(qrBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := CreateWechatLoginQRCodeWithDB(db, WechatLoginClient{
		BaseURL: server.URL,
		Token:   "bearer-token",
		Client:  server.Client(),
	})
	if err != nil {
		t.Fatalf("create qrcode: %v", err)
	}
	if authHeader != "Bearer bearer-token" {
		t.Fatalf("expected bearer token header, got %s", authHeader)
	}
	if result.LoginToken != "login-token-1" || result.SceneID != "login_scene_1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.HasPrefix(result.QRCodeImageData, "data:image/png;base64,") {
		t.Fatalf("expected qrcode image data, got %s", result.QRCodeImageData)
	}
	if result.ExpireSeconds != 180 {
		t.Fatalf("expected expire seconds 180, got %d", result.ExpireSeconds)
	}
}

func TestCreateWechatLoginQRCodeCapsTTLAt180SecondsAndStoresSession(t *testing.T) {
	db := newWechatLoginTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/wechat/create_login_qrcode":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": map[string]any{
					"scene_id":       "scene-ttl",
					"qrcode_url":     "http://" + r.Host + "/qr.png",
					"login_token":    "login-token-ttl",
					"expire_seconds": 600,
				},
			})
		case "/qr.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNGBytes(t))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := NewWechatLoginSessionStore()
	result, err := CreateWechatLoginQRCodeWithStore(db, WechatLoginClient{
		BaseURL: server.URL,
		Token:   "token",
		Client:  server.Client(),
	}, store)
	if err != nil {
		t.Fatalf("create qrcode: %v", err)
	}
	if result.ExpireSeconds != 180 {
		t.Fatalf("expected 180 seconds, got %d", result.ExpireSeconds)
	}
	if result.ExpiresAtUnix <= time.Now().UnixMilli() {
		t.Fatalf("expected future expiry, got %d", result.ExpiresAtUnix)
	}
	if _, err := store.Resolve(result.LoginToken, time.Now()); err != nil {
		t.Fatalf("expected stored qr session: %v", err)
	}
}

func TestCheckWechatLoginStatusRejectsExpiredQRSession(t *testing.T) {
	db := newWechatLoginTestDB(t)
	store := NewWechatLoginSessionStore()
	store.Create("expired-token", time.Now().Add(-time.Minute), time.Now().Add(-time.Second))

	_, err := CheckWechatLoginStatusWithDB(db, WechatLoginStatusInput{
		Client:       WechatLoginClient{BaseURL: "https://wechat.example.com", Token: "token"},
		SessionStore: store,
		LoginToken:   "expired-token",
		RequestIP:    "127.0.0.1",
	})
	if err == nil || !strings.Contains(err.Error(), "二维码已过期") {
		t.Fatalf("expected expired qr error, got %v", err)
	}
}

func TestCheckWechatLoginStatusWithDBReturnsJWTForBoundUser(t *testing.T) {
	db := newWechatLoginTestDB(t)
	wechatID := "openid-bound"
	if err := db.Create(&entity.User{
		ID:             10,
		Username:       "bound-user",
		Password:       "Abc12345!",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
		WechatID:       &wechatID,
	}).Error; err != nil {
		t.Fatalf("create bound user: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/wechat/login_status" || r.URL.Query().Get("login_token") != "login-token-1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"status": "success",
				"wechat_user": map[string]any{
					"openid": wechatID,
				},
			},
		})
	}))
	defer server.Close()

	sessionStore := NewWechatLoginSessionStore()
	sessionStore.Create("login-token-1", time.Now(), time.Now().Add(time.Minute))
	result, err := CheckWechatLoginStatusWithDB(db, WechatLoginStatusInput{
		Client:       WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()},
		SessionStore: sessionStore,
		LoginToken:   "login-token-1",
		RequestIP:    "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("check status: %v", err)
	}
	if result.Status != WechatLoginStatusSuccess {
		t.Fatalf("expected success status, got %+v", result)
	}
	if strings.TrimSpace(result.JWT) == "" {
		t.Fatal("expected jwt")
	}

	var stored entity.User
	if err := db.Where("id = ?", 10).First(&stored).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.LastLoginAt == nil || stored.LastLoginIP != "127.0.0.1" {
		t.Fatalf("expected login metadata, got %+v", stored)
	}
}

func TestCheckWechatLoginStatusWithDBReturnsBindTokenForUnboundUser(t *testing.T) {
	db := newWechatLoginTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"status": "success",
				"wechat_user": map[string]any{
					"openid": "openid-new",
				},
			},
		})
	}))
	defer server.Close()

	sessionStore := NewWechatLoginSessionStore()
	sessionStore.Create("login-token-2", time.Now(), time.Now().Add(time.Minute))
	result, err := CheckWechatLoginStatusWithDB(db, WechatLoginStatusInput{
		Client:       WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()},
		SessionStore: sessionStore,
		LoginToken:   "login-token-2",
		RequestIP:    "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("check status: %v", err)
	}
	if result.Status != WechatLoginStatusNeedBind {
		t.Fatalf("expected need_bind status, got %+v", result)
	}
	if strings.TrimSpace(result.BindToken) == "" {
		t.Fatal("expected bind token")
	}
}

func TestLoginWechatByCodeWithDBReturnsJWTForBoundUser(t *testing.T) {
	db := newWechatLoginTestDB(t)
	wechatID := "openid-code-bound"
	if err := db.Create(&entity.User{
		ID:             30,
		Username:       "wechat-code-user",
		Password:       "Abc12345!",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
		WechatID:       &wechatID,
	}).Error; err != nil {
		t.Fatalf("create bound user: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/wechat/user" || r.URL.Query().Get("code") != "104014" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    wechatID,
		})
	}))
	defer server.Close()

	result, err := LoginWechatByCodeWithDB(db, WechatCodeLoginInput{
		Client:    WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()},
		Code:      "104014",
		RequestIP: "127.0.0.3",
	})
	if err != nil {
		t.Fatalf("login by code: %v", err)
	}
	if result.Status != WechatLoginStatusSuccess || strings.TrimSpace(result.JWT) == "" {
		t.Fatalf("expected success jwt result, got %+v", result)
	}

	var stored entity.User
	if err := db.Where("id = ?", 30).First(&stored).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.LastLoginAt == nil || stored.LastLoginIP != "127.0.0.3" {
		t.Fatalf("expected login metadata, got %+v", stored)
	}
}

func TestLoginWechatByCodeWithDBReturnsBindTokenForUnboundUser(t *testing.T) {
	db := newWechatLoginTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    "openid-code-new",
		})
	}))
	defer server.Close()

	store := NewWechatBindTokenStore()
	result, err := LoginWechatByCodeWithDB(db, WechatCodeLoginInput{
		Client:    WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()},
		Store:     store,
		Code:      "104014",
		RequestIP: "127.0.0.3",
	})
	if err != nil {
		t.Fatalf("login by code: %v", err)
	}
	if result.Status != WechatLoginStatusNeedBind || strings.TrimSpace(result.BindToken) == "" {
		t.Fatalf("expected need_bind bind token, got %+v", result)
	}
	wechatID, err := store.Resolve(result.BindToken, time.Now())
	if err != nil {
		t.Fatalf("resolve bind token: %v", err)
	}
	if wechatID != "openid-code-new" {
		t.Fatalf("expected bind token to resolve openid-code-new, got %s", wechatID)
	}
}

func TestLoginWechatByCodeWithDBRejectsEmptyWechatID(t *testing.T) {
	db := newWechatLoginTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    "",
		})
	}))
	defer server.Close()

	_, err := LoginWechatByCodeWithDB(db, WechatCodeLoginInput{
		Client: WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()},
		Code:   "bad-code",
	})
	if err == nil || !strings.Contains(err.Error(), "微信验证码无效") {
		t.Fatalf("expected invalid code error, got %v", err)
	}
}

func TestBindWechatLoginWithDBBindsExistingAccountAndConsumesToken(t *testing.T) {
	db := newWechatLoginTestDB(t)
	if err := db.Create(&entity.User{
		ID:             20,
		Username:       "existing",
		Password:       "Abc12345!",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	store := NewWechatBindTokenStore()
	bindToken := store.Create("openid-bind", time.Now().Add(5*time.Minute))

	result, err := BindWechatLoginWithDB(db, WechatBindInput{
		Store:     store,
		BindToken: bindToken,
		Username:  "existing",
		Password:  "Abc12345!",
		RequestIP: "127.0.0.2",
	})
	if err != nil {
		t.Fatalf("bind wechat: %v", err)
	}
	if strings.TrimSpace(result.JWT) == "" {
		t.Fatal("expected jwt")
	}

	var stored entity.User
	if err := db.Where("id = ?", 20).First(&stored).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.WechatID == nil || *stored.WechatID != "openid-bind" {
		t.Fatalf("expected wechat id bound, got %+v", stored.WechatID)
	}
	if stored.WechatBoundAt == nil {
		t.Fatal("expected wechat bound at")
	}
	if _, err := BindWechatLoginWithDB(db, WechatBindInput{
		Store:     store,
		BindToken: bindToken,
		Username:  "existing",
		Password:  "Abc12345!",
	}); err == nil {
		t.Fatal("expected consumed bind token reuse to fail")
	}
}

func TestBindWechatToUserWithDBBindsCurrentUserAndConsumesToken(t *testing.T) {
	db := newWechatLoginTestDB(t)
	if err := db.Create(&entity.User{
		ID:             41,
		Username:       "profile-user",
		Password:       "Abc12345!",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	store := NewWechatBindTokenStore()
	bindToken := store.Create("openid-profile", time.Now().Add(5*time.Minute))

	updated, err := BindWechatToUserWithDB(db, store, bindToken, 41)
	if err != nil {
		t.Fatalf("bind wechat to current user: %v", err)
	}
	if updated.WechatID == nil || *updated.WechatID != "openid-profile" || updated.WechatBoundAt == nil {
		t.Fatalf("expected current user wechat binding, got %+v", updated)
	}
	if _, err := BindWechatToUserWithDB(db, store, bindToken, 41); err == nil {
		t.Fatal("expected consumed bind token reuse to fail")
	}
}

func TestBindWechatCodeToUserWithDBBindsCurrentUser(t *testing.T) {
	db := newWechatLoginTestDB(t)
	if err := db.Create(&entity.User{
		ID:             43,
		Username:       "profile-code-user",
		Password:       "Abc12345!",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/wechat/user" || r.URL.Query().Get("code") != "104015" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    "openid-profile-code",
		})
	}))
	defer server.Close()

	updated, err := BindWechatCodeToUserWithDB(
		db,
		WechatLoginClient{BaseURL: server.URL, Token: "token", Client: server.Client()},
		"104015",
		43,
	)
	if err != nil {
		t.Fatalf("bind wechat by code to current user: %v", err)
	}
	if updated.WechatID == nil || *updated.WechatID != "openid-profile-code" || updated.WechatBoundAt == nil {
		t.Fatalf("expected current user wechat binding, got %+v", updated)
	}
}

func TestUnbindWechatFromUserWithDBClearsBinding(t *testing.T) {
	db := newWechatLoginTestDB(t)
	wechatID := "openid-unbind"
	now := time.Now()
	if err := db.Create(&entity.User{
		ID:             42,
		Username:       "unbind-user",
		Password:       "Abc12345!",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
		WechatID:       &wechatID,
		WechatBoundAt:  &now,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	updated, err := UnbindWechatFromUserWithDB(db, 42)
	if err != nil {
		t.Fatalf("unbind wechat: %v", err)
	}
	if updated.WechatID != nil || updated.WechatBoundAt != nil {
		t.Fatalf("expected cleared wechat binding, got %+v", updated)
	}
}
