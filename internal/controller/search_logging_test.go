package controller

import (
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/pkg/model"
)

func TestBuildSearchRequestDebugSummaryIncludesQuestionTypeAndShape(t *testing.T) {
	summary := buildSearchRequestDebugSummary(model.SearchRequest{
		Question:   "请指出下列案例属于何种类比创新方法?",
		Type:       2,
		Options:    nil,
		CourseName: "创新方法学",
		Extra:      "题干来自截图 OCR",
	})

	checks := []string{
		"type=2",
		"typeLabel=填空题/连线题",
		"options=0",
		"hasCourseName=true",
		"hasExtra=true",
		"question=请指出下列案例属于何种类比创新方法?",
	}
	for _, check := range checks {
		if !strings.Contains(summary, check) {
			t.Fatalf("expected summary %q to contain %q", summary, check)
		}
	}
}

func TestBuildSearchResponseDebugSummaryIncludesAnswerSummary(t *testing.T) {
	summary := buildSearchResponseDebugSummary(model.SearchResponse{
		Type: 15,
		Answer: model.Answer{
			BestAnswer: []string{"左侧术语对应右侧案例"},
			AllAnswer:  [][]string{{"左侧术语对应右侧案例"}},
			AnswerText: "左侧术语对应右侧案例",
		},
	})

	checks := []string{
		"type=15",
		"typeLabel=阅读理解",
		"bestAnswerCount=1",
		"allAnswerGroups=1",
		"answerText=左侧术语对应右侧案例",
	}
	for _, check := range checks {
		if !strings.Contains(summary, check) {
			t.Fatalf("expected summary %q to contain %q", summary, check)
		}
	}
}
