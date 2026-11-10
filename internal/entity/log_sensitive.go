package entity

import "time"

// LogSensitive 是 logs 表上「敏感词检测打标」四列的**非生成**扩展声明。
//
// 为什么不直接往 logs.gen.go 里加字段：
//   - logs.gen.go 由 gorm.io/gen 生成，文件头写着 DO NOT EDIT；
//   - 本项目已有的先例（CorrelationID）是手改生成文件，但那样每跑一次
//     生成器就会把新字段抹掉，改的人未必知道；
//   - 本项目约定「新列由显式 ensure*Schema 迁移补」（AutoMigrate 不参与建列），
//     生成结构体本身不参与建表。
//
// 因此把四列声明成同一张表的第二个结构体：列的物理定义由
// internal/registry/sensitive_word_migration.go 用显式 ALTER 补齐，
// 读写在业务层用这一组字段，互不干扰。
//
// 纪律：**不要**对这个结构体调用 AutoMigrate —— 它缺少 logs 表其余列，
// AutoMigrate 会试图改动已存在的表结构。
type LogSensitive struct {
	// LogID 映射 logs.id，仅作为 Update / Where 的主键锚点。
	LogID int32 `gorm:"column:id;primaryKey" json:"logId"`
	// Flag 任一检测对象命中敏感词即为 true。
	//
	// 注意语义：**只打标记，不过滤**。答案原文照常返回与落库，
	// 该标记只用于前端提示与 chaoxing 面板降级展示。
	Flag bool `gorm:"column:sensitive_flag;not null;default:false" json:"sensitiveFlag"`
	// Words 按检测对象（question / answer）分组的命中明细，JSON 文本。
	//
	// 形如 [{"scope":"question","status":"forbidden","forbidden":true,"words":["词1"]}]。
	// 分组是为了区分「题目命中」与「答案命中」，两者标记各自独立。
	Words string `gorm:"column:sensitive_words;type:longtext" json:"sensitiveWords"`
	// CheckedAt 检测完成时间（上海时区）。
	//
	// 为空表示这次请求从未检测过（开关关闭、或历史行）。
	CheckedAt *time.Time `gorm:"column:sensitive_checked_at" json:"sensitiveCheckedAt"`
	// CheckError 检测失败的**原因**，供排障；成功时为空。
	//
	// 检测失败绝不影响答题，也不会把 Flag 置为 true —— 宁可漏标，不可误标。
	CheckError string `gorm:"column:sensitive_check_error;type:varchar(255)" json:"sensitiveCheckError"`
}

// TableName 与 entity.Log 指向同一张物理表。
func (*LogSensitive) TableName() string {
	return TableNameLog
}
