package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func generatedAdminFile(t *testing.T, dir string) (string, string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "admin-bootstrap-*.password"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one operator-only initial password file, got %d", len(paths))
	}
	info, err := os.Lstat(paths[0])
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatal("initial password is not a private regular file")
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	password := strings.TrimSpace(string(raw))
	if len(password) < 26 || validateBootstrapPassword(password) != nil {
		t.Fatal("operator credential does not meet the generated password policy")
	}
	return paths[0], password
}

func TestProductionGeneratesPrivateInstanceCredential(t *testing.T) {
	t.Setenv("SQL_DSN", "")
	t.Setenv("TIKU_APP_MODE", "production")
	var prior []string
	for _, supplied := range []string{"", " 123456 ", "changeme"} {
		t.Run("instance", func(t *testing.T) {
			t.Setenv("TIKU_ADMIN_PASSWORD", supplied)
			resetRegistryDBState(t)
			t.Cleanup(func() { resetRegistryDBState(t) })
			dir := filepath.Join(t.TempDir(), "instance")
			config := configs.Config{Database: configs.DatabaseConfig{Path: filepath.Join(dir, "fixture.db")}}
			if _, err := preflightAdminBootstrap(config, "production", supplied); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("preflight wrote to disk")
			}
			d, err := RegisterDBWithError(config)
			if err != nil {
				t.Fatal(err)
			}
			var admin entity.User
			if err := d.First(&admin, 1).Error; err != nil {
				t.Fatal(err)
			}
			for _, invalid := range []string{"123456", "changeme"} {
				if ok, _ := util.VerifyPassword(admin.Password, invalid); ok {
					t.Fatal("a shared placeholder still authenticates")
				}
			}
			path, secret := generatedAdminFile(t, dir)
			if ok, upgrade := util.VerifyPassword(admin.Password, secret); !ok || upgrade {
				t.Fatal("operator password does not authenticate with bcrypt")
			}
			for _, old := range prior {
				if secret == old {
					t.Fatal("distinct fresh databases shared an initial password")
				}
			}
			prior = append(prior, secret)
			// Even a different environment must not reset the committed account or replace its file.
			t.Setenv("TIKU_ADMIN_PASSWORD", "replacement")
			resetRegistryDBState(t)
			d, err = RegisterDBWithError(config)
			if err != nil {
				t.Fatal(err)
			}
			var restarted entity.User
			if err = d.First(&restarted, 1).Error; err != nil || restarted.Password != admin.Password {
				t.Fatal("restart changed existing credentials")
			}
			reopened, existing := generatedAdminFile(t, dir)
			if reopened != path || secret != existing {
				t.Fatal("restart replaced initial password file")
			}
		})
	}
}

func TestGeneratedBootstrapDryRunAndConcurrentPlans(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "fixture.db")
	d, err := gorm.Open(sqlite.Open(util.SQLiteConcurrentDSN(path)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := d.DB()
	t.Cleanup(func() { s.Close() })
	if err = d.AutoMigrate(&entity.User{}); err != nil {
		t.Fatal(err)
	}
	if err = d.Create(&entity.User{ID: 1, Username: "original", Password: "", Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	plan, err := resolveAdminBootstrap(d, "production", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = applyAdminBootstrap(d.Session(&gorm.Session{DryRun: true}), plan); err != nil {
		t.Fatal("dry run must not materialize a password")
	}
	if paths, _ := filepath.Glob(filepath.Join(dir, "admin-bootstrap-*.password")); len(paths) != 0 {
		t.Fatal("dry run published a credential")
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; results <- applyAdminBootstrap(d, plan) }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("simultaneous instance bootstrap failed: %v", err)
		}
	}
	_, secret := generatedAdminFile(t, dir)
	var admin entity.User
	if err = d.First(&admin, 1).Error; err != nil || admin.Username != "original" {
		t.Fatal("empty-credential administrator was replaced")
	}
	if ok, _ := util.VerifyPassword(admin.Password, secret); !ok {
		t.Fatal("concurrent bootstrap published a different credential than it committed")
	}
}

func TestMemoryDatabaseNeverReusesOtherInitialCredential(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	set := map[string]bool{}
	for i := 0; i < 2; i++ {
		d, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, _ := d.DB()
		sqlDB.SetMaxOpenConns(1)
		if err = d.AutoMigrate(&entity.User{}); err != nil {
			t.Fatal(err)
		}
		plan, err := resolveAdminBootstrap(d, "production", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = applyAdminBootstrap(d, plan); err != nil {
			t.Fatal(err)
		}
		var admin entity.User
		if err = d.First(&admin, 1).Error; err != nil {
			t.Fatal(err)
		}
		_ = sqlDB.Close()
		paths, _ := filepath.Glob(filepath.Join(dir, "data", "admin-bootstrap-*.password"))
		if len(paths) != i+1 {
			t.Fatal("ephemeral instances did not publish separate operator files")
		}
		for _, p := range paths {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			value := string(bytes.TrimSpace(raw))
			match, _ := util.VerifyPassword(admin.Password, value)
			if _, known := set[value]; match == known {
				t.Fatal("credential identity crossed ephemeral instance boundary")
			}
			if match {
				set[value] = true
			}
		}
	}
}

func TestGeneratedBootstrapDoesNotCommitInaccessibleCredential(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("data", []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := d.DB()
	s.SetMaxOpenConns(1)
	t.Cleanup(func() { s.Close() })
	if err = d.AutoMigrate(&entity.User{}); err != nil {
		t.Fatal(err)
	}
	plan, err := resolveAdminBootstrap(d, "production", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = applyAdminBootstrap(d, plan); err == nil {
		t.Fatal("unreadable operator credential accepted")
	}
	var count int64
	if err = d.Model(&entity.User{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("unrecoverable administrator committed")
	}
	if got, err := os.ReadFile("data"); err != nil || string(got) != "not a directory" {
		t.Fatal("non-owned file changed")
	}
}

func TestMariaDBConcurrentGeneratedAdministrator(t *testing.T) {
	d := mysqlTestFreshDB(t) // isolated integration database; normal -short jobs without a DSN skip
	mustAutoMigrate(t, d, &entity.User{})
	s, err := d.DB()
	if err != nil {
		t.Fatal(err)
	}
	s.SetMaxOpenConns(8)
	dir := t.TempDir()
	t.Chdir(dir)
	plan, err := resolveAdminBootstrap(d, "production", "")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 6)
	for i := 0; i < cap(results); i++ {
		go func() { <-start; results <- applyAdminBootstrap(d, plan) }()
	}
	close(start)
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Errorf("parallel bootstrap failed: %v", err)
		}
	}
	_, secret := generatedAdminFile(t, filepath.Join(dir, "data"))
	var admin entity.User
	if err = d.First(&admin, 1).Error; err != nil {
		t.Fatal(err)
	}
	if ok, _ := util.VerifyPassword(admin.Password, secret); !ok {
		t.Fatal("published file differs from the committed credential")
	}
}
