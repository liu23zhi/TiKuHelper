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
  // 页面**不读也不写**它们（读写走 /api/tickets/config）。
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
  // R8：单用户 24h 建单 ≥N（默认 5）
  { value: 'ticket_flood', label: '工单刷屏' },
  // R11：同前缀 + 连续递增数字用户名 ≥N（默认 3）
  { value: 'abnormal_register_pattern', label: '注册名高度模板化' },
  // R12：配额消耗倍数 或 窗口内多 IP 漂移（默认阈值 3）
  { value: 'token_abuse', label: 'access token 滥用' },
  // R16：连续 N 天凌晨活动占比超阈值（默认 3 天；时段 02:00-05:00 固定）
  // ⚠️ label 里带「需 3 天历史」提示，避免被误认为规则坏了
  { value: 'offhours_activity', label: '凌晨作息异常（需 3 天历史）' },
  // R14：滚动 24h 内主动换绑 ≥N 次（默认 2；被抢号不计入）
  { value: 'binding_churn', label: '频繁换绑' },
  // R18：同一手机号/微信出现在 ≥N 个已封禁账号上（默认 3）
  // ⚠️ label 带「仅当前绑定」提示，因为 users 只存当前值，历史共用查不到
  { value: 'wechat_phone_share', label: '封禁账号共用号（仅当前绑定）' },
  // R3：相邻两次成功登录的移动速度超过民航上限（默认 900 km/h）
  // ⚠️ label 必须写明**两条限制**，因为前端不展示 Description：
  //    ① 仅公网 IP（私网/缺归属地的登录被跳过）
  //    ② 只回溯 7 天（超 7 天未登录的用户，那一对不会被比较）
  // 只写其一会让运营按错误的边界理解告警行为。
  { value: 'impossible_travel', label: '异地瞬移（仅公网 IP · 回溯 7 天）' },
  // R9：同一 IP 在 30 分钟滑动窗口内试了 ≥20 个不同账号名
  // ⚠️ label 写明「只看失败」+「30 分钟」，因为前端不展示 Description
  { value: 'credential_stuffing', label: '撞库（仅失败登录 · 30 分钟）' },
  // R2：同一账号或 IP 在 10 分钟滑动窗口内失败 ≥10 次
  // ⚠️ label 写明「同账号或 IP」，因为这是它与「撞库」的分界（深度 vs 广度）
  { value: 'login_bruteforce', label: '登录爆破（同账号或 IP · 10 分钟）' },
  // R13：30 天内成功退款 ≥3 次且每笔接近满额（90% 为本仓自定）
  { value: 'refund_abuse', label: '退款滥用（仅成功退款 · 近满额）' },
  // R10：登录 IP 的 ASN 落在运营配置的名单内
  // ⚠️ label 必须写明「需配名单」，否则运营会以为它坏了（空名单时规则静默）
  { value: 'proxy_ip_login', label: '代理IP登录（需配 ASN 名单）' },
  // R5：同一 /24 段 1h 内自助注册 ≥3 个账号
  // ⚠️ label 写明「仅自助注册」与「/24」，因为管理员建号不计入、段粒度是 /24
  { value: 'register_burst', label: '注册突增（同 /24 段 · 仅自助注册）' },
  // R17：同一用户 7 天内对同一道题反复搜题
  // ⚠️ label 写明「搜题」而非「提交」——本仓数据源是 billing_records（搜题计费）
  { value: 'answer_pattern_repeat', label: '同题重复（搜题 · 7 天）' },
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

// buildRiskWebhookUpdatePayload 是 PUT /api/risk/webhook 的**唯一**请求体构造处。
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
//
// ─────────────────────────────────────────────────────────────────────
// 2026-11 更新：上述阻塞**已解除**。后端补齐了
// GET/PUT/DELETE /risk/thresholds[/:key] 与 GET /risk/config + PUT/DELETE /risk/config/:key，
// 页面已提供全部输入控件。上面这段保留为历史记录（它解释了为什么这个页面
// 曾经「有面板没输入框」）。

