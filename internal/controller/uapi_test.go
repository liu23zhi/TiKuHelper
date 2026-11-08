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
	"github.com/itihey/tikuAdapter/internal/service"
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

func TestLocationCacheManualLookupRejectsInvalidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)
	recorder := performUAPIRequest(t, "/adapter-service/location-cache/manual", "admin", LocationCacheManualLookup)
	// GET 没有 JSON body，ShouldBindJSON 必须稳定返回 400，而不能触发上游调用。
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid manual lookup body, got %d", recorder.Code)
	}
}

func TestLocationCacheManualLookupRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := performUAPIRequest(t, "/adapter-service/location-cache/manual", "user", LocationCacheManualLookup)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin manual lookup, got %d", recorder.Code)
	}
}

func TestLocationCacheBatchRefreshValidatesSelectedSourceWithoutKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)
	for _, testCase := range []struct {
		role   string
		source string
		status int
	}{
		{role: "user", source: "", status: http.StatusForbidden},
		{role: "admin", source: "commercial", status: http.StatusServiceUnavailable},
		{role: "admin", source: "", status: http.StatusServiceUnavailable},
		{role: "admin", source: "legacy", status: http.StatusBadRequest},
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		body := fmt.Sprintf(`{"kind":"ip","items":[{"lookupKey":"8.8.8.8","source":"legacy"}],"querySource":%q}`, testCase.source)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/location-cache/batch-refresh", strings.NewReader(body))
		ctx.Set("user", &entity.User{ID: 1, Role: testCase.role, Status: "enabled"})
		LocationCacheBatchRefresh(ctx)
		if recorder.Code != testCase.status {
			t.Fatalf("role=%s source=%s: expected %d, got %d %s", testCase.role, testCase.source, testCase.status, recorder.Code, recorder.Body.String())
		}
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

// 缺少密钥时只可返回已有缓存，configured 仍准确表示是否存在密钥。
func TestUAPIPhoneInfoWithoutAPIKeyReturnsCachedLocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)
	originalLookup := lookupPhoneLocationsDetailed
	t.Cleanup(func() { lookupPhoneLocationsDetailed = originalLookup })
	var calls int
	lookupPhoneLocationsDetailed = func(_ *gorm.DB, phones []string, options service.LocationQueryOptions) (map[string]service.PhoneLocation, error) {
		calls++
		if options.UAPIKey != "" || len(phones) != 1 || phones[0] != "13800138000" {
			t.Fatalf("unexpected query: phones=%v options=%+v", phones, options)
		}
		return map[string]service.PhoneLocation{"13800138000": {Address: "广东深圳 移动"}}, nil
	}

	recorder := performUAPIRequest(t, "/adapter-service/uapi/phoneinfo?phone=13800138000", "admin", UAPIPhoneInfo)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Data       map[string]service.PhoneLocation `json:"data"`
		Configured bool                             `json:"configured"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if calls != 1 || payload.Configured || payload.Data["13800138000"].Address != "广东深圳 移动" {
		t.Fatalf("expected existing cached location without new external calls, got %+v calls=%d", payload, calls)
	}
	for _, testCase := range []struct {
		role   string
		target string
		status int
	}{
		{role: "user", target: "/adapter-service/uapi/phoneinfo?phone=13800138000", status: http.StatusForbidden},
		{role: "admin", target: "/adapter-service/uapi/phoneinfo", status: http.StatusBadRequest},
	} {
		rejected := performUAPIRequest(t, testCase.target, testCase.role, UAPIPhoneInfo)
		if rejected.Code != testCase.status || calls != 1 {
			t.Fatalf("rejected query must not reach lookup: status=%d calls=%d", rejected.Code, calls)
		}
	}
}

func TestLocationCacheLookupReportsKeyRequirementAndConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalRefresh, originalManual := refreshLocationCache, manualLookupLocationCache
	t.Cleanup(func() { refreshLocationCache, manualLookupLocationCache = originalRefresh, originalManual })
	for _, endpoint := range []struct {
		name    string
		handler gin.HandlerFunc
		manual  bool
	}{
		{name: "refresh", handler: LocationCacheRefresh},
		{name: "manual", handler: LocationCacheManualLookup, manual: true},
	} {
		for _, key := range []string{"", "uapi-configured"} {
			keyName := "keyless"
			if key != "" {
				keyName = "with-key"
			}
			t.Run(endpoint.name+"/"+keyName, func(t *testing.T) {
				db := newUAPILocationTestDB(t)
				if key != "" {
					if err := db.Create(&entity.SystemSetting{Key: "uapi_api_key", Value: key}).Error; err != nil {
						t.Fatal(err)
					}
				}
				var calls int
				lookup := func(_ *gorm.DB, kind, lookupKey string, options service.LocationQueryOptions) (entity.LocationCache, error) {
					calls++
					if kind != "phone" || lookupKey != "13800138000" || options.UAPIKey != key || options.Source != "" {
						t.Fatalf("unexpected lookup: kind=%s key=%s options=%+v", kind, lookupKey, options)
					}
					if key == "" {
						return entity.LocationCache{}, service.ErrLocationNotConfigured
					}
					return entity.LocationCache{Kind: kind, LookupKey: lookupKey, Address: "广东深圳 移动"}, nil
				}
				if endpoint.manual {
					manualLookupLocationCache = lookup
				} else {
					refreshLocationCache = lookup
				}
				perform := func(role, body string) *httptest.ResponseRecorder {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/location-cache/"+endpoint.name, strings.NewReader(body))
					ctx.Set("user", &entity.User{ID: 1, Role: role, Status: "enabled"})
					endpoint.handler(ctx)
					return recorder
				}
				body := `{"kind":"phone","lookupKey":"13800138000"}`
				recorder := perform("admin", body)
				var payload struct {
					Configured bool                 `json:"configured"`
					Data       entity.LocationCache `json:"data"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if (key == "" && (recorder.Code != http.StatusServiceUnavailable || payload.Configured || payload.Data.Address != "")) || (key != "" && (recorder.Code != http.StatusOK || !payload.Configured || payload.Data.Address != "广东深圳 移动")) || calls != 1 {
					t.Fatalf("incorrect success response: status=%d payload=%+v calls=%d", recorder.Code, payload, calls)
				}
				if rejected := perform("user", body); rejected.Code != http.StatusForbidden || calls != 1 {
					t.Fatalf("non-admin must not call lookup: status=%d calls=%d", rejected.Code, calls)
				}
				if rejected := perform("admin", `{`); rejected.Code != http.StatusBadRequest || calls != 1 {
					t.Fatalf("invalid JSON must not call lookup: status=%d calls=%d", rejected.Code, calls)
				}
			})
		}
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

