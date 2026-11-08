package registry

import (
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestScriptKeyLinkPreservesLegacyDisabledQuota(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/tokens.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	for _, sql := range []string{
		"CREATE TABLE access_tokens (id INTEGER PRIMARY KEY, status TEXT NOT NULL, total_quota_limit TEXT)",
		"INSERT INTO access_tokens (status,total_quota_limit) VALUES ('disabled','1000000000000000000001'),('enabled','1.125001')",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	type originalQuota struct {
		ID     int
		Status string
		Value  string
		Kind   string
	}
	snapshot := func() []originalQuota {
		var rows []originalQuota
		if err := db.Raw("SELECT id,status,total_quota_limit AS value,typeof(total_quota_limit) AS kind FROM access_tokens ORDER BY id").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := snapshot()
	for i := 0; i < 2; i++ {
		if err := ensureScriptKeyLinkOnPreservedQuotaSchema(db); err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := db.Table("access_tokens").Where("script_id = 0").Count(&count).Error; err != nil || count != 2 {
			t.Fatalf("existing tokens must remain ordinary Keys: count=%d err=%v", count, err)
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("migration rewrote existing quota values, types or statuses")
		}
	}
}
