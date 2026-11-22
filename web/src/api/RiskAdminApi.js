import request from '../utils/request'

// 风控管理：告警/通知、规则和共享配置；两项核心保护的处置独立于告警级别。
// 后端契约（internal/controller/risk_admin.go，路由需在 internal/api/router.go 接线）：
//   GET  /api/risk/alerts            告警列表：分页 + ruleId / userId / notified / createdFrom / createdTo
//   GET  /api/risk/alerts/:id        告警详情：Detail 是触发时刻的证据快照 JSON
//   GET  /api/risk/webhook           风控 webhook 配置：webhookUrl 恒为空 + 脱敏 preview
//   PUT  /api/risk/webhook           风控 webhook 配置：空白/掩码 = 保持原值，clearWebhook = 清空
//   POST /api/risk/webhook/test      用**风控配置**发一条测试消息
//
// ⚠️ C2/C3（用户明确要求，不可协商）
//
// 本文件的 webhook 是**风控专用**配置（wecom_risk_* 三键）。它与工单 Webhook
// （wecom_ticket_* 三键、接口 /api/tickets/config）是**两套独立记录**：
// 值可以相同，但：
//   - 本文件**不读写** /api/tickets/config；
//   - 本文件**没有任何**「风控为空就回退工单值」的分支 —— 那种隐式复用会让风控告警
//     发到工单群，且管理员以为自己配的是风控通道（96 §0 C2 点名禁止的写法）；
//   - 清空风控地址只写 wecom_risk_webhook_url，工单那条一个字节都不动。
//
// 全部使用 errorFeedback: 'local'：页面自己有失败态与重试入口，
// 不能在被页面接管的请求上再弹一次全局错误提示（与 BlacklistApi.js / ReferralApi.js 同约定）。

const RISK_BASE = '/api/risk'

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

// ════════════════════════════════════════════════════════════════════
// 风控阈值读写
//
// ⚠️ 这三条接口在**本次改动之前不存在**。此前 `risk_rule_<ruleId>_threshold`
// 这些 system_settings 键没有任何写入路径（UpdateSystemSettingInput 是固定 struct、
// 没有 risk_rules 写接口），所以页面**故意不做**阈值输入框 —— 因为做了也打不到后端，
// 表现为「能填、能点保存、毫无反应」，比缺失更难排查。
//
// 现在后端补齐了 GET/PUT/DELETE /risk/thresholds[/:key]，页面可以正式提供输入框。
// `:key` 走服务端白名单校验，前端不做二次推断（只做基础类型/范围提示，
// 最终裁决权在后端 —— 两边各写一套校验必然会漂移）。
// ════════════════════════════════════════════════════════════════════

// getRiskThresholds 读取全部可调阈值及其当前生效值。
//
// 响应：{ items: [{ key, ruleId, label, kind, value, defaultValue, source, configured, unit }] }
// `source` 说明当前值来自哪一层（system_settings / risk_rules / code），
// `configured` 区分「读不到走了默认值」与「显式配置成默认值」。
export function getRiskThresholds() {
  return localRequest({
    url: RISK_BASE + '/thresholds',
    method: 'get',
  })
}

// updateRiskThreshold 设置某个阈值的显式值。
//
// 入参只做「是不是有限数」这一层类型守卫；**范围校验交给后端**（占比 0~1、
// 绝对数 >0、连续天数为正整数这三套规则只在 service.ValidateRiskThresholdValue
// 里写一份）。前端若再写一套，改一处忘另一处就会让「页面允许但后端拒绝」。
export function updateRiskThreshold(key, value) {
  const name = String(key ?? '').trim()
  if (!name) throw new Error('阈值键参数错误')
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) throw new Error('阈值必须是数字')
  return localRequest({
    url: RISK_BASE + '/thresholds/' + encodeURIComponent(name),
    method: 'put',
    data: { value: parsed },
  })
}

