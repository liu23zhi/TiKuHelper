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
		reject, missing                      bool
	}{
		{name: "missing_production_password", reject: true, missing: true},
		{name: "weak_production_password", password: "123456", reject: true, missing: true},
		{name: "explicit_development_compat", mode: "development", expected: "123456"},
		{name: "strong_production_bootstrap", password: "Create#123", expected: "Create#123"},
		{name: "legacy_same_secret_upgrade_not_environment_reset", password: "Replace#456", seed: "Original#123", expected: "Original#123"},
		{name: "known_default_existing_refused_not_reset", password: "Replace#456", seed: "123456", reject: true},
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
			if tc.reject {
				if err == nil {
					t.Fatalf("production policy accepted unsafe startup; child=%s", out)
				}
				if tc.missing {
					if _, e := os.Stat(path); !os.IsNotExist(e) {
						t.Fatal("rejected startup created SQLite file")
					}
					return
				}
			} else if err != nil {
				t.Fatalf("valid fixture rejected: %v child=%s", err, out)
			}
			d := integrationAdminDB(t, path)
			defer func() { s, _ := d.DB(); _ = s.Close() }()
			var row entity.User
			if e := d.Where("id=?", 1).First(&row).Error; e != nil {
				t.Fatal(e)
			}
			if tc.reject {
				if row.Password != tc.seed {
					t.Fatal("refused credential was reset")
				}
				return
			}
			matched, _ := util.VerifyPassword(row.Password, tc.expected)
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
