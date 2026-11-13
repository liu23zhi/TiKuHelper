// 风控管理页的纯函数层（不 import vue / axios，可在 node --test 里直接执行）。
//
// 为什么单独一文件：页面（index.vue）无法在无 node_modules 的环境里加载，
// 而 API 模块顶层 import 了 request.js（依赖 axios / ant-design-vue）。
// 把**判据相关**的逻辑（payload 构造、参数校验、文案常量）放在这里，
// 测试就能真正执行实现而不是对源码做字符串断言 —— 这是「真钉子」与
// 「假钉子」的分界（本仓 t142/t143 反复吃过的教训）。

// 96 §4.2 要求出现在 UI 上的原文。
//
// ⚠️ 这段话是 C3 的用户可见形态，不能简化成「两个配置互相独立」之类的概括：
// 用户明确要求「即使填入同一个群机器人地址，也请分别填写 —— 清除其中一个不会影响另一个」，
// 少了「清除其中一个不会影响另一个」，管理员就会以为填同一个地址等于共享一份配置。
export const RISK_INDEPENDENCE_NOTICE =
  '⚠️ 风控通知与工单通知是两套独立配置。即使填入同一个群机器人地址，也请分别填写 —— 清除其中一个不会影响另一个。'

// 风控通道的两套配置分别落在哪组 setting key 上（只用于页面展示与排错提示，
// **不参与任何读写决策**：真正的读写由后端按 key 前缀完成）。
export const RISK_SETTING_KEYS = Object.freeze({
  enabled: 'wecom_risk_notifications_enabled',
  webhook: 'wecom_risk_webhook_url',
  mentioned: 'wecom_risk_mentioned_list',
  // 这里刻意只列出**工单那组**的 key，用于页面上的「两套独立」对照展示。
  // 页面**不读也不写**它们（读写走 /adapter-service/tickets/config）。
  ticketEnabled: 'wecom_ticket_notifications_enabled',
  ticketWebhook: 'wecom_ticket_webhook_url',
  ticketMentioned: 'wecom_ticket_mentioned_list',
})

export const RISK_RULE_OPTIONS = Object.freeze([
  { value: 'debt_user', label: '单用户欠款超阈值' },
  { value: 'debt_batch', label: '单批次欠款超阈值' },
  { value: 'debt_system', label: '系统总欠款突增' },
  // 96 §2.1 的 R1（18 条行为规则里第一条落地的）。
  // 后端 rule_id 是 `multi_account_ip`，阈值键 `risk_rule_multi_account_ip_threshold`。
  { value: 'multi_account_ip', label: '同 IP 多账号' },
])

const RISK_RULE_LABELS = Object.freeze(
  Object.fromEntries(RISK_RULE_OPTIONS.map(item => [item.value, item.label])),
)

export const RISK_SEVERITY_META = Object.freeze({
  high: { label: '高', color: 'red', order: 3 },
  medium: { label: '中', color: 'gold', order: 2 },
  low: { label: '低', color: 'blue', order: 1 },
})

// 风控命中「什么都没做」的唯一可能结论（96 C1）：
// 页面上任何位置都不提供「封禁 / 拒绝 / 改状态」按钮，处置必须去黑名单或封禁各自的页面。
export const RISK_ADVISORY_NOTE =
  '风控只产生告警记录与企业微信推送。命中后不会封禁账号、不会拒绝请求、不会修改任何用户状态；处置请到「黑名单管理」或其他对应页面人工执行。'

export function riskRuleLabel(ruleId) {
  const key = String(ruleId ?? '').trim()
  if (!key) return '未知规则'
  return RISK_RULE_LABELS[key] || key
}

export function riskSeverityMeta(severity) {
  const key = String(severity ?? '').trim().toLowerCase()
  return RISK_SEVERITY_META[key] || { label: severity || '未知', color: 'default', order: 0 }
}

// parseMentionedInput 把「逗号分隔的成员 ID」解析成去重数组。
//
// 判据与后端 normalizeTicketMentionedList 对齐（风控复用同一套归一化）：
// 支持中英文逗号/分号/换行分隔，单个 ID 超过 64 个 rune 直接丢弃。
// 这里**不**把 `@all` 特殊化：企业微信侧接受 `@all`，由发送器决定 <@all>。
export function parseMentionedInput(value) {
  const raw = Array.isArray(value) ? value : String(value ?? '').split(/[,，;；\n\r\t]+/)
  const seen = new Set()
  const result = []
  for (const item of raw) {
    const text = String(item ?? '').trim()
    if (!text) continue
    if (Array.from(text).length > 64) continue
    if (seen.has(text)) continue
    seen.add(text)
    result.push(text)
  }
  return result
}

export function formatMentionedInput(list) {
  return parseMentionedInput(list).join('，')
}

