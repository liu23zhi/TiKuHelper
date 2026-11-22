package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestScriptFormatsPublishesServerReservedPathsAndReadableError(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 0, "prefix-reader", "user")
	previous := scriptDB
	scriptDB = func() *gorm.DB { return db }
	t.Cleanup(func() { scriptDB = previous })
	prefixes := service.ScriptReservedPrefixes()
	service.SetReservedScriptPrefixes(append(prefixes, "another-api"))
	t.Cleanup(func() { service.SetReservedScriptPrefixes(prefixes) })
	router := gin.New()
	router.GET("/formats", func(c *gin.Context) { c.Set("user", &user) }, ScriptDomainFormats)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/formats", nil))
	var body struct {
		ReservedPrefixes []string `json:"reservedPrefixes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"api", "another-api", "healthz", "readyz", "index.html", "theme-init.js"} {
		if !slices.Contains(body.ReservedPrefixes, prefix) {
			t.Fatalf("server did not publish protected path %s: %s", prefix, response.Body)
		}
	}
	router.GET("/error", func(c *gin.Context) { writeScriptError(c, service.ErrScriptReservedPrefix, "fallback") })
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/error", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("reserved path should be a validation error: %d %s", response.Code, response.Body)
	}
}
