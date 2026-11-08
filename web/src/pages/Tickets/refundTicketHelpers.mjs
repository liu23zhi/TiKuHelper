export const REFUND_PROOF_LIMIT = 2 * 1024 * 1024

const STATUS_LABELS = Object.freeze({
  pending_review: '待审核',
  rejected: '审核未通过',
  frozen: '资金已冻结，待处理',
  processing: '渠道处理中，资金仍冻结',
  unknown: '结果待核对，资金仍冻结',
  succeeded: '退款成功',
  failed: '退款未完成，冻结已解除',
})

export function refundStatusLabel(status) {
  return Object.hasOwn(STATUS_LABELS, status) ? STATUS_LABELS[status] : '状态待确认'
}

export function validRefundID(value) {
  if (typeof value === 'number' && !Number.isSafeInteger(value)) return ''
  const text = String(value ?? '')
  return /^[1-9]\d{0,18}$/.test(text) ? text : ''
}

export function parseRefundYuanCents(value) {
  const result = /^(0|[1-9]\d{0,11})(?:\.(\d{1,2}))?$/.exec(String(value ?? '').trim())
  if (!result) return null
  const cents = Number(result[1]) * 100 + Number((result[2] ?? '').padEnd(2, '0'))
  return Number.isSafeInteger(cents) && cents > 0 ? cents : null
}

export function formatRefundCents(value) {
  if (!Number.isSafeInteger(value) || value < 0) return '—'
  return String(Math.floor(value / 100)) + '.' + String(value % 100).padStart(2, '0')
}

export function refundProofAllowed(file) {
  return Boolean(file && Number.isSafeInteger(file.size) && file.size > 0 &&
    file.size <= REFUND_PROOF_LIMIT && (file.type === 'image/png' || file.type === 'image/jpeg'))
}

export function refundResponseData(response) {
  if (!Number.isInteger(response?.status) || response.status < 200 || response.status >= 300) {
    const failure = new Error('退款请求未成功')
    failure.httpStatus = response?.status
    failure.code = typeof response?.data?.code === 'string' ? response.data.code : ''
    throw failure
  }
  return response.data
}

export function refundListData(payload) {
  const rows = Array.isArray(payload) ? payload : payload?.items
  if (!Array.isArray(rows) || rows.some((row) => !row || !validRefundID(row.id))) {
    throw new Error('退款列表格式无效')
  }
  return rows
}
