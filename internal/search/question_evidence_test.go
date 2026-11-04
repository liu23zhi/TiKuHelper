package search

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestStructuredQuestionEvidencePreservesImageOrderAndAlt(t *testing.T) {
	req := model.SearchRequest{Question: "图片题 https://example.com/legacy.png", Type: -1, QuestionEvidence: &model.QuestionEvidence{
		StemImages:   []model.QuestionImageEvidence{{URL: "https://example.com/stem.png", Alt: "x≤4 的函数图", Index: 0}, {Alt: "懒加载未取得地址的辅助文字", Index: 1}},
		OptionImages: []model.QuestionImageEvidence{{URL: "https://example.com/option.png", Alt: "选项甲", Index: 0}, {URL: "https://example.com/stem.png", Index: 1}, {URL: "data:image/png;base64,AAAA", Index: 2}},
	}}
	urls := extractAISearchURLs(req)
	if !reflect.DeepEqual(urls, []string{"https://example.com/stem.png", "https://example.com/option.png", "https://example.com/legacy.png"}) {
		t.Fatalf("image order or compatibility changed: %v", urls)
	}
	prompt := buildAISearchPrompt(req)
	for _, expected := range []string{"x≤4 的函数图", "懒加载未取得地址的辅助文字", "选项甲", "alt仅为辅助文字"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing structured image context: %s", expected)
		}
	}
}

func TestLocalTikuSearchSharesBudgetCachesQueriesAndRecordsFailures(t *testing.T) {
	tool := &testAITikuSearchTool{result: AITikuSearchResult{MatchKind: "none", Candidates: []AITikuSearchCandidate{}}}
	client := &ChatGptClient{TikuSearchTool: tool}
	web, local := 1, 0
	cache := map[string]string{}
	usage := AISearchUsage{}
	call := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: "tiku_search", Arguments: `{"question":"无命中题目","type":-1,"options":[]}`}}
	first, abort := client.executeAITikuSearchTool(context.Background(), call, &web, &local, 2, cache, &usage)
	if abort || local != 1 || tool.calls != 1 || len(usage.TikuSearchCalls) != 1 || usage.TikuSearchCalls[0].Status != "success" {
		t.Fatalf("successful no-hit query must be counted: %+v local=%d", usage, local)
	}
	second, abort := client.executeAITikuSearchTool(context.Background(), call, &web, &local, 2, cache, &usage)
	if abort || first != second || tool.calls != 1 || len(usage.TikuSearchCalls) != 1 || len(usage.Trace) != 1 {
		t.Fatalf("cached query must not consume billing sequence: %+v", usage)
	}
	call.Function.Arguments = `{"question":"另一个题目","type":-1,"options":[]}`
	_, abort = client.executeAITikuSearchTool(context.Background(), call, &web, &local, 2, cache, &usage)
	if !abort || tool.calls != 1 || local != 1 {
		t.Fatal("web and local calls must share search budget")
	}
	tool.err = errors.New("isolated database error")
	_, _ = client.executeAITikuSearchTool(context.Background(), call, &web, &local, 3, cache, &usage)
	if tool.calls != 2 || usage.TikuSearchCalls[len(usage.TikuSearchCalls)-1].Status != "error" {
		t.Fatal("database failure must be recorded as nonbillable error")
	}
}

func TestLocalTikuToolAcceptsLongStructuredArguments(t *testing.T) {
	question := strings.Repeat("题干", 1500)
	arguments := `{"question":"` + question + `","type":-1,"options":[]}`
	if len(arguments) <= aiToolMaxArgumentBytes {
		t.Fatal("test must exceed old web-search argument limit")
	}
	if len(arguments) > aiToolArgumentLimit("tiku_search") {
		t.Fatal("valid long question exceeds tiku tool transport allowance")
	}
	tool := &testAITikuSearchTool{result: AITikuSearchResult{MatchKind: "none"}}
	client := &ChatGptClient{TikuSearchTool: tool}
	web, local := 1, 0
	usage := AISearchUsage{}
	call := openAIToolCall{Type: "function", Function: openAIToolFunction{Name: "tiku_search", Arguments: arguments}}
	_, abort := client.executeAITikuSearchTool(context.Background(), call, &web, &local, 2, map[string]string{}, &usage)
	if abort || tool.calls != 1 || usage.TikuSearchCalls[0].Status != "success" {
		t.Fatalf("long local question was rejected: %+v", usage)
	}
	recorder := httptest.NewRecorder()
	writeAITikuSearchToolStream(recorder, "long-tiku", map[string]any{"question": question, "type": -1, "options": []string{}})
	stream, err := readAIToolStream(strings.NewReader(recorder.Body.String()))
	if err != nil || len(stream.Calls) != 1 {
		t.Fatalf("long tiku SSE transport rejected: %+v err=%v", stream.Calls, err)
	}
}
