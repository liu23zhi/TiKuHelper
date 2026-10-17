package service

import (
	"encoding/json"
	stderrs "errors"
	"reflect"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestNormalizeAIFallbackAnswersForRequestPreservesRepeatedFillAnswers(t *testing.T) {
	got := normalizeAIFallbackAnswersForRequest([]string{"北京", "北京"}, model.SearchRequest{
		Type: 2,
	})
	if !reflect.DeepEqual(got, []string{"北京", "北京"}) {
		t.Fatalf("expected repeated fill answers to stay in order, got %v", got)
	}

	got = normalizeAIFallbackAnswersForRequest([]string{"北京", "北京"}, model.SearchRequest{
		Type: 0,
	})
	if !reflect.DeepEqual(got, []string{"北京"}) {
		t.Fatalf("expected non-fill answers to remain deduplicated, got %v", got)
	}
}

// 回归：所有非客观题都必须保留重复答案，不只有 type 2。
//
// 历史实现用 `questionType == 2` 判断，type 4 / -4 的重复答案被错误去重，
// 会让答案数与空数对不上，前端按序填答就会错位。
func TestNormalizeAIFallbackAnswersForRequestPreservesRepeatedAnswersForAllNonObjectiveTypes(t *testing.T) {
	nonObjectiveTypes := []struct {
		name         string
		questionType int
	}{
		{name: "填空题/连线题", questionType: 2},
		{name: "简答题/阅读理解", questionType: 4},
		{name: "阅读理解", questionType: -4},
	}

	for _, testCase := range nonObjectiveTypes {
		t.Run(testCase.name, func(t *testing.T) {
			// 无选项路径
			got := normalizeAIFallbackAnswersForRequest([]string{"春天", "春天"}, model.SearchRequest{Type: testCase.questionType})
			if !reflect.DeepEqual(got, []string{"春天", "春天"}) {
				t.Fatalf("expected repeated answers preserved without options, got %v", got)
			}

			// 有选项但答案映射不到选项（线上 options 被编辑器脚本污染的情形）
			got = normalizeAIFallbackAnswersForRequest([]string{"春天", "春天"}, model.SearchRequest{
				Type:    testCase.questionType,
				Options: []string{"段落格式字体字号", `var wordNum = "";`},
			})
			if !reflect.DeepEqual(got, []string{"春天", "春天"}) {
				t.Fatalf("expected repeated answers preserved with unmappable options, got %v", got)
			}
		})
	}

	// 客观题仍然去重。
	for _, questionType := range []int{0, 1, 3} {
		got := normalizeAIFallbackAnswersForRequest([]string{"北京", "北京"}, model.SearchRequest{Type: questionType})
		if !reflect.DeepEqual(got, []string{"北京"}) {
			t.Fatalf("expected objective type %d to deduplicate, got %v", questionType, got)
		}
	}
}

func TestIsAISearchConfigUsableRequiresBaseURLAPIKeyAndModel(t *testing.T) {
	if IsAISearchConfigUsable(AISearchRequestConfig{}) {
		t.Fatal("expected empty config to be unusable")
	}

	if IsAISearchConfigUsable(AISearchRequestConfig{BaseURL: "https://ai.example.com", APIKey: "sk-test"}) {
		t.Fatal("expected config without model to be unusable")
	}

	if !IsAISearchConfigUsable(AISearchRequestConfig{BaseURL: "https://ai.example.com", APIKey: "sk-test", Model: "gpt-4o-mini"}) {
		t.Fatal("expected full config to be usable")
	}
}

func TestBuildAIFallbackUnavailableResultReturnsAIUnavailableCode(t *testing.T) {
	result := BuildAIFallbackUnavailableResult()

	if result.Success {
		t.Fatal("expected unavailable result success=false")
	}
	if result.Error == nil {
		t.Fatal("expected unavailable result to include error")
	}
	if result.Error.Code != AIFallbackErrorCodeAIUnavailable {
		t.Fatalf("expected AI_UNAVAILABLE, got %s", result.Error.Code)
	}
}

func TestSearchAIFallbackWithDBReturnsAIUnavailableWhenConfigMissing(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if result.Success {
		t.Fatal("expected unavailable result when config is missing")
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeAIUnavailable {
		t.Fatalf("expected AI_UNAVAILABLE, got %+v", result.Error)
	}
}

func TestSearchAIFallbackWithDBReturnsNoAnswerWhenAnswerIsUncertain(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"无法确定"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if result.Success {
		t.Fatal("expected no-answer result for uncertain output")
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeNoAnswer {
		t.Fatalf("expected NO_ANSWER, got %+v", result.Error)
	}
}

func TestSearchAIFallbackWithDBKeepsUsableAnswerFromMixedOutput(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{" ", "不确定", "B"}}, internalsearch.AISearchUsage{
			InputTokens:  1000,
			OutputTokens: 500,
			TotalTokens:  1500,
		}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if !result.Success {
		t.Fatalf("expected success result, got %+v", result)
	}
	if result.Result == nil || result.Result.Answer != "2" {
		t.Fatalf("expected cleaned answer 2, got %+v", result.Result)
	}
	if result.Model != "gpt-4o-mini" {
		t.Fatalf("expected fallback model to be preserved, got %q", result.Model)
	}
	if result.Usage.InputTokens != 1000 || result.Usage.OutputTokens != 500 || result.Usage.TotalTokens != 1500 {
		t.Fatalf("expected fallback usage to be preserved, got %+v", result.Usage)
	}
}

func TestSearchAIFallbackWithDBReturnsUnsafeToAnswerWhenAIRefuses(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"无法回答"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if result.Success {
		t.Fatal("expected unsafe-to-answer result")
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeUnsafeToAnswer {
		t.Fatalf("expected UNSAFE_TO_ANSWER, got %+v", result.Error)
	}
}

// 回归：多选题模型把选中的多个选项拆成 answers 数组的多个元素时，
// 兜底链路必须把它们视为**同一个答案集合**。
//
// 线上事故：`{"answers":["A. ...","B. ...","C. ...","D. ..."]}` 被拆成 4 个单元素组，
// FillAnswerResponse 的多选题分支要求每组命中 >1 个选项 → 全部丢弃 → NO_ANSWER。
func TestNormalizeAIFallbackAnswersForRequestMergesMultiSelectArrayItems(t *testing.T) {
	options := []string{
		"A. 让对方了解你的独特性",
		"B. 展示你清晰的求职目标",
		"C. 获得对方对你KST的信任",
		"D. 提供对方足够多的好处",
	}
	answers := []string{
		"A. 让对方了解你的独特性",
		"B. 展示你清晰的求职目标",
		"C. 获得对方对你KST的信任",
		"D. 提供对方足够多的好处",
	}

	got := normalizeAIFallbackAnswersForRequest(answers, model.SearchRequest{
		Question: "要想获得朋友、亲戚、老师等人际网络的职业机会推荐，应该：",
		Options:  options,
		Type:     1,
	})
	want := []string{
		"让对方了解你的独特性",
		"展示你清晰的求职目标",
		"获得对方对你KST的信任",
		"提供对方足够多的好处",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected multi-select answers to merge into one group, got %v", got)
	}
}

// 回归：判断题模型返回 对/错/√/×/true/false 时，也要映射到「正确/错误」选项。
func TestNormalizeAIFallbackAnswersForRequestMapsJudgementVariants(t *testing.T) {
	cases := map[string]string{
		"对":     "正确",
		"√":     "正确",
		"true":  "正确",
		"错":     "错误",
		"×":     "错误",
		"false": "错误",
	}
	for answer, want := range cases {
		got := normalizeAIFallbackAnswersForRequest([]string{answer}, model.SearchRequest{
			Options: []string{"A. 正确", "B. 错误"},
			Type:    3,
		})
		if !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("answer %q: expected %q, got %v", answer, want, got)
		}
	}
}

// 端到端：真实线上数据（多选题 + A/B/C/D 分开返回）必须成功，而不是 NO_ANSWER。
func TestSearchAIFallbackWithDBKeepsSeparatedMultiSelectAnswers(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if _, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-6.1-sol",
	}); err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{
			"A. 让对方了解你的独特性",
			"B. 展示你清晰的求职目标",
			"C. 获得对方对你KST的信任",
			"D. 提供对方足够多的好处",
		}}, internalsearch.AISearchUsage{InputTokens: 16000, OutputTokens: 423}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{
		Question: "要想获得朋友、亲戚、老师等人际网络的职业机会推荐，应该：",
		Options: []string{
			"A. 让对方了解你的独特性",
			"B. 展示你清晰的求职目标",
			"C. 获得对方对你KST的信任",
			"D. 提供对方足够多的好处",
		},
		Type: 1,
	})
	if !result.Success || result.Result == nil {
		t.Fatalf("expected multi-select fallback to succeed, got %+v", result)
	}
	want := "让对方了解你的独特性#展示你清晰的求职目标#获得对方对你KST的信任#提供对方足够多的好处"
	if result.Result.Answer != want {
		t.Fatalf("expected %q, got %q", want, result.Result.Answer)
	}
}

