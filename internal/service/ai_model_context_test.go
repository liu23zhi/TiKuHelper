package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestModelContextWindowCRUDPreservesLegacyClientsAndAllowsClearing(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	window := int64(128000)
	created, err := CreateAIModelPriceWithDB(db, CreateAIModelPriceInput{ModelName: "context-fixture", InputPrice: 2, ContextWindowTokens: &window})
	if err != nil || created.ContextWindowTokens != window {
		t.Fatalf("create: %+v error=%v", created, err)
	}
	updated, err := UpdateAIModelPriceWithDB(db, created.ID, UpdateAIModelPriceInput{ModelName: created.ModelName, InputPrice: 3})
	if err != nil || updated.ContextWindowTokens != window {
		t.Fatalf("legacy update must retain context: %+v error=%v", updated, err)
	}
	zero := int64(0)
	updated, err = UpdateAIModelPriceWithDB(db, created.ID, UpdateAIModelPriceInput{ModelName: created.ModelName, ContextWindowTokens: &zero})
	if err != nil || updated.ContextWindowTokens != 0 {
		t.Fatalf("explicit zero must clear context: %+v error=%v", updated, err)
	}
	for _, invalid := range []int64{-1, MaxAIModelContextWindowTokens + 1} {
		if _, err := UpdateAIModelPriceWithDB(db, created.ID, UpdateAIModelPriceInput{ModelName: created.ModelName, ContextWindowTokens: &invalid}); !errors.Is(err, ErrAIModelContextWindowInvalid) {
			t.Fatalf("invalid context %d accepted: %v", invalid, err)
		}
		if _, err := CreateAIModelPriceWithDB(db, CreateAIModelPriceInput{ModelName: "invalid", ContextWindowTokens: &invalid}); !errors.Is(err, ErrAIModelContextWindowInvalid) {
			t.Fatalf("invalid context %d accepted on create: %v", invalid, err)
		}
	}
}

func TestModelContextDefaultsOnlyFillUnknownWindow(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	for index, context := range []int64{0, 432100} {
		item := entity.AIModelPrice{ModelName: "custom-context-" + string(rune('a'+index)), DisplayName: "自定名称", ContextWindowTokens: context, InputPrice: 0, Multiplier: 1.9, IsUserModified: true}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		seed := entity.AIModelPrice{ModelName: item.ModelName, DisplayName: "默认名称", ContextWindowTokens: 128000, InputPrice: 10}
		if err := syncOneDefaultAIModelPrice(db, seed, 10); err != nil {
			t.Fatal(err)
		}
		var got entity.AIModelPrice
		if err := db.First(&got, item.ID).Error; err != nil {
			t.Fatal(err)
		}
		wantContext := context
		if wantContext == 0 {
			wantContext = seed.ContextWindowTokens
		}
		if got.ContextWindowTokens != wantContext || got.InputPrice != 0 || got.Multiplier != item.Multiplier || got.DisplayName != item.DisplayName {
			t.Fatalf("must only fill missing context, got %+v", got)
		}
	}
}

func TestLegacyModelCatalogDoesNotResurrectMissingModels(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := storeAIModelPriceSeededMarker(db, "old-release-without-manifest"); err != nil {
		t.Fatal(err)
	}
	kept := entity.AIModelPrice{ModelName: "gpt-5.5", DisplayName: "已有显示名", InputPrice: 1.234, Multiplier: 2}
	if err := db.Create(&kept).Error; err != nil {
		t.Fatal(err)
	}
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatal(err)
	}
	var deletedCount int64
	if err := db.Model(&entity.AIModelPrice{}).Where("model_name = ?", "gpt-6-astra").Count(&deletedCount).Error; err != nil {
		t.Fatal(err)
	}
	if deletedCount != 0 {
		t.Fatal("old deleted model was resurrected during upgrade")
	}
	var got entity.AIModelPrice
	if err := db.First(&got, kept.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.InputPrice != kept.InputPrice || got.Multiplier != kept.Multiplier || got.DisplayName != kept.DisplayName {
		t.Fatalf("upgrade changed existing values: %+v", got)
	}
}

