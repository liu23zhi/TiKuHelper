package search

import "testing"

// 回归：非客观题（填空题/连线题/简答题/阅读理解）必须保留重复答案。
//
// 多个空、多组连线、多个要点可能都是同一个答案文本；
// 历史实现用 `questionType == 2` 判断，type 4 / -4 的重复答案被错误去重，
// 会让答案数与空数对不上，前端按序填答就会错位。
func TestNormalizeAISearchAnswersPreservesRepeatedAnswersForNonObjectiveTypes(t *testing.T) {
	testCases := []struct {
		name         string
		questionType int
		raw          []string
		want         []string
	}{
		{
			name:         "填空题重复答案保留",
			questionType: 2,
			raw:          []string{"春天", "春天"},
			want:         []string{"春天", "春天"},
		},
		{
			name:         "简答题重复要点保留",
			questionType: 4,
			raw:          []string{"要点A", "要点A"},
			want:         []string{"要点A", "要点A"},
		},
		{
			name:         "阅读理解重复答案保留",
			questionType: -4,
			raw:          []string{"甲", "甲", "乙"},
			want:         []string{"甲", "甲", "乙"},
		},
		{
			name:         "单选题去重",
			questionType: 0,
			raw:          []string{"A", "A"},
			want:         []string{"A"},
		},
		{
			name:         "多选题去重",
			questionType: 1,
			raw:          []string{"A", "B", "A"},
			want:         []string{"A", "B"},
		},
		{
			name:         "判断题去重",
			questionType: 3,
			raw:          []string{"正确", "正确"},
			want:         []string{"正确"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := normalizeAISearchAnswers(testCase.raw, nil, testCase.questionType)
			if len(got) != len(testCase.want) {
				t.Fatalf("expected %v, got %v", testCase.want, got)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("expected %v, got %v", testCase.want, got)
				}
			}
		})
	}
}
