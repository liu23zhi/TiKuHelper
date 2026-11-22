package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestScriptDomainDeniedErrorsHidePolicyFromAnonymousRequests(t *testing.T) {
	db := newSearchControllerTestDB(t)
	if _, err := service.UpdateScriptDomainSettingsWithDB(db, service.ScriptDomainSettings{DeniedDomains: []string{"*.internal.a.com"}}); err != nil {
		t.Fatal(err)
	}
	denied := service.ValidateScriptDomainWithDB(db, "secret.internal.a.com")
	if denied == nil {
		t.Fatal("fixture was not denied")
	}
	for _, authenticated := range []bool{false, true} {
		router := gin.New()
		router.GET("/", func(c *gin.Context) {
			if authenticated {
				c.Set("user", &entity.User{ID: 1})
			}
			writeScriptError(c, denied, "脚本入口暂不可用")
		})
		res := httptest.NewRecorder()
		router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
		if authenticated {
			if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "*.internal.a.com") {
				t.Fatalf("authenticated error lacks deny reason: %d %s", res.Code, res.Body.String())
			}
		} else if res.Code != http.StatusNotFound || strings.Contains(res.Body.String(), "internal.a.com") || strings.Contains(res.Body.String(), "禁止") {
			t.Fatalf("anonymous error exposed deny rules: %d %s", res.Code, res.Body.String())
		}
	}
}

func TestScriptDomainDeniedSettingsHTTPPreservesOldPayloadAndDoesNotBlockOtherRoutes(t *testing.T) {
	db := newSearchControllerTestDB(t)
	admin := seedControllerUser(t, db, 0, "deny-http-admin", "admin")
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	router := gin.New()
	router.Use(ScriptPublicHostMiddleware)
	router.Use(func(c *gin.Context) { c.Set("user", &admin) })
	router.PUT("/api/script-domain-settings", UpdateScriptDomainSettings)
	router.GET("/api/script-domain-settings", ScriptDomainSettings)
	router.GET("/api/script-domain-formats", ScriptDomainFormats)
	router.GET("/health", func(c *gin.Context) { c.String(http.StatusOK, "healthy") })
	call := func(method, path, payload string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(payload))
		req.Host = "admin.a.com"
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s = %d: %s", method, path, res.Code, res.Body.String())
		}
		return res.Body.String()
	}
	call(http.MethodPut, "/api/script-domain-settings", `{"domains":["*.a.com"],"deniedDomains":["admin.a.com"]}`, http.StatusOK)
	call(http.MethodPut, "/api/script-domain-settings", `{"allowFullNeverExpire":false}`, http.StatusOK)
	if body := call(http.MethodGet, "/api/script-domain-settings", "", http.StatusOK); !strings.Contains(body, `"deniedDomains":["admin.a.com"]`) {
		t.Fatalf("old HTTP payload erased deny list: %s", body)
	}
	if body := call(http.MethodGet, "/api/script-domain-formats", "", http.StatusOK); strings.Contains(body, "deniedDomains") || strings.Contains(body, "admin.a.com") {
		t.Fatalf("user formats exposed administrator deny policy: %s", body)
	}
	call(http.MethodGet, "/health", "", http.StatusOK)
	call(http.MethodPut, "/api/script-domain-settings", `{"domains":["other.test/*"],"deniedDomains":["bad.a.com/path"]}`, http.StatusBadRequest)
	if body := call(http.MethodGet, "/api/script-domain-settings", "", http.StatusOK); !strings.Contains(body, `"domains":["*.a.com"]`) {
		t.Fatalf("invalid batch partially changed allow list: %s", body)
	}
}
