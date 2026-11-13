package service

import "strings"

// 调用来源的三值集合（task_doc/111 §6.2 定案）。
//
// ⚠️ 明确**没有** exe_non_exam：用户口径是「来源只有 exe 客户端（只算考试模式）
// 和扩展」，故 exe 只用于考试，不存在第四种取值（task_doc/111 §6.1）。
const (
	// ClientKindExamClient 是 exe 客户端的考试模式。
	ClientKindExamClient = "exam_client"
	// ClientKindExtension 是浏览器扩展。
	ClientKindExtension = "extension"
	// ClientKindUnknown 是兜底值：老数据、判定不出来、或证据互相矛盾。
	//
	// 它同时是 logs.client_kind / access_tokens.client_kind 的列默认值
	// （见 internal/registry/client_kind_migration.go）。
	ClientKindUnknown = "unknown"

	// ClientKindColumn 是两张表上的物理列名（107 Q11a）。
	ClientKindColumn = "client_kind"
	// ClientKindHeader 是唯一可信的显式声明头（task_doc/111 §6.3 第 ① 步）。
	ClientKindHeader = "X-Client-Kind"
)

// ClientKindInput 是判定所需的全部原始证据。
//
// 故意做成纯数据结构而不是 *gin.Context：判定函数必须是纯函数，
// 才能覆盖「同一组证据永远得出同一个值」这条可回归的契约。
// 取值由调用方从请求里摘出来（header / UA / token 记录）。
type ClientKindInput struct {
	// DeclaredKind 是 ClientKindHeader 的原始值，未经清洗。
	DeclaredKind string
	// TokenKind 是 access_tokens.client_kind 的值（令牌签发来源）。
	TokenKind string
	// UserAgent 是请求的 User-Agent 原文。
	UserAgent string
	// Origin 是请求的 Origin 头原文。
	Origin string
	// ScriptIdentity / ScriptToken 是客户端侧包装层带上的脚本凭证头
	// （X-Script-Identity / X-Script-Token，见 controller/script.go 与
	// controller/search.go 的读取点）。
	ScriptIdentity string
	ScriptToken    string
}

// extensionOriginSchemes 是浏览器扩展页面的 origin scheme。
//
// 依据：web 侧与 controller 侧的跨域测试里出现的就是 chrome-extension://
// （internal/controller/s01_cookie_context_test.go、cors_open_token_test.go），
// 说明扩展发请求时 Origin 会是这个形态 —— 它是浏览器强制写入、页面 JS
// 改不了的字段，因此比 UA 更难伪造。
var extensionOriginSchemes = []string{
	"chrome-extension://",
	"moz-extension://",
	"safari-web-extension://",
	"ms-browser-extension://",
}

// extensionUserAgentMarkers 是扩展自身 UA 里的产品名特征。
//
// 依据：仓库内自建 UA 的命名就是 tikuAdapter-extension-updater/1
// （internal/service/extension_update.go:332），所以扩展侧的 UA 产品名
// 前缀是 tikuadapter-extension。
var extensionUserAgentMarkers = []string{
	"tikuadapter-extension",
}

// examClientUserAgentMarkers 是超星考试客户端侧的 UA 产品名特征。
//
// 依据：真实客户端宿主进程是 CXExam.exe（chaoxing_plus 的
// scripts/run-real-client-regression.ps1:195 按这个文件名判定客户端目录），
// 注入层产品名是 chaoxing-plus / cxhook（windows/hook-source 与 native-source 的命名）。
// 只匹配产品名，不匹配 "cef" / "chromium"：那是 libcef 的通用串，
// 浏览器也会带，用它判 exe 会把普通浏览器全判成 exam_client。
var examClientUserAgentMarkers = []string{
	"cxexam",
	"chaoxing-plus",
	"chaoxing_plus",
	"cxhook",
}

// NormalizeClientKind 把任意字符串收敛到三值集合之一。
//
// 契约：**永不返回空串**。任何无法识别的输入（空、未知字面值、
// 大小写/空白噪声）一律收敛为 ClientKindUnknown。
//
// 严格只认三个字面值，不做 exe→exam_client 之类的别名映射：
// 早期文档草案里曾出现过 exe / plugin，但定案是 exam_client / extension，
// 悄悄接受旧写法会让「落库值到底是什么」变得不可判定。
func NormalizeClientKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ClientKindExamClient:
		return ClientKindExamClient
	case ClientKindExtension:
		return ClientKindExtension
	case ClientKindUnknown:
		return ClientKindUnknown
	}
	return ClientKindUnknown
}

// ResolveClientKind 判定一次请求的调用来源，返回三值之一（绝不返回空串）。
//
// 判定顺序固定（task_doc/111 §6.3，证据强度从强到弱）：
//
//	① ClientKindHeader 显式声明且合法 → 用它
//	② access_tokens.client_kind 已记录 → 用它（令牌是长期凭证，来源固定）
//	③ Origin 是扩展 scheme / UA 带扩展产品名 → extension
//	④ UA 带考试客户端产品名 / 带脚本凭证头 → exam_client
//	⑤ 都不匹配 → unknown
//
// 为什么显式声明排第一：它由客户端自己写明，且服务端只在鉴权通过后才采信
// （伪造它需要有效令牌，因此不比 token 路径更弱），而它比 UA 精确。
//
// 为什么 token 排在 UA 前面：UA 是客户端随便写的，token 的 kind 是
// 用户建 key 时在后台定的持久属性（107 Q11b：exe 侧只有考试模式）。
//
// 冲突处理：③ 排在 ④ 前（与文档一致）。一行 UA 同时命中两族特征的情形
// 现实中不存在，但顺序必须固定，否则同一个请求在不同编译/迭代下会得出
// 不同取值，破坏可回归性。
func ResolveClientKind(in ClientKindInput) string {
	// ① 显式声明。
	if declared := NormalizeClientKind(in.DeclaredKind); declared != ClientKindUnknown {
		return declared
	}
	// ② 令牌签发来源。unknown 视为「没记录」，继续往下推断。
	if tokenKind := NormalizeClientKind(in.TokenKind); tokenKind != ClientKindUnknown {
		return tokenKind
	}
	// ③ 扩展特征。
	if isExtensionRequest(in) {
		return ClientKindExtension
	}
	// ④ 考试客户端特征。
	if isExamClientRequest(in) {
		return ClientKindExamClient
	}
	// ⑤ 判不出来就是 unknown。
	return ClientKindUnknown
}

// isExtensionRequest 判扩展特征。
func isExtensionRequest(in ClientKindInput) bool {
	origin := strings.ToLower(strings.TrimSpace(in.Origin))
	for _, scheme := range extensionOriginSchemes {
		if strings.HasPrefix(origin, scheme) {
			return true
		}
	}
	ua := strings.ToLower(in.UserAgent)
	for _, marker := range extensionUserAgentMarkers {
		if strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}

// isExamClientRequest 判考试客户端特征。
func isExamClientRequest(in ClientKindInput) bool {
	// 脚本凭证头：客户端侧包装层（chaoxing_plus）请求 tikuAdapter 时带的就是这两个头
	// （src/projects/tiku-adapter-config.ts 的 Authorization / X-Script-Identity 组合）。
	if strings.TrimSpace(in.ScriptIdentity) != "" || strings.TrimSpace(in.ScriptToken) != "" {
		return true
	}
	ua := strings.ToLower(in.UserAgent)
	for _, marker := range examClientUserAgentMarkers {
		if strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}
