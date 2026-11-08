import { formatMoneyValue } from './moneyFormat.mjs'
import { limitRestrictedNameLength, normalizeRestrictedName, USERNAME_MAX_LENGTH } from './restrictedName.mjs'

export { USERNAME_MAX_LENGTH }

export function limitUsernameInput(value) {
  return limitRestrictedNameLength(value, USERNAME_MAX_LENGTH)
}

export function resolveAdapterHomePath(user) {
  return String(user?.role || '').trim().toLowerCase() === 'admin'
    ? '/adapter/component'
    : '/adapter/user-usage'
}

export function buildPortalMenuKeys(user) {
  return String(user?.role || '').trim().toLowerCase() === 'admin'
    ? [
        'profile',
        'component',
        'import',
        'user-usage',
        'userlist',
        'loglist',
        'recharge-orders',
        'billing-records',
        'search-account-config',
        'ai-search-config',
        'system-settings',
        'access-tokens',
        'scripts',
        'tickets',
      ]
    : [
        'profile',
        'user-usage',
        'access-tokens',
        'scripts',
        'recharge-orders',
        'billing-records',
        'tickets',
      ]
}

export function buildRegisterSourceLabel(value) {
  const normalized = String(value || '').trim().toLowerCase()
  if (normalized === 'public') {
    return '公开注册'
  }
  if (normalized === 'admin') {
    return '后台创建'
  }
  if (normalized === 'system') {
    return '系统初始化创建'
  }
  if (normalized === 'legacy') {
    return '历史数据'
  }
  return String(value || '-').trim() || '-'
}

export const ADMIN_MANAGED_PASSWORD_MAX_BYTES = 72

function normalizeAdminManagedPassword(passwordValue, emptyMessage = '请输入密码') {
  const password = String(passwordValue ?? '').trim()
  if (!password) {
    throw new Error(emptyMessage)
  }
  if (new TextEncoder().encode(password).length > ADMIN_MANAGED_PASSWORD_MAX_BYTES) {
    throw new Error(`密码不能超过 ${ADMIN_MANAGED_PASSWORD_MAX_BYTES} 字节`)
  }
  return password
}

export function isStrongAdminPassword(passwordValue) {
  const password = String(passwordValue ?? '').trim()
  return Array.from(password).length >= 8
    && /[A-Z]/.test(password)
    && /[a-z]/.test(password)
    && /\d/.test(password)
    && /[^A-Za-z0-9]/.test(password)
}

export async function runAdminPasswordAction(passwordValue, submit, confirmWeak) {
  if (!isStrongAdminPassword(passwordValue)) {
    const confirmed = await confirmWeak()
    if (!confirmed) {
      return false
    }
  }
  await submit()
  return true
}

export function buildResetPasswordPayload(passwordValue) {
  return { password: normalizeAdminManagedPassword(passwordValue, '请输入新密码') }
}

export function buildAdminCreateUserPayload(formValue) {
  const username = normalizeRestrictedName(formValue?.username, USERNAME_MAX_LENGTH, '用户名')
  const password = normalizeAdminManagedPassword(formValue?.password, '请完善新增用户必填项')
  if (!username || !password) {
    throw new Error('请完善新增用户必填项')
  }

  const payload = {
    username,
    password,
    nickname: String(formValue?.nickname || '').trim(),
    role: String(formValue?.role || 'user').trim() || 'user',
    status: String(formValue?.status || 'enabled').trim() || 'enabled',
    registerSource: String(formValue?.registerSource || '').trim() || 'admin',
  }

  const giftEnabled = formValue?.giftEnabled !== false
  payload.giftEnabled = giftEnabled
  if (giftEnabled) {
    const rawGiftAmount = String(formValue?.giftAmount ?? '').trim()
    const giftAmount = rawGiftAmount === '' ? 0 : Number(rawGiftAmount)
    if (!Number.isFinite(giftAmount) || giftAmount < 0) {
      throw new Error('请输入有效的赠送金额')
    }
    payload.giftAmount = giftAmount
  }
  return payload
}

