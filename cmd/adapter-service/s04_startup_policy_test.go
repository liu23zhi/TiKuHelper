package main

import (
	"context"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// 语义以 task_doc/67-TRUSTED_PROXIES语义定义.md 为准：
//
//	空 或 none     → 信任所有（0.0.0.0/0 + ::/0），生产与开发一致
//	具体 IP/CIDR   → 规范化后的用户条目（本函数不前置局域网段）
//	无效内容       → 报错
func TestS04StrictTrustedProxyPolicy(t *testing.T) {
	trustAll := []string{trustAllProxiesIPv4, trustAllProxiesIPv6}
	for _, tc := range []struct {
		name, mode, value string
		want              []string
		bad               bool
	}{
		// ① 空 / none → 信任所有（不再拒绝启动）
		{"missing", "production", "", trustAll, false},
		{"whitespace", "production", "  ", trustAll, false},
		{"none", "production", " none ", trustAll, false},
		{"dev_missing", "development", "", trustAll, false},
		{"dev_none", "development", "none", trustAll, false},
		// ② 具体条目 → 规范化（局域网段由 loadTrustedProxies 补，这里不补）
		{"restricted", "production", "127.0.0.1, 10.1.0.0/16, 2001:db8::/32", []string{"127.0.0.1", "10.1.0.0/16", "2001:db8::/32"}, false},
		{"public_ip", "production", "8.8.8.8", []string{"8.8.8.8"}, false},
		{"mapped_ip", "production", "::ffff:127.0.0.1", []string{"127.0.0.1"}, false},
		// ③ 无效 → 报错
		{"invalid", "production", "garbage", nil, true},
		{"empty_entry", "production", "127.0.0.1,", nil, true},
		{"mixed_none", "production", "none,127.0.0.1", nil, true},
		{"all4", "production", "0.0.0.0/0", nil, true}, {"all6", "production", "::/0", nil, true},
		{"split_all4", "production", "0.0.0.0/1,128.0.0.0/1", nil, true},
		{"split_all6", "production", "::/1,8000::/1", nil, true},
		{"mapped_all4", "production", "::ffff:0.0.0.0/96", nil, true},
		{"mapped_split_all4", "production", "::ffff:0.0.0.0/97,::ffff:128.0.0.0/97", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveStartupTrustedProxies(tc.mode, tc.value)
			if (err != nil) != tc.bad || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%v err=%v want=%v bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

// 语义以 67 号文件为准。注意 none 现在等于「信任所有」，
// 因此带 none 的用例会读到转发头里的 203.0.113.7，而不是 TCP 对端。
func TestS04ProductionClientIPBoundary(t *testing.T) {
	for _, tc := range []struct{ policy, remote, want string }{
		{"none", "10.1.1.1:5678", "203.0.113.7"}, // none = 信任所有 → 读转发头
		{"", "10.1.1.1:5678", "203.0.113.7"},     // 空 = 信任所有 → 读转发头
		{"0.0.0.1", "10.1.1.1:5678", "10.1.1.1"}, // 不在可信列表 → 回退 TCP 对端
		{"127.0.0.1", "127.0.0.1:5678", "203.0.113.7"},
		{"127.0.0.1", "198.51.100.9:5678", "198.51.100.9"},
		{"2001:db8::/32", "[2001:db8:1::1]:5678", "203.0.113.7"},
		{"2001:db8::/32", "[2001:db9::1]:5678", "2001:db9::1"},
		{"::ffff:127.0.0.1", "[::ffff:127.0.0.1]:5678", "203.0.113.7"},
	} {
		proxies, err := resolveStartupTrustedProxies("production", tc.policy)
		if err != nil {
			t.Fatal(err)
		}
		engine := gin.New()
		if err = engine.SetTrustedProxies(proxies); err != nil {
			t.Fatal(err)
		}
		engine.GET("/", func(c *gin.Context) { c.String(200, c.ClientIP()) })
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.remote
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Body.String() != tc.want {
			t.Fatalf("policy=%q got=%s want=%s", tc.policy, w.Body.String(), tc.want)
		}
	}
}
func TestS04InvalidModePrecedesDB(t *testing.T) {
	if os.Getenv("S04_INVALID_MODE_CHILD") == "1" {
		main()
		return
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestS04InvalidModePrecedesDB$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "S04_INVALID_MODE_CHILD=1", "TIKU_APP_MODE=invalid", "TRUSTED_PROXIES=none", "TIKU_ADMIN_PASSWORD=FixtureAdmin123!", "SQL_DSN=")
	if err := cmd.Run(); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "tiku.db")); !os.IsNotExist(err) {
		t.Fatal("invalid mode reached DB init")
	}
}
