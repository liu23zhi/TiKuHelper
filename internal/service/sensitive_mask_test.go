package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// 遮挡逻辑的契约测试。
//
// 这些断言必须**只看遮挡结果**，不看实现：判断标准是
// 「返回值里到底还有没有原文的字符」，而不是「函数被调用了几次」。

// isMeaningfulRune 判断这个字符是否该被视为「原文证据」。
//
// 标点与空白不算：占位模板自身就带全角括号/冒号，
// 用它们当泄露判据会产生永远消不掉的假失败。
func isMeaningfulRune(char rune) bool {
	switch {
	case char >= '0' && char <= '9':
		return true
	case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z':
		return true
	case char >= 0x4E00 && char <= 0x9FFF: // CJK 统一汉字
		return true
	}
	return false
}

func TestBuildSensitiveMaskedContentLeaksNothing(t *testing.T) {
	const original = "这道题的答案是：操你妈的"
	masked := BuildSensitiveMaskedContent(original)
	if masked.Empty {
		t.Fatalf("非空原文不该标记为空")
	}
	if masked.Runes != 12 {
		t.Fatalf("字符数必须按 rune 计（中英混排 12 个字符），实际 %d", masked.Runes)
	}
	// 逐字符检查：遮挡结果不得包含原文的任何**实义字符**（汉字/字母/数字）。
	// 标点不参与：占位模板自己就带「：」这类全角标点，它不是原文证据。
	for _, char := range original {
		if !isMeaningfulRune(char) {
			continue
		}
		if strings.ContainsRune(masked.Masked, char) {
			t.Fatalf("遮挡结果泄露了原文字符 %q：%q", string(char), masked.Masked)
		}
	}
	if !strings.Contains(masked.Masked, "12") {
		t.Fatalf("必须保留可读长度提示（共 12 字）：%q", masked.Masked)
	}
}

func TestBuildSensitiveMaskedContentCountsRunesNotBytes(t *testing.T) {
	// 「敏」(3 字节) × 3 = 9 字节 / 3 字符。按 len() 会算成 9。
	masked := BuildSensitiveMaskedContent("敏感词")
	if masked.Runes != 3 {
		t.Fatalf("3 个汉字必须算 3 字，实际 %d（用 len() 会得到 9）", masked.Runes)
	}
}

func TestBuildSensitiveMaskedContentHandlesEmptyAndBlank(t *testing.T) {
	for _, value := range []string{"", "   ", "\n\t "} {
		masked := BuildSensitiveMaskedContent(value)
		if !masked.Empty {
			t.Fatalf("%q 应标记为 empty", value)
		}
		if masked.Runes != 0 {
			t.Fatalf("%q 的字符数必须是 0，实际 %d", value, masked.Runes)
		}
		if masked.Masked == "" {
			t.Fatalf("%q 仍必须给出占位文本，否则前端会显示成空白", value)
		}
	}
}

func TestMaskSensitiveWordsReplacesOnlyHitWords(t *testing.T) {
	text := "这个答案包含敏感词和另一个词"
	masked := MaskSensitiveWords(text, []string{"敏感词"})
	if strings.Contains(masked, "敏感词") {
		t.Fatalf("命中词必须被替换：%q", masked)
	}
	// 上下文必须留下：这是「局部遮挡」与「整段遮挡」的区别。
	for _, kept := range []string{"这个答案包含", "和另一个词"} {
		if !strings.Contains(masked, kept) {
			t.Fatalf("局部遮挡必须保留上下文 %q：%q", kept, masked)
		}
	}
	// 长度不变（3 个字符 → 3 个遮挡符），排版不跳动。
	if len([]rune(masked)) != len([]rune(text)) {
		t.Fatalf("遮挡后字符数必须不变：before=%d after=%d", len([]rune(text)), len([]rune(masked)))
	}
}

func TestMaskSensitiveWordsReplacesLongestFirst(t *testing.T) {
	// 短的先换会把「敏感词」切成「＊＊＊词」，留下可读碎片。
	text := "这是敏感词内容"
	masked := MaskSensitiveWords(text, []string{"敏感", "敏感词"})
	if strings.Contains(masked, "词") {
		t.Fatalf("必须按长度从长到短替换，避免残留可读碎片：%q", masked)
	}
	if !strings.Contains(masked, "这是") || !strings.Contains(masked, "内容") {
		t.Fatalf("上下文必须保留：%q", masked)
	}
}

func TestMaskSensitiveWordsIsIdempotent(t *testing.T) {
	text := "这句话里有敏感词"
	once := MaskSensitiveWords(text, []string{"敏感词"})
	twice := MaskSensitiveWords(once, []string{"敏感词"})
	if once != twice {
		t.Fatalf("重复遮挡必须幂等（前端可能再遮一遍）：once=%q twice=%q", once, twice)
	}
}

