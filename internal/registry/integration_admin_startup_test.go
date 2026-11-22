package registry

import (
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func integrationAdminDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	d, e := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func TestIntegrationAdminBootstrapChild(t *testing.T) {
	if os.Getenv("ADMIN_INTEGRATION_CHILD") != "1" {
		t.Skip("isolated child only")
	}
	RegisterDB(configs.Config{Database: configs.DatabaseConfig{Path: os.Getenv("ADMIN_INTEGRATION_DB")}})
	if idempotencyCleanupStop != nil {
		idempotencyCleanupStop()
	}
	if db != nil {
		s, e := db.DB()
		if e == nil {
			_ = s.Close()
		}
	}
}
func TestIntegrationAdminBootstrap(t *testing.T) {
	cases := []struct {
		name, mode, password, seed, expected string
		defaultWarning                       bool
	}{
		{name: "missing_production_password_uses_default", expected: util.FactoryAdminPassword, defaultWarning: true},
		{name: "explicit_default_password_is_stored", password: util.FactoryAdminPassword, expected: util.FactoryAdminPassword, defaultWarning: true},
		{name: "explicit_placeholder_is_stored", password: "changeme", expected: "changeme", defaultWarning: true},
		{name: "operator_weak_non_placeholder_is_not_rejected", password: "mine", expected: "mine"},
		{name: "explicit_development_compat", mode: "development", expected: util.FactoryAdminPassword, defaultWarning: true},
		{name: "strong_production_bootstrap", password: "Create#123", expected: "Create#123"},
		{name: "legacy_same_secret_upgrade_not_environment_reset", password: "Replace#456", seed: "Original#123", expected: "Original#123"},
		{name: "known_default_existing_accepted_not_reset", password: "Replace#456", seed: util.FactoryAdminPassword, expected: util.FactoryAdminPassword, defaultWarning: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.db")
			if tc.seed != "" {
				d := integrationAdminDB(t, path)
				if e := d.AutoMigrate(&entity.User{}); e != nil {
					t.Fatal(e)
				}
				if e := d.Create(&entity.User{ID: 1, Username: "admin", Password: tc.seed, Nickname: "Fixture", Role: "admin", Status: "enabled"}).Error; e != nil {
					t.Fatal(e)
				}
				s, _ := d.DB()
				_ = s.Close()
			}
			c := exec.Command(os.Args[0], "-test.run=^TestIntegrationAdminBootstrapChild$", "-log-dir", filepath.Join(t.TempDir(), "logs"))
			c.Env = append(os.Environ(), "ADMIN_INTEGRATION_CHILD=1", "ADMIN_INTEGRATION_DB="+path, "TIKU_APP_MODE="+tc.mode, "TIKU_ADMIN_PASSWORD="+tc.password, "SQL_DSN=")
			out, err := c.CombinedOutput()
			if err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
			d := integrationAdminDB(t, path)
			defer func() { s, _ := d.DB(); _ = s.Close() }()
			var row entity.User
			if e := d.Where("id=?", 1).First(&row).Error; e != nil {
				t.Fatal(e)
			}
			expected := tc.expected
			if strings.Contains(string(out), "可直接网页登录") != tc.defaultWarning {
				t.Fatal("unexpected default credential startup warning")
			}
			if strings.Contains(string(out), expected) || strings.Contains(string(out), "--init-admin-password") {
				t.Fatal("startup disclosed plaintext credentials or required local setup")
			}
			if paths, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "admin-bootstrap-*.password")); len(paths) != 0 {
				t.Fatal("unexpected initial password file")
			}
			matched, _ := util.VerifyPassword(row.Password, expected)
			if !matched {
				t.Fatal("stored credential does not preserve intended secret")
			}
			if !strings.HasPrefix(row.Password, "$2") {
				t.Fatal("credential was not upgraded to bcrypt")
			}
			if tc.seed != "" {
				wrong, _ := util.VerifyPassword(row.Password, tc.password)
				if wrong {
					t.Fatal("environment silently reset existing credential")
				}
			}
		})
	}
}
