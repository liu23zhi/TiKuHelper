package service

import (
	stderrs "errors"
	"testing"

	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/model"
	pkgerrors "github.com/itihey/tikuAdapter/pkg/errors"
)

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
		return [][]string{{" ", "不确定", "B"}}, internalsearch.AISearchUsage{}, nil
	}

	result := SearchAIFallbackWithDB(db, model.SearchRequest{Question: "1+1=?", Options: []string{"1", "2"}, Type: 0})

	if !result.Success {
		t.Fatalf("expected success result, got %+v", result)
	}
	if result.Result == nil || result.Result.Answer != "2" {
		t.Fatalf("expected cleaned answer 2, got %+v", result.Result)
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