export function buildPersonalUsageBillingRecordParams(page, pageSize) {
  const normalizedPage = Number(page || 1)
  const normalizedPageSize = Number(pageSize || 10)
  return {
    page: Number.isFinite(normalizedPage) && normalizedPage > 0 ? normalizedPage : 1,
    pageSize: Number.isFinite(normalizedPageSize) && normalizedPageSize > 0 ? normalizedPageSize : 10,
    scope: 'self',
  }
}

export function buildPersonalUsageQuestionText(record) {
  const subject = String(record?.subject || '').trim()
  if (subject) {
    return subject
  }

  const questionText = String(record?.questionText || '').trim()
  if (questionText) {
    return questionText
  }

  const question = String(record?.question || '').trim()
  if (question) {
    return question
  }

  const providerLabel = String(record?.providerLabel || '').trim()
  if (providerLabel) {
    return providerLabel
  }

  return '-'
}

export function buildPersonalUsageAnswerSourceText(record) {
  if (String(record?.type || '').trim() === 'balance_adjustment') {
    const operatorId = Number(record?.operatorId)
    return Number.isInteger(operatorId) && operatorId > 0 ? `操作管理员ID：${operatorId}` : '管理员调整余额'
  }
  const detail = String(record?.detail || '').trim()
  if (detail) {
    return detail
  }

  const answerSourceLabel = String(record?.answerSourceLabel || '').trim()
  if (answerSourceLabel) {
    return answerSourceLabel
  }

  const providerLabel = String(record?.providerLabel || '').trim()
  if (providerLabel) {
    return providerLabel
  }

  const provider = String(record?.provider || '').trim()
  if (provider) {
    return provider
  }

  return '-'
}

export function buildBillingRecordTypeText(record) {
  const typeLabel = String(record?.typeLabel || '').trim()
  if (typeLabel) {
    return typeLabel
  }
  const type = String(record?.type || '').trim()
  if (type === 'recharge') {
    return '充值入账'
  }
  if (type === 'normal_search') {
    return '题目消费'
  }
  if (type === 'balance_adjustment') {
    return '管理员调整余额'
  }
  if (type === 'script_reservation') {
    return '脚本预扣'
  }
  if (type === 'script_refund') {
    return '脚本退款'
  }
  if (type === 'script_activation_purchase') {
    return '脚本激活码购买'
  }
  return type || '题目消费'
}

/**
 * 账单的搜索模式展示文本：深度思考 / 普通搜索。
 *
 * 后端已给出 searchModeLabel，这里再兜一层原始值映射（searchMode=deep|normal），
 * 都取不到时返回占位符（非搜题账单、或加列之前的历史行）。
 *
 * @param {object} record
 * @returns {string}
 */
export function buildBillingRecordModeText(record) {
  const label = String(record?.searchModeLabel || '').trim()
  if (label) {
    return label
  }
  const mode = String(record?.searchMode || '').trim().toLowerCase()
  if (mode === 'deep') {
    return '深度思考'
  }
  if (mode === 'deep_native') {
    return '深度思考 原生工具'
  }
  if (mode === 'deep_mcp') {
    return '深度思考 MCP编排'
  }
  if (mode === 'normal') {
    return '普通搜索'
  }
  return '-'
}

export function buildBillingRecordSubjectText(record) {
  return buildPersonalUsageQuestionText(record)
}

export function buildBillingRecordListSubjectText(record) {
  if (String(record?.type || '').trim() === 'normal_search') {
    const question = String(record?.questionText || '').trim()
    if (question) return question
  }
  return buildBillingRecordSubjectText(record)
}

export const billingTableColumnWidths = Object.freeze({
  id: 104,
  username: 170,
  typeLabel: 124,
  subject: 170,
  subjectExpanded: 236,
  amount: 120,
  source: 160,
  searchMode: 170,
  referenceNo: 300,
  effectiveMultiplier: 104,
  cacheHitRate: 112,
  accessTokenName: 122,
  createdAt: 112,
  clientIp: 270,
  feeDetail: 88,
})

export function formatBillingRecordTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return String(value)
  const parts = Object.fromEntries(new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
  }).formatToParts(date).map(({ type, value: partValue }) => [type, partValue]))
  return `${parts.year}-${parts.month}-${parts.day}\n${parts.hour}:${parts.minute}:${parts.second}`
}

export function buildBillingRecordSourceText(record) {
  return buildPersonalUsageAnswerSourceText(record)
}

