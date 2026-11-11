package search

import (
	"reflect"
	"testing"
)

// P0-4 选项标签扩展的回归钉子。
//
// 缺陷背景（t67 实测，t82 复核）：
//
//	tikuOptionLabelPattern 旧值 `^[A-Z][.．:：、)]\s*` 只认「大写字母 + 分隔符」，
//	不认小写、不认成对括号。实测剥离结果：
//	    "a.甲"   -> "a.甲"    （没剥）
//	    "（A）甲" -> "（A）甲"  （没剥）
//	    "【A】甲" -> "【A】甲"  （没剥）
//	后果：同一道题，题库用 `A.甲` 写法、客户端传 `a.甲` 或 `（A）甲` 写法时，
//	normalizedTikuOptions 得到不同结果 → compatibleTikuOptions 判不等
//	→ 整条候选被丢弃（漏答）。

// TestTikuOptionLabelPatternStripsLabels 覆盖任务要求的 5 组，另加扩展形态。
func TestTikuOptionLabelPatternStripsLabels(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		// —— 任务要求第 1、2、3 组：本次新增的形态 ——
		{"小写点号（第1组）", "a.甲", "甲"},
		{"全角圆括号（第2组）", "（A）甲", "甲"},
		{"方头括号（第3组）", "【A】甲", "甲"},

		// —— 任务要求第 4 组：原有功能不能坏 ——
		{"大写点号（第4组）", "A.甲", "甲"},

		// —— 任务要求第 5 组：无标签不动 ——
		{"无标签（第5组）", "甲", "甲"},

		// —— 扩展：其它真实存在的标号形态 ——
		{"小写右括号", "a)甲", "甲"},
		{"小写顿号", "a、甲", "甲"},
		{"大写顿号", "A、甲", "甲"},
		{"全角句点", "A．甲", "甲"},
		{"全角冒号", "A：甲", "甲"},
		{"半角冒号", "A:甲", "甲"},
		{"半角右括号", "A)甲", "甲"},
		{"全角右括号", "A）甲", "甲"},
		{"半角圆括号", "(A)甲", "甲"},
		{"半角方括号", "[A]甲", "甲"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := tikuOptionLabelPattern.ReplaceAllString(tt.in, "")
			if got != tt.want {
				t.Fatalf("剥离 %q = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestTikuOptionLabelPatternKeepsLegitimateContent 是 P0-4 的**误删保护**。
//
// 放宽正则最容易踩的坑：把「A类物质」「AB两项」这类正常选项的首字母削掉。
// 一旦误删，compatibleTikuOptions 会判不等 → 整条候选被丢，比「漏剥标号」更糟。
//
// 设计上的保证：标号后**必须有分隔符或闭合括号**。以下用例钉住这个约束。
func TestTikuOptionLabelPatternKeepsLegitimateContent(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
	}{
		{"字母后直接接汉字", "A类物质"},
		{"字母后直接接汉字2", "a类物质"},
		{"两个字母连写", "AB两项"},
		{"单个字母无分隔符", "A"},
		{"单个小写字母", "a"},
		{"纯汉字", "甲"},
		{"纯汉字长句", "甲方案优于乙方案"},
		{"字母在中间", "选项A是最优的"},
		{"括号无字母", "（甲）"},
		{"括号内多字母", "（AB）两项"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := tikuOptionLabelPattern.ReplaceAllString(tt.in, "")
			if got != tt.in {
				t.Fatalf("不应改动 %q，实际被改成 %q（误删正当内容）", tt.in, got)
			}
		})
	}
}

// TestCompatibleTikuOptionsUnifiesLabelVariants 端到端钉住「标号写法不同但选项相同」。
//
// 这条是 P0-4 真正的价值所在：单看正则剥离对不对不够，
// 要确认 compatibleTikuOptions 最终能把不同标号写法判为同一个选项集合。
//
// ⚠️ 这是「比较两侧」类判定，按第七形态的风险，必须同时有函数级断言
// （上面两条）—— 否则两侧同错也会判等。
func TestCompatibleTikuOptionsUnifiesLabelVariants(t *testing.T) {
	for _, tt := range []struct {
		name     string
		left     []string
		right    []string
		wantSame bool
	}{
		{
			name: "小写对大写", left: []string{"a.甲", "b.乙"}, right: []string{"A.甲", "B.乙"},
			wantSame: true,
		},
		{
			name: "全角圆括号对点号", left: []string{"（A）甲", "（B）乙"}, right: []string{"A.甲", "B.乙"},
			wantSame: true,
		},
		{
			name: "方头括号对点号", left: []string{"【A】甲", "【B】乙"}, right: []string{"A.甲", "B.乙"},
			wantSame: true,
		},
		{
			name: "混合形态", left: []string{"a.甲", "【B】乙", "（C）丙"}, right: []string{"A.甲", "B.乙", "C.丙"},
			wantSame: true,
		},
		{
			name: "内容不同必须判不等", left: []string{"a.甲", "b.乙"}, right: []string{"A.甲", "B.丙"},
			wantSame: false,
		},
		{
			name: "无标签对带标签", left: []string{"甲", "乙"}, right: []string{"A.甲", "B.乙"},
			wantSame: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := compatibleTikuOptions(tt.left, tt.right)
			if got != tt.wantSame {
				t.Fatalf("compatibleTikuOptions(%v, %v) = %v, want %v",
					tt.left, tt.right, got, tt.wantSame)
			}
		})
	}
}

// TestNormalizedTikuOptionsLabelVariantsAgree 是上面那条的**函数级对照**。
//
// 第七形态预防：compatibleTikuOptions 是比较两侧是否相等，
// 若归一化本身坏了（两侧同错），端到端仍可能判等。
// 这里直接断言归一化后的结果，堵住这个盲区。
func TestNormalizedTikuOptionsLabelVariantsAgree(t *testing.T) {
	cases := [][]string{
		{"a.甲", "b.乙"},
		{"（A）甲", "（B）乙"},
		{"【A】甲", "【B】乙"},
		{"A.甲", "B.乙"},
		{"甲", "乙"},
	}
	want := []string{"乙", "甲"} // normalizedTikuOptions 内部 sort.Strings
	for _, options := range cases {
		got := normalizedTikuOptions(options)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("normalizedTikuOptions(%v) = %v, want %v", options, got, want)
		}
	}
}

// TestNormalizedTikuOptionsKeepsContentAfterLabelPatternChange 钉住误删不会影响归一化结果。
//
// 「A类物质」这类正常选项在归一化后必须保留完整内容 ——
// 若正则误删首字母，这里会得到「类物质」而非「A类物质」。
func TestNormalizedTikuOptionsKeepsContentAfterLabelPatternChange(t *testing.T) {
	got := normalizedTikuOptions([]string{"A类物质", "AB两项"})
	// normalizeTikuText 会小写化；sort.Strings 按字节序：'b'(0x62) < '类'(0xE7...)
	// 所以 "ab两项" 排在 "a类物质" 之前。
	want := []string{"ab两项", "a类物质"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizedTikuOptions = %v, want %v（正则误删了正常内容的首字母？）", got, want)
	}
	// 更强的断言：直接确认首字母还在（不依赖排序知识的表述）。
	seen := map[string]bool{}
	for _, v := range got {
		seen[v] = true
	}
	if !seen["a类物质"] {
		t.Fatalf("「A类物质」归一化后应保留首字母 A（小写为 a），got %v", got)
	}
	if !seen["ab两项"] {
		t.Fatalf("「AB两项」归一化后应保留 AB，got %v", got)
	}
}
