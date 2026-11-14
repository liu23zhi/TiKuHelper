package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	TicketSubjectMaxLength             = 120
	TicketBodyMaxLength                = 10000
	TicketPageSizeDefault              = 20
	TicketPageSizeMax                  = 100
	TicketWecomEnabledSettingKey       = "wecom_ticket_notifications_enabled"
	TicketWecomWebhookURLSettingKey    = "wecom_ticket_webhook_url"
	TicketWecomMentionedListSettingKey = "wecom_ticket_mentioned_list"

	ticketNotificationPending     = "pending"
	ticketNotificationSending     = "sending"
	ticketNotificationSent        = "sent"
	ticketNotificationFailed      = "failed"
	ticketNotificationSkipped     = "skipped"
	TicketNotificationMaxAttempts = 3
)

var (
	ErrTicketSubjectRequired          = errors.New("工单标题不能为空")
	ErrTicketBodyRequired             = errors.New("工单内容不能为空")
	ErrTicketTextTooLong              = errors.New("工单内容长度超出限制")
	ErrTicketNotFound                 = errors.New("工单不存在")
	ErrTicketForbidden                = errors.New("无权访问该工单")
	ErrTicketInvalidStatus            = errors.New("工单状态无效")
	ErrTicketInvalidPriority          = errors.New("工单优先级无效")
	ErrTicketInvalidOrigin            = errors.New("工单来源无效")
	ErrTicketInvalidCategory          = errors.New("工单分类无效")
	ErrTicketSystemUserRequired       = errors.New("系统工单必须记录被涉及用户")
	ErrTicketContextTooLarge          = errors.New("工单上下文快照过大")
	ErrTicketWebhookInvalid           = errors.New("企业微信机器人 Webhook 必须是 HTTPS 地址")
	ErrTicketWebhookNotConfigured     = errors.New("企业微信工单通知未启用或未配置有效 Webhook")
	ErrTicketNotificationNotRetryable = errors.New("该提醒当前不可重试")
)

// TicketListQuery 是列表筛选。Origin / Category 只做**精确匹配**，
// 空串表示不过滤（与 Status 同构）。
type TicketListQuery struct {
	Status      string
	Keyword     string
	Origin      string
	Category    string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	UpdatedFrom *time.Time
	UpdatedTo   *time.Time
}

type TicketListResult struct {
	Items    []TicketListItem `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

type TicketListItem struct {
	ID            int64      `json:"id"`
	UserID        int32      `json:"userId"`
	UserName      string     `json:"userName"`
	Subject       string     `json:"subject"`
	Status        string     `json:"status"`
	Priority      string     `json:"priority"`
	Origin        string     `json:"origin"`
	Category      string     `json:"category"`
	LastMessageAt time.Time  `json:"lastMessageAt"`
	ClosedAt      *time.Time `json:"closedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	MessageCount  int64      `json:"messageCount"`
	// Refund 只在 category=refund 的行上有值（T-209 退款工单合并）。
	//
	// 为什么必须单独下发退款真实状态：列表的 `Status` 是**映射出来的展示口径**
	// （退款 7 态 → 工单 4 态），管理员真正要看的「冻结中 / 处理中 / 结果待核对」
	// 只能从后端拿。前端据此显示退款徽标并给出「去处理退款」入口。
	Refund *TicketRefundSummary `json:"refund,omitempty"`
}

type TicketDetail struct {
	Ticket       entity.Ticket           `json:"ticket"`
	Messages     []entity.TicketMessage  `json:"messages"`
	Notification *TicketNotificationInfo `json:"notification,omitempty"`
	// Attachments 是工单的全部图片（含「未通过检测」的条目，后者只有占位信息）。
	// 敏感图**永远**只以占位条目出现，字节必须走单独的下载接口。
	Attachments []TicketAttachmentInfo `json:"attachments"`
}

type TicketNotificationInfo struct {
	ID           int64      `json:"id"`
	Status       string     `json:"status"`
	AttemptCount int        `json:"attemptCount"`
	LastError    string     `json:"lastError,omitempty"`
	SentAt       *time.Time `json:"sentAt,omitempty"`
}

type CreateTicketInput struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// AttachmentIds 是「发工单时带图」：图片先通过上传接口落库（此时 ticket_id=0），
	// 建单时在同一事务里认领到首条消息。未认领的草稿图不会被任何工单读到。
	AttachmentIds []int64 `json:"attachmentIds,omitempty"`
	// TicketType 是**用户在新建表单里选的工单类型**（T-209）。
	//
	// # 为什么它叫 TicketType 而不是 Category
	//
	// 69-*.md §七.1 的红线是「category 不得由用户自选」——那条红线的理由是套利：
	// 用户挑 `registration_bonus` 就能诱导管理员点「一键补发赠金」，
	// 挑 `blacklist_appeal` 就能绕过短信验证进入解封流程。
	//
	// 用户现在明确要求「新建工单的时候让用户选择工单类型」。两者并不矛盾：
	// 把「用户能选什么」收窄成一份**白名单**（见 UserSelectableTicketTypes），
	// 只有 general 与 refund 两项，且两项都不带任何「一键动作」。
	// 归因类（registration_bonus / blacklist_appeal / account_*）仍然只能由
	// 服务端按来源强制赋值，客户端传了也不认 —— 那条安全用例
	// （TestTicketCategoryCannotBeChosenByUser）因此依旧成立。
	TicketType string `json:"ticketType,omitempty"`
	// 以下三个字段只在 TicketType=refund 时有意义，其余类型必须留空。
	//
	// 它们是**退款流程的入参**，不是工单字段：选了退款就走
	// service.CreateTicketRefundWithDB（真实落 ticket_refunds 行 + 冻结/打款状态机），
	// 而不是把 category 改个字符串了事。
	RefundOrderID        *int32 `json:"refundOrderId,omitempty"`
	RefundRequestedCents int64  `json:"refundRequestedCents,omitempty"`
	RefundReason         string `json:"refundReason,omitempty"`
}

// 用户可自选的工单类型（T-209）。
//
// 收窄到两项是刻意的：这两项都**没有**「一键动作」，用户选它们不会给自己
// 换来特权（general 无动作；refund 的审核/打款只有管理员能点）。
// 任何将来要加进来的类型，判据是「用户自选后能不能触发一个管理员侧的一键动作」——
// 能，就不许加。
const (
	TicketTypeGeneral = entity.TicketCategoryGeneral
	TicketTypeRefund  = entity.TicketCategoryRefund
)

// UserSelectableTicketTypes 是新建表单允许出现、且服务端认可的取值。
//
// 空串等价于 general（老客户端不发这个字段，行为必须与改造前一致）。
func normalizeUserTicketType(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", TicketTypeGeneral:
		return TicketTypeGeneral, nil
	case TicketTypeRefund:
		return TicketTypeRefund, nil
	default:
		// 明确报错而不是静默降级：用户选了一个不存在的类型却拿到普通工单，
		// 会让他以为「退款申请已提交」，而实际上什么都没发生。
		return "", ErrTicketInvalidCategory
	}
}

type ReplyTicketInput struct {
	Body   string  `json:"body"`
	Status *string `json:"status,omitempty"`
	// AttachmentIds 是本条回复要挂上的、已上传的附件（先上传后回复）。
	AttachmentIds []int64 `json:"attachmentIds,omitempty"`
}

type UpdateTicketInput struct {
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

func normalizeTicketText(value string, max int, field error) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", field
	}
	if utf8.RuneCountInString(value) > max {
		return "", fmt.Errorf("%w：最多%d个字符", ErrTicketTextTooLong, max)
	}
	return value, nil
}

func normalizeTicketStatus(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case entity.TicketStatusOpen, entity.TicketStatusPending, entity.TicketStatusResolved, entity.TicketStatusClosed:
		return value, nil
	default:
		return "", ErrTicketInvalidStatus
	}
}

