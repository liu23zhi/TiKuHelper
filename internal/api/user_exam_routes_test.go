package api

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// 考试客户端本地库工具的三个接口必须注册在 /adapter-service/admin/userexam 下。
//
// 这里只断言路由存在：真正发请求会经过 GlobalAPIRateLimit，而它依赖
// manager 单例；权限行为由 internal/controller 的用例覆盖（非管理员 403）。
func TestSetAPIRouterRegistersUserExamToolRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	SetAPIRouter(router)

	want := map[string]bool{
		"GET /adapter-service/admin/userexam/tool/version": false,
		"GET /adapter-service/admin/userexam/tool":         false,
		"POST /adapter-service/admin/userexam/inspect":     false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Fatalf("expected route %s to be registered", route)
		}
	}
}
