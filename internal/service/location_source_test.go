package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestIPLocationCacheSeparatesCommercialSource(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		region := "默认地区"
		if r.URL.Query().Get("source") == "commercial" {
			region = "商业增强地区"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ip": "8.8.8.8", "region": region, "source": r.URL.Query().Get("source")})
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")
	db := newLocationTestDB(t)

	defaultResult, err := LookupIPLocationsDetailedWithDB(db, []string{"8.8.8.8"}, LocationQueryOptions{UAPIKey: "uapi-key"})
	if err != nil || defaultResult["8.8.8.8"].Address != "默认地区" {
		t.Fatalf("default lookup failed: %+v err=%v", defaultResult, err)
	}
	commercialResult, err := LookupIPLocationsDetailedWithDB(db, []string{"8.8.8.8"}, LocationQueryOptions{UAPIKey: "uapi-key", Source: "commercial"})
	if err != nil || commercialResult["8.8.8.8"].Address != "商业增强地区" {
		t.Fatalf("commercial lookup reused the default cache: %+v err=%v", commercialResult, err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected one upstream request per source, got %d", got)
	}

	var rows []entity.LocationCache
	if err := db.Order("source").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Source != "" || rows[1].Source != "commercial" {
		t.Fatalf("expected separate source cache rows, got %+v", rows)
	}
}

func TestIPLocationLookupDoesNotReuseUnknownLegacySource(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"8.8.8.8","region":"确认的默认地区"}`))
	}))
	defer server.Close()
	newDefaultLocationClient(t, server, "uapi-key")
	db := newLocationTestDB(t)
	legacy := entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: "8.8.8.8", Source: entity.LocationSourceLegacy, Address: "未知模式地区", RawJSON: `{"extra":"preserve"}`}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := LookupIPLocationsDetailedWithDB(db, []string{legacy.LookupKey}, LocationQueryOptions{UAPIKey: "uapi-key"})
		if err != nil || result[legacy.LookupKey].Address != "确认的默认地区" {
			t.Fatalf("lookup must use a known source: %+v err=%v", result, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("known source should be fetched only once, got %d requests", requests.Load())
	}
	var retained entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ? AND source = ?", legacy.Kind, legacy.LookupKey, legacy.Source).First(&retained).Error; err != nil || retained.RawJSON != legacy.RawJSON {
		t.Fatalf("historical original response must remain available: %+v err=%v", retained, err)
	}
}
