package util

import (
	"reflect"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestFillAnswerResponsePreservesTextAnswersForNonObjectiveTypesWithOptions(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"左侧术语对应右侧案例"}}, &model.SearchRequest{
		Question: "请完成连线",
		Options:  []string{"选项一", "选项二"},
		Type:     15,
	})

	expected := []string{"左侧术语对应右侧案例"}
	if !reflect.DeepEqual(resp.Answer.BestAnswer, expected) {
		t.Fatalf("expected best answer %v, got %v", expected, resp.Answer.BestAnswer)
	}
	if resp.Answer.AnswerText != "左侧术语对应右侧案例" {
		t.Fatalf("expected answer text to preserve raw text answer, got %q", resp.Answer.AnswerText)
	}
	if len(resp.Answer.AnswerKey) != 0 {
		t.Fatalf("expected no answer key for non-objective text answers, got %v", resp.Answer.AnswerKey)
	}
}

func TestFillAnswerResponsePreservesAnswerTextForReaderStyleTypes(t *testing.T) {
	resp := FillAnswerResponse([][]string{{"先分析材料，再给出结论"}}, &model.SearchRequest{
		Question: "请阅读材料后作答",
		Options:  []string{"材料一", "材料二"},
		Type:     4,
	})

	expected := []string{"先分析材料，再给出结论"}
	if !reflect.DeepEqual(resp.Answer.BestAnswer, expected) {
		t.Fatalf("expected best answer %v, got %v", expected, resp.Answer.BestAnswer)
	}
	if resp.Answer.AnswerText != "先分析材料，再给出结论" {
		t.Fatalf("expected answer text to preserve reader style text answer, got %q", resp.Answer.AnswerText)
	}
	if len(resp.Answer.AnswerKey) != 0 {
		t.Fatalf("expected no answer key for reader style text answers, got %v", resp.Answer.AnswerKey)
	}
}
