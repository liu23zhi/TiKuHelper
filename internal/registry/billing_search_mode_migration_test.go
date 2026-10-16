package registry

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 旧库（没有 search_mode 列）必须被补上该列，且已有数据不丢。
func TestEnsureBillingSearchModeSchemaAddsColumn(t *testing.T) {
	db := newBillingClientIPTestDB(t)

	if err := db.Exec(`CREATE TABLE billing_records (
		id integer PRIMARY KEY AUTOINCREMENT,
		user_id integer NOT NULL,
		type varchar(32) NOT NULL,
		amount decimal(18,8) NOT NULL DEFAULT 0,
		created_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec(`INSERT INTO billing_records (user_id, type, amount) VALUES (1, 'normal_search', 0.5)`).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := ensureBillingSearchModeSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !db.Migrator().HasColumn(entity.TableNameBillingRecord, "search_mode") {
		t.Fatal("expected the search_mode column to be added")
	}

	// 幂等：重复调用不再加列。
	if err := ensureBillingSearchModeSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}

	// 历史行的 search_mode 保持为空（没有可靠关联键，不回填）。
	var stored entity.BillingRecord
	if err := db.Where("user_id = ?", 1).First(&stored).Error; err != nil {
		t.Fatalf("load legacy row: %v", err)
	}
	if stored.SearchMode != "" {
		t.Fatalf("expected legacy rows to keep an empty search mode, got %q", stored.SearchMode)
	}
}

// billing_records 表不存在时必须直接返回，不报错。
func TestEnsureBillingSearchModeSchemaSkipsWhenTableMissing(t *testing.T) {
	db := newBillingClientIPTestDB(t)
	if err := ensureBillingSearchModeSchema(db); err != nil {
		t.Fatalf("expected no error when the table is missing, got %v", err)
	}
}
