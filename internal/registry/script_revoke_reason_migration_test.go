package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件钉住 script_run_sessions.revoked_reason 的迁移行为。
//
// 迁移的判据与业务逻辑不同：业务要证明「值写对了」，迁移要证明
// **对存量数据零破坏**且**重复执行幂等**。两条缺一不可：
//
//   - 存量行：加列后必须仍是空串（不能猜原因），且原有 revoked_at 等列不变。
//     如果迁移顺手回填一个猜测值，通知区就会对历史会话说谎。
//   - 幂等：启动每次都跑迁移（本仓是列检测型），重复执行必须不报错、
//     不丢数据。这条在「每次启动都迁移」的设计下是硬要求。

// newRevokeReasonMigrationDB 造一个只有 script_run_sessions 且**没有** revoked_reason
// 的旧库，模拟升级前的现状。
func newRevokeReasonMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/revoke.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// 刻意手写旧表结构（不含 revoked_reason），而不是用 AutoMigrate ——
	// AutoMigrate 会按当前实体建出带新列的表，那样这条用例就测不到「加列」。
	if err := db.Exec(`CREATE TABLE script_run_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		script_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		access_token_id INTEGER NOT NULL,
		token_hash CHAR(64) NOT NULL,
		identity_hash CHAR(64) NOT NULL,
		lease_slot INTEGER,
		source_zone_hash CHAR(64) NOT NULL DEFAULT '',
		last_heartbeat_at DATETIME,
		activated_at DATETIME NOT NULL,
		expires_at DATETIME NOT NULL,
		revoked_at DATETIME,
		created_at DATETIME
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

// TestRevokeReasonMigrationAddsColumnAndKeepsExistingRows 是主判据。
func TestRevokeReasonMigrationAddsColumnAndKeepsExistingRows(t *testing.T) {
	db := newRevokeReasonMigrationDB(t)

	// 预置两条历史行：一条从未撤销，一条在本列加入前就被撤销了。
	if err := db.Exec(`INSERT INTO script_run_sessions
		(script_id, user_id, access_token_id, token_hash, identity_hash, activated_at, expires_at, revoked_at)
		VALUES (1, 1, 1, 'aaa', 'bbb', '2026-11-01 10:00:00', '2026-11-01 12:30:00', NULL),
		       (1, 1, 1, 'ccc', 'ddd', '2026-11-02 10:00:00', '2026-11-02 12:30:00', '2026-11-02 11:00:00')`).Error; err != nil {
		t.Fatal(err)
	}

	// 前置断言：迁移前这一列确实不存在，否则后面「加列成功」的断言是空的。
	if db.Migrator().HasColumn(&entity.ScriptRunSession{}, "RevokedReason") {
		t.Fatal("前置不成立：旧库本来就有 revoked_reason 列，本用例测不到加列")
	}

	if err := ensureScriptRunSessionRevokeReasonSchema(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if !db.Migrator().HasColumn(&entity.ScriptRunSession{}, "RevokedReason") {
		t.Fatal("迁移后 revoked_reason 列仍不存在")
	}

	// 存量行必须落空串 —— 迁移**不得**猜测原因。
	var rows []struct {
		ID            int64
		RevokedReason string
		RevokedAt     *string
	}
	if err := db.Table("script_run_sessions").Select("id, revoked_reason, revoked_at").Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("存量行数必须保持 2，实际 %d", len(rows))
	}
	for _, row := range rows {
		if row.RevokedReason != "" {
			t.Fatalf("存量行 %d 的 revoked_reason 必须为空串（迁移不猜原因），实际 %q", row.ID, row.RevokedReason)
		}
	}
	// 已撤销那行的 revoked_at 不能被迁移改掉。
	if rows[1].RevokedAt == nil {
		t.Fatal("迁移不得改动已有的 revoked_at")
	}
}

// TestRevokeReasonMigrationIsIdempotent 钉住「重复执行不报错、不丢数据」。
//
// 本仓的列检测型迁移每次启动都会跑一遍，所以这不是可选加固。
func TestRevokeReasonMigrationIsIdempotent(t *testing.T) {
	db := newRevokeReasonMigrationDB(t)
	if err := db.Exec(`INSERT INTO script_run_sessions
		(script_id, user_id, access_token_id, token_hash, identity_hash, activated_at, expires_at)
		VALUES (1, 1, 1, 'aaa', 'bbb', '2026-11-01 10:00:00', '2026-11-01 12:30:00')`).Error; err != nil {
		t.Fatal(err)
	}

	for round := 1; round <= 3; round++ {
		if err := ensureScriptRunSessionRevokeReasonSchema(db); err != nil {
			t.Fatalf("第 %d 次执行失败（迁移必须幂等）: %v", round, err)
		}
	}

	var count int64
	if err := db.Table("script_run_sessions").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("重复迁移不得增删行，实际 %d 行", count)
	}

	// 幂等之后仍能正常写入原因 —— 否则「列在但不可写」同样是坏的。
	if err := db.Model(&entity.ScriptRunSession{}).Where("id = ?", 1).
		Update("revoked_reason", entity.ScriptRevokeReasonHeartbeatTimeout).Error; err != nil {
		t.Fatalf("写原因失败: %v", err)
	}
	var reason string
	if err := db.Table("script_run_sessions").Select("revoked_reason").Where("id = ?", 1).Scan(&reason).Error; err != nil {
		t.Fatal(err)
	}
	if reason != "heartbeat_timeout" {
		t.Fatalf("写入后读回的原因必须是立字面量 %q，实际 %q", "heartbeat_timeout", reason)
	}
}

// TestRevokeReasonMigrationToleratesMissingTable 钉住空库/未建表时不报错。
//
// 启动顺序里迁移可能早于 AutoMigrate 建表（或跑在只有部分表的旧库上），
// 这时必须是「无事可做」而不是「启动失败」。
func TestRevokeReasonMigrationToleratesMissingTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/empty.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := ensureScriptRunSessionRevokeReasonSchema(db); err != nil {
		t.Fatalf("表不存在时必须静默返回，实际报错: %v", err)
	}
	if err := ensureScriptRunSessionRevokeReasonSchema(nil); err != nil {
		t.Fatalf("nil 句柄必须静默返回，实际报错: %v", err)
	}
}
