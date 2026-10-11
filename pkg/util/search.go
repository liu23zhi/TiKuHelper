package util

import "strings"

// SearchKeywordMaxLength 限制列表搜索关键词的长度，
// 避免超长匹配式拖慢查询。
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

// BuildInstrCondition 为若干列生成一个「包含关键词」的 OR 条件，并返回查询参数。
//
// 生成的分支形如：INSTR(LOWER(COALESCE(列, ”)), LOWER(?)) > 0
//
// 为什么不用 LIKE：
//
//	LIKE '%kw%' 需要转义用户输入里的 % 与 _，而转义依赖 SQL 的 ESCAPE 子句；
//	实测 glebarez/sqlite（v1.10.0 / SQLite 3.41.2）下 ESCAPE 会静默失效 ——
//	`WHERE v LIKE '%!_%' ESCAPE '!'` 依然把 `_` 当通配符，输入 "_" 会匹配到全部记录。
//	根因是 GORM 会把 ESCAPE 的字面量渲染成双引号（`ESCAPE "!"`），
//	而 SQLite 里双引号是标识符；改用参数绑定也无法修复。
//
// INSTR 是纯子串查找、没有通配符概念，因此不存在转义问题，
// 且 MySQL 与 SQLite 都原生支持，行为完全一致。
//
// 注意：INSTR 区分大小写，所以两侧都套 LOWER()，
// 与原先 LIKE 在 SQLite 下的默认大小写不敏感行为保持一致。
func BuildInstrCondition(keyword string, columnsExpr ...string) (string, []interface{}) {
	normalized := NormalizeSearchKeyword(keyword)
	if normalized == "" || len(columnsExpr) == 0 {
		return "", nil
	}

	clauses := make([]string, 0, len(columnsExpr))
	args := make([]interface{}, 0, len(columnsExpr))
	for _, column := range columnsExpr {
		clauses = append(clauses, "INSTR(LOWER("+column+"), LOWER(?)) > 0")
		args = append(args, normalized)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}
