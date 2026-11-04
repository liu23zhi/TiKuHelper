package controller

import (
	"strings"

	"github.com/itihey/tikuAdapter/internal/search"
)

type aiBillingSequence struct {
	Models       map[int]int
	Searches     []int
	WebReads     []int
	TikuSearches []int
}

// buildAIBillingSequence maps billed model/tool calls to their chronological
// position in the AI trace. Trace order is the source of truth because charge
// records can otherwise be grouped by provider instead of actual execution.
func buildAIBillingSequence(usage search.AISearchUsage) aiBillingSequence {
	sequence := aiBillingSequence{Models: make(map[int]int)}
	for index, trace := range usage.Trace {
		position := index + 1
		stage := strings.TrimSpace(trace.Stage)
		if strings.Contains(stage, "模型响应") {
			modelCallIndex := trace.ModelCallIndex
			if modelCallIndex <= 0 {
				modelCallIndex = trace.Round
			}
			if modelCallIndex > 0 {
				if _, exists := sequence.Models[modelCallIndex]; !exists {
					sequence.Models[modelCallIndex] = position
				}
			}
		}
		if strings.Contains(stage, "搜索 API 请求开始") {
			sequence.Searches = append(sequence.Searches, position)
		}
		if strings.Contains(stage, "网页读取 API 请求开始") {
			sequence.WebReads = append(sequence.WebReads, position)
		}
		if strings.Contains(stage, "本地题库检索开始") || stage == "本地题库检索未执行" {
			sequence.TikuSearches = append(sequence.TikuSearches, position)
		}
	}
	return sequence
}

func applyAIModelBillingSequence(calls []aiStagedTokenBillingCall, sequence aiBillingSequence) {
	for index := range calls {
		calls[index].Sequence = sequence.Models[calls[index].CallIndex]
	}
}

func sequenceAt(sequence []int, callIndex int) int {
	if callIndex <= 0 || callIndex > len(sequence) {
		return 0
	}
	return sequence[callIndex-1]
}
