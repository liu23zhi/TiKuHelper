package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestAIModelPriceSchemaAddsContextWithoutChangingExistingRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec("CREATE TABLE ai_model_prices (id INTEGER PRIMARY KEY, model_name TEXT, multiplier REAL, updated_at TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO ai_model_prices (id, model_name, multiplier) VALUES (1, 'existing', 2.7)").Error; err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ensureAIModelPriceSchema(db); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasColumn(entity.TableNameAIModelPrice, "context_window_tokens") {
		t.Fatal("missing context window column")
	}
	var item entity.AIModelPrice
	if err := db.First(&item, 1).Error; err != nil {
		t.Fatal(err)
	}
	if item.ModelName != "existing" || item.Multiplier != 2.7 || item.ContextWindowTokens != 0 {
		t.Fatalf("schema migration changed existing values: %+v", item)
	}
}
