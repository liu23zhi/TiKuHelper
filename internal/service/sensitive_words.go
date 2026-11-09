package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// SensitiveWordCheckEnabledSettingKey 是敏感词检测总开关，默认 **false**。
//
// 与其它商业化/增强开关一致：默认关闭，关闭时**完全不调外部 API、不写 sensitive 字段**。
// 缺表、缺行、值非法一律按关闭处理 —— 配置读不出来绝不能变成「悄悄开始计费外呼」。
const SensitiveWordCheckEnabledSettingKey = "sensitive_word_check_enabled"

// DefaultSensitiveWordCheckEnabled 默认关闭。
//
// 理由：检测会按次消耗 UApiPro 积分，且需求是「打标记」，不是答题必需能力。
// 未明确开启前不得自动生效。
const DefaultSensitiveWordCheckEnabled = false

// sensitiveWordMaxTextBytes 是**上游硬限制**：uapis.cn 敏感词检测接口的 `text`
// 字段最大 262144 字节（256 KiB），按 **UTF-8 字节**计，不是字符数。
//
// 实测（2026-11-09，真实密钥，见 task_doc/51-*.md）：
//   - text 262144 字节 → HTTP 200
//   - text 262145 字节 → HTTP 413 {"error":"Text exceeds maximum length of 256KB"}
//   - 中文 87300 字符（261900 B）→ 200；87400 字符（262200 B）→ 413
//     （证明按字节而非 rune：同样的字符数，字节数不同结果不同）
//
// 取 240 KiB 而非贴着 262144，留 ~8.6% 安全余量：
//   - 避免边界抖动：反向代理/网关可能对请求体另有限制，贴着上限容易 413；
//   - JSON 包装开销实测不计入该额度，但上游若改实现可能计入。
//
// ⚠️ 这里必须是**字节**预算。历史实现用 rune 上限（先 2000 后 1000），
// 两次都是错的：真实上限比它们大两个数量级，导致「题目被砍掉大半、后半段
// 敏感词全部漏检」而没有任何报错 —— 过度保守比直接报错更难被发现。
const sensitiveWordMaxTextBytes = 240 * 1024 // 245760

// 单次检测送检文本的字节预算。
//
// 按 **字节** 截断，并回退到完整 rune 边界（见 truncateSensitiveWordText）。
// 超长文本按预算截断，只为命中判定服务；原文一个字都不改。
// 两个预算都派生自 sensitiveWordMaxTextBytes，不要各自硬编码数字。
const (
	sensitiveWordQuestionMaxBytes = sensitiveWordMaxTextBytes
	sensitiveWordAnswerMaxBytes   = sensitiveWordMaxTextBytes
)

// 检测对象标识。落库时按 scope 分组，前端与 chaoxing 面板据此区分「题目命中」与「答案命中」。
const (
	SensitiveWordScopeQuestion = "question"
	SensitiveWordScopeAnswer   = "answer"
)

// ErrSensitiveWordNotConfigured 表示没有可用的检测通道（无密钥）。
var ErrSensitiveWordNotConfigured = errors.New("敏感词检测未配置 UAPI 密钥")

// SensitiveWordErrorTextTooLong 是送检文本超长失败原因的前缀。
//
// 单独一个稳定前缀，便于运维在 logs.sensitive_check_error 里 grep 出
// 「因为超长而没检测成」的行 —— 这类失败意味着字节预算需要复核，
// 与偶发的上游故障性质完全不同。
const SensitiveWordErrorTextTooLong = "text_too_long"

// SensitiveWordDetector 是「检测一段文本」的最小抽象。
//
// 抽成函数类型有两个目的：① 生产实现可以注入 httptest 服务器地址；
// ② 测试可以完全绕过网络，直接断言标记判定逻辑。
type SensitiveWordDetector func(ctx context.Context, text string) (uapi.ProfanityCheckResponse, error)

// SensitiveWordScopeResult 是一个检测对象的检测结果。
//
// 单独分组而不是合并成一个词表：需求要求「题目命中、答案命中、都命中 → 各自的标记」。
type SensitiveWordScopeResult struct {
	Scope     string   `json:"scope"`
	Status    string   `json:"status,omitempty"`
	Forbidden bool     `json:"forbidden"`
	Words     []string `json:"words"`
	// Error 只记录本次检测失败的原因；失败不等于命中（见 BuildSensitiveWordOutcome）。
	Error string `json:"error,omitempty"`
}

