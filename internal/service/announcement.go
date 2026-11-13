package service

// 公告系统（N4，task_doc/112 §4.3 + 113 + 121 §4.5）。
//
// 本文件只做数据与判定，不含任何 HTTP 适配（那在 controller/announcement_admin.go），
// 也不含任何响应结构体改动（pkg/model 由 captain 统一接线）。
//
// 两种公告的分工（113 §8.1 是权威口径，112 §4.8 的「横幅」已被它推翻）：
//
//	kind=normal    → 只在 TIKU WebUI 显示，**绝不下发**到搜题响应
//	kind=incident  → WebUI + 搜题响应下发（PLUS 侧 swal.fire 弹一次）
//
// 关键约束（113 §1.1 / §4.2）：
//   - 故障公告**不能影响搜题**。因此下发查询必须是「读不到就当作没有」的 fail-open
//     语义 —— 用 ActiveIncidentsOrEmpty（吞错），而不是把错误继续往搜题主链路扔。
//   - 下发只决定「把哪些公告带出去」；PLUS 侧的去重（本页面每 ID 只弹一次）不在这里，
//     也无法在这里做（服务端无状态）。

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

var (
	// ErrAnnouncementDatabaseUnavailable 数据库句柄为空。
	ErrAnnouncementDatabaseUnavailable = errors.New("数据库未初始化")
	// ErrAnnouncementNotFound 公告不存在（或已被删除）。
	ErrAnnouncementNotFound = errors.New("公告不存在")
	// ErrAnnouncementTitleRequired 标题不能为空。
	ErrAnnouncementTitleRequired = errors.New("公告标题不能为空")
	// ErrAnnouncementContentRequired 正文不能为空。
	ErrAnnouncementContentRequired = errors.New("公告内容不能为空")
	// ErrAnnouncementKindInvalid Kind 只能取 normal / incident（⑥ 二值约束）。
	ErrAnnouncementKindInvalid = errors.New("公告类型只能是 normal 或 incident")
	// ErrAnnouncementSeverityInvalid Severity 只能取 info / warning / critical。
	ErrAnnouncementSeverityInvalid = errors.New("公告严重级别只能是 info、warning 或 critical")
	// ErrAnnouncementWindowInvalid 时间窗非法（结束早于开始）。
	ErrAnnouncementWindowInvalid = errors.New("公告结束时间不能早于开始时间")
)

// 字段长度上限与实体列宽一致（varchar(200) / text）。
// 在 service 层截断而不是让数据库报错：SQLite 不强制 varchar 长度，
// MySQL 严格模式会直接 1406 报错 —— 同一份输入两个环境两种行为是最难排查的形态。
const (
	announcementTitleMaxRunes   = 200
	announcementContentMaxRunes = 20000
)

// ActiveIncidentsLimit 是单次搜题随响应下发的公告条数上限。
//
// 存在的意义不是性能，而是「不让响应膨胀」：故障期间管理员可能连发多条，
// PLUS 侧只会弹第一条（113 §6 的 N4-g），下发 50 条既无用又会把响应撑大。
const ActiveIncidentsLimit = 10

// AnnouncementInput 是新建/编辑公告的输入。
//
// Enabled 用指针：区分「没传」（Create 默认 true、Update 保持不变）
// 与「显式传 false」（要停用）。裸 bool 做不到这个区分 ——
// 这正是实体上刻意不写 `default:true` 的同一个坑的另一面。
type AnnouncementInput struct {
	Kind     string     `json:"kind"`
	Title    string     `json:"title"`
	Content  string     `json:"content"`
	Severity string     `json:"severity"`
	Enabled  *bool      `json:"enabled"`
	StartsAt *time.Time `json:"startsAt"`
	EndsAt   *time.Time `json:"endsAt"`
}

// AnnouncementListFilter 是管理端列表的筛选条件。
//
// Kind / Keyword 空串表示不筛；Enabled 为 nil 表示不筛（裸 bool 的零值会把
// 「不筛」退化成「只看停用」，与 controller.parseOptionalBoolQuery 同一教训）。
//
// ActiveAt 非 nil 时只返回「在那一刻生效」的公告（人工开关 + 时间窗都满足），
// 供 WebUI 首页复用与人工核对「现在到底有哪些公告在生效」。
type AnnouncementListFilter struct {
	Kind     string
	Keywords string
	Enabled  *bool
	ActiveAt *time.Time
}