// normalizeTicketPriority 放行 low / normal / high / urgent 四档（69-*.md §2.3）。
//
// allowUrgent 是**权限闸门**而不是取值校验：urgent 只给管理员手设。
// 系统建单与用户建单都传 false，因此它们即使要求 urgent 也会被拒成普通档。
func normalizeTicketPriority(value string, allowUrgent bool) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return entity.TicketPriorityNormal, nil
	}
	switch value {
	case entity.TicketPriorityLow, entity.TicketPriorityNormal, entity.TicketPriorityHigh:
		return value, nil
	case entity.TicketPriorityUrgent:
		if allowUrgent {
			return value, nil
		}
		return "", ErrTicketInvalidPriority
	default:
		return "", ErrTicketInvalidPriority
	}
}

// normalizeTicketOrigin 只用于**筛选条件**：空串表示「不过滤」。
// 它不接受任何自由文本，非法值直接报错而不是静默忽略（静默忽略会让
// 前端以为筛了、后端给出全量，是最难查的一类错）。
func normalizeTicketOrigin(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	switch value {
	case entity.TicketOriginUser, entity.TicketOriginSystem, entity.TicketOriginAppeal:
		return value, nil
	default:
		return "", ErrTicketInvalidOrigin
	}
}

func normalizeTicketCategory(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	switch value {
	case entity.TicketCategoryGeneral, entity.TicketCategoryRegistrationBonus,
		entity.TicketCategoryBlacklistAppeal, entity.TicketCategoryAccountRebind,
		entity.TicketCategoryAccountClosure, entity.TicketCategoryRefund:
		return value, nil
	default:
		return "", ErrTicketInvalidCategory
	}
}

// ticketDefaultPriorityByCategory 是各类工单的默认优先级（69-*.md §2.3）。
//
// 为什么 blacklist_appeal 与 registration_bonus 是 high：前者用户完全无法使用产品，
// 后者涉及钱且有重试窗口。其余常规业务给 normal。
var ticketDefaultPriorityByCategory = map[string]string{
	entity.TicketCategoryGeneral:           entity.TicketPriorityNormal,
	entity.TicketCategoryRegistrationBonus: entity.TicketPriorityHigh,
	entity.TicketCategoryBlacklistAppeal:   entity.TicketPriorityHigh,
	entity.TicketCategoryAccountRebind:     entity.TicketPriorityNormal,
	entity.TicketCategoryAccountClosure:    entity.TicketPriorityNormal,
	entity.TicketCategoryRefund:            entity.TicketPriorityNormal,
}

func defaultTicketPriorityForCategory(category string) string {
	if priority, ok := ticketDefaultPriorityByCategory[strings.ToLower(strings.TrimSpace(category))]; ok {
		return priority
	}
	return entity.TicketPriorityNormal
}

func isTicketAdmin(user *entity.User) bool {
	return user != nil && strings.EqualFold(strings.TrimSpace(user.Role), "admin")
}