// SensitiveWordCheckOutcome 是一次完整检测要写回 logs 表的四个字段值。
type SensitiveWordCheckOutcome struct {
	Flag    bool
	Words   string
	Error   string
	Checked bool
	Results []SensitiveWordScopeResult
}

// GetSensitiveWordCheckEnabledWithDB 返回敏感词检测总开关，默认关闭。
//
// 缺表、缺记录、值非法都按关闭处理：读不到配置时不能外呼、不能计费。
func GetSensitiveWordCheckEnabledWithDB(db *gorm.DB) bool {
	return getBoolSystemSetting(db, SensitiveWordCheckEnabledSettingKey)
}

// IsSensitiveWordDetectorConfiguredWithDB 报告是否具备实际外呼条件（开关 + 可用密钥）。
//
// 只读判断，不构造客户端、不发请求、不暴露密钥。
func IsSensitiveWordDetectorConfiguredWithDB(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if !GetSensitiveWordCheckEnabledWithDB(db) {
		return false
	}
	return hasUsableUAPIKey(GetUAPIAPIKeyWithDB(db))
}

// SensitiveWordCheckStatus 供系统设置页展示「开关已开但没配密钥」这类不可用状态。
type SensitiveWordCheckStatus struct {
	Enabled    bool `json:"enabled"`
	Configured bool `json:"configured"`
}

// GetSensitiveWordCheckStatusWithDB 汇总开关与密钥可用性。
func GetSensitiveWordCheckStatusWithDB(db *gorm.DB) SensitiveWordCheckStatus {
	return SensitiveWordCheckStatus{
		Enabled:    GetSensitiveWordCheckEnabledWithDB(db),
		Configured: IsSensitiveWordDetectorConfiguredWithDB(db),
	}
}

// sensitiveWordCheckURL 覆盖检测端点。
//
// 生产恒为空（用 uapi.DefaultProfanityCheckURL）。存在的唯一理由：让
// 「开关关闭时到底有没有外呼」这类断言可以用本地 httptest 计数来证明，
// 而不是靠「结果为空」猜测 —— 空结果和「没发请求」看起来一样（AGENTS.md 第 21 条）。
var sensitiveWordCheckURL string

// SetSensitiveWordCheckURLForTesting 覆盖检测端点，返回恢复函数。
//
// 仅用于测试：让「开关关闭时是否真的没外呼」可以用本地 httptest 计数来证明。
// 生产代码不要调用 —— 端点由 uapi.DefaultProfanityCheckURL 决定。
func SetSensitiveWordCheckURLForTesting(address string) func() {
	previous := sensitiveWordCheckURL
	sensitiveWordCheckURL = strings.TrimSpace(address)
	return func() { sensitiveWordCheckURL = previous }
}

// BuildSensitiveWordDetectorWithDB 构造生产检测器。
//
// 返回 nil 表示**不可用**（开关关闭或没有可用密钥）：调用方据此完全不外呼。
// 复用 UAPIAPIKeySettingKey（uapis.cn），与图片 OCR / 联网搜索同一套鉴权，
// 不新增密钥配置。
func BuildSensitiveWordDetectorWithDB(db *gorm.DB) SensitiveWordDetector {
	if db == nil || !GetSensitiveWordCheckEnabledWithDB(db) {
		return nil
	}
	key := strings.TrimSpace(GetUAPIAPIKeyWithDB(db))
	if !hasUsableUAPIKey(key) {
		return nil
	}
	// SetMaxRetries(0)：提交型 POST 不重放（AGENTS.md 第 19 / 21 条），
	// 重试次数只保留客户端内部那一层，避免上层再叠加导致相乘。
	client := uapi.NewClient(key).SetMaxRetries(0)
	if override := strings.TrimSpace(sensitiveWordCheckURL); override != "" {
		client.SetProfanityCheckURL(override)
	}
	return func(ctx context.Context, text string) (uapi.ProfanityCheckResponse, error) {
		return client.CheckProfanity(ctx, uapi.ProfanityCheckRequest{Text: text})
	}
}

