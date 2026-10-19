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
      ]
    : [
        'profile',
        'user-usage',
        'access-tokens',
        'recharge-orders',
        'billing-records',
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

export function buildResetPasswordPayload(passwordValue) {
  const password = String(passwordValue || '').trim()
  if (!password) {
    throw new Error('请输入新密码')
  }
  return { password }
}

export function buildAdminCreateUserPayload(formValue) {
  const username = normalizeRestrictedName(formValue?.username, USERNAME_MAX_LENGTH, '用户名')
  const password = String(formValue?.password || '').trim()
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
  if (mode === 'normal') {
    return '普通搜索'
  }
  return '-'
}

export function buildBillingRecordSubjectText(record) {
  return buildPersonalUsageQuestionText(record)
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
    .map((item) => ({
      provider: String(item?.provider || '').trim(),
      providerLabel: String(item?.providerLabel || item?.provider || '').trim() || '-',
      amount: Number(item?.amount || 0),
      ...(String(item?.detail || '').trim() ? { detail: String(item.detail).trim() } : {}),
    }))
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

export function buildBillingRecordFeeDetailHtml(record) {
  const details = buildBillingRecordFeeDetails(record)
  const direction = String(record?.direction || '').trim()
  const subject = buildBillingRecordSubjectText(record)
  const rows = details.map((item) => `
    <div class="billing-fee-item">
      <span class="billing-fee-provider">${escapeHtmlText(item.providerLabel)}</span>
      <strong class="billing-fee-amount billing-fee-amount--${direction === 'income' ? 'income' : 'expense'}">
        ${escapeHtmlText(buildBillingRecordAmountText({ amount: item.amount, direction }))}
      </strong>
    </div>
    ${item.detail ? `<div class="billing-fee-item-detail">${escapeHtmlText(item.detail)}</div>` : ''}
  `).join('')
  const totalAmount = Number(record?.amount || 0)
  const totalText = buildBillingRecordAmountText({
    amount: Number.isFinite(totalAmount) ? totalAmount : 0,
    direction,
  })

  return `
    <div class="billing-fee-detail">
      <section class="billing-fee-card billing-fee-card--question">
        <div class="billing-fee-section-title">题目</div>
        <div class="billing-fee-question">${escapeHtmlText(subject)}</div>
      </section>
      <section class="billing-fee-card billing-fee-card--cost">
        <div class="billing-fee-section-title">费用详情</div>
        <div class="billing-fee-list">${rows}</div>
        <div class="billing-fee-total">
          <span>合计</span>
          <strong class="billing-fee-amount billing-fee-amount--${direction === 'income' ? 'income' : 'expense'}">
            ${escapeHtmlText(totalText)}
          </strong>
        </div>
      </section>
    </div>
  `
}

export function buildUserBalanceUpdatePayload(formValue) {
  const payload = {
    id: formValue?.id,
    nickname: String(formValue?.nickname || '').trim(),
    role: String(formValue?.role || 'user').trim(),
    status: String(formValue?.status || 'enabled').trim(),
    phone: String(formValue?.phone || '').trim(),
    forceReassignPhone: !!formValue?.forceReassignPhone,
    wechatId: String(formValue?.wechatId || '').trim(),
    forceReassignWechat: !!formValue?.forceReassignWechat,
  }

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
