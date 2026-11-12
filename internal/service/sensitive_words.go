package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
// 取 240 KiB 而非贴着 262144，留 6.25% 安全余量：
//   - 避免边界抖动：反向代理/网关可能对请求体另有限制，贴着上限容易 413；
//   - JSON 包装开销实测不计入该额度，但上游若改实现可能计入。
//
// 余量算式（可复算）：
//
//	预算        240*1024 = 245760 字节
//	上游硬上限  256*1024 = 262144 字节
//	差额        262144 - 245760 = 16384 字节
//	余量比例    16384 / 262144 = 6.25%
//
// （此前注释写「~8.6%」是错的：那是按 240/262 的倒数估出来的，
//  正确口径是差额除以**上游上限**。）
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
// 而不是靠「结果为空」猜测 —— 空结果和「没发请求」看起来一样。
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
	// SetMaxRetries(0)：提交型 POST 不重放，
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

// 缓存参数。
const (
	// sensitiveWordCacheMaxAttempts 是同一文本允许的**失败**次数上限。
	//
	// 达到上限后不再重试，直接把上次的失败原因返回：上游抖动不该被固化成
	// 永远失败（所以允许重试），但也不能无限重试白烧 API 额度（所以有上限）。
	// 成功一次即把 AttemptCount 归零，语义是「连续失败次数」。
	sensitiveWordCacheMaxAttempts = 3

	// sensitiveWordCacheProfile 是哈希域分隔前缀。
	//
	// 换判定口径（例如改用不同的检测端点/参数）时改这个值，等于整体换一套键，
	// 避免新旧语义混用同一批缓存行。
	sensitiveWordCacheProfile = "uapi-profanity-plain-v1"
)

// sensitiveWordCachePayload 是一个文本的检测结论（与 scope 无关）。
//
// 题目与答案是两个 scope，但**同一段文本的检测结论只有一个**；
// 用这个结构承载结论，让「同文本只处理一次」与「按 scope 分组落库」分开。
type sensitiveWordCachePayload struct {
	Status    string
	Forbidden bool
	Words     []string
	Error     string
}

// hashSensitiveWordText 计算缓存主键：sha256(profile + NUL + 送检文本) 的 hex。
//
// 主键用哈希而不是原文：原文可能上千字且是题库内容，直接当主键会膨胀索引；
// 哈希只压缩表示，不改变相等语义（同一文本 ↔ 同一 hash）。
func hashSensitiveWordText(text string) string {
	sum := sha256.Sum256([]byte(sensitiveWordCacheProfile + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

// sensitiveWordCacheDB 派生一个**低优先级**的缓存访问会话。
//
// 缓存是旁路加速，绝不能和答题/计费抢数据库准入名额 —— 与 controller 层
// withDatabasePriority(..., dbqueue.PriorityLow) 的做法同源。
//
// ⚠️ NewDB + Context 必须写在**同一次** Session 调用里：
// 分开写（先 NewDB 再 WithContext）会让新会话继承上一层残留的 Statement.Model。
// 生产路径的 db 来自 dao.Log.UnderlyingDB()，它自带 logs 模型，
// 继承下来就会把缓存查询渲染成 `SELECT logs.text_hash ... FROM logs`。
// 调用方另外还会显式 .Model(&entity.SensitiveWordCache{})，双保险。
func sensitiveWordCacheDB(db *gorm.DB, ctx context.Context) *gorm.DB {
	if db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return db.Session(&gorm.Session{
		Context: dbqueue.WithPriority(ctx, dbqueue.PriorityLow),
		NewDB:   true,
	})
}

// loadSensitiveWordCacheWithDB 读取一个文本的缓存行。
//
// **fail-open**：db 为空、表不存在、查询报错，一律返回未命中，
// 让调用方照常外呼。缓存是纯加速结构，绝不能因为它坏了就阻断检测。
func loadSensitiveWordCacheWithDB(ctx context.Context, db *gorm.DB, textHash string) (*entity.SensitiveWordCache, bool) {
	session := sensitiveWordCacheDB(db, ctx)
	if session == nil || textHash == "" {
		return nil, false
	}
	var row entity.SensitiveWordCache
	result := session.Model(&entity.SensitiveWordCache{}).
		Where("text_hash = ?", textHash).
		Limit(1).
		Find(&row)
	if result.Error != nil {
		// 缺表是最常见的「还没迁移」场景，不该刷 warning；只有表在、查询却失败
		// 才是真异常。判定方式：再问一次 HasTable，把两种情况分开记。
		if !db.Migrator().HasTable(entity.TableNameSensitiveWordCache) {
			logger.SysLog("敏感词缓存表不存在，本次按未命中处理（不影响答题）")
		} else {
			logger.SysWarn(fmt.Sprintf("敏感词缓存查询失败，本次按未命中处理：%s",
				logger.RedactSecrets(result.Error.Error())))
		}
		return nil, false
	}
	if result.RowsAffected == 0 {
		return nil, false
	}
	return &row, true
}

// saveSensitiveWordCacheWithDB 写入/覆盖一个文本的检测结论。
//
// 成功与失败都写：成功写结论，失败写 Error + AttemptCount+1（供上限判定）。
// **fail-open**：写失败只记日志，检测结果照常返回给调用方。
func saveSensitiveWordCacheWithDB(ctx context.Context, db *gorm.DB, row entity.SensitiveWordCache) {
	session := sensitiveWordCacheDB(db, ctx)
	if session == nil {
		return
	}
	row.CheckedAt = time.Now()
	err := session.Model(&entity.SensitiveWordCache{}).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "text_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"sensitive", "words", "masked_text", "api_status", "error", "attempt_count", "checked_at",
		}),
	}).Create(&row).Error
	if err != nil {
		logger.SysLog(fmt.Sprintf("敏感词缓存写入失败，检测结果照常使用：%s", logger.RedactSecrets(err.Error())))
	}
}