// ─────────────────────── 下发（搜题响应） ───────────────────────

// ActiveIncidents 返回 now 时刻应当随搜题响应下发的故障公告。
//
// 判定三条同时成立：kind=incident、enabled=true、starts_at <= now 且
// (ends_at 为空 或 ends_at > now)。起点用闭区间、终点用开区间 ——
// 这样「14:00–14:30 的公告」在 14:30:00 整就不再下发，不会因边界重复多弹一次。
//
// 排序：critical > warning > info，同级别内新的在前。PLUS 只弹第一条（113 §6），
// 所以「最先被看到的那条」必须是严重级别最高的那条。
//
// now 由调用方传入而不是内部取 time.Now()：判定逻辑必须能被确定性测试，
// 否则「窗口边界」这类用例只能靠 sleep。
func ActiveIncidents(db *gorm.DB, now time.Time) ([]entity.Announcement, error) {
	if db == nil {
		return nil, ErrAnnouncementDatabaseUnavailable
	}
	if now.IsZero() {
		now = time.Now()
	}
	at := announcementNormalizeTime(now)

	var rows []entity.Announcement
	query := db.Session(&gorm.Session{NewDB: true}).
		Where("kind = ?", entity.AnnouncementKindIncident).
		Where("enabled = ?", true).
		Where("starts_at <= ?", at).
		Where("(ends_at IS NULL OR ends_at > ?)", at).
		Order(announcementSeverityOrderClause).
		Order("starts_at DESC").
		Order("id DESC").
		Limit(ActiveIncidentsLimit)
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询生效故障公告失败: %w", err)
	}
	return rows, nil
}

// ActiveIncidentsOrEmpty 是 ActiveIncidents 的 fail-open 包装，供搜题主链路调用。
//
// 语义：**任何失败都当作「没有公告」**，只记一行 SysLog。
//
// 为什么必须有这个包装（113 §4「弹窗绝不能阻塞搜题」的反面）：
// 公告是「附加信息」，搜题是「主业务」。如果查公告的失败（表还没迁移好、
// 数据库抖动、SQL 出错）冒泡到搜题 handler，用户会因为「一条通知没查到」
// 而搜不了题 —— 这正是 113 反复强调不能发生的事。
//
// 注意这里的 fail-open 与「建表失败要不要 FATAL」是**两个不同问题**：
// 结构缺失必须让人看见（在迁移里返回 error），运行期读不到则可以降级。
func ActiveIncidentsOrEmpty(db *gorm.DB, now time.Time) []entity.Announcement {
	if db == nil {
		return nil
	}
	rows, err := ActiveIncidents(db, now)
	if err != nil {
		logger.SysLog(fmt.Sprintf("读取故障公告失败（已按无公告降级，不影响搜题）：%v", err))
		return nil
	}
	return rows
}

// ─────────────────────── 管理端读写 ───────────────────────

// ListAllAnnouncements 返回管理端列表（含停用与已过期），按「新的在前」排序。
//
// 管理页必须能看到停用与过期行：它们的语义是「建过这条，但现在不生效」，
// 而不是「不存在」。隐藏它们会让管理员以为公告被删了，重复发布一遍。
func ListAllAnnouncements(db *gorm.DB, filter AnnouncementListFilter) ([]entity.Announcement, error) {
	if db == nil {
		return nil, ErrAnnouncementDatabaseUnavailable
	}
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.Announcement{})

	if kind := strings.ToLower(strings.TrimSpace(filter.Kind)); kind != "" {
		query = query.Where("kind = ?", kind)
	}
	if filter.Enabled != nil {
		query = query.Where("enabled = ?", *filter.Enabled)
	}
	if keyword := strings.TrimSpace(filter.Keywords); keyword != "" {
		// LIKE 两侧都加通配符：管理页的关键字是「我记得标题里有这几个字」，
		// 只做前缀匹配会让中文短词几乎搜不到。
		pattern := "%" + keyword + "%"
		query = query.Where("title LIKE ? OR content LIKE ?", pattern, pattern)
	}
	if filter.ActiveAt != nil {
		at := announcementNormalizeTime(*filter.ActiveAt)
		query = query.
			Where("enabled = ?", true).
			Where("starts_at <= ?", at).
			Where("(ends_at IS NULL OR ends_at > ?)", at)
	}

	var rows []entity.Announcement
	if err := query.Order("id DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询公告列表失败: %w", err)
	}
	return rows, nil
}

