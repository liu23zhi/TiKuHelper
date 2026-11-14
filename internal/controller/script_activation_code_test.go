package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
)

// newScriptActivationCodeRouter 组装与 router.go 同构的管理员路由（含中间件顺序）。
func newScriptActivationCodeRouter(user *entity.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/admin/script-activation-codes")
	group.Use(stubUser(user), middleware.RequireAdmin)
	group.POST("", ScriptActivationCodeCreate)
	group.GET("", ScriptActivationCodeList)
	group.GET("/:id", ScriptActivationCodeDetail)
	group.PUT("/:id", ScriptActivationCodeUpdate)
	return router
}

// 普通用户与匿名访问四个管理端点必须 403。
//
// 这是「仅管理员可见」的行为级断言。注意它**不足以**证明 router.go 挂对了
// 中间件（handler 内还有一次 requireAdmin），所以另有
// TestScriptActivationCodeRoutesMountRequireAdmin 读源码兜底。
func TestScriptActivationCodeEndpointsRejectNonAdmin(t *testing.T) {
	for _, user := range []*entity.User{ordinaryUser(), nil} {
		router := newScriptActivationCodeRouter(user)
		cases := []struct {
			method, path string
			body         string
		}{
			{http.MethodPost, "/api/admin/script-activation-codes", `{"count":1}`},
			{http.MethodGet, "/api/admin/script-activation-codes", ""},
			{http.MethodGet, "/api/admin/script-activation-codes/1", ""},
			{http.MethodPut, "/api/admin/script-activation-codes/1", `{"remark":"x"}`},
		}
		for _, testCase := range cases {
			// 用 io.Reader 而不是 *bytes.Buffer：把 nil 的 *bytes.Buffer 装进
			// interface{} 后它**不是** nil（类型化 nil），httptest 会在里面
			// 调 Len() 直接 panic。
			var body io.Reader
			if testCase.body != "" {
				body = bytes.NewBufferString(testCase.body)
			}
			request := httptest.NewRequest(testCase.method, testCase.path, body)
			if testCase.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("%s %s: 非管理员必须 403，实际 %d", testCase.method, testCase.path, response.Code)
			}
		}
	}
}

// 批量上限：超过上限必须是 400（而不是 500，也不是静默截断）。
func TestScriptActivationCodeCreateRejectsOverBatchLimit(t *testing.T) {
	router := newScriptActivationCodeRouter(adminUser())
	// count 超过 service.MaxAdminScriptActivationCodeBatch（100）。
	body := bytes.NewBufferString(`{"count":101,"remark":"too many"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/script-activation-codes", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("超过批量上限必须 400，实际 %d: %s", response.Code, response.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 文案里必须带上上限值：调用方需要知道边界是多少，否则只能靠猜。
	if payload["message"] == "" || !bytes.Contains([]byte(payload["message"]), []byte("100")) {
		t.Fatalf("拒绝文案应包含上限 100，实际 %q", payload["message"])
	}
}

// 未登录（无 token）访问必须 401，而不是 403。
func TestScriptActivationCodeEndpointsRejectUnauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/admin/script-activation-codes")
	group.Use(Auth, middleware.RequireAdmin)
	group.POST("", ScriptActivationCodeCreate)
	group.GET("", ScriptActivationCodeList)
	group.GET("/:id", ScriptActivationCodeDetail)
	group.PUT("/:id", ScriptActivationCodeUpdate)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/admin/script-activation-codes"},
		{http.MethodGet, "/api/admin/script-activation-codes"},
		{http.MethodGet, "/api/admin/script-activation-codes/1"},
		{http.MethodPut, "/api/admin/script-activation-codes/1"},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(testCase.method, testCase.path, bytes.NewBufferString(""))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: 未登录必须 401，实际 %d", testCase.method, testCase.path, response.Code)
		}
	}
}

// 非法 ID 必须是 400（而不是拿 0 去查库再 500）。
func TestScriptActivationCodeDetailRejectsInvalidID(t *testing.T) {
	router := newScriptActivationCodeRouter(adminUser())
	for _, path := range []string{
		"/api/admin/script-activation-codes/0",
		"/api/admin/script-activation-codes/abc",
		"/api/admin/script-activation-codes/-1",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		// 0/abc/-1 都应被 ID 校验挡在 400 或路由层 404，不能 500。
		if response.Code == http.StatusInternalServerError {
			t.Fatalf("%s: 非法 ID 不能 500，实际 %d", path, response.Code)
		}
	}
}
