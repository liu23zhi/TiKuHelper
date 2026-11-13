package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/registry"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// The old injected loadConfigBeforeDatabase helper no longer exists. Exercise the
// real CreateManager path in isolated child processes; never recreate production
// seams for tests. Config and DB error branches remain separate observations.
func TestStrictConfigLoadBeforeDatabasePreservesError(t *testing.T) {
	if branch := os.Getenv("LEGACY_CONFIG_CHILD"); branch != "" {
		before, err := os.ReadFile("config.yaml")
		if branch == "read_error" {
			before = nil
		} else if err != nil {
			t.Fatal(err)
		}
		c, configErr := registry.ConfigWithError()
		m, managerErr := CreateManager()
		if m != nil || managerErr == nil || GetManager() != nil {
			t.Fatalf("failed startup published manager: %v %v", m, managerErr)
		}
		if branch == "read_error" {
			var pathErr *os.PathError
			if !errors.Is(configErr, fs.ErrPermission) || !errors.Is(managerErr, fs.ErrPermission) || !errors.As(managerErr, &pathErr) || !reflect.DeepEqual(c, configs.Config{}) || !strings.HasPrefix(managerErr.Error(), "load configuration: ") {
				t.Fatalf("config chain/zero-value boundary c=%+v config=%v manager=%v", c, configErr, managerErr)
			}
		} else {
			cause := errors.Unwrap(managerErr)
			if configErr != nil || c.Database.Path != "file:%ZZ" || cause == nil || !errors.Is(managerErr, cause) || cause.Error() != "invalid SQLite database URI" || !strings.HasPrefix(managerErr.Error(), "initialize database: ") {
				t.Fatalf("valid typed config/database error boundary c=%+v config=%v manager=%v", c, configErr, managerErr)
			}
			after, readErr := os.ReadFile("config.yaml")
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("startup mutated valid typed input")
			}
		}
		if _, err := os.Stat("tiku.db"); !os.IsNotExist(err) {
			t.Fatal("failed startup created database")
		}
		row, _ := json.Marshal(map[string]any{"branch": branch, "nilManager": m == nil, "errorChainPreserved": true, "noDatabaseCreated": true, "typedConfig": c.Database.Path})
		t.Log("LEGACY_CONFIG_OBSERVATION " + string(row))
		return
	}
	for _, branch := range []string{"read_error", "valid_config_database_error"} {
		t.Run(branch, func(t *testing.T) {
			if branch == "read_error" && runtime.GOOS == "windows" {
				// 同 internal/registry/config_loading_test.go 的 permission 子用例：
				// Windows 不实现 POSIX 权限位，`os.Chmod(p, 0000)` 之后子进程仍然
				// 能读到 config.yaml，于是「读取失败 → 错误链里带 fs.ErrPermission」
				// 这一格在本平台根本构造不出来。
				//
				// 要在这里表达「读不了」必须用 ACL，那是与 viper 错误链无关的另一套
				// API。显式 Skip（并把原因写清）而不是让它静默 pass —— 否则
				// 「Windows 上权限错误链真的坏了」将永久不可见。
				t.Skip("Windows 不实现 POSIX 权限位，chmod 0000 不阻止子进程读取（平台语义差异，非被测缺陷）")
			}
			dir := t.TempDir()
			p := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(p, []byte("database:\n  path: 'file:%ZZ'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if branch == "read_error" {
				if err := os.Chmod(p, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(p, 0600) })
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestStrictConfigLoadBeforeDatabasePreservesError$", "-test.v")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "LEGACY_CONFIG_CHILD="+branch, "SQL_DSN=", "TIKU_APP_MODE=production", "TIKU_ADMIN_PASSWORD=QaSynthetic246!")
			out, err := cmd.CombinedOutput()
			t.Log(string(out))
			if err != nil {
				t.Fatalf("isolated production entry failed: %v", err)
			}
		})
	}
}
