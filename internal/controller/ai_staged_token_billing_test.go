package controller

import (
	"encoding/json"
	"math"
	"testing"

	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestCalculateAIStagedTokenMultiplierUsesFixedInclusiveBands(t *testing.T) {
	tests := []struct {
		tokens int64
		want   float64
	}{
		{tokens: 0, want: 4},
		{tokens: 2999, want: 4},
		{tokens: 3000, want: 2},
		{tokens: 9999, want: 2},
		{tokens: 10000, want: 1},
		{tokens: 29999, want: 1},
		{tokens: 30000, want: 0.65},
		{tokens: 99999, want: 0.65},
		{tokens: 100000, want: 0.3},
		{tokens: 250000, want: 0.3},
	}

	for _, test := range tests {
		if got := calculateAIStagedTokenMultiplier(test.tokens, false); got != test.want {
			t.Errorf("tokens=%d: got %.6f, want %.6f", test.tokens, got, test.want)
		}
	}
}

func TestCalculateAIStagedTokenMultiplierSmoothlyInterpolatesAndClamps(t *testing.T) {
	tests := []struct {
		tokens int64
		want   float64
	}{
		{tokens: 0, want: 4},
		{tokens: 1500, want: 4},
		{tokens: 2999, want: 4},
		{tokens: 3000, want: 2},
		{tokens: 6500, want: 1.5},
		{tokens: 10000, want: 1},
		{tokens: 20000, want: 0.825},
		{tokens: 30000, want: 0.65},
		{tokens: 65000, want: 0.475},
		{tokens: 100000, want: 0.3},
		{tokens: 150000, want: 0.3},
	}

	previous := 4.0
	for _, test := range tests {
		got := calculateAIStagedTokenMultiplier(test.tokens, true)
		if got != test.want {
			t.Errorf("tokens=%d: got %.6f, want %.6f", test.tokens, got, test.want)
		}
		if got > previous {
			t.Errorf("multiplier must not increase: tokens=%d previous=%.6f got=%.6f", test.tokens, previous, got)
		}
		if got < 0.3 || got > 4 {
			t.Errorf("multiplier out of bounds: tokens=%d multiplier=%.6f", test.tokens, got)
		}
		previous = got
	}
}

func TestCalculateAIStagedTokenMultiplierUsesCustomPoints(t *testing.T) {
	points := []service.AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 4},
		{Tokens: 5000, Multiplier: 2},
		{Tokens: 100000, Multiplier: 0.3},
	}
	if got := calculateAIStagedTokenMultiplierWithPoints(4999, false, points); got != 4 {
		t.Fatalf("expected fixed multiplier 4 before custom threshold, got %v", got)
	}
	if got := calculateAIStagedTokenMultiplierWithPoints(5000, false, points); got != 2 {
		t.Fatalf("expected custom threshold multiplier 2, got %v", got)
	}
	if got := calculateAIStagedTokenMultiplierWithPoints(2500, true, points); got != 4 {
		t.Fatalf("expected first node multiplier to remain constant before the second node, got %v", got)
	}
	if got := calculateAIStagedTokenMultiplierWithPoints(52500, true, points); got != 1.15 {
		t.Fatalf("expected smooth custom multiplier 1.15, got %v", got)
	}
}

func TestCalculateAIStagedTokenMultiplierDoesNotClampCustomValues(t *testing.T) {
	points := []service.AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 12.5},
		{Tokens: 2_000_000_000, Multiplier: 12.5},
		{Tokens: 9_000_000_000, Multiplier: -3.25},
	}
	if got := calculateAIStagedTokenMultiplierWithPoints(5_500_000_000, false, points); got != 12.5 {
		t.Fatalf("expected fixed mode to preserve custom multiplier above 4x, got %v", got)
	}
	if got := calculateAIStagedTokenMultiplierWithPoints(5_500_000_000, true, points); got != 4.625 {
		t.Fatalf("expected smooth mode to interpolate custom multiplier range without clamping, got %v", got)
	}
	if got := calculateAIStagedTokenMultiplierWithPoints(9_000_000_000, true, points); got != -3.25 {
		t.Fatalf("expected smooth tail to preserve custom negative multiplier, got %v", got)
	}
}

func TestCalculateAIStagedTokenMultiplierInterpolatesFiniteExtremesWithoutOverflow(t *testing.T) {
	points := []service.AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: math.MaxFloat64},
		{Tokens: 1, Multiplier: math.MaxFloat64},
		{Tokens: 3, Multiplier: -math.MaxFloat64},
	}
	if got := calculateAIStagedTokenMultiplierWithPoints(2, true, points); got != 0 {
		t.Fatalf("expected midpoint interpolation between finite extremes to be zero, got %v", got)
	}
}

func TestCalculateAIStagedTokenMultiplierKeepsTailNodeMultiplierConstant(t *testing.T) {
	points := []service.AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 4},
		{Tokens: 3000, Multiplier: 2},
		{Tokens: 80000, Multiplier: 0.3},
	}
	for _, tokens := range []int64{80000, 100000, 250000} {
		if got := calculateAIStagedTokenMultiplierWithPoints(tokens, true, points); got != 0.3 {
			t.Errorf("tokens=%d: expected the final node multiplier to stay constant, got %v", tokens, got)
		}
	}
}

