package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestLocationCacheRefreshDistinguishesRecordAndQuerySource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)
	original, originalRecord := refreshLocationCache, refreshLocationCacheRecord
	t.Cleanup(func() { refreshLocationCache, refreshLocationCacheRecord = original, originalRecord })
	legacyCalls, recordCalls := 0, 0
	refreshLocationCache = func(_ *gorm.DB, kind, key string, options service.LocationQueryOptions) (entity.LocationCache, error) {
		legacyCalls++
		return entity.LocationCache{Kind: kind, LookupKey: key, Source: options.Source}, nil
	}
	var capturedRecordSource string
	refreshLocationCacheRecord = func(_ *gorm.DB, kind, key, recordSource string, options service.LocationQueryOptions) (entity.LocationCache, error) {
		recordCalls++
		capturedRecordSource = recordSource
		return entity.LocationCache{Kind: kind, LookupKey: key, Source: options.Source, Address: "新地址"}, nil
	}
	for _, tc := range []struct {
		name, source string
	}{
		{name: "legacy to ordinary", source: "legacy"},
		{name: "ordinary explicit empty source", source: ""},
		{name: "commercial to ordinary", source: "commercial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			body := fmt.Sprintf(`{"kind":"ip","lookupKey":"8.8.8.8","source":"","recordSource":%q}`, tc.source)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/location-cache/refresh", strings.NewReader(body))
			ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
			before := recordCalls
			LocationCacheRefresh(ctx)
			var response struct {
				Data entity.LocationCache `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK || recordCalls != before+1 || capturedRecordSource != tc.source || legacyCalls != 0 || response.Data.Source != "" || response.Data.Address != "新地址" {
				t.Fatalf("record source must select migration while response reports query source: status=%d record=%q calls=%d/%d response=%+v", recorder.Code, capturedRecordSource, recordCalls, legacyCalls, response)
			}
		})
	}
	// Old callers do not identify an original row and retain the old contract.
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/location-cache/refresh", strings.NewReader(`{"kind":"ip","lookupKey":"8.8.8.8","source":""}`))
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	LocationCacheRefresh(ctx)
	if recorder.Code != http.StatusOK || legacyCalls != 1 || recordCalls != 3 {
		t.Fatalf("missing recordSource must retain the existing refresh contract: status=%d calls=%d/%d", recorder.Code, recordCalls, legacyCalls)
	}
}

func TestLocationCacheRefreshRecordRejectsInvalidSourceBeforeLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newUAPILocationTestDB(t)
	for _, body := range []string{
		`{"kind":"ip","lookupKey":"8.8.8.8","source":"","recordSource":"invalid"}`,
		`{"kind":"ip","lookupKey":"8.8.8.8","source":"legacy","recordSource":""}`,
		`{"kind":"phone","lookupKey":"13800138000","source":"","recordSource":"commercial"}`,
	} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/location-cache/refresh", strings.NewReader(body))
		ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
		LocationCacheRefresh(ctx)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid source must fail locally: status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
}
