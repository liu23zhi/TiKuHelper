package model

import (
	"context"
	"time"
)

// SearchRequest 搜题的请求体
//
// 注意：不再有 plat 字段。它历史取值恒为 -1、没有任何区分度，已彻底移除。
// 旧客户端若仍发送 "plat": -1，会被 JSON 解码**静默忽略**（本项目未启用
// DisallowUnknownFields），不会报错。
type SearchRequest struct {
	QID              string            `json:"qid"`
	Question         string            `json:"question"`
	Options          []string          `json:"options"`
	Type             int               `json:"type"`
	SkipCache        *bool             `json:"skipCache,omitempty"`
	DeepThinking     bool              `json:"deepThinking,omitempty"`
	QuestionEvidence *QuestionEvidence `json:"questionEvidence,omitempty"`
	RequestID        string            `json:"-"`
	// RequestContext belongs to synchronous server work, never client input.
	// Async task constructors leave it nil to keep their independent lifetime.
	RequestContext context.Context `json:"-" form:"-"`

	CourseName string `json:"courseName"`

	Extra string `json:"extra"`
}

// QuestionEvidence is optional structured image context from newer clients.
// Slice order follows the question DOM; alt is auxiliary evidence, not truth.
type QuestionEvidence struct {
	StemImages   []QuestionImageEvidence `json:"stemImages,omitempty"`
	OptionImages []QuestionImageEvidence `json:"optionImages,omitempty"`
}

type QuestionImageEvidence struct {
	URL   string `json:"url"`
	Alt   string `json:"alt"`
	Index int    `json:"index"`
}

// SearchResponse 搜题响应体
type SearchResponse struct {
	Question string       `json:"question"`
	Options  []string     `json:"options"`
	Type     int          `json:"type"`
	Answer   Answer       `json:"answer"` // 最相似，最可能的答案->即moreAnswer中出现最多的答案
	Error    *SearchError `json:"error,omitempty"`

	// Warnings 是面向调用方的非阻断提示，例如「AI 正在使用未配置价格的模型」。
	// 只在确有需要提醒的情况出现，正常响应不会带该字段，避免影响既有客户端。
	Warnings []string `json:"warnings,omitempty"`

	// Announcements 是**故障公告**（kind=incident），随每一次搜题响应下发给 PLUS。
	//
	// 为什么必须带在搜题响应里：PLUS 没有独立轮询通道，它只在用户搜题时与服务端
	// 交互。故障公告要送达考场页面，唯一的时机就是这一次响应（113 §1.1）。
	//
	// 三个不可协商的性质：
	//   - **只放 incident**：kind=normal 是「通知你一件事」，只在 TIKU WebUI 显示，
	//     下发到 PLUS 会让考生在考场弹出「系统维护通知」（113 §8.1）。
	//     过滤在 service.ActiveIncidents 的 SQL 里，本结构不负责筛选。
	//   - **失败必须降级为空**：公告是附加信息，搜题是主业务。读公告失败绝不能
	//     让搜题失败（接线点用 service.ActiveIncidentsOrEmpty，见 113 §4）。
	//   - **omitempty**：没有公告时不出现该键，既有客户端与既有断言零影响。
	Announcements []AnnouncementPayload `json:"announcements,omitempty"`

	// ServiceStatus 是**被动监测**推导出的当前服务状态（T-18 预留，本次恒为 nil）。
	//
	// 与 Announcements 语义独立、互不推导（112 §N4-f / 116 §N4-f 的裁定）：
	// 公告是人工发布，状态是自动推导；管理员发了 critical 公告也不会把
	// serviceStatus 强制改成 down —— 那是伪造数据。
	//
	// 用指针而不是值：nil 表示「本轮没有采集到状态」，序列化后该键整个不出现；
	// 用值类型则会把零值 `""` 或 "unknown" 发出去，客户端无法区分
	// 「服务健康」与「服务端还没实现这一项」。
	ServiceStatus *ServiceStatusPayload `json:"serviceStatus,omitempty"`
}

// AnnouncementPayload 是故障公告的**对外**投影。
//
// 刻意不复用 entity.Announcement：
//   - CreatedBy / CreatedAt / UpdatedAt 是内部审计字段，泄漏给 PLUS 没有用途，
//     还会把「哪个管理员发的」变成客户端可见信息；
//   - Enabled 不下发：能进这个列表的都是 enabled 且落在时间窗内的行，
//     再带一个恒为 true 的字段只会让人误以为客户端也要判它。
//
// 字段按 113 §3.1 的契约：ID / Title / Content / Severity，另加 Kind 与时间窗
// （客户端按 id 去重时需要，时间窗便于 PLUS 自己判断是否已过期）。
type AnnouncementPayload struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"` // 恒为 incident（保留字段以便客户端校验来源）
	Title    string `json:"title"`
	Content  string `json:"content"`
	Severity string `json:"severity"` // info / warning / critical

	// StartsAt / EndsAt 用 RFC3339（time.Time 的默认编码）。
	// EndsAt 为 nil 表示永久有效，与 entity 的语义一致。
	StartsAt time.Time  `json:"startsAt"`
	EndsAt   *time.Time `json:"endsAt,omitempty"`
}

// ServiceStatusPayload 是服务状态的对外投影（T-18 实现，本次仅占位）。
//
// 这里先定义结构而不填值，是为了让 T-18 只需往接线点塞对象、
// 不必再改一次响应结构（改结构会再动一遍客户端契约）。
type ServiceStatusPayload struct {
	Status string `json:"status"` // healthy / degraded / down / unknown
}

// SearchError only carries public diagnostics; private upstream details stay in audits.
type SearchError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Answer 答案 该实体只会增加而不会减少
type Answer struct {
	AnswerKey     []string `json:"answerKey"`     // 如果传了选项可以格式化为ABCD
	AnswerKeyText string   `json:"answerKeyText"` // 答案ABCD文本 分隔符默认是 #

	AnswerIndex []int  `json:"answerIndex"` // 如果用户传了options 将会自动帮用户计算出答案的角标，依据Answer来计算的
	AnswerText  string `json:"answerText"`  // 答案文本 分隔符默认是 #

	BestAnswer []string   `json:"bestAnswer"` // 最有可能的答案
	AllAnswer  [][]string `json:"allAnswer"`  // 所有接口聚合的答案
}

// Question 最简单的通用问题答案模型
type Question struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Type     uint     `json:"type"`
	Answer   []string `json:"answer"`
}
