package search

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"github.com/itihey/tikuAdapter/pkg/util"
)

// TestDBTikuSearchToolExcludesArchivedQuestions 是 Q77-1「影响面 1：搜题匹配」的钉子。
//
// 已归档题库若仍参与匹配，等于没删（77-*.md §11.3）。两条匹配路径都要覆盖：
//   - exactRows：走 dao 语义（Find 到 entity.Tiku，GORM 软删除作用域生效）；
//   - fuzzyRows：走 `db.Table("tiku")` 裸查询，必须显式带 deleted_at IS NULL。
//
// 只测其中一条就会留下假钉子：exact 通过只说明 GORM 的自动作用域生效，
// 完全测不到裸 SQL 分支。
func TestDBTikuSearchToolExcludesArchivedQuestions(t *testing.T) {
	db := newTikuSearchTestDB(t)

	question := "光合作用的产物是什么？"
	options := []string{"A. 氧气", "B. 二氧化碳"}
	encodedOptions, _ := json.Marshal(options)
	encodedAnswer, _ := json.Marshal([]string{"A"})
	hash := util.BuildTikuHashCandidates(question, options, 0)[0]

	row := entity.Tiku{
		Question:   question,
		Type:       0,
		Options:    string(encodedOptions),
		Answer:     string(encodedAnswer),
		Hash:       hash,
		CourseName: "归档匹配样例",
		SourceName: "AI 搜索",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}

	tool := NewDBTikuSearchTool(db)

	// 归档前：两条路径都必须能命中，否则下面的「归档后为 0」毫无意义。
	before, err := tool.SearchTiku(context.Background(), model.SearchRequest{Question: question, Options: options, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if before.MatchKind != "exact" || len(before.Candidates) != 1 {
		t.Fatalf("precondition failed: expected one exact candidate before archiving, got %+v", before)
	}

	// 归档：等价于 controller 里写的 deleted_at = now。
	if err := db.Model(&entity.Tiku{}).Where("id = ?", row.ID).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatalf("archive row: %v", err)
	}

	exact, err := tool.SearchTiku(context.Background(), model.SearchRequest{Question: question, Options: options, Type: 0})
	if err != nil {
		t.Fatal(err)
	}
	if exact.MatchKind != "none" || len(exact.Candidates) != 0 {
		t.Fatalf("archived question still matched by the exact path: %+v", exact)
	}

	// 近似题面会落到 fuzzyRows 的 `db.Table("tiku")` 裸查询分支。
	fuzzy, err := tool.SearchTiku(context.Background(), model.SearchRequest{Question: "光合作用产物是什么", Type: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(fuzzy.Candidates) != 0 {
		t.Fatalf("archived question still matched by the fuzzy (bare table) path: %+v", fuzzy)
	}
}