// buildRiskWebhookUpdatePayload 是 PUT /adapter-service/risk/webhook 的**唯一**请求体构造处。
//
// 四条不可协商的语义：
//   1. enabled **恒传**（后端 *bool：nil = 保持现值；不发就永远改不动开关）；
//   2. webhookUrl 只在新填了地址时出现 —— 空白 = 保持原值（凭据不回显，表单没有旧值可交）；
//   3. clearWebhook 只在显式清空时为 true，且清空时**不**同时提交地址
//      （避免回放日志里出现「清了又填」的歧义记录）；
//   4. 请求体里**没有任何工单字段**：清空风控地址只写 wecom_risk_webhook_url，
//      工单那条（/tickets/config）一个字节都不会动。
export function buildRiskWebhookUpdatePayload(form) {
  const clearWebhook = form?.clearWebhook === true
  const payload = {
    enabled: form?.enabled === true,
    clearWebhook,
    mentionedList: parseMentionedInput(form?.mentionedList),
  }
  const url = String(form?.webhookUrl ?? '').trim()
  if (url && !clearWebhook) payload.webhookUrl = url
  return payload
}

// normalizeRiskWebhookSettings 校验并归一化 GET/PUT /risk/webhook 的响应。
//
// 响应里 `webhookUrl` 恒为空（凭据不回显）：这里的归一化**故意**把任何
// 非空 webhookUrl 也丢弃，页面永远只展示 preview。这样即使某天后端误回显了
// 真实地址，页面上也不会出现一个可被截图/复制带走的凭据。
export function normalizeRiskWebhookSettings(data) {
  if (!data || typeof data !== 'object') throw new Error('风控通知配置响应无效')
  if (typeof data.enabled !== 'boolean' || typeof data.configured !== 'boolean') {
    throw new Error('风控通知配置响应无效')
  }
  return {
    enabled: data.enabled,
    configured: data.configured,
    webhookUrl: '',
    preview: String(data.preview ?? '').trim(),
    mentionedList: parseMentionedInput(data.mentionedList),
  }
}

// describeRiskWebhookState 把配置状态翻译成一句人话（避免页面各处自己拼）。
export function describeRiskWebhookState(settings) {
  if (!settings || typeof settings !== 'object') return '尚未读取配置'
  if (!settings.configured) return '未配置风控机器人地址（风控告警只会落库，不会推送）'
  const preview = String(settings.preview ?? '').trim()
  return settings.enabled
    ? `已启用${preview ? '：' + preview : ''}`
    : `已配置但未启用${preview ? '：' + preview : ''}（告警只落库，不推送）`
}

// validateRiskAlertFilters 是筛选条件的前端预校验。
//
// 与后端 risk_admin.go 的判据**逐条对齐**（userId 正整数、时间可解析且 from<=to、
// notified 只接受 true/false），错一条就前后端各说一套，管理员会看到
// 「页面能提交、接口 400」这种最难自查的组合。
//
// 返回空串表示通过。
export function validateRiskAlertFilters(filters) {
  const userId = String(filters?.userId ?? '').trim()
  if (userId && !/^[0-9]+$/.test(userId)) return '用户 ID 只能是正整数'
  if (userId && Number(userId) <= 0) return '用户 ID 只能是正整数'
  if (userId && Number(userId) > 2147483647) return '用户 ID 超出范围'
  const notified = String(filters?.notified ?? '').trim()
  if (notified && notified !== 'true' && notified !== 'false') return '推送状态只能是「已推送」或「未推送」'
  const from = String(filters?.createdFrom ?? '').trim()
  const to = String(filters?.createdTo ?? '').trim()
  if (from && !to && !isParsableRiskDate(from)) return '开始日期格式无效'
  if (to && !isParsableRiskDate(to)) return '结束日期格式无效'
  if (from && to) {
    if (!isParsableRiskDate(from)) return '开始日期格式无效'
    if (!isParsableRiskDate(to)) return '结束日期格式无效'
    if (from > to) return '时间范围无效：开始日期晚于结束日期'
  }
  return ''
}

function isParsableRiskDate(value) {
  const text = String(value ?? '').trim()
  if (!text) return false
  // 纯日期（YYYY-MM-DD）按**字符串**比较即可保证 from<=to（定长补零），
  // 这也是后端接受的主要形态；带时间的 RFC3339 交给 Date 判合法性。
  if (/^\d{4}-\d{2}-\d{2}$/.test(text)) {
    const [year, month, day] = text.split('-').map(Number)
    if (month < 1 || month > 12 || day < 1 || day > 31) return false
    const probe = new Date(Date.UTC(year, month - 1, day))
    return probe.getUTCFullYear() === year && probe.getUTCMonth() === month - 1 && probe.getUTCDate() === day
  }
  return Number.isFinite(new Date(text).getTime())
}

