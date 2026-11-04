package search

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func newTikuSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
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

func TestDBTikuSearchToolReturnsCoexistingUnverifiedEvidence(t *testing.T) {
	db := newTikuSearchTestDB(t)
	question := "光合作用的主要产物是什么？"
	options := []string{"A. 氧气", "B. 二氧化碳", "C. 氮气"}
	encodedOptions, _ := json.Marshal(options)
	encodedAnswer, _ := json.Marshal([]string{"A"})
	hash := util.BuildTikuHashCandidates(question, options, 0)[0]
	if err := db.Create(&entity.Tiku{Question: question, Type: 0, Options: string(encodedOptions), Answer: string(encodedAnswer), Hash: hash, SourceName: "AI 搜索"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.TikuAnswerEvidence{EvidenceKey: "platform-proof", QuestionHash: hash, Question: question, Type: 0, Options: string(encodedOptions), Answer: string(encodedAnswer), Source: "chaoxing-graded", Evidence: `{"page":"review"}`, Status: "unverified"}).Error; err != nil {
		t.Fatal(err)
	}
	tool := NewDBTikuSearchTool(db)
	result, err := tool.SearchTiku(context.Background(), model.SearchRequest{Question: question, Options: options, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.MatchKind != "exact" || len(result.Candidates) != 2 {
		t.Fatalf("expected old answer and independent platform evidence, got %+v", result)
	}
	if result.Candidates[0].Status != "unverified" || result.Candidates[1].Status != "unverified" {
		t.Fatalf("search results must not imply verification: %+v", result.Candidates)
	}
}

func TestDBTikuSearchToolFuzzyCandidatesPreserveMathematicalOperators(t *testing.T) {
	db := newTikuSearchTestDB(t)
	for _, question := range []string{"x < 4 时应选择哪个条件？", "x > 4 时应选择哪个条件？"} {
		answers, _ := json.Marshal([]string{question[:5]})
		hash := util.BuildTikuHashCandidates(question, nil, -1)[0]
		if err := db.Create(&entity.Tiku{Question: question, Type: -1, Options: "[]", Answer: string(answers), Hash: hash, SourceName: "AI 搜索"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: "x < 4 时选择哪个条件", Type: -1, Options: []string{"x < 4"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.MatchKind != "fuzzy" || len(result.Candidates) != 1 || result.Candidates[0].Question != "x < 4 时应选择哪个条件？" {
		t.Fatalf("math operator distinction was lost: %+v", result)
	}
}

func TestDBTikuSearchToolAbstainsOnLowConfidenceAndTypeOrOptionConflict(t *testing.T) {
	db := newTikuSearchTestDB(t)
	question := "下面哪项属于哺乳动物？"
	options, _ := json.Marshal([]string{"鸟", "鲨鱼"})
	answer, _ := json.Marshal([]string{"A"})
	hash := util.BuildTikuHashCandidates(question, []string{"鸟", "鲨鱼"}, 0)[0]
	if err := db.Create(&entity.Tiku{Question: question, Type: 0, Options: string(options), Answer: string(answer), Hash: hash, SourceName: "AI 搜索"}).Error; err != nil {
		t.Fatal(err)
	}
	tool := NewDBTikuSearchTool(db)
	for _, req := range []model.SearchRequest{
		{Question: question, Type: 1, Options: []string{"鸟", "鲨鱼"}},
		{Question: question, Type: 0, Options: []string{"苹果", "香蕉"}},
		{Question: "请简述量子纠缠与经典相关性的区别", Type: -1},
	} {
		result, err := tool.SearchTiku(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Candidates) != 0 {
			t.Fatalf("expected conservative abstention for %+v, got %+v", req, result)
		}
	}
}

func TestTikuCompatibilityPreservesKnownTypesNumbersAndReorderedOptions(t *testing.T) {
	if compatibleQuestionType(-4, 0) || compatibleQuestionType(0, -4) {
		t.Fatal("known negative types must not match arbitrary types")
	}
	if !compatibleQuestionType(-1, -4) {
		t.Fatal("unknown type may return typed evidence")
	}
	if compatibleMathOperators("x < 4", "x < 5") {
		t.Fatal("numeric math operands must retain meaning")
	}
	if !compatibleTikuOptions([]string{"A. x < 4", "B. x > 4"}, []string{"A. x > 4", "B. x < 4"}) {
		t.Fatal("option reordering must remain compatible")
	}
	if compatibleTikuOptions([]string{"x < 4", "x > 4"}, []string{"x < 5", "x > 5"}) {
		t.Fatal("conflicting option operands must be rejected")
	}
}

func TestDBTikuSearchToolMarksExactAnswerConflict(t *testing.T) {
	db := newTikuSearchTestDB(t)
	question := "2+2=?"
	hash := util.BuildTikuHashCandidates(question, nil, 0)[0]
	for index, answer := range []string{`["4"]`, `["5"]`} {
		row := entity.TikuAnswerEvidence{EvidenceKey: string(rune('a' + index)), QuestionHash: hash, Question: question, Type: 0, Options: "[]", Answer: answer, Source: "platform", Evidence: `{"page":"review"}`, Status: "unverified"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: question, Type: 0})
	if err != nil || !result.Ambiguous || result.CandidateCount != 2 {
		t.Fatalf("expected visible exact conflict: %+v err=%v", result, err)
	}
}