func TestMaskSensitiveWordsWithoutWordsReturnsTextUnchanged(t *testing.T) {
	// 没有命中明细时**绝不猜测性遮挡**：那是「误标」。
	for _, words := range [][]string{nil, {}, {"", "  "}} {
		if got := MaskSensitiveWords("原文内容", words); got != "原文内容" {
			t.Fatalf("没有可用命中词时必须原样返回，words=%v got=%q", words, got)
		}
	}
}

func TestMaskSensitiveWordsInJSONKeepsStructure(t *testing.T) {
	raw := `{"status":"success","answer":{"answerText":"包含敏感词的答案"},"durationMs":1234}`
	masked := MaskSensitiveWordsInJSON(raw, []string{"敏感词"})
	if strings.Contains(masked, "敏感词") {
		t.Fatalf("JSON 里的命中词必须被替换：%q", masked)
	}
	// 结构必须完好：前端要靠 status / durationMs 派生状态与耗时列。
	var decoded map[string]any
	if err := json.Unmarshal([]byte(masked), &decoded); err != nil {
		t.Fatalf("遮挡后必须仍是合法 JSON：%v (%q)", err, masked)
	}
	if decoded["status"] != "success" {
		t.Fatalf("非敏感字段不能被改动：%v", decoded["status"])
	}
	if decoded["durationMs"] != float64(1234) {
		t.Fatalf("数字必须保持数值类型与原值：%v (%T)", decoded["durationMs"], decoded["durationMs"])
	}
	answer, _ := decoded["answer"].(map[string]any)
	if answer == nil || !strings.Contains(answer["answerText"].(string), "包含") {
		t.Fatalf("字符串值的上下文必须保留：%v", decoded["answer"])
	}
}

func TestMaskSensitiveWordsInJSONLeavesUntouchedJSONByteIdentical(t *testing.T) {
	// 没有命中词时不能重新序列化：那会制造无谓的格式漂移（空格、键序）。
	raw := `{"status":"clean","b":2,"a":1}`
	if got := MaskSensitiveWordsInJSON(raw, []string{"敏感词"}); got != raw {
		t.Fatalf("无命中时响应必须逐字节不变：got=%q want=%q", got, raw)
	}
}

func TestMaskSensitiveWordsInJSONFallsBackForNonJSON(t *testing.T) {
	// 历史行的快照可能是纯文本：不能因为不是 JSON 就放行原文。
	got := MaskSensitiveWordsInJSON("普通文本里也有敏感词", []string{"敏感词"})
	if strings.Contains(got, "敏感词") {
		t.Fatalf("非 JSON 文本同样必须遮挡：%q", got)
	}
}

func TestMaskSensitiveWordsInJSONDoesNotMaskKeys(t *testing.T) {
	// 键名被遮坏会让前端整列解析失败（例如把 "status" 遮掉）。
	raw := `{"sensitiveField":"值"}`
	got := MaskSensitiveWordsInJSON(raw, []string{"sensitive"})
	if !strings.Contains(got, `"sensitiveField"`) {
		t.Fatalf("键名必须原样保留：%q", got)
	}
}

func TestSensitiveWordListFromScopesOnlyTakesForbidden(t *testing.T) {
	scopes := []SensitiveWordScopeResult{
		{Scope: "question", Forbidden: false, Words: []string{"不该出现"}},
		{Scope: "answer", Forbidden: true, Words: []string{" 敏感词 ", "敏感词", ""}},
	}
	words := SensitiveWordListFromScopes(scopes)
	if len(words) != 1 || words[0] != "敏感词" {
		t.Fatalf("只取命中分组并去重去空白，实际 %#v", words)
	}
}

func TestParseSensitiveWordDetailJSONToleratesGarbage(t *testing.T) {
	if got := ParseSensitiveWordDetailJSON(""); got != nil {
		t.Fatalf("空串必须返回 nil，实际 %#v", got)
	}
	if got := ParseSensitiveWordDetailJSON("not-json"); got != nil {
		t.Fatalf("脏数据必须静默返回 nil（历史行格式不可控），实际 %#v", got)
	}
	parsed := ParseSensitiveWordDetailJSON(`[{"scope":"answer","forbidden":true,"words":["敏感"]}]`)
	if len(parsed) != 1 || !parsed[0].Forbidden || parsed[0].Words[0] != "敏感" {
		t.Fatalf("合法明细必须解析出来，实际 %#v", parsed)
	}
}
