export const REFUND_PROOF_LIMIT = 2 * 1024 * 1024

const STATUS_LABELS = Object.freeze({
  pending_review: '待审核',
  rejected: '审核未通过',
  frozen: '资金已冻结，待处理',
  processing: '渠道处理中，资金仍冻结',
  unknown: '结果待核对，资金仍冻结',
  succeeded: '退款成功',
  partially_succeeded: '部分退款成功，其余资金已解冻',
  failed: '退款未完成，冻结已解除',
})
export const REFUND_STATUS_OPTIONS = Object.freeze(Object.entries(STATUS_LABELS).map(([value, label]) => ({ value, label })))

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

export const REFUND_ALLOCATION_MISMATCH_MESSAGE = '审核金额须等于订单分配合计。请先点「修改退款申请」调整申请金额和逐笔分配。'

const REFUND_FAILURE_MESSAGES = Object.freeze({
  REFUND_INVALID: '请核对退款参数、金额及订单分配。',
  REFUND_BINDINGS_REVIEWED: '客服已核对并关联订单；可修改理由，调整金额或退款方式请联系管理员。',
  REFUND_ALLOCATION_MISMATCH: REFUND_ALLOCATION_MISMATCH_MESSAGE,
  REFUND_FORBIDDEN: '无权执行此退款操作。',
  REFUND_VERIFICATION_REQUIRED: '请核对收款信息和承诺书签署。',
  REFUND_IMAGE_UNAVAILABLE: '收款凭证图片不可用，请核对图片。',
  REFUND_ORDER_UNAVAILABLE: '充值订单已被使用或不符合退款条件，请重新选择。',
  REFUND_INSUFFICIENT_BALANCE: '可用余额不足，未冻结资金。',
  REFUND_PRINCIPAL_UNAVAILABLE: '可退本金不足，已消耗的赠金不能退成现金。',
  REFUND_PROOF_REQUIRED: '请先核验收款码和签名，或受限凭证。',
  REFUND_STATE_CONFLICT: '退款状态已变化，请刷新核对。',
  REFUND_CHANNEL_UNAVAILABLE: '原支付渠道未启用或不可用。',
})

export function refundFailureMessage(error) {
  const status = error?.response?.status ?? error?.httpStatus ?? error?.status
  const code = error?.response?.data?.code ?? error?.code
  if (!Number.isInteger(status) || status < 400) return ''
  return REFUND_FAILURE_MESSAGES[code] || (status < 500 ? '退款操作未通过，请核对填写内容和当前状态。' : '')
}

export function refundListData(payload) {
  const rows = Array.isArray(payload) ? payload : payload?.items
  if (!Array.isArray(rows) || rows.some((row) => !row || !validRefundID(row.id))) {
    throw new Error('退款列表格式无效')
  }
  return rows
}
