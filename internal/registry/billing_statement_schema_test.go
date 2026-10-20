package registry

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestBillingStatementColumnsKeepThirtyThousandLegacyRecordsVisible(t *testing.T) {
	db, err := gorm.Open(sqliteQuietDialector{Dialector: sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))}, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec("CREATE TABLE billing_records (id integer PRIMARY KEY, user_id integer NOT NULL, type text NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`WITH RECURSIVE numbers(id) AS (SELECT 1 UNION ALL SELECT id + 1 FROM numbers WHERE id < 30000) INSERT INTO billing_records(id, user_id, type) SELECT id, 7, 'normal_search' FROM numbers`).Error; err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for i := 0; i < 2; i++ {
		if err := db.AutoMigrate(&entity.BillingRecord{}); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&entity.BillingRecord{}).Where("hidden_from_statement = ?", false).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 30000 {
		t.Fatalf("legacy records hidden or lost: %d", count)
	}
	t.Logf("two idempotent schema runs with 30000 legacy records: %s", time.Since(started))
}
