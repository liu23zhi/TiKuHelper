package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newTikuEvidenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:evidence-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.Tiku{}, &entity.TikuAnswerEvidence{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestSaveTikuAnswerEvidenceIsIdempotentAndKeepsContradictoryAnswers(t *testing.T) {
	db := newTikuEvidenceTestDB(t)
	input := TikuAnswerEvidenceInput{QID: "q-1", Question: "2+2=?", Type: -1, Options: []string{}, Answer: json.RawMessage(`"4"`), Source: "chaoxing-reviewed", Evidence: []byte(`{"page":"review","label":"correct answer"}`)}
	first, created, err := SaveTikuAnswerEvidenceWithDB(db, input)
	if err != nil || !created || first.Status != "unverified" {
		t.Fatalf("first evidence: %+v created=%t err=%v", first, created, err)
	}
	again, created, err := SaveTikuAnswerEvidenceWithDB(db, input)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("duplicate evidence must be idempotent: %+v created=%t err=%v", again, created, err)
	}
	input.Answer = json.RawMessage(`"5"`)
	second, created, err := SaveTikuAnswerEvidenceWithDB(db, input)
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("contradictory answer must coexist: %+v created=%t err=%v", second, created, err)
	}
	var count int64
	if err := db.Model(&entity.TikuAnswerEvidence{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("expected two coexisting answers, count=%d err=%v", count, err)
	}
}

func TestSaveTikuAnswerEvidenceRejectsMissingOrInvalidProof(t *testing.T) {
	db := newTikuEvidenceTestDB(t)
	base := TikuAnswerEvidenceInput{Question: "题干", Type: -1, Answer: json.RawMessage(`"答案"`), Source: "chaoxing-reviewed", Evidence: []byte(`{"page":"review"}`)}
	for _, mutate := range []func(*TikuAnswerEvidenceInput){
		func(input *TikuAnswerEvidenceInput) { input.Source = "" },
		func(input *TikuAnswerEvidenceInput) { input.Evidence = []byte(`not-json`) },
		func(input *TikuAnswerEvidenceInput) { input.Type = -5 },
		func(input *TikuAnswerEvidenceInput) { input.Answer = json.RawMessage(`[" "]`) },
		func(input *TikuAnswerEvidenceInput) { input.Evidence = json.RawMessage(`null`) },
		func(input *TikuAnswerEvidenceInput) { input.Evidence = json.RawMessage(`{}`) },
	} {
		input := base
		mutate(&input)
		if _, _, err := SaveTikuAnswerEvidenceWithDB(db, input); err == nil {
			t.Fatalf("expected invalid evidence rejected: %+v", input)
		}
	}
}

func TestSaveTikuAnswerEvidenceAcceptsLongAnswersAndCanonicalizesProof(t *testing.T) {
	db := newTikuEvidenceTestDB(t)
	answer := strings.Repeat("完整回答", 100)
	encoded, _ := json.Marshal(answer)
	input := TikuAnswerEvidenceInput{Question: "综合作业", Type: -4, Answer: encoded, Source: "chaoxing-reviewed", Evidence: json.RawMessage(`{"page":"review","label":"correct"}`)}
	first, created, err := SaveTikuAnswerEvidenceWithDB(db, input)
	if err != nil || !created || first.Type != -4 {
		t.Fatalf("long answer: %+v %t %v", first, created, err)
	}
	input.Answer, _ = json.Marshal([]string{answer})
	input.Evidence = json.RawMessage(`{ "label": "correct", "page": "review" }`)
	second, created, err := SaveTikuAnswerEvidenceWithDB(db, input)
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("canonical duplicate: %+v %t %v", second, created, err)
	}
}
