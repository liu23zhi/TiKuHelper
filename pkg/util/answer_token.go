package util

import (
	"regexp"
	"strings"
)

// 本文件是答案文本的**统一脏数据清洗**入口。
//
// 背景：题库 provider 返回的答案并不总是干净的一个选项文本或一个字母。
// 实测常见形态：
//
//	答案：A      【答案】A      参考答案: A      选A
//	A、          A.             (A)            （A）
//	A,B          A、B           A B            ABD
//	Ａ（全角）    Ａ、
//
// 旧实现只有 stripAnswerLabelPrefix 一道（`^[A-Za-z][.．、:：)）]\s*`），
// 它能处理 "B. 还原反应" 这种「标签 + 正文」，但处理不了：
//   - 带「答案」字样前缀的（`答案：A`、`【答案】A`）；
//   - 带「选」前缀的（`选A`）；
//   - 全角字母（`Ａ`）；
//   - 多选答案的紧凑/分隔写法（`A,B`、`A、B`、`ABD`）。
//
// 这些形态此前一律落到「无法映射」→ 整组答案被丢弃 → 明明题库有答案却返回空。
//
// 清洗**只用于比对**，不改写返回给客户端的答案正文（与 normalize.go 的口径一致：
// 归一化参与「算不算同一个答案」的判定，不参与输出）。

// answerHeadLabelBracket 匹配方括号/圆括号包裹的「答案」字样前缀：
// 【答案】A、[答案]A、（答案）A。
var answerHeadLabelBracket = regexp.MustCompile(`^[【\[（(]\s*(?:参考答案|正确答案|标准答案|答案)\s*[】\]）)]\s*`)

// answerHeadLabelPlain 匹配「答案：」「参考答案:」这类带冒号的前缀。
//
// 刻意**要求冒号**：没有冒号的「答案」可能是正文的一部分
// （如「答案是否定的」），不能无条件剥掉。
var answerHeadLabelPlain = regexp.MustCompile(`^(?:参考答案|正确答案|标准答案|答案)\s*[:：]\s*`)

// answerTokenTrimSet 是答案 token 首尾可能残留的分隔/装饰字符。
//
// 含全角与半角两种写法。刻意不含 `)`/`)`：右括号是 `(A)` 的成对结尾，
// 由 optionLabelIndex 成对处理，在这一层单边裁剪会把 `(A` 弄坏。
const answerTokenTrimSet = ",，、;；|/ \t\n\r"

// cleanAnswerToken 做**不带选项标签剥离**的底层清洗。
//
// 单独拆出来的理由：`A、D` 这类多答案分隔写法，若先做标签剥离
// （`^[A-Za-z][.．、:：)）]`），开头的 `A、` 会被当成「标签 + 空正文」吃掉，
// 整条答案退化成 `D` —— 多选题直接少选一个，是静默的错答。
//
// 因此顺序必须是：先按分隔符判定多答案形态（用本函数的结果），
// 判定失败后再退回「单 token 清洗」（normalizeAnswerToken，多做标签剥离）。
//
// 步骤：
//  1. 全角转半角（`ＡＢ` → `AB`、`（` → `(`）；
//  2. 剥掉「答案」字样前缀（带冒号，或方/圆括号包裹），剥到稳定；
//  3. 剥掉「选」前缀（仅当剩余部分足够短）；
//  4. 裁掉首尾的分隔符（顿号、逗号、分号、竖线、空白）。
func cleanAnswerToken(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	s := FullWidthStrToHalfWidthStr(trimmed)

	// 括号式与冒号式前缀可能嵌套（`【答案】：A`），循环剥到稳定。
	for i := 0; i < 3; i++ {
		before := s
		strippedBracket := answerHeadLabelBracket.ReplaceAllString(s, "")
		if strippedBracket != s {
			s = strippedBracket
			// 括号前缀后面常跟一个游离的冒号（`【答案】：A`）。
			// 只在**确实剥掉了括号前缀**时裁它，避免误伤正文。
			s = strings.TrimLeft(strings.TrimSpace(s), ":")
		}
		s = strings.TrimSpace(answerHeadLabelPlain.ReplaceAllString(s, ""))
		if s == before {
			break
		}
	}

	// 「选A」形态：仅当剥离后剩余部分很短（≤3 字符）才认。
	if rest, ok := trimAnswerChoiceMarker(s); ok {
		s = rest
	}

	s = strings.Trim(s, answerTokenTrimSet)

	if strings.TrimSpace(s) == "" {
		return trimmed
	}
	return s
}

// normalizeAnswerToken 把一条答案文本清洗成「可比较」的形式。
//
// 在 cleanAnswerToken 之上多剥一层**选项标签前缀**，与选项侧
// FormatOptions（formatOptions 里的 `^[A-Z][.．:：、]\s?`）同源。
//
// 若清洗结果为空，返回**原始输入**的去空白版本：宁可让后续匹配失败，
// 也不要把答案洗成空串（那会让「有答案」变成「无答案」）。
func normalizeAnswerToken(raw string) string {
	cleaned := cleanAnswerToken(raw)
	if cleaned == "" {
		return ""
	}
	cleaned = stripAnswerLabelPrefix(cleaned)
	cleaned = strings.Trim(cleaned, answerTokenTrimSet)
	if strings.TrimSpace(cleaned) == "" {
		return strings.TrimSpace(raw)
	}
	return cleaned
}

