package controller

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/registry"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 存储状态端点的行为钉子（task_doc/108:297、109:474/486）。
//
// 三条要求必须同时成立，缺一条这个功能就不可用：
//  1. SQLite 时返回 driver=sqlite + 非空 warning；
//  2. **普通用户能读**（handler 内部不校验管理员 —— 挂载链那条在 api 侧另测）；
//  3. 响应字段是**白名单**：只回 driver / warning / production，
//     不回 DSN、数据库路径、账号。第 3 条用「字段集合恰好等于」来钉，
//     而不是查关键字 —— 黑名单式的「不含 .db」挡不住新增一个 dsn 字段。

// newStorageStatusSQLiteDB 开一个真实 SQLite 连接供 storageStatusDB 注入。
func newStorageStatusSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 路径故意起一个有辨识度的名字：下面要断言它绝不出现在响应里。
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "secret-storage-fixture.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// withStorageStatusDB 替换包级注入点并在清理时还原。
func withStorageStatusDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	original := storageStatusDB
	storageStatusDB = func() *gorm.DB { return db }
	t.Cleanup(func() { storageStatusDB = original })
}

// callStorageStatus 直接调 handler（不经过路由中间件），返回状态码与解析后的 body。
//
// 刻意不走 SetAPIRouter：那条链里有 GlobalAPIRateLimit，它直接解引用
// manager.GetManager()，单测无 manager 会 nil panic（本仓既有注释已记录）。
// 「路由是否挂了正确中间件」由 internal/api 的源码级用例负责。
func callStorageStatus(t *testing.T, actor any) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	if actor != nil {
		ctx.Set("user", actor)
	}
	ctx.Request = httptest.NewRequest("GET", "/api/system/storage-status", nil)
	StorageStatus(ctx)

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON 对象：%v，原始 body=%q", err, recorder.Body.String())
	}
	return recorder.Code, body
}

func TestStorageStatusReturnsSQLiteDriverAndWarning(t *testing.T) {
	withStorageStatusDB(t, newStorageStatusSQLiteDB(t))

	code, body := callStorageStatus(t, nil)
	if code != 200 {
		t.Fatalf("status = %d, want 200（不得因为用 SQLite 就报错）", code)
	}
	if body["driver"] != "sqlite" {
		t.Fatalf("driver = %v, want sqlite", body["driver"])
	}
	warning, ok := body["warning"].(string)
	if !ok || strings.TrimSpace(warning) == "" {
		t.Fatalf("warning 必须是非空字符串，实际 %#v", body["warning"])
	}
	// 与 registry 导出的常量同源（日志也用这一份）。
	if want := registry.StorageStatusForDB(newStorageStatusSQLiteDB(t)).Warning; warning != want {
		t.Fatalf("端点文案必须来自 registry，实际 %q != %q", warning, want)
	}
	// production 字段必须存在（布尔，不是 null）。
	if _, ok := body["production"].(bool); !ok {
		t.Fatalf("production 必须是布尔值，实际 %#v", body["production"])
	}
}

func TestStorageStatusFieldSetIsWhitelisted(t *testing.T) {
	withStorageStatusDB(t, newStorageStatusSQLiteDB(t))
	_, body := callStorageStatus(t, nil)

	// 白名单式断言：键集合必须**恰好**是这三项。
	// 用「多了就红」而不是「查关键字」，新增 dsn/path 字段时必然被抓住。
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"driver", "production", "warning"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("响应字段集合 = %v, want %v（多一个字段都算泄露面）", keys, want)
	}

	// 即使字段集合对了，值也可能带出路径/DSN —— 逐个值再扫一遍。
	raw := strings.ToLower(strings.Join(keys, " ") + " " +
		strings.ToLower(body["driver"].(string)) + " " + strings.ToLower(body["warning"].(string)))
	for _, forbidden := range []string{".db", "secret-storage-fixture", "file:", "sqlite:", "dsn", "password", "tiku.db"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("响应不得包含 %q（数据库路径/DSN 属于凭据面），实际：%s", forbidden, raw)
		}
	}
}

func TestStorageStatusDegradesWhenManagerUnavailable(t *testing.T) {
	// manager 未初始化（单测 / 启动失败）→ 200 + mysql + 空 warning。
	// 不返回 5xx：一个查不到存储状态的接口把前端弄成错误态，代价远大于收益。
	withStorageStatusDB(t, nil)

	code, body := callStorageStatus(t, nil)
	if code != 200 {
		t.Fatalf("status = %d, want 200（降级而非报错）", code)
	}
	if body["driver"] != "mysql" {
		t.Fatalf("driver = %v, want mysql（判定不了时不误报 sqlite）", body["driver"])
	}
	if body["warning"] != "" {
		t.Fatalf("warning = %v, want 空串（判定不了就不警告）", body["warning"])
	}
}

// TestStorageStatusDoesNotRequireAdmin 钉住「普通用户也能读」这条**handler 侧**保证。
//
// handlers 在测试期间本就不会自己校验角色，所以这条本质是「回归保护」：
// 若将来有人往 StorageStatus 里加 requireAdmin(c)，它立刻变红。
func TestStorageStatusDoesNotRequireAdmin(t *testing.T) {
	withStorageStatusDB(t, newStorageStatusSQLiteDB(t))

	plainUser := map[string]any{"id": 9001, "username": "storage-status-plain-user", "role": "user"}
	code, body := callStorageStatus(t, plainUser)
	if code != 200 {
		t.Fatalf("普通用户读存储状态得到 %d，want 200 —— 挂管理员校验会让警告只对管理员可见", code)
	}
	if body["driver"] != "sqlite" {
		t.Fatalf("普通用户拿到的 driver = %v, want sqlite", body["driver"])
	}
}
