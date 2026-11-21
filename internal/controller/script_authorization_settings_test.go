package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestScriptAuthorizationSettingsHTTPAllowsUsersToReadAndAdminsToUpdate(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "script-policy-user", "user")
	admin := seedControllerUser(t, db, 0, "script-policy-admin", "admin")
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	var actor *entity.User
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if actor != nil {
			c.Set("user", actor)
		}
	})
	router.GET("/api/script-domain-formats", ScriptDomainFormats)
	router.GET("/api/script-domain-settings", ScriptDomainSettings)
	router.PUT("/api/script-domain-settings", UpdateScriptDomainSettings)
	call := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s = %d, want %d (%s)", method, path, res.Code, status, res.Body.String())
		}
		return res
	}
	assertPolicy := func(response *httptest.ResponseRecorder, full, short bool) {
		t.Helper()
		var actual struct {
			AllowFullNeverExpire  *bool `json:"allowFullNeverExpire"`
			AllowShortNeverExpire *bool `json:"allowShortNeverExpire"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
			t.Fatal(err)
		}
		if actual.AllowFullNeverExpire == nil || *actual.AllowFullNeverExpire != full || actual.AllowShortNeverExpire == nil || *actual.AllowShortNeverExpire != short {
			t.Fatalf("wrong policy response: %s", response.Body.String())
		}
	}
	const settings = "/api/script-domain-settings"
	const formats = "/api/script-domain-formats"
	call(http.MethodGet, formats, "", http.StatusUnauthorized)
	actor = &user
	assertPolicy(call(http.MethodGet, formats, "", http.StatusOK), true, false)
	call(http.MethodGet, settings, "", http.StatusForbidden)
	call(http.MethodPut, settings, `{"allowFullNeverExpire":false}`, http.StatusForbidden)
	actor = &admin
	assertPolicy(call(http.MethodGet, settings, "", http.StatusOK), true, false)
	assertPolicy(call(http.MethodPut, settings, `{"allowShortNeverExpire":true}`, http.StatusOK), true, true)
	assertPolicy(call(http.MethodPut, settings, `{"allowFullNeverExpire":false}`, http.StatusOK), false, true)
	actor = &user
	assertPolicy(call(http.MethodGet, formats, "", http.StatusOK), false, true)
}
