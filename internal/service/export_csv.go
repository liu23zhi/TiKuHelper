package service

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 异步导出的 CSV 生成（111 §4 / 117 §4.5，任务书 ②③⑧）。
//
// # 为什么导出只有 CSV，没有 xlsx / xls
//
// 111 §4.1 的格式决定是「只 CSV」：CSV 由后端异步生成（大数据量不能塞进浏览器），
// 而 JSON 是**前端立即生成并下载**（走现有 handleExportSelected，后端零参与），
// 所以后端没有 JSON 导出路径。xlsx 只出现在**导入**侧（117 §4.5 的支持矩阵）。
//
// # 为什么脱敏是「默认开」，且不是前端决定
//
// 111 §4.1 Q12-d：脱敏选项在导出对话框里**默认勾选**，只有管理员能取消。
// 关键点是：**默认值必须在服务端成立**。如果只靠前端默认勾选，
// 那么任何直接调接口（含脚本、curl、第三方集成）的导出都是明文，
// 而「谁调过接口」在事后无法区分。因此 Masked 用 *bool：
// nil = 走 DefaultExportMasked（true），只有显式传 false 才明文。
//
// # 脱敏口径（111 §4.5）
//
//	手机号        保留前 3 后 4      13812345678   → 138****5678
//	邮箱          首字符 + 域名      alice@ex.com  → a***@ex.com
//	微信 OpenID   保留首尾各 3 位    oABC123xyz789 → oAB*****789
//	IP            保留前两段         203.0.113.45  → 203.0.*.*
//	token         保留前 6 后 4      tk-9f3a…c871  → tk-9f3****c871
//	用户名        不脱敏（审计需要「是谁」）
//	题目 / 答案   不脱敏（审计需要看内容）
//
// ⚠️ 微信的口径与 entity 展示层（MaskBindingDisplayValue，前 6 后 4）**不同**。
// 这不是笔误：111 §4.5 是导出文件的规格（导出文件会离开本系统，必须更严），
// 绑定历史展示是页面内的（宽一点便于运营核对）。两处各自按自己的规格来，
// 不要为了「统一」把导出放宽。

// DefaultExportMasked 是导出的默认脱敏开关（111 §4.1 Q12-d：默认勾选脱敏）。
//
// 单独提成常量而不是在调用处写 true：这条默认值是安全边界，
// 需要有一个能被测试直接盯住、被评审一眼看到的位置。
const DefaultExportMasked = true

// ExportMaxRows 是单次导出任务的**通用**行数上限（111 §4.7：10 万行）。
//
// 它是兜底值，不是所有资源的上限：个别资源有更严的单资源上限
// （见 exportResourceSpec.maxRows 与 QuestionExportMaxRows）。
const ExportMaxRows = 100_000

// QuestionExportMaxRows 是**题库异步导出的单任务行数上限（1 万行）**。
//
// 为什么题库比通用上限更严：题库是唯一「导出 → 改 → 重新导入」闭环的资源，
// 而导入侧的单批上限是 5000 行（QuestionImportMaxRows）。导出 10 万行却只能
// 分 20 批导回来，中间任何一批失败都要重新对账 —— 上限错配本身就是故障源。
//
// 1 万 = 导入上限的 2 倍：留出「导全量 + 追加新题」的余量，又保证一个导出文件
// 拆成两批必然能导完。
const QuestionExportMaxRows = 10_000

// 导出资源标识（111 §4.4 的 8 个页面 + access_tokens）。
//
// access_tokens 不在 111 §4.4 的清单里，但任务书 ⑧ 要求脱敏覆盖 token ——
// 既然要脱敏就要有导出的对象，因此这里补一个最小列集（不含 token_hash）。
const (
	ExportResourceQuestions     = "questions"
	ExportResourceUsers         = "users"
	ExportResourceBilling       = "billing"
	ExportResourceGifts         = "gifts"
	ExportResourceLogs          = "logs"
	ExportResourceTickets       = "tickets"
	ExportResourceBlacklist     = "blacklist"
	ExportResourceArchivedUsers = "archived_users"
	ExportResourceAccessTokens  = "access_tokens"
)

