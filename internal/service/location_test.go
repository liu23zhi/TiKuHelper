package service

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newLocationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.LocationCache{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// 展示文本规则：{province}{city} {sp}，并覆盖退化形态。
func TestFormatPhoneLocationAddress(t *testing.T) {
	testCases := []struct {
		name     string
		province string
		city     string
		sp       string
		want     string
	}{
		{name: "省市与运营商齐全", province: "云南", city: "玉溪", sp: "移动", want: "云南玉溪 移动"},
		{name: "直辖市 city 等于 province", province: "北京", city: "北京", sp: "联通", want: "北京 联通"},
		{name: "city 为空", province: "北京", city: "", sp: "联通", want: "北京 联通"},
		{name: "sp 为空", province: "云南", city: "玉溪", sp: "", want: "云南玉溪"},
		{name: "只有 province", province: "云南", city: "", sp: "", want: "云南"},
		{name: "只有 sp", province: "", city: "", sp: "移动", want: "移动"},
		{name: "全为空", province: "", city: "", sp: "", want: ""},
		{name: "首尾空白被清理", province: " 云南 ", city: " 玉溪 ", sp: " 移动 ", want: "云南玉溪 移动"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := FormatPhoneLocationAddress(testCase.province, testCase.city, testCase.sp)
			if got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

// 文档示例：13800138000 不保证，但云南玉溪移动必须拼成「云南玉溪 移动」。
func TestFormatPhoneLocationAddressMatchesDocumentedShape(t *testing.T) {
	got := FormatPhoneLocationAddress("云南", "玉溪", "移动")
	if got != "云南玉溪 移动" {
		t.Fatalf("expected 云南玉溪 移动, got %q", got)
	}
}

// IP 展示文本规则：{region} {运营商}，运营商优先 llc，回退 isp。
func TestFormatIPLocationAddress(t *testing.T) {
	testCases := []struct {
		name   string
		region string
		llc    string
		isp    string
		want   string
	}{
		{
			name:   "llc 优先（用户反馈的 IPv6 例子）",
			region: "中国 广东 中山", llc: "移动",
			isp:  "China Mobile Communications Group Co., Ltd.",
			want: "中国 广东 中山 移动",
		},
		{name: "llc 为空回退 isp", region: "美国", isp: "GOOGLE", want: "美国 GOOGLE"},
		{name: "两者都为空", region: "美国", want: "美国"},
		{name: "占位 N/A 不展示", region: "私有地址", isp: "N/A", want: "私有地址"},
		{name: "占位 N/A 但 llc 有值", region: "中国 广东 中山", llc: "移动", isp: "N/A", want: "中国 广东 中山 移动"},
		{name: "只有运营商", llc: "移动", want: "移动"},
		{name: "全为空", want: ""},
		{name: "首尾空白被清理", region: " 美国 ", llc: " GOOGLE ", want: "美国 GOOGLE"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := FormatIPLocationAddress(testCase.region, testCase.llc, testCase.isp)
			if got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

// 回归：历史缓存行的 address 列只有 region（缺运营商），
// 但 RawJSON 里存着 llc，展示时必须补上，且不需要回源重新付费查询。
func TestLookupIPLocationsFixesLegacyAddressWithoutOperator(t *testing.T) {
	db := newLocationTestDB(t)

	const ip = "2409:8a55:f1b1:51c0:68ac:ad9:3802:23fd"
	if err := db.Create(&entity.LocationCache{
		Kind:      entity.LocationKindIP,
		LookupKey: ip,
		// 旧版本写入的展示文本：只有 region。
		Address: "中国 广东 中山",
		RawJSON: `{"ip":"` + ip + `","beginip":"2409:8a55:f100::","endip":"2409:8a55:f1ff:ffff:ffff:ffff:ffff:ffff",` +
			`"region":"中国 广东 中山","isp":"China Mobile Communications Group Co., Ltd.","asn":"AS9808","llc":"移动",` +
			`"latitude":22.713907,"longitude":113.296358}`,
		FetchedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	locations := LookupIPLocationsWithDB(db, []string{ip}, LocationQueryOptions{})
	got, ok := locations[ip]
	if !ok {
		t.Fatalf("expected the persisted cache to be used, got %v", locations)
	}
	if got.Address != "中国 广东 中山 移动" {
		t.Fatalf("expected 中国 广东 中山 移动, got %q", got.Address)
	}
	if got.LLC != "移动" || got.Region != "中国 广东 中山" {
		t.Fatalf("expected structured fields to be restored, got %+v", got)
	}
}

func TestNormalizeLocationPhone(t *testing.T) {
	valid := []string{"13800138000", " 13800138000 "}
	for _, input := range valid {
		if got := NormalizeLocationPhone(input); got != "13800138000" {
			t.Fatalf("expected %q to normalize to 13800138000, got %q", input, got)
		}
	}

	invalid := []string{
		"", "1380013800", "138001380000", "23800138000",
		"1380013800a", "138-0013-8000", "+8613800138000",
	}
	for _, input := range invalid {
		if got := NormalizeLocationPhone(input); got != "" {
			t.Fatalf("expected %q to be rejected, got %q", input, got)
		}
	}
}

// 内网/保留地址必须被识别出来，避免白耗积分。
func TestIsPrivateOrReservedIP(t *testing.T) {
	private := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.1.1", "0.0.0.0", "255.255.255.255",
		"::1", "fc00::1", "fe80::1", "",
	}
	for _, ip := range private {
		if !IsPrivateOrReservedIP(ip) {
			t.Fatalf("expected %q to be treated as private/reserved", ip)
		}
	}

	// 域名不是 IP，不能因为解析不出来就跳过（它可能指向公网地址）。
	public := []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "cn.bing.com", "example.com"}
	for _, ip := range public {
		if IsPrivateOrReservedIP(ip) {
			t.Fatalf("expected %q to be queryable", ip)
		}
	}
}

// 公网与本地/私有 IP 都应进入统一的归属地流程；本地/私有 IP 由本地合成结果，
// 不会发起 UApiPro 请求，但必须能持久化并展示。
func TestNormalizeIPLookupKeysRetainsPrivateAndDeduplicates(t *testing.T) {
	keys := normalizeIPLookupKeys([]string{
		"8.8.8.8", "8.8.8.8", " 8.8.8.8 ",
		"192.168.1.1", "127.0.0.1",
		"1.1.1.1",
		"0.0.0.0", "255.255.255.255", "", "bad ip!",
	})

	want := map[string]bool{"8.8.8.8": true, "192.168.1.1": true, "127.0.0.1": true, "1.1.1.1": true}
	if len(keys) != len(want) {
		t.Fatalf("expected %v, got %v", want, keys)
	}
	for _, key := range keys {
		if !want[key] {
			t.Fatalf("unexpected key %q in %v", key, keys)
		}
	}
}

func TestLookupIPLocationsReturnsAndPersistsSyntheticLocalAndPrivate(t *testing.T) {
	db := newLocationTestDB(t)

	locations, err := LookupIPLocationsDetailedWithDB(db, []string{"127.0.0.1", "192.168.1.1"}, LocationQueryOptions{})
	if err != nil {
		t.Fatalf("expected local/private addresses to work without an API key, got %v", err)
	}

	local, ok := locations["127.0.0.1"]
	if !ok || local.Address != "本地地址" || local.Region != "本地地址" || local.ISP != "N/A" || local.ASN != "N/A" {
		t.Fatalf("unexpected local address result: %+v", local)
	}
	private, ok := locations["192.168.1.1"]
	if !ok || private.Address != "私有地址" || private.Region != "私有地址" || private.ISP != "N/A" || private.ASN != "N/A" {
		t.Fatalf("unexpected private address result: %+v", private)
	}

	var rows []entity.LocationCache
	if err := db.Where("kind = ?", entity.LocationKindIP).Order("lookup_key asc").Find(&rows).Error; err != nil {
		t.Fatalf("read synthetic cache rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two persisted synthetic rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Address != "本地地址" || rows[1].Address != "私有地址" {
		t.Fatalf("unexpected persisted synthetic rows: %+v", rows)
	}

	// 没有 API Key 仍可从持久化缓存读取，且不应退化为未配置错误。
	second, err := LookupIPLocationsDetailedWithDB(db, []string{"127.0.0.1", "192.168.1.1"}, LocationQueryOptions{})
	if err != nil || len(second) != 2 {
		t.Fatalf("expected persisted synthetic rows to be reusable, err=%v result=%+v", err, second)
	}
}

func TestNormalizePhoneLookupKeysDeduplicatesAndSkipsInvalid(t *testing.T) {
	keys := normalizePhoneLookupKeys([]string{
		"13800138000", "13800138000", " 13800138000 ",
		"13900139000", "invalid", "",
	})

	if len(keys) != 2 {
		t.Fatalf("expected 2 unique valid phones, got %v", keys)
	}
}

func TestLookupPhoneLocationsWithoutAPIKeyDeniesMissesButReusesCache(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"province":"广东","city":"深圳","sp":"移动"}`))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "")
	db := newLocationTestDB(t)
	for i := 0; i < 2; i++ {
		locations, err := LookupPhoneLocationsDetailedWithDB(db, []string{"13800138000"}, LocationQueryOptions{})
		if err != ErrLocationNotConfigured || len(locations) != 0 {
			t.Fatalf("uncached keyless query must fail locally: %v err=%v", locations, err)
		}
	}
	var count int64
	db.Model(&entity.LocationCache{}).Count(&count)
	if count != 0 || requests.Load() != 0 {
		t.Fatalf("keyless query populated cache or called upstream: rows=%d requests=%d", count, requests.Load())
	}
	if err := storeLocationCacheRows(db, []entity.LocationCache{{Kind: "phone", LookupKey: "13800138000", Address: "广东深圳 移动"}}); err != nil {
		t.Fatal(err)
	}
	locations, err := LookupPhoneLocationsDetailedWithDB(db, []string{"13800138000"}, LocationQueryOptions{})
	if err != nil || locations["13800138000"].Address != "广东深圳 移动" || requests.Load() != 0 {
		t.Fatalf("existing cache must remain readable without calls: %v err=%v requests=%d", locations, err, requests.Load())
	}
}

func TestLookupPhoneLocationsWithInvalidNumbersReturnsEmpty(t *testing.T) {
	db := newLocationTestDB(t)

	locations := LookupPhoneLocationsWithDB(db, []string{"not-a-phone", "123"}, LocationQueryOptions{UAPIKey: "uapi-fake"})
	if len(locations) != 0 {
		t.Fatalf("expected invalid numbers to be skipped, got %v", locations)
	}
}

// 持久化缓存命中时不再回源：这里预置一行，验证能直接读出且不依赖 API Key。
func TestLookupPhoneLocationsUsesPersistedCache(t *testing.T) {
	db := newLocationTestDB(t)

	if err := db.Create(&entity.LocationCache{
		Kind:      entity.LocationKindPhone,
		LookupKey: "13800138000",
		Address:   "云南玉溪 移动",
		RawJSON:   `{"province":"云南","city":"玉溪","sp":"移动","area_code":"0877"}`,
		FetchedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// 故意不传 API Key：能读出来就证明走的是持久化缓存而非回源。
	locations := LookupPhoneLocationsWithDB(db, []string{"13800138000"}, LocationQueryOptions{})

	got, ok := locations["13800138000"]
	if !ok {
		t.Fatalf("expected the persisted cache to be used, got %v", locations)
	}
	if got.Address != "云南玉溪 移动" {
		t.Fatalf("expected address 云南玉溪 移动, got %q", got.Address)
	}
	if got.Province != "云南" || got.City != "玉溪" || got.SP != "移动" || got.AreaCode != "0877" {
		t.Fatalf("expected structured fields to be restored, got %+v", got)
	}
}

// 已有有效地址永久复用，只有管理员显式刷新才重新付费查询。
func TestLookupIPLocationsReusesOldCacheRows(t *testing.T) {
	db := newLocationTestDB(t)

	if err := db.Create(&entity.LocationCache{
		Kind:      entity.LocationKindIP,
		LookupKey: "8.8.8.8",
		Address:   "美国",
		RawJSON:   `{"ip":"8.8.8.8","region":"美国"}`,
		FetchedAt: time.Now().Add(-30 * 24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	locations := LookupIPLocationsWithDB(db, []string{"8.8.8.8"}, LocationQueryOptions{})
	if locations["8.8.8.8"].Address != "美国" {
		t.Fatalf("expected old persisted address to be reused, got %v", locations)
	}
}

func TestLocationCacheWithoutAddressIsNotUsable(t *testing.T) {
	db := newLocationTestDB(t)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "8.8.8.8", " ", `{}`)
	if rows := loadLocationCacheRows(db, entity.LocationKindIP, "", []string{"8.8.8.8"}); len(rows) != 0 {
		t.Fatalf("expected empty addresses to be treated as cache misses, got %+v", rows)
	}
}

// IP 缓存命中能还原结构化字段，展示文本拼成「region + 运营商」。
func TestLookupIPLocationsUsesPersistedCache(t *testing.T) {
	db := newLocationTestDB(t)

	if err := db.Create(&entity.LocationCache{
		Kind:      entity.LocationKindIP,
		LookupKey: "8.8.8.8",
		Address:   "美国",
		RawJSON:   `{"ip":"8.8.8.8","region":"美国","isp":"GOOGLE","llc":"GOOGLE","asn":"AS15169"}`,
		FetchedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	locations := LookupIPLocationsWithDB(db, []string{"8.8.8.8"}, LocationQueryOptions{})

	got, ok := locations["8.8.8.8"]
	if !ok {
		t.Fatalf("expected the persisted cache to be used, got %v", locations)
	}
	if got.Address != "美国 GOOGLE" || got.Region != "美国" {
		t.Fatalf("expected 美国 GOOGLE, got %+v", got)
	}
	if got.ISP != "GOOGLE" || got.ASN != "AS15169" {
		t.Fatalf("expected structured fields to be restored, got %+v", got)
	}
}

// 落库必须是 upsert：同 key 第二次查询只更新一行。
func TestStoreLocationCacheRowsUpserts(t *testing.T) {
	db := newLocationTestDB(t)

	rows := []entity.LocationCache{{
		Kind:      entity.LocationKindPhone,
		LookupKey: "13800138000",
		Address:   "云南玉溪 移动",
		RawJSON:   `{"province":"云南","city":"玉溪","sp":"移动"}`,
	}}
	storeLocationCacheRows(db, rows)
	storeLocationCacheRows(db, []entity.LocationCache{{
		Kind:      entity.LocationKindPhone,
		LookupKey: "13800138000",
		Address:   "云南昆明 移动",
		RawJSON:   `{"province":"云南","city":"昆明","sp":"移动"}`,
	}})

	var count int64
	if err := db.Model(&entity.LocationCache{}).Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected upsert to keep a single row, got %d", count)
	}

	var stored entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ?", entity.LocationKindPhone, "13800138000").First(&stored).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if stored.Address != "云南昆明 移动" {
		t.Fatalf("expected the row to be refreshed, got %q", stored.Address)
	}
}

// 原始响应解析失败时不能 panic，只退化为零值。
func TestParseLocationRawJSONToleratesBadPayloads(t *testing.T) {
	if got := parsePhoneInfoRawJSON("not json"); got.Province != "" || got.City != "" || got.SP != "" {
		t.Fatalf("expected zero value for bad payload, got %+v", got)
	}
	if got := parseIPInfoRawJSON("{oops"); got.Region != "" || got.IP != "" {
		t.Fatalf("expected zero value for bad payload, got %+v", got)
	}
	if got := parsePhoneInfoRawJSON(""); got.Province != "" {
		t.Fatalf("expected zero value for empty payload, got %+v", got)
	}
	if got := parseIPInfoRawJSON(""); got.Region != "" {
		t.Fatalf("expected zero value for empty payload, got %+v", got)
	}
}

// 合法响应必须能解出全部字段（结构化字段从 RawJSON 还原）。
func TestParseLocationRawJSONExtractsFields(t *testing.T) {
	phone := parsePhoneInfoRawJSON(`{"province":"云南","city":"玉溪","sp":"移动","area_code":"0877"}`)
	if phone.Province != "云南" || phone.City != "玉溪" || phone.SP != "移动" || phone.AreaCode != "0877" {
		t.Fatalf("unexpected phone fields: %+v", phone)
	}

	ip := parseIPInfoRawJSON(`{"ip":"8.8.8.8","region":"美国","isp":"GOOGLE","llc":"GOOGLE","asn":"AS15169"}`)
	if ip.IP != "8.8.8.8" || ip.Region != "美国" || ip.ISP != "GOOGLE" || ip.ASN != "AS15169" {
		t.Fatalf("unexpected ip fields: %+v", ip)
	}
}

// 日志里不得出现完整手机号。
func TestMaskPhoneForLogHidesDigits(t *testing.T) {
	masked := maskPhoneForLog("13800138000")
	if masked == "13800138000" {
		t.Fatal("expected the phone number to be masked")
	}
	if masked[len(masked)-4:] != "8000" {
		t.Fatalf("expected the last 4 digits to remain for correlation, got %q", masked)
	}
}