// 回归：填空题/连线题（type=2）是非客观题，答案允许是自由文本。
//
// 线上事故：上游把 UEditor 的 JS 代码当成选项一起传上来（55 条 options），
// 早期实现要求答案必须命中选项，于是 AI 正常给出的文本答案被判为 NO_ANSWER，
// 而同一道题走 /search 却能成功（FillAnswerResponse 对非客观题直接采用文本）。
func TestSearchAIFallbackWithDBKeepsFreeTextAnswerForFillInQuestions(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-6.1-sol"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()

	const longAnswer = "常见的科技发明有电话、计算机、互联网、飞机、汽车、电视机、智能手机、机器人和人工智能等。课后我可以认真学习相关内容，主动查阅资料、整理笔记并及时复习。"
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{longAnswer}}, internalsearch.AISearchUsage{InputTokens: 1680, OutputTokens: 111}, nil
	}

	// 复刻线上形态：选项里混入编辑器脚本。
	options := []string{
		"段落格式字体字号点击上传x",
		`var wordNum = "";`,
		`if (wordNum == "") {`,
		`window.UEDITOR_CONFIG.wordCount = false;`,
		`editor1.addListener('contentChange', function() {`,
	}
	for i := 0; i < 50; i++ {
		options = append(options, "// filler")
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{
		Question: "请简单列举常见的科技发明。课后是否可以好好的学习相关内容？",
		Options:  options,
		Type:     2,
	})

	if !result.Success {
		t.Fatalf("expected the free-text answer to be kept, got %+v", result.Error)
	}
	if result.Result == nil || result.Result.Answer != longAnswer {
		t.Fatalf("expected the original answer text, got %+v", result.Result)
	}
}

