package controller

import (
	"fmt"
	"strconv"
	"strings"

	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// aiStagedTokenBillingCall explains one token-bearing upstream model response.
// CallIndex is zero only for the compatibility aggregate fallback, where the
// old caller did not provide per-response usage details.
type aiStagedTokenBillingCall struct {
	CallIndex         int
	Model             string
	Round             int
	Attempt           int
	Sequence          int
	InputTokens       int64
	CachedInputTokens int64
	CacheWriteTokens  int64
	OutputTokens      int64
	TotalTokens       int64
	BaseAmount        float64
	Amount            float64
	StageMultiplier   float64
	FinalMultiplier   float64
	Applicable        bool
	AggregateFallback bool
}

type aiStagedTokenBillingResult struct {
	BaseAmount    float64
	Amount        float64
	RoundedAmount float64
	Calls         []aiStagedTokenBillingCall
}

func aiStagedTokenBillingMode(settings service.AIStagedTokenBillingSettings) string {
	if !settings.Enabled {
		return "disabled"
	}
	if settings.SmoothFactor {
		return "smooth"
	}
	return "fixed"
}

func calculateAIStagedTokenMultiplier(totalTokens int64, smooth bool) float64 {
	return calculateAIStagedTokenMultiplierWithPoints(totalTokens, smooth, service.DefaultAIStagedTokenMultiplierPoints())
}

func calculateAIStagedTokenMultiplierWithPoints(totalTokens int64, smooth bool, points []service.AIStagedTokenMultiplierPoint) float64 {
	if len(points) == 0 {
		points = service.DefaultAIStagedTokenMultiplierPoints()
	}
	if totalTokens < 0 {
		totalTokens = 0
	}
	if !smooth {
		for index := 1; index < len(points); index++ {
			if totalTokens < points[index].Tokens {
				return service.RoundAIStagedTokenMultiplier(points[index-1].Multiplier)
			}
		}
		return service.RoundAIStagedTokenMultiplier(points[len(points)-1].Multiplier)
	}

	// The first node is a flat-rate floor before the second node. The last node
	// is a flat-rate tail after its threshold; only interior ranges interpolate.
	if len(points) < 2 || totalTokens < points[1].Tokens {
		return service.RoundAIStagedTokenMultiplier(points[0].Multiplier)
	}
	last := points[len(points)-1]
	if totalTokens >= last.Tokens {
		return service.RoundAIStagedTokenMultiplier(last.Multiplier)
	}
	for index := 2; index < len(points); index++ {
		left := points[index-1]
		right := points[index]
		if totalTokens > right.Tokens {
			continue
		}
		if totalTokens == left.Tokens {
			return service.RoundAIStagedTokenMultiplier(left.Multiplier)
		}
		if totalTokens == right.Tokens {
			return service.RoundAIStagedTokenMultiplier(right.Multiplier)
		}
		ratio := float64(totalTokens-left.Tokens) / float64(right.Tokens-left.Tokens)
		return service.RoundAIStagedTokenMultiplier((1-ratio)*left.Multiplier + ratio*right.Multiplier)
	}

	return service.RoundAIStagedTokenMultiplier(last.Multiplier)
}

// calculateAIStagedTokenBilling prices each actual model response separately.
// Unit prices already contain the price-row multiplier, so only the stage
// multiplier is applied here. Intermediate amounts stay unrounded until the
// final billing-record boundary.
func calculateAIStagedTokenBilling(price service.ResolvedAIModelTokenPrice, usage searchpkg.AISearchUsage, settings service.AIStagedTokenBillingSettings, matched bool) aiStagedTokenBillingResult {
	calls := usage.ModelCalls
	if len(calls) == 0 {
		baseAmount, amount := calculateAIModelPriceTableAmount(price, usage)
		totalTokens := usage.TotalTokens
		if totalTokens == 0 {
			totalTokens = usage.InputTokens + usage.OutputTokens
		}
		stageMultiplier := 1.0
		if settings.Enabled && matched && totalTokens > 0 {
			stageMultiplier = calculateAIStagedTokenMultiplierWithPoints(totalTokens, settings.SmoothFactor, settings.MultiplierPoints)
			amount = util.RoundMoney(baseAmount * stageMultiplier)
		}
		return aiStagedTokenBillingResult{
			BaseAmount:    baseAmount,
			Amount:        baseAmount * stageMultiplier,
			RoundedAmount: amount,
			Calls: []aiStagedTokenBillingCall{{
				CallIndex:         0,
				Model:             strings.TrimSpace(price.ModelName),
				InputTokens:       usage.InputTokens,
				CachedInputTokens: usage.CachedInputTokens,
				CacheWriteTokens:  usage.CacheWriteTokens,
				OutputTokens:      usage.OutputTokens,
				TotalTokens:       totalTokens,
				BaseAmount:        baseAmount,
				Amount:            baseAmount * stageMultiplier,
				StageMultiplier:   stageMultiplier,
				FinalMultiplier:   price.Multiplier * stageMultiplier,
				Applicable:        matched && totalTokens > 0,
				AggregateFallback: true,
			}},
		}
	}

	result := aiStagedTokenBillingResult{Calls: make([]aiStagedTokenBillingCall, 0, len(calls))}
	for _, call := range calls {
		totalTokens := call.TotalTokens
		if totalTokens == 0 {
			totalTokens = call.InputTokens + call.OutputTokens
		}
		if totalTokens < 0 {
			totalTokens = 0
		}
		explanation := aiStagedTokenBillingCall{
			CallIndex:         call.Index,
			Model:             call.Model,
			Round:             call.Round,
			Attempt:           call.Attempt,
			InputTokens:       call.InputTokens,
			CachedInputTokens: call.CachedInputTokens,
			CacheWriteTokens:  call.CacheWriteTokens,
			OutputTokens:      call.OutputTokens,
			TotalTokens:       totalTokens,
		}
		if totalTokens <= 0 || !matched {
			result.Calls = append(result.Calls, explanation)
			continue
		}

		callUsage := searchpkg.AISearchUsage{
			InputTokens:       call.InputTokens,
			CachedInputTokens: call.CachedInputTokens,
			CacheWriteTokens:  call.CacheWriteTokens,
			OutputTokens:      call.OutputTokens,
			TotalTokens:       totalTokens,
		}
		baseAmount, _ := calculateAIModelPriceTableAmount(price, callUsage)
		stageMultiplier := 1.0
		if settings.Enabled {
			stageMultiplier = calculateAIStagedTokenMultiplierWithPoints(totalTokens, settings.SmoothFactor, settings.MultiplierPoints)
		}
		amount := baseAmount * stageMultiplier
		explanation.BaseAmount = baseAmount
		explanation.Amount = amount
		explanation.StageMultiplier = stageMultiplier
		explanation.FinalMultiplier = price.Multiplier * stageMultiplier
		explanation.Applicable = true
		result.BaseAmount += baseAmount
		result.Amount += amount
		result.Calls = append(result.Calls, explanation)
	}
	if !settings.Enabled {
		// Preserve the legacy aggregate calculation exactly while retaining a
		// per-response explanation with a neutral stage multiplier.
		result.BaseAmount, result.Amount = calculateAIModelPriceTableAmount(price, usage)
	}
	result.RoundedAmount = util.RoundMoney(result.Amount)
	return result
}

func decorateAIStagedBillingTrace(traces []searchpkg.AISearchTrace, billing aiStagedTokenBillingResult, price service.ResolvedAIModelTokenPrice, settings service.AIStagedTokenBillingSettings, matched bool) []searchpkg.AISearchTrace {
	decorated := append([]searchpkg.AISearchTrace(nil), traces...)
	mode := aiStagedTokenBillingMode(settings)
	for index := range decorated {
		trace := &decorated[index]
		if strings.TrimSpace(trace.Stage) == "" || (trace.InputTokens == 0 && trace.OutputTokens == 0 && trace.TotalTokens == 0) {
			continue
		}
		var explanation *aiStagedTokenBillingCall
		for callIndex := range billing.Calls {
			call := &billing.Calls[callIndex]
			if call.CallIndex > 0 && call.CallIndex == trace.ModelCallIndex {
				explanation = call
				break
			}
		}
		if explanation == nil && len(billing.Calls) == 1 && billing.Calls[0].CallIndex == 0 {
			explanation = &billing.Calls[0]
		}
		if explanation == nil {
			continue
		}
		trace.StageBillingMode = mode
		if explanation.AggregateFallback {
			trace.StageBillingMode = "aggregate_fallback_legacy"
		}
		if !matched {
			trace.StageBillingMode = "unpriced"
			trace.MultiplierApplicable = false
			continue
		}
		if !explanation.Applicable {
			trace.StageBillingMode = "not_applicable"
			trace.MultiplierApplicable = false
			continue
		}
		trace.StageMultiplier = explanation.StageMultiplier
		trace.FinalMultiplier = explanation.FinalMultiplier
		trace.MultiplierApplicable = true
	}
	return decorated
}

func formatAIStagedBillingMultiplierSummary(billing aiStagedTokenBillingResult) string {
	if len(billing.Calls) == 0 {
		return "none"
	}
	capacity := len(billing.Calls)
	if capacity > 20 {
		capacity = 20
	}
	parts := make([]string, 0, capacity)
	for index, call := range billing.Calls {
		if index >= 20 {
			parts = append(parts, "omitted="+strconv.Itoa(len(billing.Calls)-index))
			break
		}
		if !call.Applicable {
			if call.AggregateFallback && call.TotalTokens > 0 {
				parts = append(parts, fmt.Sprintf("call=aggregate stage=legacy final=%.4fx tokens=%d", call.FinalMultiplier, call.TotalTokens))
				continue
			}
			parts = append(parts, fmt.Sprintf("call=%d stage=na final=na tokens=%d", call.CallIndex, call.TotalTokens))
			continue
		}
		parts = append(parts, fmt.Sprintf("call=%d stage=%.4fx final=%.4fx tokens=%d", call.CallIndex, call.StageMultiplier, call.FinalMultiplier, call.TotalTokens))
	}
	return strings.Join(parts, ";")
}
