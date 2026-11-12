package util

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/gookit/goutil/strutil"
)

// legacyPlatValue 是历史 hash 公式里 plat 的固定取值。
//
// plat 曾参与本地题库 hash：md5(题面 + 排序选项 + type + plat)。
// 它在**所有调用方**（chaoxing_plus 与生产库 330 行）恒为 -1，没有任何区分度，
// 因此 hash 公式已去掉该维度。保留这个常量只为了兼容**旧版本服务器写入的行**。
const legacyPlatValue = -1

// SortOptionsJSON 把选项排序后序列化成 JSON，供 hash 计算使用。
//
// 选项顺序不影响题目身份，因此先排序再参与 hash。
func SortOptionsJSON(options []string) []byte {
	sorted := make([]string, len(options))
	copy(sorted, options)
	sort.Strings(sorted)

	payload, err := json.Marshal(sorted)
	if err != nil {
		return []byte("[]")
	}
	return payload
}

// BuildTikuHash 计算当前版本的题库 hash（不含 plat）。
func BuildTikuHash(question string, options []string, questionType int) string {
	return strutil.Md5(
		FormatString(question) +
			string(SortOptionsJSON(options)) +
			strconv.Itoa(questionType),
	)
}

// BuildLegacyTikuHash 计算旧版本的题库 hash（含 plat，固定为 -1）。
//
// 仅用于**读取兼容**：灰度发布期间可能有旧实例仍在用旧公式写入，
// 新实例需要能命中这些行。写入侧永远只用 BuildTikuHash。
func BuildLegacyTikuHash(question string, options []string, questionType int) string {
	return strutil.Md5(
		FormatString(question) +
			string(SortOptionsJSON(options)) +
			strconv.Itoa(questionType) +
			strconv.Itoa(legacyPlatValue),
	)
}

// BuildLegacyTikuHashRawQuestion 计算旧版本里「未格式化题面」的 hash 变体。
//
// 历史实现同时用格式化题面与原始题面各算一次（兼容更早的入库格式），
// 读取时同样要带上，否则那些行也会失联。
func BuildLegacyTikuHashRawQuestion(question string, options []string, questionType int) string {
	return strutil.Md5(
		question +
			string(SortOptionsJSON(options)) +
			strconv.Itoa(questionType) +
			strconv.Itoa(legacyPlatValue),
	)
}

// BuildTikuHashCandidates 返回查询本地题库时应该匹配的全部 hash。
//
// 之所以要多个候选，是因为「同一个问题」在库里的写法可能不同：
//  1. 当前公式（格式化题面 + 原样选项）
//  2. 旧公式（含 plat=-1，等待启动迁移回填）
//  3. 更早的「未格式化题面」变体
//  4. **选项文本抖动**的变体（标点全角/半角、行尾多余标点、首尾空白）
//
// 第 4 类来自线上实测：抓页面得到的选项标点不稳定，若把它当成「不同的题」，
// 就会出现「库里明明有答案却匹配不到 → 转去调 AI → 答案不准还照常计费」。
//
// 选项与题面都参与排序/归一化，因此**顺序不同也不影响**命中。
func BuildTikuHashCandidates(question string, options []string, questionType int) []string {
	normalizedOptions := NormalizeCacheKeyOptions(options)
	normalizedQuestion := NormalizeCacheKeyText(question)

	raw := []string{
		// 当前公式（含旧 plat 变体 + 未格式化题面变体）
		BuildTikuHash(question, options, questionType),
		BuildLegacyTikuHash(question, options, questionType),
		BuildLegacyTikuHashRawQuestion(question, options, questionType),
	}

	// 选项被归一化后才相等的变体。当 options 本身已归一化时这些会与上面重合，
	// 由下面的去重收敛，不产生额外查询成本。
	normalized := []string{
		BuildTikuHash(question, normalizedOptions, questionType),
		BuildLegacyTikuHash(question, normalizedOptions, questionType),
		BuildLegacyTikuHashRawQuestion(question, normalizedOptions, questionType),
		// 题面也归一化的组合：覆盖「历史行用归一化题面写入」的情况。
		BuildTikuHash(normalizedQuestion, normalizedOptions, questionType),
	}

	// 第 5 类：**括号内空白无关**的变体（2026-11-13 实测补）。
	//
	// 线上题库里「以下属于指示剂的有( )」与「...有()」是同一道题，
	// 但 FormatString 不动内部空白 → hash 不同 → 漏答。
	// 本地开发库 348 行中 46 行（13.2%）含 `( )` 形式。
	//
	// 只加**查询侧候选**，写入公式（FillHash → BuildTikuHash）一字不动，
	// 因此存量题库 hash 完全不受影响，是纯粹的召回增强。
	//
	// 注意：这里对「题面」和「选项」都套用空白无关归一 ——
	// 选项里同样常见 `( )` 形式的填空占位。
	spaceInsensitiveQuestion := NormalizeSpaceInsensitiveText(question)
	spaceInsensitiveOptions := normalizeSpaceInsensitiveOptions(options)
	spaceInsensitive := []string{
		BuildTikuHash(spaceInsensitiveQuestion, spaceInsensitiveOptions, questionType),
		BuildLegacyTikuHash(spaceInsensitiveQuestion, spaceInsensitiveOptions, questionType),
	}

	seen := make(map[string]struct{}, len(raw)+len(normalized)+len(spaceInsensitive))
	candidates := make([]string, 0, len(raw)+len(normalized)+len(spaceInsensitive))
	for _, hash := range append(append(raw, normalized...), spaceInsensitive...) {
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		candidates = append(candidates, hash)
	}
	return candidates
}

// normalizeSpaceInsensitiveOptions 对每个选项套用空白无关归一，**保持原顺序**。
//
// 与 NormalizeCacheKeyOptions 的差别只有一处：额外折叠括号内空白。
// 顺序必须保留（选项顺序是题目身份的一部分）。
func normalizeSpaceInsensitiveOptions(options []string) []string {
	if len(options) == 0 {
		return nil
	}
	normalized := make([]string, len(options))
	for i, option := range options {
		normalized[i] = NormalizeSpaceInsensitiveText(option)
	}
	return normalized
}
