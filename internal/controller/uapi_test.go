package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// newUAPILocationTestDB 建一个只含归属地缓存与设置的库，并让控制器读它。
//
// 控制器通过 dao 解析持久化库（与其它 controller 测试一致），因此这里用 dao.SetDefault。
func newUAPILocationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:uapi-location-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.LocationCache{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	dao.SetDefault(db)
	return db
}

// performUAPIRequest 构造一个带指定角色的上下文并调用端点。
func performUAPIRequest(t *testing.T, target string, role string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	ctx.Set("user", &entity.User{ID: 1, Role: role, Status: "enabled"})

	handler(ctx)
	return recorder
}

// seedLocationRow 预置一条持久化归属地记录。
func seedLocationRow(t *testing.T, db *gorm.DB, kind string, lookupKey string, address string, rawJSON string) {
	t.Helper()

	if err := db.Create(&entity.LocationCache{
		Kind:      kind,
		LookupKey: lookupKey,
		Address:   address,
		RawJSON:   rawJSON,
		FetchedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed location cache: %v", err)
	}
}

// 非管理员必须被拒绝，不能查询归属地（也不该消耗积分）。
func TestUAPIPhoneInfoRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/phoneinfo?phone=13800138000", "user", UAPIPhoneInfo)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestUAPIIPInfoRejectsNonAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/ipinfo?ip=8.8.8.8", "user", UAPIIPInfo)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

// 缺少参数必须报 400 并给出明确提示，而不是静默返回空结果。
func TestUAPIPhoneInfoRequiresParameter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/phoneinfo", "admin", UAPIPhoneInfo)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when the parameter is missing, got %d", recorder.Code)
	}
}

func TestUAPIIPInfoRequiresParameter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/ipinfo", "admin", UAPIIPInfo)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when the parameter is missing, got %d", recorder.Code)
	}
}

// 超过上限必须拒绝，避免一次请求把上游速率打满。
func TestUAPIPhoneInfoRejectsTooManyKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)

	phones := make([]string, 0, maxLocationLookupKeys+1)
	for i := 0; i <= maxLocationLookupKeys; i++ {
		phones = append(phones, "13800138000")
	}

	recorder := performUAPIRequest(t, "/adapter-service/uapi/phoneinfo?phones="+strings.Join(phones, ","), "admin", UAPIPhoneInfo)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when exceeding the key limit, got %d", recorder.Code)
	}
}

// source 只接受 commercial，其它值必须报 400。
func TestUAPIIPInfoRejectsUnknownSource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/ipinfo?ip=8.8.8.8&source=standard", "admin", UAPIIPInfo)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unsupported source, got %d", recorder.Code)
	}
}

// 未配置密钥时必须返回 200 与空 data（前端据此只显示原始值），不能报错。
func TestUAPIPhoneInfoWithoutAPIKeyReturnsEmptyData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/phoneinfo?phone=13800138000", "admin", UAPIPhoneInfo)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 0 {
		t.Fatalf("expected empty data without an API key, got %v", payload.Data)
	}
}

// 命中持久化缓存时必须返回归属地（且不需要 API Key）。
func TestUAPIPhoneInfoReturnsPersistedLocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUAPILocationTestDB(t)

	seedLocationRow(t, db, entity.LocationKindPhone, "18287719599", "云南玉溪 移动",
		`{"province":"云南","city":"玉溪","sp":"移动","area_code":"0877"}`)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/phoneinfo?phone=18287719599", "admin", UAPIPhoneInfo)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Data map[string]struct {
			Address  string `json:"address"`
			Province string `json:"province"`
			City     string `json:"city"`
			SP       string `json:"sp"`
			AreaCode string `json:"areaCode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	row, ok := payload.Data["18287719599"]
	if !ok {
		t.Fatalf("expected an entry for the phone, got %v", payload.Data)
	}
	// address 是前端展示「18287719599 （云南玉溪 移动）」所依赖的字段。
	if row.Address != "云南玉溪 移动" {
		t.Fatalf("expected address 云南玉溪 移动, got %q", row.Address)
	}
	if row.Province != "云南" || row.City != "玉溪" || row.SP != "移动" || row.AreaCode != "0877" {
		t.Fatalf("expected structured fields, got %+v", row)
	}
}

// IP 端点同理：命中持久化缓存直接返回。
func TestUAPIIPInfoReturnsPersistedLocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUAPILocationTestDB(t)

	seedLocationRow(t, db, entity.LocationKindIP, "8.8.8.8", "美国",
		`{"ip":"8.8.8.8","region":"美国","isp":"GOOGLE","asn":"AS15169"}`)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/ipinfo?ip=8.8.8.8", "admin", UAPIIPInfo)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Data map[string]struct {
			Address string `json:"address"`
			Region  string `json:"region"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	row, ok := payload.Data["8.8.8.8"]
	if !ok {
		t.Fatalf("expected an entry for the ip, got %v", payload.Data)
	}
	if row.Address != "美国" || row.Region != "美国" {
		t.Fatalf("expected region-based address, got %+v", row)
	}
}

// 批量参数必须一次覆盖多个 key。
func TestUAPIPhoneInfoAcceptsBulkParameter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUAPILocationTestDB(t)

	seedLocationRow(t, db, entity.LocationKindPhone, "13800138000", "广东深圳 移动", `{}`)
	seedLocationRow(t, db, entity.LocationKindPhone, "13900139000", "北京 联通", `{}`)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/phoneinfo?phones=13800138000,13900139000", "admin", UAPIPhoneInfo)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Data map[string]struct {
			Address string `json:"address"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("expected both keys to be resolved, got %v", payload.Data)
	}
	if payload.Data["13800138000"].Address != "广东深圳 移动" {
		t.Fatalf("unexpected first address: %+v", payload.Data["13800138000"])
	}
}

// 内网 IP 必须被跳过：不发请求、也没有结果。
func TestUAPIIPInfoSkipsPrivateAddresses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/ipinfo?ips=192.168.1.1,127.0.0.1", "admin", UAPIIPInfo)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 0 {
		t.Fatalf("expected private addresses to be skipped, got %v", payload.Data)
	}
}