func CreateTicketWithDB(db *gorm.DB, user *entity.User, input CreateTicketInput) (TicketDetail, error) {
	if db == nil || user == nil {
		return TicketDetail{}, ErrTicketForbidden
	}
	// T-209：先解析用户选的类型。非法值直接报错（不静默降级成 general ——
	// 用户以为提交了退款、实际只是一张普通工单，是最坏的一种「成功」）。
	ticketType, err := normalizeUserTicketType(input.TicketType)
	if err != nil {
		return TicketDetail{}, err
	}
	if ticketType == TicketTypeRefund {
		// 选了「退款」就必须真的走退款流程：落 ticket_refunds 行、进 7 态状态机，
		// 而不是只把工单的 category 改成 refund。
		return createRefundTicketWithDB(db, user, input)
	}
	// 选了退款却带了退款字段（或反之）：视作参数错，避免「提交了但字段被忽略」。
	if input.RefundOrderID != nil || input.RefundRequestedCents != 0 || strings.TrimSpace(input.RefundReason) != "" {
		return TicketDetail{}, ErrTicketInvalidCategory
	}
	subject, err := normalizeTicketText(input.Subject, TicketSubjectMaxLength, ErrTicketSubjectRequired)
	if err != nil {
		return TicketDetail{}, err
	}
	body, err := normalizeTicketText(input.Body, TicketBodyMaxLength, ErrTicketBodyRequired)
	if err != nil {
		return TicketDetail{}, err
	}

	now := time.Now()
	var ticket entity.Ticket
	err = db.Transaction(func(tx *gorm.DB) error {
		ticket = entity.Ticket{
			UserID: user.ID, UserName: ticketUserName(user), Subject: subject,
			Status: entity.TicketStatusOpen,
			// 用户建单一律 user / general / normal：category 由服务端按来源强制赋值，
			// **不接受客户端自选**（否则用户会挑「能一键补发」的类别套利）。
			// CreateTicketInput 里刻意没有这些字段，客户端多传的同名字段会被 JSON
			// 反序列化直接丢弃。
			Origin:        entity.TicketOriginUser,
			Category:      entity.TicketCategoryGeneral,
			Priority:      entity.TicketPriorityNormal,
			LastMessageAt: now,
		}
		if err := tx.Create(&ticket).Error; err != nil {
			return err
		}
		message := entity.TicketMessage{
			TicketID: ticket.ID, UserID: user.ID, AuthorName: ticketUserName(user),
			Body: body, AuthorRole: "user", CreatedAt: now,
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		if err := BindDraftTicketAttachmentsInTx(tx, user, ticket.ID, message.ID, input.AttachmentIds); err != nil {
			return err
		}
		if err := tx.Model(&entity.Ticket{}).Where("id = ?", ticket.ID).Updates(map[string]interface{}{
			"last_message_at": now,
			"updated_at":      now,
		}).Error; err != nil {
			return err
		}
		return enqueueTicketNotificationInTx(tx, ticket, body, "new_ticket")
	})
	if err != nil {
		return TicketDetail{}, err
	}
	return GetTicketWithDB(db, user, ticket.ID)
}

// CreateSystemTicketInput 是「系统自动建单」的入参。
//
// ⚠️ UserID 是**被涉及用户**的 ID，必须 > 0：它是唯一隔离维度（45-*.md §0.23.2 定案
// 「系统单对被涉及用户可见，隔离靠 user_id」）。用 0 会让「按用户查工单」整体失效，
// 也会让系统单变成孤儿单（管理员查不到这张单是给谁的）。
type CreateSystemTicketInput struct {
	UserID   int32
	UserName string
	Subject  string
	Body     string
	Category string
	// Priority 留空表示按 category 取默认档（见 ticketDefaultPriorityByCategory）。
	// 系统建单不允许 elevated 到 urgent：urgent 只给管理员手设。
	Priority string
	// Context 是类别上下文快照。落库前会过 redactTicketContext：
	// 手机号只留后四位，验证码 / token / password / 密钥一律抹掉。
	Context map[string]interface{}
}

// SystemTicketResult 是系统建单结果。Created=false 表示命中幂等规则，
// Ticket 是既有的那张「同一用户 + 同一 category 且未关闭」的工单。
type SystemTicketResult struct {
	Ticket  TicketDetail `json:"ticket"`
	Created bool         `json:"created"`
}

// ticketSystemAuthorName 是系统消息/系统单在用户可见文本里的署名。
const ticketSystemAuthorName = "系统自动创建"

// CreateSystemTicketWithDB 建一张系统工单（Q26 注册赠金判定失败等场景的后台直调入口）。
//
// 幂等判据：**同一用户 + 同一 category 只允许一张 status != closed 的单**
// （45-*.md §0.23.2 已裁决系统单对被涉及用户可见，幂等仍按「未关闭单」计算）。
// 命中时返回既有工单且 Created=false，调用方（后台重试任务）可以安全重复调用。
//
// 为什么幂等键里只有 userID + category，没有失败时间/重试次数：
// 这些是**每次触发都不同**的字段，放进去等于每次都生成新键，幂等直接失效。
// 它们只进 Context 快照（供管理员看「重试了几次、首末失败时间」）。
//
// 并发窗口：判重与写入在同一个事务里，SQLite（生产 DSN 为 _txlock=immediate）下
// 写事务串行化因而严格成立；MySQL 在默认隔离级别下理论上仍可能双开一张，
// 因此这里不加唯一索引（部分唯一索引在两种方言上不可移植），
// 由「后台任务串行重试」+ 事务内判重共同保证。
func CreateSystemTicketWithDB(db *gorm.DB, input CreateSystemTicketInput) (SystemTicketResult, error) {
	if db == nil {
		return SystemTicketResult{}, ErrTicketForbidden
	}
	if input.UserID <= 0 {
		return SystemTicketResult{}, ErrTicketSystemUserRequired
	}
	category, err := normalizeTicketCategory(input.Category)
	if err != nil {
		return SystemTicketResult{}, err
	}
	if category == "" {
		// 系统单必须有明确分类：general 是「用户自由提问」的兜底，系统单挂 general
		// 会让「按 category 一键动作」无从判断，因此缺省不是 general 而是报错。
		return SystemTicketResult{}, ErrTicketInvalidCategory
	}
	priorityValue := strings.TrimSpace(input.Priority)
	if priorityValue == "" {
		priorityValue = defaultTicketPriorityForCategory(category)
	}
	priority, err := normalizeTicketPriority(priorityValue, false)
	if err != nil {
		return SystemTicketResult{}, err
	}
	subject, err := normalizeTicketText(input.Subject, TicketSubjectMaxLength, ErrTicketSubjectRequired)
	if err != nil {
		return SystemTicketResult{}, err
	}
	body, err := normalizeTicketText(input.Body, TicketBodyMaxLength, ErrTicketBodyRequired)
	if err != nil {
		return SystemTicketResult{}, err
	}
	contextJSON, err := marshalTicketContext(input.Context)
	if err != nil {
		return SystemTicketResult{}, err
	}
	userName := strings.TrimSpace(input.UserName)
	if userName == "" {
		userName = lookupTicketUserName(db, input.UserID)
	}

	now := time.Now()
	var ticket entity.Ticket
	created := false
	err = db.Transaction(func(tx *gorm.DB) error {
		var existing entity.Ticket
		findErr := tx.Where("user_id = ? AND category = ? AND origin = ? AND status <> ?",
			input.UserID, category, entity.TicketOriginSystem, entity.TicketStatusClosed).
			Order("id DESC").First(&existing).Error
		if findErr == nil {
			ticket = existing
			return nil
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		ticket = entity.Ticket{
			UserID:   input.UserID,
			UserName: userName,
			Subject:  subject,
			Status:   entity.TicketStatusOpen,
			// origin=system 是唯一的可见性判据；category 由服务端按调用来源强制赋值。
			Origin:        entity.TicketOriginSystem,
			Category:      category,
			Priority:      priority,
			Context:       contextJSON,
			LastMessageAt: now,
		}
		if err := tx.Create(&ticket).Error; err != nil {
			return err
		}
		// 首条消息由系统署名：AuthorRole=system 让前端显示「系统自动创建」标识。
		// UserID 仍记被涉及用户，管理员按用户筛消息链路才不会断。
		message := entity.TicketMessage{
			TicketID: ticket.ID, UserID: input.UserID, AuthorName: ticketSystemAuthorName,
			Body: body, AuthorRole: entity.TicketAuthorRoleSystem, CreatedAt: now,
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		if err := tx.Model(&entity.Ticket{}).Where("id = ?", ticket.ID).Updates(map[string]interface{}{
			"last_message_at": now,
			"updated_at":      now,
		}).Error; err != nil {
			return err
		}
		created = true
		// 系统单同样进企业微信提醒队列：管理员在 IM 侧就能看到，不依赖进后台刷新。
		return enqueueTicketNotificationInTx(tx, ticket, body, "new_ticket")
	})
	if err != nil {
		return SystemTicketResult{}, err
	}
	detail, err := loadTicketDetailForSystemWithDB(db, ticket.ID)
	if err != nil {
		return SystemTicketResult{}, err
	}
	return SystemTicketResult{Ticket: detail, Created: created}, nil
}

// lookupTicketUserName 取用户显示名；查不到时退化成「用户 #id」而不是空串
// （企业微信提醒里空白的「用户：」等于没有信息）。
func lookupTicketUserName(db *gorm.DB, userID int32) string {
	var user entity.User
	if err := db.Select("id", "username", "nickname").Where("id = ?", userID).First(&user).Error; err == nil {
		if name := ticketUserName(&user); name != "" {
			return name
		}
	}
	return fmt.Sprintf("用户 #%d", userID)
}

// loadTicketDetailForSystemWithDB 读系统工单详情。
//
// 不复用 GetTicketWithDB：那里要求传一个 entity.User，而系统建单没有「当前操作人」。
// 这里也不返回 notification（管理员才需要的投递状态），只给工单本体 + 消息。
func loadTicketDetailForSystemWithDB(db *gorm.DB, id int64) (TicketDetail, error) {
	var ticket entity.Ticket
	if err := db.Where("id = ?", id).First(&ticket).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TicketDetail{}, ErrTicketNotFound
		}
		return TicketDetail{}, err
	}
	var messages []entity.TicketMessage
	if err := db.Where("ticket_id = ?", ticket.ID).Order("created_at ASC, id ASC").Find(&messages).Error; err != nil {
		return TicketDetail{}, err
	}
	return TicketDetail{Ticket: ticket, Messages: messages, Attachments: []TicketAttachmentInfo{}}, nil
}

// ticketContextMaxBytes 限制上下文快照体积：它只用于人工查看，不是日志仓库。
const ticketContextMaxBytes = 16 * 1024

// ticketContextRedacted 是凭据类字段的替换值。
const ticketContextRedacted = "[已隐藏]"

// 凭据类字段的键名片段（小写子串匹配，中英双语都覆盖）。
// 命中即整段替换，不做「保留后四位」——验证码/token 的后四位同样是凭据。
//
// ⚠️ 这里**刻意不含裸 "code"**：子串匹配下 "code" 会命中
// error_code / status_code / area_code / payment_code / country_code / trace_code …，
// 而这些是**排查系统单最需要看的编码字段**，不是凭据。
// 「凭据 code」改用精确键匹配，见 ticketContextSecretExactKeys。
var ticketContextSecretKeyFragments = []string{
	"password", "passwd", "secret", "token", "credential", "cookie", "authorization",
	"captcha", "otp", "verification",
	"密码", "口令", "验证码", "校验码", "密钥", "令牌",
}

// 凭据类字段的**精确键**（小写、去首尾空格后全等匹配）。
//
// 为什么这些不能留在子串列表里：它们的名字都以 code 结尾，但 code 这个子串
// 同时也是「编码类」字段的结尾（error_code / status_code / …）。
// 用子串判会把两类混为一谈；用精确键则各自成立、互不牵连。
//
// 收录判据：这个名字指的是**一次性、可作为身份凭证的短串**（验证码/短信码/动态口令）。
// 反例（明确不收）：error_code / status_code / area_code / payment_code / country_code /
// trace_code / return_code —— 它们是分类与状态标记，公开可见，抹掉只会让快照失去意义。
var ticketContextSecretExactKeys = []string{
	"code",              // 裸 code：绝大多数回调/表单里就是验证码本身
	"sms_code",          // 短信验证码
	"verify_code",       // 校验码
	"verification_code", // 校验码（全称）
	"captcha_code",      // 图形验证码
	"backup_code",       // 两步验证备用码：一次性凭据
	"recovery_code",     // 恢复码：一次性凭据
	"auth_code",         // 授权码
	"access_code",       // 访问码
	"otp",               // 动态口令
}

// ticketContextErrorScopeKeys 是「其子树里的 code 属于错误码而非凭据」的父键。
//
// 为什么需要它：redactTicketContextEntry 会递归进嵌套 map，而嵌套里的键名
// 只有 code 本身，看不出它在这层是不是凭据。但父键给了语义：
//   - {"error": {"code": "UAPI_TIMEOUT"}} → 这是错误码，必须保留
//   - {"wechat": {"code": "081Abc..."}}   → 这是授权码，必须脱敏
//
// 因此只对**明确的错误/响应容器**放行 code，其余嵌套里的 code 仍按凭据处理。
// 这个方向是刻意选的安全侧：漏脱敏的代价（把凭据写进长期保留的日志）远高于
// 多脱敏一个错误码。
var ticketContextErrorScopeKeys = []string{
	"error", "err", "errors", "failure", "exception", "resp", "response",
	"错误", "异常", "失败",
}

// 手机号/号码类字段：只留后四位，满足「管理员能核对是哪个号」又不落全集。
var ticketContextPhoneKeyFragments = []string{"phone", "mobile", "tel", "手机号", "手机", "电话", "号码"}

// marshalTicketContext 把类别上下文序列化成落库 JSON，**先脱敏再序列化**。
//
// 红线（69-*.md §3.1 / §七.3）：Context 不得存凭据 —— 手机号只存后四位，
// 不存验证码 / token / password。脱敏放在服务端统一入口，调用方不需要记得。
func marshalTicketContext(raw map[string]interface{}) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	payload, err := json.Marshal(redactTicketContextValue(raw))
	if err != nil {
		return "", err
	}
	if len(payload) > ticketContextMaxBytes {
		return "", ErrTicketContextTooLarge
	}
	return string(payload), nil
}

