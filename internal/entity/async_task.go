package entity

import "time"

// TableNameAsyncTask 是「异步任务」表的物理表名（121 §4.10「表名至今没定」，本轮定为
// async_tasks：中性、涵盖导入 / 导出 / 批量赠送 / 批量回滚四类，不绑定某一个业务）。
const TableNameAsyncTask = "async_tasks"

// 任务类型。
//
// import / export 由本模块自己注册执行器；batch_grant / batch_rollback 这里只定义常量，
// 执行器由批量赠送模块（u-grant）通过 service.RegisterAsyncTaskExecutor 注册 ——
// 四类共用同一张表与同一套生命周期（提交 → 排队 → 执行 → 过期清理 → 重启恢复）。
//
// 为什么后两类不自己实现：它们要读写钱包桶与账单，属于资金链路，
// 由资金模块独占实现才能保证「扣减 + 记账 + 幂等」在一个事务里。
const (
	AsyncTaskTypeImport        = "import"
	AsyncTaskTypeExport        = "export"
	AsyncTaskTypeBatchGrant    = "batch_grant"
	AsyncTaskTypeBatchRollback = "batch_rollback"
)

// 任务状态机：pending → running → succeeded / failed。
//
// 只有 running 是「进程内」状态：进程重启时幸存下来的 running 行是**假 running**
// （执行它的 goroutine 随进程一起没了），恢复逻辑必须把它们翻回 pending 重跑。
const (
	AsyncTaskStatusPending   = "pending"
	AsyncTaskStatusRunning   = "running"
	AsyncTaskStatusSucceeded = "succeeded"
	AsyncTaskStatusFailed    = "failed"
)