// CreateAnnouncement 新建一条公告。
//
// 缺省值刻意保守：
//   - Kind 缺省 normal（不填就不要影响 PLUS）；
//   - Severity 缺省 info；
//   - Enabled 缺省 true（建了就该生效，除非显式传 false）；
//   - StartsAt 缺省 now（立刻开始），EndsAt 缺省 nil（永久）。
func CreateAnnouncement(db *gorm.DB, input AnnouncementInput, createdBy int32, now time.Time) (entity.Announcement, error) {
	if db == nil {
		return entity.Announcement{}, ErrAnnouncementDatabaseUnavailable
	}
	if now.IsZero() {
		now = time.Now()
	}
	normalized, err := announcementNormalizeInput(input, now, true)
	if err != nil {
		return entity.Announcement{}, err
	}
	record := entity.Announcement{
		Kind:      normalized.Kind,
		Title:     normalized.Title,
		Content:   normalized.Content,
		Severity:  normalized.Severity,
		Enabled:   normalized.Enabled,
		StartsAt:  normalized.StartsAt,
		EndsAt:    normalized.EndsAt,
		CreatedBy: createdBy,
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Create(&record).Error; err != nil {
		return entity.Announcement{}, fmt.Errorf("新建公告失败: %w", err)
	}
	return record, nil
}

// UpdateAnnouncement 编辑一条公告（全量覆盖）。
//
// 全量语义而不是「部分字段":编辑表单本来就提交完整内容，部分更新会让
// 「把标题清空」这类操作无法表达（空串会被当成「没传」）。Enabled 仍用指针，
// 因为「不动开关」是编辑正文时的常见意图，见 AnnouncementInput 的注释。
func UpdateAnnouncement(db *gorm.DB, id int64, input AnnouncementInput, now time.Time) (entity.Announcement, error) {
	if db == nil {
		return entity.Announcement{}, ErrAnnouncementDatabaseUnavailable
	}
	if id <= 0 {
		return entity.Announcement{}, ErrAnnouncementNotFound
	}
	if now.IsZero() {
		now = time.Now()
	}
	existing, err := announcementByID(db, id)
	if err != nil {
		return entity.Announcement{}, err
	}
	normalized, err := announcementNormalizeInput(input, now, false)
	if err != nil {
		return entity.Announcement{}, err
	}
	enabled := existing.Enabled
	if normalized.EnabledSet {
		enabled = normalized.Enabled
	}
	// 时间窗按「传了就覆盖、没传就保留」处理：编辑正文时不该顺手把
	// 已经设好的结束时间抹成「永久有效」。
	startsAt := existing.StartsAt
	if normalized.StartsAtSet {
		startsAt = normalized.StartsAt
	}
	endsAt := existing.EndsAt
	if normalized.EndsAtSet {
		endsAt = normalized.EndsAt
	}
	if err := announcementValidateWindow(startsAt, endsAt); err != nil {
		return entity.Announcement{}, err
	}

	updates := map[string]any{
		"kind":       normalized.Kind,
		"title":      normalized.Title,
		"content":    normalized.Content,
		"severity":   normalized.Severity,
		"enabled":    enabled,
		"starts_at":  startsAt,
		"ends_at":    endsAt,
		"updated_at": announcementNormalizeTime(now),
	}
	result := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.Announcement{}).
		Where("id = ?", id).
		Updates(updates)
	if result.Error != nil {
		return entity.Announcement{}, fmt.Errorf("保存公告失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return entity.Announcement{}, ErrAnnouncementNotFound
	}
	return announcementByID(db, id)
}

// SetAnnouncementEnabled 只改总开关（启停），不动其它列。
//
// 单独一条而不是「让前端走 UpdateAnnouncement 传全量」：启停是列表页的一键操作，
// 若必须回传整条公告，前端一份过期快照就会把别人刚改的标题覆盖掉。
func SetAnnouncementEnabled(db *gorm.DB, id int64, enabled bool, now time.Time) (entity.Announcement, error) {
	if db == nil {
		return entity.Announcement{}, ErrAnnouncementDatabaseUnavailable
	}
	if id <= 0 {
		return entity.Announcement{}, ErrAnnouncementNotFound
	}
	if now.IsZero() {
		now = time.Now()
	}
	result := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.Announcement{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"enabled":    enabled,
			"updated_at": announcementNormalizeTime(now),
		})
	if result.Error != nil {
		return entity.Announcement{}, fmt.Errorf("启停公告失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return entity.Announcement{}, ErrAnnouncementNotFound
	}
	return announcementByID(db, id)
}

// DeleteAnnouncement 硬删一条公告。
//
// 为什么是硬删而不是软删：公告不是账务/审计数据（与黑名单「删了就丢审计」不同），
// 它本身就是「当下的告示」；留一堆 disabled 的历史行会让列表越用越乱。
// 需要留痕的地方是账户审计日志（controller 里写），不是这张表。
func DeleteAnnouncement(db *gorm.DB, id int64) error {
	if db == nil {
		return ErrAnnouncementDatabaseUnavailable
	}
	if id <= 0 {
		return ErrAnnouncementNotFound
	}
	result := db.Session(&gorm.Session{NewDB: true}).
		Where("id = ?", id).
		Delete(&entity.Announcement{})
	if result.Error != nil {
		return fmt.Errorf("删除公告失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrAnnouncementNotFound
	}
	return nil
}

// ─────────────────────── 短名别名（任务书约定的导出名） ───────────────────────

// ListAll / Create / Update / Delete 是任务书（t144）指定的导出名。
//
// 它们只是上面带业务后缀版本的薄别名：保留短名的原因是对外契约要能一眼对到
// 任务书签名清单；同时保留长名是因为 service 包里有二十多张表的读写函数，
// 裸 Create/Update/Delete 在阅读时无法判断操作对象，也不利于后续
// 「按表前缀收敛」的清理。两组名同时存在不会有歧义（参数与返回完全一致）。
func ListAll(db *gorm.DB, filter AnnouncementListFilter) ([]entity.Announcement, error) {
	return ListAllAnnouncements(db, filter)
}

func Create(db *gorm.DB, input AnnouncementInput, createdBy int32, now time.Time) (entity.Announcement, error) {
	return CreateAnnouncement(db, input, createdBy, now)
}

func Update(db *gorm.DB, id int64, input AnnouncementInput, now time.Time) (entity.Announcement, error) {
	return UpdateAnnouncement(db, id, input, now)
}

func Delete(db *gorm.DB, id int64) error {
	return DeleteAnnouncement(db, id)
}

// ─────────────────────── 内部实现 ───────────────────────

// announcementSeverityOrderClause 让 critical 排在 warning、info 之前。
//
// 用 CASE WHEN 而不是在 Go 里排序：Limit 是在 SQL 里生效的，
// 若先取 10 条再排序，轻级别的公告会把 critical 挤出窗口。
// CASE 表达式 SQLite 与 MySQL 都支持。
const announcementSeverityOrderClause = "CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END"

// announcementNormalizeTime 统一时间到 UTC 并截断到秒。
//
// 为什么必须统一：SQLite 把 DATETIME 按字符串比较，`starts_at <= ?` 的可靠性
// 完全取决于写入侧与查询侧用同一个时区偏移。混用 `2026-11-13T14:00:00+08:00`
// 与 `2026-11-13T06:00:00Z` 会让字符串比较得出错误的先后关系 ——
// 表现是「公告在错误的时间生效」，而不会报任何错。
//
// 截断到秒：MySQL 的 DATETIME 默认精度是秒，带毫秒的值会被四舍五入，
// 导致「到点生效」在数据库和进程内看到的时刻不一致。
func announcementNormalizeTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Second)
}