// IP 端点同理：命中持久化缓存直接返回，且 address 带上运营商。
func TestUAPIIPInfoReturnsPersistedLocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUAPILocationTestDB(t)

	// 故意把 address 列写成旧版本的「只有 region」，验证展示时按 raw_json 补上运营商。
	seedLocationRow(t, db, entity.LocationKindIP, "8.8.8.8", "美国",
		`{"ip":"8.8.8.8","region":"美国","isp":"GOOGLE","llc":"GOOGLE","asn":"AS15169"}`)

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
	if row.Address != "美国 GOOGLE" || row.Region != "美国" {
		t.Fatalf("expected 美国 GOOGLE, got %+v", row)
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

// 本地和私有 IP 不访问上游，但仍返回可识别的合成归属地。
func TestUAPIIPInfoLabelsPrivateAddresses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)

	recorder := performUAPIRequest(t, "/adapter-service/uapi/ipinfo?ips=192.168.1.1,127.0.0.1", "admin", UAPIIPInfo)
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
	if len(payload.Data) != 2 {
		t.Fatalf("expected local and private addresses to be labeled, got %v", payload.Data)
	}
	if payload.Data["127.0.0.1"].Address != "本地地址" || payload.Data["127.0.0.1"].Region != "本地地址" {
		t.Fatalf("unexpected loopback location: %+v", payload.Data["127.0.0.1"])
	}
	if payload.Data["192.168.1.1"].Address != "私有地址" || payload.Data["192.168.1.1"].Region != "私有地址" {
		t.Fatalf("unexpected private location: %+v", payload.Data["192.168.1.1"])
	}
}

func TestUAPIMyLocationsAllowsOnlyOwnedIPs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUAPILocationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.Log{}, &entity.BillingRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.BillingRecord{UserID: 1, ClientIP: "8.8.8.8"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.Log{UserID: 1, ClientIP: "1.1.1.1"}).Error; err != nil {
		t.Fatal(err)
	}
	seedLocationRow(t, db, entity.LocationKindIP, "8.8.8.8", "账单来源", `{}`)
	seedLocationRow(t, db, entity.LocationKindIP, "1.1.1.1", "日志来源", `{}`)
	seedLocationRow(t, db, entity.LocationKindIP, "9.9.9.9", "其他用户", `{}`)
	recorder := performUAPIRequest(t, "/adapter-service/uapi/my-locations?ips=8.8.8.8,1.1.1.1", "user", UAPIMyLocations)
	var payload struct {
		IP map[string]any `json:"ip"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || len(payload.IP) != 2 {
		t.Fatalf("expected own billing and audit IPs, got %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = performUAPIRequest(t, "/adapter-service/uapi/my-locations?ips=9.9.9.9", "user", UAPIMyLocations)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected arbitrary IP lookup to be forbidden, got %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestUAPIPhoneInfoWithoutAPIKeyCacheMissRequiresConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)
	original := lookupPhoneLocationsDetailed
	t.Cleanup(func() { lookupPhoneLocationsDetailed = original })
	lookupPhoneLocationsDetailed = func(_ *gorm.DB, _ []string, options service.LocationQueryOptions) (map[string]service.PhoneLocation, error) {
		if options.UAPIKey != "" {
			t.Fatal("expected missing key")
		}
		return map[string]service.PhoneLocation{}, service.ErrLocationNotConfigured
	}
	recorder := performUAPIRequest(t, "/adapter-service/uapi/phoneinfo?phone=13800138000", "admin", UAPIPhoneInfo)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"configured":false`) || !strings.Contains(recorder.Body.String(), service.LocationQueryErrorMessage(service.ErrLocationNotConfigured)) {
		t.Fatalf("cache miss must preserve degraded200 with unconfigured/error message: %d %s", recorder.Code, recorder.Body.String())
	}
}