// decodeSensitiveWordCacheWords 把缓存里的命中词 JSON 还原成切片。
//
// 解析失败按空切片处理：命中明细只是取证信息，不能因为它坏了就否认命中结论。
func decodeSensitiveWordCacheWords(payload string) []string {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" || trimmed == "[]" {
		return []string{}
	}
	var words []string
	if err := json.Unmarshal([]byte(trimmed), &words); err != nil {
		return []string{}
	}
	return normalizeSensitiveWordList(words)
}

// truncateSensitiveWordCacheStatus 把上游 status 收进 api_status 的宽度（varchar(32)）。
func truncateSensitiveWordCacheStatus(status string) string {
	const limit = 32
	trimmed := strings.TrimSpace(status)
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return string(runes[:limit])
}

// resolveSensitiveWordPayloadWithDB 取一个文本的检测结论：先查缓存，未命中才外呼。
//
// 决策表（与需求逐条对应）：
//
//	命中 且 Error == ""                → 直接用，零外呼
//	命中 且 Error != "" 且 Attempt < 3 → 重试外呼
//	命中 且 Error != "" 且 Attempt >= 3 → 按失败处理，不再外呼
//	未命中 / 缓存不可用                → 外呼，然后写缓存（成功失败都写）
//
// memo 是**单次调用内**的 memo：题目与答案文本相同时 hash 相同，直接复用，
// 既不重复查库也不重复外呼（需求第 4 条）。
//
// ⚠️ 截断在**这里**做，不在调用方：缓存键必须基于截断后的送检文本，
// 而截断规则是按**字节**预算（sensitiveWordMaxTextBytes，见 truncateSensitiveWordText）。
// 若在调用方截断后再传进来，键的语义会随调用方是否记得截断而漂移。
func resolveSensitiveWordPayloadWithDB(ctx context.Context,
	db *gorm.DB,
	detector SensitiveWordDetector,
	scope string,
	rawText string,
	maxBytes int,
	memo map[string]sensitiveWordCachePayload,
) sensitiveWordCachePayload {
	// 先截断，再算键：键对应「真正送给上游的那段文本」。
	submitted := truncateSensitiveWordText(scope, rawText, maxBytes)
	textHash := hashSensitiveWordText(submitted)
	// memo 允许为 nil（一次性调用方只关心结论，不需要单次调用内复用）。
	// 这里补一个本地 map 而不是在写回处判空：写回点分散在四个分支，
	// 判空很容易漏一处 → nil map 赋值 panic，且 panic 发生在旁路 goroutine 里。
	if memo == nil {
		memo = make(map[string]sensitiveWordCachePayload, 1)
	}
	if payload, ok := memo[textHash]; ok {
		return payload
	}

	row, hit := loadSensitiveWordCacheWithDB(ctx, db, textHash)
	attempts := 0
	if hit {
		attempts = row.AttemptCount
		if strings.TrimSpace(row.Error) == "" {
			payload := sensitiveWordCachePayload{
				Status:    row.APIStatus,
				Forbidden: row.Sensitive,
				Words:     decodeSensitiveWordCacheWords(row.Words),
			}
			memo[textHash] = payload
			return payload
		}
		if attempts >= sensitiveWordCacheMaxAttempts {
			// 已知失败且已达上限：按失败返回，不再为同一文本花钱。
			payload := sensitiveWordCachePayload{
				Status: "error",
				Words:  []string{},
				Error:  truncateSensitiveWordError(row.Error),
			}
			memo[textHash] = payload
			return payload
		}
	}

	response, err := detector(ctx, submitted)
	if err != nil {
		// 超长是**可区分**的失败，不能与「上游挂了」混在一条错误里：
		// 前者说明我们的字节预算算错了（或上游改了限制），要立刻被人看见；
		// 后者是偶发故障，重试可能自愈。
		// 注意：413 同时满足 errors.Is(err, uapi.ErrUpstream)，所以这里用
		// ErrTextTooLong 优先判定，不影响其它错误的归类。
		//
		// 超长**不写缓存**：它不是「这段文本的结论」，而是「我们的预算算错了」。
		// 固化进缓存会让同一文本在预算修好之后仍然拿不到结果，而且会白白占掉
		// AttemptCount 的重试额度 —— 预算修正属于代码变更，不该由缓存行来记。
		if errors.Is(err, uapi.ErrTextTooLong) {
			message := truncateSensitiveWordError(
				fmt.Sprintf("%s: 送检文本 %d 字节超出上游长度限制（预算 %d 字节）：%s",
					SensitiveWordErrorTextTooLong, len(rawText), maxBytes, err.Error()))
			payload := sensitiveWordCachePayload{Status: "error", Words: []string{}, Error: message}
			memo[textHash] = payload
			return payload
		}
		message := truncateSensitiveWordError(err.Error())
		saveSensitiveWordCacheWithDB(ctx, db, entity.SensitiveWordCache{
			TextHash:     textHash,
			Sensitive:    false,
			Words:        "[]",
			APIStatus:    "",
			Error:        message,
			AttemptCount: attempts + 1,
		})
		payload := sensitiveWordCachePayload{Status: "error", Words: []string{}, Error: message}
		memo[textHash] = payload
		return payload
	}

	status := strings.TrimSpace(response.Status)
	forbidden := response.Forbidden()
	words := []string{}
	if forbidden {
		words = normalizeSensitiveWordList(response.ForbiddenWords)
	}
	encodedWords := "[]"
	if payload, marshalErr := json.Marshal(words); marshalErr == nil {
		encodedWords = string(payload)
	}
	cacheError := ""
	if !forbidden && !isTrustworthySensitiveWordStatus(status) {
		// 关键：这里**不能**当成「判定为干净」写进缓存。
		//
		// 上游 200 但不给 status（或给未知 status）是「结论不可知」，不是「干净」。
		// 不缓存它的坏处是同一文本再外呼一次；缓存它的坏处却是永久的——
		// 一行 status 空、error 空、sensitive=false 的缓存，会命中「命中 且 Error == ""」
		// 分支被无限复用，等于把一次上游异常固化成「这段文本永远干净」
		// （写入必须带可用性判据，空结果不能进缓存）。
		// 走失败语义：Error 非空 + AttemptCount 递增，AttemptCount 到上限后自然停止重试。
		cacheError = fmt.Sprintf("上游未给出可判定状态 status=%q", status)
		if cacheError = truncateSensitiveWordError(cacheError); cacheError == "" {
			cacheError = "上游未给出可判定状态"
		}
	}
	saveSensitiveWordCacheWithDB(ctx, db, entity.SensitiveWordCache{
		TextHash:     textHash,
		Sensitive:    forbidden,
		Words:        encodedWords,
		MaskedText:   response.MaskedText,
		APIStatus:    truncateSensitiveWordCacheStatus(status),
		Error:        cacheError,
		AttemptCount: 0,
	})
	// 注意：缓存侧的失败**不影响**本次返回的结论。本次响应是上游真实给的，
	// 该怎样就怎样返回，只是「不予缓存」。
	payload := sensitiveWordCachePayload{Status: status, Forbidden: forbidden, Words: words}
	memo[textHash] = payload
	return payload
}

