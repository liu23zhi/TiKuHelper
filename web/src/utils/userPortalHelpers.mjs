export function resolveAdapterHomePath(user) {
  return String(user?.role || '').trim().toLowerCase() === 'admin'
    ? '/adapter/component'
    : '/adapter/user-usage'
}

export function buildPortalMenuKeys(user) {
  return String(user?.role || '').trim().toLowerCase() === 'admin'
    ? [
        'component',
        'import',
        'user-usage',
        'userlist',
        'loglist',
        'billing-records',
        'search-account-config',
        'ai-search-config',
        'system-settings',
        'access-tokens',
      ]
    : [
        'user-usage',
        'access-tokens',
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

export function buildUserBalanceUpdatePayload(formValue) {
  const payload = {
    id: formValue?.id,
    nickname: String(formValue?.nickname || '').trim(),
    role: String(formValue?.role || 'user').trim(),
    status: String(formValue?.status || 'enabled').trim(),
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
