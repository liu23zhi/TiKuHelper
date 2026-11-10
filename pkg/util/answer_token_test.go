package util

import (
	"reflect"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

// 本文件钉住「判断题极性同义词表」与「答案 token 统一清洗」两组行为。
//
// 依据（上游 chaoxing-exam/README.md:30）：
//
//	「视觉模型的 prompt 明确要求：『不要分析、不要给答案，只输出文字』」
//
// 上游把「看」与「想」拆开，是为了让下游拿到**结构化、干净**的输入。
// 本项目对应的一层是：provider 给出的答案文本必须先清洗成可比较的 token，
// 否则「有答案」会被误判成「无答案」—— 这是准确率最直接的损失。
//
// 另一条依据（chaoxingexam/readme.md 表格）：
// 客观题与主观题分开解析，题干+选项+参考答案分别处理 ——
// 说明参考答案的**文本形态**与选项文本不同，必须单独归一化。

// 判断题：每个新增同义词都要能映射到正确选项。
//
// 若把 judgementPolarityTable 里的条目录删掉，对应用例会变红。
func TestJudgementPolaritySynonymsMapToOptions(t *testing.T) {
	for _, tt := range []struct {
		name     string
		answer   string
		options  []string
		wantBest string
		wantKey  string
	}{
		// —— 真 ——
		{"真", "真", []string{"正确", "错误"}, "正确", "A"},
		{"正确的", "正确的", []string{"正确", "错误"}, "正确", "A"},
		// 注意：选项写成「对/错」时，FormatOptions 会用 IsTrue/IsFalse
		// 把它们改写成「正确/错误」，因此期望值是改写后的文本 ——
		// 这正是 TestFormatOptionsNormalizesLetterOptionsForJudgementButPolarityDoesNot
		// 钉住的选项侧行为。
		{"对的", "对的", []string{"对", "错"}, "正确", "A"},
		{"是的", "是的", []string{"是", "否"}, "正确", "A"},
		{"right", "right", []string{"正确", "错误"}, "正确", "A"},
		{"勾号", "✓", []string{"正确", "错误"}, "正确", "A"},
		{"粗勾号", "✔", []string{"正确", "错误"}, "正确", "A"},

		// —— 假 ——
		{"不是", "不是", []string{"正确", "错误"}, "错误", "B"},
		{"不对", "不对", []string{"正确", "错误"}, "错误", "B"},
		{"假", "假", []string{"正确", "错误"}, "错误", "B"},
		{"错误的", "错误的", []string{"正确", "错误"}, "错误", "B"},
		{"错的", "错的", []string{"对", "错"}, "错误", "B"},
		{"叉号", "✗", []string{"正确", "错误"}, "错误", "B"},
		{"粗叉号", "✘", []string{"正确", "错误"}, "错误", "B"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{{tt.answer}}, &model.SearchRequest{
				Question: "判断",
				Options:  append([]string(nil), tt.options...),
				Type:     3,
			})
			if !reflect.DeepEqual(resp.Answer.BestAnswer, []string{tt.wantBest}) {
				t.Fatalf("want %v, got %v", tt.wantBest, resp.Answer.BestAnswer)
			}
			if resp.Answer.AnswerKeyText != tt.wantKey {
				t.Fatalf("want key %q, got %q", tt.wantKey, resp.Answer.AnswerKeyText)
			}
		})
	}
}

// 极性表必须仍然**不认字母**：这是与选项侧 IsTrue/IsFalse 的关键分界。
// 若有人把 A/B 加进答案侧词表，答案为 "A" 的判断题会无视选项顺序直接判「正确」，
// 遇到 A=错误/B=正确的题库就系统性答反。
func TestJudgementPolarityStillRejectsLetters(t *testing.T) {
	for _, letter := range []string{"A", "B", "a", "b", "C", "D"} {
		if _, ok := JudgementPolarity(letter); ok {
			t.Fatalf("JudgementPolarity 必须不认字母 %q（字母是标号不是极性）", letter)
		}
	}
}

// 极性表大小写不敏感、且能吃全角。
func TestJudgementPolarityCaseAndWidthInsensitive(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  bool
	}{
		{"TRUE", true},
		{"True", true},
		{"FALSE", false},
		{"False", false},
		{"YES", true},
		{"No", false},
		{"  正确  ", true}, // 首尾空白
	} {
		got, ok := JudgementPolarity(tt.value)
		if !ok {
			t.Fatalf("%q 应被识别为极性", tt.value)
		}
		if got != tt.want {
			t.Fatalf("%q 极性应为 %v，got %v", tt.value, tt.want, got)
		}
	}
}