func redactTicketContextValue(value interface{}) interface{} {
	return redactTicketContextValueIn(value, "")
}

// redactTicketContextValueIn 是带父键上下文的递归入口。
//
// parentKey 只用于一处判断：嵌套 map 里的裸 code 是「错误码」还是「凭据码」
// （见 ticketContextErrorScopeKeys）。其余键的判定与父键无关。
func redactTicketContextValueIn(value interface{}, parentKey string) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = redactTicketContextEntryIn(key, item, parentKey)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			redacted := redactTicketContextEntryIn(key, item, parentKey)
			if text, ok := redacted.(string); ok {
				out[key] = text
				continue
			}
			out[key] = ticketContextRedacted
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			// 数组里的元素沿用外层父键：{"error":[{...},{...}]} 里每个元素的 code
			// 仍然处于错误上下文。
			out = append(out, redactTicketContextValueIn(item, parentKey))
		}
		return out
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, renderTicketContextScalar(redactTicketContextEntryIn("", item, parentKey)))
		}
		return out
	default:
		return value
	}
}

// redactTicketContextEntryIn 是带父键上下文的单条处理内核。
//
// 判定顺序（顺序即语义，不要调换）：
//  1. 键名命中**精确凭据键**（code / sms_code / otp …）→ 整段替换。
//     例外：父键属于错误/响应容器时，「裸 code」按错误码保留（见下）。
//  2. 键名命中**凭据子串**（password / token / captcha …）→ 整段替换。
//  3. 键名命中**手机号片段** → 只留后四位。
//  4. 否则递归处理值，并把当前键当作子树的父键。
func redactTicketContextEntryIn(key string, value interface{}, parentKey string) interface{} {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower != "" {
		if isTicketContextSecretExactKey(lower, parentKey) {
			return ticketContextRedacted
		}
		if containsAnyFragment(lower, ticketContextSecretKeyFragments) {
			return ticketContextRedacted
		}
		if containsAnyFragment(lower, ticketContextPhoneKeyFragments) {
			return maskTicketPhone(renderTicketContextScalar(value))
		}
	}
	return redactTicketContextValueIn(value, lower)
}

// isTicketContextSecretExactKey 判断精确键是否属于凭据。
//
// 唯一的例外是「裸 code」在错误上下文里：
//
//	{"code": "123456"}                      → 凭据（默认，安全侧）
//	{"error": {"code": "UAPI_TIMEOUT"}}     → 错误码（父键 error，保留）
//
// 注意这个例外**只对裸 code 生效**：{"error": {"sms_code": "123456"}} 里的
// sms_code 仍然脱敏 —— 错误容器里也可能夹带用户输入，不能因为父键就整片放行。
func isTicketContextSecretExactKey(lowerKey string, parentKey string) bool {
	if !containsExactKey(lowerKey, ticketContextSecretExactKeys) {
		return false
	}
	if lowerKey == "code" && containsExactKey(parentKey, ticketContextErrorScopeKeys) {
		return false
	}
	return true
}

// containsExactKey 判断 value 是否与列表中某一项**完全相等**（不是子串）。
func containsExactKey(value string, keys []string) bool {
	if value == "" {
		return false
	}
	for _, key := range keys {
		if value == key {
			return true
		}
	}
	return false
}

func containsAnyFragment(value string, fragments []string) bool {
	for _, fragment := range fragments {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}

// renderTicketContextScalar 把任意标量渲染成字符串（数字型手机号也要能脱敏）。
func renderTicketContextScalar(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(value)
	}
}

// maskTicketPhone 只保留后四位：13800001234 → ****1234。
// 短于等于 4 位的值直接整体隐藏（4 位以内没有「后四位」可供核对）。
func maskTicketPhone(value string) string {
	trimmed := strings.TrimSpace(value)
	runes := []rune(trimmed)
	if len(runes) <= 4 {
		if trimmed == "" {
			return ""
		}
		return ticketContextRedacted
	}
	return "****" + string(runes[len(runes)-4:])
}

