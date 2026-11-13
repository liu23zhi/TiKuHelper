package registry

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ════════════════════════════════════════════════════════════════════
// R9 失败登录流水表的迁移与索引
//
// ⚠️ **这条变异的如实说明**：去掉 `(client_ip, created_at)` 复合索引**没有行为判据** ——
// 索引只影响查询速度，不影响 R9 的判定结果（`TestRiskCredentialStuffing*` 全绿）。
// 所以这里只验证「索引确实被建出来了」+「username 上没有任何索引」，
// **不假装验证了「查询走了索引」**（那需要 EXPLAIN QUERY PLAN，且 SQLite/MySQL 输出
// 不可通约，本仓无先例）。
// ════════════════════════════════════════════════════════════════════

func newLoginFailureMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:login-failure-migration-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	return db
}

// 判据①：迁移建出表 + `(client_ip, created_at)` 复合索引。
func TestEnsureLoginFailureEventSchemaCreatesTableAndIndex(t *testing.T) {
	db := newLoginFailureMigrationTestDB(t)
	if err := EnsureLoginFailureEventSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameLoginFailureEvent) {
		t.Fatalf("迁移后表 %s 应存在", entity.TableNameLoginFailureEvent)
	}
	if !db.Migrator().HasIndex(entity.TableNameLoginFailureEvent, "idx_login_failure_ip_time") {
		t.Fatalf("复合索引 idx_login_failure_ip_time 必须被创建 —— " +
			"R9 的「按 IP + 时间窗」查询依赖它（没有它只是变慢，不影响正确性）")
	}
}

// 判据②（**关键**）：`username` 上**不能有任何索引**。
//
// 理由：`username` 是攻击者完全控制的任意字符串（撞库时每请求不同），
// 建索引会在长度维度被撑爆（varchar(255)）且选择性极差 —— 收益为零，只有维护成本。
//
// 变异方向：给 `Username` 字段加 `index` gorm tag → 本用例必红。
func TestLoginFailureEventHasNoUsernameIndex(t *testing.T) {
	db := newLoginFailureMigrationTestDB(t)
	if err := EnsureLoginFailureEventSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	type indexRow struct {
		Name string
		SQL  string
	}
	var rows []indexRow
	if err := db.Raw(
		"SELECT name, COALESCE(sql,'') AS sql FROM sqlite_master "+
			"WHERE type='index' AND tbl_name=?", entity.TableNameLoginFailureEvent).
		Scan(&rows).Error; err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	// 前置自检：确实查到了索引（否则下面的循环是空转，用例恒绿）。
	if len(rows) == 0 {
		t.Fatalf("前置条件失效：一个索引都没查到，无法验证 username 无索引")
	}
	for _, row := range rows {
		sql := strings.ToLower(row.SQL)
		if sql == "" {
			continue // 自动索引（sqlite_autoindex_*）没有 SQL 文本
		}
		// 只关心「索引列里含 username」的情形。
		if strings.Contains(sql, "(username") || strings.Contains(sql, ", username") ||
			strings.Contains(sql, "( username") || strings.Contains(sql, ",username") {
			t.Fatalf("username 上不该有任何索引（攻击者控制的任意字符串，"+
				"会撑爆且选择性极差），但发现：%s", row.SQL)
		}
	}
}

// 判据③：迁移幂等（重复调用不报错、不重复建索引）。
func TestEnsureLoginFailureEventSchemaIsIdempotent(t *testing.T) {
	db := newLoginFailureMigrationTestDB(t)
	for i := 0; i < 3; i++ {
		if err := EnsureLoginFailureEventSchema(db); err != nil {
			t.Fatalf("第 %d 次调用失败: %v", i+1, err)
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameLoginFailureEvent, "idx_login_failure_ip_time") {
		t.Fatalf("幂等调用后索引应仍存在")
	}
}

// 判据④：`user_id = 0`（账号不存在）能被正常写入并读回。
//
// 这是 D3 的数据层前提：若 schema 上把 user_id 约束成「必须 > 0」或有外键引用
// users(id)，「账号不存在」的失败就写不进来 —— 而那是撞库的典型形态。
//
// ⚠️ 同时钉住一条容易漏的：本表**没有** `autoCreateTime` tag，
// 所以 CreatedAt 必须由写入方显式给，不填会落零值（那行对 R9 的时间窗无意义，
// service 层会跳过它 —— 见 TestRecordLoginFailureEventSkipsInvalidRows）。
func TestLoginFailureEventAcceptsZeroUserID(t *testing.T) {
	db := newLoginFailureMigrationTestDB(t)
	if err := EnsureLoginFailureEventSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	createdAt := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	event := entity.LoginFailureEvent{
		CreatedAt: createdAt,
		ClientIP:  "203.0.113.7",
		UserID:    0,
		Username:  "ghost",
		Reason:    entity.LoginFailureReasonUserNotFound,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("user_id=0 必须能写入（账号不存在是撞库的典型形态），实际报错: %v", err)
	}
	var loaded entity.LoginFailureEvent
	if err := db.Where("client_ip = ?", "203.0.113.7").First(&loaded).Error; err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if loaded.UserID != 0 {
		t.Fatalf("user_id 应为 0，实际 %d", loaded.UserID)
	}
	if loaded.Username != "ghost" {
		t.Fatalf("username 应为 ghost，实际 %q", loaded.Username)
	}
}
