package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// newTikuMigrationTestDB 建一个只含 tiku / system_settings 的内存库。
func newTikuMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.Tiku{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// seedLegacyTikuRow 插入一行「旧公式（含 plat=-1）」的题库数据，模拟迁移前的历史数据。
func seedLegacyTikuRow(t *testing.T, db *gorm.DB, question string, options []string, questionType int, courseName string) string {
	t.Helper()

	optionsPayload, _ := util.SortOptionsJSON(options), error(nil)
	legacyHash := util.BuildLegacyTikuHash(question, options, questionType)

	payloadBytes := optionsPayload
	row := entity.Tiku{
		Question:   question,
		Type:       int32(questionType),
		Options:    string(payloadBytes),
		Answer:     `["旧答案"]`,
		Hash:       legacyHash,
		CourseName: courseName,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed legacy tiku row: %v", err)
	}
	return legacyHash
}

// 回填必须把历史行的 hash 改写成新公式。
func TestEnsureTikuHashWithoutPlatSchemaBackfillsLegacyRows(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	question := "迁移题目"
	options := []string{"A", "B"}
	legacyHash := seedLegacyTikuRow(t, db, question, options, 0, "")
	expected := util.BuildTikuHash(question, options, 0)

	if legacyHash == expected {
		t.Fatal("test setup invalid: legacy and current hash should differ")
	}

	if err := ensureTikuHashWithoutPlatSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var stored string
	if err := db.Table(entity.TableNameTiku).Select("hash").Where("question = ?", question).Scan(&stored).Error; err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if stored != expected {
		t.Fatalf("expected the hash to be rewritten to %q, got %q", expected, stored)
	}

	// 回填后必须能用「读取候选」查到该行 —— 这是迁移真正要保证的效果。
	var count int64
	if err := db.Table(entity.TableNameTiku).
		Where("hash IN ?", util.BuildTikuHashCandidates(question, options, 0)).
		Count(&count).Error; err != nil {
		t.Fatalf("count by candidates: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the migrated row to stay reachable, matched %d rows", count)
	}
}

// 回填必须幂等：已迁移过就不再改动（靠标记短路）。
func TestEnsureTikuHashWithoutPlatSchemaIsIdempotent(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	seedLegacyTikuRow(t, db, "幂等题目", []string{"A"}, 0, "")

	if err := ensureTikuHashWithoutPlatSchema(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if !systemSettingFlagIsSet(db, tikuHashWithoutPlatBackfilledKey) {
		t.Fatal("expected the backfill marker to be written")
	}

	// 人为把 hash 改回旧公式，再跑一次：标记命中应当直接跳过，不会再次改写。
	legacy := util.BuildLegacyTikuHash("幂等题目", []string{"A"}, 0)
	if err := db.Table(entity.TableNameTiku).Where("question = ?", "幂等题目").Update("hash", legacy).Error; err != nil {
		t.Fatalf("rewrite hash: %v", err)
	}
	if err := ensureTikuHashWithoutPlatSchema(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var stored string
	_ = db.Table(entity.TableNameTiku).Select("hash").Where("question = ?", "幂等题目").Scan(&stored).Error
	if stored != legacy {
		t.Fatal("expected the second run to be skipped by the marker")
	}
}

// 已经是新公式的行不应被改写（hash 相等即跳过）。
func TestEnsureTikuHashWithoutPlatSchemaSkipsUpToDateRows(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	question := "已是新公式"
	options := []string{"A"}
	current := util.BuildTikuHash(question, options, 0)
	if err := db.Create(&entity.Tiku{
		Question: question,
		Type:     0,
		Options:  `["A"]`,
		Answer:   `["答案"]`,
		Hash:     current,
	}).Error; err != nil {
		t.Fatalf("seed current row: %v", err)
	}

	if err := ensureTikuHashWithoutPlatSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var stored string
	_ = db.Table(entity.TableNameTiku).Select("hash").Where("question = ?", question).Scan(&stored).Error
	if stored != current {
		t.Fatalf("expected an up-to-date row to be left untouched, got %q", stored)
	}
}

// tiku 表不存在时必须直接返回，不能报错。
func TestEnsureTikuHashWithoutPlatSchemaSkipsWhenTableMissing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if err := ensureTikuHashWithoutPlatSchema(db); err != nil {
		t.Fatalf("expected no error when tiku table is missing, got %v", err)
	}
}

// 坏选项 JSON 不能让回填崩溃（按空选项处理，与 FillHash 一致）。
func TestEnsureTikuHashWithoutPlatSchemaToleratesBrokenOptions(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	if err := db.Exec(
		`INSERT INTO tiku (question, type, options, answer, hash, course_name) VALUES (?, ?, ?, ?, ?, ?)`,
		"坏选项题目", 0, "this-is-not-json", `["答案"]`, "legacy-broken-hash", "",
	).Error; err != nil {
		t.Fatalf("seed broken row: %v", err)
	}

	if err := ensureTikuHashWithoutPlatSchema(db); err != nil {
		t.Fatalf("migrate with broken options: %v", err)
	}

	var stored string
	_ = db.Table(entity.TableNameTiku).Select("hash").Where("question = ?", "坏选项题目").Scan(&stored).Error
	if stored != util.BuildTikuHash("坏选项题目", nil, 0) {
		t.Fatalf("expected broken options to be treated as empty, got %q", stored)
	}
}

// 回填后 (hash, course_name) 唯一性必须保持：迁移不能制造重复。
func TestEnsureTikuHashWithoutPlatSchemaKeepsUniqueRows(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	// 同题不同课程：两行都必须保留，且 hash 相同。
	seedLegacyTikuRow(t, db, "多课程题目", []string{"A"}, 0, "课程一")
	seedLegacyTikuRow(t, db, "多课程题目", []string{"A"}, 0, "课程二")

	if err := ensureTikuHashWithoutPlatSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var count int64
	if err := db.Table(entity.TableNameTiku).Where("question = ?", "多课程题目").Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected both course rows to survive, got %d", count)
	}

	var hashes []string
	if err := db.Table(entity.TableNameTiku).Where("question = ?", "多课程题目").Order("course_name asc").Pluck("hash", &hashes).Error; err != nil {
		t.Fatalf("pluck hashes: %v", err)
	}
	expected := util.BuildTikuHash("多课程题目", []string{"A"}, 0)
	for i, hash := range hashes {
		if hash != expected {
			t.Fatalf("row %d: expected hash %q, got %q", i, expected, hash)
		}
	}
}

// 冲突行必须「跳过并完成」，不能中止回填、更不能重跑一辈子。
//
// 线上事故复现：同一道题既有一行旧公式、又有一行已经写成新公式，
// 回填旧行时会撞 UNIQUE(hash, course_name) —— 旧实现直接 return err，
// 标记永远写不上，于是每次启动都重跑（见 AGENTS.md 第 18 条）。
func TestEnsureTikuHashWithoutPlatSchemaSkipsConflictingRowAndCompletes(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	question := "冲突题目"
	options := []string{"A"}
	currentHash := util.BuildTikuHash(question, options, 0)
	legacyHash := util.BuildLegacyTikuHash(question, options, 0)
	if currentHash == legacyHash {
		t.Fatal("test setup invalid: legacy and current hash should differ")
	}

	// 已经是新公式的行（较新代码写入）。
	if err := db.Create(&entity.Tiku{
		Question: question, Type: 0, Options: string(util.SortOptionsJSON(options)),
		Answer: `["新答案"]`, Hash: currentHash, CourseName: "课程",
	}).Error; err != nil {
		t.Fatalf("seed current row: %v", err)
	}
	// 旧公式的历史行：回填后会与上面那行撞唯一键。
	if err := db.Create(&entity.Tiku{
		Question: question, Type: 0, Options: string(util.SortOptionsJSON(options)),
		Answer: `["旧答案"]`, Hash: legacyHash, CourseName: "课程",
	}).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := ensureTikuHashWithoutPlatSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !tikuHashBackfillDone(db) {
		t.Fatal("expected the backfill flag to be written even when a row conflicts")
	}

	// 一行都不能少，冲突行保留旧 hash。
	var count int64
	if err := db.Table(entity.TableNameTiku).Where("question = ?", question).Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected both rows to survive, got %d", count)
	}
	var kept int64
	if err := db.Table(entity.TableNameTiku).
		Where("question = ? AND hash = ?", question, legacyHash).Count(&kept).Error; err != nil {
		t.Fatalf("count legacy hash: %v", err)
	}
	if kept != 1 {
		t.Fatalf("expected the conflicting row to keep its legacy hash, found %d", kept)
	}
}

