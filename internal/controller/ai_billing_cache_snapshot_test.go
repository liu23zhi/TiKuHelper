package controller

import (
	"fmt"
	"strings"
	"testing"

	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestAppendAIStagedBillingSnapshotPreservesEachCallTotalInputAndCacheTokens(t *testing.T) {
	usage := searchpkg.AISearchUsage{
		InputTokens: 10000, CachedInputTokens: 4600, OutputTokens: 700, TotalTokens: 10700,
		ModelCalls: []searchpkg.AIModelCallUsage{
			{Model: "gpt-test", InputTokens: 1000, CachedInputTokens: 100, OutputTokens: 200, TotalTokens: 1200},
			{Model: "gpt-test", InputTokens: 9000, CachedInputTokens: 4500, OutputTokens: 500, TotalTokens: 9500},
		},
	}
	price := aiModelPriceTableTestPrice(5, 0.5, 30)
	price.ModelName = "gpt-test"
	settings := service.AIStagedTokenBillingSettings{}
	billing := calculateAIStagedTokenBilling(price, usage, settings, true)
	baseSnapshot := buildAIModelPriceTableBillingSnapshot(resolvedSearchRequestContext{}, model.SearchRequest{}, usage, price, billing.BaseAmount, billing.Amount)
	snapshot := appendAIStagedBillingSnapshot(baseSnapshot, settings, billing, billing.BaseAmount, billing.Amount, true)

	if got := service.ExtractBillingSnapshotField(snapshot, "inputTokens"); got != "5400" {
		t.Fatalf("top-level price snapshot must keep fresh input tokens, got %q", got)
	}
	for index, want := range []string{
		",inputTokens=1000,cachedInputTokens=100,",
		",inputTokens=9000,cachedInputTokens=4500,",
	} {
		call := service.ExtractBillingSnapshotField(snapshot, fmt.Sprintf("stageBillingCall%d", index+1))
		if !strings.Contains(call, want) {
			t.Errorf("call %d must preserve total input and cached input separately: got %q, want %q", index+1, call, want)
		}
	}
}

func TestAppendAIStagedBillingSnapshotKeepsTwentyCallsWithTokenBreakdown(t *testing.T) {
	billing := aiStagedTokenBillingResult{Calls: make([]aiStagedTokenBillingCall, 22)}
	for index := range billing.Calls {
		billing.Calls[index] = aiStagedTokenBillingCall{
			CallIndex: index + 1, Model: "gpt-test", InputTokens: int64(1000 + index), CachedInputTokens: int64(index), TotalTokens: int64(1000 + index),
		}
	}
	snapshot := appendAIStagedBillingSnapshot("", service.AIStagedTokenBillingSettings{}, billing, 0, 0, false)
	if got := service.ExtractBillingSnapshotField(snapshot, "stageBillingModelCallCount"); got != "22" {
		t.Fatalf("full call count must remain available, got %q", got)
	}
	if got := service.ExtractBillingSnapshotField(snapshot, "stageBillingCallsOmitted"); got != "2" {
		t.Fatalf("expected two omitted calls, got %q", got)
	}
	last := service.ExtractBillingSnapshotField(snapshot, "stageBillingCall20")
	if !strings.Contains(last, ",inputTokens=1019,cachedInputTokens=19,") {
		t.Fatalf("last displayed call lost token breakdown: %q", last)
	}
	for _, key := range []string{"stageBillingCall21", "stageBillingCall22"} {
		if got := service.ExtractBillingSnapshotField(snapshot, key); got != "" {
			t.Fatalf("call above display limit must be omitted, got %s=%q", key, got)
		}
	}
}
