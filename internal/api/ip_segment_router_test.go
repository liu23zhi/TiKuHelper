package api

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// IP 段归属管理页（task_doc/71 §3.5）的路由注册。
//
// 断言两件事：
//  1. 五条路由真的挂在 /adapter-service/admin/ip-segments 族上（路径用**复数**，
//     与单 IP 的 ip-location-cache 不能混）；
//  2. 每条路由的末端 handler 是 IP 段那个函数，而不是被复制粘贴成别的管理接口。
//
// 中间件链**不在**这里断：gin v1.12 的 RouteInfo 只有 Method/Path/Handler/HandlerFunc，
// 不暴露每路由的完整链（没有 HandlersCount），所以「非管理员 403」只能由
// controller 侧的行为测试来钉（internal/controller/ip_segment_test.go）。
func TestSetAPIRouterRegistersIPSegmentAdminRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	want := map[string]string{
		"GET /adapter-service/admin/ip-segments":               "IPSegmentList",
		"GET /adapter-service/admin/ip-segments/lookup":        "IPSegmentLookup",
		"POST /adapter-service/admin/ip-segments/batch-delete": "IPSegmentBatchDelete",
		"DELETE /adapter-service/admin/ip-segments/:id":        "IPSegmentDelete",
		"POST /adapter-service/admin/ip-segments/:id/refresh":  "IPSegmentRefresh",
	}
	found := make(map[string]string, len(want))
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := want[key]; exists {
			found[key] = route.Handler
		}
	}

	for routeKey, handlerName := range want {
		actual, ok := found[routeKey]
		if !ok {
			t.Fatalf("missing IP segment admin route %s", routeKey)
		}
		if !strings.HasSuffix(actual, "."+handlerName) {
			t.Fatalf("%s must be wired to controller.%s, got handler %q", routeKey, handlerName, actual)
		}
	}

	// 反向钉子：不许出现单数路径（ip-segment）。复数不是风格问题 ——
	// 单数写法会和既有的 ip-location-cache 在侧边栏与日志排查时混淆，
	// 而且与 entity 表名 ip_segments 不对应。
	for _, route := range router.Routes() {
		if strings.Contains(route.Path, "/ip-segment") && !strings.Contains(route.Path, "/ip-segments") {
			t.Fatalf("IP segment routes must use the plural path, got %s %s", route.Method, route.Path)
		}
	}
}
