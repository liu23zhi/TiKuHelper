package search

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

func TestDBTikuSearchToolKeepsOlderRelevantCandidateBeyondNewest200(t *testing.T) {
	for _, evidenceTable := range []bool{false, true} {
		t.Run(fmt.Sprintf("evidence=%t", evidenceTable), func(t *testing.T) {
			db := newTikuSearchTestDB(t)
			query := "下列关于基础课程知识的叙述中，细胞呼吸主要在哪个细胞器进行？"
			target := "下列关于基础课程知识的叙述中，细胞呼吸主要在哪个细胞器中进行？"
			insert := func(question, key string) {
				t.Helper()
				hash := util.BuildTikuHashCandidates(question, nil, 0)[0]
				var err error
				if evidenceTable {
					err = db.Create(&entity.TikuAnswerEvidence{Question: question, Type: 0, Options: "[]", Answer: `["线粒体"]`, QuestionHash: hash, EvidenceKey: key, Source: "synthetic-reviewed", Status: "unverified", Evidence: `{"synthetic":true}`}).Error
				} else {
					err = db.Create(&entity.Tiku{Question: question, Type: 0, Options: "[]", Answer: `["线粒体"]`, Hash: hash, SourceName: "synthetic"}).Error
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			insert(target, "old-relevant")
			for index := 0; index < 205; index++ {
				insert(fmt.Sprintf("下列关于基础课程知识的叙述中，第%d条无关材料讨论地球公转周期，请选择对应日期", index), fmt.Sprintf("new-unrelated-%d", index))
			}
			result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: query, Type: 0})
			if err != nil || result.MatchKind != "fuzzy" || result.CandidateCount != 1 || result.Candidates[0].Question != target {
				t.Fatalf("older high-similarity candidate was hidden by recent common-prefix rows: %+v err=%v", result, err)
			}
		})
	}
}

func TestDBTikuSearchToolDoesNotTreatFactorialAsAnExactMatch(t *testing.T) {
	db := newTikuSearchTestDB(t)
	question := "计算3!"
	hash := util.BuildTikuHashCandidates(question, nil, 4)[0]
	if err := db.Create(&entity.Tiku{Question: question, Type: 4, Options: "[]", Answer: `["6"]`, Hash: hash, SourceName: "synthetic"}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := NewDBTikuSearchTool(db).SearchTiku(context.Background(), model.SearchRequest{Question: "计算3", Type: 4})
	if err != nil || result.CandidateCount != 0 {
		t.Fatalf("factorial semantics were discarded by legacy hash normalization: %+v err=%v", result, err)
	}
}

func TestTikuSearchMathGuardDistinguishesLogicalAndSetOperators(t *testing.T) {
	for _, pair := range [][2]string{{"A∪B", "A∩B"}, {"x∈A", "x∉A"}, {"p∧q", "p∨q"}, {"¬p", "p"}, {"A⊂B", "A⊆B"}} {
		if compatibleMathOperators(pair[0], pair[1]) {
			t.Errorf("incompatible expressions matched: %q and %q", pair[0], pair[1])
		}
	}
}

func TestTikuSearchTermsIncludeWholeQuestionAndLaterDistinctiveTerms(t *testing.T) {
	question := strings.Repeat("通用开头", 10) + "线粒体呼吸"
	terms := tikuSearchTerms(question)
	if len(terms) > 12 {
		t.Fatalf("shortlist terms must stay bounded: %d", len(terms))
	}
	if len(terms) == 0 || terms[0] != normalizeTikuText(question) {
		t.Fatalf("whole question must be searched before sampled terms: %v", terms)
	}
	later := false
	for _, term := range terms[1:] {
		if strings.Contains("线粒体呼吸", term) {
			later = true
		}
	}
	if !later {
		t.Fatalf("only common prefix terms were selected: %v", terms)
	}
}