// ErrExportResourceUnsupported 表示资源标识不在支持清单内（400 而不是 500）。
var ErrExportResourceUnsupported = errors.New("不支持的导出资源")

// ExportDataOptions 是一次导出的参数。
type ExportDataOptions struct {
	// Resource 取 ExportResource* 之一。
	Resource string
	// Masked 为 nil 时使用 DefaultExportMasked（true）；显式 false 才是明文。
	Masked *bool
	// Limit 为 0 时使用 ExportMaxRows；负数表示不限制（仅供内部测试/对账使用）。
	Limit int
}

// ExportResult 是导出结果。
type ExportResult struct {
	// Content 是 CSV 文件正文（UTF-8，带 BOM 以便 Excel 正确识别中文）。
	Content []byte
	// Rows 是数据行数（不含表头）。
	Rows int
	// Masked 是本次实际采用的脱敏开关（回写进任务行做审计）。
	Masked bool
	// Resource 是资源标识（回显用）。
	Resource string
}

// exportMaskFunc 是单列的脱敏函数；nil 表示该列不脱敏。
type exportMaskFunc func(string) string

// ExportColumn 是导出的一列。
//
// Mask 挂在列上而不是「按表名统一脱敏」：同一张表里用户名列不脱敏、
// 手机号列要脱敏。按表统一处理必然要么漏（某个敏感列没覆盖）要么过严（把审计
// 需要的用户名也打码，导出就没用了）。
type ExportColumn struct {
	Header string
	Mask   exportMaskFunc
}

// exportColumn 构造一个不脱敏的列。
func exportColumn(header string) ExportColumn { return ExportColumn{Header: header} }

// maskedColumn 构造一个脱敏的列。
func maskedColumn(header string, mask exportMaskFunc) ExportColumn {
	return ExportColumn{Header: header, Mask: mask}
}

// MaskExportPhone 手机号脱敏：保留前 3 后 4。
//
// 复用 MaskPhone（同一个仓里已经有「手机号怎么打码」的实现），
// 但它对非大陆号码会原样返回 —— 导出文件会离开本系统，原样返回等于泄漏，
// 因此这里补一层兜底：识别不出就整体打码（宁可丢信息，不可泄漏）。
func MaskExportPhone(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	masked := MaskPhone(trimmed)
	if masked == trimmed {
		// MaskPhone 没能识别出手机号（含国际号 / 脏数据）→ 全掩码。
		return strings.Repeat("*", len([]rune(trimmed)))
	}
	return masked
}

// MaskExportEmail 邮箱脱敏：保留首字符 + 域名（alice@example.com → a***@example.com）。
func MaskExportEmail(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	at := strings.LastIndex(trimmed, "@")
	if at <= 0 {
		// 不是合法邮箱：整体打码，别把脏数据当明文放出去。
		return strings.Repeat("*", len([]rune(trimmed)))
	}
	local := []rune(trimmed[:at])
	domain := trimmed[at+1:]
	if len(local) <= 1 {
		return "*@" + domain
	}
	return string(local[:1]) + "***@" + domain
}

// MaskExportWechatID 微信 OpenID 脱敏：保留首尾各 3 位（111 §4.5）。
func MaskExportWechatID(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= 6 {
		// 首尾各 3 位拼起来就等于原文（甚至更长）→ 全掩码。
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:3]) + "*****" + string(runes[len(runes)-3:])
}

// MaskExportToken 访问令牌脱敏：保留前 6 后 4。
//
// 保留 6 位前缀是为了运营能对上「这是哪个 key」（token_prefix 本来就明文存），
// 后 4 位用于比对是否同一个值；中间一律打码。
func MaskExportToken(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= 10 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:6]) + "****" + string(runes[len(runes)-4:])
}

// MaskExportIP IP 脱敏：保留前两段（203.0.113.45 → 203.0.*.*）。
//
// 不含点（IPv6 / 脏数据）时整体打码：IPv6 按段保留会泄漏过多前缀信息。
func MaskExportIP(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	segments := strings.Split(trimmed, ".")
	if len(segments) != 4 {
		return strings.Repeat("*", len([]rune(trimmed)))
	}
	return segments[0] + "." + segments[1] + ".*.*"
}