// resetRiskThreshold 把阈值重置为代码默认值（服务端删除显式配置的行）。
export function resetRiskThreshold(key) {
  const name = String(key ?? '').trim()
  if (!name) throw new Error('阈值键参数错误')
  return localRequest({
    url: RISK_BASE + '/thresholds/' + encodeURIComponent(name),
    method: 'delete',
  })
}
// ════════════════════════════════════════════════════════════════════
// 规则状态（GET /risk/rules + PUT /risk/rules/:ruleId/enabled）
//
// 后端：internal/controller/risk_admin.go 的 RiskRuleStateList / SetRiskRuleEnabled，
// 路由：internal/api/router.go:496-497。
//
// ⚠️ 为什么需要这两个函数（历史原因必须记下）：
// 这两个接口**早就实现了**，但前端一直没接。原因之一是 riskAdminSource.test.mjs
// 里曾有一条 `assert.doesNotMatch(api, /\/risk\/rules/)`，理由写的是「后端不存在
// 该路由」—— 那条理由在路由挂上之后就过期了，却一直拦着不让接。
// 后果：10 条规则的 Description（含「需 3 天历史」「被抢号不计入」这类关键限制）
// 与全部启停能力，在管理页上完全不可见。
// ════════════════════════════════════════════════════════════════════

// getRiskRules 读取全部规则的当前状态（启用与否 + 生效阈值 + 描述）。
//
// 响应：{ items: [{ ruleId, name, type, source, enabled, severity, threshold, description, updatedAt, updatedBy }] }
export function getRiskRules() {
  return localRequest({
    url: RISK_BASE + '/rules',
    method: 'get',
  })
}

// getRiskRuleTypes 返回服务端允许执行的检测器类型；新增规则只能选这里登记的类型。
export function getRiskRuleTypes() {
  return localRequest({
    url: RISK_BASE + '/rule-types',
    method: 'get',
  })
}

export function createRiskRule(payload) {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('规则参数错误')
  return localRequest({
    url: RISK_BASE + '/rules',
    method: 'post',
    data: payload,
  })
}

export function updateRiskRule(ruleId, payload) {
  const id = String(ruleId ?? '').trim()
  if (!id) throw new Error('规则 ID 参数错误')
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('规则参数错误')
  return localRequest({
    url: RISK_BASE + '/rules/' + encodeURIComponent(id),
    method: 'put',
    data: payload,
  })
}

export function deleteRiskRule(ruleId) {
  const id = String(ruleId ?? '').trim()
  if (!id) throw new Error('规则 ID 参数错误')
  return localRequest({
    url: RISK_BASE + '/rules/' + encodeURIComponent(id),
    method: 'delete',
  })
}

// setRiskRuleEnabled 启用/停用一条规则。
//
// enabled 必须是布尔值：后端用 `*bool` 区分「没传」与「传 false」（停用是最常见的
// 操作，用零值判断会把停用当成没传）。所以这里**不做真值转换**（不把 'false' 当 false），
// 传进来不是布尔就直接报错，避免「以为停用了、其实发了个字符串」。
export function setRiskRuleEnabled(ruleId, enabled) {
  const id = String(ruleId ?? '').trim()
  if (!id) throw new Error('规则 ID 参数错误')
  if (typeof enabled !== 'boolean') throw new Error('规则启用状态必须是布尔值')
  return localRequest({
    url: RISK_BASE + '/rules/' + encodeURIComponent(id) + '/enabled',
    method: 'put',
    data: { enabled },
  })
}