export function buildBillingRecordReferenceText(record) {
  const referenceNo = String(record?.referenceNo || '').trim()
  if (referenceNo) {
    return referenceNo
  }
  const requestId = String(record?.requestId || '').trim()
  if (requestId) {
    return requestId
  }
  return '-'
}

export function buildBillingRecordFinalMultiplierText(record) {
  const multiplier = String(record?.finalMultiplier || '').trim()
  if (multiplier) {
    return multiplier
  }
  if (String(record?.type || '').trim() === 'normal_search') {
    return '1x'
  }
  return '-'
}

export function buildBillingRecordEffectiveMultiplierText(record) {
  return String(record?.effectiveMultiplier || '').trim() || '-'
}

export function formatBillingCacheHitRate(value) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > 1) return '-'
  return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value * 100)}%`
}

export function buildBillingRecordAmountText(record) {
  const amount = Number(record?.amount || 0)
  // 金额统一最多 6 位小数，保证小额 AI 扣费（如 0.000062）不会被显示成 0。
  const formatted = formatMoneyValue(Number.isFinite(amount) ? amount : 0)
  const direction = String(record?.direction || '').trim()
  if (direction === 'income') {
    return `+¥ ${formatted}`
  }
  if (direction === 'expense') {
    return `-¥ ${formatted}`
  }
  return `¥ ${formatted}`
}

export function buildBillingRecordFeeDetails(record) {
  const feeDetails = Array.isArray(record?.feeDetails) ? record.feeDetails : []
  const normalized = feeDetails
    .map((item) => {
      const normalizedItem = {
        provider: String(item?.provider || '').trim(),
        providerLabel: String(item?.providerLabel || item?.provider || '').trim() || '-',
        amount: Number(item?.amount || 0),
      }
      const url = String(item?.url || '').trim()
      if (url) normalizedItem.url = url
      const category = String(item?.category || '').trim()
      const callIndex = Number(item?.callIndex || 0)
      if (category) normalizedItem.category = category
      if (Number.isInteger(callIndex) && callIndex > 0) normalizedItem.callIndex = callIndex
      if (String(item?.detail || '').trim()) normalizedItem.detail = String(item.detail).trim()
      if (item?.cacheHitRate !== undefined) normalizedItem.cacheHitRate = item.cacheHitRate
      return normalizedItem
    })
    .filter((item) => Number.isFinite(item.amount))

  if (normalized.length > 0) {
    return normalized
  }

  const fallbackAmount = Number(record?.amount || 0)
  return [{
    provider: String(record?.provider || '').trim(),
    providerLabel: String(record?.providerLabel || record?.answerSourceLabel || record?.provider || '').trim() || '-',
    amount: Number.isFinite(fallbackAmount) ? fallbackAmount : 0,
  }]
}

function parseBillingRecordFeeDetailFields(detail) {
  const fields = Object.create(null)
  for (const segment of String(detail || '').split(/[；;]/)) {
    const trimmed = segment.trim()
    if (!trimmed) continue
    const equalsIndex = trimmed.indexOf('=')
    const colonIndex = trimmed.search(/[：:]/)
    const separatorIndex = equalsIndex > 0 && (colonIndex < 0 || equalsIndex < colonIndex)
      ? equalsIndex
      : colonIndex
    if (separatorIndex > 0) {
      fields[trimmed.slice(0, separatorIndex).trim()] = trimmed.slice(separatorIndex + 1).trim()
    }
  }
  return fields
}

function readBillingRecordFeeField(fields, names) {
  for (const name of names) {
    const value = String(fields?.[name] || '').trim()
    if (value) return value
  }
  return ''
}

function formatBillingRecordFeeMultiplier(value) {
  const raw = String(value || '').trim().replace(/×/g, 'x')
  if (!raw) return ''
  const numeric = Number(raw.replace(/x$/i, ''))
  if (Number.isFinite(numeric)) {
    return `${formatMoneyValue(Math.round(numeric * 100) / 100)}x`
  }
  return /x$/i.test(raw) ? raw : `${raw}x`
}

function formatBillingRecordFeeTokens(value) {
  const raw = String(value || '').trim()
  if (!raw) return ''
  const numeric = Number(raw.replace(/,/g, ''))
  if (Number.isFinite(numeric) && numeric >= 0) {
    return new Intl.NumberFormat('zh-CN').format(Math.trunc(numeric))
  }
  return raw
}

function buildBillingRecordFeeItemTitle(item) {
  const category = String(item?.category || '').trim()
  const callSuffix = item?.callIndex > 0 ? ` 第${item.callIndex}次` : ''
  if (item?.provider === 'ai_search') {
    return '联网搜索'
  }
  if (item?.provider === 'tiku_search') {
    return `本地题库检索${callSuffix}`
  }
  if (item?.provider === 'script_answer') {
    return `脚本答题${callSuffix}`
  }
  if (item?.provider === 'ai' || category === 'AI模型调用') {
    return `AI 模型调用${callSuffix}`
  }
  if (item?.provider === 'web_read' || category === '网页转Markdown') {
    return `浏览网页${callSuffix}`
  }
  if (item?.provider === 'ai_ocr' || category === '图片 OCR') {
    return `图片 OCR${callSuffix}`
  }
  if (category) {
    return `${category}${callSuffix}`
  }
  return String(item?.providerLabel || '-').trim() || '-'
}

function buildBillingRecordFeeItemMeta(item, options = {}) {
  const fields = parseBillingRecordFeeDetailFields(item?.detail)
  const showAIModelMeta = options.showAIModelMeta !== false
  if (item?.provider === 'ai_search') {
    const keyword = readBillingRecordFeeField(fields, ['搜索词', '关键词', 'query', 'searchQuery'])
    return keyword ? [{ label: '关键词', value: keyword, keyword: true }] : []
  }
  if (item?.provider === 'tiku_search') {
    const meta = []
    const query = readBillingRecordFeeField(fields, ['query', 'tikuSearchQuery', '检索词', '搜索词'])
    const candidateCount = readBillingRecordFeeField(fields, ['candidate_count', 'candidateCount', '候选数'])
    if (query) meta.push({ label: '检索词', value: query })
    if (candidateCount) meta.push({ label: '候选数', value: candidateCount })
    return meta
  }
  if ((item?.provider === 'ai' || item?.category === 'AI模型调用') && showAIModelMeta) {
    const meta = []
    const model = readBillingRecordFeeField(fields, ['model', '模型'])
    const multiplier = formatBillingRecordFeeMultiplier(
      readBillingRecordFeeField(fields, ['finalMultiplier', '最终倍率', 'multiplier']),
    )
    const tokens = formatBillingRecordFeeTokens(
      readBillingRecordFeeField(fields, ['totalTokens', 'Token数量', 'Token 数量', 'tokens']),
    )
    if (model) meta.push({ label: '模型', value: model })
    if (multiplier) meta.push({ label: '最终倍率', value: multiplier })
    if (tokens) meta.push({ label: 'Token数量', value: tokens })
    meta.push({ label: '缓存命中率', value: formatBillingCacheHitRate(item?.cacheHitRate) })
    return meta
  }
  if (item?.provider === 'web_read') {
    const url = String(item?.url || '').trim() || readBillingRecordFeeField(fields, ['URL', 'url', '网页'])
    return url ? [{ label: '网页', value: url }] : []
  }
  if (item?.provider === 'ai_ocr') {
    const imageURL = readBillingRecordFeeField(fields, ['图片', 'ocrImageURL', 'imageURL'])
    const textChars = readBillingRecordFeeField(fields, ['文字字符数', 'ocrTextChars', 'textChars'])
    const meta = []
    if (imageURL) meta.push({ label: '图片', value: imageURL })
    if (textChars) meta.push({ label: '文字字符数', value: textChars })
    return meta
  }
  return []
}

export function buildBillingDetailViewModel(record, options = {}) {
  const admin = options.viewerScope === 'admin'
  const direction = String(record?.direction || '').trim()
  const metadata = []
  if (String(record?.operatorUsername || '').trim()) {
    metadata.push({ label: '操作管理员', value: `${record.operatorUsername}${record.operatorId ? `（ID ${record.operatorId}）` : ''}` })
  }
  if (record?.type === 'balance_adjustment') {
    metadata.push({ label: '余额变化', value: `¥ ${formatMoneyValue(record.balanceBefore || 0)} → ¥ ${formatMoneyValue(record.balanceAfter || 0)}` })
    const adjustmentMode = { increase: '添加余额', decrease: '减少余额', override: '覆盖余额' }[record?.adjustmentMode]
    if (adjustmentMode) metadata.push({ label: '调整模式', value: adjustmentMode })
    if (admin) metadata.push({ label: '账单流水展示', value: record.showInStatement === false ? '隐藏' : '显示' })
  }
  return {
    id: record?.id,
    direction,
    directionLabel: direction === 'income' ? '收入账单' : direction === 'expense' ? '支出账单' : '账单详情',
    typeLabel: buildBillingRecordTypeText(record),
    subject: buildBillingRecordSubjectText(record),
    mode: buildBillingRecordModeText(record),
    amount: buildBillingRecordAmountText(record),
    items: buildBillingRecordFeeDetails(record).map((item, index) => ({
      key: `${index}:${item.provider}:${item.callIndex || ''}`,
      title: buildBillingRecordFeeItemTitle(item),
      amount: buildBillingRecordAmountText({ amount: item.amount, direction }),
      metadata: buildBillingRecordFeeItemMeta(item, { showAIModelMeta: admin }),
    })),
    metadata,
    showInStatement: record?.showInStatement !== false,
    canEditStatement: admin && options.canEditStatement === true,
  }
}

export function buildBillingRecordFeeDetailHtml(record, options = {}) {
  const details = buildBillingRecordFeeDetails(record)
  const showStatementControl = options.showStatementControl !== false
  const direction = String(record?.direction || '').trim()
  const subject = buildBillingRecordSubjectText(record)
  const directionLabel = direction === 'income' ? '收入账单' : direction === 'expense' ? '支出账单' : '账单详情'
  const typeLabel = buildBillingRecordTypeText(record)
  const modeLabel = buildBillingRecordModeText(record)
  const showMode = modeLabel && modeLabel !== '-'
  const rows = details.map((item) => {
    const meta = buildBillingRecordFeeItemMeta(item, options)
    const metaHtml = meta.length
      ? `<div class="billing-fee-item-meta">${meta.map((entry) => `
          <span class="billing-fee-meta-item${entry.keyword ? ' billing-fee-meta-item--keyword' : ''}">
            <span class="billing-fee-meta-label">${escapeHtmlText(entry.label)}：</span>
            <span class="billing-fee-meta-value">${escapeHtmlText(entry.value)}</span>
          </span>
        `).join('')}</div>`
      : ''
    return `
      <div class="billing-fee-item">
        <div class="billing-fee-item-head">
          <span class="billing-fee-provider">${escapeHtmlText(buildBillingRecordFeeItemTitle(item))}</span>
          <strong class="billing-fee-amount billing-fee-amount--${direction === 'income' ? 'income' : 'expense'}">
            ${escapeHtmlText(buildBillingRecordAmountText({ amount: item.amount, direction }))}
          </strong>
        </div>
        ${metaHtml}
      </div>
    `
  }).join('')
  const totalAmount = Number(record?.amount || 0)
  const totalText = buildBillingRecordAmountText({
    amount: Number.isFinite(totalAmount) ? totalAmount : 0,
    direction,
  })
  const metadata = []
  if (String(record?.operatorUsername || '').trim()) {
    metadata.push(`操作管理员：${record.operatorUsername}${record.operatorId ? `（ID ${record.operatorId}）` : ''}`)
  }
  if (record?.type === 'balance_adjustment') {
    metadata.push(`余额变化：¥ ${formatMoneyValue(record.balanceBefore || 0)} → ¥ ${formatMoneyValue(record.balanceAfter || 0)}`)
    const adjustmentModes = { increase: '添加余额', decrease: '减少余额', override: '覆盖余额' }
    const adjustmentMode = adjustmentModes[String(record?.adjustmentMode || '').trim()]
    if (adjustmentMode) metadata.push(`调整模式：${adjustmentMode}`)
    metadata.push(`账单流水展示：${record.showInStatement === false ? '隐藏' : '显示'}`)
  }
  if (showStatementControl && record?.canEditStatement) {
    metadata.push('__STATEMENT_CONTROL__')
  }

  return `
    <div class="billing-fee-detail">
      <section class="billing-fee-card billing-fee-card--question billing-fee-overview">
        <div class="billing-fee-overview-head">
          <div class="billing-fee-overview-tags">
            <span class="billing-fee-status billing-fee-status--${direction === 'income' ? 'income' : 'expense'}">${escapeHtmlText(directionLabel)}</span>
            <span class="billing-fee-type">${escapeHtmlText(typeLabel)}</span>
          </div>
          ${showMode ? `<span class="billing-fee-mode">${escapeHtmlText(modeLabel)}</span>` : ''}
        </div>
        <div class="billing-fee-question">${escapeHtmlText(subject)}</div>
      </section>
      <section class="billing-fee-card billing-fee-card--cost">
        <div class="billing-fee-section-head">
          <div>
            <div class="billing-fee-section-title">费用详情</div>
            <div class="billing-fee-section-subtitle">按实际调用拆分</div>
          </div>
          <span class="billing-fee-count">${details.length} 项</span>
        </div>
        <div class="billing-fee-list">${rows}</div>
        <div class="billing-fee-total">
          <span>合计</span>
          <strong class="billing-fee-amount billing-fee-amount--${direction === 'income' ? 'income' : 'expense'}">
            ${escapeHtmlText(totalText)}
          </strong>
        </div>
      </section>
      ${metadata.length ? `<section class="billing-fee-card billing-fee-card--metadata">${metadata.map((item) => item === '__STATEMENT_CONTROL__' ? `<div class="billing-statement-control"><button type="button" role="switch" aria-label="显示在用户账单流水" aria-checked="${record.showInStatement !== false}" class="billing-statement-switch billing-statement-toggle" data-show-in-statement="${record.showInStatement !== false}"><span class="billing-statement-switch__thumb" aria-hidden="true"></span></button><span class="billing-statement-switch__label">显示在用户账单流水</span></div>` : `<div class="billing-fee-item-detail">${escapeHtmlText(item)}</div>`).join('')}</section>` : ''}
    </div>
  `
}

export function buildUserProfileUpdatePayload(formValue) {
  return {
    id: formValue?.id,
    nickname: String(formValue?.nickname || '').trim(),
    role: String(formValue?.role || 'user').trim(),
    status: String(formValue?.status || 'enabled').trim(),
    phone: String(formValue?.phone || '').trim(),
    forceReassignPhone: !!formValue?.forceReassignPhone,
    wechatId: String(formValue?.wechatId || '').trim(),
    forceReassignWechat: !!formValue?.forceReassignWechat,
  }
}

// Kept for legacy callers; current profile forms use the balance-free helper.
export function buildUserBalanceUpdatePayload(formValue) {
  const payload = buildUserProfileUpdatePayload(formValue)

  const rawBalance = String(formValue?.balance ?? '').trim()
  if (!rawBalance) {
    return payload
  }

  const balance = Number(rawBalance)
  if (!Number.isFinite(balance)) {
    throw new Error('请输入有效的余额数值')
  }

  payload.balance = balance
  payload.balanceMode = String(formValue?.balanceMode || 'override').trim() || 'override'
  return payload
}

export function buildUserBalanceAdjustmentPayload(formValue) {
  const mode = String(formValue?.mode || '').trim()
  if (!['increase', 'decrease', 'override'].includes(mode)) {
    throw new Error('请选择有效的余额修改模式')
  }
  const rawAmount = String(formValue?.amount ?? '').trim()
  const amount = Number(rawAmount)
  if (!/^[+-]?(?:\d+(?:\.\d{0,6})?|\.\d{1,6})$/.test(rawAmount) || !Number.isFinite(amount) || Math.abs(amount) >= 1e12) {
    throw new Error('请输入最多 6 位小数的有效金额')
  }
  if (mode !== 'override' && amount < 0) {
    throw new Error('添加或减少金额不能为负数')
  }
  return { id: formValue?.id, mode, amount, showInStatement: formValue?.showInStatement !== false }
}

function escapeHtmlText(value) {
  return String(value || '').replace(/[&<>"']/g, (character) => {
    const entities = {
      '&': '&amp;',
      '<': '&lt;',
      '>': '&gt;',
      '"': '&quot;',
      "'": '&#39;',
    }
    return entities[character]
  })
}