// 非客观题里，AI 返回选项标号时仍应映射成选项原文。
func TestSearchAIFallbackWithDBMapsOptionLabelForFillInQuestions(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"B"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{
		Question: "匹配题：把左侧术语与右侧案例对应起来",
		Options:  []string{"第一组案例", "第二组案例"},
		Type:     2,
	})

	if !result.Success {
		t.Fatalf("expected success, got %+v", result.Error)
	}
	if result.Result == nil || result.Result.Answer != "第二组案例" {
		t.Fatalf("expected option label B to map to 第二组案例, got %+v", result.Result)
	}
}

// 非客观题里，纯粹是「选项标号」但映射失败的答案仍应丢弃，避免把错标号当答案。
func TestSearchAIFallbackWithDBDropsUnmappableOptionNumbersForFillInQuestions(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()

	// "9" 越界（只有 2 个选项）→ 说明标号对不上，不能当答案。
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"9"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{
		Question: "匹配题",
		Options:  []string{"甲", "乙"},
		Type:     2,
	})

	if result.Success {
		t.Fatalf("expected an unmappable option number to be dropped, got %+v", result.Result)
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeNoAnswer {
		t.Fatalf("expected NO_ANSWER, got %+v", result.Error)
	}
}

// 客观题（单选）仍然必须命中选项：越界答案不能被当作自由文本采用。
func TestSearchAIFallbackWithDBStillRejectsChoiceAnswerOutsideOptions(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"火龙果"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "选择苹果", Options: []string{"苹果", "香蕉"}, Type: 0})

	if result.Success {
		t.Fatalf("expected the out-of-options choice answer to be rejected, got %+v", result.Result)
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeNoAnswer {
		t.Fatalf("expected NO_ANSWER, got %+v", result.Error)
	}
}

