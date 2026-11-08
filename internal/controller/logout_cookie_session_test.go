package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestSingleSessionLogoutPreservesCookieOriginBoundary(t *testing.T) {
	restoreS01CookieLoginGlobals(t)
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, origin, site, header string
		cookie                     bool
		status                     int
		revoke                     bool
	}{
		{name: "same-origin", origin: "http://service.fixture.invalid", cookie: true, status: 200, revoke: true},
		{name: "legacy-metadata-absent", cookie: true, status: 200, revoke: true},
		{name: "same-site-other-origin", origin: "http://other.fixture.invalid", site: "same-site", cookie: true, status: 403},
		{name: "same-site-without-origin", site: "same-site", cookie: true, status: 403},
		{name: "cross-site", site: "cross-site", cookie: true, status: 403},
		{name: "null-origin", origin: "null", cookie: true, status: 403},
		{name: "explicit-header-priority", origin: "http://other.fixture.invalid", site: "cross-site", header: "valid", cookie: true, status: 200, revoke: true},
		{name: "bad-header-no-cookie-fallback", origin: "http://other.fixture.invalid", header: "invalid", cookie: true, status: 200},
		{name: "anonymous-clear", origin: "http://other.fixture.invalid", site: "cross-site", status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newUserControllerTestDB(t)
			closeS01CookieLoginDB(t, db)
			user := entity.User{Username: tc.name, Password: "synthetic-password", Status: "enabled"}
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			token, err := service.IssueUserLoginJWTWithDB(db, &user, "127.0.0.1", "")
			if err != nil {
				t.Fatal(err)
			}
			previousSID := user.LoginSessionID
			updates := 0
			if err := db.Callback().Update().Before("gorm:update").Register("logout_update_count", func(*gorm.DB) { updates++ }); err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.POST("/logout", UserLogout)
			req := httptest.NewRequest(http.MethodPost, "http://service.fixture.invalid/logout", nil)
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
			}
			if tc.header == "valid" {
				req.Header.Set("Authorization", token)
			} else if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				req.Header.Set("Sec-Fetch-Site", tc.site)
			}
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Errorf("logout HTTP=%d want=%d", out.Code, tc.status)
			}
			var stored entity.User
			if err := db.First(&stored, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			changed := stored.LoginSessionID != previousSID
			if changed != tc.revoke {
				t.Errorf("server session revoked=%t want=%t", changed, tc.revoke)
			}
			var logs int64
			if err := db.Model(&entity.Log{}).Count(&logs).Error; err != nil {
				t.Fatal(err)
			}
			if tc.revoke {
				if updates != 1 || logs != 1 {
					t.Errorf("successful logout updates=%d audit=%d", updates, logs)
				}
			} else if updates != 0 || logs != 0 {
				t.Errorf("rejected/anonymous logout wrote: updates=%d audit=%d", updates, logs)
			}
			cookies := out.Result().Cookies()
			if tc.status == 403 && len(cookies) != 0 {
				t.Error("rejected cross-origin request changed client cookie")
			}
			if tc.status == 200 {
				cleared := false
				for _, c := range cookies {
					if c.Name == authCookieName && c.MaxAge < 0 {
						cleared = true
					}
				}
				if !cleared {
					t.Error("public logout did not clear client cookie")
				}
			}
		})
	}
}
