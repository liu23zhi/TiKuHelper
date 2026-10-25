package controller

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestMoneyPrecisionRegressionQuotaMicroBoundaries(t *testing.T) {
	// Pure quota consumers: no database, global stubs or provider requests.
	cases := []struct {
		name                      string
		limit, used, charge       float64
		wantExhausted, wantExceed bool
	}{
		{"unlimited_zero_quota", 0, 1, 1, false, false},
		{"unused_sub_cent_quota", 0.000063, 0, 0.000062, false, false},
		{"exact_last_micro_allowed", 0.000063, 0.000062, 0.000001, false, false},
		{"one_micro_over_rejected", 0.000063, 0.000062, 0.000002, false, true},
		{"at_limit_positive_charge", 0.000063, 0.000063, 0.000001, true, true},
		{"at_limit_zero_charge", 0.000063, 0.000063, 0, true, false},
		{"below_rounding_boundary", 0.0000634, 0.0000624, 0.000001, false, false},
		{"above_rounding_boundary", 0.0000634, 0.0000624, 0.0000012, false, true},
		{"same_rounded_micro_is_exhausted", 0.0000634, 0.0000626, 0.0000001, true, false},
		{"one_yuan_exact_last_micro", 1, 0.999999, 0.000001, false, false},
		{"one_yuan_one_micro_over", 1, 0.999999, 0.000002, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := entity.AccessToken{TotalQuotaLimit: tc.limit, TotalAmountUsed: tc.used}
			if got := accessTokenQuotaExhausted(token); got != tc.wantExhausted {
				t.Fatalf("quota exhausted (used=%.9f limit=%.9f) = %t, want %t", tc.used, tc.limit, got, tc.wantExhausted)
			}
			if got := accessTokenQuotaWouldExceed(token, tc.charge); got != tc.wantExceed {
				t.Fatalf("quota exceed (used=%.9f charge=%.9f limit=%.9f) = %t, want %t", tc.used, tc.charge, tc.limit, got, tc.wantExceed)
			}
		})
	}
}

func TestMoneyPrecisionRegressionAIUsageMicroCharges(t *testing.T) {
	cases := []struct {
		name      string
		unitPrice float64
		usage     searchpkg.AISearchUsage
		want      float64
	}{
		{"fresh_input_micro", 1, searchpkg.AISearchUsage{InputTokens: 1}, 0.000001},
		{"cached_input_micro", 1, searchpkg.AISearchUsage{InputTokens: 1, CachedInputTokens: 1}, 0.000001},
		{"cache_write_micro", 1, searchpkg.AISearchUsage{CacheWriteTokens: 1}, 0.000001},
		{"output_micro", 1, searchpkg.AISearchUsage{OutputTokens: 1}, 0.000001},
		{"sub_cent_consumption", 62, searchpkg.AISearchUsage{InputTokens: 1}, 0.000062},
		{"half_micro_rounds_away_from_zero", 0.5, searchpkg.AISearchUsage{InputTokens: 1}, 0.000001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			price := service.ResolvedAIModelTokenPrice{
				InputUnitPrice:      tc.unitPrice,
				CachedUnitPrice:     tc.unitPrice,
				CacheWriteUnitPrice: tc.unitPrice,
				OutputUnitPrice:     tc.unitPrice,
			}
			_, amount := calculateAIModelPriceTableAmount(price, tc.usage)
			if amount != tc.want {
				t.Fatalf("one-token amount = %.9f, want %.9f", amount, tc.want)
			}
		})
	}
}