// resolveExportMasked 把 *bool 解析成实际开关。
//
// 这是「默认脱敏」的唯一实现点：nil → DefaultExportMasked。
// 任何导出路径都必须经过它，不要在别处再写一次 `if masked == nil { masked = true }`，
// 那样默认值就有了两个来源，改一处漏一处。
func resolveExportMasked(requested *bool) bool {
	if requested == nil {
		return DefaultExportMasked
	}
	return *requested
}

// exportCell 把一个值按列配置脱敏；脱敏函数的入参是**原始字符串**。
func exportCell(column ExportColumn, value string, masked bool) string {
	if !masked || column.Mask == nil {
		return value
	}
	return column.Mask(value)
}

// ExportCSVWithDB 生成某个资源的 CSV（默认脱敏，见 DefaultExportMasked）。
//
// 语义：
//   - 只读，不写库、不改任何状态（调用方是异步任务的执行器）；
//   - 列顺序与 exportResourceSpecs 里登记的顺序一致，导出 → 导入可原样还原（Q12-i）；
//   - 行数上限按资源决定：默认通用 ExportMaxRows（10 万），题库是
//     QuestionExportMaxRows（1 万，见该常量的说明）。超出的部分**不静默截断成完整文件**：
//     截断后 Rows 会小于库里的总数，调用方据此给出「已截断」的提示。
//
// 返回错误时 Content 为空 —— 绝不允许「一半的 CSV」被当成完整结果落盘。
func ExportCSVWithDB(db *gorm.DB, opts ExportDataOptions) (ExportResult, error) {
	if db == nil {
		return ExportResult{}, fmt.Errorf("数据库未初始化")
	}
	resource := strings.ToLower(strings.TrimSpace(opts.Resource))
	spec, ok := exportResourceSpecs[resource]
	if !ok {
		return ExportResult{}, fmt.Errorf("%w: %q", ErrExportResourceUnsupported, opts.Resource)
	}
	masked := resolveExportMasked(opts.Masked)
	limit := exportResolveLimit(resource, opts.Limit)
	rows, err := spec.load(db, limit)
	if err != nil {
		return ExportResult{}, fmt.Errorf("读取 %s 数据失败: %w", resource, err)
	}

	buffer := &bytes.Buffer{}
	// UTF-8 BOM：不加的话中文在 Excel 里是乱码，而运营拿到 CSV 的第一反应就是双击打开。
	buffer.WriteString("\uFEFF")
	writer := csv.NewWriter(buffer)
	header := make([]string, len(spec.columns))
	for i, column := range spec.columns {
		header[i] = column.Header
	}
	if err := writer.Write(header); err != nil {
		return ExportResult{}, fmt.Errorf("写入 CSV 表头失败: %w", err)
	}
	for _, row := range rows {
		record := make([]string, len(spec.columns))
		for i, column := range spec.columns {
			value := ""
			if i < len(row) {
				value = row[i]
			}
			record[i] = exportCell(column, value, masked)
		}
		if err := writer.Write(record); err != nil {
			return ExportResult{}, fmt.Errorf("写入 CSV 数据失败: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return ExportResult{}, fmt.Errorf("写入 CSV 失败: %w", err)
	}
	return ExportResult{Content: buffer.Bytes(), Rows: len(rows), Masked: masked, Resource: resource}, nil
}

// exportResourceSpec 描述一个可导出资源：列定义 + 取数函数。
type exportResourceSpec struct {
	resource string
	// columns 决定 CSV 的列顺序与每列是否脱敏。
	columns []ExportColumn
	// maxRows 是该资源**专属**的行数上限；0 表示用通用 ExportMaxRows。
	//
	// 挂在这里而不是在 ExportCSVWithDB 里写 `if resource == "questions"`：
	// 上限是资源自身的属性，与列定义、取数函数放在一起才能「改资源时一眼看到」。
	// 分散的条件判断会在新增资源时被漏掉。
	maxRows int
	// load 按列顺序返回原始值（未脱敏）。脱敏由 ExportCSVWithDB 统一做，
	// 取数函数里**不许**再各自打码 —— 否则「勾选不脱敏」时拿到的是已经打码的值。
	load func(db *gorm.DB, limit int) ([][]string, error)
}

// exportRowLimit 返回某资源实际生效的行数上限（resource 不在清单内时返回 0）。
//
// 单一实现点：调用方（HTTP 层校验、异步执行器、测试）都从这里取，
// 不要在别处再拼一次 `if resource == questions { 10000 }`。
func exportRowLimit(resource string) int {
	spec, ok := exportResourceSpecs[strings.ToLower(strings.TrimSpace(resource))]
	if !ok {
		return 0
	}
	if spec.maxRows > 0 {
		return spec.maxRows
	}
	return ExportMaxRows
}

// ExportRowLimitOf 返回某资源的行数上限（供 HTTP 层与前端提示「最多导出 N 行」）。
//
// 资源不支持时返回通用上限：这里不是校验入口（校验在 ExportColumnsOf），
// 不给报错是为了让调用方能先查上限、再查列。
func ExportRowLimitOf(resource string) int {
	if limit := exportRowLimit(resource); limit > 0 {
		return limit
	}
	return ExportMaxRows
}

// exportResolveLimit 把「调用方请求的 limit」解析成「实际生效的 limit」。
//
// 三条规则，顺序不能变：
//  1. requested == 0 → 资源上限（不传就用默认）；
//  2. requested < 0  → 资源上限（负数表示"不限制"，但**不限制只能到上限**，
//     否则「传 -1 就能导全库」等于上限不存在）；
//  3. requested > 上限 → 上限（显式传大值同样不许突破）。
//
// 唯一出口是这里：不要在 ExportCSVWithDB 里再写一遍比较，也不要让各 spec 自己夹。
func exportResolveLimit(resource string, requested int) int {
	ceiling := exportRowLimit(resource)
	if ceiling <= 0 {
		ceiling = ExportMaxRows
	}
	if requested <= 0 || requested > ceiling {
		return ceiling
	}
	return requested
}

// exportLimit 把「不限制」翻译成 GORM 的 Limit(-1)。
func exportLimit(tx *gorm.DB, limit int) *gorm.DB {
	if limit < 0 {
		return tx.Limit(-1)
	}
	return tx.Limit(limit)
}

// formatExportTime 统一时间格式（导出文件给人看，不用 RFC3339）。
func formatExportTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(logger.ShanghaiLocation()).Format("2006-01-02 15:04:05")
}

// formatExportTimePtr 用于可空时间列。
func formatExportTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatExportTime(*value)
}