// trimAnswerChoiceMarker 剥掉「选」前缀，返回 (剩余, 是否剥过)。
//
// 只剥一层，且只在剩余部分足够短时剥 —— 「选择题的正确答案是……」
// 这类正文绝不能剥。以 3 个字符为界。
func trimAnswerChoiceMarker(value string) (string, bool) {
	runes := []rune(value)
	if len(runes) == 0 || runes[0] != '选' {
		return value, false
	}
	rest := strings.TrimSpace(string(runes[1:]))
	if rest == "" || len([]rune(rest)) > 3 {
		return value, false
	}
	return rest, true
}

// answerLabelSeparator 切分多答案 token 的分隔符集合。
//
// 覆盖 `A,B` / `A、B` / `A B` / `A;B` / `A/B` / `A+B`。
// `+` 与 `/` 也在内是刻意的：部分题库用它们连接多选答案。
var answerLabelSeparator = regexp.MustCompile(`[\s,，、;；/|\\+]+`)

// answerLabelIndexes 把一条答案文本解析成若干 0 基选项下标。
//
// 支持三种形态，全部要求「每个部分都是单个字母」：
//
//	标签式：  "A"、"a"、"A."、"(A)"、"【A】"        → 1 个下标
//	分隔式：  "A,B"、"A、B"、"A B"、"a;b"、"A.、B."  → 多个下标
//	紧凑式：  "ABD"、"AC"                          → 多个下标（**仅大写**）
//
// 紧凑式刻意只认大写：英文单词（如 "acid"）在 4 选项的题上会全部落进 A-D 范围，
// 若允许小写就会把正常英文答案误映射成字母组合。题库的字母答案统一是大写。
//
// 任一组成部分无法解析、或下标越界，整条返回 false —— 不做部分采纳。
// 部分采纳会把「A、丁」这种半对半错的组映射成只选 A，属于制造错答。
func answerLabelIndexes(raw string, optionCount int) ([]int, bool) {
	token := cleanAnswerToken(raw)
	if token == "" || optionCount <= 0 {
		return nil, false
	}

	// **先切分再解析**，顺序不能反。
	//
	// 若先走「单 token 清洗」再切分，`A、D` 会被 stripAnswerLabelPrefix
	// 的 `^[A-Za-z][.．、:：)）]` 当成「标签 A + 正文 D」吃掉，整条退化成
	// `D` —— 多选题静默少选一个，是比漏答更糟的错答。
	parts := answerLabelSeparator.Split(token, -1)
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}

	switch {
	case len(nonEmpty) >= 2:
		// 分隔式：每段都必须是单个字母标号（可带自身装饰，如 `A.`、`(A)`）。
		indexes := make([]int, 0, len(nonEmpty))
		seen := make(map[int]bool, len(nonEmpty))
		for _, part := range nonEmpty {
			index, ok := optionLabelIndex(part, optionCount)
			if !ok {
				return nil, false
			}
			if seen[index] {
				// 重复标号（`A,A`）视为脏数据，不静默去重 ——
				// 去重会让脏数据看起来像正常答案。
				return nil, false
			}
			seen[index] = true
			indexes = append(indexes, index)
		}
		return indexes, true

	case len(nonEmpty) == 1:
		single := nonEmpty[0]
		// 单标号：剥掉标签前缀后必须恰好剩一个字母。
		if index, ok := optionLabelIndex(normalizeAnswerToken(single), optionCount); ok {
			return []int{index}, true
		}
		// 紧凑式：`ABD`。
		return compactAnswerLabelIndexes(single, optionCount)
	}
	return nil, false
}

// compactAnswerLabelIndexes 解析 `ABD` 这种无分隔的紧凑字母答案。
//
// 三个必要条件（缺一不可）：
//  1. 全是**大写** ASCII 字母（见 answerLabelIndexes 的说明）；
//  2. 长度 ≥ 2（单字母由 optionLabelIndex 处理，走各自的题型规则）；
//  3. 长度 ≤ optionCount（多于选项数必然是别的含义，不猜）。
//
// 返回的下标按**字母顺序**给出；调用方最终按选项顺序生成 answerKey，
// 因此 `DA` 与 `AD` 得到同一个答案键。
func compactAnswerLabelIndexes(token string, optionCount int) ([]int, bool) {
	runes := []rune(token)
	if len(runes) < 2 || len(runes) > optionCount {
		return nil, false
	}
	indexes := make([]int, 0, len(runes))
	seen := make(map[int]bool, len(runes))
	for _, r := range runes {
		if r < 'A' || r > 'Z' {
			return nil, false
		}
		index := int(r - 'A')
		if index >= optionCount || seen[index] {
			return nil, false
		}
		seen[index] = true
		indexes = append(indexes, index)
	}
	return indexes, true
}
