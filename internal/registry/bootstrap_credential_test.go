package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func checkInitialAdminRace(t *testing.T, d *gorm.DB) {
	t.Helper()
	plan, err := resolveAdminBootstrap(d, "production", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = applyAdminBootstrap(d.Session(&gorm.Session{DryRun: true}), plan); err != nil {
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
			t.Fatal(err)
		}
	}
	var admin entity.User
	if err = d.First(&admin, 1).Error; err != nil {
		t.Fatal(err)
	}
	if ok, upgrade := util.VerifyPassword(admin.Password, util.FactoryAdminPassword); !ok || upgrade {
		t.Fatal("concurrent startup lost factory credential")
	}
}

func TestInitialAdministratorRaceDryRunAndEmptyRow(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	d, err := gorm.Open(sqlite.Open(util.SQLiteConcurrentDSN(filepath.Join(dir, "fixture.db"))), &gorm.Config{})
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
	checkInitialAdminRace(t, d)
	var a entity.User
	if err = d.First(&a, 1).Error; err != nil || a.Username != "original" {
		t.Fatal("existing administrator replaced")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "admin-bootstrap-*.password")); len(files) != 0 {
		t.Fatal("bootstrap created a password file")
	}
}

func TestMariaDBConcurrentInitialAdministrator(t *testing.T) {
	d := mysqlTestFreshDB(t)
	mustAutoMigrate(t, d, &entity.User{})
	s, err := d.DB()
	if err != nil {
		t.Fatal(err)
	}
	s.SetMaxOpenConns(8)
	checkInitialAdminRace(t, d)
}

func TestLocalSetupDoesNotCreateMissingDatabaseOrAttachToMemory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("SQL_DSN", "")
	for _, p := range []string{"missing.db", ":memory:", "file:temporary?mode=memory&cache=shared"} {
		if err := CompleteLocalAdminSetup(configs.Config{Database: configs.DatabaseConfig{Path: p}}, util.FactoryAdminPassword, "KnownFixture-246!"); err == nil {
			t.Fatal("uninitialized database accepted operator command")
		}
	}
	items, err := os.ReadDir(dir)
	if err != nil || len(items) != 0 {
		t.Fatal("local setup created files instead of opening an existing database")
	}
}

func TestLocalAdminSetupIsOneShotAndDoesNotResetOtherUsers(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("SQL_DSN", "")
	for _, current := range []string{util.FactoryAdminPassword, "ChangeMe"} {
		p := filepath.Join(t.TempDir(), "fixture.db")
		cfg := configs.Config{Database: configs.DatabaseConfig{Path: p}}
		d, err := gorm.Open(sqlite.Open(util.SQLiteConcurrentDSN(p)), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		s, _ := d.DB()
		t.Cleanup(func() { s.Close() })
		if err = d.AutoMigrate(&entity.User{}); err != nil {
			t.Fatal(err)
		}
		hashed, err := util.HashPassword(current)
		if err != nil {
			t.Fatal(err)
		}
		a := entity.User{ID: 1, Username: "admin", Role: "admin", Password: hashed, LoginSessionID: "previous-session"}
		other := entity.User{ID: 2, Username: "other", Role: "user", Password: hashed}
		if err = d.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		if err = d.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		const replacement = "SelectedFixture!246"
		for _, bad := range [][2]string{{"wrong", replacement}, {current, util.FactoryAdminPassword}, {current, "CHANGEME"}, {current, ""}} {
			if err = CompleteLocalAdminSetup(cfg, bad[0], bad[1]); err == nil {
				t.Fatal("wrong current or reserved replacement accepted")
			}
		}
		if err = d.First(&a, 1).Error; err != nil || a.Password != hashed {
			t.Fatal("refused attempt changed stored credentials")
		}
		if err = CompleteLocalAdminSetup(cfg, current, replacement); err != nil {
			t.Fatal(err)
		}
		if err = d.First(&a, 1).Error; err != nil {
			t.Fatal(err)
		}
		if ok, _ := util.VerifyPassword(a.Password, replacement); !ok || a.LoginSessionID == "previous-session" || a.LoginSessionID == "" {
			t.Fatal("personalized password or session retirement failed")
		}
		if err = d.First(&other, 2).Error; err != nil || other.Password != hashed {
			t.Fatal("another account changed")
		}
		for _, claimed := range []string{current, replacement} {
			if err = CompleteLocalAdminSetup(cfg, claimed, "AnotherFixture!246"); err == nil {
				t.Fatal("local setup reset a personalized administrator")
			}
		}
	}
}