// ListTicketsWithDB 列出工单。
//
// 可见性规则（45-*.md §0.23.2 Q69-2 定案，覆盖 69-*.md 的旧建议）：
//   - 普通用户：`user_id = ?`（**唯一**隔离维度；系统自动建给他的单同样可见，
//     别人的单——无论 origin——都看不到）；
//   - 管理员：可见全部，可按 origin / category 精确筛选。
func ListTicketsWithDB(db *gorm.DB, user *entity.User, query TicketListQuery, page, pageSize int) (TicketListResult, error) {
	if db == nil || user == nil {
		return TicketListResult{}, ErrTicketForbidden
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > TicketPageSizeMax {
		if pageSize > TicketPageSizeMax {
			pageSize = TicketPageSizeMax
		} else {
			pageSize = TicketPageSizeDefault
		}
	}
	base := db.Model(&entity.Ticket{})
	if !isTicketAdmin(user) {
		// 隔离**只靠 user_id**（45-*.md §0.23.2 / Q69-2：被涉及用户**可见**发给自己的
		// 系统单，原「保密」建议已被用户推翻）。因此这里**不加** `origin <> 'system'`：
		// 加了会把 Q26 自动建的赠金核对单从用户视野里彻底抹掉，
		// 用户只会觉得「我的赠金为什么没到账、也没人跟我说」。
		// 别人的系统单靠 user_id 天然看不到。
		base = base.Where("tickets.user_id = ?", user.ID)
	}
	status := strings.TrimSpace(query.Status)
	if status != "" {
		var err error
		status, err = normalizeTicketStatus(status)
		if err != nil {
			return TicketListResult{}, err
		}
		base = base.Where("tickets.status = ?", status)
	}
	category, err := normalizeTicketCategory(query.Category)
	if err != nil {
		return TicketListResult{}, err
	}
	if category != "" {
		base = base.Where("tickets.category = ?", category)
	}
	// origin 参数**先校验再决定是否应用**：非法值一律报错（静默忽略会让前端以为
	// 筛了、后端给出全量，是最难查的一类错）；但只对管理员应用 ——
	// 普通用户已经被上面钉死为「自己的、非系统单」，再叠加 origin 只会让语义混乱。
	origin, err := normalizeTicketOrigin(query.Origin)
	if err != nil {
		return TicketListResult{}, err
	}
	if origin != "" && isTicketAdmin(user) {
		base = base.Where("tickets.origin = ?", origin)
	}
	if condition, args := ticketKeywordCondition(query.Keyword); condition != "" {
		base = base.Where(condition, args...)
	}
	if query.CreatedFrom != nil {
		base = base.Where("tickets.created_at >= ?", query.CreatedFrom)
	}
	if query.CreatedTo != nil {
		base = base.Where("tickets.created_at < ?", query.CreatedTo)
	}
	if query.UpdatedFrom != nil {
		base = base.Where("tickets.updated_at >= ?", query.UpdatedFrom)
	}
	if query.UpdatedTo != nil {
		base = base.Where("tickets.updated_at < ?", query.UpdatedTo)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return TicketListResult{}, err
	}
	var tickets []entity.Ticket
	if err := base.Order("tickets.last_message_at DESC, tickets.id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&tickets).Error; err != nil {
		return TicketListResult{}, err
	}
	counts := make(map[int64]int64, len(tickets))
	if len(tickets) > 0 {
		type row struct {
			TicketID int64
			Count    int64
		}
		var rows []row
		if err := db.Model(&entity.TicketMessage{}).Select("ticket_id, COUNT(*) AS count").Where("ticket_id IN ?", ticketIDs(tickets)).Group("ticket_id").Scan(&rows).Error; err != nil {
			return TicketListResult{}, err
		}
		for _, item := range rows {
			counts[item.TicketID] = item.Count
		}
	}
	items := make([]TicketListItem, 0, len(tickets))
	// T-209：一次性反查本页里 category=refund 的壳工单对应的退款，
	// 避免逐行反查变成 N+1（管理台「全部工单」视图一页 100 行就是 100 次查询）。
	refundsByTicket := map[int64]entity.TicketRefund{}
	var refundTicketIDs []int64
	for _, ticket := range tickets {
		if strings.EqualFold(strings.TrimSpace(ticket.Category), entity.TicketCategoryRefund) {
			refundTicketIDs = append(refundTicketIDs, ticket.ID)
		}
	}
	if len(refundTicketIDs) > 0 {
		var refundRows []entity.TicketRefund
		if err := db.Session(&gorm.Session{NewDB: true}).
			Where("ticket_id IN ?", refundTicketIDs).Find(&refundRows).Error; err == nil {
			for _, row := range refundRows {
				if row.TicketID != nil {
					refundsByTicket[*row.TicketID] = row
				}
			}
		}
		// 反查失败不影响列表可用性：退款摘要缺失时前端只少一个徽标，
		// 列表本身仍然完整（判据是「退款出现在列表里」，靠 category 已保证）。
	}
	for _, ticket := range tickets {
		item := TicketListItem{ID: ticket.ID, UserID: ticket.UserID, UserName: ticket.UserName,
			Subject: ticket.Subject, Status: ticket.Status, Priority: ticket.Priority,
			Origin: ticket.Origin, Category: ticket.Category,
			LastMessageAt: ticket.LastMessageAt,
			ClosedAt:      ticket.ClosedAt, CreatedAt: ticket.CreatedAt, UpdatedAt: ticket.UpdatedAt, MessageCount: counts[ticket.ID]}
		if refund, ok := refundsByTicket[ticket.ID]; ok {
			item.Refund = refundSummaryOf(refund)
		}
		items = append(items, item)
	}
	return TicketListResult{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func ticketIDs(tickets []entity.Ticket) []int64 {
	ids := make([]int64, 0, len(tickets))
	for _, ticket := range tickets {
		ids = append(ids, ticket.ID)
	}
	return ids
}

func ticketKeywordCondition(keyword string) (string, []interface{}) {
	normalized := util.NormalizeSearchKeyword(keyword)
	if normalized == "" {
		return "", nil
	}
	mainCondition, args := util.BuildInstrCondition(normalized,
		"COALESCE(tickets.subject, '')", "COALESCE(tickets.user_name, '')")
	messageCondition, messageArgs := util.BuildInstrCondition(normalized, "COALESCE(ticket_messages.body, '')", "COALESCE(ticket_messages.author_name, '')")
	args = append(args, messageArgs...)
	parts := []string{mainCondition}
	if messageCondition != "" {
		parts = append(parts, "EXISTS (SELECT 1 FROM ticket_messages WHERE ticket_messages.ticket_id = tickets.id AND "+messageCondition+")")
	}
	userCondition, userArgs := util.BuildInstrCondition(normalized, "COALESCE(users.username, '')")
	if userCondition != "" {
		parts = append(parts, "EXISTS (SELECT 1 FROM users WHERE users.id = tickets.user_id AND "+userCondition+")")
		args = append(args, userArgs...)
	}
	if id, err := strconv.ParseInt(normalized, 10, 64); err == nil && id > 0 {
		parts = append(parts, "tickets.id = ?")
		args = append(args, id)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func GetTicketWithDB(db *gorm.DB, user *entity.User, id int64) (TicketDetail, error) {
	if db == nil || user == nil {
		return TicketDetail{}, ErrTicketForbidden
	}
	if id <= 0 {
		return TicketDetail{}, ErrTicketNotFound
	}
	query := db.Where("id = ?", id)
	if !isTicketAdmin(user) {
		// 同列表：隔离只靠 user_id（系统单对被涉及用户可见，45-*.md §0.23.2）。
		query = query.Where("user_id = ?", user.ID)
	}
	var ticket entity.Ticket
	if err := query.First(&ticket).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TicketDetail{}, ErrTicketNotFound
		}
		return TicketDetail{}, err
	}
	var messages []entity.TicketMessage
	if err := db.Where("ticket_id = ?", ticket.ID).Order("created_at ASC, id ASC").Find(&messages).Error; err != nil {
		return TicketDetail{}, err
	}
	detail := TicketDetail{Ticket: ticket, Messages: messages}
	attachments, err := ListTicketAttachmentsWithDB(db, ticket.ID)
	if err != nil {
		return TicketDetail{}, err
	}
	detail.Attachments = attachments
	if !isTicketAdmin(user) {
		return detail, nil
	}
	var notification entity.TicketNotification
	if err := db.Where("ticket_id = ?", ticket.ID).Order("id DESC").First(&notification).Error; err == nil {
		detail.Notification = &TicketNotificationInfo{ID: notification.ID, Status: notification.Status, AttemptCount: notification.AttemptCount, LastError: notification.LastError, SentAt: notification.SentAt}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TicketDetail{}, err
	}
	return detail, nil
}

func AddTicketMessageWithDB(db *gorm.DB, user *entity.User, id int64, input ReplyTicketInput) (TicketDetail, error) {
	if db == nil || user == nil {
		return TicketDetail{}, ErrTicketForbidden
	}
	body, err := normalizeTicketText(input.Body, TicketBodyMaxLength, ErrTicketBodyRequired)
	if err != nil {
		return TicketDetail{}, err
	}
	var ticket entity.Ticket
	now := time.Now()
	err = db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("id = ?", id)
		if !isTicketAdmin(user) {
			// 隔离只靠 user_id：系统单可见 **且可回复**（45-*.md §0.23.2 的
			// 「可见 ≠ 可回复」补充结论是「可回复」，但不能改 category / priority）。
			query = query.Where("user_id = ?", user.ID)
		}
		if err := query.First(&ticket).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTicketNotFound
			}
			return err
		}
		status := entity.TicketStatusPending
		if !isTicketAdmin(user) {
			status = entity.TicketStatusOpen
		}
		if input.Status != nil && isTicketAdmin(user) {
			status, err = normalizeTicketStatus(*input.Status)
			if err != nil {
				return err
			}
		}
		message := entity.TicketMessage{TicketID: ticket.ID, UserID: user.ID, AuthorName: ticketUserName(user), Body: body, AuthorRole: ticketAuthorRole(user), CreatedAt: now}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		if err := BindTicketAttachmentsInTx(tx, user, ticket.ID, message.ID, input.AttachmentIds); err != nil {
			return err
		}
		updates := map[string]interface{}{"status": status, "last_message_at": now, "updated_at": now}
		if status == entity.TicketStatusClosed {
			updates["closed_at"] = now
		} else {
			updates["closed_at"] = nil
		}
		if err := tx.Model(&entity.Ticket{}).Where("id = ?", ticket.ID).Updates(updates).Error; err != nil {
			return err
		}
		ticket.Status, ticket.LastMessageAt = status, now
		if !isTicketAdmin(user) {
			return enqueueTicketNotificationInTx(tx, ticket, body, "user_reply")
		}
		return nil
	})
	if err != nil {
		return TicketDetail{}, err
	}
	return GetTicketWithDB(db, user, id)
}

