package util

import (
	"testing"
)

// 「括号内空白无关」召回的回归钉子。
//
// 缺陷背景（2026-11-13 实测真实题库）：
//
//	FormatString 只 TrimSpace 首尾空白，**不动内部空白**。于是
//	    "以下属于指示剂的有( )"  vs  "以下属于指示剂的有()"
//	hash 不同，被当成两道题 → 漏答。
//
//	本地开发库 run/data/tiku.db 共 348 行，其中 **46 行（13.2%）**
//	题干含 `( )` 形式的空白括号（实测 SQL：
//	`SELECT COUNT(*) FROM tiku WHERE question LIKE '%( %' OR question LIKE '% )%'`）。
//	客户端抓取时 `()` / `( )` / `（ ）` 混用极常见，任一写法差异都会导致漏答，
//	且漏答后转去调 AI 还照常计费。
//
// 修法：新增 NormalizeSpaceInsensitiveText，并以「额外查询候选」的形式接入
// BuildTikuHashCandidates。**写入公式（FillHash → BuildTikuHash）一字不动**，
// 因此存量题库 hash 完全不受影响 —— 这是纯粹的召回增强。

// TestNormalizeSpaceInsensitiveTextCollapsesBracketBlanks 钉住折叠行为本身。
func TestNormalizeSpaceInsensitiveTextCollapsesBracketBlanks(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"半角括号内单空格", "有( )", "有()"},
		{"半角括号内双空格", "有(  )", "有()"},
		{"全角括号内空格", "有（ ）", "有()"},
		{"全角括号内多空格", "有（   ）", "有()"},
		{"制表符", "有(\t)", "有()"},
		{"括号内无空白", "有()", "有()"},
		{"多个空白括号", "有( )和( )", "有()和()"},
		{"无括号不动", "以下属于指示剂的有", "以下属于指示剂的有"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeSpaceInsensitiveText(tt.in); got != tt.want {
				t.Fatalf("NormalizeSpaceInsensitiveText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeSpaceInsensitiveTextDoesNotCollapseMeaningfulContent 是**误删保护**。
//
// 折叠空白时必须保住语义差异：
//   - 括号外的空格不折叠（「下列 属于」与「下列属于」在题库里可能真的是两种写法，
//     而且折叠它会让「计算 1+1」与「计算1+1」意外等同 —— 那是另一类判断）；
//   - 换行不折叠（分项列举有结构意义）。
func TestNormalizeSpaceInsensitiveTextDoesNotCollapseMeaningfulContent(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		// 括号外空格必须保留（本函数只处理括号内）
		{"括号外空格保留", "计算 1+1", "计算 1+1"},
		{"词间空格保留", "下列 属于 指示剂", "下列 属于 指示剂"},
		// 括号内有实际内容时不得改动
		{"括号内有内容", "有(酚酞)", "有(酚酞)"},
		{"括号内有内容带空格", "有( 酚酞 )", "有( 酚酞 )"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeSpaceInsensitiveText(tt.in); got != tt.want {
				t.Fatalf("NormalizeSpaceInsensitiveText(%q) = %q, want %q（误改了有意义内容）",
					tt.in, got, tt.want)
			}
		})
	}
}

// TestBuildTikuHashCandidatesUnifiesBracketBlanks 是端到端核心钉子。
//
// 对同一道题的不同空白写法，候选集合必须有交集 —— 有交集就意味着
// 主链路（dBSearch，`hash IN (...)`）能查到。
func TestBuildTikuHashCandidatesUnifiesBracketBlanks(t *testing.T) {
	base := "以下属于指示剂的有( )"
	variants := []string{
		"以下属于指示剂的有( )",
		"以下属于指示剂的有()",
		"以下属于指示剂的有(  )",
		"以下属于指示剂的有（ ）",
		"以下属于指示剂的有（  ）",
	}
	baseHashes := BuildTikuHashCandidates(base, nil, 0)

	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			variantHashes := BuildTikuHashCandidates(variant, nil, 0)
			if !hasHashOverlap(baseHashes, variantHashes) {
				t.Fatalf("候选哈希无交集，主链路会漏答：\n  base(%q)    = %v\n  variant(%q) = %v",
					base, baseHashes, variant, variantHashes)
			}
		})
	}
}

// TestBuildTikuHashCandidatesRejectsDifferentQuestions 是**反向保护**。
//
// 增加候选不能把不同题目变成同一道 —— 否则会返回错误答案（比漏答更糟）。
func TestBuildTikuHashCandidatesRejectsDifferentQuestions(t *testing.T) {
	for _, tt := range []struct {
		name  string
		left  string
		right string
	}{
		{"否定题不得撞", "以下属于指示剂的有( )", "以下不属于指示剂的有()"},
		{"不同数字不得撞", "计算 1+1 的值", "计算 1+2 的值"},
		{"标点差异不得撞", "A,B", "AB"},
		{"不同主体不得撞", "以下属于酸性的有( )", "以下属于碱性的有()"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			leftHashes := BuildTikuHashCandidates(tt.left, nil, 0)
			rightHashes := BuildTikuHashCandidates(tt.right, nil, 0)
			if hasHashOverlap(leftHashes, rightHashes) {
				t.Fatalf("不同题目不应产生相同 hash：\n  %q = %v\n  %q = %v",
					tt.left, leftHashes, tt.right, rightHashes)
			}
		})
	}
}

// TestBuildTikuHashCandidatesCandidateBounded 钉住候选数量上限，防止性能回归。
//
// 每个候选都会变成 SQL `hash IN (...)` 的一个参数。若无节制增加候选，
// 会让主链路每次查询的 IN 列表膨胀。
// 实测当前最大 5 个（含全角 + 空白 + 选项变体），留到 8 作为硬上限。
func TestBuildTikuHashCandidatesCandidateBounded(t *testing.T) {
	for _, q := range []string{
		"下列哪个是质数",
		"以下属于指示剂的有( )",
		"以下属于指示剂的有（ ）",
		"下列关于细胞呼吸的叙述中，正确的是哪一个选项呢请选择",
	} {
		got := len(BuildTikuHashCandidates(q, []string{"A. 甲", "B. 乙", "C. 丙", "D. 丁"}, 0))
		if got > 8 {
			t.Fatalf("候选数 %d 超过上限 8（%q）—— SQL IN 列表会膨胀", got, q)
		}
	}
}

// hasHashOverlap 判断两组哈希是否有交集。
func hasHashOverlap(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, h := range left {
		seen[h] = struct{}{}
	}
	for _, h := range right {
		if _, ok := seen[h]; ok {
			return true
		}
	}
	return false
}
