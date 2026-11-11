package registry

import (
	"bytes"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
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
		// 2026-11-10 用户拍板：production 下「库里仍是已知默认密码 123456」
		// **不再拒绝启动**（避免一键 docker 起默认库后进不去后台改密码的死锁），
		// 改为照常启动 + WebUI 告警。因此 bad 由 true 改 false；
		// 校验点平移到 plan.defaultCredential（见下方 wantDefaultCredential 断言）。
		{"existing_default", "123456", "FixtureAdmin123!", "production", false, false},
		{"existing_hash_default", "HASH_DEFAULT", "FixtureAdmin123!", "production", false, true},
		{"existing_hash", "HASH_CUSTOM", strings.Repeat("x", 100), "production", false, true},
		{"dev_default", "", "", "development", false, false},
		{"dev_weak", "", "weak", "development", false, false},
		{"dev_existing_default", "123456", "FixtureAdmin123!", "development", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantDefaultCredential := tc.stored == "123456" || tc.stored == "HASH_DEFAULT"
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
			// 默认密码的校验点**平移**到这里：不再以「是否报错」判断，
			// 而以「是否识别出 defaultCredential」判断。
			// 少了这条，等于把「已知默认密码」这条语义整条删掉 ——
			// WebUI 告警需求会零测试覆盖。
			if plan.defaultCredential != wantDefaultCredential {
				t.Fatalf("defaultCredential=%v want=%v（必须识别出库里仍是默认密码，"+
					"否则 WebUI 告警需求零测试覆盖）", plan.defaultCredential, wantDefaultCredential)
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

// TestS04BootstrapFailureHasNoWrites 验证「启动被拒绝时一个字节都不写」。
//
// ⚠️ 2026-11-10 规则变更后本用例必须换失败诱因：原来用「库里是默认密码 123456」
// 制造拒绝，但新规则下它**不再拒绝启动**（改为启动 + WebUI 告警），
// 若继续用会变成「测一个不存在的拒绝」。
//
// 现在用「库里是 73 字节明文密码」制造真实拒绝：bcrypt 上限 72 字节，
// util.HashPassword 必然失败 → resolveAdminBootstrap 返回错误 → 启动拒绝。
// 这条无论规则怎么演进都会失败（物理限制），因此是稳定的诱因。
//
// 断言主体保持不变：拒绝后 DB 文件逐字节不变、不创建目录。
// 被测语义「失败无写入」因此完全没有被削弱。
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
				createMinimalUsersTable(t, d)
				// 73 字节明文：超出 bcrypt 的 72 字节上限，升级必然失败。
				d.Exec("INSERT INTO users VALUES(1,?,NULL)", strings.Repeat("x", 73))
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

// TestS04SQLiteURIIdentityAndNoWrites 验证两件事：
//  1. file:// URI 形式的 DSN 与裸路径**指向同一个文件**（身份等价）；
//  2. 启动被拒绝时该文件逐字节不变。
//
// ⚠️ 2026-11-10 规则变更后失败诱因同样必须换：原来用「库里是默认密码 123456」，
// 新规则下它不再拒绝启动。现在改用 73 字节明文（超出 bcrypt 72 字节上限），
// 这是物理限制导致的必然拒绝，不受产品规则演进影响。
func TestS04SQLiteURIIdentityAndNoWrites(t *testing.T) {
	t.Setenv("SQL_DSN", "")
	t.Setenv("TIKU_APP_MODE", "production")
	t.Setenv("TIKU_ADMIN_PASSWORD", "FixtureAdmin123!")
	dir := t.TempDir()
	path := filepath.Join(dir, "credential%2Ffixture.db")
	// 用与生产同一个构造函数（sqliteReadOnlyFileURI 去掉 ?mode=ro 前缀的形态）。
	// ⚠️ 不能直接写 `(&url.URL{Scheme:"file", Path:path}).String()` ——
	// 在 Windows 上它产出 `file://C:%5C...`（反斜杠转义 + 缺斜杠），
	// SQLite 报 invalid URI，这条测试会以「与代码无关」的方式假红。
	uri := sqliteFileURI(path).String()
	parsed, memory, err := sqliteFilesystemPath(uri)
	// 身份等价的判据是「指向同一个文件」，不是「字符串逐字相等」。
	// sqliteFilesystemPath 返回的是 URI 里的 Path 段：
	//   Linux:   /home/u/...         = 原始绝对路径
	//   Windows: /C:/Users/...       = 原始 `C:\Users\...` 加前导斜杠（URI 规范要求）
	// 所以统一用「去掉前导斜杠 + ToSlash」的形式比对，两边都规范化后再比。
	if err != nil || memory || !sameFilesystemPath(parsed, path) {
		t.Fatalf("URI identity: parsed=%q want=%q memory=%v err=%v (uri=%s)", parsed, path, memory, err, uri)
	}
	d, err := gorm.Open(sqlite.Open(uri), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	createMinimalUsersTable(t, d)
	// 73 字节明文：升级必然失败。
	if err = d.Exec("INSERT INTO users VALUES(1,?,NULL)", strings.Repeat("x", 73)).Error; err != nil {
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
		t.Fatal("URI over-limit credential accepted")
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
		// 2026-11-10 用户拍板：bcrypt 版的已知默认密码（明文 123456）同样
		// **不再拒绝启动**，改为启动 + WebUI 告警。校验点平移到 defaultCredential。
		{"default_bcrypt_accepted", "HASH", "123456", "FixtureAdmin123!", "production", false, true},
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

// sameFilesystemPath 判断两个路径字符串是否指向同一个文件。
//
// 为什么要它：`sqliteFilesystemPath` 解析 file: URI 时返回的是 URI 的 Path 段，
// 按规范它是**以 / 开头的绝对路径**。所以在 Windows 上：
//
//	原始路径      C:\Users\me\data\x.db
//	解析回来的     /C:/Users/me/data/x.db      ← 多了前导斜杠，反斜杠变正斜杠
//
// 直接 `parsed != path` 会在 Windows 上恒不相等 —— 这不是「身份不等价」，
// 而是同一路径的两种合法写法。Linux 上两者本来就相等，所以这个 bug
// 只在 Windows 暴露，且表现为「与本次改动无关」的假红。
func sameFilesystemPath(a, b string) bool {
	normalize := func(p string) string {
		s := strings.TrimPrefix(filepath.ToSlash(p), "/")
		return filepath.Clean(filepath.FromSlash(s))
	}
	return normalize(a) == normalize(b)
}
