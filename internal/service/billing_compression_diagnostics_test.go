package service

import (
	"strings"
	"testing"
)

func TestCompressionDiagnosticBillingProjectNames(t *testing.T) {
	for _, test := range []struct{ mode, tool, target, want string }{
		{"compression", "remote", "历史上下文", "AI 历史压缩测试：历史上下文"},
		{"ai_compression", "web_search", "OpenAI", "AI 工具调用压缩测试（联网搜索）：OpenAI"},
		{"ai_compression", "web_read", "https://example.com", "AI 工具调用压缩测试（网页浏览）：https://example.com"},
		{"ai_compression", "both", "OpenAI", "AI 工具调用压缩测试（上下文压缩 + 联网搜索 + 网页浏览）：OpenAI"},
	} {
		got := BuildAIToolTestBillingQuestion(test.mode, test.tool, test.target)
		if got != test.want {
			t.Errorf("mode=%s tool=%s got=%q want=%q", test.mode, test.tool, got, test.want)
		}
		if strings.Contains(got, "ai_compression") || strings.Contains(got, "web_") {
			t.Fatalf("raw tool identifiers leaked into project name: %q", got)
		}
	}
}
