package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestRefreshLocationCacheRecordConvertsSelectedSourceOnly(t *testing.T) {
	for _, testCase := range []struct {
		name, original, target, unselected string
		collision                          bool
	}{
		{name: "legacy to ordinary", original: "legacy", target: "", unselected: "commercial"},
		{name: "ordinary to commercial", original: "", target: "commercial", unselected: "legacy"},
		{name: "existing target", original: "legacy", target: "commercial", unselected: "", collision: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Query().Get("source") != testCase.target {
					t.Errorf("unexpected query source: %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"ip":"8.8.8.8","region":"新地址"}`))
			}))
			defer server.Close()
			newDefaultLocationClient(t, server, "uapi-key")
			db := newLocationTestDB(t)
			for _, source := range []string{testCase.original, testCase.unselected} {
				if err := db.Create(&entity.LocationCache{Kind: "ip", LookupKey: "8.8.8.8", Source: source, Address: "旧地址", RawJSON: `{"original":true}`}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if testCase.collision {
				if err := db.Create(&entity.LocationCache{Kind: "ip", LookupKey: "8.8.8.8", Source: testCase.target, Address: "旧目标地址"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			row, err := RefreshLocationCacheRecordWithDB(db, "ip", "8.8.8.8", testCase.original, LocationQueryOptions{UAPIKey: "uapi-key", Source: testCase.target})
			if err != nil || row.Source != testCase.target || row.Address != "新地址" || requests.Load() != 1 {
				t.Fatalf("unexpected converted result: %+v calls=%d err=%v", row, requests.Load(), err)
			}
			var rows []entity.LocationCache
			if err := db.Order("source").Find(&rows).Error; err != nil || len(rows) != 2 {
				t.Fatalf("expected target and unselected source only: %+v err=%v", rows, err)
			}
			for _, stored := range rows {
				if stored.Source == testCase.unselected && (stored.Address != "旧地址" || stored.RawJSON != `{"original":true}`) {
					t.Fatalf("unselected source was changed: %+v", stored)
				}
				if stored.Source != testCase.target && stored.Source != testCase.unselected {
					t.Fatalf("selected original source was retained: %+v", stored)
				}
			}
		})
	}
}

func TestRefreshLocationCacheRecordRollsBackAllSourcesOnFailure(t *testing.T) {
	for _, failure := range []string{"upstream", "save", "delete"} {
		for _, collision := range []bool{false, true} {
			name := failure
			if collision {
				name += " with existing target"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if failure == "upstream" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = w.Write([]byte(`{"ip":"8.8.8.8","region":"新地址"}`))
				}))
				defer server.Close()
				newDefaultLocationClient(t, server, "uapi-key")
				db := newLocationTestDB(t)
				for _, source := range []string{"legacy", "commercial"} {
					if err := db.Create(&entity.LocationCache{Kind: "ip", LookupKey: "8.8.8.8", Source: source, Address: "原地址", RawJSON: `{"preserve":true}`}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if collision {
					if err := db.Create(&entity.LocationCache{Kind: "ip", LookupKey: "8.8.8.8", Address: "原目标地址", RawJSON: `{"target":true}`}).Error; err != nil {
						t.Fatal(err)
					}
				}
				var before []entity.LocationCache
				if err := db.Order("source").Find(&before).Error; err != nil {
					t.Fatal(err)
				}
				storageErr := errors.New("injected storage failure")
				switch failure {
				case "save":
					if err := db.Callback().Create().After("gorm:create").Register("test:fail_location_save", func(tx *gorm.DB) { tx.AddError(storageErr) }); err != nil {
						t.Fatal(err)
					}
				case "delete":
					if err := db.Callback().Delete().After("gorm:delete").Register("test:fail_location_delete", func(tx *gorm.DB) { tx.AddError(storageErr) }); err != nil {
						t.Fatal(err)
					}
				}
				_, err := RefreshLocationCacheRecordWithDB(db, "ip", "8.8.8.8", "legacy", LocationQueryOptions{UAPIKey: "uapi-key"})
				if err == nil || requests.Load() != 1 {
					t.Fatalf("failure must stop after one paid query: calls=%d err=%v", requests.Load(), err)
				}
				var after []entity.LocationCache
				if err := db.Order("source").Find(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("failed conversion changed records: before=%+v after=%+v err=%v", before, after, err)
				}
			})
		}
	}
}

func TestRefreshLocationCacheBatchDeduplicatesSelectedSourcesAndPreservesOthers(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"ip":"8.8.8.8","region":"新地址"}`))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")
	db := newLocationTestDB(t)
	for _, source := range []string{"", "legacy", "commercial"} {
		if err := db.Create(&entity.LocationCache{Kind: "ip", LookupKey: "8.8.8.8", Source: source, Address: "旧地址"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := RefreshLocationCacheBatchWithDB(db, LocationCacheBatchRefreshRequest{
		Kind: "ip", QuerySource: "commercial", Items: []LocationCacheDeleteItem{
			{LookupKey: "8.8.8.8", Source: "legacy"},
			{LookupKey: "8.8.8.8", Source: "legacy"},
		},
	}, LocationQueryOptions{UAPIKey: "uapi-key"})
	if err != nil || result.Requested != 1 || result.Refreshed != 1 || requests.Load() != 1 {
		t.Fatalf("unexpected deduplicated refresh: %+v calls=%d err=%v", result, requests.Load(), err)
	}
	var rows []entity.LocationCache
	if err := db.Order("source").Find(&rows).Error; err != nil || len(rows) != 2 || rows[0].Source != "" || rows[0].Address != "旧地址" || rows[1].Source != "commercial" || rows[1].Address != "新地址" {
		t.Fatalf("unselected ordinary source must remain unchanged: %+v err=%v", rows, err)
	}
}

func TestRefreshLocationCacheRecordFollowerConvertsItsOwnOriginalSource(t *testing.T) {
	db := newLocationTestDB(t)
	for _, source := range []string{"", "commercial", "legacy"} {
		if err := db.Create(&entity.LocationCache{Kind: "ip", LookupKey: "8.8.8.8", Source: source, Address: "旧地址"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&entity.LocationCache{}).Where("kind = ? AND lookup_key = ? AND source = ?", "ip", "8.8.8.8", "").Updates(map[string]interface{}{"address": "后续刷新地址", "raw_json": `{"version":2}`}).Error; err != nil {
		t.Fatal(err)
	}
	// 固定已由另一请求取得的共享结果，确保本调用走搭车分支而不再外呼。
	flightKey := "ip||8.8.8.8"
	call := &locationFlightCall{done: make(chan struct{}), result: locationFlightResult{ok: true, address: "共享新地址", rawJSON: `{"shared":true}`}}
	locationLookupGroup.mu.Lock()
	locationLookupGroup.calls[flightKey] = call
	locationLookupGroup.mu.Unlock()
	close(call.done)
	t.Cleanup(func() {
		locationLookupGroup.mu.Lock()
		delete(locationLookupGroup.calls, flightKey)
		locationLookupGroup.mu.Unlock()
	})
	row, err := RefreshLocationCacheRecordWithDB(db, "ip", "8.8.8.8", "legacy", LocationQueryOptions{UAPIKey: "uapi-key"})
	if err != nil || row.Source != "" || row.Address != "后续刷新地址" || row.RawJSON != `{"version":2}` {
		t.Fatalf("follower overwrote a newer target with the shared old result: %+v err=%v", row, err)
	}
	var rows []entity.LocationCache
	if err := db.Order("source").Find(&rows).Error; err != nil || len(rows) != 2 || rows[0].Source != "" || rows[1].Source != "commercial" || rows[1].Address != "旧地址" {
		t.Fatalf("follower must convert only its own original source: %+v err=%v", rows, err)
	}
}

func TestRefreshLocationCacheRecordFollowerDoesNotRecreateDeletedTarget(t *testing.T) {
	db := newLocationTestDB(t)
	if err := db.Create(&entity.LocationCache{Kind: "ip", LookupKey: "8.8.8.8", Source: "legacy", Address: "历史地址", RawJSON: `{"preserve":true}`}).Error; err != nil {
		t.Fatal(err)
	}
	var before []entity.LocationCache
	if err := db.Order("source").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	flightKey := "ip||8.8.8.8"
	call := &locationFlightCall{done: make(chan struct{}), result: locationFlightResult{ok: true, address: "已删除的旧结果", rawJSON: `{"version":1}`}}
	locationLookupGroup.mu.Lock()
	locationLookupGroup.calls[flightKey] = call
	locationLookupGroup.mu.Unlock()
	close(call.done)
	t.Cleanup(func() {
		locationLookupGroup.mu.Lock()
		delete(locationLookupGroup.calls, flightKey)
		locationLookupGroup.mu.Unlock()
	})
	if _, err := RefreshLocationCacheRecordWithDB(db, "ip", "8.8.8.8", "legacy", LocationQueryOptions{UAPIKey: "uapi-key"}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing target must fail without replaying a stale result, got %v", err)
	}
	var after []entity.LocationCache
	if err := db.Order("source").Find(&after).Error; err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("follower changed records after target deletion: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestRefreshLocationCacheWithoutOriginalSourceKeepsCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ip":"8.8.8.8","region":"新地址"}`))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")
	db := newLocationTestDB(t)
	if err := db.Create(&entity.LocationCache{Kind: "ip", LookupKey: "8.8.8.8", Source: "legacy", Address: "历史地址"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshLocationCacheWithDB(db, "ip", "8.8.8.8", LocationQueryOptions{UAPIKey: "uapi-key"}); err != nil {
		t.Fatal(err)
	}
	var rows []entity.LocationCache
	if err := db.Order("source").Find(&rows).Error; err != nil || len(rows) != 2 || rows[0].Address != "新地址" || rows[1].Source != "legacy" || rows[1].Address != "历史地址" {
		t.Fatalf("legacy callers must refresh only the requested target: %+v err=%v", rows, err)
	}
}