// plat 列必须被删除，且不影响数据行。
func TestEnsureTikuPlatColumnDroppedRemovesColumn(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	// AutoMigrate 建出的表已不含 plat（实体里删掉了），这里手工补上模拟旧库。
	if err := db.Exec("ALTER TABLE " + entity.TableNameTiku + " ADD COLUMN plat int NOT NULL DEFAULT -1").Error; err != nil {
		t.Fatalf("add legacy plat column: %v", err)
	}
	if err := db.Create(&entity.Tiku{
		Question: "带 plat 的历史题目",
		Type:     0,
		Options:  `["A"]`,
		Answer:   `["答案"]`,
		Hash:     util.BuildTikuHash("带 plat 的历史题目", []string{"A"}, 0),
	}).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}

	if err := ensureTikuPlatColumnDropped(db); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if db.Migrator().HasColumn(entity.TableNameTiku, "plat") {
		t.Fatal("expected the plat column to be dropped")
	}

	// 数据行必须还在。
	var count int64
	if err := db.Table(entity.TableNameTiku).Where("question = ?", "带 plat 的历史题目").Count(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected the row to survive the column drop, got %d", count)
	}
}

// 列不存在时（全新库）必须标记完成且不报错。
func TestEnsureTikuPlatColumnDroppedSkipsWhenColumnMissing(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	if db.Migrator().HasColumn(entity.TableNameTiku, "plat") {
		t.Skip("fresh schema unexpectedly contains plat")
	}
	if err := ensureTikuPlatColumnDropped(db); err != nil {
		t.Fatalf("expected no error when the column is already gone, got %v", err)
	}
	if !systemSettingFlagIsSet(db, tikuPlatColumnDroppedKey) {
		t.Fatal("expected the drop marker to be written for a fresh schema")
	}
}

// 幂等：重复调用不再尝试删列。
func TestEnsureTikuPlatColumnDroppedIsIdempotent(t *testing.T) {
	db := newTikuMigrationTestDB(t)

	if err := db.Exec("ALTER TABLE " + entity.TableNameTiku + " ADD COLUMN plat int NOT NULL DEFAULT -1").Error; err != nil {
		t.Fatalf("add legacy plat column: %v", err)
	}

	if err := ensureTikuPlatColumnDropped(db); err != nil {
		t.Fatalf("first drop: %v", err)
	}
	if err := ensureTikuPlatColumnDropped(db); err != nil {
		t.Fatalf("second drop: %v", err)
	}
	if db.Migrator().HasColumn(entity.TableNameTiku, "plat") {
		t.Fatal("expected the plat column to stay dropped")
	}
}