func UpdateTicketWithDB(db *gorm.DB, user *entity.User, id int64, input UpdateTicketInput) (TicketDetail, error) {
	if db == nil || user == nil || !isTicketAdmin(user) {
		return TicketDetail{}, ErrTicketForbidden
	}
	var ticket entity.Ticket
	query := db.Where("id = ?", id)
	if err := query.First(&ticket).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TicketDetail{}, ErrTicketNotFound
		}
		return TicketDetail{}, err
	}
	updates := make(map[string]interface{})
	if strings.TrimSpace(input.Status) != "" {
		status, err := normalizeTicketStatus(input.Status)
		if err != nil {
			return TicketDetail{}, err
		}
		updates["status"] = status
		if status == entity.TicketStatusClosed {
			now := time.Now()
			updates["closed_at"] = now
		} else {
			updates["closed_at"] = nil
		}
	}
	if strings.TrimSpace(input.Priority) != "" {
		// 这条路径本身已经被 isTicketAdmin 挡住，因此 urgent 在这里合法。
		priority, err := normalizeTicketPriority(input.Priority, true)
		if err != nil {
			return TicketDetail{}, err
		}
		updates["priority"] = priority
	}
	if len(updates) == 0 {
		return GetTicketWithDB(db, user, id)
	}
	updates["updated_at"] = time.Now()
	if err := db.Model(&entity.Ticket{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return TicketDetail{}, err
	}
	return GetTicketWithDB(db, user, id)
}

func ticketUserName(user *entity.User) string {
	if user == nil {
		return ""
	}
	if name := strings.TrimSpace(user.Nickname); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func ticketAuthorRole(user *entity.User) string {
	if isTicketAdmin(user) {
		return entity.TicketAuthorRoleAdmin
	}
	return entity.TicketAuthorRoleUser
}

// TicketWecomConfig is read from system_settings by the notification worker.
// The webhook URL is never included in ordinary ticket responses or logs.
type TicketWecomConfig struct {
	Enabled       bool
	WebhookURL    string
	MentionedList []string
}

func GetTicketWecomConfigWithDB(db *gorm.DB) (TicketWecomConfig, error) {
	if db == nil {
		return TicketWecomConfig{}, errors.New("数据库未初始化")
	}
	var settings []entity.SystemSetting
	if err := db.Where(clause.IN{Column: clause.Column{Name: "key"}, Values: []interface{}{TicketWecomEnabledSettingKey, TicketWecomWebhookURLSettingKey, TicketWecomMentionedListSettingKey}}).Find(&settings).Error; err != nil {
		return TicketWecomConfig{}, err
	}
	config := TicketWecomConfig{}
	for _, setting := range settings {
		switch setting.Key {
		case TicketWecomEnabledSettingKey:
			config.Enabled = strings.EqualFold(strings.TrimSpace(setting.Value), "true") || strings.TrimSpace(setting.Value) == "1"
		case TicketWecomWebhookURLSettingKey:
			config.WebhookURL = strings.TrimSpace(setting.Value)
		case TicketWecomMentionedListSettingKey:
			config.MentionedList = normalizeMentionedList(setting.Value)
		}
	}
	return config, nil
}

func normalizeMentionedList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || value == "null" {
		return nil
	}
	var list []string
	if strings.HasPrefix(value, "[") {
		_ = json.Unmarshal([]byte(value), &list)
	} else {
		for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == '，' || r == ';' || r == '；' }) {
			list = append(list, strings.TrimSpace(item))
		}
	}
	seen := make(map[string]struct{}, len(list))
	result := make([]string, 0, len(list))
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" || utf8.RuneCountInString(item) > 128 {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

// Persist the outbox event in the same transaction as its source message. The
// configuration decision is made in the same transaction so a disabled or
// malformed integration never creates an automatically retryable task.
func enqueueTicketNotificationInTx(tx *gorm.DB, ticket entity.Ticket, body, eventType string) error {
	content := buildTicketNotificationContent(ticket, body, eventType)
	now := time.Now()
	config, err := GetTicketWecomConfigWithDB(tx)
	if err != nil {
		return err
	}
	status, reason := ticketNotificationStatusForConfig(config)
	notification := entity.TicketNotification{TicketID: ticket.ID, EventType: eventType, Payload: content, Status: status}
	if status == ticketNotificationPending {
		notification.NextAttemptAt = &now
	} else {
		notification.LastError = reason
	}
	return tx.Create(&notification).Error
}

func QueueTicketNotificationWithDB(db *gorm.DB, ticketID int64, eventType, content string, mentionedList []string) error {
	if db == nil || ticketID <= 0 {
		return ErrTicketNotFound
	}
	mentioned, err := json.Marshal(normalizeMentionedList(strings.Join(mentionedList, ",")))
	if err != nil {
		return err
	}
	now := time.Now()
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		return err
	}
	status, reason := ticketNotificationStatusForConfig(config)
	notification := entity.TicketNotification{TicketID: ticketID, EventType: strings.TrimSpace(eventType), Payload: content, MentionedList: string(mentioned), Status: status}
	if status == ticketNotificationPending {
		notification.NextAttemptAt = &now
	} else {
		notification.LastError = reason
	}
	return db.Create(&notification).Error
}

func RetryTicketNotificationWithDB(db *gorm.DB, notificationID int64) error {
	if db == nil || notificationID <= 0 {
		return gorm.ErrRecordNotFound
	}
	var existing entity.TicketNotification
	if err := db.Select("status").First(&existing, notificationID).Error; err != nil {
		return err
	}
	if existing.Status != ticketNotificationFailed {
		return ErrTicketNotificationNotRetryable
	}
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		return err
	}
	if configErr := validateTicketWecomConfig(config); configErr != nil {
		return configErr
	}
	now := time.Now()
	result := db.Model(&entity.TicketNotification{}).Where("id = ? AND status = ?", notificationID, ticketNotificationFailed).Updates(map[string]interface{}{"status": ticketNotificationPending, "attempt_count": 0, "last_error": "", "next_attempt_at": now, "sent_at": nil})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrTicketNotificationNotRetryable
	}
	return nil
}

// ticketNotificationStatusForConfig is the enqueue policy. It deliberately
// returns skipped for every invalid configuration, including a missing system
// setting, so a later configuration change cannot silently replay old events.
func ticketNotificationStatusForConfig(config TicketWecomConfig) (string, string) {
	if err := validateTicketWecomConfig(config); err != nil {
		return ticketNotificationSkipped, err.Error()
	}
	return ticketNotificationPending, ""
}

func validateTicketWecomConfig(config TicketWecomConfig) error {
	if !config.Enabled || strings.TrimSpace(config.WebhookURL) == "" {
		return ErrTicketWebhookNotConfigured
	}
	if err := validateTicketWecomWebhookURL(config.WebhookURL); err != nil {
		return ErrTicketWebhookInvalid
	}
	return nil
}

