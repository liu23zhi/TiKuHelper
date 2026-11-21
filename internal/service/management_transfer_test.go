package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func managementTransferDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.LocationCache{}, &entity.UserBindingRecord{}, &entity.Log{}, &entity.BillingRecord{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestManagementLocationTransferRoundTripAndValidation(t *testing.T) {
	for _, kind := range []string{"ip", "phone"} {
		t.Run(kind, func(t *testing.T) {
			db := managementTransferDB(t)
			key := "203.0.113.5"
			if kind == "phone" {
				key = "13812345678"
			}
			resource := kind + "_locations"
			input := `[{"lookup_key":"` + key + `","source":"","address":"中国 测试","raw_json":"{}","fetched_at":"2026-11-20T00:00:00Z"}]`
			preview, err := ImportDataWithDB(db, ImportDataOptions{Resource: resource, Filename: "locations.json", Content: []byte(input), DryRun: true})
			if err != nil || preview.Succeeded != 1 {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			var count int64
			db.Model(&entity.LocationCache{}).Count(&count)
			if count != 0 {
				t.Fatal("preview wrote data")
			}
			result, err := ImportDataWithDB(db, ImportDataOptions{Resource: resource, Filename: "locations.json", Content: []byte(input)})
			if err != nil || result.Succeeded != 1 {
				t.Fatalf("import=%+v err=%v", result, err)
			}
			plain := false
			for _, format := range []string{"csv", "json"} {
				exported, err := ExportDataWithDB(db, ExportDataOptions{Resource: resource, Format: format, Masked: &plain})
				if err != nil || exported.Rows != 1 {
					t.Fatalf("export=%+v err=%v", exported, err)
				}
				if err := db.Where("kind = ?", kind).Delete(&entity.LocationCache{}).Error; err != nil {
					t.Fatal(err)
				}
				restored, err := ImportDataWithDB(db, ImportDataOptions{Resource: resource, Format: format, Content: exported.Content})
				if err != nil || restored.Succeeded != 1 {
					t.Fatalf("round trip %s: %+v %v", format, restored, err)
				}
				var row entity.LocationCache
				if err := db.First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.LookupKey != key || row.Address != "中国 测试" || !row.FetchedAt.Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)) {
					t.Fatalf("restored=%+v", row)
				}
			}
			invalid := strings.ReplaceAll(input, key, "not a valid key")
			bad, err := ImportDataWithDB(db, ImportDataOptions{Resource: resource, Format: "json", Content: []byte(invalid)})
			if err != nil || bad.Failed != 1 || bad.Succeeded != 0 {
				t.Fatalf("invalid=%+v %v", bad, err)
			}
			masked, err := ExportDataWithDB(db, ExportDataOptions{Resource: resource, Format: "json"})
			if err != nil || strings.Contains(string(masked.Content), key) {
				t.Fatalf("default export leaked key: %s %v", masked.Content, err)
			}
		})
	}
}

func TestManagementBindingExportKeepsKindsAndMasksValues(t *testing.T) {
	db := managementTransferDB(t)
	for _, row := range []entity.UserBindingRecord{
		{UserID: 1, Kind: "wechat", Value: "openid-private-abcdef", ValueHash: "wechat-hash", State: "released"},
		{UserID: 2, Kind: "phone", Value: "13812345678", ValueHash: "phone-hash", State: "voided", VoidReason: "测试"},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"wechat", "phone"} {
		result, err := ExportDataWithDB(db, ExportDataOptions{Resource: kind + "_bindings", Format: "json"})
		if err != nil || result.Rows != 1 {
			t.Fatalf("%s %+v %v", kind, result, err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(result.Content, &rows); err != nil {
			t.Fatal(err)
		}
		if rows[0]["kind"] != kind || strings.Contains(string(result.Content), "13812345678") || strings.Contains(string(result.Content), "openid-private-abcdef") || strings.Contains(string(result.Content), "value_hash") {
			t.Fatalf("unsafe export: %s", result.Content)
		}
	}
}

func TestManagementBillingExportPreservesSmallChargesAndUsage(t *testing.T) {
	db := managementTransferDB(t)
	row := entity.BillingRecord{UserID: 31, Type: "ocr", Amount: 0.00000001, GiftCharged: 0.00000001, Provider: "test-provider", ModelName: "test-model", InputTokens: 1000, CachedInputTokens: 700, OutputTokens: 30, TotalTokens: 1030, RequestID: "billing-transfer-request", ClientIP: "203.0.113.55", UnitPriceSnapshot: `{"price":0.00000001}`}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	output, err := ExportDataWithDB(db, ExportDataOptions{Resource: "billing", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	if err := json.Unmarshal(output.Content, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%+v", records)
	}
	got := records[0]
	for key, want := range map[string]string{"amount": "0.00000001", "gift_charged": "0.00000001", "provider": "test-provider", "model_name": "test-model", "cached_input_tokens": "700", "request_id": "billing-transfer-request", "unit_price_snapshot": row.UnitPriceSnapshot} {
		if got[key] != want {
			t.Fatalf("%s=%v want %s", key, got[key], want)
		}
	}
	if strings.Contains(string(output.Content), row.ClientIP) {
		t.Fatal("default export leaked full client IP")
	}
}