// AsyncTask 是一条异步任务（导入 / 导出 / 批量赠送 / 批量回滚）。
//
// 设计要点：
//
//  1. **任务本身可恢复**：执行只需要 task 行 + 落盘的源文件，不依赖提交它的那个 HTTP
//     请求的上下文。进程重启后 pending（以及崩溃遗留的 running）要被重新捡起来，
//     因此所有执行参数都序列化进 Params，而不是留在内存里。
//
//  2. **结果文件在磁盘上，不在库里的 BLOB**（107 Q12-2 选 A：服务器临时目录 +
//     定期清理）。库里只存路径与过期时间；文件过了 ExpiresAt 由清理任务删除，
//     下载接口对过期文件返回 410。
//
//  3. **计数四列分开**（Total / Processed / Succeeded / Skipped / Failed）：
//     导入是「部分成功」语义（107 Q13b 选 A），把「跳过」和「失败」糊成一个数字
//     会让运营无法判断该不该重跑。两个桶对不上（Succeeded+Skipped+Failed != Processed）
//     时以明细为准。
//
//  4. **ExpiresAt 在提交时也写一份**：正常任务在完成时把 ExpiresAt 改写为
//     「完成时刻 + 保留时长」；但如果任务卡死在 pending（源文件丢了 / 执行器没注册），
//     没有兜底过期时间就永远不会被清理。清理逻辑据此排除 running 状态，
//     避免把正在跑的任务连行带文件删掉。
//
// 脱敏：Masked 记录这条导出**当时**是否脱敏。它是审计字段 —— 事后无法从结果文件
// 反推「当时勾没勾脱敏」（不脱敏的文件里就是原文），所以必须落库。
type AsyncTask struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	// TaskID 是对外的任务标识（exp-xxxx / imp-xxxx / grn-xxxx / rbl-xxxx）。
	// 对外只暴露它，不暴露自增 ID：自增 ID 可枚举，会让「猜别人的导出文件」变得容易。
	TaskID string `gorm:"column:task_id;type:varchar(64);not null;default:''" json:"taskId"`
	// Type 取四类之一（见上方常量）。
	Type string `gorm:"column:type;type:varchar(32);not null;default:'';index" json:"type"`
	// Resource 是业务资源标识（questions / users / billing / gifts / logs / tickets /
	// blacklist / archived_users）。批量赠送类留空。
	Resource string `gorm:"column:resource;type:varchar(64);not null;default:''" json:"resource"`
	// Format 是文件格式（csv / json / xlsx / xls）。导出只产出 csv。
	Format string `gorm:"column:format;type:varchar(16);not null;default:''" json:"format"`
	// Status 取 pending / running / succeeded / failed。
	Status string `gorm:"column:status;type:varchar(24);not null;default:'pending';index" json:"status"`
	// Masked 表示这条导出是否脱敏（true = 打了码）。
	//
	// ⚠️ 标签必须是 `default:false`，**不能是 `default:true`**。实测（DryRun 抓 SQL +
	// 真库读回，两处都已验证）：
	//
	//	tag=default:true,  传 Masked=false → INSERT 里 masked 列在，但**值被替换成 true**
	//	                                      → 库里读回 true（显式取消脱敏被静默吞掉）
	//	tag=default:false, 传 Masked=false → INSERT 里 masked=false → 库里读回 false ✅
	//
	// 注意机制：**不是「把列从 INSERT 里省略」**，而是 GORM 用默认值替换了零值字段
	// （Driver 的 default value 覆盖逻辑）。所以「靠 DB 默认值也恰好是 false 兜住」
	// 这类巧合式正确并不存在 —— 传 false 时 INSERT 确实带 masked 且值为 false，
	// 不依赖 DB 默认值。
	//
	// 本仓已有两次同类实测事故（entity/blacklist.go 的 Enabled、review_rebate.go 的 Enabled），
	// 都是「带 default:true 后显式 false 被静默改成 true」。
	//
	// 这里是安全边界，方向必须可预测：默认值只在 Go 侧表达
	// （service.DefaultExportMasked / resolveExportMasked 是唯一来源），
	// 数据库层不承载「默认脱敏」这个语义。
	//
	// 回归钉子：TestAsyncTaskExportEndToEndUnmaskedKeepsRawValue（断言结果文件里是原文）
	// 与 TestAsyncTaskExportCreateDefaultsToMasked（断言落库 masked=false）。
	Masked bool `gorm:"column:masked;not null;default:false" json:"masked"`
	// DryRun 表示只校验不落库（导入预览）。
	DryRun bool `gorm:"column:dry_run;not null;default:false" json:"dryRun"`

	// ActorUserID / ActorUsername 是提交人（审计要能回答「谁导的」）。
	ActorUserID   int32  `gorm:"column:actor_user_id;type:int(11);not null;default:0" json:"actorUserId"`
	ActorUsername string `gorm:"column:actor_username;type:varchar(255);not null;default:''" json:"actorUsername"`

	// Params 是执行参数的 JSON 快照（resource / masked / 适用列等）。
	// 必须落库而不是留在内存：进程重启后要能凭它重跑。
	Params string `gorm:"column:params;type:text" json:"params"`

	// SourcePath 是导入任务的**源文件**路径（上传落盘后的位置）。
	// 与 FilePath（结果文件）分开：混用一个列会导致「清理结果文件」把还要重跑的源文件删掉。
	SourcePath string `gorm:"column:source_path;type:varchar(512);not null;default:''" json:"sourcePath"`
	// FilePath 是导出结果文件路径（导入任务的成功明细也可以写一份）。
	FilePath string `gorm:"column:file_path;type:varchar(512);not null;default:''" json:"filePath"`
	// ErrorReportPath 是失败行报告（errors.csv）路径。
	ErrorReportPath string `gorm:"column:error_report_path;type:varchar(512);not null;default:''" json:"errorReportPath"`

	// 计数四列：Total 是输入总数，Processed 是已处理数（进度用），
	// Succeeded / Skipped / Failed 是最终归类（部分成功语义）。
	Total     int `gorm:"column:total;not null;default:0" json:"total"`
	Processed int `gorm:"column:processed;not null;default:0" json:"processed"`
	Succeeded int `gorm:"column:succeeded;not null;default:0" json:"succeeded"`
	Skipped   int `gorm:"column:skipped;not null;default:0" json:"skipped"`
	Failed    int `gorm:"column:failed;not null;default:0" json:"failed"`

	// Message 是给用户看的一句话结论（如「导出完成，共 120 行」）。
	Message string `gorm:"column:message;type:varchar(512);not null;default:''" json:"message"`
	// ErrorMessage 是失败原因（截断到 1024 字符，绝不落完整堆栈：可能含库路径）。
	ErrorMessage string `gorm:"column:error_message;type:varchar(1024);not null;default:''" json:"errorMessage"`

	// ExpiresAt 是结果文件过期时刻（nil = 不需要清理，例如仍在排队）。
	ExpiresAt *time.Time `gorm:"column:expires_at;index" json:"expiresAt,omitempty"`
	// StartedAt / FinishedAt 是执行起止时刻（用于诊断「卡在 running 多久了」）。
	StartedAt  *time.Time `gorm:"column:started_at" json:"startedAt,omitempty"`
	FinishedAt *time.Time `gorm:"column:finished_at" json:"finishedAt,omitempty"`

	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (*AsyncTask) TableName() string { return TableNameAsyncTask }

// AsyncTaskIsTerminal 判断状态是否终态（终态才会设 FinishedAt 与结果过期时间）。
func AsyncTaskIsTerminal(status string) bool {
	switch status {
	case AsyncTaskStatusSucceeded, AsyncTaskStatusFailed:
		return true
	default:
		return false
	}
}