func TestDefaultModelCatalogIncludesVerifiedContextAndKeepsOriginalPrices(t *testing.T) {
	byID := make(map[string]entity.AIModelPrice, len(defaultAIModelPrices))
	for _, item := range defaultAIModelPrices {
		byID[item.ModelName] = item
	}
	for _, original := range legacyDefaultAIModelPrices {
		item := byID[original.ModelName]
		if item.InputPrice != original.InputPrice || item.CachedPrice != original.CachedPrice || item.CacheWritePrice != original.CacheWritePrice || item.OutputPrice != original.OutputPrice || item.DisplayName != original.DisplayName {
			t.Fatalf("original default was changed: before=%+v after=%+v", original, item)
		}
	}
	for _, expected := range []struct {
		id      string
		context int64
	}{
		{"gpt-6.1-sol", 1050000}, {"gpt-4.1", 1047576},
		{"deepseek-flash", 1000000}, {"deepseek-v4-pro", 1000000},
		{"claude-sonnet-4-5", 200000}, {"claude-sonnet-5-5", 1000000},
		{"gpt-rosalind-discovery", 0}, {"daybreak-red", 0},
	} {
		item, found := byID[expected.id]
		if !found || item.ContextWindowTokens != expected.context {
			t.Fatalf("incorrect verified context for %q: %+v", expected.id, item)
		}
	}
}

func TestModelCatalogFailureRollsBackAndRetriesWithoutSuccessMarker(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	const callback = "model-catalog-test-reject-manifest"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if setting, ok := tx.Statement.Dest.(*entity.SystemSetting); ok && setting.Key == aimodelPriceCatalogSettingKey {
			tx.AddError(errors.New("simulated manifest write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDefaultAIModelPricesWithDB(db); err == nil {
		t.Fatal("catalog error was ignored")
	}
	var count int64
	if err := db.Model(&entity.AIModelPrice{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || aimodelPriceSeededMarker(db) != "" {
		t.Fatalf("failed synchronization left rows or success marker: rows=%d marker=%q", count, aimodelPriceSeededMarker(db))
	}
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if err := db.Model(&entity.AIModelPrice{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != int64(len(defaultAIModelPrices)) || aimodelPriceSeededMarker(db) == "" {
		t.Fatalf("retry did not complete catalog: rows=%d marker=%q", count, aimodelPriceSeededMarker(db))
	}
}

func TestModelPriceUpdatePreservesConcurrentContextCompletion(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	item, err := CreateAIModelPriceWithDB(db, CreateAIModelPriceInput{ModelName: "concurrent-context"})
	if err != nil {
		t.Fatal(err)
	}
	const callback = "model-context-completed-after-read"
	armed := true
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if armed && tx.Statement.Table == entity.TableNameAIModelPrice {
			armed = false
			if err := db.Model(&entity.AIModelPrice{}).Where("id = ?", item.ID).UpdateColumn("context_window_tokens", 128000).Error; err != nil {
				t.Error(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	updated, err := UpdateAIModelPriceWithDB(db, item.ID, UpdateAIModelPriceInput{ModelName: item.ModelName, InputPrice: 2})
	if err != nil || updated.ContextWindowTokens != 128000 {
		t.Fatalf("omitted context overwrote concurrent completion: %+v error=%v", updated, err)
	}
	var stored entity.AIModelPrice
	if err := db.First(&stored, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ContextWindowTokens != 128000 {
		t.Fatalf("concurrent context was erased: %+v", stored)
	}
}

func TestModelPriceUpdateCannotResurrectConcurrentlyDeletedRow(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	item, err := CreateAIModelPriceWithDB(db, CreateAIModelPriceInput{ModelName: "concurrent-delete"})
	if err != nil {
		t.Fatal(err)
	}
	const callback = "model-price-deleted-after-read"
	armed := true
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if armed && tx.Statement.Table == entity.TableNameAIModelPrice {
			armed = false
			if err := db.Delete(&entity.AIModelPrice{}, item.ID).Error; err != nil {
				t.Error(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, err := UpdateAIModelPriceWithDB(db, item.ID, UpdateAIModelPriceInput{ModelName: item.ModelName, InputPrice: 2}); !errors.Is(err, ErrAIModelPriceNotFound) {
		t.Fatalf("deleted row update should fail without reinserting: %v", err)
	}
	var count int64
	if err := db.Model(&entity.AIModelPrice{}).Where("id = ?", item.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("update resurrected a concurrently deleted model")
	}
}
