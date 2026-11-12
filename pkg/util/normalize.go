package util

// 归一化工具：把「同一个问题的不同文本写法」收敛成同一个形式。
//
// 为什么必须有：上游抓取页面的文本**标点不稳定**（全角 `。`/半角 `.`、
// 全角 `，`/半角 `,`、行尾多余标点、首尾空白）。若把这些差异当作「不同的题」，
// 会造成两类线上故障：
//   - 结果缓存永不命中 → 同一道题被反复计费；
//   - 本地题库 hash 对不上 → 明明库里有答案却去调 AI。
//
// 归一化**只用于「判断是不是同一个问题」**（缓存键、题库 hash、去重），
// 绝不改写实际发给上游的题目文本、也不改写写回客户端的响应。

// NormalizeCacheKeyText 把文本归一化到「无语义差异」的形式。
//
// 复用 FormatString：全角转半角、中文引号/句号转英文、去尾部标点、TrimSpace。
// 因此 `机器人。` 与 `机器人.`、`内容，相同` 与 `内容,相同` 会归一成同一个值。
//
// 刻意**不**去掉文本中间的标点：`A,B` 与 `AB` 是不同内容，不能混为一谈。
func NormalizeCacheKeyText(value string) string {
	return FormatString(value)
}

// NormalizeCacheKeyOptions 逐项归一化选项，**保持原顺序**。
//
// 顺序必须保留：选项顺序本身就是题目的一部分（`A.1 B.2` 与 `A.2 B.1` 不同题）。
// 这里只消除标点/空白层面的抖动，不做排序、不去重。
//
// 空输入统一返回 nil，让 `nil` 与 `[]string{}` 映射成同一个值，
// 避免「无选项」的两种表示分裂缓存键与 hash。
func NormalizeCacheKeyOptions(options []string) []string {
	if len(options) == 0 {
		return nil
	}

	normalized := make([]string, len(options))
	for i, option := range options {
		normalized[i] = NormalizeCacheKeyText(option)
	}
	return normalized
}

// NormalizeSpaceInsensitiveText 生成「空白无关」的题面变体，用于**查询侧**的额外候选 hash。
//
// 动机（2026-11-13 实测真实题库）：
//
//	FormatString 只 TrimSpace 首尾空白，**不动文本内部空白**。于是
//	    "以下属于指示剂的有( )"  vs  "以下属于指示剂的有()"
//	hash 不同，被当成两道题。而题库里这类「空白括号」是真实存在的主流写法 ——
//	本地开发库 tiku.db 共 348 行，其中 46 行（13.2%）题干含 `( )` 形式的空白括号。
//	客户端抓取时 `()` / `( )` / `（ ）` 混用极常见，任一写法差异都会导致**漏答**，
//	且漏答后转去调 AI 还照常计费。
//
// 为什么做成**独立函数 + 只进查询候选**，而不是改 FormatString：
//
//	FormatString 被写入侧 middleware.FillHash 使用（`t.Hash = util.BuildTikuHash(...)`）。
//	改它会改变写入公式 → 存量题库全部 hash 失效 → 反而造成更大面积的漏答。
//	所以只把「空包无关」作为**查询侧的额外 hash 候选**加进 BuildTikuHashCandidates，
//	写入公式一字不动，存量数据完全兼容。
//
// 只处理空白，不做其它归一：
//   - 不删中间标点（`A,B` 与 `AB` 是不同内容）；
//   - 不动全角（已由 FormatString 的 FullWidthStrToHalfWidthStr 处理）。
//
// 具体把**空括号**内部的空白折叠掉（`( )` → `()`、`(  )` → `()`、`（ ）` → `()`）。
// 关键是只在「左括号后紧跟空白、再紧跟右括号」时才动手 ——
// 括号内有实际内容时（`( 酚酞 )`）一律不动，避免误改有意义内容。
func NormalizeSpaceInsensitiveText(value string) string {
	formatted := FormatString(value)
	runes := []rune(formatted)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if !isTikuOpenBracket(r) {
			out = append(out, r)
			continue
		}
		// 探测：左括号 + 若干空白 + 右括号 → 折叠成「左括号 + 对应右括号」。
		j := i + 1
		for j < len(runes) && isTikuBlankRune(runes[j]) {
			j++
		}
		// 必须至少有一个空白（否则本来就是 `()`，无需处理），且空白后紧跟右括号。
		if j > i+1 && j < len(runes) && isTikuCloseBracket(runes[j]) {
			out = append(out, r, runes[j])
			i = j // 跳过已消费的空白与右括号
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// isTikuOpenBracket 判断是否为左括号（半角或全角）。
func isTikuOpenBracket(r rune) bool { return r == '(' || r == '（' }

// isTikuCloseBracket 判断是否为右括号（半角或全角）。
func isTikuCloseBracket(r rune) bool { return r == ')' || r == '）' }

// isTikuBlankRune 判断是否为「括号内可安全忽略的空白」。
//
// 只认 ASCII 空格、制表、全角空格；**不认换行** ——
// 换行在题干里通常有结构意义（分项列举），折叠掉更容易造成误配。
func isTikuBlankRune(r rune) bool {
	return r == ' ' || r == '\t' || r == '\u3000'
}
