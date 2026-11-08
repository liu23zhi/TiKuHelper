package registry

import (
	"bytes"
	"context"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func dqRegistryRaw(t *testing.T, db *gorm.DB) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := db.Raw("SELECT quote(total_quota_limit) AS quota, typeof(total_quota_limit) AS storage, quote(status) AS status FROM access_tokens ORDER BY id").Scan(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}
func TestDisabledInvalidQuotaPolicyRegistryInspection(t *testing.T) {
	for _, c := range []struct {
		name, status string
		allow        bool
	}{{"disabled", "disabled", true}, {"disabled_case", " DISABLED ", true}, {"enabled", "enabled", false}, {"unknown", "paused", false}, {"blank", "", false}} {
		t.Run(c.name, func(t *testing.T) {
			db := newAccessTokenQuotaMigrationDB(t)
			if err := db.Create(&entity.AccessToken{UserID: 1, Name: "disabled_bad", TokenHash: fmt.Sprintf("%064x", 1), Status: "enabled", TotalQuotaLimit: -2}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&entity.AccessToken{}).Where("id = 1").Update("status", c.status).Error; err != nil {
				t.Fatal(err)
			}
			before := dqRegistryRaw(t, db)
			for i := 0; i < 2; i++ {
				err := ensureAccessTokenQuotaLimitData(db)
				if (err == nil) != c.allow {
					t.Errorf("startup exception allow=%t gotError=%t", c.allow, err != nil)
				}
			}
			if !reflect.DeepEqual(before, dqRegistryRaw(t, db)) {
				t.Error("inspection changed raw quota/status/storage")
			}
		})
	}
}
func TestDisabledInvalidQuotaPolicyStartup(t *testing.T) {
	if os.Getenv("TIKU_TEST_DQ_CHILD") == "1" {
		t.Fatal("parent called as child")
	}
	path := filepath.Join(t.TempDir(), "owned-disabled-quota.db")
	seed, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.AutoMigrate(&entity.AccessToken{}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Create(&entity.AccessToken{ID: 1, UserID: 1, Name: "disabled_bad", TokenHash: fmt.Sprintf("%064x", 1), TokenValue: "tk_synthetic_never_print", Status: "disabled", TotalQuotaLimit: 1e24}).Error; err != nil {
		t.Fatal(err)
	}
	before := dqRegistryRaw(t, seed)
	var schemaBefore string
	if err := seed.Raw("SELECT sql FROM sqlite_master WHERE name = 'access_tokens'").Scan(&schemaBefore).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := seed.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDisabledInvalidQuotaPolicyStartupChild$", "-test.v", "-test.timeout=18s")
	cmd.Env = append(os.Environ(), "TIKU_TEST_DQ_CHILD=1", "TIKU_TEST_DQ_DB="+path, "SQL_DSN=", "TIKU_APP_MODE=production", "TIKU_ADMIN_PASSWORD=OwnedOnlyQuotaPolicy!20261103")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	t.Logf("owned startup exitSuccess=%t\nDQ_CHILD_STDOUT:\n%s\nDQ_CHILD_STDERR:\n%s", runErr == nil, stdout.Bytes(), stderr.Bytes())
	if runErr != nil {
		t.Errorf("disabled historical key blocked other startup: %v", runErr)
	}
	if strings.Contains(stdout.String(), "tk_synthetic_never_print") {
		t.Error("startup leaked raw token")
	}
	check, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := check.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !reflect.DeepEqual(before, dqRegistryRaw(t, check)) {
		t.Error("real startup changed raw disabled value/type/status")
	}
	var schemaAfter string
	check.Raw("SELECT sql FROM sqlite_master WHERE name = 'access_tokens'").Scan(&schemaAfter)
	if schemaBefore != schemaAfter {
		t.Error("held token schema changed")
	}
	var quick string
	check.Raw("PRAGMA quick_check").Scan(&quick)
	if quick != "ok" {
		t.Error("owned database quick_check failed")
	}
}
func TestDisabledInvalidQuotaPolicyStartupChild(t *testing.T) {
	if os.Getenv("TIKU_TEST_DQ_CHILD") != "1" {
		t.Skip("private owned startup child")
	}
	path := os.Getenv("TIKU_TEST_DQ_DB")
	if !filepath.IsAbs(path) {
		t.Fatal("owned absolute DB path required")
	}
	RegisterDB(configs.Config{Database: configs.DatabaseConfig{Path: path}})
	defer CloseDB()
	if !db.Migrator().HasTable(&entity.AIOCRCache{}) || !db.Migrator().HasTable(&entity.BillingRecord{}) {
		t.Error("other domain models did not start")
	}
	t.Log("DISABLED_INVALID_QUOTA_OWNED_STARTUP_COMPLETE")
}
