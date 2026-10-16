package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// seedLocationRowForTest 预置一条持久化归属地记录。
func seedLocationRowForTest(t *testing.T, db *gorm.DB, kind string, lookupKey string, address string, rawJSON string) {
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

// newDefaultLocationClient 建一个指向测试服务器的归属地客户端。
func newDefaultLocationClient(t *testing.T, server *httptest.Server, apiKey string) {
	t.Helper()

	phoneURL := locationPhoneInfoURL
	ipURL := locationIPInfoURL
	locationPhoneInfoURL = server.URL + "/api/v1/misc/phoneinfo"
	locationIPInfoURL = server.URL + "/api/v1/network/ipinfo"
	t.Cleanup(func() {
		locationPhoneInfoURL = phoneURL
		locationIPInfoURL = ipURL
		locationLookupGroup.reset()
	})
}

// 密钥错误（401）时：同一个号码连续 3 次都失败就整批放弃，后面的数据一次都不再请求。
//
// 这是用户明确要求的保护：避免因 UApiPro 密钥配错而不断尝试。
func TestPhoneLocationStopsAfterConsecutiveFailures(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"INVALID_API_KEY","message":"密钥无效"}`))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-wrong")

	db := newLocationTestDB(t)
	phones := []string{"13800138001", "13800138002", "13800138003", "13800138004", "13800138005"}

	locations := LookupPhoneLocationsWithDB(db, phones, LocationQueryOptions{UAPIKey: "uapi-wrong"})

	if len(locations) != 0 {
		t.Fatalf("expected no locations when the key is invalid, got %v", locations)
	}
	// 第一个号码尝试 3 次后放弃 → 总共只应有 3 次请求，而不是 5×3=15 次。
	if got := atomic.LoadInt64(&requests); got != locationLookupMaxAttempts {
		t.Fatalf("expected exactly %d requests before giving up, got %d", locationLookupMaxAttempts, got)
	}
}

// 同一个号码内部必须重试到上限才放弃（扛偶发抖动）。
func TestPhoneLocationRetriesSameKeyUpToMaxAttempts(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")

	db := newLocationTestDB(t)
	LookupPhoneLocationsWithDB(db, []string{"13800138001"}, LocationQueryOptions{UAPIKey: "uapi-key"})

	total := atomic.LoadInt64(&requests)
	if total != locationLookupMaxAttempts {
		t.Fatalf("expected exactly %d attempts without nested retries, got %d", locationLookupMaxAttempts, total)
	}
}

func TestConcurrentIPLocationsShareOnePersistedLookup(t *testing.T) {
	var requests atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"8.8.8.8","region":"美国","asn":15169}`))
	}))
	defer server.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	newDefaultLocationClient(t, server, "uapi-key")
	db := newLocationTestDB(t)
	type lookupResult struct {
		locations map[string]IPLocation
		err       error
	}
	const callers = 12
	results := make(chan lookupResult, callers)
	lookup := func() {
		locations, err := LookupIPLocationsDetailedWithDB(db, []string{"8.8.8.8"}, LocationQueryOptions{UAPIKey: "uapi-key"})
		results <- lookupResult{locations: locations, err: err}
	}
	go lookup()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream lookup did not start")
	}
	for i := 1; i < callers; i++ {
		go lookup()
	}
	releaseOnce.Do(func() { close(release) })
	for i := 0; i < callers; i++ {
		select {
		case result := <-results:
			if result.err != nil || result.locations["8.8.8.8"].Address != "美国" || result.locations["8.8.8.8"].ASN != "15169" {
				t.Fatalf("unexpected concurrent lookup result: %+v err=%v", result.locations, result.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent location lookup did not finish")
		}
	}
	// 再次调用也应复用持久化结果，而不是仅在并发窗口内去重。
	locations, err := LookupIPLocationsDetailedWithDB(db, []string{"8.8.8.8"}, LocationQueryOptions{UAPIKey: "uapi-key"})
	if err != nil || locations["8.8.8.8"].Address != "美国" {
		t.Fatalf("persisted lookup failed: %+v err=%v", locations, err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected one upstream request across concurrent and sequential lookups, got %d", got)
	}
}

// 中间成功一次就必须重置计数：单个坏数据不能拖垮整批。
//
// 这里的失败是 404（该号码上游无记录）——属于**单条数据**问题，不是密钥问题，
// 因此后续号码必须继续查询。
func TestPhoneLocationItemLevelFailureDoesNotAbortBatch(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		phone := r.URL.Query().Get("phone")
		if phone == "13800138002" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"未找到"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"province": "云南", "city": "玉溪", "sp": "移动", "area_code": "0877",
		})
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")

	db := newLocationTestDB(t)
	// 第二个号码会失败，但它前后都有成功的号码 → 不能触发整批放弃。
	phones := []string{"13800138001", "13800138002", "13800138003"}

	locations := LookupPhoneLocationsWithDB(db, phones, LocationQueryOptions{UAPIKey: "uapi-key"})

	if _, ok := locations["13800138001"]; !ok {
		t.Fatalf("expected the first phone to resolve, got %v", locations)
	}
	if _, ok := locations["13800138003"]; !ok {
		t.Fatalf("expected the last phone to resolve (failure counter must reset), got %v", locations)
	}
	if _, ok := locations["13800138002"]; ok {
		t.Fatalf("expected the failing phone to be absent, got %v", locations)
	}
}

