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
// 之所以要多个候选：hash 公式变更后，库里的行可能来自
//   ① 新公式（当前写入）
//   ② 旧公式（含 plat=-1，等待启动迁移回填）
//   ③ 更早的「未格式化题面」变体
// 三者都要能命中，否则公式切换会导致已有题库短期内失联。
func BuildTikuHashCandidates(question string, options []string, questionType int) []string {
	current := BuildTikuHash(question, options, questionType)
	legacy := BuildLegacyTikuHash(question, options, questionType)
	legacyRaw := BuildLegacyTikuHashRawQuestion(question, options, questionType)

	seen := make(map[string]struct{}, 3)
	candidates := make([]string, 0, 3)
	for _, hash := range []string{current, legacy, legacyRaw} {
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		candidates = append(candidates, hash)
	}
	return candidates
}
