package registry

import (
	"bytes"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS04ApplicationMode(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		bad      bool
	}{{"", "production", false}, {" production ", "production", false}, {"development", "development", false}, {"DEVELOPMENT", "development", false}, {"dev", "", true}, {"test", "", true}} {
		got, err := ResolveApplicationMode(tc.in)
		if (err != nil) != tc.bad || got != tc.want {
			t.Fatalf("mode %q: %q %v", tc.in, got, err)
		}
	}
}
func TestS04ProductionAdminPlan(t *testing.T) {
	for _, tc := range []struct {
		name, stored, env, mode string
		bad, unchanged          bool
	}{
		{"new_empty", "", "", "production", true, false}, {"new_weak", "", "weak", "production", true, false},
		{"new_default", "", "123456", "production", true, false}, {"new_strong", "", "FixtureAdmin123!", "production", false, false},
		{"new_short", "", "Aa1!abc", "production", true, false}, {"no_upper", "", "abcdefgh1!", "production", true, false},
		{"no_lower", "", "ABCDEFGH1!", "production", true, false}, {"no_digit", "", "Abcdefghi!", "production", true, false},
		{"no_special", "", "Abcdefghi1", "production", true, false},
		{"bytes72", "", "Aa1!" + strings.Repeat("x", 68), "production", false, false},
		{"bytes73", "", "Aa1!" + strings.Repeat("x", 69), "production", true, false},
		{"unicode_under72", "", "Aa1!" + strings.Repeat("题", 22), "production", false, false},
		{"unicode_over72", "", "Aa1!" + strings.Repeat("题", 23), "production", true, false},
		{"existing_plain", "LegacyWeak", "different", "production", false, false},
		{"existing_plain_long", strings.Repeat("x", 73), "FixtureAdmin123!", "production", true, false},
		{"existing_default", "123456", "FixtureAdmin123!", "production", true, false},
		{"existing_hash_default", "HASH_DEFAULT", "FixtureAdmin123!", "production", true, true},
		{"existing_hash", "HASH_CUSTOM", strings.Repeat("x", 100), "production", false, true},
		{"dev_default", "", "", "development", false, false},
		{"dev_weak", "", "weak", "development", false, false},
		{"dev_existing_default", "123456", "FixtureAdmin123!", "development", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := gorm.Open(sqlite.Open("file:"+tc.name+"?mode=memory&cache=shared"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := d.DB()
			defer sqlDB.Close()
			if err = d.AutoMigrate(&entity.User{}); err != nil {
				t.Fatal(err)
			}
			stored := tc.stored
			plain := stored
			if stored == "HASH_DEFAULT" {
				plain = "123456"
				stored, err = util.HashPassword(plain)
			}
			if stored == "HASH_CUSTOM" {
				plain = "Existing456!"
				stored, err = util.HashPassword(plain)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.stored != "" {
				if err = d.Create(&entity.User{ID: 1, Username: "fixture", Password: stored, Nickname: "unchanged", Balance: 1.234567, Role: "admin", Status: "enabled", RegisterSource: "fixture"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			plan, err := resolveAdminBootstrap(d, tc.mode, tc.env)
			if (err != nil) != tc.bad {
				t.Fatalf("plan accepted=%v want=%v", err == nil, !tc.bad)
			}
			if tc.bad {
				var count int64
				d.Model(&entity.User{}).Count(&count)
				if count != boolCount(tc.stored != "") {
					t.Fatal("validation changed rows")
				}
				if stored != "" {
					var got entity.User
					d.First(&got, 1)
					if got.Password != stored {
						t.Fatal("validation changed credential")
					}
				}
				return
			}
			if err := applyAdminBootstrap(d, plan); err != nil {
				t.Fatal(err)
			}
			var got entity.User
			if err = d.First(&got, 1).Error; err != nil {
				t.Fatal(err)
			}
			want := plain
			if tc.stored == "" {
				want = strings.TrimSpace(tc.env)
				if want == "" {
					want = "123456"
				}
			}
			matched, upgrade := util.VerifyPassword(got.Password, want)
			if !matched || upgrade {
				t.Fatal("credential not same-value bcrypt")
			}
			if tc.unchanged && got.Password != stored {
				t.Fatal("existing bcrypt bytes changed")
			}
			if tc.stored != "" && (got.Username != "fixture" || got.Nickname != "unchanged" || got.Balance != 1.234567 || got.RegisterSource != "fixture") {
				t.Fatal("unrelated account fields changed")
			}
		})
	}
}
func boolCount(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
func TestS04BootstrapFailureHasNoWrites(t *testing.T) {
	t.Setenv("SQL_DSN", "")
	t.Setenv("TIKU_APP_MODE", "production")
	t.Setenv("TIKU_ADMIN_PASSWORD", "weak")
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			resetRegistryDBState(t)
			defer resetRegistryDBState(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "nested", "synthetic.db")
			var before []byte
			if existing {
				os.MkdirAll(filepath.Dir(path), 0700)
				d, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
				if err != nil {
					t.Fatal(err)
				}
				d.Exec("CREATE TABLE users(id integer primary key,password text)")
				d.Exec("INSERT INTO users VALUES(1,'123456')")
				sqlDB, _ := d.DB()
				sqlDB.Close()
				before, _ = os.ReadFile(path)
			}
			got, err := RegisterDBWithError(configs.Config{Database: configs.DatabaseConfig{Path: path}})
			if err == nil || got != nil {
				t.Fatal("weak production startup accepted")
			}
			if existing {
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("strict rejection changed pristine DB bytes")
				}
			} else if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("strict rejection created directory: %v", err)
			}
		})
	}
}

func TestS04SQLiteURIIdentityAndNoWrites(t *testing.T) {
	t.Setenv("SQL_DSN", "")
	t.Setenv("TIKU_APP_MODE", "production")
	t.Setenv("TIKU_ADMIN_PASSWORD", "FixtureAdmin123!")
	dir := t.TempDir()
	path := filepath.Join(dir, "credential%2Ffixture.db")
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	parsed, memory, err := sqliteFilesystemPath(uri)
	if err != nil || memory || parsed != path {
		t.Fatalf("URI identity: %s %v %v", parsed, memory, err)
	}
	d, err := gorm.Open(sqlite.Open(uri), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Exec("CREATE TABLE users(id integer primary key,password text)").Error; err != nil {
		t.Fatal(err)
	}
	if err = d.Exec("INSERT INTO users VALUES(1,'123456')").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := d.DB()
	sqlDB.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	resetRegistryDBState(t)
	defer resetRegistryDBState(t)
	if got, err := RegisterDBWithError(configs.Config{Database: configs.DatabaseConfig{Path: uri}}); err == nil || got != nil {
		t.Fatal("URI known-default accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected URI DB bytes changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "credential", "fixture.db")); !os.IsNotExist(err) {
		t.Fatal("URI decoded twice")
	}
}
func TestS04ExistingEmptyAdminUsesStrongBootstrapOnly(t *testing.T) {
	d, err := gorm.Open(sqlite.Open("file:empty-admin?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := d.DB()
	defer sqlDB.Close()
	if err = d.AutoMigrate(&entity.User{}); err != nil {
		t.Fatal(err)
	}
	if err = d.Create(&entity.User{ID: 1, Username: "fixture", Password: "", Role: "admin", Status: "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAdminBootstrap(d, "production", "weak"); err == nil {
		t.Fatal("weak empty-admin bootstrap accepted")
	}
	var before entity.User
	d.First(&before, 1)
	if before.Password != "" {
		t.Fatal("rejection changed empty credential")
	}
	plan, err := resolveAdminBootstrap(d, "production", "FixtureAdmin123!")
	if err != nil {
		t.Fatal(err)
	}
	if err = applyAdminBootstrap(d, plan); err != nil {
		t.Fatal(err)
	}
	var got entity.User
	d.First(&got, 1)
	if ok, upgrade := util.VerifyPassword(got.Password, "FixtureAdmin123!"); !ok || upgrade {
		t.Fatal("strong bootstrap not bcrypt")
	}
}

func TestS04FullCredentialStartupPreservesOrRejectsExisting(t *testing.T) {
	t.Setenv("SQL_DSN", "")
	for _, tc := range []struct {
		name, stored, plain, environment, mode string
		bad, preserve                          bool
	}{
		{"plain_same_value", "LegacyWeak", "LegacyWeak", "FixtureAdmin123!", "production", false, false},
		{"bcrypt_ignores_long_environment", "HASH", "ExistingAdmin456!", strings.Repeat("x", 100), "production", false, true},
		{"default_bcrypt_rejected", "HASH", "123456", "FixtureAdmin123!", "production", true, true},
		{"long_plain_rejected", strings.Repeat("x", 73), "", "FixtureAdmin123!", "production", true, true},
		{"empty_admin_strong_bootstrap", "", "FixtureAdmin123!", "FixtureAdmin123!", "production", false, false},
		{"default_plain_development_same_value", "123456", "123456", "FixtureAdmin123!", "development", false, false},
		{"invalid_mode_no_writes", "LegacyWeak", "LegacyWeak", "FixtureAdmin123!", "invalid", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TIKU_APP_MODE", tc.mode)
			t.Setenv("TIKU_ADMIN_PASSWORD", tc.environment)
			resetRegistryDBState(t)
			defer resetRegistryDBState(t)
			path := filepath.Join(t.TempDir(), "synthetic.db")
			d, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err = d.AutoMigrate(&entity.User{}); err != nil {
				t.Fatal(err)
			}
			stored := tc.stored
			if stored == "HASH" {
				stored, err = util.HashPassword(tc.plain)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = d.Create(&entity.User{ID: 1, Username: "existing-fixture", Password: stored, Nickname: "unchanged", Balance: 1.234567, Role: "admin", Status: "enabled", RegisterSource: "fixture"}).Error; err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := d.DB()
			sqlDB.Close()
			before, _ := os.ReadFile(path)
			gotDB, err := RegisterDBWithError(configs.Config{Database: configs.DatabaseConfig{Path: path}})
			if (err != nil) != tc.bad {
				t.Fatalf("startup error=%v expectedbad=%v", err, tc.bad)
			}
			if tc.bad {
				if gotDB != nil {
					t.Fatal("rejected startup returnedDB")
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("rejected startup wrote DB")
				}
				return
			}
			var got entity.User
			if err = gotDB.First(&got, 1).Error; err != nil {
				t.Fatal(err)
			}
			if match, upgrade := util.VerifyPassword(got.Password, tc.plain); !match || upgrade {
				t.Fatal("startup changed original password value")
			}
			if tc.preserve && got.Password != stored {
				t.Fatal("existing bcrypt bytes changed")
			}
			if got.Username != "existing-fixture" || got.Nickname != "unchanged" || got.Balance != 1.234567 || got.RegisterSource != "fixture" {
				t.Fatal("startup changed unrelated fields")
			}
		})
	}
}