func TestSearchAIFallbackWithDBReturnsUpstreamErrorWhenAISearchFails(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return nil, internalsearch.AISearchUsage{}, stderrs.New("upstream timeout Authorization: Bearer sk-test")
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if result.Success {
		t.Fatal("expected upstream error result")
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeUpstreamError {
		t.Fatalf("expected UPSTREAM_ERROR, got %+v", result.Error)
	}
	if !strings.Contains(result.FailureDetail, "upstream timeout") || strings.Contains(result.FailureDetail, "sk-test") {
		t.Fatalf("expected preserved and redacted failure detail, got %q", result.FailureDetail)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "upstream timeout") || strings.Contains(string(raw), "failureDetail") {
		t.Fatalf("internal upstream details leaked into public result: %s", raw)
	}
}

func TestSearchAIFallbackWithDBMapsChoiceLetterToRealOptionText(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"B"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if !result.Success {
		t.Fatalf("expected success result, got %+v", result)
	}
	if result.Result == nil || result.Result.Answer != "2" {
		t.Fatalf("expected mapped answer 2, got %+v", result.Result)
	}
}

func TestSearchAIFallbackWithDBRejectsChoiceAnswerOutsideProvidedOptions(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"火龙果"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "选择苹果", Options: []string{"苹果", "香蕉"}, Type: 0})

	if result.Success {
		t.Fatalf("expected no answer result, got %+v", result)
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeNoAnswer {
		t.Fatalf("expected NO_ANSWER, got %+v", result.Error)
	}
}

func TestSearchAIFallbackWithDBMapsErrTargetNoAnswerToNoAnswerCode(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return nil, internalsearch.AISearchUsage{}, pkgerrors.ErrTargetNoAnswer
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if result.Success {
		t.Fatal("expected no answer result")
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeNoAnswer {
		t.Fatalf("expected NO_ANSWER, got %+v", result.Error)
	}
}

func TestSearchAIFallbackWithDBIgnoresLocalQuestionCacheRecords(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if err := db.AutoMigrate(&entity.Tiku{}); err != nil {
		t.Fatalf("migrate tiku table: %v", err)
	}
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}
	if err := db.Create(&entity.Tiku{
		Question:   "1+1=?",
		Type:       0,
		Options:    `["1","2"]`,
		Answer:     `["1"]`,
		Hash:       "preseeded-ai-fallback-cache-hit",
		CourseName: "",
		Extra:      "",
		SourceName: "缓存旧答案",
	}).Error; err != nil {
		t.Fatalf("seed local question cache: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"B"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})
	if !result.Success {
		t.Fatalf("expected success result, got %+v", result)
	}
	if result.Result == nil || result.Result.Answer != "2" {
		t.Fatalf("expected ai fallback answer 2 instead of local cached answer, got %+v", result.Result)
	}
}

func TestSearchAIFallbackWithDBDoesNotWriteQuestionCacheRecords(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	if err := db.AutoMigrate(&entity.Tiku{}); err != nil {
		t.Fatalf("migrate tiku table: %v", err)
	}
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return [][]string{{"B"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})
	if !result.Success {
		t.Fatalf("expected success result, got %+v", result)
	}

	var count int64
	if err := db.Model(&entity.Tiku{}).Count(&count).Error; err != nil {
		t.Fatalf("count tiku records: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected ai fallback to skip local question cache writes, got %d records", count)
	}
}
