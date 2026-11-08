package service

import (
	"database/sql"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

// openUserExamFixture 打开一个可写的测试库；只用于造 fixture。
func openUserExamFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping fixture %s: %v", path, err)
	}
	return db
}
