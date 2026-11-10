package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// 敏感内容的**展示遮挡**：把已经判定为敏感的文本变成可安全下发的形态。
//
// 与「检测打标」的分工（见同目录 sensitive_words.go）：
//   - 检测只写 logs 的四列标记，原文一个字都不改（红线：不影响答题链路）；
//   - 本文件**只做单向的展示变换**，由读取方决定采用与否：
//     TIKU Webui 的日志接口、前端 <SensitiveText>、PLUS 的 chaoxing 悬浮面板。
//
// 四条纪律：
//  1. **原文不留在任何返回值里**：整段遮挡时返回值不含原文的任何字符；
//     局部遮挡只保留非敏感部分，命中词一律替换成遮挡符。
//  2. 判定与切片统一按 rune：中文 1 字符 = 3 字节，
//     用 len() 计数会让「共 N 字」直接错三倍，甚至切出半个汉字。
//  3. 幂等：对已经遮挡过的文本再遮一次，结果必须与一次相同，
//     否则「前端再遮一遍」会把占位符本身也啃掉。
//  4. 无状态、无数据库、无网络：可以在 goroutine、模板渲染、单元测试里随便调。
const (
	// SensitiveMaskMarker 是局部遮挡使用的替代字符（全角星号，与中文排版宽度一致）。
	SensitiveMaskMarker = "＊"

	// SensitiveMaskedPrefix 是整段遮挡时的占位前缀。
	SensitiveMaskedPrefix = "敏感内容已遮挡"

	// SensitiveMaskedEmpty 是「原文为空但仍被判定命中」时的占位。
	SensitiveMaskedEmpty = "（敏感内容已遮挡，原文为空）"
)

// SensitiveMaskedContent 是一段被判为敏感的文本在「默认遮挡」下的展示形态。
//
// 之所以同时返回占位文本与字符数：前端要在不接触原文的前提下，
// 仍然告诉用户「这里原本有多少字」，否则用户无法判断该不该点「显示原文」。
type SensitiveMaskedContent struct {
	// Masked 是可以安全下发的占位文本，**不含原文任何字符**。
	Masked string
	// Runes 是原文字符数（按 rune），用于「共 N 字」的可读长度提示。
	Runes int
	// Empty 表示原文为空（占位无长度可报）。
	Empty bool
}

// BuildSensitiveMaskedContent 把一段敏感原文折叠成「整段遮挡」占位。
//
// 整段遮挡（而不是只替换命中词）是需求「所有页面都默认打标，只是管理员有按钮
// 可以显示原文」的直接结论：默认状态下连上下文都不该出现，
// 只保留可读的长度提示，让用户知道这里有内容、需要管理员点开。
//
// 缺失细节：
//   - 原文为空且被判命中（例如答案只有空白字符）时返回 SensitiveMaskedEmpty，
//     不伪造「共 0 字」，避免前端显示成「什么都没遮挡」。
func BuildSensitiveMaskedContent(original string) SensitiveMaskedContent {
	if strings.TrimSpace(original) == "" {
		return SensitiveMaskedContent{Masked: SensitiveMaskedEmpty, Runes: 0, Empty: true}
	}
	// 长度提示按 rune 计：utf8.RuneCountInString 对非法字节也按 1 计，
	// 不会因为上游塞进半截 UTF-8 就 panic（对比 []rune(s)[:n] 的越界风险）。
	runes := utf8.RuneCountInString(original)
	return SensitiveMaskedContent{
		Masked: fmt.Sprintf("（%s：共 %d 字）", SensitiveMaskedPrefix, runes),
		Runes:  runes,
	}
}

// MaskSensitiveWords 只把命中的词替换成遮挡符，保留其余上下文。
//
// 适用场景与整段遮挡相反：**工单、聊天、评论**这类「内容本身就是处理依据」的文本，
// 用户原话是「工单系统要允许敏感图片上传和敏感词，避免因为误判而导致无法处理。
// 只是用户端做隐藏」—— 也就是说这些地方必须留下可读上下文，只藏掉命中的词。
//
// 规则：
//   - 命中词按 rune 长度从长到短替换，避免「敏感」先替换掉导致「敏感词」匹配不到；
//   - 替换后每个命中字符对应一个遮挡符，长度不变，排版不跳动；
//   - words 为空或全是空白 → 原样返回（没有命中明细时绝不猜测性地整段遮挡）；
//   - 幂等：遮挡符本身不在任何命中词里，重复调用结果一致。
func MaskSensitiveWords(text string, words []string) string {
	if text == "" {
		return ""
	}
	normalized := normalizeSensitiveWordList(words)
	if len(normalized) == 0 {
		return text
	}
	// 长的先换：短词先换会把长词切碎，留下可读的碎片（部分泄露）。
	sort.SliceStable(normalized, func(i, j int) bool {
		return utf8.RuneCountInString(normalized[i]) > utf8.RuneCountInString(normalized[j])
	})
	masked := text
	for _, word := range normalized {
		masked = strings.ReplaceAll(masked, word, strings.Repeat(SensitiveMaskMarker, utf8.RuneCountInString(word)))
	}
	return masked
}

