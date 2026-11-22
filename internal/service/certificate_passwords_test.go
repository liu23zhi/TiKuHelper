package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func certificatePasswordTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "settings.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestGatewayCertificatePasswordsNormalizeAndValidate(t *testing.T) {
	got, err := NormalizeGatewayCertificatePasswords([]string{" one ", "", "one ", "two\r", "two\r"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(got) != 3 || got[0] != " one " || got[1] != "one " || got[2] != "two" {
		t.Fatalf("unexpected normalized passwords: %#v", got)
	}
	tooLong := strings.Repeat("x", MaxGatewayCertificatePasswordBytes+1)
	if _, err := NormalizeGatewayCertificatePasswords([]string{tooLong}); err != ErrGatewayCertificatePasswordTooLong {
		t.Fatalf("expected length error, got %v", err)
	}
	tooMany := make([]string, MaxGatewayCertificatePasswordCandidates+1)
	for i := range tooMany {
		tooMany[i] = string(rune('a'+i)) + "-candidate"
	}
	if _, err := NormalizeGatewayCertificatePasswords(tooMany); err != ErrTooManyGatewayCertificatePasswords {
		t.Fatalf("expected count error, got %v", err)
	}
}

func TestGatewayCertificatePasswordsMetadataNeverReturnsValue(t *testing.T) {
	db := certificatePasswordTestDB(t)
	if _, err := SetGatewayCertificatePasswordsWithDB(db, []string{"secret-one", "secret-two"}); err != nil {
		t.Fatalf("save passwords: %v", err)
	}
	state, err := GetGatewayCertificatePasswordStateWithDB(db)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Count != 2 || state.UpdatedAt == nil {
		t.Fatalf("unexpected state: %+v", state)
	}
	passwords, err := GetGatewayCertificatePasswordsWithDB(db)
	if err != nil || len(passwords) != 2 || passwords[0] != "secret-one" {
		t.Fatalf("gateway reader mismatch: %#v, %v", passwords, err)
	}
	if _, err := SetGatewayCertificatePasswordsWithDB(db, nil); err != nil {
		t.Fatalf("clear passwords: %v", err)
	}
	state, err = GetGatewayCertificatePasswordStateWithDB(db)
	if err != nil || state.Count != 0 {
		t.Fatalf("expected cleared metadata: %+v, %v", state, err)
	}
}
