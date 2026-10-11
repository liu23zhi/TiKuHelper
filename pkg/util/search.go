package util

import "strings"

// SearchKeywordMaxLength 限制列表搜索关键词的长度，
// 避免超长 LIKE 模式拖慢查询。
const SearchKeywordMaxLength = 100

// NormalizeSearchKeyword 归一化列表搜索关键词：去首尾空白并截断到上限。
// 空关键词表示不参与过滤，调用方应据此跳过该条件。
//
// 按「字符数」截断：关键词多为中文，用 len() 得到的是字节数，
// 混用会把字符切成半个、产生乱码。
func NormalizeSearchKeyword(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) > SearchKeywordMaxLength {
		return string(runes[:SearchKeywordMaxLength])
	}
	return trimmed
}

// EscapeLikeKeyword 转义 LIKE 模式中的特殊字符，防止用户输入被当成通配符
// （例如输入 "%" 不应该匹配全部记录）。
// 返回值需要配合 SQL 里的 ESCAPE '\' 使用。
func EscapeLikeKeyword(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	)
	return replacer.Replace(value)
}

// BuildLikePattern 生成 "%关键词%" 形式的 LIKE 模式（已转义），
// 关键词为空时返回空字符串。
func BuildLikePattern(value string) string {
	keyword := NormalizeSearchKeyword(value)
	if keyword == "" {
		return ""
	}
	return "%" + EscapeLikeKeyword(keyword) + "%"
}
