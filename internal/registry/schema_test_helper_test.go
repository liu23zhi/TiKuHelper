package registry

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 老库 users 表的建表 helper（schema 迁移测试专用）。
//
// ## 为什么需要它
//
// 有一批测试要验证「ensure*Schema 能给老库补列」：它们必须先手写一张
// **不带后来新增列**的 users 表，再跑迁移，最后断言列被补上。
// 手写 DDL 的问题是它会随 entity.User 的演进静默失配 ——
// 只要实体多一个字段，所有手写点就一起挂。
//
// ## ⚠️ 必须包含 entity.User 声明的全部 **GORM 机制字段**
//
// 当前是 `deleted_at`（gorm.DeletedAt）。GORM 的软删除是**模型级**行为：
// 字段一存在，`db.Where(...).First(&entity.User{})` 就会被自动追加
// `WHERE deleted_at IS NULL`。如果老库 DDL 里没有这一列，查询直接报
//
//	SQL logic error: no such column: users.deleted_at
//
// 而且**报在查询上、不报在建表上**，栈里看不到 createLegacyUsersTable，
// 排查时会误以为是迁移逻辑坏了（实测踩过一次）。
//
// 注意区分两类列：
//   - **业务列**（如 invited_by_user_id / gift_balance）：本 helper **故意不带**，
//     那是被测迁移的职责，带上就测不出「补列」了；
//   - **机制列**（deleted_at）：本 helper **必须带**，它不是任何迁移的产物，
//     而是 ORM 强加给每条查询的运行时条件。
//
// 新增 GORM 机制字段（例如未来加 gorm.DeletedAt 之外的软删除/多租户字段）时，
// 这里必须同步补，否则所有 schema 迁移测试会一起变红。
func createLegacyUsersTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db == nil {
		t.Fatal("createLegacyUsersTable: db 不能为空")
	}
	// deleted_at 放在最后并允许 NULL：老库里的行都是「未删除」，
	// 与 entity.User.DeletedAt 为 gorm.DeletedAt 零值的语义一致。
	ddl := `CREATE TABLE ` + entity.TableNameUser + ` (
		id integer PRIMARY KEY AUTOINCREMENT,
		username varchar(255),
		password varchar(255),
		role varchar(32) NOT NULL DEFAULT 'user',
		status varchar(32) NOT NULL DEFAULT 'enabled',
		balance decimal(18,6) NOT NULL DEFAULT 0,
		register_source varchar(32) NOT NULL DEFAULT 'legacy',
		created_at datetime,
		deleted_at datetime
	)`
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create legacy users: %v", err)
	}
}

// createMinimalUsersTable 建一张**只有主键与密码**的 users 表。
//
// 用于「不在乎业务列、只要能让 resolveAdminBootstrap / applyAdminBootstrap
// 读 password」的启动策略测试。与 createLegacyUsersTable 的差别是它刻意极简
// （没有 username/role/balance 等），但同样**必须带机制列 deleted_at** ——
// 理由见 createLegacyUsersTable 的注释。
//
// 这类测试后续会用 GORM 查询 entity.User，因此机制列不可省。
func createMinimalUsersTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if db == nil {
		t.Fatal("createMinimalUsersTable: db 不能为空")
	}
	ddl := "CREATE TABLE " + entity.TableNameUser +
		"(id integer primary key, password text, deleted_at datetime)"
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create minimal users: %v", err)
	}
}
