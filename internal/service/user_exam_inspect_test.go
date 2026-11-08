package service

import (
	"os"
	"path/filepath"
	"testing"
)

// writeUserExamFixture 造一个与考试客户端同构的明文库：
// baseInfo(Token, schoolId, schoolName, thirdFild) + macAddress(Address, Flag)。
func writeUserExamFixture(t *testing.T, directory string, extraTable bool) string {
	t.Helper()
	path := filepath.Join(directory, "UserExamInfo.db")
	db := openUserExamFixture(t, path)
	defer db.Close()

	statements := []string{
		`CREATE TABLE baseInfo(Token TEXT, schoolId TEXT, schoolName TEXT, thirdFild INTEGER)`,
		`CREATE TABLE macAddress(Address TEXT, Flag Text)`,
		`INSERT INTO baseInfo VALUES('84261c02fd12a91ddb9f6f85abd7d5eb','196586','某某工程大学自考',0)`,
		`INSERT INTO macAddress VALUES('','wsh')`,
	}
	if extraTable {
		statements = append(statements,
			`CREATE TABLE unrelated(id INTEGER PRIMARY KEY, payload BLOB)`,
			`INSERT INTO unrelated VALUES(1, x'00ff')`)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
	return path
}

func TestInspectUserExamDatabaseReadsKnownTables(t *testing.T) {
	directory := t.TempDir()
	path := writeUserExamFixture(t, directory, false)

	result, err := InspectUserExamDatabase(path)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(result.Tables) != 2 {
		t.Fatalf("expected baseInfo and macAddress, got %v", result.Tables)
	}
	baseInfo, ok := result.Tables[userExamBaseInfoTable]
	if !ok {
		t.Fatal("baseInfo missing")
	}
	if len(baseInfo.Columns) != 4 {
		t.Fatalf("unexpected baseInfo columns: %v", baseInfo.Columns)
	}
	if len(baseInfo.Rows) != 1 {
		t.Fatalf("unexpected baseInfo rows: %d", len(baseInfo.Rows))
	}
	if baseInfo.Rows[0]["schoolId"] != "196586" {
		t.Fatalf("unexpected schoolId: %v", baseInfo.Rows[0]["schoolId"])
	}
	if result.RowCounts[userExamBaseInfoTable] != 1 || result.RowCounts[userExamMacAddressTable] != 1 {
		t.Fatalf("unexpected row counts: %v", result.RowCounts)
	}
	if len(result.OtherTables) != 0 {
		t.Fatalf("unexpected other tables: %v", result.OtherTables)
	}
}

func TestInspectUserExamDatabaseOnlyNamesUnknownTables(t *testing.T) {
	directory := t.TempDir()
	path := writeUserExamFixture(t, directory, true)

	result, err := InspectUserExamDatabase(path)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(result.OtherTables) != 1 || result.OtherTables[0] != "unrelated" {
		t.Fatalf("unexpected other tables: %v", result.OtherTables)
	}
	if _, leaked := result.Tables["unrelated"]; leaked {
		t.Fatal("unknown tables must only be named, never read")
	}
}

func TestInspectUserExamDatabaseRejectsEncryptedOrNonSQLite(t *testing.T) {
	directory := t.TempDir()

	notDatabase := filepath.Join(directory, "broken.db")
	if err := os.WriteFile(notDatabase, []byte("not a database at all"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := InspectUserExamDatabase(notDatabase); err == nil {
		t.Fatal("expected non-SQLite input to be rejected")
	}

	// 加密库的头部不是 "SQLite format 3\0"。
	encrypted := filepath.Join(directory, "UserExamInfo.db")
	if err := os.WriteFile(encrypted, []byte("SQLite format 4\x00encrypted payload"), 0o600); err != nil {
		t.Fatalf("write encrypted fixture: %v", err)
	}
	if _, err := InspectUserExamDatabase(encrypted); err == nil {
		t.Fatal("expected encrypted input to be rejected")
	}
}

func TestInspectUserExamDatabaseDoesNotWriteSidecars(t *testing.T) {
	directory := t.TempDir()
	path := writeUserExamFixture(t, directory, false)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	if _, err := InspectUserExamDatabase(path); err != nil {
		t.Fatalf("inspect: %v", err)
	}

	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			t.Fatalf("inspect must not create %s", suffix)
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("inspect must not modify the source file")
	}
}

func TestInspectUserExamDatabaseRejectsOversizedFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "huge.db")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := file.Truncate(MaxUserExamInspectBytes + 1); err != nil {
		file.Close()
		t.Fatalf("truncate: %v", err)
	}
	file.Close()

	if _, err := InspectUserExamDatabase(path); err == nil {
		t.Fatal("expected oversized input to be rejected")
	}
}
