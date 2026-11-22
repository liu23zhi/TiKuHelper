package api

import (
	"embed"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestLateRouterRegistrationRefreshesScriptReservedPaths(t *testing.T) {
	before := service.ScriptReservedPrefixes()
	t.Cleanup(func() { service.SetReservedScriptPrefixes(before) })
	router := gin.New()
	router.GET("/earlier-api/state", func(c *gin.Context) { c.Status(200) })
	service.SetReservedScriptPrefixes([]string{"earlier-api"})
	router.GET("/late-api/state", func(c *gin.Context) { c.Status(200) })
	SetHealthRouter(router, nil)
	if !slices.Contains(service.ScriptReservedPrefixes(), "late-api") {
		t.Fatal("health registration left a stale API prefix snapshot")
	}
	router.GET("/last-api/state", func(c *gin.Context) { c.Status(200) })
	SetWebRouter(embed.FS{}, []byte("fixture"), router)
	if !slices.Contains(service.ScriptReservedPrefixes(), "last-api") {
		t.Fatal("web registration left a stale API prefix snapshot")
	}
}