// hasUsableUAPIKey 判断密钥形态是否可用（与 OCR / 搜索工具同一判据）。
func hasUsableUAPIKey(key string) bool {
	trimmed := strings.TrimSpace(key)
	if !strings.HasPrefix(trimmed, "uapi-") || len(trimmed) <= len("uapi-") {
		return false
	}
	return !strings.ContainsFunc(trimmed, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// BuildSensitiveWordOutcome 把各对象的检测结果折叠成落库值。
//
// 判定规则（这是「只打标记不过滤」的核心）：
//
//	任一对象 status == "forbidden"        → Flag = true
//	所有对象都成功且都非 forbidden        → Flag = false（检测通过）
//	某对象失败、另一对象命中              → Flag = true（已确认的命中不会被失败掩盖）
//	某对象失败、没有对象命中              → Flag = false + Error 记录原因（宁漏标不误标）
//
// Words 始终是**全部对象的完整结果**（含未命中的），这样排障时能看出
// 「这次到底检测了什么、哪些通过了」，而不是只知道有没有命中。
func BuildSensitiveWordOutcome(results []SensitiveWordScopeResult) SensitiveWordCheckOutcome {
	outcome := SensitiveWordCheckOutcome{Results: results, Checked: true}
	payload, err := json.Marshal(results)
	if err != nil {
		// 序列化失败不影响标记本身：只丢明细，Flag 照常计算。
		outcome.Words = "[]"
	} else {
		outcome.Words = string(payload)
	}
	failures := make([]string, 0, len(results))
	for _, result := range results {
		if result.Forbidden {
			outcome.Flag = true
		}
		if message := strings.TrimSpace(result.Error); message != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", result.Scope, message))
		}
	}
	if len(failures) > 0 {
		outcome.Error = truncateSensitiveWordError(strings.Join(failures, "; "))
	}
	return outcome
}

// RunSensitiveWordCheck 依次检测题目与答案，返回可落库的结论。
//
// 空文本直接跳过（不发请求、不计积分），并在结果里标注 skipped。
// 任何单个对象的失败都被收敛进该对象的 Error，**不会**中断另一个对象的检测。
func RunSensitiveWordCheck(ctx context.Context, detector SensitiveWordDetector, question string, answer string) SensitiveWordCheckOutcome {
	scopes := []struct {
		scope    string
		text     string
		maxBytes int
	}{
		{scope: SensitiveWordScopeQuestion, text: strings.TrimSpace(question), maxBytes: sensitiveWordQuestionMaxBytes},
		{scope: SensitiveWordScopeAnswer, text: strings.TrimSpace(answer), maxBytes: sensitiveWordAnswerMaxBytes},
	}
	results := make([]SensitiveWordScopeResult, 0, len(scopes))
	for _, item := range scopes {
		results = append(results, checkSensitiveWordScope(ctx, detector, item.scope, item.text, item.maxBytes))
	}
	return BuildSensitiveWordOutcome(results)
}

func checkSensitiveWordScope(ctx context.Context, detector SensitiveWordDetector, scope string, text string, maxBytes int) SensitiveWordScopeResult {
	result := SensitiveWordScopeResult{Scope: scope, Words: []string{}}
	if text == "" {
		result.Status = "skipped_empty"
		return result
	}
	if detector == nil {
		result.Status = "skipped_unconfigured"
		result.Error = ErrSensitiveWordNotConfigured.Error()
		return result
	}
	response, err := detector(ctx, truncateSensitiveWordText(scope, text, maxBytes))
	if err != nil {
		result.Status = "error"
		// 超长是**可区分**的失败，不能与「上游挂了」混在一条错误里：
		// 前者说明我们的字节预算算错了（或上游改了限制），要立刻被人看见；
		// 后者是偶发故障，重试可能自愈。
		// 注意：413 同时满足 errors.Is(err, uapi.ErrUpstream)，所以这里用
		// ErrTextTooLong 优先判定，不影响其它错误的归类。
		if errors.Is(err, uapi.ErrTextTooLong) {
			result.Error = truncateSensitiveWordError(
				fmt.Sprintf("%s: 送检文本 %d 字节超出上游长度限制（预算 %d 字节）：%s",
					SensitiveWordErrorTextTooLong, len(text), maxBytes, err.Error()))
			return result
		}
		result.Error = truncateSensitiveWordError(err.Error())
		return result
	}
	result.Status = strings.TrimSpace(response.Status)
	result.Forbidden = response.Forbidden()
	if result.Forbidden {
		result.Words = normalizeSensitiveWordList(response.ForbiddenWords)
	}
	return result
}

// normalizeSensitiveWordList 去空白、去重并保序；nil 结果统一成空切片，
// 避免落库时出现 null 与 [] 两种表示。
func normalizeSensitiveWordList(words []string) []string {
	if len(words) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(words))
	normalized := make([]string, 0, len(words))
	for _, word := range words {
		trimmed := strings.TrimSpace(word)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

// truncateSensitiveWordText 按**字节预算**截断送检文本，并回退到完整 rune 边界。
//
// 只截断**送检副本**：题面与答案原文、返回给调用方的内容一个字都不改。
//
// 为什么按字节而不是 rune：上游限制是 262144 **字节**（UTF-8），同样的字符数
// 在中英文下字节数差 3 倍，用 rune 预算无法保证不越界（见 sensitiveWordMaxTextBytes 注释）。
//
// 为什么要回退 rune 边界：`text[:maxBytes]` 可能正好切在多字节字符中间，
// 产生非法 UTF-8 —— 送上游会被判 400，落库也会是坏字符串。这里逐字节回退到
// 最后一个完整字符的边界，保证输出始终是合法 UTF-8 前缀。
//
// 截断意味着**送检内容不完整**：超出预算的那部分文本根本没有被检测，里面的敏感词
// 会整段漏检。这是必须让人看见的事实，所以超长时打一条 WARN。
// 同时给出字节与 rune 两个维度：字节是上游口径（便于对账），
// rune 是人能理解的「砍了多少字」。
func truncateSensitiveWordText(scope string, text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	// 回退到 rune 起始字节：utf8.RuneStart 为 false 说明正处在某个字符的中间。
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	truncated := text[:cut]
	if scope == "" {
		scope = "unknown"
	}
	logger.SysWarn(fmt.Sprintf(
		"sensitive_word_text_truncated scope=%s originalBytes=%d originalRunes=%d limitBytes=%d truncatedBytes=%d droppedBytes=%d droppedRunes=%d",
		scope,
		len(text), utf8.RuneCountInString(text),
		maxBytes,
		len(truncated), len(text)-len(truncated),
		utf8.RuneCountInString(text)-utf8.RuneCountInString(truncated)))
	return truncated
}

// truncateSensitiveWordError 限制失败原因长度，与 logs.sensitive_check_error 的 varchar(255) 对齐。
func truncateSensitiveWordError(message string) string {
	const limit = 240
	runes := []rune(strings.TrimSpace(message))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-3]) + "..."
}