func buildTicketNotificationContent(ticket entity.Ticket, body, eventType string) string {
	label := "新工单"
	if eventType == "user_reply" {
		label = "用户追问"
	}
	status := map[string]string{entity.TicketStatusOpen: "待处理", entity.TicketStatusPending: "处理中", entity.TicketStatusResolved: "已解决", entity.TicketStatusClosed: "已关闭"}[ticket.Status]
	if status == "" {
		status = "待处理"
	}
	header := fmt.Sprintf("## %s\n**工单 #%d**\n> **标题：** %s\n> **用户：** %s\n> **状态：** %s", label, ticket.ID, escapeTicketMarkdown(strings.Join(strings.Fields(ticket.Subject), " ")), escapeTicketMarkdown(strings.Join(strings.Fields(ticket.UserName), " ")), status)
	if !ticket.LastMessageAt.IsZero() {
		header += "\n> **时间：** " + ticket.LastMessageAt.Format("2006-01-02 15:04:05")
	}
	return header + "\n\n**内容：**\n" + escapeTicketMarkdown(body)
}

func escapeTicketMarkdown(value string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]", "#", "\\#").Replace(html.EscapeString(value))
}

const ticketMarkdownMaxBytes = 4096

func truncateTicketMarkdown(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "\n\n> 内容过长，已截断。"
	return truncateUTF8(value, maxBytes-len(suffix)) + suffix
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

type webhookError struct {
	err       error
	retryable bool
}

func (e *webhookError) Error() string { return e.err.Error() }
func (e *webhookError) Unwrap() error { return e.err }

var ticketWebhookHTTPClient = &http.Client{Timeout: 10 * time.Second}

func SendTicketWebhook(ctx context.Context, config TicketWecomConfig, content string, mentionedList []string) error {
	if strings.TrimSpace(config.WebhookURL) == "" || validateTicketWecomWebhookURL(config.WebhookURL) != nil {
		return &webhookError{err: ErrTicketWebhookInvalid, retryable: false}
	}
	parsed, _ := url.Parse(strings.TrimSpace(config.WebhookURL))
	mentioned := normalizeMentionedList(strings.Join(mentionedList, ","))
	tags := make([]string, 0, len(mentioned))
	for _, member := range mentioned {
		if strings.ContainsAny(member, "<>&\"' \t\r\n") {
			continue
		}
		if member == "@all" {
			member = "all"
		}
		tags = append(tags, "<@"+member+">")
	}
	suffix := ""
	if len(tags) > 0 {
		suffix = "\n\n" + strings.Join(tags, " ")
	}
	// Markdown mentions are part of content. Reserve their bytes before
	// truncating user details so neither UTF-8 characters nor tags are cut.
	if ticketMarkdownMaxBytes-len(suffix) < 128 {
		return &webhookError{err: errors.New("企业微信提醒成员列表过长"), retryable: false}
	}
	markdown := truncateTicketMarkdown(content, ticketMarkdownMaxBytes-len(suffix)) + suffix
	body, err := json.Marshal(map[string]interface{}{"msgtype": "markdown", "markdown": map[string]interface{}{"content": markdown}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), strings.NewReader(string(body)))
	if err != nil {
		return &webhookError{err: err, retryable: false}
	}
	req.Header.Set("Content-Type", "application/json")
	client := *ticketWebhookHTTPClient
	// A Webhook is a credential-bearing endpoint; never forward its request to
	// a redirect target, even when the initial URL passed the host allowlist.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return &webhookError{err: err, retryable: true}
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if readErr != nil {
		return &webhookError{err: fmt.Errorf("读取企业微信响应失败: %w", readErr), retryable: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &webhookError{err: fmt.Errorf("企业微信 Webhook HTTP %d: %s", resp.StatusCode, truncateUTF8(string(responseBody), 500)), retryable: resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500}
	}
	var result struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return &webhookError{err: fmt.Errorf("企业微信响应格式错误: %w", err), retryable: false}
	}
	if result.ErrCode == nil {
		return &webhookError{err: errors.New("企业微信响应缺少 errcode"), retryable: false}
	}
	if *result.ErrCode != 0 {
		retryable := *result.ErrCode == -1 || *result.ErrCode == 45009
		return &webhookError{err: fmt.Errorf("企业微信返回错误 %d: %s", *result.ErrCode, truncateUTF8(result.ErrMsg, 500)), retryable: retryable}
	}
	return nil
}

func ProcessPendingTicketNotifications(ctx context.Context, db *gorm.DB, now time.Time) (int, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	if ctx.Err() != nil {
		return 0, nil
	}
	db = db.WithContext(dbqueue.WithPriority(ctx, dbqueue.PriorityLow))
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		return 0, err
	}
	if err := validateTicketWecomConfig(config); err != nil {
		return 0, nil
	}
	var rows []entity.TicketNotification
	if err := db.Where("status = ? AND attempt_count < ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", ticketNotificationPending, TicketNotificationMaxAttempts, now).Order("id ASC").Limit(20).Find(&rows).Error; err != nil {
		return 0, err
	}
	processed := 0
	var firstError error
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		processed++
		if err := processTicketNotification(ctx, db, row, now); err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_notification_process_failed notificationId=%d ticketId=%d error=%v", row.ID, row.TicketID, err))
			if firstError == nil {
				firstError = err
			}
		}
	}
	return processed, firstError
}

// RecoverStaleTicketNotifications is intentionally explicit. It is called at
// worker startup and on a fixed recovery interval, rather than on every poll.
func RecoverStaleTicketNotifications(db *gorm.DB, now time.Time) (int64, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	recoveryBefore := now.Add(-2 * time.Minute)
	var affected int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var staleCount int64
		if err := tx.Model(&entity.TicketNotification{}).
			Where("status = ? AND updated_at < ?", ticketNotificationSending, recoveryBefore).
			Count(&staleCount).Error; err != nil {
			return err
		}
		if staleCount == 0 {
			return nil
		}
		result := tx.Model(&entity.TicketNotification{}).
			Where("status = ? AND updated_at < ?", ticketNotificationSending, recoveryBefore).
			Updates(map[string]interface{}{"status": ticketNotificationPending, "next_attempt_at": now, "updated_at": now})
		affected = result.RowsAffected
		return result.Error
	})
	return affected, err
}

// SkipPendingTicketNotifications marks legacy pending rows as skipped after a
// configuration is found invalid. New rows already use this policy at enqueue.
func SkipPendingTicketNotifications(db *gorm.DB, reason string, now time.Time) (int64, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	var affected int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		affected, err = skipPendingTicketNotificationsInTx(tx, reason, now)
		return err
	})
	return affected, err
}

func skipPendingTicketNotificationsInTx(tx *gorm.DB, reason string, now time.Time) (int64, error) {
	if tx == nil {
		return 0, errors.New("数据库未初始化")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = ErrTicketWebhookNotConfigured.Error()
	}
	var pendingCount int64
	if err := tx.Model(&entity.TicketNotification{}).Where("status = ?", ticketNotificationPending).Count(&pendingCount).Error; err != nil {
		return 0, err
	}
	if pendingCount == 0 {
		return 0, nil
	}
	result := tx.Model(&entity.TicketNotification{}).
		Where("status = ?", ticketNotificationPending).
		Updates(map[string]interface{}{"status": ticketNotificationSkipped, "last_error": reason, "next_attempt_at": nil, "updated_at": now})
	return result.RowsAffected, result.Error
}