// ParseSensitiveWordDetailJSON 解析 logs.sensitive_words 里存的命中明细 JSON。
//
// 与前端 logListHelpers.mjs 的 parseSensitiveWords 同一语义：
// **解析失败返回空结果而不是报错** —— 历史行格式不可控，
// 一条脏 JSON 不能让整个日志列表读取失败（列表的首要用途是排障）。
func ParseSensitiveWordDetailJSON(raw string) []SensitiveWordScopeResult {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var scopes []SensitiveWordScopeResult
	if err := json.Unmarshal([]byte(trimmed), &scopes); err != nil {
		return nil
	}
	return scopes
}

// MaskSensitiveWordDetailForDisplay 把命中明细 JSON 里的词本身也遮挡。
//
// 为什么连命中词一起遮：徽章/提示会展示「题目：操你妈的」，那就等于把敏感词
// 直接读给所有非管理员听 —— 遮挡了答案却从徽章漏出去，等于没遮。
//
// 保留的部分：scope、status、forbidden、每组命中了几个词、每个词几个字。
// 这些是排障与「该不该点显示原文」的判断依据，本身不含敏感内容。
func MaskSensitiveWordDetailForDisplay(scopes []SensitiveWordScopeResult) string {
	if len(scopes) == 0 {
		return ""
	}
	masked := make([]SensitiveWordScopeResult, len(scopes))
	for i, scope := range scopes {
		masked[i] = scope
		words := make([]string, 0, len(scope.Words))
		for _, word := range scope.Words {
			words = append(words, strings.Repeat(SensitiveMaskMarker, utf8.RuneCountInString(word)))
		}
		masked[i].Words = words
	}
	encoded, err := json.Marshal(masked)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// SensitiveWordListFromScopes 从分组检测结果里汇总所有**命中**的词。
//
// 只取 forbidden=true 分组：未命中分组即便上游回填了词（不该发生）也不参与遮挡，
// 否则会把「没判违规」的文本也挡掉，正好违反「宁可漏标，不可误标」。
func SensitiveWordListFromScopes(scopes []SensitiveWordScopeResult) []string {
	words := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if !scope.Forbidden {
			continue
		}
		words = append(words, scope.Words...)
	}
	return normalizeSensitiveWordList(words)
}

// MaskSensitiveWordsInJSON 在**保留 JSON 结构**的前提下遮挡命中词。
//
// 日志快照（old_answer / new_answer）是 JSON 文本，前端要靠它派生状态、耗时、
// 命中渠道、请求参数等列。整段替换会把这些列一并打空
// （同源问题：展示了但字段是空的），所以这里只逐字符替换**字符串值**里的命中词：
//
//   - 只改字符串值，**键名与结构一个字节都不动**（避免把 "status" 这类键遮坏）；
//   - 数字用 json.Number 保留原样，不会把 1234 变成 1.234e+03；
//   - 没有命中词出现在文本里时**原样返回**，不重新序列化（不制造无谓的格式漂移）；
//   - 解析失败（历史行不是 JSON）退化为整段文本替换，绝不因为格式不对就放行原文。
func MaskSensitiveWordsInJSON(raw string, words []string) string {
	normalized := normalizeSensitiveWordList(words)
	if raw == "" || len(normalized) == 0 {
		return raw
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		// 非 JSON：按纯文本处理。
		return MaskSensitiveWords(raw, normalized)
	}
	masked, changed := maskSensitiveValue(parsed, normalized)
	if !changed {
		return raw
	}
	encoded, err := json.Marshal(masked)
	if err != nil {
		// 序列化失败不能让读取方拿到原文：退化为整段文本替换。
		return MaskSensitiveWords(raw, normalized)
	}
	return string(encoded)
}

// maskSensitiveValue 递归处理解码后的 JSON 值，返回「是否真的改动了内容」。
func maskSensitiveValue(value any, words []string) (any, bool) {
	switch typed := value.(type) {
	case string:
		masked := MaskSensitiveWords(typed, words)
		return masked, masked != typed
	case []any:
		changed := false
		for i := range typed {
			next, itemChanged := maskSensitiveValue(typed[i], words)
			if itemChanged {
				typed[i] = next
				changed = true
			}
		}
		return typed, changed
	case map[string]any:
		changed := false
		for key, item := range typed {
			next, itemChanged := maskSensitiveValue(item, words)
			if itemChanged {
				// 只覆盖值，键保持原样。
				typed[key] = next
				changed = true
			}
		}
		return typed, changed
	default:
		return value, false
	}
}
