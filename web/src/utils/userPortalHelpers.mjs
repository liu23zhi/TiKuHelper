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
  const username = String(formValue?.username || '').trim()
  const password = String(formValue?.password || '').trim()
  if (!username || !password) {
    throw new Error('请完善新增用户必填项')
  }

  return {
    username,
    password,
    nickname: String(formValue?.nickname || '').trim(),
    role: String(formValue?.role || 'user').trim() || 'user',
    status: String(formValue?.status || 'enabled').trim() || 'enabled',
    registerSource: String(formValue?.registerSource || '').trim() || 'admin',
  }
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

export function buildBillingRecordAmountText(record) {
  const amount = Number(record?.amount || 0)
  const formatted = Number.isFinite(amount) ? amount.toFixed(2) : '0.00'
  const direction = String(record?.direction || '').trim()
  if (direction === 'income') {
    return `+¥ ${formatted}`
  }
  if (direction === 'expense') {
    return `-¥ ${formatted}`
  }
  return `¥ ${formatted}`
}

export function buildUserBalanceUpdatePayload(formValue) {
  const payload = {
    id: formValue?.id,
    nickname: String(formValue?.nickname || '').trim(),
    role: String(formValue?.role || 'user').trim(),
    status: String(formValue?.status || 'enabled').trim(),
    phone: String(formValue?.phone || '').trim(),
    forceReassignPhone: !!formValue?.forceReassignPhone,
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
