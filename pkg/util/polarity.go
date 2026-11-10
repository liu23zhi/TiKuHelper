package util

import (
	"strings"
)

// 归一化工具：把「同一个答案的不同文本写法」收敛成同一个形式。
//
// 为什么必须有：provider 写答案的形态高度不统一（见 answer_token.go 的说明），
// 若把这些差异当成「不同的答案」，会造成两类故障：
//   - 明明题库有答案却返回空（漏答，准确率最直接的损失）；
//   - 多个 provider 对同一答案重复投票时被当成不同意见，聚合失去意义。
//
// 与 normalize.go 的口径一致：归一化**只用于比对**，不改写返回给客户端的
// 答案正文。

// judgementPolarityTable 是判断题「对/错」的唯一权威同义词表。
//
// 键必须是小写、去空白的形式（JudgementPolarity 负责归一化后再查表）。
//
// 为什么集中成一张表：选项侧（IsTrue/IsFalse，被 FormatOptions 与题库导入
// parse_xls 使用）与答案侧（JudgementPolarity，被搜题与 AI 兜底使用）
// 此前各维护一份词表，结果选项侧认 "A/B"、答案侧不认，同一道判断题
// 换个写法就两侧表现不一致。现在两侧共用本表，仅「字母标号」由选项侧
// 单独叠加（因为字母在答案侧是标号而非极性，见 optionLabelIndex）。
var judgementPolarityTable = map[string]bool{
	// —— 真 ——
	"正确":    true,
	"正确的":   true,
	"对":     true,
	"对的":    true,
	"是":     true,
	"是的":    true,
	"真":     true,
	"√":     true,
	"✓":     true,
	"✔":     true,
	"true":  true,
	"t":     true,
	"yes":   true,
	"y":     true,
	"right": true,
	// "ri" 是历史遗留别名（旧 IsTrue 正则里就有），保留以免题库脏数据回退。
	"ri": true,

	// —— 假 ——
	"错误":    false,
	"错误的":   false,
	"错":     false,
	"错的":    false,
	"否":     false,
	"不是":    false,
	"不对":    false,
	"假":     false,
	"×":     false,
	"✗":     false,
	"✘":     false,
	"false": false,
	"f":     false,
	"no":    false,
	"n":     false,
	// "fa" 是历史遗留别名（旧 IsFalse 正则里就有），保留以免题库脏数据回退。
	"fa": false,
}

// JudgementPolarity 判断一个答案/选项是否表达「对/错」，返回 (polarity, ok)。
//
// 判断题的模型输出并不稳定：正确/错误、对/错、√/×、true/false、yes/no、
// 真/假、是/不是 都出现过。搜题链路与 AI 兜底链路必须用**同一套**判定，
// 否则会出现「/search 能答、兜底判无答案」。
//
// 刻意**不认**字母标号（A/B）：字母在答案侧是「第几个选项」，不是「对还是错」。
// 把它当极性会把 "A" 直接判成「正确」而与选项顺序无关，一旦题库把
// A=错误、B=正确（真实存在），就会系统性答反。字母由 optionLabelIndex 处理。
func JudgementPolarity(value string) (bool, bool) {
	normalized := normalisePolarityToken(value)
	if normalized == "" {
		return false, false
	}
	polarity, ok := judgementPolarityTable[normalized]
	return polarity, ok
}

// normalisePolarityToken 把极性 token 归一化成查表用的键。
//
// 做三件事：全角转半角（`√` 的全角变体、全角空格）、去首尾空白、转小写。
func normalisePolarityToken(value string) string {
	trimmed := strings.TrimSpace(FullWidthStrToHalfWidthStr(value))
	if trimmed == "" {
		return ""
	}
	return strings.ToLower(trimmed)
}