func TestCalculateAIStagedTokenBillingPricesEachModelResponseBeforeRounding(t *testing.T) {
	price := service.ResolvedAIModelTokenPrice{
		ModelName:           "model-a",
		Multiplier:          2,
		InputUnitPrice:      1,
		CachedUnitPrice:     0.5,
		CacheWriteUnitPrice: 1.5,
		OutputUnitPrice:     2,
	}
	usage := searchpkg.AISearchUsage{ModelCalls: []searchpkg.AIModelCallUsage{
		{Index: 1, Model: "model-a", InputTokens: 1_000_000, TotalTokens: 3_000},
		{Index: 2, Model: "model-a", InputTokens: 2_000_000, CachedInputTokens: 500_000, CacheWriteTokens: 100_000, OutputTokens: 1_000_000, TotalTokens: 99_999},
	}}

	got := calculateAIStagedTokenBilling(price, usage, service.AIStagedTokenBillingSettings{Enabled: true}, true)
	// First call: base 1.0 * 2x. Second call: 1.5 + 0.25 + 0.15 + 2 = 3.9 * 0.65x.
	if got.BaseAmount != 4.9 || got.Amount != 4.535 {
		t.Fatalf("expected unrounded total base/staged amount 4.9/4.535, got %v/%v", got.BaseAmount, got.Amount)
	}
	if len(got.Calls) != 2 {
		t.Fatalf("expected one explanation per model response, got %+v", got.Calls)
	}
	if got.Calls[0].StageMultiplier != 2 || got.Calls[0].FinalMultiplier != 4 {
		t.Fatalf("expected price multiplier * stage multiplier for first response, got %+v", got.Calls[0])
	}
	if got.Calls[1].StageMultiplier != 0.65 || got.Calls[1].FinalMultiplier != 1.3 {
		t.Fatalf("expected price multiplier * stage multiplier for second response, got %+v", got.Calls[1])
	}
	if got.RoundedAmount != 4.535 {
		t.Fatalf("expected final rounding once at money precision, got %v", got.RoundedAmount)
	}
}

func TestCalculateAIStagedTokenBillingUsesOneAggregateFallbackWhenCallDetailsMissing(t *testing.T) {
	price := service.ResolvedAIModelTokenPrice{ModelName: "model-a", Multiplier: 1.5, InputUnitPrice: 2}
	usage := searchpkg.AISearchUsage{InputTokens: 1_000_000, TotalTokens: 3_000}
	got := calculateAIStagedTokenBilling(price, usage, service.AIStagedTokenBillingSettings{Enabled: true}, true)
	if got.BaseAmount != 2 || got.Amount != 4 || got.RoundedAmount != 4 || len(got.Calls) != 1 {
		t.Fatalf("expected one backwards-compatible aggregate call, got %+v", got)
	}
	if call := got.Calls[0]; call.Amount != 4 || call.StageMultiplier != 2 || call.FinalMultiplier != 3 || call.CallIndex != 0 {
		t.Fatalf("expected aggregate fallback explanation, got %+v", call)
	}
}

func TestCalculateAIStagedTokenBillingKeepsCallsUnpricedAtZero(t *testing.T) {
	price := service.ResolvedAIModelTokenPrice{Multiplier: 3}
	got := calculateAIStagedTokenBilling(price, searchpkg.AISearchUsage{ModelCalls: []searchpkg.AIModelCallUsage{{Index: 1, Model: "unpriced", TotalTokens: 1000}}}, service.AIStagedTokenBillingSettings{Enabled: true}, false)
	if got.BaseAmount != 0 || got.RoundedAmount != 0 {
		t.Fatalf("unpriced call must remain zero, got %+v", got)
	}
	if len(got.Calls) != 1 || got.Calls[0].Applicable {
		t.Fatalf("expected non-applicable explanation for zero-price call, got %+v", got.Calls)
	}
}

func TestApplyAIStagedBillingToSearchAuditRefreshesRawResponse(t *testing.T) {
	usage := searchpkg.AISearchUsage{
		Trace: []searchpkg.AISearchTrace{{
			Stage:          "model_response",
			ModelCallIndex: 1,
			InputTokens:    1000,
			TotalTokens:    1000,
		}},
	}
	billing := calculateAIStagedTokenBilling(
		service.ResolvedAIModelTokenPrice{ModelName: "model-a", Multiplier: 1, InputUnitPrice: 1},
		searchpkg.AISearchUsage{ModelCalls: []searchpkg.AIModelCallUsage{{Index: 1, Model: "model-a", InputTokens: 1000, TotalTokens: 1000}}},
		service.AIStagedTokenBillingSettings{Enabled: true},
		true,
	)
	audit := searchAuditMetadata{RawResponse: `{"status":"success"}`}
	applyAIStagedBillingToSearchAudit(&audit, usage.Trace, []searchBillingCharge{{
		Provider:        searchModeAI,
		AIStagedBilling: &billing,
		AIPrice:         service.ResolvedAIModelTokenPrice{ModelName: "model-a", Multiplier: 1},
		AIPriceMatched:  true,
		AIStageSettings: service.AIStagedTokenBillingSettings{Enabled: true},
	}})

	var payload struct {
		AISearchTrace []searchpkg.AISearchTrace `json:"aiSearchTrace"`
	}
	if err := json.Unmarshal([]byte(audit.RawResponse), &payload); err != nil {
		t.Fatalf("unmarshal refreshed audit response: %v", err)
	}
	if len(payload.AISearchTrace) != 1 || payload.AISearchTrace[0].StageMultiplier != 4 || !payload.AISearchTrace[0].MultiplierApplicable {
		t.Fatalf("expected final multiplier in refreshed audit response, got %+v", payload.AISearchTrace)
	}
}
