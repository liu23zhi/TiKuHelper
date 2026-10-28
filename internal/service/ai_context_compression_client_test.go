package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestBuildAIContextCompressionConfigResolvesCurrentModelAndDisplayName(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	window := int64(321000)
	if _, err := CreateAIModelPriceWithDB(db, CreateAIModelPriceInput{ModelName: "context-model", DisplayName: "上下文模型", ContextWindowTokens: &window}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"context-model", "上下文模型", "CONTEXT-MODEL", "unknown-model"} {
		cfg := AISearchRequestConfig{Model: model, ContextCompressionEnabled: true, UseRemoteContextCompression: true, ContextCompressionTriggerPercent: 85, ContextCompressionTargetPercent: 45, ContextCompressionMaxAttempts: 2}
		got := BuildAIContextCompressionConfigWithDB(db, cfg)
		wantWindow := window
		if model == "unknown-model" {
			wantWindow = 0
		}
		if !got.Enabled || !got.RemoteEnabled || got.TriggerPercent != 85 || got.TargetPercent != 45 || got.MaxAttempts != 2 || got.ContextWindowTokens != wantWindow {
			t.Fatalf("model=%q mapped incorrectly: %+v", model, got)
		}
	}
}

func TestBuildAIContextCompressionConfigDisabledDoesNotQueryDatabase(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	queries := 0
	count := func(*gorm.DB) { queries++ }
	if err := db.Callback().Query().Before("gorm:query").Register("disabled-compression-query", count); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register("disabled-compression-row", count); err != nil {
		t.Fatal(err)
	}
	got := BuildAIContextCompressionConfigWithDB(db, AISearchRequestConfig{Model: "gpt-6.1-sol", ContextCompressionEnabled: false, UseRemoteContextCompression: true, ContextCompressionTriggerPercent: 80, ContextCompressionTargetPercent: 50, ContextCompressionMaxAttempts: 3})
	if queries != 0 || got.Enabled || got.ContextWindowTokens != 0 {
		t.Fatalf("disabled compression queried or resolved a window: queries=%d config=%+v", queries, got)
	}
}

func TestBuildAIContextCompressionConfigLookupFailureDegradesToUnknownWindow(t *testing.T) {
	db := newAIModelPriceTestDB(t)
	if err := EnsureDefaultAIModelPricesWithDB(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("compression-window-query-error", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameAIModelPrice {
			tx.AddError(errors.New("simulated model lookup failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	got := BuildAIContextCompressionConfigWithDB(db, AISearchRequestConfig{Model: "gpt-6.1-sol", ContextCompressionEnabled: true, ContextCompressionTriggerPercent: 80, ContextCompressionTargetPercent: 50, ContextCompressionMaxAttempts: 3})
	if !got.Enabled || got.ContextWindowTokens != 0 || got.MaxAttempts != 3 {
		t.Fatalf("lookup error changed configuration or invented a window: %+v", got)
	}
}
