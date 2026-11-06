package middleware

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func newCollectAnswerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:collect-answer-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.Tiku{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	dao.SetDefault(db)
	return db
}

func TestCollectAnswerStoresResolvedSourceName(t *testing.T) {
	db := newCollectAnswerTestDB(t)

	CollectAnswer(model.SearchResponse{
		Question: "第三方命中题目",
		Options:  []string{"A"},
		Type:     4,
		Answer: model.Answer{
			BestAnswer: []string{"第三方答案"},
		},
	}, "课程", "", "wanneng")

	var sourceName sql.NullString
	if err := db.Raw("SELECT source_name FROM tiku LIMIT 1").Row().Scan(&sourceName); err != nil {
		t.Fatalf("scan source_name: %v", err)
	}
	if strings.TrimSpace(sourceName.String) != "万能题库" {
		t.Fatalf("expected source_name 万能题库, got %q", sourceName.String)
	}
}

func TestCollectAnswerDoesNotMutateSearchResponseOptionOrder(t *testing.T) {
	newCollectAnswerTestDB(t)
	options := []string{"zeta option", "alpha option"}
	response := model.SearchResponse{
		Question: "多选题选项顺序",
		Options:  options,
		Type:     1,
		Answer: model.Answer{
			AnswerKey:   []string{"B"},
			AnswerIndex: []int{1},
			BestAnswer:  []string{"alpha option"},
		},
	}

	CollectAnswer(response, "课程", "", "wanneng")

	wantOptions := []string{"zeta option", "alpha option"}
	if !reflect.DeepEqual(options, wantOptions) {
		t.Fatalf("collection changed the caller's option order: want %v, got %v", wantOptions, options)
	}
	if !reflect.DeepEqual(response.Options, wantOptions) {
		t.Fatalf("response options no longer align with answer indexes: want %v, got %v", wantOptions, response.Options)
	}
}

// 回归：重复收录同一道题不能再走「插入失败 → 报 ERROR」的老路。
//
// 线上 8 次收录里有 3 次撞 UNIQUE(hash, course_name)，每次都往 error 日志里刷一行。
// 现在必须是 upsert 语义：不报错、不产生第二行、已有答案不被覆盖。
func TestCollectAnswerRepeatedQuestionDoesNotFailOrDuplicate(t *testing.T) {
	db := newCollectAnswerTestDB(t)

	response := model.SearchResponse{
		Question: "重复收录题目",
		Options:  []string{"A"},
		Type:     4,
		Answer:   model.Answer{BestAnswer: []string{"第一次的答案"}},
	}
	CollectAnswer(response, "课程", "", "wanneng")

	// 第二次换一个答案，模拟同一道题再次命中。
	response.Answer = model.Answer{BestAnswer: []string{"第二次的答案"}}
	CollectAnswer(response, "课程", "", "wanneng")

	var count int64
	if err := db.Table(entity.TableNameTiku).Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row for the repeated question, got %d", count)
	}

	var answer string
	if err := db.Table(entity.TableNameTiku).Select("answer").Scan(&answer).Error; err != nil {
		t.Fatalf("load answer: %v", err)
	}
	if !strings.Contains(answer, "第一次的答案") {
		t.Fatalf("expected the stored answer to be kept, got %q", answer)
	}
}

func TestCollectAnswerReturnsInsertedAndExistingQuestionID(t *testing.T) {
	db := newCollectAnswerTestDB(t)
	response := model.SearchResponse{
		Question: "返回题库 ID",
		Options:  []string{"A"},
		Type:     4,
		Answer:   model.Answer{BestAnswer: []string{"答案"}},
	}

	first := CollectAnswer(response, "课程", "", "wanneng")
	second := CollectAnswer(response, "课程", "", "wanneng")
	if !first.Stored || first.ID <= 0 {
		t.Fatalf("expected inserted question id, got %+v", first)
	}
	if !second.Stored || second.ID != first.ID {
		t.Fatalf("expected existing question id %d, got %+v", first.ID, second)
	}

	var count int64
	if err := db.Table(entity.TableNameTiku).Count(&count).Error; err != nil {
		t.Fatalf("count tiku rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one stored question, got %d", count)
	}
}
