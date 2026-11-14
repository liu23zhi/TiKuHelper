package service

import (
	"errors"
	"strings"
	"sync"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 手机号 / 微信黑名单判定入口（46-黑名单设计.md §1.2 §3.1 §3.4，BL-3）。
//
// 为什么单独抽一个 gate 而不是在每条业务链路里各写一遍判定：本仓已经因为
// 「同一语义两份实现」踩过坑（AGENTS.md 第 13 条）。黑名单判定有 4 个分支
// （开关关 / 快照缺失 fail-open / 命中 / 未命中），每处抄一遍必然漏掉其中一个
// ——漏掉 fail-open 就是「数据库抖动时注册页全 500」，漏掉留痕就是
// 「规则失效无人知」。
//
// 语义与 IP 中间件（internal/middleware/blacklist.go）**逐条对齐**：
//
//	开关 blacklist_enabled=false（默认） → 零查询、零写日志、直接放行
//	开关开 + 快照不可用                 → **fail-open 放行** + 写 action='logged_only' 留痕
//	命中                                → 写 action='blocked' 并返回命中
//	未命中                              → 放行，不写任何行
//
// 与 IP 中间件的唯一区别是匹配键：IP 走 c.ClientIP()，这里走**归一化后的手机号**
// 或 **TrimSpace 后的微信 OpenID（大小写敏感）**。
//
// ⚠️ 本文件**不**负责写响应。命中后如何措辞由调用方决定（控制器写 403 JSON，
// 且只回 reason、绝不回 Pattern —— 46-*.md §3.2）。

// 命中留痕时记录的请求路径。
//
// 微信登录在上游解析出 OpenID 之后才知道要不要拦，而那一步发生在 service 层，
// 拿不到 gin.Context。因此这里用**固定的路径标签**而不是真实 URL：
// 命中记录的目的是让运营能解释「他在哪一步被拦」，标签足够，且不会因为
// 上游回调地址变化而失真。
const (
	BlacklistGatePathWechatLogin  = "/api/public/wechat-login"
	BlacklistGatePathWechatBind   = "/api/public/wechat-login/bind"
	BlacklistGatePathUserWechat   = "/api/user/wechat"
	BlacklistGatePathUserWechatBy = "/api/user/wechat/bind-code"
)

// BlacklistBlockedFallbackReason 是规则没填 reason 时的兜底文案。
//
// 不能回空字符串：用户看到空 message 会以为接口坏了。
const BlacklistBlockedFallbackReason = "当前访问受限，如有疑问请联系管理员"

// BlacklistGateMeta 是命中留痕需要的请求上下文（都不参与判定）。
type BlacklistGateMeta struct {
	// ClientIP 是客户端 IP，写进 blacklist_hits.client_ip 便于运营排查。
	ClientIP string
	// Path 是请求路径（或上面的固定标签），写进 blacklist_hits.path。
	Path string
}

// ErrBlacklistValueBlocked 是「手机号/微信命中黑名单」的哨兵错误。
//
// 用错误而不是布尔返回值串到 service 层调用方（微信登录/绑定）：那些函数的
// 调用链很深，多返回值会传染到每一层；用错误可以在最外层统一映射成 403，
// 同时 errors.Is 让调用方不必依赖具体文案。
var ErrBlacklistValueBlocked = errors.New(BlacklistBlockedFallbackReason)

// BlacklistBlockedError 携带命中信息（原因、脱敏值），供控制器回填人类可读文案。
//
// 它**只**携带 BlacklistMatch —— 里面没有 Pattern，因此把它一路传上去也不可能
// 泄漏规则边界（46-*.md §3.2）。
type BlacklistBlockedError struct {
	Match BlacklistMatch
}

func (e *BlacklistBlockedError) Error() string {
	if e == nil {
		return ErrBlacklistValueBlocked.Error()
	}
	return BlacklistBlockedMessage(e.Match)
}

func (e *BlacklistBlockedError) Unwrap() error { return ErrBlacklistValueBlocked }

// NewBlacklistBlockedError 构造一个命中错误（service 内部各入口用）。
func NewBlacklistBlockedError(match BlacklistMatch) error {
	return &BlacklistBlockedError{Match: match}
}

// ResolveBlacklistBlocked 从错误链里取出命中信息。
//
// 返回 ok=false 表示这个错误与黑名单无关，调用方按原有分支继续处理。
func ResolveBlacklistBlocked(err error) (BlacklistMatch, bool) {
	var blocked *BlacklistBlockedError
	if errors.As(err, &blocked) && blocked != nil {
		return blocked.Match, true
	}
	return BlacklistMatch{}, false
}

// BlacklistBlockedMessage 返回给被拦用户看的文案：**只回 reason**。
//
// reason 是运营写给用户看的话；Pattern 是规则边界，泄漏它等于告诉攻击者
// 精确的绕过方式（46-*.md §3.2 / 45-*.md §0.1.1）。
func BlacklistBlockedMessage(match BlacklistMatch) string {
	if reason := strings.TrimSpace(match.Reason); reason != "" {
		return reason
	}
	return BlacklistBlockedFallbackReason
}

// BlacklistPhoneGate 判定手机号是否命中黑名单（注册 / 短信登录 / 换绑新号）。
//
// 返回值 hit=false 表示「放行」，包括开关关闭、值非法、未命中、fail-open 四种情况。
// 四种情况都返回 false 是有意的：黑名单是 fail-open 的闸门（46-*.md §3.4），
// 「放行」只有一种结果，调用方不需要区分。
func BlacklistPhoneGate(db *gorm.DB, rawPhone string, meta BlacklistGateMeta) (BlacklistMatch, bool) {
	return BlacklistPhoneGateWith(DefaultBlacklistCache(), DefaultBlacklistGateHitRecorder(), db, rawPhone, meta)
}

// BlacklistWechatGate 判定微信 OpenID 是否命中黑名单（微信登录 / 注册 / 绑定换绑）。
//
// 归一化只 TrimSpace、**不转小写**：OpenID 大小写敏感（46-*.md §1.2）。
func BlacklistWechatGate(db *gorm.DB, rawWechatID string, meta BlacklistGateMeta) (BlacklistMatch, bool) {
	return BlacklistWechatGateWith(DefaultBlacklistCache(), DefaultBlacklistGateHitRecorder(), db, rawWechatID, meta)
}

// BlacklistPhoneGateWith 返回使用指定缓存/聚合器的判定（测试与显式装配用）。
func BlacklistPhoneGateWith(cache *BlacklistCache, recorder *BlacklistHitRecorder, db *gorm.DB, rawPhone string, meta BlacklistGateMeta) (BlacklistMatch, bool) {
	return blacklistValueGate(cache, recorder, db, BlacklistKindPhone, rawPhone, meta)
}

// BlacklistWechatGateWith 同 BlacklistPhoneGateWith，匹配键是微信 OpenID。
func BlacklistWechatGateWith(cache *BlacklistCache, recorder *BlacklistHitRecorder, db *gorm.DB, rawWechatID string, meta BlacklistGateMeta) (BlacklistMatch, bool) {
	return blacklistValueGate(cache, recorder, db, BlacklistKindWechat, rawWechatID, meta)
}

// blacklistValueGate 是手机号 / 微信共用的判定主体。
func blacklistValueGate(cache *BlacklistCache, recorder *BlacklistHitRecorder, db *gorm.DB, kind string, raw string, meta BlacklistGateMeta) (BlacklistMatch, bool) {
	// 第一道闸：总开关。关闭（默认）时**零查询、零写日志**，
	// 全站行为与引入黑名单前完全一致（46-*.md 红线与 §7 验收第 1 条）。
	if cache == nil || !cache.Enabled() {
		return BlacklistMatch{}, false
	}
	normalized, masked := normalizeBlacklistGateValue(kind, raw)
	if normalized == "" {
		// 值本身不合法（手机号格式错 / 空 OpenID）：交给调用方原有的参数校验报错。
		// 这里不写命中记录 —— 那不是「命中规则」，是请求参数问题。
		return BlacklistMatch{}, false
	}

	snapshot := cache.Snapshot()
	if snapshot == nil {
		// 开关为真却没有快照：数据库抖动 / 迁移未完成 / 快照尚未加载。
		// 按 fail-open 放行并留痕，绝不因此拦人（46-*.md §3.4）。
		blacklistGateFailOpen(recorder, db, kind, masked, meta)
		return BlacklistMatch{}, false
	}

	match, hit := matchBlacklistGateValue(snapshot, kind, normalized)
	if !hit {
		return BlacklistMatch{}, false
	}

	// 先写日志再返回：调用方拿到 hit 之后会直接回 403，若那时才写库，
	// 写失败就变成「拒了但查不到记录」，用户投诉时无法解释。
	recordBlacklistGateHit(recorder, db, entity.BlacklistHit{
		EntryID:     match.EntryID,
		Kind:        match.Kind,
		ValueMasked: match.ValueMasked,
		ClientIP:    meta.ClientIP,
		Path:        meta.Path,
		Action:      BlacklistActionBlocked,
	})
	return match, true
}

// normalizeBlacklistGateValue 归一化请求值并给出脱敏展示值。
//
// 返回 normalized=="" 表示该值不参与判定（非法或为空）。
func normalizeBlacklistGateValue(kind string, raw string) (normalized string, masked string) {
	switch kind {
	case BlacklistKindPhone:
		phone, err := NormalizeMainlandPhoneInput(raw)
		if err != nil {
			return "", ""
		}
		return phone, MaskBlacklistValue(BlacklistKindPhone, phone, BlacklistMatchExact)
	case BlacklistKindWechat:
		// 只 TrimSpace：OpenID 大小写敏感，ToLower 会把两个不同账号并成一条规则。
		wechatID := strings.TrimSpace(raw)
		if wechatID == "" {
			return "", ""
		}
		return wechatID, MaskBlacklistValue(BlacklistKindWechat, wechatID, BlacklistMatchExact)
	default:
		return "", ""
	}
}

// matchBlacklistGateValue 在快照里查这个值。
func matchBlacklistGateValue(snapshot *BlacklistSnapshot, kind string, normalized string) (BlacklistMatch, bool) {
	switch kind {
	case BlacklistKindPhone:
		return snapshot.MatchPhone(normalized)
	case BlacklistKindWechat:
		return snapshot.MatchWechat(normalized)
	default:
		return BlacklistMatch{}, false
	}
}

// blacklistGateFailOpen 是 fail-open 的统一出口：打 SysWarn + 写 logged_only 留痕。
//
// 「fail-open 必须留痕」（46-*.md §3.4）：否则规则失效无人知，
// 运营会以为自己在拦人而实际上全站放行。
func blacklistGateFailOpen(recorder *BlacklistHitRecorder, db *gorm.DB, kind string, masked string, meta BlacklistGateMeta) {
	logger.SysWarn("手机号/微信黑名单判定降级放行（fail-open）：黑名单缓存快照缺失")
	recordBlacklistGateHit(recorder, db, entity.BlacklistHit{
		// EntryID=0 表示「不知道命中哪条」——它本来就是降级留痕，不是真命中。
		EntryID:     0,
		Kind:        kind,
		ValueMasked: masked,
		ClientIP:    meta.ClientIP,
		Path:        meta.Path,
		Action:      BlacklistActionLoggedOnly,
	})
}

// recordBlacklistGateHit 写命中日志；失败只记日志，绝不影响请求处置。
func recordBlacklistGateHit(recorder *BlacklistHitRecorder, db *gorm.DB, hit entity.BlacklistHit) {
	if recorder == nil || db == nil {
		return
	}
	if err := recorder.Record(cleanBlacklistGateDB(db), hit); err != nil {
		logger.SysWarn("写入黑名单命中日志失败（不影响本次请求处置）：" + err.Error())
	}
}

// cleanBlacklistGateDB 返回一个**不带 DAO 元数据的干净 Session**。
//
// ⚠️ 这不是洁癖，是本仓已踩过的真实崩溃点（见 admin_login.go 的 adminLoginDB 注释）：
// 业务代码普遍把 `dao.User.UnderlyingDB()` 传进来，那个句柄的 Statement 里
// 已经解析成 `users` 模型的 schema。拿它去 `Create(&entity.BlacklistHit{})`，
// GORM 会拿 User 的字段 index 去 reflect 一个 BlacklistHit 值 ——
// 直接 `panic: reflect: Field index out of range`，**且是在命中路径上 panic**
// （也就是「真有人被拦」时才炸，平时测不出来）。
//
// NewDB:true 清掉 Statement 的 model/schema 但保留同一连接与事务上下文，
// 写日志落到与业务完全相同的库。
func cleanBlacklistGateDB(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

// defaultBlacklistGateRecorder 是进程级聚合器（槽位表跨请求共享，必须唯一）。
//
// 与中间件那个分开实例是有意的：两者写入的 key 空间（entryID + clientIP + action）
// 相同但来源不同，共用会让「哪条链路在拦」在排障时看不出来。
var (
	defaultBlacklistGateRecorder     *BlacklistHitRecorder
	defaultBlacklistGateRecorderOnce sync.Once
)

// DefaultBlacklistGateHitRecorder 返回手机号/微信判定用的进程级聚合器。
func DefaultBlacklistGateHitRecorder() *BlacklistHitRecorder {
	defaultBlacklistGateRecorderOnce.Do(func() {
		defaultBlacklistGateRecorder = NewBlacklistHitRecorder()
	})
	return defaultBlacklistGateRecorder
}
