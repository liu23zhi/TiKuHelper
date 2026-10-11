package controller

// hasUsableSearchAnswers 判断一组答案里是否存在非空分组。
func hasUsableSearchAnswers(answers [][]string) bool {
	for _, group := range answers {
		if len(group) > 0 {
			return true
		}
	}
	return false
}

// cloneSearchAnswers 深拷贝二维答案切片。
//
// 缓存存取两侧都用它：避免调用方与缓存条目共享同一块底层数组，
// 任一方后续修改都会串改另一方的数据。
func cloneSearchAnswers(answers [][]string) [][]string {
	cloned := make([][]string, len(answers))
	for i := range answers {
		cloned[i] = append([]string(nil), answers[i]...)
	}
	return cloned
}