// 成功结果必须落库（供下次零积分命中）。
func TestPhoneLocationPersistsSuccessfulResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"province": "云南", "city": "玉溪", "sp": "移动", "area_code": "0877",
		})
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")

	db := newLocationTestDB(t)
	locations := LookupPhoneLocationsWithDB(db, []string{"18287719599"}, LocationQueryOptions{UAPIKey: "uapi-key"})

	if locations["18287719599"].Address != "云南玉溪 移动" {
		t.Fatalf("expected 云南玉溪 移动, got %+v", locations["18287719599"])
	}

	var stored entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ?", entity.LocationKindPhone, "18287719599").First(&stored).Error; err != nil {
		t.Fatalf("expected the result to be persisted: %v", err)
	}
	if stored.Address != "云南玉溪 移动" {
		t.Fatalf("expected the persisted address, got %q", stored.Address)
	}
	if stored.RawJSON == "" {
		t.Fatal("expected the raw JSON to be persisted")
	}
}

// IP 端点同样受「连续失败即放弃」保护。
func TestIPLocationStopsAfterConsecutiveFailures(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"INVALID_API_KEY","message":"密钥无效"}`))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-wrong")

	db := newLocationTestDB(t)
	ips := []string{"8.8.8.8", "1.1.1.1", "9.9.9.9", "8.8.4.4"}

	locations := LookupIPLocationsWithDB(db, ips, LocationQueryOptions{UAPIKey: "uapi-wrong"})

	if len(locations) != 0 {
		t.Fatalf("expected no locations when the key is invalid, got %v", locations)
	}
	if got := atomic.LoadInt64(&requests); got != locationLookupMaxAttempts {
		t.Fatalf("expected exactly %d requests before giving up, got %d", locationLookupMaxAttempts, got)
	}
}

// IP 成功结果同样落库，address 拼成「region + 运营商」。
func TestIPLocationPersistsSuccessfulResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ip": "8.8.8.8", "region": "美国", "isp": "GOOGLE", "asn": "AS15169",
		})
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")

	db := newLocationTestDB(t)
	locations := LookupIPLocationsWithDB(db, []string{"8.8.8.8"}, LocationQueryOptions{UAPIKey: "uapi-key"})

	if locations["8.8.8.8"].Address != "美国 GOOGLE" {
		t.Fatalf("expected 美国 GOOGLE, got %+v", locations["8.8.8.8"])
	}
	var stored entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ?", entity.LocationKindIP, "8.8.8.8").First(&stored).Error; err != nil {
		t.Fatalf("expected the result to be persisted: %v", err)
	}
	if stored.Address != "美国 GOOGLE" {
		t.Fatalf("expected the persisted address to include the operator, got %q", stored.Address)
	}
}

// 失败结果绝不能落库，否则失败会被固化。
func TestLocationFailureIsNotPersisted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-wrong")

	db := newLocationTestDB(t)
	LookupPhoneLocationsWithDB(db, []string{"13800138001"}, LocationQueryOptions{UAPIKey: "uapi-wrong"})

	var count int64
	if err := db.Model(&entity.LocationCache{}).Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected nothing to be persisted on failure, got %d rows", count)
	}
}

// 同一批里重复 key 只查一次（去重在前置步骤完成）。
func TestLocationDeduplicatesKeysBeforeQuerying(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"province": "云南", "city": "玉溪", "sp": "移动"})
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")

	db := newLocationTestDB(t)
	LookupPhoneLocationsWithDB(db, []string{"13800138001", "13800138001", " 13800138001 "}, LocationQueryOptions{UAPIKey: "uapi-key"})

	if got := atomic.LoadInt64(&requests); got != 1 {
		t.Fatalf("expected a single upstream request after dedupe, got %d", got)
	}
}

// 内网 IP 不得触发任何请求。
func TestIPLocationSkipsPrivateAddressesWithoutRequests(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")

	db := newLocationTestDB(t)
	LookupIPLocationsWithDB(db, []string{"192.168.1.1", "127.0.0.1", "10.0.0.1"}, LocationQueryOptions{UAPIKey: "uapi-key"})

	if got := atomic.LoadInt64(&requests); got != 0 {
		t.Fatalf("expected no requests for private addresses, got %d", got)
	}
}

// 已缓存的部分不该再回源；只有缺失的才请求。
func TestLocationOnlyQueriesMissingKeys(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.URL.Query().Get("phone"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"province": "北京", "city": "", "sp": "联通"})
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")

	db := newLocationTestDB(t)
	seedLocationRowForTest(t, db, entity.LocationKindPhone, "13800138001", "云南玉溪 移动", `{}`)

	locations := LookupPhoneLocationsWithDB(db, []string{"13800138001", "13800138002"}, LocationQueryOptions{UAPIKey: "uapi-key"})

	if locations["13800138001"].Address != "云南玉溪 移动" {
		t.Fatalf("expected the cached address, got %+v", locations["13800138001"])
	}
	if locations["13800138002"].Address != "北京 联通" {
		t.Fatalf("expected the fetched address, got %+v", locations["13800138002"])
	}
	if len(requested) != 1 || requested[0] != "13800138002" {
		t.Fatalf("expected only the missing key to be queried, got %v", requested)
	}
}
