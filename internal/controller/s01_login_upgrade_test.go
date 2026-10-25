package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginUpgradeSignsOnlyAfterByteCAS(t *testing.T) {
	restoreS01CookieLoginGlobals(t)
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"bcrypt", "legacy", "hash-error-UTF8-bytes", "DB-error", "concurrent-CAS-zero"} {
		t.Run(kind, func(t *testing.T) {
			db := newUserControllerTestDB(t)
			closeS01CookieLoginDB(t, db)
			if err := db.AutoMigrate(&entity.CaptchaChallenge{}, &entity.CaptchaToken{}); err != nil {
				t.Fatal(err)
			}
			password := "Login-Fixture-123!"
			if kind == "hash-error-UTF8-bytes" {
				password = strings.Repeat("界", 25)
			}
			stored := password
			if kind == "bcrypt" {
				var err error
				stored, err = util.HashPassword(password)
				if err != nil {
					t.Fatal(err)
				}
			}
			user := entity.User{ID: 801, Username: "login-" + kind, Password: stored, Role: "user", Status: "enabled", RegisterSource: "system"}
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			concurrent := "Concurrent-Password-Changed"
			if kind == "DB-error" || kind == "concurrent-CAS-zero" {
				if err := db.Callback().Update().Before("gorm:update").Register("s01-password-write", func(tx *gorm.DB) {
					values, ok := tx.Statement.Dest.(map[string]interface{})
					if !ok {
						return
					}
					if _, ok := values["password"]; !ok {
						return
					}
					if kind == "DB-error" {
						tx.AddError(errors.New("S01-SYNTHETIC-PASSWORD-WRITE-FAILURE"))
						return
					}
					if err := tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE users SET password = ? WHERE id = ?", concurrent, user.ID).Error; err != nil {
						tx.AddError(err)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			answer := "AB12CD"
			sum := sha256.Sum256([]byte(answer))
			challenge := entity.CaptchaChallenge{ChallengeID: "s01-" + kind, Scene: service.CaptchaSceneLoginPassword, AnswerHash: hex.EncodeToString(sum[:]), Status: service.CaptchaChallengeStatusPending, RequestIP: "127.0.0.1", ExpiresAt: time.Now().Add(time.Minute)}
			if err := db.Create(&challenge).Error; err != nil {
				t.Fatal(err)
			}
			captcha, err := service.VerifyCaptchaChallengeWithDB(db, challenge.ChallengeID, service.CaptchaSceneLoginPassword, answer, "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			_ = router.SetTrustedProxies(nil)
			router.POST("/login", UserLogin)
			router.GET("/auth", Auth, UserInfo)
			payload, _ := json.Marshal(map[string]string{"username": user.Username, "password": password, "captchaToken": captcha.Token})
			req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "127.0.0.1:1234"
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			var body struct {
				JWT string `json:"jwt"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			t.Logf("loginCase=%s status=%d tokenPresent=%t cookies=%d", kind, rec.Code, body.JWT != "", len(rec.Header().Values("Set-Cookie")))
			var current entity.User
			if err := db.Where("id = ?", user.ID).First(&current).Error; err != nil {
				t.Fatal(err)
			}
			if kind == "bcrypt" || kind == "legacy" {
				if rec.Code != 200 || body.JWT == "" {
					t.Fatalf("successful login: %d %s", rec.Code, rec.Body.String())
				}
				next := httptest.NewRequest(http.MethodGet, "/auth", nil)
				next.Header.Set("Authorization", body.JWT)
				auth := httptest.NewRecorder()
				router.ServeHTTP(auth, next)
				t.Logf("loginCase=%s followupAuth=%d", kind, auth.Code)
				if auth.Code != 200 {
					t.Errorf("login200 -> Auth%d: %s", auth.Code, auth.Body.String())
				}
				return
			}
			if rec.Code != 400 || body.JWT != "" || len(rec.Header().Values("Set-Cookie")) != 0 {
				t.Errorf("failed upgrade must not issue JWT/Cookie: status=%d tokenPresent=%t cookies=%d", rec.Code, body.JWT != "", len(rec.Header().Values("Set-Cookie")))
			}
			if kind == "concurrent-CAS-zero" {
				if current.Password != concurrent {
					t.Errorf("concurrent password overwritten")
				}
			} else if current.Password != stored {
				t.Errorf("failed upgrade changed stored password")
			}
		})
	}
}

func closeS01CookieLoginDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close synthetic SQL: %v", err)
		}
	})
}
