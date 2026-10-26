package registry

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestAuditCorrelationMigrationAddsIndexedColumnsWithoutHistoricalGuessing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE logs (id integer PRIMARY KEY, user_id integer, username varchar(255), description text)",
		"CREATE TABLE sms_verification_codes (id integer PRIMARY KEY, phone varchar(32), scene varchar(32))",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Production-sized history must not turn this structural change into an
	// ownership backfill or a query-per-row startup cost.
	for offset := 0; offset < 30000; offset += 500 {
		values, arguments := make([]string, 0, 500), make([]any, 0, 2000)
		for i := 0; i < 500; i++ {
			values = append(values, "(?, ?, ?, ?)")
			arguments = append(arguments, offset+i+1, 0, "anonymous", fmt.Sprintf("legacy captcha %d", offset+i))
		}
		if err := db.Exec("INSERT INTO logs (id, user_id, username, description) VALUES "+strings.Join(values, ","), arguments...).Error; err != nil {
			t.Fatal(err)
		}
	}
	reads := 0
	if err := db.Callback().Query().Before("gorm:query").Register("audit_history_read_counter", func(tx *gorm.DB) {
		if tx.Statement.Table == "logs" || tx.Statement.Table == "sms_verification_codes" {
			reads++
		}
	}); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	if err := ensureAuditCorrelationSchema(db); err != nil {
		t.Fatal(err)
	}
	t.Logf("indexed correlation migration with 30000 historical logs took %s", time.Since(startedAt))
	if reads != 0 {
		t.Fatalf("structural migration read %d historical data queries", reads)
	}
	for _, check := range []struct {
		model any
		field string
		index string
	}{
		{&entity.Log{}, "CorrelationID", "idx_logs_correlation_id"},
		{&entity.SMSVerificationCode{}, "CaptchaChallengeID", "idx_sms_verification_codes_captcha_challenge_id"},
	} {
		if !db.Migrator().HasColumn(check.model, check.field) || !db.Migrator().HasIndex(check.model, check.index) {
			t.Fatalf("missing indexed field %s", check.field)
		}
	}
	if !systemSettingFlagIsSet(db, auditCorrelationMigrationKey) {
		t.Fatal("successful migration must persist its marker")
	}
	var attributed int64
	if err := db.Table("logs").Where("COALESCE(correlation_id, '') <> '' OR user_id <> 0").Count(&attributed).Error; err != nil || attributed != 0 {
		t.Fatalf("migration must not infer historical ownership: count=%d err=%v", attributed, err)
	}
	if err := ensureAuditCorrelationSchema(db); err != nil {
		t.Fatalf("migration must be repeatable: %v", err)
	}
}

func TestAuditCorrelationMigrationDoesNotMarkIncompleteSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.Log{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureAuditCorrelationSchema(db); err == nil {
		t.Fatal("missing SMS table must report a structural error")
	}
	if systemSettingFlagIsSet(db, auditCorrelationMigrationKey) {
		t.Fatal("failed migration must remain retryable")
	}
	if err := db.AutoMigrate(&entity.SMSVerificationCode{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureAuditCorrelationSchema(db); err != nil {
		t.Fatalf("migration must recover after schema repaired: %v", err)
	}
}