// normalizeAnswerToken 的逐条规则用例。
//
// 每条规则一个用例：删掉对应规则，对应用例必须变红。
// 注意：清洗**不得**改动正文语义 —— 最后几组用例专门保护这一点。
func TestNormalizeAnswerToken(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		// 规则 1：全角转半角
		{"全角字母", "Ａ", "A"},
		{"全角字母串", "ＡＢＤ", "ABD"},
		{"全角括号", "（A）", "(A)"},

		// 规则 2：剥「答案」字样前缀（括号式）
		{"方括号答案", "【答案】A", "A"},
		{"方头括号答案", "【答案】还原反应", "还原反应"},
		{"方括号参考答案", "[参考答案]B", "B"},
		{"圆括号答案", "（答案）C", "C"},

		// 规则 2b：剥「答案」字样前缀（冒号式）
		{"答案冒号", "答案：A", "A"},
		{"参考答案冒号", "参考答案: B", "B"},
		{"正确答案全角冒号", "正确答案：正确", "正确"},

		// 规则 2c：嵌套前缀（括号式 + 冒号式）
		{"嵌套前缀", "【答案】：A", "A"},

		// 规则 3：剥「选」前缀（仅当剩余很短）
		{"选前缀", "选A", "A"},
		{"选前缀带括号", "选(A)", "(A)"},

		// 规则 4：剥选项标签前缀（与选项侧同源）
		{"点号标签", "B. 还原反应", "还原反应"},
		{"顿号标签", "C、38%", "38%"},
		{"冒号标签", "A：细胞膜", "细胞膜"},

		// 规则 5：裁首尾分隔符
		{"尾随顿号", "A、", "A"},
		{"尾随逗号", "A,", "A"},
		{"前导逗号", ",A", "A"},
		{"尾随分号", "A;", "A"},
		{"前后空白与逗号", "  B ,  ", "B"},

		// 保护：正文语义不得被改写
		{"普通正文不动", "还原反应", "还原反应"},
		{"正文含答案字样但无冒号", "答案是否定的", "答案是否定的"},
		{"长正文的选前缀", "选择题的正确答案是氧化反应", "选择题的正确答案是氧化反应"},
		{"空串", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAnswerToken(tt.in); got != tt.want {
				t.Fatalf("normalizeAnswerToken(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// 「答案：A」这类带前缀的答案必须真的能映射到选项 —— 端到端钉子。
//
// 这是本次修复的主要漏答场景：provider 返回「答案：还原反应」时，
// 旧实现只剥 `^[A-Za-z][.、:：)）]` 形式的标签，前缀留着，
// 字面匹配失败 → 整组答案被丢弃 → 返回空。
func TestFillAnswerResponseHandlesAnswerLabelPrefix(t *testing.T) {
	for _, tt := range []struct {
		name    string
		answer  string
		options []string
		want    []string
		index   int
	}{
		{"答案冒号单选", "答案：还原反应", []string{"氧化反应", "还原反应"}, []string{"还原反应"}, 1},
		{"方括号答案单选", "【答案】细胞壁", []string{"细胞膜", "细胞壁"}, []string{"细胞壁"}, 1},
		{"答案冒号判断题", "答案：正确", []string{"正确", "错误"}, []string{"正确"}, 0},
		{"全角答案判断题", "答案：错误", []string{"正确", "错误"}, []string{"错误"}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			qtype := 0
			if tt.name == "答案冒号判断题" || tt.name == "全角答案判断题" {
				qtype = 3
			}
			resp := FillAnswerResponse([][]string{{tt.answer}}, &model.SearchRequest{
				Question: "q",
				Options:  append([]string(nil), tt.options...),
				Type:     qtype,
			})
			if !reflect.DeepEqual(resp.Answer.BestAnswer, tt.want) {
				t.Fatalf("want %v, got %v", tt.want, resp.Answer.BestAnswer)
			}
			if !reflect.DeepEqual(resp.Answer.AnswerIndex, []int{tt.index}) {
				t.Fatalf("want index %d, got %v", tt.index, resp.Answer.AnswerIndex)
			}
		})
	}
}

// 「选A」前缀必须真的被剥掉，且**不能靠模糊匹配蒙过**。
//
// 这条用例是被变异验证逼出来的：最初的样本 "选氧化反应" 对
// ["氧化反应","还原反应"]，即使剥前缀的实现被短路，模糊匹配也会以
// 极高相似度命中同一个选项 → 用例照样绿 → 假钉子。
//
// 「选」前缀必须真的被剥掉。
//
// 判别力说明（这条用例是被变异验证逼出来的）：
//
//	第一版用单选的 "选氧化反应" 对 ["氧化反应","还原反应"] —— 即使剥前缀
//	的实现被短路，模糊匹配也会以极高相似度救回同一个选项，用例照样绿，
//	属于假钉子。
//
// 现在改为两层断言：
//  1. **函数级**：normalizeAnswerToken / answerLabelIndexes 必须把 "选A" 洗成 "A"。
//     这是最直接的钉子，剥前缀逻辑一旦失效立即变红。
//  2. **端到端**：多选题答案 "选AD" 必须得到 key "AD"。
//
// 两层都要，因为端到端那层还可能被其它路径兜住；函数级那层不会。
func TestFillAnswerResponseStripsChoiceMarkerPrefix(t *testing.T) {
	// —— 层 1：函数级 ——
	for _, tt := range []struct{ in, want string }{
		{"选A", "A"},
		{"选(A)", "(A)"},
		{"选AD", "AD"},
		{"选正确", "正确"},
	} {
		if got := normalizeAnswerToken(tt.in); got != tt.want {
			t.Fatalf("normalizeAnswerToken(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// 保护：长正文不得被剥（「选择题的正确答案是……」不能变成「题的正确答案是……」）。
	if got := normalizeAnswerToken("选择题的正确答案是氧化"); got != "选择题的正确答案是氧化" {
		t.Fatalf("长正文不该被剥「选」前缀，got %q", got)
	}

	// —— 层 2：端到端 ——
	options := []string{"氧化", "还原", "中和", "沉淀"}
	resp := FillAnswerResponse([][]string{{"选AD"}}, &model.SearchRequest{
		Question: "q", Options: append([]string(nil), options...), Type: 1,
	})
	if resp.Answer.AnswerKeyText != "AD" {
		t.Fatalf("want key %q, got %q (best=%v)", "AD", resp.Answer.AnswerKeyText, resp.Answer.BestAnswer)
	}
	if !reflect.DeepEqual(resp.Answer.BestAnswer, []string{"氧化", "沉淀"}) {
		t.Fatalf("want [氧化 沉淀], got %v", resp.Answer.BestAnswer)
	}
}

// 多选题的字母答案：分隔式与紧凑式都必须能解析。
//
// 依据（chaoxing-exam/README.md:166 与 SKILL.md 陷阱 4）：
//
//	`if (span.textContent.trim() === displayLetter)` —— 「用 textContent（显示字母）匹配」
//
// 客户端拿显示字母填答、题库存显示字母，服务端就必须能把字母串解析回选项。
func TestFillAnswerResponseResolvesLetterAnswersForMultipleChoice(t *testing.T) {
	options := []string{"氧化", "还原", "中和", "沉淀"}

	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"逗号分隔", "A,D", "AD"},
		{"顿号分隔", "A、D", "AD"},
		{"空格分隔", "A D", "AD"},
		{"紧凑", "AD", "AD"},
		{"紧凑三选", "ABC", "ABC"},
		{"小写", "a,d", "AD"},
		{"乱序紧凑", "DA", "AD"},
		{"带点号", "A.、D.", "AD"},
		{"带括号", "(A),(D)", "AD"},
		{"答案前缀", "答案：A,D", "AD"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{{tt.in}}, &model.SearchRequest{
				Question: "多选",
				Options:  append([]string(nil), options...),
				Type:     1,
			})
			if resp.Answer.AnswerKeyText != tt.want {
				t.Fatalf("want key %q, got %q (best=%v)", tt.want, resp.Answer.AnswerKeyText, resp.Answer.BestAnswer)
			}
		})
	}
}

// 多选字母解析的安全边界：不能让脏数据被「部分采纳」成半个答案。
func TestFillAnswerResponseRejectsUnsafeLetterAnswersForMultipleChoice(t *testing.T) {
	options := []string{"甲", "乙", "丙"}

	for _, tt := range []struct {
		name string
		in   string
	}{
		{"部分越界", "A,Z"},
		{"含无法解析成分", "A,丁"},
		{"重复标号", "A,A"},
		{"紧凑越界", "AZ"},
		{"英文单词", "acid"}, // 小写单词不得被当成 a/c/i/d 字母组合
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := FillAnswerResponse([][]string{{tt.in}}, &model.SearchRequest{
				Question: "多选",
				Options:  append([]string(nil), options...),
				Type:     1,
			})
			if len(resp.Answer.BestAnswer) != 0 {
				t.Fatalf("%q 不得被部分采纳，got %v", tt.in, resp.Answer.BestAnswer)
			}
		})
	}
}

// 单选题保持「单个纯字母不映射」的既有边界（本次未放宽）。
// 多选题的字母放宽**不得**顺带把单选也放开。
func TestFillAnswerResponseKeepsSingleChoiceLetterBoundary(t *testing.T) {
	options := []string{"甲", "乙", "丙"}
	for _, answer := range []string{"B", "b", "(B)", "B."} {
		resp := FillAnswerResponse([][]string{{answer}}, &model.SearchRequest{
			Question: "单选",
			Options:  append([]string(nil), options...),
			Type:     0,
		})
		if len(resp.Answer.BestAnswer) != 0 {
			t.Fatalf("type=0 的纯字母答案 %q 必须维持不映射，got %v", answer, resp.Answer.BestAnswer)
		}
	}
}

// 判断题只有两个选项，多标号在该题型上是脏数据，不该被解析。
func TestFillAnswerResponseRejectsMultiLetterAnswerForJudgement(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"A,B"}}, &model.SearchRequest{
		Question: "判断",
		Options:  []string{"正确", "错误"},
		Type:     3,
	})
	if len(resp.Answer.BestAnswer) != 0 {
		t.Fatalf("判断题的多标号答案必须被拒绝，got %v", resp.Answer.BestAnswer)
	}
}
