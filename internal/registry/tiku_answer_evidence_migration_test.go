package registry

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Test both the driver and the production wrapper without opening the user's
// configured database. The wrapper must still create the new table and indexes.
func TestTikuAnswerEvidenceMigrationPreservesLegacyRows(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(fmt.Sprintf("production_wrapper_%t", quiet), func(t *testing.T) {
			db := newTikuAnswerEvidenceMigrationDB(t, quiet)
			if err := db.AutoMigrate(&entity.Tiku{}); err != nil {
				t.Fatal(err)
			}
			legacy := entity.Tiku{
				Question:   "保留 x < 3 与 x ≤ 3 的区别",
				Type:       0,
				Options:    `["x < 3","x ≤ 3"]`,
				Answer:     `["x < 3"]`,
				Hash:       util.BuildTikuHash("保留 x < 3 与 x ≤ 3 的区别", []string{"x < 3", "x ≤ 3"}, 0),
				CourseName: "合成迁移样例",
				Extra:      `{"tag":"preserve"}`,
				SourceName: "AI 搜索",
			}
			if err := db.Create(&legacy).Error; err != nil {
				t.Fatal(err)
			}

			for pass := 0; pass < 2; pass++ {
				// Match the startup order: retain tiku and add a separate table.
				if err := db.AutoMigrate(&entity.Tiku{}, &entity.TikuAnswerEvidence{}); err != nil {
					t.Fatalf("migration pass %d: %v", pass+1, err)
				}
				var actual entity.Tiku
				if err := db.First(&actual, legacy.ID).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual, legacy) {
					t.Fatalf("migration changed legacy row: before=%+v after=%+v", legacy, actual)
				}
				var count int64
				if err := db.Model(&entity.Tiku{}).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("legacy row count=%d err=%v", count, err)
				}
			}
			for _, field := range []string{"EvidenceKey", "QuestionHash", "Type", "Source", "Status"} {
				if !db.Migrator().HasIndex(&entity.TikuAnswerEvidence{}, field) {
					t.Errorf("missing evidence index for %s", field)
				}
			}
		})
	}
}

func TestTikuAnswerEvidenceMigrationKeepsUniqueKeyAndConflictingAnswers(t *testing.T) {
	db := newTikuAnswerEvidenceMigrationDB(t, true)
	if err := db.AutoMigrate(&entity.Tiku{}, &entity.TikuAnswerEvidence{}); err != nil {
		t.Fatal(err)
	}
	first := entity.TikuAnswerEvidence{
		EvidenceKey:  strings.Repeat("1", 64),
		QuestionHash: util.BuildTikuHash("合成题面", []string{"甲", "乙"}, 0),
		Question:     "合成题面",
		Type:         0,
		Options:      `["甲","乙"]`,
		Answer:       `["甲"]`,
		Source:       "学习通批改页",
		Evidence:     `{"correctAnswer":"甲"}`,
		Status:       "unverified",
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = 0
	second.EvidenceKey = strings.Repeat("2", 64)
	second.Answer = `["乙"]`
	second.Source = "独立来源"
	second.Evidence = `{"correctAnswer":"乙"}`
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if err := db.AutoMigrate(&entity.TikuAnswerEvidence{}); err != nil {
			t.Fatalf("repeat migration pass %d: %v", pass+1, err)
		}
		duplicate := first
		duplicate.ID = 0
		result := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "evidence_key"}}, DoNothing: true}).Create(&duplicate)
		if result.Error != nil || result.RowsAffected != 0 {
			t.Fatalf("duplicate must remain idempotent: rows=%d err=%v", result.RowsAffected, result.Error)
		}
	}
	var stored []entity.TikuAnswerEvidence
	if err := db.Order("id").Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected two independent answer records, got %d", len(stored))
	}
	for index, expected := range []entity.TikuAnswerEvidence{first, second} {
		actual := stored[index]
		if actual.ID != expected.ID || actual.EvidenceKey != expected.EvidenceKey || actual.Answer != expected.Answer || actual.Source != expected.Source || actual.Evidence != expected.Evidence || actual.Status != "unverified" || actual.Type != 0 || !actual.CreatedAt.Equal(expected.CreatedAt) {
			t.Errorf("evidence identity/content/type changed: before=%+v after=%+v", expected, actual)
		}
	}
}

func TestTikuAnswerEvidenceMigrationPreservesLargeSyntheticCorpus(t *testing.T) {
	db := newTikuAnswerEvidenceMigrationDB(t, true)
	if err := db.AutoMigrate(&entity.Tiku{}); err != nil {
		t.Fatal(err)
	}
	rows := make([]entity.Tiku, 3000)
	for index := range rows {
		question := fmt.Sprintf("合成迁移样例 %d，保留 x < %d", index, index)
		rows[index] = entity.Tiku{
			Question:   question,
			Type:       0,
			Options:    `["甲","乙"]`,
			Answer:     `["甲"]`,
			Hash:       util.BuildTikuHash(question, []string{"甲", "乙"}, 0),
			CourseName: "合成样例",
			SourceName: "AI 搜索",
		}
	}
	if err := db.CreateInBatches(&rows, 250).Error; err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		started := time.Now()
		if err := db.AutoMigrate(&entity.Tiku{}, &entity.TikuAnswerEvidence{}); err != nil {
			t.Fatalf("migration pass %d: %v", pass+1, err)
		}
		t.Logf("migration pass %d with %d legacy rows: %s", pass+1, len(rows), time.Since(started))
		var stored []entity.Tiku
		if err := db.Order("id").Find(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored, rows) {
			t.Fatal("migration changed the synthetic legacy corpus")
		}
	}
}

func newTikuAnswerEvidenceMigrationDB(t *testing.T, quiet bool) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tiku-evidence-migration.db")
	var dialector gorm.Dialector = sqlite.Open(path)
	if quiet {
		dialector = sqliteQuietDialector{Dialector: dialector}
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}