// announcementNormalizedInput 是校验、归一化之后的输入。
type announcementNormalizedInput struct {
	Kind        string
	Title       string
	Content     string
	Severity    string
	Enabled     bool
	EnabledSet  bool
	StartsAt    time.Time
	StartsAtSet bool
	EndsAt      *time.Time
	EndsAtSet   bool
}

// announcementNormalizeInput 校验并归一化输入。
//
// forCreate 决定缺省值：新建时 StartsAt 缺省 now、Enabled 缺省 true；
// 编辑时不填就保持原值（由调用方处理），这里只负责「填了的部分」合法。
//
// 校验一律返回**明确的哨兵错误**而不是静默 clamp：
// 管理员填了 `kind=maintenance` 却被静默当成 normal，会得到一条
// 「本该下发却没下发」的故障公告 —— 这种失败在故障期代价极高。
func announcementNormalizeInput(input AnnouncementInput, now time.Time, forCreate bool) (announcementNormalizedInput, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return announcementNormalizedInput{}, ErrAnnouncementTitleRequired
	}
	title = announcementTruncateRunes(title, announcementTitleMaxRunes)

	content := strings.TrimSpace(input.Content)
	if content == "" {
		return announcementNormalizedInput{}, ErrAnnouncementContentRequired
	}
	content = announcementTruncateRunes(content, announcementContentMaxRunes)

	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if kind == "" {
		kind = entity.AnnouncementKindNormal
	}
	if kind != entity.AnnouncementKindNormal && kind != entity.AnnouncementKindIncident {
		return announcementNormalizedInput{}, ErrAnnouncementKindInvalid
	}

	severity := strings.ToLower(strings.TrimSpace(input.Severity))
	if severity == "" {
		severity = entity.AnnouncementSeverityInfo
	}
	switch severity {
	case entity.AnnouncementSeverityInfo, entity.AnnouncementSeverityWarning, entity.AnnouncementSeverityCritical:
	default:
		return announcementNormalizedInput{}, ErrAnnouncementSeverityInvalid
	}

	normalized := announcementNormalizedInput{
		Kind:        kind,
		Title:       title,
		Content:     content,
		Severity:    severity,
		Enabled:     true,
		EnabledSet:  input.Enabled != nil,
		StartsAt:    announcementNormalizeTime(now),
		StartsAtSet: input.StartsAt != nil,
	}
	if normalized.EnabledSet {
		normalized.Enabled = *input.Enabled
	} else if forCreate {
		// 新建时 Enabled 缺省 true：建了就该生效，否则运营加完一条
		// 永久规则发现不生效，还得回头再点一次启用。
		// 编辑时不填则保持原值（调用方依据 EnabledSet 判断）。
		normalized.Enabled = true
	}
	if normalized.StartsAtSet {
		normalized.StartsAt = announcementNormalizeTime(*input.StartsAt)
	}

	// EndsAt 的「显式置空」如何表达：编辑接口里传 `endsAt: null` 与「没传该字段」
	// 在 encoding/json 下**无法区分**（两者都落 nil）。因此这里把 nil 一律当作
	// 「没传」，置空永久有效走 UpdateAnnouncement 的「保留原值」分支 ——
	// 即：想把有结束时间的公告改成永久，需要显式传一个很远的将来时间。
	// 这是一个已知的表达力缺口，取舍理由见报告「取舍与未解决项」；
	// 用 *time.Time 而不是 time.Time 就是为了保留将来改成三态的可能。
	if input.EndsAt != nil {
		endsAt := announcementNormalizeTime(*input.EndsAt)
		normalized.EndsAt = &endsAt
		normalized.EndsAtSet = true
	}

	if err := announcementValidateWindow(normalized.StartsAt, normalized.EndsAt); err != nil {
		return announcementNormalizedInput{}, err
	}
	return normalized, nil
}

// announcementValidateWindow 拒绝「结束早于开始」。
//
// 允许 endsAt == startsAt 吗？不允许。那是一个长度为零的窗口，
// 永远不生效 —— 语义上等价于「写了一条永远不会出现的公告」，
// 几乎必然是填错了。显式 400 比让它静默不生效更好。
func announcementValidateWindow(startsAt time.Time, endsAt *time.Time) error {
	if endsAt == nil {
		return nil
	}
	if !endsAt.After(startsAt) {
		return ErrAnnouncementWindowInvalid
	}
	return nil
}

// announcementTruncateRunes 按**字符**（不是字节）截断，与列宽语义一致。
//
// 与 WriteAccountAuditWithDB 里 description 的处理方式同构。
func announcementTruncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// announcementByID 按主键取一条公告；不存在时返回 ErrAnnouncementNotFound。
func announcementByID(db *gorm.DB, id int64) (entity.Announcement, error) {
	var row entity.Announcement
	err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.Announcement{}, ErrAnnouncementNotFound
		}
		return entity.Announcement{}, fmt.Errorf("查询公告失败: %w", err)
	}
	return row, nil
}
