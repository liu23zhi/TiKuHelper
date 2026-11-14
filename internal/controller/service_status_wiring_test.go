package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

// 本文件钉住「分桶采样的接线层」（task_doc/138 §4.2 / 判据 S1 的接线面）。
//
// # 为什么这一层必须单独测
//
// 分类函数本身由 service 包覆盖，但真正会静默失效的是**接线**：
// 中间件传给 ClassifyEndpoint 的 route 到底是什么形态？
//
// gin 的 c.FullPath() 返回**含组前缀**的模板（/api/search/tasks），
// 不是裸模板（/search/tasks）。如果 ClassifyEndpoint 只按裸模板比较，
// 线上**一个都匹配不上**，全部落 other —— 而 service 包的单测会全绿
// （它们直接喂裸模板），controller 层也会全绿（它们直接构造 StatusPoint）。
// 这就是「实现层与接线层是两个作用域，只测前者会漏掉后者」的典型形态。
//
// 所以这里用真实 gin 引擎 + 真实路由注册 + 真实中间件跑一遍，断言 FullPath 的形态。

// TestGinFullPathIncludesGroupPrefix 钉 FullPath 的形态，作为 ClassifyEndpoint
// 「必须接受组前缀」这一设计的依据。
func TestGinFullPathIncludesGroupPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()

	var observed []string
	router := engine.Group("/api")
	router.POST("/search/tasks", func(c *gin.Context) {
		observed = append(observed, c.FullPath())
		c.Status(http.StatusOK)
	})
	router.POST("/ai-fallback", func(c *gin.Context) {
		observed = append(observed, c.FullPath())
		c.Status(http.StatusOK)
	})
	legacy := engine.Group("/adapter-service")
	legacy.POST("/search/tasks", func(c *gin.Context) {
		observed = append(observed, c.FullPath())
		c.Status(http.StatusOK)
	})

	for _, path := range []string{"/api/search/tasks", "/api/ai-fallback", "/adapter-service/search/tasks"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("前置失效：%s 期望 200，got=%d", path, w.Code)
		}
	}

	want := []string{"/api/search/tasks", "/api/ai-fallback", "/adapter-service/search/tasks"}
	if len(observed) != len(want) {
		t.Fatalf("观察到 %d 个 FullPath，期望 %d：%v", len(observed), len(want), observed)
	}
	for i, path := range want {
		// 关键断言：FullPath **带组前缀**。这条红了就说明 gin 行为变了，
		// ClassifyEndpoint 的前缀剥离逻辑必须重新核对。
		if observed[i] != path {
			t.Fatalf("第 %d 个 FullPath 期望 %q（含组前缀），got=%q", i, path, observed[i])
		}
		// 而这些 FullPath 必须真的被分类成 search —— 否则线上全部落 other。
		if got := service.ClassifyEndpoint(observed[i]); got != service.EndpointSearch {
			t.Fatalf("真实 FullPath %q 必须分类成 search，got=%q（接线断了）", observed[i], got)
		}
	}
}

// TestGinFullPathUnmatchedRouteIsNotEmpty 钉未匹配路由的 FullPath 形态。
//
// 中间件把空 FullPath 兜底成 "__unmatched__"（见 middleware/request_id.go），
// 而 ClassifyEndpoint 必须把它落 other —— 不能因为「空串」被当成某个桶。
func TestGinFullPathUnmatchedRouteIsNotEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()

	var observed []string
	engine.Use(func(c *gin.Context) {
		c.Next()
		observed = append(observed, c.FullPath())
	})
	engine.POST("/api/search/tasks", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 一个不存在的路径：gin 不会写 FullPath。
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/no-such-route", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("前置失效：未匹配路由期望 404，got=%d", w.Code)
	}
	if len(observed) != 1 {
		t.Fatalf("期望观察到 1 个 FullPath，got=%d", len(observed))
	}
	if observed[0] != "" {
		t.Fatalf("未匹配路由的 FullPath 期望空串，got=%q", observed[0])
	}
	// 空串必须落 other。
	if got := service.ClassifyEndpoint(observed[0]); got != service.EndpointOther {
		t.Fatalf("空 FullPath 必须分类成 other，got=%q", got)
	}
	// 中间件兜底值 "__unmatched__" 同样必须落 other。
	if got := service.ClassifyEndpoint("__unmatched__"); got != service.EndpointOther {
		t.Fatalf("__unmatched__ 必须分类成 other，got=%q", got)
	}
}

// TestObserveServiceStatusEndpointWiring 钉中间件调用的那条包级入口真的记账。
//
// 用真实记录器（不是 nil）以免测到退化路径；断言 search / other 各自独立。
func TestObserveServiceStatusEndpointWiring(t *testing.T) {
	recorder := &service.ServiceStatusEndpointRecorder{}

	// 模拟中间件的三次调用：两次搜题、一次其它。
	recorder.ObserveEndpoint("/api/search/tasks", 200, 42)
	recorder.ObserveEndpoint("/adapter-service/search/tasks", 201, 8)
	recorder.ObserveEndpoint("/api/user/login", 200, 15)

	snapshot := recorder.Snapshot()
	if snapshot.Search.TotalRequests != 2 {
		t.Fatalf("search 桶期望 2（两条真实搜题路由都带组前缀），got=%d", snapshot.Search.TotalRequests)
	}
	if snapshot.Other.TotalRequests != 1 {
		t.Fatalf("other 桶期望 1，got=%d", snapshot.Other.TotalRequests)
	}
	// 耗时按桶分开累加。
	if snapshot.Search.LatencyTotalMillis != 50 {
		t.Fatalf("search 桶耗时期望 50（42+8），got=%d", snapshot.Search.LatencyTotalMillis)
	}
	if snapshot.Other.LatencyTotalMillis != 15 {
		t.Fatalf("other 桶耗时期望 15，got=%d", snapshot.Other.LatencyTotalMillis)
	}
}