func processTicketNotification(ctx context.Context, db *gorm.DB, row entity.TicketNotification, now time.Time) error {
	if ctx.Err() != nil {
		return nil
	}
	// Revalidate the selected snapshot. Another worker may already have tried
	// and requeued it; status alone would allow a stale worker to bypass backoff.
	now = time.Now()
	claimed := false
	if err := db.Transaction(func(tx *gorm.DB) error {
		claim := tx.Model(&entity.TicketNotification{}).
			Where("id = ? AND status = ? AND attempt_count = ? AND updated_at = ?", row.ID, ticketNotificationPending, row.AttemptCount, row.UpdatedAt).
			Where("next_attempt_at IS NULL OR next_attempt_at <= ?", now).
			Updates(map[string]interface{}{"status": ticketNotificationSending, "updated_at": now})
		if claim.Error != nil {
			return claim.Error
		}
		claimed = claim.RowsAffected > 0
		return nil
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if ctx.Err() != nil {
		return restoreCanceledTicketNotification(db, row.ID, now)
	}
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		if requeueErr := db.Model(&entity.TicketNotification{}).Where("id = ? AND status = ?", row.ID, ticketNotificationSending).
			Updates(map[string]interface{}{"status": ticketNotificationPending, "last_error": "读取企业微信配置失败", "next_attempt_at": now.Add(time.Second), "updated_at": now}).Error; requeueErr != nil {
			return requeueErr
		}
		return err
	}
	if configErr := validateTicketWecomConfig(config); configErr != nil {
		return db.Model(&entity.TicketNotification{}).Where("id = ? AND status = ?", row.ID, ticketNotificationSending).
			Updates(map[string]interface{}{"status": ticketNotificationSkipped, "last_error": configErr.Error(), "next_attempt_at": nil, "updated_at": now}).Error
	}
	mentioned := normalizeMentionedList(row.MentionedList)
	if strings.TrimSpace(row.MentionedList) == "" {
		mentioned = config.MentionedList
	}
	err = SendTicketWebhook(ctx, config, row.Payload, mentioned)
	now = time.Now()
	if err != nil && ctx.Err() != nil {
		return restoreCanceledTicketNotification(db, row.ID, now)
	}
	attempts := row.AttemptCount + 1
	if err == nil {
		return db.Model(&entity.TicketNotification{}).Where("id = ?", row.ID).Updates(map[string]interface{}{"status": ticketNotificationSent, "attempt_count": attempts, "last_error": "", "sent_at": now, "next_attempt_at": nil, "updated_at": now}).Error
	}
	var werr *webhookError
	retryable := errors.As(err, &werr) && werr.retryable
	redactedError := truncateUTF8(redactTicketWebhookError(err.Error(), config.WebhookURL), 1000)
	logger.SysWarn(fmt.Sprintf("ticket_notification_delivery_failed notificationId=%d ticketId=%d attempt=%d retryable=%t error=%s", row.ID, row.TicketID, attempts, retryable, redactedError))
	updates := map[string]interface{}{"attempt_count": attempts, "last_error": redactedError, "updated_at": now}
	if retryable && attempts < TicketNotificationMaxAttempts {
		next := now.Add(time.Duration(attempts) * time.Minute)
		updates["status"] = ticketNotificationPending
		updates["next_attempt_at"] = next
	} else {
		updates["status"] = ticketNotificationFailed
		updates["next_attempt_at"] = nil
	}
	return db.Model(&entity.TicketNotification{}).Where("id = ?", row.ID).Updates(updates).Error
}

func restoreCanceledTicketNotification(db *gorm.DB, id int64, now time.Time) error {
	cleanupContext := context.Background()
	if db != nil && db.Statement != nil && db.Statement.Context != nil {
		cleanupContext = context.WithoutCancel(db.Statement.Context)
	}
	db = db.WithContext(dbqueue.WithPriority(cleanupContext, dbqueue.PriorityLow))
	return db.Model(&entity.TicketNotification{}).Where("id = ? AND status = ?", id, ticketNotificationSending).
		Updates(map[string]interface{}{"status": ticketNotificationPending, "next_attempt_at": now, "updated_at": now}).Error
}

func redactTicketWebhookError(message, webhookURL string) string {
	message = strings.TrimSpace(message)
	if webhookURL != "" {
		message = strings.ReplaceAll(message, webhookURL, "[Webhook已隐藏]")
		if parsed, err := url.Parse(webhookURL); err == nil {
			if parsed.Host != "" {
				message = strings.ReplaceAll(message, parsed.Host, "[Webhook主机已隐藏]")
			}
			for _, key := range parsed.Query()["key"] {
				if key == "" {
					continue
				}
				message = strings.ReplaceAll(message, key, "[密钥已隐藏]")
				message = strings.ReplaceAll(message, url.QueryEscape(key), "[密钥已隐藏]")
			}
		}
	}
	return message
}

func StartTicketNotificationWorker(ctx context.Context, db *gorm.DB) <-chan struct{} {
	done := make(chan struct{})
	if db == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		if err := RunTicketNotificationWorker(ctx, db); err != nil && ctx.Err() == nil {
			logger.SysWarn(fmt.Sprintf("ticket_notification_worker_failed error=%v", err))
		}
	}()
	return done
}

const (
	ticketNotificationPollInterval     = 5 * time.Second
	ticketNotificationInvalidInterval  = 30 * time.Second
	ticketNotificationRecoveryInterval = 5 * time.Minute
)

func waitTicketNotificationWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// RunTicketNotificationWorker runs the outbox loop for the background worker
// goroutine. Database operations are short and the webhook call is always made
// after the claim transaction has committed.
func RunTicketNotificationWorker(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("数据库未初始化")
	}
	db = db.WithContext(dbqueue.WithPriority(ctx, dbqueue.PriorityLow))
	lastInvalidConfig := ""
	backoff := time.Second
	nextRecovery := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		now := time.Now()
		config, err := GetTicketWecomConfigWithDB(db)
		if err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_notification_config_read_failed error=%v", err))
			if !waitTicketNotificationWorker(ctx, backoff) {
				return nil
			}
			if backoff < time.Minute {
				backoff *= 2
				if backoff > time.Minute {
					backoff = time.Minute
				}
			}
			continue
		}
		configErr := validateTicketWecomConfig(config)
		if configErr != nil {
			state := fmt.Sprintf("%t|%s|%s", config.Enabled, config.WebhookURL, configErr.Error())
			if state != lastInvalidConfig {
				if _, skipErr := SkipPendingTicketNotifications(db, configErr.Error(), now); skipErr != nil {
					logger.SysWarn(fmt.Sprintf("ticket_notification_skip_invalid_config_failed error=%v", skipErr))
					if !waitTicketNotificationWorker(ctx, backoff) {
						return nil
					}
					if backoff < time.Minute {
						backoff *= 2
						if backoff > time.Minute {
							backoff = time.Minute
						}
					}
					continue
				}
				lastInvalidConfig = state
			}
			backoff = time.Second
			if !waitTicketNotificationWorker(ctx, ticketNotificationInvalidInterval) {
				return nil
			}
			continue
		}
		if nextRecovery.IsZero() || !now.Before(nextRecovery) {
			if _, err := RecoverStaleTicketNotifications(db, now); err != nil {
				logger.SysWarn(fmt.Sprintf("ticket_notification_recovery_failed error=%v", err))
				if !waitTicketNotificationWorker(ctx, backoff) {
					return nil
				}
				if backoff < time.Minute {
					backoff *= 2
					if backoff > time.Minute {
						backoff = time.Minute
					}
				}
				continue
			}
			nextRecovery = now.Add(ticketNotificationRecoveryInterval)
		}
		lastInvalidConfig = ""
		backoff = time.Second
		if _, err := ProcessPendingTicketNotifications(ctx, db, now); err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_notification_poll_failed error=%v", err))
			if !waitTicketNotificationWorker(ctx, backoff) {
				return nil
			}
			if backoff < time.Minute {
				backoff *= 2
				if backoff > time.Minute {
					backoff = time.Minute
				}
			}
			continue
		}
		if !waitTicketNotificationWorker(ctx, ticketNotificationPollInterval) {
			return nil
		}
	}
}