// buildRiskAlertListParams 构造 GET /risk/alerts 的查询参数。
//
// 空值一律**不出现**（而不是传空串）：后端把 `ruleId=` 当作筛选条件时
// 会拼出 `rule_id = ''`，结果永远为空 —— 这是「看起来筛了但一条都查不到」的典型来源。
export function buildRiskAlertListParams(filters, page, pageSize) {
  const params = {
    page: Number.isInteger(Number(page)) && Number(page) > 0 ? Number(page) : 1,
    pageSize: Number.isInteger(Number(pageSize)) && Number(pageSize) > 0
      ? Math.min(Number(pageSize), 100)
      : 20,
  }
  const ruleId = String(filters?.ruleId ?? '').trim()
  if (ruleId) params.ruleId = ruleId
  const userId = String(filters?.userId ?? '').trim()
  if (userId) params.userId = userId
  const notified = String(filters?.notified ?? '').trim()
  if (notified === 'true' || notified === 'false') params.notified = notified
  const createdFrom = String(filters?.createdFrom ?? '').trim()
  if (createdFrom) params.createdFrom = createdFrom
  const createdTo = String(filters?.createdTo ?? '').trim()
  if (createdTo) params.createdTo = createdTo
  return params
}

// parseRiskEvidence 解析告警里的 Detail 证据快照。
//
// 与后端 buildRiskWarningMarkdown 同一策略：**证据坏了不能让整条告警不可看**，
// 解析失败时把原文放进 unparsableDetail 而不是抛错。
export function parseRiskEvidence(detail) {
  const raw = String(detail ?? '').trim()
  if (!raw) return { evidence: {}, unparsable: '' }
  try {
    const parsed = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return { evidence: {}, unparsable: raw }
    }
    return { evidence: parsed, unparsable: '' }
  } catch (error) {
    void error
    return { evidence: {}, unparsable: raw }
  }
}

// buildRiskEvidenceRows 把证据快照摊成「字段 / 值」两列，供详情弹窗展示。
//
// 排序固定（不依赖 Object.keys 的插入顺序）：同一个告警两次打开证据顺序一致，
// 便于截图对比与人工复盘。
export function buildRiskEvidenceRows(detail) {
  const { evidence, unparsable } = parseRiskEvidence(detail)
  if (unparsable) {
    return [{ field: '原始证据（无法解析为 JSON）', value: unparsable }]
  }
  const preferred = ['ruleId', 'subjectKey', 'userId', 'username', 'threshold', 'value', 'balance', 'batchId', 'debtAmount', 'userCount', 'evaluatedAt', 'evidenceNote']
  const keys = Object.keys(evidence)
  const ordered = [
    ...preferred.filter(key => Object.hasOwn(evidence, key)),
    ...keys.filter(key => !preferred.includes(key)).sort(),
  ]
  return ordered.map(field => ({ field, value: stringifyRiskEvidenceValue(evidence[field]) }))
}

function stringifyRiskEvidenceValue(value) {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value)
    } catch (error) {
      void error
      return String(value)
    }
  }
  return String(value)
}

export function formatRiskDateTime(value) {
  const raw = String(value ?? '').trim()
  if (!raw) return '—'
  const date = new Date(raw)
  if (!Number.isFinite(date.getTime())) return raw
  return date.toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
}

// readRiskListBody 解析列表响应，区分「加载失败」与「确实为空」。
//
// 失败时**必须**让调用方保留上一批行：把失败渲染成空列表，会让运营以为
// 告警被清空了（与 Blacklist 页同一条约定）。
export function readRiskListBody(response) {
  const body = response?.data
  const items = body?.data
  const total = Number(body?.total)
  const ok = Number(response?.status) === 200 && Array.isArray(items) && Number.isFinite(total) && total >= 0
  return {
    ok,
    items: ok ? items.filter(item => item && typeof item === 'object').map(item => ({ ...item })) : [],
    total: ok ? total : null,
  }
}

// readRiskMessage 从响应里取可读错误文案（后端 400 会原文回可见的配置类错误）。
export function readRiskMessage(response, fallback) {
  const direct = String(response?.message ?? '').trim()
  if (direct) return direct
  const body = String(response?.data?.message ?? '').trim()
  if (body) return body
  return fallback
}

// readRiskThrownMessage 从 axios 抛出的错误里取后端文案。
export function readRiskThrownMessage(error, fallback) {
  const message = String(error?.response?.data?.message ?? '').trim()
  return message || fallback
}

// ⚠️ 判据 ②「页面能改阈值并落库」在本轮**未实现，原因在后端而不是前端**（实测）：
//
//   controller/risk_admin.go 只有 5 个 handler —— RiskWarningList / RiskWarningDetail /
//   RiskWecomSettings / UpdateRiskWecomSettings / TestRiskWecomWebhook，**没有任何**
//   risk_rules 的读或写接口；阈值 `risk_rule_<ruleId>_threshold` 是 system_settings 的键，
//   而 UpdateSystemSettingInput（service/system_setting.go）是固定 struct，没有该字段，
//   也没有通用的 key/value 透传口。
//
// 所以前端**故意不提供**阈值输入框：加一个会打到不存在路由上的控件，表现为
// 「能填、能点保存、没有任何反应」，比缺失更难排查（本仓「假钉子」的同族形态）。
// 落地判据②需要后端补一对 handler + 两条路由，那超出本任务的可写范围
// （禁碰 router.go、risk_admin.go 不在可写清单），已在汇报里给出原文给队长接线。

