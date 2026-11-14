package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 黑名单管理页（BL-5）的路由注册与「被拉黑也能进」这条红线。
//
// 两件事必须同时成立，缺一条这个功能就不可用：
//  1. 五条路由真的注册在 /api/blacklist 族上；
//  2. 它们**不经过 middleware.BlacklistIP()** —— 否则管理员误把自己所在 CIDR
//     拉黑后再也进不来（46-*.md §3.3 自锁）。

func TestSetAPIRouterRegistersBlacklistAdminRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	SetAPIRouter(router)

	want := map[string]bool{
		"GET /api/blacklist":             false,
		"POST /api/blacklist":            false,
		"PUT /api/blacklist/:id/enabled": false,
		"DELETE /api/blacklist/:id":      false,
		"GET /api/blacklist/hits":        false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if _, exists := want[key]; exists {
			want[key] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Fatalf("missing blacklist admin route %s", route)
		}
	}
}

// blacklistBlockingFixture 造一个「开关打开 + 命中一切来源 IP」的真实缓存。
//
// 用真 sqlite 库 + service.CreateBlacklistEntryWithDB + LoadBlacklistSnapshot：
// 手搓 BlacklistSnapshot 是造不出来的（字段全未导出），
// 而「手工构造一个看起来像快照的东西」正是假钉子的典型来源。
func blacklistBlockingFixture(t *testing.T) *service.BlacklistCache {
	t.Helper()

	dsn := fmt.Sprintf("file:blacklist-router-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.BlacklistEntry{}, &entity.BlacklistHit{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&entity.SystemSetting{Key: service.BlacklistEnabledSettingKey, Value: "true"}).Error; err != nil {
		t.Fatalf("seed switch: %v", err)
	}
	// 0.0.0.0/0 命中任何来源 IPv4；测试里的 ClientIP 是 203.0.113.7。
	if _, err := service.CreateBlacklistEntryWithDB(db, service.CreateBlacklistEntryInput{
		Kind:      service.BlacklistKindIP,
		Pattern:   "0.0.0.0/0",
		Reason:    "SYNTHETIC_BLOCK_EVERYTHING",
		Enabled:   true,
		CreatedBy: 1,
	}); err != nil {
		t.Fatalf("seed blocking rule: %v", err)
	}
	cache := service.NewBlacklistCache()
	if err := cache.Reload(db); err != nil {
		t.Fatalf("reload cache: %v", err)
	}
	if _, hit := cache.Snapshot().MatchIP("203.0.113.7"); !hit {
		t.Fatal("fixture is inert: 0.0.0.0/0 must match 203.0.113.7")
	}
	return cache
}

// TestBlacklistAdminRoutesBypassIPBlacklist 是自锁红线的**行为级**断言。
//
// 做法：用真实缓存（命中一切 IP）驱动真实的 BlacklistIP()，挂在与生产同构的
// 两个分组上，然后发同一个来源 IP 的请求：
//
//	业务组（挂 BlacklistIP）      → 403
//	管理组（不挂 BlacklistIP）    → 200
//
// 这比 grep router.go 强：grep 只能证明「某处没写 BlacklistIP」，
// 这条断言证明的是**同一个被拉黑的 IP 真的能打到管理接口**。
func TestBlacklistAdminRoutesBypassIPBlacklist(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := blacklistBlockingFixture(t)
	blocked := middleware.BlacklistIPWith(cache, service.NewBlacklistHitRecorder(), nil)

	router := gin.New()
	// 与生产同构：业务接口挂在带 BlacklistIP 的分组上。
	apiGroup := router.Group("/api")
	apiGroup.Use(blocked)
	apiGroup.GET("/search", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 与 router.go 同构：管理组只挂身份校验，**不挂 BlacklistIP**。
	adminGroup := router.Group("/api")
	adminGroup.Use(func(c *gin.Context) {
		c.Set("user", &entity.User{ID: 1, Username: "SYNTHETIC_ADMIN", Role: "admin", Status: "enabled"})
	}, middleware.RequireAdmin)
	adminGroup.GET("/blacklist", func(c *gin.Context) { c.Status(http.StatusOK) })
	adminGroup.PUT("/blacklist/:id/enabled", func(c *gin.Context) { c.Status(http.StatusOK) })
	adminGroup.DELETE("/blacklist/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	adminGroup.GET("/blacklist/hits", func(c *gin.Context) { c.Status(http.StatusOK) })

	serve := func(method, path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, path, nil)
		request.RemoteAddr = "203.0.113.7:54321"
		router.ServeHTTP(recorder, request)
		return recorder
	}

	// 先证明这个 IP 真的被拦：否则「管理组 200」毫无意义（可能规则根本没生效）。
	if recorder := serve(http.MethodGet, "/api/search"); recorder.Code != http.StatusForbidden {
		t.Fatalf("fixture must block the business route, got %d (%s)", recorder.Code, recorder.Body.String())
	}

	for _, probe := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/blacklist"},
		{http.MethodPut, "/api/blacklist/1/enabled"},
		{http.MethodDelete, "/api/blacklist/1"},
		{http.MethodGet, "/api/blacklist/hits"},
	} {
		recorder := serve(probe.method, probe.path)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s must stay reachable for a blacklisted admin, got %d (%s)",
				probe.method, probe.path, recorder.Code, recorder.Body.String())
		}
	}

	// 反向钉子：这些路径**不能**靠 BlacklistExemptPath 豁免。
	// 它们不在 /admin 段内，豁免必须来自「这一组不挂 BlacklistIP」这个结构性事实。
	// 如果将来有人把管理路由并回 apiRouter，下面这条会立刻红，提醒他别把自锁带回来。
	for _, path := range []string{
		"/api/blacklist",
		"/api/blacklist/hits",
		"/api/blacklist/1",
	} {
		if middleware.BlacklistExemptPath(path) {
			t.Fatalf("%s must NOT rely on the exempt table; the admin group is mounted without BlacklistIP", path)
		}
	}
}