// isTrustworthySensitiveWordStatus 判断上游 status 是否给出**可复用的确定结论**。
//
// 只有明确表示「查过了」的状态才允许进缓存：未命中（clean/ok/pass）与命中。
// 其余（空串、pending、failed、未知枚举）都是「不知道」，不能固化成缓存。
func isTrustworthySensitiveWordStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "clean", "ok", "pass", "passed", "normal", "safe", "forbidden", "blocked", "rejected", "hit", "sensitive":
		return true
	}
	return false
}

// RunSensitiveWordCheck 依次检测题目与答案，返回可落库的结论。
//
// 空文本直接跳过（不发请求、不计积分），并在结果里标注 skipped。
// 任何单个对象的失败都被收敛进该对象的 Error，**不会**中断另一个对象的检测。
//
// 无 db 时退化为「不用缓存」：这是纯函数路径，测试与一次性调用方用它。
// 生产路径用 RunSensitiveWordCheckWithDB 才会读写缓存表。
func RunSensitiveWordCheck(ctx context.Context, detector SensitiveWordDetector, question string, answer string) SensitiveWordCheckOutcome {
	return RunSensitiveWordCheckWithDB(ctx, nil, detector, question, answer)
}

// RunSensitiveWordCheckWithDB 是带缓存去重的版本。
//
// db 用于读写 sensitive_word_cache（调用方应传低优先级会话）：
// 同一段文本只外呼一次，之后所有请求（不限用户、不限题目）都复用结论，
// 避免「同一题被 100 人搜 = 200 次 uapis 调用」。
func RunSensitiveWordCheckWithDB(ctx context.Context, db *gorm.DB, detector SensitiveWordDetector, question string, answer string) SensitiveWordCheckOutcome {
	scopes := []struct {
		scope    string
		text     string
		maxBytes int
	}{
		{scope: SensitiveWordScopeQuestion, text: strings.TrimSpace(question), maxBytes: sensitiveWordQuestionMaxBytes},
		{scope: SensitiveWordScopeAnswer, text: strings.TrimSpace(answer), maxBytes: sensitiveWordAnswerMaxBytes},
	}
	results := make([]SensitiveWordScopeResult, 0, len(scopes))
	// 题目与答案文本相同时共用同一份结论，因此 memo 在本次调用的所有 scope 间共享。
	memo := make(map[string]sensitiveWordCachePayload, len(scopes))
	for _, item := range scopes {
		results = append(results, checkSensitiveWordScope(ctx, db, detector, item.scope, item.text, item.maxBytes, memo))
	}
	return BuildSensitiveWordOutcome(results)
}

