package service

import (
	stderrs "errors"
	"reflect"
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

func TestSearchAIFallbackWithDBReturnsUpstreamErrorWhenAISearchFails(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Enabled: true, BaseURL: "https://ai.example.com/v1", APIKey: "sk-test", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("seed ai config: %v", err)
	}

	original := runAIFallbackSearchWithUsage
	defer func() { runAIFallbackSearchWithUsage = original }()
	runAIFallbackSearchWithUsage = func(client *internalsearch.ChatGptClient, req model.SearchRequest) ([][]string, internalsearch.AISearchUsage, error) {
		return nil, internalsearch.AISearchUsage{}, stderrs.New("upstream timeout")
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if result.Success {
		t.Fatal("expected upstream error result")
	}
	if result.Error == nil || result.Error.Code != AIFallbackErrorCodeUpstreamError {
		t.Fatalf("expected UPSTREAM_ERROR, got %+v", result.Error)
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
		Plat:       -1,
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
