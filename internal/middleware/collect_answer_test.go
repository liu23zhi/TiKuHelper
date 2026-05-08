package middleware

import (
	"database/sql"
	"fmt"
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
		Plat:     1,
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
