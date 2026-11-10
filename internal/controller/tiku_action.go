package controller

// 题库操作的日志 Action 编号（logs.Module = "tiku"）。
//
// ⚠️ 编号一旦写入数据库就不得重排 —— 历史 logs 行会整体错位，
// 事后按 Action 还原「谁删了哪道题」就永久不可信。
//
// 号段占用情况（权威依据：task_doc/45-*.md §3.2.2 / 77-*.md §3.2.2）：
//   - 0/1/2/3 是题库模块历史编号（新增/修改/删除/查询），保持原值不动；
//   - 50-58 已被 account 模块占用（删除用户、注销、封禁、余额调整等），不得复用；
//   - 59/60 为本轮新增的题库归档/恢复编号。
const (
	// TikuActionCreate 新增题目（历史编号，勿改）。
	TikuActionCreate = 0
	// TikuActionUpdate 修改题目（历史编号，勿改）。
	TikuActionUpdate = 1
	// TikuActionDelete 历史物理删除编号。
	//
	// 题库改为软删除后，删除走 TikuActionArchive（59），本编号只为读历史日志保留。
	TikuActionDelete = 2
	// TikuActionArchive 归档（软删除）题库 —— Q77-1。
	TikuActionArchive = 59
	// TikuActionRestore 从归档恢复题库 —— Q77-1。
	TikuActionRestore = 60
)
