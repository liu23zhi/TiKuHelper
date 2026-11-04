package controller

import (
	"strings"
	"testing"

	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestLocalTikuBillingSequenceSkipsInvalidAttemptsWithoutLosingOrder(t *testing.T) {
	db := newSearchControllerTestDB(t)
	usage := searchpkg.AISearchUsage{
		TikuSearchCalls: []searchpkg.AITikuSearchCallUsage{{Status: "error"}, {Status: "success", Query: "有效题目", MatchKind: "none"}},
		Trace:           []searchpkg.AISearchTrace{{Stage: "本地题库检索未执行"}, {Stage: "模型响应", ModelCallIndex: 1}, {Stage: "本地题库检索开始"}},
	}
	charges := appendAIToolBillingCharges(nil, db, model.SearchRequest{Question: "有效题目"}, usage)
	if len(charges) != 1 || charges[0].Provider != "tiku_search" || !strings.Contains(charges[0].Snapshot, "feeSequence=3") || !strings.Contains(charges[0].Snapshot, "tikuSearchCall=2") {
		t.Fatalf("invalid attempt misaligned successful local billing: %+v", charges)
	}
}
