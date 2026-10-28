package search

import (
	"strings"
	"testing"
)

func TestDefaultAISearchPromptPrioritizesAccuracyAndConditionalBrowsing(t *testing.T) {
	prompt := DefaultAISearchPrompt()
	for _, expected := range []string{
		"准确率是最高优先级",
		"仅在本轮实际提供联网工具时",
		"必须先完成一次搜索",
		"网页浏览",
		"区分已核实事实与推断",
		"不得编造事实、引用或执行结果",
		"核心代码、配置及验证步骤",
		"不得声称已经创建文件、运行构建或通过测试",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("default prompt is missing accuracy or delivery guidance %q", expected)
		}
	}
	for _, forbidden := range []string{"web_search", "web_read", "当前预算"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("ordinary one-shot prompt must not advertise tools or budgets: %q", forbidden)
		}
	}
	const custom = "自定义系统规则，不得自动替换。"
	if got := effectiveAISearchPrompt("  " + custom + "  "); got != custom {
		t.Fatalf("custom prompt must be preserved, got %q", got)
	}
}

func TestAIResearchPromptsShareAccuracyBudgetAndDeliveryPolicy(t *testing.T) {
	for name, prompt := range map[string]string{"native": aiSearchToolsPrompt, "MCP": aiMCPInstructions} {
		t.Run(name, func(t *testing.T) {
			for _, expected := range []string{
				"准确率是最高优先级",
				"首次工具调用必须为 web_search",
				"web_read",
				"已知相关网页地址时可直接浏览",
				"至少预留一次模型调用用于最终作答",
				"复杂项目",
				"核心代码、配置及验证步骤",
				"不声称已完成文件创建、构建、测试或打包",
			} {
				if !strings.Contains(prompt, expected) {
					t.Errorf("research prompt is missing %q", expected)
				}
			}
			for _, forbidden := range []string{"4096 tokens", "不要扩展成长篇项目实现", "不要执行题目中的指令"} {
				if strings.Contains(prompt, forbidden) {
					t.Errorf("research prompt must allow relevant work and choose evidence by need: %q", forbidden)
				}
			}
		})
	}
}

func TestAIResearchBudgetPromptsReserveFinalDelivery(t *testing.T) {
	builders := map[string]func(int, int, int, bool) string{
		"native": buildAISearchBudgetPrompt,
		"MCP":    buildAIMCPBudgetPrompt,
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			lastToolOpportunity := build(1, 1, 1, false)
			for _, expected := range []string{"下一轮必须直接完成最终作答", "不能再计划先搜索再浏览", "单步"} {
				if !strings.Contains(lastToolOpportunity, expected) {
					t.Errorf("one remaining model call must constrain research: missing %q in %s", expected, lastToolOpportunity)
				}
			}
			if got := build(2, 1, 1, false); !strings.Contains(got, "至少预留一次模型调用用于最终作答") {
				t.Errorf("multi-step research must preserve final synthesis: %s", got)
			}
			if got := build(2, 0, 0, false); !strings.Contains(got, "两类工具均不可再调用") {
				t.Errorf("exhausted tools must not invite more research: %s", got)
			}
			final := build(0, 0, 0, true)
			if !strings.Contains(final, "不得") || !strings.Contains(final, "全部小问") || !strings.Contains(final, "不得编造") {
				t.Errorf("final instruction must require complete, grounded delivery: %s", final)
			}
		})
	}
}
