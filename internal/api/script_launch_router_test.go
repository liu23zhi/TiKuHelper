package api

import (
	"net/http"
	"testing"
)

func TestSetAPIRouterRegistersScriptLaunchResolve(t *testing.T) {
	router := ginNewEngineForDeviceRoutes()
	SetAPIRouter(router)
	for _, route := range router.Routes() {
		if route.Method == http.MethodPost && route.Path == "/api/script-launch/resolve" {
			return
		}
	}
	t.Fatal("missing POST /api/script-launch/resolve")
}
