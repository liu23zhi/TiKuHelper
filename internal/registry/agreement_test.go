package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestAgreementMigrationProtectsPublishedVersions(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	defer sql.Close()
	if e = EnsureAgreementSchema(db); e != nil {
		t.Fatal(e)
	}
	var count int64
	if e = db.Model(&entity.AgreementRelease{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("migration published an agreement")
	}
	insert := func(draftID int64) error {
		return db.Exec("INSERT INTO agreement_releases (draft_id,revision,version,body,summary,published_at) VALUES (?,1,'v1','fixture','fixture',?)", draftID, time.Now()).Error
	}
	if e = insert(1); e != nil {
		t.Fatal(e)
	}
	if e = db.Model(&entity.AgreementHead{}).Where("id=1").Update("release_id", 1).Error; e != nil {
		t.Fatal(e)
	}
	if e = EnsureAgreementSchema(db); e != nil {
		t.Fatal(e)
	}
	var head entity.AgreementHead
	if e = db.First(&head, 1).Error; e != nil || head.ReleaseID != 1 {
		t.Fatal("migration erased current pointer")
	}
	if e = insert(2); e == nil {
		t.Fatal("DB accepted duplicate published version")
	} else if !strings.Contains(e.Error(), "UNIQUE constraint failed: agreement_releases.version") {
		t.Fatalf("unexpected constraint: %v", e)
	}
}

// Index-bearing string columns must stay bounded in SQLite's declared DDL.
// The SQLite-to-MariaDB migrator cannot safely map TEXT unique keys.
func TestAgreementMigrationKeepsIndexStringBounds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	if err := EnsureAgreementSchema(db); err != nil {
		t.Fatal(err)
	}
	for tab, want := range map[string]map[string]string{
		"agreement_releases": {"version": "varchar(48)"},
		"agreement_devices":  {"id": "varchar(36)", "secret_hash": "varchar(64)"},
		"agreement_consents": {"device_id": "varchar(36)"},
	} {
		var cols []struct {
			Name string
			Type string
		}
		if err := db.Raw("PRAGMA table_info(" + tab + ")").Scan(&cols).Error; err != nil {
			t.Fatal(err)
		}
		seen := make(map[string]string, len(cols))
		for _, c := range cols {
			seen[c.Name] = strings.ToLower(c.Type)
		}
		for column, expected := range want {
			if actual := seen[column]; actual != expected {
				t.Errorf("%s.%s declared as %q, want %q for portable uniqueness", tab, column, actual, expected)
			}
		}
	}
}
