package service

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestNormalizeLocationAdminKeyValidatesAndNormalizes(t *testing.T) {
	tests := []struct {
		name string
		kind string
		key  string
		want string
		ok   bool
	}{
		{name: "phone", kind: entity.LocationKindPhone, key: " 13800138000 ", want: "13800138000", ok: true},
		{name: "ip lowercases", kind: entity.LocationKindIP, key: " 8.8.8.8 ", want: "8.8.8.8", ok: true},
		{name: "invalid kind", kind: "domain", key: "8.8.8.8"},
		{name: "invalid phone", kind: entity.LocationKindPhone, key: "123"},
		{name: "private ip", kind: entity.LocationKindIP, key: "192.168.1.1", want: "192.168.1.1", ok: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			kind, key, err := normalizeLocationAdminKey(testCase.kind, testCase.key)
			if testCase.name == "private ip" {
				if err != nil || kind != entity.LocationKindIP || key != testCase.want {
					t.Fatalf("normalization should not perform public/private policy check: %q %q %v", kind, key, err)
				}
				return
			}
			if testCase.ok {
				if err != nil || kind != testCase.kind || key != testCase.want {
					t.Fatalf("expected normalized key %q, got kind=%q key=%q err=%v", testCase.want, kind, key, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected invalid key to fail, got kind=%q key=%q", kind, key)
			}
		})
	}
}

func TestResolveOwnLocationIPsRequiresOwnership(t *testing.T) {
	db := newLocationTestDB(t)
	if err := db.AutoMigrate(&entity.User{}, &entity.Log{}, &entity.BillingRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.Log{UserID: 7, ClientIP: " 1.1.1.1 "}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.BillingRecord{UserID: 7, ClientIP: "8.8.8.8"}).Error; err != nil {
		t.Fatal(err)
	}
	operator := entity.User{ID: 7, LastLoginIP: "9.9.9.9", RegisterIP: "10.0.0.1"}

	owned, err := ResolveOwnLocationIPsWithDB(db, operator, []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"})
	if err != nil || len(owned) != 3 {
		t.Fatalf("expected owned audit, billing and login IPs, got %v err=%v", owned, err)
	}
	if _, err := ResolveOwnLocationIPsWithDB(db, operator, []string{"4.4.4.4"}); err != ErrLocationForbidden {
		t.Fatalf("expected arbitrary IP to be forbidden, got %v", err)
	}
}

func TestListAndDeleteLocationCacheWithDB(t *testing.T) {
	db := newLocationTestDB(t)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "8.8.8.8", "美国", `{}`)
	seedLocationRowForTest(t, db, entity.LocationKindPhone, "13800138000", "云南玉溪 移动", `{}`)
	if err := db.Create(&entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: "1.1.1.1", Address: ""}).Error; err != nil {
		t.Fatal(err)
	}

	result, err := ListLocationCacheWithDB(db, LocationCacheListQuery{Kind: entity.LocationKindIP, Keyword: "美国"}, 1, 20)
	if err != nil || result.Total != 1 || len(result.Data) != 1 || result.Data[0].LookupKey != "8.8.8.8" || result.Summary.Total != 1 || result.Summary.WithAddress != 1 || result.Summary.WithoutAddress != 0 {
		t.Fatalf("unexpected filtered cache result: %+v err=%v", result, err)
	}
	if err := DeleteLocationCacheWithDB(db, entity.LocationKindIP, " 8.8.8.8 "); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&entity.LocationCache{}).Where("kind = ? AND lookup_key = ?", entity.LocationKindIP, "8.8.8.8").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected IP cache row to be deleted, got %d", count)
	}
}

