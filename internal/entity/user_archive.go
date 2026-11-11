package entity

import (
	"time"

	"gorm.io/gorm"
)

// UserArchive 是 users 表上「归档（软删除）」四列的**非生成**扩展声明。
// 依据 task_doc/76 §3.2.2 与 task_doc/77。
//
// # 为什么不直接往 users.gen.go 里加字段
//
// 与 LogSensitive（internal/entity/log_sensitive.go）同一条纪律：
//   - users.gen.go 由 gorm.io/gen 生成，文件头写着 DO NOT EDIT；
//   - 手改生成文件后，每跑一次生成器就会把新字段抹掉，改的人未必知道；
//   - 本项目约定「新列由显式 ensure*Schema 迁移补」（AutoMigrate 不参与建列）。
//
// 因此把四列声明成同一张表的第二个结构体：列的物理定义由
// internal/registry/user_archive_migration.go 用显式 ALTER 补齐，
// 读写在业务层用这一组字段，互不干扰。
//
// # 为什么用 gorm.DeletedAt 而不是普通 *time.Time
//
// GORM 官方软删除类型会让 `dao.User` 的**所有既有查询自动排除**已归档用户 ——
// 不需要在每一处查询里手补 `WHERE deleted_at IS NULL`。
// 自造列的话，漏改任何一处，已归档用户就会重新出现在用户列表里。
//
// ⚠️ 代价：用 `Unscoped()` 的地方仍会看到已归档用户。
// 归档相关代码要显式用 Unscoped，其余地方不要。
//
// ⚠️ 纪律：**不要**对这个结构体调用 AutoMigrate —— 它缺少 users 表其余列，
// AutoMigrate 会试图改动已存在的表结构。
type UserArchive struct {
	// UserID 映射 users.id，仅作为 Update / Where 的主键锚点。
	UserID int32 `gorm:"column:id;primaryKey" json:"userId"`
	// DeletedAt 非空 = 已归档。
	//
	// 用 gorm.DeletedAt（内含 sql.NullTime）：它的零值是「未删除」，
	// 而普通 *time.Time 的 nil 也能表达，但不会被 GORM 的软删除机制识别。
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
	// ArchivedReason 是归档原因，取值见下方常量。
	//
	// 为什么需要这一列而不是靠「谁调用的」推断：用户端与管理端是两条
	// 完全不同的链路，事后审计必须能区分「管理员删的」与「用户自己注销的」——
	// 两者的合规义务不同。
	ArchivedReason string `gorm:"column:archived_reason;type:varchar(32);not null;default:''" json:"archivedReason"`
	// ArchivedAt 是归档时点（与 DeletedAt 同值，但独立可读：
	// DeletedAt 是 GORM 机制字段，业务展示用这个）。
	ArchivedAt *time.Time `gorm:"column:archived_at" json:"archivedAt,omitempty"`
	// ArchivedByUserID 是操作者。
	//
	// 用户自助注销时是**本人 ID**（与 task_doc/77 §5 第 5 条一致）；
	// 管理员删除时是管理员 ID。
	ArchivedByUserID int32 `gorm:"column:archived_by_user_id;type:int(11);not null;default:0" json:"archivedByUserId"`
}

// TableName 与 entity.User 指向同一张物理表。
func (*UserArchive) TableName() string {
	return TableNameUser
}

// 归档原因取值（task_doc/76 §3.2.2）。
//
// 这两个值直接决定 user_binding_records 的 release_source：
//
//	admin_delete  → release_source = admin_delete
//	user_closure  → release_source = account_close
//
// 不要合并成一个值 —— 两者的合规义务不同，事后审计要能区分。
const (
	UserArchivedReasonAdminDelete = "admin_delete"
	UserArchivedReasonUserClosure = "user_closure"
)
