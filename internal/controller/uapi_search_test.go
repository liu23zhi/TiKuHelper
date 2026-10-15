package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"gorm.io/gorm"
)

func newUAPISearchTestDB(t *testing.T, key string) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:uapi-search-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if key != "" {
		if err := db.Create(&entity.SystemSetting{Key: "uapi_api_key", Value: key}).Error; err != nil {
			t.Fatal(err)
		}
	}
	dao.SetDefault(db)
	return db
}

func performUAPISearchRequest(t *testing.T, method string, target string, body string, role string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: role, Status: "enabled"})
	handler(ctx)
	return recorder
}

func TestUAPISearchControllersRejectNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPISearchTestDB(t, "uapi-test-key")
	if got := performUAPISearchRequest(t, http.MethodGet, "/adapter-service/uapi/search/engines", "", "user", UAPISearchEngines).Code; got != http.StatusForbidden {
		t.Fatalf("engines status = %d", got)
	}
	if got := performUAPISearchRequest(t, http.MethodPost, "/adapter-service/uapi/search/aggregate", `{"query":"q"}`, "user", UAPISearchAggregate).Code; got != http.StatusForbidden {
		t.Fatalf("aggregate status = %d", got)
	}
}

func TestUAPISearchAggregateControllerValidatesJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPISearchTestDB(t, "uapi-test-key")
	recorder := performUAPISearchRequest(t, http.MethodPost, "/adapter-service/uapi/search/aggregate", "not-json", "admin", UAPISearchAggregate)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUAPISearchControllerUsesConfiguredKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPISearchTestDB(t, "uapi-test-key")
	var gotKey string
	oldFactory := newUAPISearchClient
	newUAPISearchClient = func(key string) *uapi.Client {
		gotKey = key
		return uapi.NewClient(key).SetSearchAggregateURL("http://127.0.0.1:1/api/v1/search/aggregate")
	}
	t.Cleanup(func() { newUAPISearchClient = oldFactory })
	recorder := performUAPISearchRequest(t, http.MethodPost, "/adapter-service/uapi/search/aggregate", `{"query":"q"}`, "admin", UAPISearchAggregate)
	if recorder.Code != http.StatusBadGateway || gotKey != "uapi-test-key" {
		t.Fatalf("应读取配置密钥并将网络失败映射为 502，status=%d key=%q body=%s", recorder.Code, gotKey, recorder.Body.String())
	}
}

func TestUAPISearchControllersReportMissingTokenWithoutCallingUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPISearchTestDB(t, "")
	called := false
	oldFactory := newUAPISearchClient
	newUAPISearchClient = func(key string) *uapi.Client {
		called = true
		return uapi.NewClient(key)
	}
	t.Cleanup(func() { newUAPISearchClient = oldFactory })

	recorder := performUAPISearchRequest(t, http.MethodGet, "/adapter-service/uapi/search/engines", "", "admin", UAPISearchEngines)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("缺少 UApiPro 令牌应在本地返回 400 且不外呼，status=%d called=%t body=%s", recorder.Code, called, recorder.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["code"] != "UAPI_TOKEN_MISSING" || !strings.Contains(body["message"], "令牌为空") {
		t.Fatalf("缺少令牌错误响应 = %s", recorder.Body.String())
	}
}

func TestUAPISearchAggregateControllerReturnsSuccessfulPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPISearchTestDB(t, "uapi-test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/search/aggregate" || r.Header.Get("Authorization") != "Bearer uapi-test-key" {
			t.Errorf("upstream request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"query":"q","total_results":0,"results":[],"sources":[],"process_time_ms":1,"metadata":{}}`))
	}))
	t.Cleanup(server.Close)
	oldFactory := newUAPISearchClient
	newUAPISearchClient = func(key string) *uapi.Client {
		return uapi.NewClient(key).SetSearchAggregateURL(server.URL + "/api/v1/search/aggregate")
	}
	t.Cleanup(func() { newUAPISearchClient = oldFactory })
	recorder := performUAPISearchRequest(t, http.MethodPost, "/adapter-service/uapi/search/aggregate", `{"query":"q"}`, "admin", UAPISearchAggregate)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"total_results":0`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUAPIStatusUsageControllerReturnsUsageStatistics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPISearchTestDB(t, "uapi-test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/status/usage" || r.URL.Query().Get("path") != "/api/v1/search/aggregate" || r.Header.Get("Authorization") != "Bearer uapi-test-key" {
			t.Errorf("upstream request = %s %s query=%s auth=%q", r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"path":"/api/v1/search/aggregate","count":3}`))
	}))
	t.Cleanup(server.Close)
	oldFactory := newUAPISearchClient
	newUAPISearchClient = func(key string) *uapi.Client {
		return uapi.NewClient(key).SetStatusUsageURL(server.URL + "/api/v1/status/usage")
	}
	t.Cleanup(func() { newUAPISearchClient = oldFactory })
	recorder := performUAPISearchRequest(t, http.MethodGet, "/adapter-service/uapi/status/usage?path=/api/v1/search/aggregate", "", "admin", UAPIStatusUsage)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"count":3`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUAPISearchErrorResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPISearchTestDB(t, "uapi-key")
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	writeUAPISearchError(ctx, fmt.Errorf("%w: query 不能为空", uapi.ErrInvalidArgument))
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["code"] != "INVALID_REQUEST" || body["message"] == "" {
		t.Fatalf("错误响应 = %s", recorder.Body.String())
	}
}

func TestUAPISearchErrorDoesNotReuseSessionUnauthorizedStatus(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	writeUAPISearchError(ctx, &uapi.APIError{
		HTTPStatus: http.StatusUnauthorized,
		Code:       "UNAUTHORIZED",
		Message:    "无效的访问令牌",
	})
	var body map[string]string
	if recorder.Code != http.StatusBadGateway || json.Unmarshal(recorder.Body.Bytes(), &body) != nil || body["code"] != "UPSTREAM_AUTH_ERROR" {
		t.Fatalf("上游鉴权失败不应返回会话 401，status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