// 管理页列表也必须显示补上运营商后的文本（历史行 address 列可能只有 region）。
func TestListLocationCacheComposesAddressWithOperator(t *testing.T) {
	db := newLocationTestDB(t)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "8.8.8.8", "美国",
		`{"ip":"8.8.8.8","region":"美国","isp":"GOOGLE","llc":"GOOGLE","asn":"AS15169"}`)

	result, err := ListLocationCacheWithDB(db, LocationCacheListQuery{Kind: entity.LocationKindIP}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected one row, got %+v", result.Data)
	}
	if result.Data[0].Address != "美国 GOOGLE" {
		t.Fatalf("expected 美国 GOOGLE, got %q", result.Data[0].Address)
	}

	// 只改展示，不动数据库里的 address 列。
	var stored entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ?", entity.LocationKindIP, "8.8.8.8").First(&stored).Error; err != nil {
		t.Fatalf("load stored row: %v", err)
	}
	if stored.Address != "美国" {
		t.Fatalf("expected the stored column to stay untouched, got %q", stored.Address)
	}
}

func TestRefreshLocationCachePreservesOldRecordOnFailure(t *testing.T) {
	var requests atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	const refreshedRaw = `{"ip":"8.8.8.8","region":"美国 加利福尼亚","asn":15169,"extra":{"source":"commercial"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(refreshedRaw))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")
	db := newLocationTestDB(t)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "8.8.8.8", "旧地址", `{"region":"旧地址","extra":"preserve"}`)
	var before entity.LocationCache
	if err := db.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshLocationCacheWithDB(db, entity.LocationKindIP, "8.8.8.8", LocationQueryOptions{UAPIKey: "uapi-key"}); err == nil {
		t.Fatal("failed upstream refresh should return an error")
	}
	var after entity.LocationCache
	if err := db.First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before.Address != after.Address || before.RawJSON != after.RawJSON || !before.FetchedAt.Equal(after.FetchedAt) {
		t.Fatalf("failed refresh changed the old record: before=%+v after=%+v", before, after)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("a failed paid refresh must not be replayed, got %d requests", got)
	}
	fail.Store(false)
	refreshed, err := RefreshLocationCacheWithDB(db, entity.LocationKindIP, "8.8.8.8", LocationQueryOptions{UAPIKey: "uapi-key"})
	if err != nil || refreshed.Address != "美国 加利福尼亚" || refreshed.RawJSON != refreshedRaw {
		t.Fatalf("successful refresh did not retain full response: %+v err=%v", refreshed, err)
	}
	var count int64
	if err := db.Model(&entity.LocationCache{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || requests.Load() != 2 {
		t.Fatalf("refresh should update one record with one successful request: rows=%d requests=%d", count, requests.Load())
	}
}

func TestManualLookupLocationCacheUsesRefreshValidationAndPersists(t *testing.T) {
	const raw = `{"ip":"8.8.8.8","region":"美国","isp":"GOOGLE"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")
	db := newLocationTestDB(t)

	row, err := ManualLookupLocationCacheWithDB(db, entity.LocationKindIP, " 8.8.8.8 ", LocationQueryOptions{UAPIKey: "uapi-key"})
	if err != nil {
		t.Fatalf("manual lookup: %v", err)
	}
	if row.LookupKey != "8.8.8.8" || row.RawJSON != raw {
		t.Fatalf("unexpected persisted row: %+v", row)
	}
	if _, err := ManualLookupLocationCacheWithDB(db, entity.LocationKindIP, "192.168.1.1", LocationQueryOptions{UAPIKey: "uapi-key"}); err != ErrLocationInvalidKey {
		t.Fatalf("private IP must be rejected, got %v", err)
	}
}

func TestLegacyLocationCacheCanBeReviewedAndDeletedButNotRefreshed(t *testing.T) {
	db := newLocationTestDB(t)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "8.8.8.8", "默认地区", `{}`)
	legacy := entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: "8.8.8.8", Source: "legacy", Address: "历史地区", RawJSON: `{"extra":"preserve"}`}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	result, err := ListLocationCacheWithDB(db, LocationCacheListQuery{Kind: entity.LocationKindIP, Source: "legacy"}, 1, 20)
	if err != nil || result.Total != 1 || result.Data[0].Address != legacy.Address {
		t.Fatalf("legacy cache should be available for review: %+v err=%v", result, err)
	}
	if _, err := RefreshLocationCacheWithDB(db, legacy.Kind, legacy.LookupKey, LocationQueryOptions{UAPIKey: "key", Source: "legacy"}); err != ErrLocationInvalidKey {
		t.Fatalf("unknown historical source must not be queried as an active source, got %v", err)
	}
	if err := DeleteLocationCacheWithDB(db, legacy.Kind, legacy.LookupKey, legacy.Source); err != nil {
		t.Fatal(err)
	}
	var rows []entity.LocationCache
	if err := db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].Source != "" {
		t.Fatalf("deleting legacy should preserve identified sources: %+v err=%v", rows, err)
	}
}