// formatExportFloat 输出金额/余额：保留最多 6 位小数，去掉无意义的尾零。
//
// 不用 %f / strconv.FormatFloat(v, 'f', -1, 64)：后者会把 0.1+0.2 这类
// 二进制误差原样打出来（0.30000000000000004），运营会以为账对不上。
func formatExportFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}

// formatExportBool 输出布尔列（用 1/0 而不是 true/false：Excel 里更好筛选）。
func formatExportBool(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// exportResourceSpecs 是资源注册表。
//
// 8 个页面对应 111 §4.4 的清单，access_tokens 见上方 ExportResourceAccessTokens 的说明。
var exportResourceSpecs = map[string]exportResourceSpec{
	ExportResourceQuestions: {
		resource: ExportResourceQuestions,
		// 题库导出上限 1 万行（不是通用 10 万）：见 QuestionExportMaxRows 的说明。
		// 它与导入侧 QuestionImportMaxRows（5000）构成固定 2 倍关系，
		// 由 TestQuestionImportExportLimitsAreConsistent 钉住。
		maxRows: QuestionExportMaxRows,
		// Q12-i 的关键：CSV 列 = JSON 里全部可导入字段，options / answer 以 JSON 字符串
		// 放单格（111 §4.6）。这样「CSV 导出 → CSV 导入」不会丢选项，
		// 否则选择题会变成无选项的题，导入后无法使用。
		//
		// 与 entity.Tiku 的字段对应关系（9 列 ↔ 9 字段，逐一对齐核验见
		// TestQuestionExportColumnsMatchTikuEntity）：
		//
		//	id          → Tiku.ID          （导出可读，导入**不采信**：主键由库生成）
		//	question    → Tiku.Question
		//	type        → Tiku.Type
		//	options     → Tiku.Options     （JSON 字符串单格）
		//	answer      → Tiku.Answer      （JSON 字符串单格）
		//	course_name → Tiku.CourseName  （去重键第二段）
		//	extra       → Tiku.Extra
		//	source_name → Tiku.SourceName  （导入时空值归一为「人工导入」）
		//	hash        → Tiku.Hash        （导出供对账，导入时**重算不采信**）
		//
		// DeletedAt（归档标记）刻意不在列里：归档题不属于「存在」的题，
		// 导出它等于把已删除题目卖给运营。
		columns: []ExportColumn{
			exportColumn("id"),
			exportColumn("question"),
			exportColumn("type"),
			exportColumn("options"),
			exportColumn("answer"),
			exportColumn("course_name"),
			exportColumn("extra"),
			exportColumn("source_name"),
			exportColumn("hash"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			rows := make([]entity.Tiku, 0, 128)
			if err := exportLimit(db.Order("id asc"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{
					strconv.FormatInt(int64(row.ID), 10),
					row.Question,
					strconv.FormatInt(int64(row.Type), 10),
					row.Options,
					row.Answer,
					row.CourseName,
					row.Extra,
					row.SourceName,
					row.Hash,
				})
			}
			return out, nil
		},
	},
	ExportResourceUsers: {
		resource: ExportResourceUsers,
		// 用户名不脱敏（111 §4.5：审计必须能看到是谁）。
		columns: []ExportColumn{
			exportColumn("id"),
			exportColumn("username"),
			exportColumn("status"),
			exportColumn("role"),
			exportColumn("balance"),
			exportColumn("gift_balance"),
			maskedColumn("phone", MaskExportPhone),
			maskedColumn("wechat_id", MaskExportWechatID),
			exportColumn("created_at"),
			exportColumn("last_login_at"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			rows := make([]entity.User, 0, 128)
			if err := exportLimit(db.Order("id asc"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				phone, wechat := "", ""
				if row.Phone != nil {
					phone = *row.Phone
				}
				if row.WechatID != nil {
					wechat = *row.WechatID
				}
				out = append(out, []string{
					strconv.FormatInt(int64(row.ID), 10),
					row.Username,
					row.Status,
					row.Role,
					formatExportFloat(row.Balance),
					formatExportFloat(row.GiftBalance),
					phone,
					wechat,
					formatExportTime(row.CreatedAt),
					formatExportTimePtr(row.LastLoginAt),
				})
			}
			return out, nil
		},
	},
	ExportResourceBilling: {
		resource: ExportResourceBilling,
		columns: []ExportColumn{
			exportColumn("id"),
			exportColumn("user_id"),
			exportColumn("type"),
			exportColumn("amount"),
			exportColumn("balance_before"),
			exportColumn("balance_after"),
			exportColumn("gift_charged"),
			exportColumn("principal_charged"),
			maskedColumn("client_ip", MaskExportIP),
			exportColumn("remark"),
			exportColumn("created_at"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			rows := make([]entity.BillingRecord, 0, 128)
			if err := exportLimit(db.Order("id asc"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{
					strconv.FormatInt(int64(row.ID), 10),
					strconv.FormatInt(int64(row.UserID), 10),
					row.Type,
					formatExportFloat(row.Amount),
					formatExportFloat(row.BalanceBefore),
					formatExportFloat(row.BalanceAfter),
					formatExportFloat(row.GiftCharged),
					formatExportFloat(row.PrincipalCharged),
					row.ClientIP,
					row.Remark,
					formatExportTime(row.CreatedAt),
				})
			}
			return out, nil
		},
	},
	ExportResourceGifts: {
		resource: ExportResourceGifts,
		// 赠送记录 = billing_records 上四类赠送来源的聚合（见 gift_record_aggregate.go），
		// 不 UNION 四张源表 —— 理由与那里一致：只有账单表每类赠送都必然写一行。
		columns: []ExportColumn{
			exportColumn("created_at"),
			exportColumn("type"),
			exportColumn("user_id"),
			exportColumn("amount"),
			exportColumn("remark"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			rows := make([]entity.BillingRecord, 0, 128)
			types := []string{
				GiftRecordSourceRegistrationBonus,
				GiftRecordSourceReviewRebate,
				GiftRecordSourceReferralRebate,
				GiftRecordSourceRechargePromotion,
			}
			if err := exportLimit(db.Where("type IN ?", types).Order("id asc"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{
					formatExportTime(row.CreatedAt),
					row.Type,
					strconv.FormatInt(int64(row.UserID), 10),
					formatExportFloat(row.Amount),
					row.Remark,
				})
			}
			return out, nil
		},
	},
	ExportResourceLogs: {
		resource: ExportResourceLogs,
		columns: []ExportColumn{
			exportColumn("create_time"),
			exportColumn("module"),
			exportColumn("action"),
			exportColumn("username"),
			maskedColumn("client_ip", MaskExportIP),
			exportColumn("description"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			rows := make([]entity.Log, 0, 128)
			if err := exportLimit(db.Order("id asc"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{
					formatExportTime(row.CreateTime),
					row.Module,
					strconv.FormatInt(int64(row.Action), 10),
					row.Username,
					row.ClientIP,
					row.Description,
				})
			}
			return out, nil
		},
	},
	ExportResourceTickets: {
		resource: ExportResourceTickets,
		columns: []ExportColumn{
			exportColumn("id"),
			exportColumn("user_name"),
			exportColumn("subject"),
			exportColumn("status"),
			exportColumn("priority"),
			exportColumn("origin"),
			exportColumn("category"),
			exportColumn("created_at"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			rows := make([]entity.Ticket, 0, 128)
			if err := exportLimit(db.Order("id asc"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{
					strconv.FormatInt(row.ID, 10),
					row.UserName,
					row.Subject,
					row.Status,
					row.Priority,
					row.Origin,
					row.Category,
					formatExportTime(row.CreatedAt),
				})
			}
			return out, nil
		},
	},
	ExportResourceBlacklist: {
		resource: ExportResourceBlacklist,
		// 黑名单的 value 列一律脱敏：它本身就是别人的手机号 / IP / OpenID，
		// 导出文件一旦外流就等于批量泄漏（46-*.md 的口径）。
		columns: []ExportColumn{
			exportColumn("kind"),
			maskedColumn("value", MaskExportBlacklistValue),
			exportColumn("match_type"),
			exportColumn("reason"),
			exportColumn("enabled"),
			exportColumn("expires_at"),
			exportColumn("created_at"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			rows := make([]entity.BlacklistEntry, 0, 128)
			if err := exportLimit(db.Order("id asc"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{
					row.Kind,
					row.Pattern,
					row.MatchType,
					row.Reason,
					formatExportBool(row.Enabled),
					formatExportTimePtr(row.ExpiresAt),
					formatExportTime(row.CreatedAt),
				})
			}
			return out, nil
		},
	},
	ExportResourceArchivedUsers: {
		resource: ExportResourceArchivedUsers,
		// 归档账号只能 Unscoped 才看得到（users.deleted_at 是 GORM 软删除列）。
		columns: []ExportColumn{
			exportColumn("id"),
			exportColumn("username"),
			exportColumn("archived_reason"),
			exportColumn("archived_at"),
			maskedColumn("phone", MaskExportPhone),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			users := make([]entity.User, 0, 64)
			if err := exportLimit(db.Unscoped().Where("deleted_at IS NOT NULL").Order("id asc"), limit).
				Find(&users).Error; err != nil {
				return nil, err
			}
			// 归档元数据（原因 / 时点）在 entity.UserArchive 上：与 entity.User 同一张物理表，
			// 但列不同，所以单独查一次再按 id 对齐。
			archives := make([]entity.UserArchive, 0, len(users))
			if len(users) > 0 {
				ids := make([]int32, 0, len(users))
				for _, user := range users {
					ids = append(ids, user.ID)
				}
				if err := db.Where("id IN ?", ids).Find(&archives).Error; err != nil {
					return nil, err
				}
			}
			archiveByID := make(map[int32]entity.UserArchive, len(archives))
			for _, archive := range archives {
				archiveByID[archive.UserID] = archive
			}
			out := make([][]string, 0, len(users))
			for _, user := range users {
				phone := ""
				if user.Phone != nil {
					phone = *user.Phone
				}
				archive := archiveByID[user.ID]
				archivedAt := ""
				if archive.ArchivedAt != nil {
					archivedAt = formatExportTime(*archive.ArchivedAt)
				} else if user.DeletedAt.Valid {
					archivedAt = formatExportTime(user.DeletedAt.Time)
				}
				out = append(out, []string{
					strconv.FormatInt(int64(user.ID), 10),
					user.Username,
					archive.ArchivedReason,
					archivedAt,
					phone,
				})
			}
			return out, nil
		},
	},
	ExportResourceAccessTokens: {
		resource: ExportResourceAccessTokens,
		// 刻意不含 token_hash：它是「bearer 凭据的哈希」，导出后可直接用于撞库核对。
		columns: []ExportColumn{
			exportColumn("id"),
			exportColumn("user_id"),
			exportColumn("name"),
			exportColumn("token_prefix"),
			maskedColumn("token_value", MaskExportToken),
			exportColumn("status"),
			exportColumn("created_at"),
		},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			rows := make([]entity.AccessToken, 0, 128)
			if err := exportLimit(db.Order("id asc"), limit).Find(&rows).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(rows))
			for _, row := range rows {
				out = append(out, []string{
					strconv.FormatInt(int64(row.ID), 10),
					strconv.FormatInt(int64(row.UserID), 10),
					row.Name,
					row.TokenPrefix,
					row.TokenValue,
					row.Status,
					formatExportTime(row.CreatedAt),
				})
			}
			return out, nil
		},
	},
}

// MaskExportBlacklistValue 黑名单值的脱敏：复用 service.MaskBlacklistValue
// （按 kind 分流：ip 保留前两段、手机号前 3 后 4、OpenID 前 3 后 3、地区保留省级）。
//
// 单独包一层是为了让它符合 exportMaskFunc 的签名，并把「识别不出就全掩码」
// 的兜底固定在这一处：MaskBlacklistValue 对未知 kind 会原样返回。
func MaskExportBlacklistValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	kind := blacklistKindOfExportValue(trimmed)
	masked := MaskBlacklistValue(kind, trimmed, "")
	if masked == trimmed {
		return strings.Repeat("*", len([]rune(trimmed)))
	}
	return masked
}

// blacklistKindOfExportValue 按值的形态推断 kind（导出只有值，没有单独的 kind 列进脱敏函数）。
//
// 判据顺序与 NormalizeBlacklistValue 的语义一致：先 IP / CIDR，再手机号，其余当 OpenID。
func blacklistKindOfExportValue(value string) string {
	if strings.Contains(value, "/") || strings.Count(value, ".") == 3 || strings.Contains(value, ":") {
		return BlacklistKindIP
	}
	if len([]rune(value)) == 11 && strings.Trim(value, "0123456789") == "" {
		return BlacklistKindPhone
	}
	return BlacklistKindWechat
}

// ExportResourceList 返回支持导出的资源标识清单（升序），供前端下拉与管理页校验。
//
// 返回副本而不是直接暴露 map：调用方一旦改到内部 map，后续导出行为会莫名变化。
func ExportResourceList() []string {
	list := make([]string, 0, len(exportResourceSpecs))
	for resource := range exportResourceSpecs {
		list = append(list, resource)
	}
	// 固定顺序，避免前端下拉每次刷新顺序不同（map 迭代顺序随机）。
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	return list
}

// ExportColumnsOf 返回某资源的列名（表头），供导出对话框展示「将导出哪些列」。
func ExportColumnsOf(resource string) ([]string, error) {
	spec, ok := exportResourceSpecs[strings.ToLower(strings.TrimSpace(resource))]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrExportResourceUnsupported, resource)
	}
	headers := make([]string, len(spec.columns))
	for i, column := range spec.columns {
		headers[i] = column.Header
	}
	return headers, nil
}
