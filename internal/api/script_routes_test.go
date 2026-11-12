package api

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSetAPIRouterRegistersPublicScriptExchangeRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)
	want := map[string]bool{
		"GET /adapter-service/script-command/:shortCode/exchange-script": false,
		"GET /adapter-service/script-command/:shortCode/file":            false,
	}
	for _, route := range router.Routes() {
		if _, ok := want[route.Method+" "+route.Path]; ok {
			want[route.Method+" "+route.Path] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Fatalf("expected public script route %s", route)
		}
	}
}