func checkSensitiveWordScope(ctx context.Context, db *gorm.DB, detector SensitiveWordDetector, scope string, text string, maxBytes int, memo map[string]sensitiveWordCachePayload) SensitiveWordScopeResult {
	result := SensitiveWordScopeResult{Scope: scope, Words: []string{}}
	if text == "" {
		// 空文本既不查缓存也不外呼：没有可判定的内容。
		result.Status = "skipped_empty"
		return result
	}
	if detector == nil {
		// 没有检测通道（无密钥）时不做任何缓存读写：缓存里也不该有它的结论。
		result.Status = "skipped_unconfigured"
		result.Error = ErrSensitiveWordNotConfigured.Error()
		return result
	}
	// 截断在 resolve 内部做（按**字节**预算，见 truncateSensitiveWordText），
	// 因为缓存键必须基于**截断后**的文本计算 —— 与真正送给上游的内容严格一致。
	payload := resolveSensitiveWordPayloadWithDB(ctx, db, detector, scope, text, maxBytes, memo)
	result.Status = payload.Status
	result.Forbidden = payload.Forbidden
	if len(payload.Words) > 0 {
		result.Words = payload.Words
	}
	if strings.TrimSpace(payload.Error) != "" {
		result.Error = truncateSensitiveWordError(payload.Error)
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
// 与 RechargePromotionEnabled / ReviewRebateEnabled 完全一致（都是「省略 = 保持现值」的设置语义）。
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
//     会误更新同一秒的其它行；
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