// ResolveSensitiveWordSettingInput 处理开关的「省略 = 保持现值」写入语义。
//
// requested 为 nil 表示调用方没有提交该字段：读库现值，读不到就是 false。
// 与 RechargePromotionEnabled / ReviewRebateEnabled 完全一致（AGENTS.md 第 22 条上下文）。
func ResolveSensitiveWordSettingInput(db *gorm.DB, requested *bool) bool {
	return resolveBoolSettingInput(db, requested, SensitiveWordCheckEnabledSettingKey)
}

// SensitiveWordLogColumns 列出本功能允许写入的列。
//
// 集中在一处便于审计：任何新增列都必须同时出现在这里、entity.LogSensitive
// 与启动迁移里，三者缺一不可。
func SensitiveWordLogColumns() []string {
	return []string{"sensitive_flag", "sensitive_words", "sensitive_checked_at", "sensitive_check_error"}
}

// SaveSensitiveWordOutcomeWithDB 把检测结论写回 logs 表的四个独立字段。
//
// 关键约束：
//   - **只改这四列**：description / old_answer / new_answer 等原有列一个字都不碰，
//     答案原文因此完整保留（需求：打标记，不过滤）；
//   - 用 map 而不是结构体，避免 GORM 把零值（Flag=false）当成「未赋值」跳过，
//     否则「检测通过」会与「从未检测」在库里无法区分；
//   - logID <= 0 直接放弃：logs 表没有 request_id 列，按 requestId 模糊匹配
//     会误更新同一秒的其它行（AGENTS.md 第 18b 条的同源教训）；
//   - 失败只返回错误给调用方记日志，绝不向上冒泡成答题失败。
func SaveSensitiveWordOutcomeWithDB(db *gorm.DB, logID int32, outcome SensitiveWordCheckOutcome, checkedAt time.Time) error {
	if db == nil {
		return errors.New("敏感词标记落库失败：数据库不可用")
	}
	if logID <= 0 {
		return errors.New("敏感词标记落库失败：缺少日志主键")
	}
	updates := map[string]any{
		"sensitive_flag":        outcome.Flag,
		"sensitive_words":       outcome.Words,
		"sensitive_checked_at":  checkedAt,
		"sensitive_check_error": strings.TrimSpace(outcome.Error),
	}
	// Session(NewDB) + 主键条件：不继承调用方可能残留的 Where，
	// 避免把「一次检测结果」写到多行上。
	result := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.LogSensitive{}).
		Where("id = ?", logID).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("敏感词标记落库失败：日志 %d 不存在", logID)
	}
	return nil
}
