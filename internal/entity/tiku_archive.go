package entity

import "time"

// 题库归档相关的常量。
const (
	// TikuArchiveSchemaVersionKey 是「题库归档列已就绪」的一次性 system_settings 标记。
	//
	// 用标记而不是每次都 ALTER：显式 ALTER 在 MySQL 上会加表锁，
	// 每次启动都跑一遍是没必要的风险面（HasColumn 幂等只是保险，不是常规路径）。
	TikuArchiveSchemaVersionKey = "tiku_archive_schema_v1"

	// TikuArchiveDeletedAtIndex 是软删除条件用的索引名。
	//
	// ⚠️ 必须存在：gorm.DeletedAt 会给**每一条** dao.Tiku 查询都附上
	// `deleted_at IS NULL`，题库表通常几十万行，没索引就是全表扫描。
	TikuArchiveDeletedAtIndex = "idx_tikus_deleted_at"

	// TikuArchivedReasonAdminDelete 是管理员手工删除（= 归档）时的原因分类。
	//
	// archived_reason 同时还承载管理员填写的自由文本原因（Q77-6 要求强制填），
	// 这个常量只标记「归档来源是管理员删除」，与自由文本原因分开存放
	// （自由文本写 archived_reason 之外的 logs.Description 与旧值快照）。
	TikuArchivedReasonAdminDelete = "admin_delete"
)

// TikuArchive 是题库归档元数据的扩展视图，落到同一张 tiku 表。
//
// 与 entity.Tiku 的分工（Q77-1，复用 users 的成熟做法）：
//   - entity.Tiku    —— GORM 模型，带 gorm.DeletedAt，所有业务查询默认排除归档行；
//   - entity.TikuArchive —— 只读扩展视图，用于读取同名表上的归档四列
//     （deleted_at / archived_reason / archived_at / archived_by_user_id），
//     供归档管理页与日志快照使用。
//
// ⚠️ 本结构体**不带** gorm.DeletedAt（用一个普通指针字段），
// 因此对它的查询不会自动附加 `deleted_at IS NULL` —— 这正是「能读到归档行」的原因。
// 也正因如此，它只用于读；写归档/恢复请走 dao.Tiku / dao.Tiku.Unscoped()。
type TikuArchive struct {
	// TikuID 对应 tiku.id，不是本表自己的主键（同一张表，没有第二套主键）。
	TikuID int32 `gorm:"column:id;primaryKey" json:"tikuId"`
	// DeletedAt 是归档时间；非 NULL 即「已归档」。
	DeletedAt *time.Time `gorm:"column:deleted_at" json:"deletedAt"`
	// ArchivedReason 是归档来源分类（如 TikuArchivedReasonAdminDelete）。
	ArchivedReason *string `gorm:"column:archived_reason;type:varchar(32)" json:"archivedReason"`
	// ArchivedAt 与 DeletedAt 同时写入，单独存一列是为了语义清晰与可索引。
	ArchivedAt *time.Time `gorm:"column:archived_at" json:"archivedAt"`
	// ArchivedByUserID 是执行归档的管理员 user_id（0/NULL 表示未知）。
	ArchivedByUserID *int32 `gorm:"column:archived_by_user_id" json:"archivedByUserId"`
}

// TableName 复用题库表：归档元数据是 tiku 表上的列，不是独立表。
//
// ⚠️ 表名常量取自 TableNameTiku（值为 "tiku"），不是拼出来的 "tikus"。
func (*TikuArchive) TableName() string {
	return TableNameTiku
}