// ════════════════════════════════════════════════════════════════════
// 非数值配置（GET /risk/config + PUT/DELETE /risk/config/:key）
//
// 后端：internal/controller/risk_admin.go 的 RiskConfigList / UpdateRiskConfig /
// ResetRiskConfig；service：internal/service/risk_config_settings.go（白名单）。
// 路由：internal/api/router.go（风控段末尾三条）。
//
// ⚠️ 为什么这些不能走 /risk/thresholds（这是本次补齐的关键判断）：
// 阈值接口的值类型是 `float64`（`updateRiskThresholdRequest.Value *float64`），
// 而这两项的取值是**文本**：
//   · risk_proxy_asn_list            ASN 名单（"AS15169,AS16509"，空串 = 关闭规则）
//   · risk_evaluate_interval_minutes 整数分钟（1–1440，空串 = 回到默认）
// 塞进数值接口只会得到 JSON 解析 400。所以**数值归阈值、文本归配置**。
//
// 这两项此前**完全没有写入路径**，后果是同一种静默失效：
//   · 名单为空时 R10 连查询都不做 ⇒ 该规则永不告警，而运营以为它在工作；
//   · 评估间隔只能改库，且填 >1440 会**反向生效**（回落 10 分钟 = 更频繁）。
// ════════════════════════════════════════════════════════════════════

// getRiskConfigs 读取全部非数值配置及其当前生效值。
//
// 响应：{ items: [{ key, ruleId, label, kind, value, defaultValue, source,
//                  configured, unit, help, minValue, maxValue }] }
// `kind` 决定前端控件（asn_list → 文本域；positive_int → 数字框）。
// `help` 由后端给（含「留空意味着什么」），前端不自己拼 —— 那是第二份真相。
export function getRiskConfigs() {
  return localRequest({
    url: RISK_BASE + '/config',
    method: 'get',
  })
}

// updateRiskConfig 设置某项非数值配置。
//
// ⚠️ `value` 必须是**字符串**，且空串是**合法值**（对名单 = 关闭规则，
// 对间隔 = 回到默认）。所以这里**不做 trim 后判空抛错** —— 那会把「清空」
// 这个正常操作挡在前端。后端用 `*string` 区分「没传」与「传空串」。
export function updateRiskConfig(key, value) {
  const name = String(key ?? '').trim()
  if (!name) throw new Error('配置键参数错误')
  if (typeof value !== 'string') throw new Error('配置值必须是字符串')
  return localRequest({
    url: RISK_BASE + '/config/' + encodeURIComponent(name),
    method: 'put',
    data: { value },
  })
}

// resetRiskConfig 把某项配置清回「未配置」（服务端删除显式行，回到代码默认值）。
export function resetRiskConfig(key) {
  const name = String(key ?? '').trim()
  if (!name) throw new Error('配置键参数错误')
  return localRequest({
    url: RISK_BASE + '/config/' + encodeURIComponent(name),
    method: 'delete',
  })
}

// 整个窗口一次读写；不要用两个旧单键请求模拟原子保存。
export const getRiskWindow = () => localRequest({ url: RISK_BASE + '/offhours-window', method: 'get' })
export const putRiskWindow = (startHour, endHour) => localRequest({ url: RISK_BASE + '/offhours-window', method: 'put', data: { startHour, endHour } })
export const resetRiskWindow = () => localRequest({ url: RISK_BASE + '/offhours-window', method: 'delete' })

const policyURL = detector => RISK_BASE + '/response-policies/' + encodeURIComponent(detector)
export const getRiskPolicy = detector => localRequest({ url: policyURL(detector), method: 'get' })
export const putRiskPolicy = (detector, mode, durationMinutes) => localRequest({ url: policyURL(detector), method: 'put', data: { mode, durationMinutes } })
const responseURL = id => RISK_BASE + '/alerts/' + readPositiveId(id, '风控告警') + '/response'
export const getAlertResponse = id => localRequest({ url: responseURL(id), method: 'get' })
export const applyAlertResponse = (id, { action, durationMinutes, reason, confirmed, expectedTarget }) =>
  localRequest({ url: responseURL(id), method: 'post', data: { action, durationMinutes, reason, confirmed, ...(action === 'ip' ? {} : { expectedTarget }) } })
