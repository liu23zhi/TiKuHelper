package service

import "github.com/itihey/tikuAdapter/internal/entity"

const ApprovedDiagnosticAgreementVersion = "v1.0"

const approvedDiagnosticAgreementSummary = "用户协议与必要运行日志告知"

const approvedDiagnosticAgreementBody = `用户协议与必要运行日志告知

版本：v1.0
生效时间：以 TiKuHelper 的正式发布记录为准。

一、确认和更新
请完整阅读后主动确认。关闭页面、拒绝、离线缓存或旧版本的同意不是本版本同意。需要本协议的功能仅在服务器确认当前设备已同意后开放。日志用途、字段、接收方或保留规则有实质变化时，需要发布新版本并重新取得同意。

二、诊断日志的用途和您的选择
仅为排查故障、版本兼容和服务稳定性问题收集必要的运行事件，不用于营销、模型训练、行为画像或计费判定。诊断回传只在设备已有效同意当前协议、服务端策略允许时默认开启。您可以通过客户端的诊断回传设置关闭；关闭或授权失效后不再新传。未同意、授权过期或服务端关闭时不回传；网络错误不视为已接收。

三、字段
客户端只记录运行模式（mode）、客户端版本（version）、运行标识（device_id、launch_id；适用的原生浏览器另有 browser_id）、事件发生时间（time）、级别（level）、固定枚举事件（event），必要时仅附加有范围限制的耗时（duration_ms）或计数（count）。浏览器构建中的事件包括就绪、设置保存、同步成功或失败、网络超时或离线、运行错误。固定事件码不含业务正文。服务端从真实认证补充内部 user_id、接收时间 received_at 和入库序号 seq；运行标识可与账号关联，并非完全匿名。传输只使用防止重复接收所需的技术标识，不作为业务诊断内容展示。

诊断日志不包含密码、API Key、设备凭据、访问令牌、Cookie、验证码、姓名、联系方式、原始 IP、URL 或浏览历史、页面内容、表单、题目或答案、截图、自由文本或堆栈；不额外记录协议版本、请求 ID、客户端实例、HTTP 或业务状态码、重试次数。协议同意记录和管理员审计与诊断日志分开保存，不适用此处的诊断留存约定。

四、接收方和保存
上传只发往第一方 TiKuHelper，仅经认证并获授权负责排障的管理员可按需查看，不向第三方或其他平台发送。服务端按实际接收时间最多保留 7×24 小时；诊断容量达到 10 GB 时先清理最旧记录。客户端本地待传队列如有启用，最多保留 24 小时；到期清理，不无限重试或占用空间。

五、联系和反馈
登录后可在 TiKuHelper 的工单反馈中报告故障、隐私疑问或误采集。拒绝本协议、无法登录时，可在本服务网站无需登录的 /agreement-support 页查看管理员设置的公开支持联系方式。公开联系方式是您主动联系支持方的入口，不是自动日志接收方。请勿发送密码、验证码或访问凭据。`

var approvedDiagnosticAgreementHash = AgreementContentHash(approvedDiagnosticAgreementBody)

// ApprovedDiagnosticAgreementTemplate is an inert starting point for admin
// review. Only the existing explicit publish operation makes it current.
func ApprovedDiagnosticAgreementTemplate() entity.AgreementContent {
	return entity.AgreementContent{
		Version: ApprovedDiagnosticAgreementVersion,
		Summary: approvedDiagnosticAgreementSummary,
		Body:    approvedDiagnosticAgreementBody,
	}
}

// IsApprovedDiagnosticAgreement is in addition to current-release/device
// consent and diagnostics policy; a draft or a version label alone is no proof.
func IsApprovedDiagnosticAgreement(version, contentHash string) bool {
	return version == ApprovedDiagnosticAgreementVersion && contentHash == approvedDiagnosticAgreementHash
}
