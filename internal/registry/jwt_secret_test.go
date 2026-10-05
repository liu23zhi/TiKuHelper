package registry

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func newJWTSecretTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:jwt-secret-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open jwt secret test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate jwt secret test db: %v", err)
	}
	return db
}

func readStoredJWTSecret(t *testing.T, db *gorm.DB) string {
	t.Helper()
	secret, err := loadStoredJWTSecret(db)
	if err != nil {
		t.Fatalf("load stored jwt secret: %v", err)
	}
	return secret
}

func TestResolveJWTSecretPrefersEnvironmentAndPersistsIt(t *testing.T) {
	db := newJWTSecretTestDB(t)
	envSecret := "env-secret-0123456789abcdef-0123456789"
	configuredSecret := "configured-secret-0123456789abcdef"

	got, err := resolveJWTSecret(db, configuredSecret, envSecret)
	if err != nil {
		t.Fatalf("resolve jwt secret: %v", err)
	}
	if got != envSecret {
		t.Fatalf("expected environment secret, got %q", got)
	}
	if stored := readStoredJWTSecret(t, db); stored != envSecret {
		t.Fatalf("expected environment secret persisted, got %q", stored)
	}
}

func TestResolveJWTSecretUsesConfiguredValueWhenEnvironmentIsEmpty(t *testing.T) {
	db := newJWTSecretTestDB(t)
	configuredSecret := "configured-secret-0123456789abcdef"

	got, err := resolveJWTSecret(db, configuredSecret, "")
	if err != nil {
		t.Fatalf("resolve jwt secret: %v", err)
	}
	if got != configuredSecret {
		t.Fatalf("expected configured secret, got %q", got)
	}
	if stored := readStoredJWTSecret(t, db); stored != configuredSecret {
		t.Fatalf("expected configured secret persisted, got %q", stored)
	}
}

func TestResolveJWTSecretReusesStoredSecretWhenEnvironmentIsEmpty(t *testing.T) {
	db := newJWTSecretTestDB(t)
	storedSecret := "stored-secret-0123456789abcdef-0123456789"
	if _, err := persistJWTSecret(db, storedSecret); err != nil {
		t.Fatalf("persist stored jwt secret: %v", err)
	}

	got, err := resolveJWTSecret(db, "", "")
	if err != nil {
		t.Fatalf("resolve jwt secret: %v", err)
	}
	if got != storedSecret {
		t.Fatalf("expected stored secret, got %q", got)
	}
}

func TestResolveJWTSecretGeneratesWhenEnvironmentIsInvalid(t *testing.T) {
	db := newJWTSecretTestDB(t)
	if _, err := persistJWTSecret(db, "stored-secret-0123456789abcdef-0123456789"); err != nil {
		t.Fatalf("persist stored jwt secret: %v", err)
	}

	got, err := resolveJWTSecret(db, "", "invalid")
	if err != nil {
		t.Fatalf("resolve jwt secret: %v", err)
	}
	if got == "invalid" {
		t.Fatal("expected invalid environment secret to be replaced")
	}
	if err := util.ValidateJWTSecret(got); err != nil {
		t.Fatalf("generated secret should be valid: %v", err)
	}
	if stored := readStoredJWTSecret(t, db); stored != got {
		t.Fatalf("expected generated secret persisted, got %q", stored)
	}
}

func TestResolveJWTSecretGeneratesAndReusesWhenNoSecretExists(t *testing.T) {
	db := newJWTSecretTestDB(t)

	first, err := resolveJWTSecret(db, "", "")
	if err != nil {
		t.Fatalf("generate jwt secret: %v", err)
	}
	second, err := resolveJWTSecret(db, "", "")
	if err != nil {
		t.Fatalf("reload jwt secret: %v", err)
	}
	if first != second {
		t.Fatalf("expected generated secret to be reused, first=%q second=%q", first, second)
	}
}
