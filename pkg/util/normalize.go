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