// ─────────────────────────────────────────────────────────────────────
// 「内置规则集不可新增」—— 这是**数据模型事实**，不是未做的功能
//
// 任务书要求先证清模型再决定是否做「新建规则」。实测结论：**不能新建**。
// 依据（三处，缺一条都不足以定论）：
//
//  1. `risk_rules` 确实是数据库表（entity/risk.go 的 RiskRule），但它**不是规则的来源**：
//     `loadRiskRules`（service/risk_alert.go:464）遍历的是
//     `entity.DefaultRiskRules()` 这个**代码常量切片**，只把 DB 行当「覆盖值」用。
//     它在 L478 用 `result := make(..., len(defaults))` 只按 defaults 迭代 ——
//     表里多出来的行会被**直接丢弃**，不报错、不告警。所以往表里插一条新规则，
//     判定链根本看不到它。
//
//  2. 判定逻辑是代码：`EvaluateDebtAlerts`（risk_alert.go:511）是一串硬编码的
//     `if rule, ok := enabledRules[entity.RiskRuleIDXxx]; ok { collectXxxCandidates(...) }`。
//     新增一条规则必须新写一个 `collectXxxCandidates` + 在 entity 里登记常量 +
//     在 riskThresholdSpecs 里登记阈值 —— 三处都是代码改动，不是数据录入。
//
//  3. `SetRiskRuleEnabledWithDB` 走 `isKnownRiskRuleID` 白名单（risk_threshold.go:620），
//     未知 ruleId 直接返回 ErrRiskRuleUnknown。即「新建的规则连启停都设不了」。
//
// ⇒ 若硬做 POST /risk/rules，会创建出一条**永远不参与判定**的行：列表里看得见、
//    开关点得动（开关本身会被白名单拦住）、告警永不产生。这正是任务书点名的
//    「造假功能」，所以本轮如实记为不可新增，并把该事实写进页面文案。
// ─────────────────────────────────────────────────────────────────────

// RISK_BUILTIN_RULE_NOTICE 是「规则集内置、不可在管理页新增」的用户可见说明。
//
// 为什么必须有这段文案：管理员看到「规则清单」一定会找「新建」按钮。
// 没有按钮却没有解释，最容易被理解成「这功能忘了做/坏了」，然后是反复提工单。
// 写明「新增需要代码改动 + 需要的是哪一种改动」既解释现状，也给出正确路径。
export const RISK_BUILTIN_RULE_NOTICE =
  '规则集是**内置**的，管理页不支持新增或删除规则 —— 规则的可执行判定写在服务端代码里（每条规则一个判定函数），'
  + '不是数据库里的可编辑行，因此往规则表插行不会让它参与判定。当前可调的是每条规则的**启用开关**、**数值阈值**，'
  + '以及两项**文本配置**（ASN 名单 / 评估间隔）。新增一条规则需要服务端代码改动（登记规则常量 + 判定函数 + 阈值规格）。'

// 非数值配置的 kind → 前端控件类型。
//
// 与后端 service.RiskConfigKind* 逐字对齐；未知 kind 必须**报错而不是猜**
// （猜成数字框会让 ASN 名单无法输入，而界面上不会提示任何原因）。
const RISK_CONFIG_KIND_CONTROL = Object.freeze({
  asn_list: 'textarea',
  positive_int: 'number',
})

// RISK_CONFIG_KIND_LABELS 是控件类型的中文说明（用于占位符/提示）。
const RISK_CONFIG_KIND_LABELS = Object.freeze({
  asn_list: '名单',
  positive_int: '整数',
})