func TestRefreshLocationCacheBatchMapsLegacyToDefaultAndKeepsHistory(t *testing.T) {
	const raw = `{"ip":"8.8.8.8","region":"美国","isp":"GOOGLE"}`
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")
	db := newLocationTestDB(t)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "8.8.8.8", "旧默认地址", `{"region":"旧默认地址"}`)
	if err := db.Create(&entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: "8.8.8.8", Source: entity.LocationSourceLegacy, Address: "历史地址", RawJSON: `{"region":"历史地址"}`}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := RefreshLocationCacheBatchWithDB(db, LocationCacheBatchRefreshRequest{
		Kind: entity.LocationKindIP,
		Items: []LocationCacheDeleteItem{
			{LookupKey: "8.8.8.8", Source: ""},
			{LookupKey: "8.8.8.8", Source: entity.LocationSourceLegacy},
		},
	}, LocationQueryOptions{UAPIKey: "uapi-key"})
	if err != nil || result.Requested != 2 || result.Refreshed != 2 || result.Failed != 0 {
		t.Fatalf("unexpected batch refresh result: %+v err=%v", result, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("default and legacy selection should share one upstream refresh, got %d", requests.Load())
	}
	var legacy entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ? AND source = ?", entity.LocationKindIP, "8.8.8.8", entity.LocationSourceLegacy).First(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.Address != "历史地址" {
		t.Fatalf("legacy history should remain untouched, got %q", legacy.Address)
	}
	var current entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ? AND source = ?", entity.LocationKindIP, "8.8.8.8", "").First(&current).Error; err != nil {
		t.Fatal(err)
	}
	if current.RawJSON != raw {
		t.Fatalf("default row was not refreshed: %+v", current)
	}
}

func TestDeleteLocationCacheBatchSupportsExplicitAndFilteredAllSelection(t *testing.T) {
	db := newLocationTestDB(t)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "8.8.8.8", "美国", `{}`)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "1.1.1.1", "澳大利亚", `{}`)
	seedLocationRowForTest(t, db, entity.LocationKindIP, "9.9.9.9", "美国", `{}`)

	deleted, err := DeleteLocationCacheBatchWithDB(db, LocationCacheBatchDeleteRequest{
		Kind:  entity.LocationKindIP,
		Items: []LocationCacheDeleteItem{{LookupKey: "8.8.8.8"}},
	})
	if err != nil || deleted != 1 {
		t.Fatalf("expected one explicit row to be deleted, got deleted=%d err=%v", deleted, err)
	}

	deleted, err = DeleteLocationCacheBatchWithDB(db, LocationCacheBatchDeleteRequest{
		Kind:    entity.LocationKindIP,
		Keyword: "美国",
		All:     true,
		Except:  []LocationCacheDeleteItem{{LookupKey: "9.9.9.9"}},
	})
	if err != nil || deleted != 0 {
		t.Fatalf("expected the exception to preserve the only matching row, got deleted=%d err=%v", deleted, err)
	}

	deleted, err = DeleteLocationCacheBatchWithDB(db, LocationCacheBatchDeleteRequest{Kind: entity.LocationKindIP, All: true})
	if err != nil || deleted != 2 {
		t.Fatalf("expected remaining row to be deleted by filtered all selection, got deleted=%d err=%v", deleted, err)
	}
}
