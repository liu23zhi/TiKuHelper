package util

import "testing"

// 同一道题在同一平台重复入库必须得到同样的 hash，否则本地题库会反复插入重复行。
func TestBuildTikuHashIsStable(t *testing.T) {
	first := BuildTikuHash("1+1=?", []string{"1", "2"}, 0)
	second := BuildTikuHash("1+1=?", []string{"1", "2"}, 0)
	if first != second {
		t.Fatalf("expected a stable hash, got %q vs %q", first, second)
	}
	if len(first) != 32 {
		t.Fatalf("expected an md5 hex digest, got %q", first)
	}
}

// 选项顺序不影响题目身份。
func TestBuildTikuHashIgnoresOptionOrder(t *testing.T) {
	ordered := BuildTikuHash("题干", []string{"A", "B", "C"}, 1)
	shuffled := BuildTikuHash("题干", []string{"C", "A", "B"}, 1)
	if ordered != shuffled {
		t.Fatalf("expected option order to be normalized, got %q vs %q", ordered, shuffled)
	}
}

// 题面、选项、题型任一改变都必须得到不同的 hash。
func TestBuildTikuHashDistinguishesQuestionIdentity(t *testing.T) {
	base := BuildTikuHash("题干", []string{"A", "B"}, 0)

	others := map[string]string{
		"不同题面": BuildTikuHash("另一道题", []string{"A", "B"}, 0),
		"不同选项": BuildTikuHash("题干", []string{"A", "C"}, 0),
		"不同题型": BuildTikuHash("题干", []string{"A", "B"}, 1),
		"选项增减": BuildTikuHash("题干", []string{"A", "B", "C"}, 0),
	}
	for name, hash := range others {
		if hash == base {
			t.Fatalf("expected %s to produce a different hash", name)
		}
	}
}

// 回归：plat 不再参与 hash。
//
// 该维度在所有调用方恒为 -1，没有区分度；去掉后同一道题只会有一种 hash。
func TestBuildTikuHashDoesNotDependOnPlat(t *testing.T) {
	// 旧公式（含 plat=-1）与新公式必须不同，否则说明根本没去掉。
	newHash := BuildTikuHash("题干", []string{"A"}, 0)
	legacyHash := BuildLegacyTikuHash("题干", []string{"A"}, 0)
	if newHash == legacyHash {
		t.Fatal("expected the new hash formula to differ from the legacy plat-based one")
	}
}

// 读取候选必须同时包含当前公式与历史公式，否则公式切换会让已有题库失联。
func TestBuildTikuHashCandidatesCoversLegacyFormulas(t *testing.T) {
	question := "题干"
	options := []string{"B", "A"}
	questionType := 2

	candidates := BuildTikuHashCandidates(question, options, questionType)

	want := []string{
		BuildTikuHash(question, options, questionType),
		BuildLegacyTikuHash(question, options, questionType),
		BuildLegacyTikuHashRawQuestion(question, options, questionType),
	}

	index := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		index[candidate] = struct{}{}
	}
	for _, hash := range want {
		if _, ok := index[hash]; !ok {
			t.Fatalf("expected candidate %q to be present in %v", hash, candidates)
		}
	}
}

// 题面含全角字符时，格式化与未格式化会算出不同的 hash，两个候选都要在。
func TestBuildTikuHashCandidatesDistinguishFormattedAndRawQuestion(t *testing.T) {
	// 全角问号会被 FormatString 转成半角，且末尾标点会被裁掉。
	question := "这是题干？"

	candidates := BuildTikuHashCandidates(question, []string{"A"}, 0)

	formatted := BuildTikuHash(question, []string{"A"}, 0)
	raw := BuildLegacyTikuHashRawQuestion(question, []string{"A"}, 0)
	if formatted == raw {
		t.Fatalf("expected the formatted and raw question hashes to differ for %q", question)
	}

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		seen[candidate] = struct{}{}
	}
	for _, hash := range []string{formatted, raw} {
		if _, ok := seen[hash]; !ok {
			t.Fatalf("expected candidate %q in %v", hash, candidates)
		}
	}
}

// 三个公式互不相同且都要保留，去重逻辑不应误合并。
func TestBuildTikuHashCandidatesAreDistinct(t *testing.T) {
	candidates := BuildTikuHashCandidates("这是题干？", []string{"A"}, 0)

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, dup := seen[candidate]; dup {
			t.Fatalf("expected distinct candidates, got duplicate %q in %v", candidate, candidates)
		}
		seen[candidate] = struct{}{}
	}
	if len(candidates) != 3 {
		t.Fatalf("expected 3 distinct candidates, got %d (%v)", len(candidates), candidates)
	}
}

// 题面无需格式化时，两个历史变体会重合，去重必须把它收敛成一个候选。
func TestBuildTikuHashCandidatesDeduplicatesWhenFormatsAgree(t *testing.T) {
	candidates := BuildTikuHashCandidates("plain question", []string{"A"}, 0)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates (current + one legacy variant), got %d (%v)", len(candidates), candidates)
	}
}

// 题面仅首尾空白不同视为同一道题（hash 前会 FormatString + TrimSpace）。
func TestBuildTikuHashIgnoresSurroundingWhitespace(t *testing.T) {
	trimmed := BuildTikuHash("题干", []string{"A"}, 0)
	padded := BuildTikuHash("  题干  ", []string{"A"}, 0)
	if trimmed != padded {
		t.Fatalf("expected surrounding whitespace to be ignored, got %q vs %q", trimmed, padded)
	}
}

// 空选项与 nil 选项必须一致，避免入库路径与查询路径算出不同 hash。
func TestBuildTikuHashTreatsNilAndEmptyOptionsAlike(t *testing.T) {
	nilOptions := BuildTikuHash("题干", nil, 0)
	emptyOptions := BuildTikuHash("题干", []string{}, 0)
	if nilOptions != emptyOptions {
		t.Fatalf("expected nil and empty options to hash alike, got %q vs %q", nilOptions, emptyOptions)
	}
}
