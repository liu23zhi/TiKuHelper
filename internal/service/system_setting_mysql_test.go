package service

import (
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestGetSystemSettingByKeyQuotesMySQLReservedColumn(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "user:password@tcp(127.0.0.1:3306)/test", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	var sql string
	if err := db.Callback().Query().After("gorm:query").Register("test:capture_setting_query", func(tx *gorm.DB) { sql = tx.Statement.SQL.String() }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := getSystemSettingByKey(db, "test_key"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "WHERE `key` = ?") {
		t.Fatalf("expected quoted MySQL key column, got %s", sql)
	}
}
