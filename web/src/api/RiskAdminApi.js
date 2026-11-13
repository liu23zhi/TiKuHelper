import request from '../utils/request'

// 风控管理（task_doc/96-风控管理仅告警与用户画像-设计.md §4）。
// 后端契约（internal/controller/risk_admin.go，路由需在 internal/api/router.go 接线）：
//   GET  /adapter-service/risk/alerts            告警列表：分页 + ruleId / userId / notified / createdFrom / createdTo
//   GET  /adapter-service/risk/alerts/:id        告警详情：Detail 是触发时刻的证据快照 JSON
//   GET  /adapter-service/risk/webhook           风控 webhook 配置：webhookUrl 恒为空 + 脱敏 preview
//   PUT  /adapter-service/risk/webhook           风控 webhook 配置：空白/掩码 = 保持原值，clearWebhook = 清空
//   POST /adapter-service/risk/webhook/test      用**风控配置**发一条测试消息
//
// ⚠️ C2/C3（用户明确要求，不可协商）
//
// 本文件的 webhook 是**风控专用**配置（wecom_risk_* 三键）。它与工单 Webhook
// （wecom_ticket_* 三键、接口 /adapter-service/tickets/config）是**两套独立记录**：
// 值可以相同，但：
//   - 本文件**不读写** /adapter-service/tickets/config；
//   - 本文件**没有任何**「风控为空就回退工单值」的分支 —— 那种隐式复用会让风控告警
//     发到工单群，且管理员以为自己配的是风控通道（96 §0 C2 点名禁止的写法）；
//   - 清空风控地址只写 wecom_risk_webhook_url，工单那条一个字节都不动。
//
// 全部使用 errorFeedback: 'local'：页面自己有失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 BlacklistApi.js / ReferralApi.js 同约定）。

const RISK_BASE = '/adapter-service/risk'

function localRequest(config) {
  return request({ ...config, errorFeedback: 'local' })
}

function readPositiveId(value, label) {
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || parsed <= 0) throw new Error(label + '参数错误')
  return parsed
}

// getRiskAlerts 拉取告警列表（只读）。
//
// params 由调用方用 buildRiskAlertListParams 构造好再传：参数判据只在一个地方
// （pages/RiskAdmin/riskAdminHelpers.mjs），避免这里再挑一遍字段把它丢光。
export function getRiskAlerts(params) {
  return localRequest({
    url: RISK_BASE + '/alerts',
    method: 'get',
    params,
  })
}

// getRiskAlertDetail 读取单条告警（只读）。
//
// 先校验 id：`/risk/alerts/abc` 在后端是 400，前端提前拦住能省一次往返，
// 但**不做任何降级**（不猜 id、不回落到列表第一条）。
export function getRiskAlertDetail(id) {
  const value = readPositiveId(id, '风控告警')
  return localRequest({
    url: RISK_BASE + '/alerts/' + value,
    method: 'get',
  })
}

// getRiskWebhookSettings 读取风控 webhook 配置（脱敏形态）。
//
// 响应：{ enabled, configured, webhookUrl: '', preview, mentionedList }。
// webhookUrl 恒为空是**服务端契约**（凭据不回显），页面只能展示 preview。
export function getRiskWebhookSettings() {
  return localRequest({
    url: RISK_BASE + '/webhook',
    method: 'get',
  })
}

// updateRiskWebhookSettings 写入风控 webhook 配置。
//
// ⚠️ 入参是**已经构造好的请求体**（来自 buildRiskWebhookUpdatePayload），
// 这里**不再挑一遍字段**：本仓踩过「helper 算对了但中间层重新挑字段把它丢了」
// （TikuComponentApi.adjustUserBalance 漏 bucket 那次），所以这个函数只做透传。
// 也会拦住 payload 里混进 ticket 相关键的情况 —— 两个通道的 payload 结构不允许合流。
export function updateRiskWebhookSettings(payload) {
  const body = payload && typeof payload === 'object' ? payload : {}
  const forbidden = ['ticketWebhookUrl', 'wecomTicketWebhookUrl', 'ticketEnabled']
  for (const key of forbidden) {
    if (Object.hasOwn(body, key)) throw new Error('风控配置请求体不得包含工单字段：' + key)
  }
  return localRequest({
    url: RISK_BASE + '/webhook',
    method: 'put',
    data: body,
  })
}

// testRiskWebhook 用**风控配置**发一条测试消息。
//
// 服务端在风控未配置或未启用时返回 400（文案会指明是「风控」那个框），
// 并且**绝不**改用工单配置发送 —— 所以这里也不做任何前置「借用」判断。
export function testRiskWebhook() {
  return localRequest({
    url: RISK_BASE + '/webhook/test',
    method: 'post',
    // 真实发信 + 企业微信往返，比默认 60s 留足余量但不必更长。
    timeout: 30_000,
  })
}
