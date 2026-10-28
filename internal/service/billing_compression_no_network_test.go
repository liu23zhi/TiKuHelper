package service

import "testing"

func TestCompressionHistoryReadbackBillingProjectHasNoImplicitSearchTarget(t *testing.T) {
	for _, target := range []string{"", "OpenAI Responses API context compaction", "private history"} {
		if got := BuildAIToolTestBillingQuestion("ai_compression", "none", target); got != "AI 压缩历史回读测试" {
			t.Fatalf("history-only project must describe readback without a tool target: got=%q", got)
		}
	}
	for _, mode := range []string{"ai", "direct"} {
		if got := BuildAIToolTestBillingQuestion(mode, "none", "keyword"); got != "" {
			t.Fatalf("ordinary tool test accepted history-only strategy: mode=%q got=%q", mode, got)
		}
	}
}
