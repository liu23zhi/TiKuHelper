package util

import "testing"

// 线上真实污染样本：编辑器脚本 + 工具栏前缀必须被清掉，真实内容必须完整保留。
//
// 这两条数据直接取自生产库 tiku.options（id 680 / 681）。
func TestSanitizeOptionsForAIOnRealPollutedSamples(t *testing.T) {
	options := []string{
		"}else if ((max > 0 && count > max) || (min > 0 && min > count)) {",
		"段落格式字体字号点击上传x我列举的常见科技发明有电话、电视、电脑、互联网、智能手机、汽车、飞机和机器人.我课后可以认真学习相关内容,及时复习并主动查阅资料,巩固所学知识",
	}

	cleaned := SanitizeOptionsForAI(options)

	if len(cleaned) != 1 {
		t.Fatalf("expected exactly one real option to survive, got %d: %v", len(cleaned), cleaned)
	}
	if cleaned[0] != "我列举的常见科技发明有电话、电视、电脑、互联网、智能手机、汽车、飞机和机器人.我课后可以认真学习相关内容,及时复习并主动查阅资料,巩固所学知识" {
		t.Fatalf("unexpected cleaned option: %q", cleaned[0])
	}
}

// 真实选项（含中文的伪脚本词、公式、数学式）绝不能被误删。
func TestSanitizeOptionsForAIKeepsRealOptions(t *testing.T) {
	options := []string{
		"A. 正确",
		"B. 错误",
		"C. 词语解释",
		"D. 以上都对",
		"f(x) = 2x + 1",
		"变量 var 的值",
		"if 条件成立",
		"输出结果为 1；",
		"3x + 2",
		"(1, 2)",
	}

	cleaned := SanitizeOptionsForAI(options)

	if len(cleaned) != len(options) {
		t.Fatalf("real options must not be dropped: %d -> %d (%v)", len(options), len(cleaned), cleaned)
	}
	for i := range options {
		if cleaned[i] != options[i] {
			t.Fatalf("option %d was altered: %q -> %q", i, options[i], cleaned[i])
		}
	}
}

// 全是噪音时必须回退为原始列表，绝不能返回空。
func TestSanitizeOptionsForAIFallsBackWhenEverythingLooksLikeNoise(t *testing.T) {
	options := []string{
		"var a = 1;",
		`$( "#x" ).removeClass("y");`,
		"{}",
	}

	cleaned := SanitizeOptionsForAI(options)

	if len(cleaned) != len(options) {
		t.Fatalf("expected a fallback to the raw list, got %d items: %v", len(cleaned), cleaned)
	}
}

// 重复项去重（编辑器噪音常整段重复）。
func TestSanitizeOptionsForAIDeduplicates(t *testing.T) {
	options := []string{"A. 正确", "A. 正确", "B. 错误"}

	cleaned := SanitizeOptionsForAI(options)

	if len(cleaned) != 2 {
		t.Fatalf("expected duplicates to collapse, got %v", cleaned)
	}
}

// 空输入与单元素输入不应 panic。
func TestSanitizeOptionsForAIHandlesEdgeCases(t *testing.T) {
	if got := SanitizeOptionsForAI(nil); len(got) != 0 {
		t.Fatalf("expected nil to stay empty, got %v", got)
	}
	if got := SanitizeOptionsForAI([]string{}); len(got) != 0 {
		t.Fatalf("expected empty to stay empty, got %v", got)
	}
	single := []string{"只有一个选项"}
	if got := SanitizeOptionsForAI(single); len(got) != 1 || got[0] != single[0] {
		t.Fatalf("expected a single real option to pass through, got %v", got)
	}
}

// 编辑器强特征：即使行里含中文也要丢弃（那是源码里的中文注释/文案）。
func TestIsEditorScriptOptionDropsStrongEditorFeatures(t *testing.T) {
	noise := []string{
		`window.UEDITOR_CONFIG.wordCount = false;`,
		`$("#answer406157359").parent().find(".edui-editor").removeClass("borblue");`,
		`editor1.addListener('contentChange', function() {`,
		`var count = UE.getEditor("answer406157359").pureWordCount();`,
		"}else if ((max > 0 && count > max) || (min > 0 && min > count)) {",
		"{}",
		"// var wordCount = false;",
	}
	for _, option := range noise {
		if !IsEditorScriptOption(option) {
			t.Fatalf("expected %q to be treated as editor noise", option)
		}
	}
}

// 弱特征（var/if/else 开头）只在**不含中文**时才判为噪音。
func TestIsEditorScriptOptionKeepsChineseContentStartingWithKeywords(t *testing.T) {
	content := []string{
		"if 条件成立",
		"变量 var 的值",
		"else 分支的结果",
		"return 语句的作用",
	}
	for _, option := range content {
		if IsEditorScriptOption(option) {
			t.Fatalf("expected Chinese content %q to be kept", option)
		}
	}
}

// 剥外壳：只有确实剥掉了内容才生效，剥完过短时保留原文。
func TestStripEditorOptionChrome(t *testing.T) {
	cases := map[string]string{
		"段落格式字体字号点击上传x我列举的常见科技发明有电话、电视": "我列举的常见科技发明有电话、电视",
		"我列举的常见科技发明有电话、电视和电脑等":          "我列举的常见科技发明有电话、电视和电脑等",
		"段落格式字体字号": "段落格式字体字号", // 剥完过短 → 保留原文
	}
	for input, want := range cases {
		if got := StripEditorOptionChrome(input); got != want {
			t.Fatalf("StripEditorOptionChrome(%q) = %q, want %q", input, got, want)
		}
	}
}
