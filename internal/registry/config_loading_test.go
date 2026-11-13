package registry

import (
	"errors"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/spf13/viper"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func s01FreshGlobal(t *testing.T) {
	t.Helper()
	v := viper.GetViper()
	prior := *v
	*v = *viper.New()
	t.Cleanup(func() { *v = prior })
}
func TestStrictConfigExistingPriorityAndLegalCoercion(t *testing.T) {
	s01FreshGlobal(t)
	t.Chdir(t.TempDir())
	t.Setenv("S01_JWTSECRET", "S01_SYNTHETIC_ENV")
	viper.SetEnvPrefix("S01")
	viper.AutomaticEnv()
	viper.Set("database.path", "S01_SYNTHETIC_OVERRIDE_DB")
	viper.Set("limit.enable", false)
	raw := []byte("jwtSecret: S01_SYNTHETIC_FILE\nlimit:\n  enable: true\n  duration: \"61\"\n  requests: 123\ndatabase:\n  path: S01_SYNTHETIC_FILE_DB\nunknownField: legacy-accepted\n")
	if err := os.WriteFile("config.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	legacy := Config()
	strict, err := ConfigWithError()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy, strict) || strict.Database.Path != "S01_SYNTHETIC_OVERRIDE_DB" || strict.Limit.Enable || strict.Limit.Duration != 61 || strict.JWTSecret != "S01_SYNTHETIC_ENV" {
		t.Errorf("changed prior override/env/coercion precedence legacy=%+v strict=%+v", legacy, strict)
	}
	viper.Set("jwtSecret", "S01_SYNTHETIC_OVERRIDE_JWT")
	strict, err = ConfigWithError()
	if err != nil || strict.JWTSecret != "S01_SYNTHETIC_OVERRIDE_JWT" {
		t.Error("explicit override lost precedence")
	}
}
func TestStrictConfigSamePathRepairNeverReturnsPartialOrStale(t *testing.T) {
	s01FreshGlobal(t)
	t.Chdir(t.TempDir())
	steps := []struct {
		raw       string
		wantError bool
		mysql     string
	}{{"mysql: FIRST_SYNTHETIC\n", false, "FIRST_SYNTHETIC"}, {"mysql: PARTIAL_SYNTHETIC\nlimit: [\n", true, ""}, {"mysql: REPAIRED_SYNTHETIC\n", false, "REPAIRED_SYNTHETIC"}, {"mysql: PARTIAL_SYNTHETIC\nlimit:\n  duration: [1,2]\n", true, ""}, {"mysql: RETRIED_SYNTHETIC\n", false, "RETRIED_SYNTHETIC"}}
	for _, step := range steps {
		if err := os.WriteFile("config.yaml", []byte(step.raw), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := ConfigWithError()
		if (err != nil) != step.wantError {
			t.Errorf("error state %v", err)
		}
		if step.wantError {
			if !reflect.DeepEqual(c, configs.Config{}) {
				t.Error("partial/stale config escaped")
			}
		} else if c.Mysql != step.mysql {
			t.Error("same path repair reused stale file layer")
		}
	}
	if err := os.Remove("config.yaml"); err != nil {
		t.Fatal(err)
	}
	c, err := ConfigWithError()
	if err != nil || c.Mysql != "" || c.Limit.Requests != 120 {
		t.Errorf("missing file failed to reset file layer: %+v %v", c, err)
	}
}
func TestStrictConfigMissingAndReadErrorBranches(t *testing.T) {
	dir := t.TempDir()
	missing := viper.New()
	missing.SetConfigName("config")
	missing.SetConfigType("yaml")
	missing.AddConfigPath(dir)
	c, err := readConfigWithError(missing, defaultConfig)
	if err != nil || c.Limit.Requests != 120 {
		t.Error("ConfigFileNotFoundError did not retain defaults")
	}
	explicit := viper.New()
	explicit.SetConfigFile(filepath.Join(dir, "explicit-missing.yaml"))
	explicit.SetConfigType("yaml")
	c, err = readConfigWithError(explicit, defaultConfig)
	if err != nil || c.Limit.Requests != 120 {
		t.Error("actual explicit ErrNotExist did not retain defaults")
	}
	badDefaults := viper.New()
	badDefaults.SetConfigFile(filepath.Join(dir, "missing-default.yaml"))
	badDefaults.SetConfigType("yaml")
	c, err = readConfigWithError(badDefaults, []byte("limit: [\n"))
	var parse viper.ConfigParseError
	if err == nil || !errors.As(err, &parse) || !reflect.DeepEqual(c, configs.Config{}) {
		t.Error("default parse error was swallowed")
	}
	bad := filepath.Join(dir, "malformed.yaml")
	if e := os.WriteFile(bad, []byte("limit: [\n"), 0600); e != nil {
		t.Fatal(e)
	}
	denied := filepath.Join(dir, "denied.yaml")
	if e := os.WriteFile(denied, []byte("mysql: SYNTHETIC\n"), 0000); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0600) })
	directory := filepath.Join(dir, "directory.yaml")
	if e := os.Mkdir(directory, 0700); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, path string
		permission bool
	}{{"malformed", bad, false}, {"permission", denied, true}, {"directory-io", directory, false}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.permission && runtime.GOOS == "windows" {
				// Windows 不实现 POSIX 权限位：`os.Chmod(f, 0000)` 之后
				// `os.ReadFile` 仍然成功（实测读到的就是文件内容），
				// 于是这里既没有 ErrPermission 也没有任何 error。
				//
				// 这不是被测量的缺陷，是平台语义差异 —— 要在这个平台表达
				// 「读不了」必须用 ACL（icacls / SetNamedSecurityInfo），
				// 那是另一套 API，与 viper 的错误链无关。
				//
				// 所以只在本平台跳过这一格，并**显式说明跳过的原因**：
				// 静默 pass 会让「Windows 上权限错误链真的坏了」永久不可见。
				t.Skip("Windows 不实现 POSIX 权限位，chmod 0000 不阻止读取（平台语义差异，非被测缺陷）")
			}
			v := viper.New()
			v.SetConfigFile(tc.path)
			v.SetConfigType("yaml")
			c, err := readConfigWithError(v, defaultConfig)
			if err == nil || !reflect.DeepEqual(c, configs.Config{}) || errors.Is(err, fs.ErrNotExist) {
				t.Errorf("invalid read fallback c=%+v err=%v", c, err)
			}
			if tc.permission && !errors.Is(err, fs.ErrPermission) {
				t.Errorf("permission error chain changed: %v", err)
			}
		})
	}
}
