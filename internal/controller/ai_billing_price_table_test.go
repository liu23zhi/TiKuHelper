package controller

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func aiModelPriceTableTestPrice(input float64, cached float64, output float64) service.ResolvedAIModelTokenPrice {
	return service.ResolvedAIModelTokenPrice{
		ModelName:       "GPT-5.5",
		PriceID:         3,
		InputPrice:      input,
		CachedPrice:     cached,
		OutputPrice:     output,
		InputUnitPrice:  input,
		CachedUnitPrice: cached,
		OutputUnitPrice: output,
	}
}

func TestCalculateAIModelPriceTableAmountSplitsFreshAndCachedInput(t *testing.T) {
	price := aiModelPriceTableTestPrice(5, 0.5, 30)

	baseAmount, amount := calculateAIModelPriceTableAmount(price, searchpkg.AISearchUsage{
		InputTokens:       1000000,
		CachedInputTokens: 400000,
		OutputTokens:      200000,
		TotalTokens:       1200000,
	})

	// 600k 未命中缓存输入 * $5/M = 3.0；400k 缓存输入 * $0.5/M = 0.2；200k 输出 * $30/M = 6.0
	if baseAmount != 9.2 {
		t.Fatalf("expected base amount 9.2, got %v", baseAmount)
	}
	if amount != 9.2 {
		t.Fatalf("expected rounded amount 9.2, got %v", amount)
	}
}

func TestCalculateAIModelPriceTableAmountHandlesZeroAndRounding(t *testing.T) {
	price := aiModelPriceTableTestPrice(0.1, 0.01, 0.5)

	// 1 token 的极小额应四舍五入到 0.00，不会产生负值或 NaN。
	if _, amount := calculateAIModelPriceTableAmount(price, searchpkg.AISearchUsage{InputTokens: 1}); amount != 0 {
		t.Fatalf("expected tiny usage to round to 0, got %v", amount)
	}

	// 只有输出 token 时，缓存字段缺失不应该影响结果。
	_, outputOnly := calculateAIModelPriceTableAmount(price, searchpkg.AISearchUsage{OutputTokens: 1000000})
	if outputOnly != 0.5 {
		t.Fatalf("expected output-only amount 0.5, got %v", outputOnly)
	}
}

func TestCalculateAIModelPriceTableAmountClampsInvalidCachedTokens(t *testing.T) {
	price := aiModelPriceTableTestPrice(5, 0.5, 30)

	// 上游异常返回「缓存 token 大于输入 token」时，不能把缓存价用成负成本。
	baseAmount, _ := calculateAIModelPriceTableAmount(price, searchpkg.AISearchUsage{
		InputTokens:       1000,
		CachedInputTokens: 999999,
		OutputTokens:      0,
	})
	expected := 1000.0 / 1000000 * 0.5
	if baseAmount != expected {
		t.Fatalf("expected clamped base amount %v, got %v", expected, baseAmount)
	}

	baseAmount, _ = calculateAIModelPriceTableAmount(price, searchpkg.AISearchUsage{
		InputTokens:       1000,
		CachedInputTokens: -50,
		OutputTokens:      -5,
	})
	expected = 1000.0 / 1000000 * 5
	if baseAmount != expected {
		t.Fatalf("expected negative tokens to be clamped, got %v want %v", baseAmount, expected)
	}
}

func TestBuildAIModelPriceTableBillingSnapshotRecordsThreeTierPrices(t *testing.T) {
	price := aiModelPriceTableTestPrice(12.5, 1.25, 62.5)
	price.ModelName = "GPT-6 Astra Law *"

	snapshot := buildAIModelPriceTableBillingSnapshot(
		resolvedSearchRequestContext{AIConfig: service.AISearchRequestConfig{Model: "GPT-6 Astra Law *"}},
		model.SearchRequest{Question: "公式题"},
		searchpkg.AISearchUsage{InputTokens: 2000, CachedInputTokens: 500, OutputTokens: 1000, TotalTokens: 3000},
		price,
		0.01875,
		0.02,
	)

	// 覆盖到全部关键字段，便于账单页与排查时核对计价口径。
	for _, part := range []string{
		"pricing=model_price_table",
		"inputPrice=12.5000",
		"cachedPrice=1.2500",
		"outputPrice=62.5000",
		"inputTokens=1500",
		"cachedInputTokens=500",
		"outputTokens=1000",
		"question=" + url.QueryEscape("公式题"),
	} {
		if !strings.Contains(snapshot, part) {
			t.Fatalf("expected snapshot %q to contain %q", snapshot, part)
		}
	}
}

func TestBuildAIModelPriceTableBillingSnapshotClampsCachedTokens(t *testing.T) {
	price := aiModelPriceTableTestPrice(5, 0.5, 30)

	snapshot := buildAIModelPriceTableBillingSnapshot(
		resolvedSearchRequestContext{AIConfig: service.AISearchRequestConfig{Model: "GPT-5.5"}},
		model.SearchRequest{},
		searchpkg.AISearchUsage{InputTokens: 100, CachedInputTokens: 5000, OutputTokens: 10},
		price,
		0, 0,
	)

	if !strings.Contains(snapshot, "inputTokens=0") {
		t.Fatalf("expected fresh input tokens to clamp to 0, got %q", snapshot)
	}
	if !strings.Contains(snapshot, "cachedInputTokens=100") {
		t.Fatalf("expected cached tokens to clamp to input tokens, got %q", snapshot)
	}
	if bytes.Contains([]byte(snapshot), []byte("inputTokens=-")) {
		t.Fatalf("expected no negative token counts in snapshot %q", snapshot)
	}
}