// normalizeRiskConfigItems 校验并归一化 GET /risk/config 的响应。
//
// 三条不可协商的性质：
//  1. 未知 kind 必须抛错 —— 静默降级成文本域会让「1–1440 整数」这一层完全失效
//     （运营填 "abc" 之后只有后端 400，而前端本来能提前拦住）。
//  2. `value` / `defaultValue` 一律用 String() 归一：后端是 string，但一旦它
//     变回数字（历史行是数值形态），界面上 `value === ''` 之类的判空会静默失效。
//  3. **空串是合法值**，不能把空串当成「缺值」丢掉那一项 —— 空串对名单
//     意味着「关闭 R10」，把它过滤掉等于让人无法看到自己刚清空的状态。
export function normalizeRiskConfigItems(items) {
  const list = Array.isArray(items) ? items : []
  return list
    .filter(item => item && typeof item === 'object' && typeof item.key === 'string' && item.key)
    .map(item => {
      const kind = String(item.kind ?? '').trim()
      const control = RISK_CONFIG_KIND_CONTROL[kind]
      if (!control) {
        // 明确的失败信息：让「后端加了新 kind、前端没跟上」立刻可见。
        throw new Error(`风控配置 ${item.key} 的类型 ${kind || '(空)'} 前端不认识，请先更新管理页`)
      }
      const minValue = Number(item.minValue)
      const maxValue = Number(item.maxValue)
      return {
        key: item.key,
        ruleId: String(item.ruleId ?? '').trim(),
        label: String(item.label ?? item.key),
        kind,
        control,
        kindLabel: RISK_CONFIG_KIND_LABELS[kind] || '',
        value: String(item.value ?? ''),
        defaultValue: String(item.defaultValue ?? ''),
        // 只有「有显式配置」才允许重置：没有配置时重置是空操作，
        // 按钮还留着会让人以为「重置过了但没生效」。
        configured: item.configured === true,
        source: String(item.source ?? ''),
        unit: String(item.unit ?? ''),
        help: String(item.help ?? ''),
        // ASN 名单类没有数值区间：min/max 为 0 表示不适用，不能当成「区间是 [0,0]」。
        min: control === 'number' && Number.isFinite(minValue) ? minValue : null,
        max: control === 'number' && Number.isFinite(maxValue) && maxValue > 0 ? maxValue : null,
      }
    })
}

// RISK_CONFIG_EMPTY_MEANING 给出「清空这一项会发生什么」的说明。
//
// 这是本页最容易让人误操作的一处：空串对两项配置的语义**完全不同**，
// 共用一句「留空表示使用默认值」会把「关闭 R10」错说成「回到默认」。
export function describeRiskConfigEmptyMeaning(item) {
  if (!item) return ''
  if (item.kind === 'asn_list') {
    return '留空 = 关闭这条规则（名单为空时该规则完全不告警，这是有意的「宁可漏报不要误报」）'
  }
  if (item.kind === 'positive_int') {
    return `留空 = 回到默认值 ${item.defaultValue}${item.unit ? ' ' + item.unit : ''}`
  }
  return ''
}

// validateRiskConfigDraft 是保存前的**前端类型守卫**（范围裁决仍在后端）。
//
// 只拦「一眼就是打错」的输入，不重复实现后端的白名单/归一化：
//  · 数字控件：必须是纯数字且落在 [min,max] —— 这一步前端有 min/max，能提前拦住；
//  · 文本控件：只校验长度上限（后端会做 ASN 形态校验，前端不抄那套归一化逻辑，
//    抄了就会在「大小写 / 分隔符」上与后端漂移）。
// 返回空串表示通过。
export function validateRiskConfigDraft(item, draft) {
  if (!item?.key) return '配置项参数错误'
  const raw = String(draft ?? '')
  if (item.control === 'number') {
    const trimmed = raw.trim()
    // 空串合法（= 回到默认值），不在这里拦。
    if (!trimmed) return ''
    if (!/^[0-9]+$/.test(trimmed)) {
      return `${item.label} 必须是整数（当前「${trimmed}」）—— 不支持小数、负号与科学计数法`
    }
    const value = Number(trimmed)
    if (Number.isFinite(item.min) && value < item.min) {
      return `${item.label} 不能小于 ${item.min}`
    }
    if (Number.isFinite(item.max) && value > item.max) {
      return `${item.label} 不能大于 ${item.max}（超过这个值会让规则实际失效，请用启用开关停用规则）`
    }
    return ''
  }
  // 文本类（ASN 名单）：只做长度上限。后端 ValidateProxyASNList 负责形态校验。
  if (raw.length > 2000) return `${item.label} 过长（上限 2000 字符）`
  return ''
}

