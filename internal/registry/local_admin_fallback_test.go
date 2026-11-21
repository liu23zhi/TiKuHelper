package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
)

func TestFreshAdministratorUsesConfiguredOrFactoryCredentialWithoutFiles(t *testing.T) {
	for _, tc := range []struct{ name, environment, want string }{{"unset", "", "123456"}, {"explicit_default", "123456", "123456"}, {"explicit_placeholder", "ChangeMe", "ChangeMe"}, {"chosen", "ChosenFixture-209!", "ChosenFixture-209!"}} {
		t.Run(tc.name, func(t *testing.T) {
			resetRegistryDBState(t)
			t.Cleanup(func() { resetRegistryDBState(t) })
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("TIKU_ADMIN_PASSWORD", tc.environment)
			t.Setenv("TIKU_APP_MODE", "production")
			t.Setenv("SQL_DSN", "")
			cfg := configs.Config{Database: configs.DatabaseConfig{Path: filepath.Join(dir, "data", "tiku.db")}}
			d, err := RegisterDBWithError(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var row entity.User
			if err = d.First(&row, 1).Error; err != nil {
				t.Fatal(err)
			}
			if ok, upgrade := util.VerifyPassword(row.Password, tc.want); !ok || upgrade {
				t.Fatal("initial administrator does not match configured or fallback credential")
			}
			if files, _ := filepath.Glob(filepath.Join(dir, "data", "admin-bootstrap-*.password")); len(files) != 0 {
				t.Fatal("initialization published a password file")
			}
			before := row.Password
			resetRegistryDBState(t)
			t.Setenv("TIKU_ADMIN_PASSWORD", "OtherFixture123!")
			d, err = RegisterDBWithError(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = d.First(&row, 1).Error; err != nil || row.Password != before {
				t.Fatal("restart reset the existing administrator")
			}
			if _, err = os.Stat(filepath.Join(dir, "data", "tiku.db")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
