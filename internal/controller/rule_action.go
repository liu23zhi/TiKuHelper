package controller

// 规则类删除的 logs.action 编号登记。
//
// **为什么单独一个编号**：`logs.action = 2`（logActionDelete）是「普通删除」，
// 而规则类删除多了一层「删除前完整快照」的语义（45-*.md §11.4 / Q77-2）。
// 给它们一个独立编号，运维在 LogList 页按 action 筛「哪些删除带快照」时
// 不需要靠 module 猜，也不会把题库/账号的删除混进来。
//
// **占用表（logs.action 是 tinyint，值域 0-127，切勿复用已有编号）**：
//
//	0        创建（logActionCreate）
//	1        更新（logActionUpdate）
//	2        删除（logActionDelete）
//	3        搜索（logActionSearch）
//	10       登录（logActionLogin）
//	11       AI 兜底（logActionAIFallback）
//	12       用户注册成功
//	13       用户退出登录
//	14       管理员重置用户密码
//	15       修改手机号（用户自助 / 管理员）
//	16       修改资料（用户自助 / 管理员）
//	4-9      各模块内部的写操作编号
//	         例：blacklist 1=新增 2=启停 3=新增审计 4=删除审计
//	             review_rebate 1=提交 2=改活动 3=新建规则 4=复核
//	50-58     账号注销与保留区（user.go / archived_user.go / closure.go）
//	59-60     题库归档与恢复（tiku，r11-tikuarch 实施）
//	61        规则类删除快照（blacklist / review_rule / payment_method，本文件）
//	62+       空闲
//
// ⚠️ 新增编号只能从 62 往后取，不要动 61 及以前。
const RuleActionDelete int32 = 61
