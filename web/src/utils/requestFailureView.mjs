import { getTransportRequestId } from './requestHelpers.mjs'

const inheritedFailureViews = new WeakMap()
function isFailureObject(value) {
  return value !== null && (typeof value === 'object' || typeof value === 'function')
}

// Preserve object identity for the transport WeakMap; malformed return/rejection
// values still need visible failure feedback, without inventing an HTTP/trace ID.
export function ensureRequestFailure(value) {
  return value !== null && (typeof value === 'object' || typeof value === 'function')
    ? value : Object.freeze({ code: 'MALFORMED_RESPONSE' })
}

// Keep only the classifier's fixed public semantics, never a source reference or
// an unknown caller fallback. The ID is an association guard, not a display ID;
// wrappers transfer validated transport metadata before transferring this view.
export function inheritRequestFailureView(target, source) {
  if (!isFailureObject(target)) return target
  const view = buildRequestFailureView(source)
  const expectedTransportId = getTransportRequestId(source)
  inheritedFailureViews.delete(target)
  if (view && view.kind !== 'unknown') {
    const { kind, title, description, allowRetry } = view
    inheritedFailureViews.set(target, Object.freeze({
      view: Object.freeze({ kind, title, description, allowRetry }),
      expectedTransportId,
    }))
  }
  return target
}

// Do not render a server message, Error.message, config, URL, body or credential.
// The optional fallback is fixed public copy supplied by the call site.
function read(value, key) {
  try { return value !== null && (typeof value === 'object' || typeof value === 'function') ? value[key] : undefined } catch { return undefined }
}
export function buildRequestFailureView(failure, message = '请求未完成，请稍后核对。') {
  if (failure === null || failure === undefined) return null
  const inherited = isFailureObject(failure) && inheritedFailureViews.get(failure)
  if (inherited) {
    const diagnosticId = getTransportRequestId(failure)
    if (diagnosticId === inherited.expectedTransportId) {
      return Object.freeze({ ...inherited.view, diagnosticId })
    }
    // Reusing only the ID channel must not pair a new ID with old semantics.
    inheritedFailureViews.delete(failure)
  }
  const response = read(failure, 'response')
  const body = read(response, 'data') ?? read(failure, 'data')
  const status = read(response, 'status') ?? read(failure, 'status')
  const business = read(read(body, 'error'), 'code') ?? read(body, 'code')
  const code = read(failure, 'code')
  const name = read(failure, 'name')
  let kind = 'unknown', title = '请求未完成', description = typeof message === 'string' && message ? message : '请求未完成，请稍后核对。'
  let allowRetry = true
  const choose = (k, t, d, retry = true) => { kind = k; title = t; description = d; allowRetry = retry }
  if (code === 'UNKNOWN_OUTCOME') choose('unknown-outcome', '操作结果尚未确认', '请先刷新核对结果，勿重复提交或支付。', false)
  else if (business === 'INSUFFICIENT_BALANCE') choose('balance', '账户余额不足', '请核对账户余额；已执行操作请先检查记录，勿直接重复提交。', false)
  else if (business === 'ACCESS_TOKEN_QUOTA_EXCEEDED') choose('quota', 'Key 额度受限', '请核对该 Key 的额度上限与已用额度，确认后再操作。', false)
  else if (business === 'NO_ANSWER') choose('no-answer', '本次未取得可用答案', '可调整问题条件；如已发生模型调用，请先核对记录。', false)
  else if (business === 'AI_UNAVAILABLE') choose('unavailable', 'AI 服务暂不可用', '请先确认服务配置，或选择其他方式。', false)
  else if (business === 'INVALID_INPUT') choose('input', '请求参数不符合要求', '请检查当前输入，不会自动重新提交。', false)
  else if (business === 'UNSAFE_TO_ANSWER') choose('unavailable', '本次无法提供可用答案', '请核对问题内容，已执行操作请先检查记录。', false)
  else if (status === 401) choose('unauthorized', '登录状态已失效', '请重新登录后再操作。', false)
  else if (status === 403) choose('forbidden', '当前账号无权执行此操作', '请确认账号权限后再操作。')
  else if (status === 429) choose('rate-limit', '请求过于频繁', '请稍后使用页面上的明确入口重试。')
  else if (code === 'ERR_CANCELED' || name === 'AbortError') choose('canceled', '请求已取消或上下文已退役', '请在当前页面核对状态，尚未确认的操作不要重复提交。', false)
  else if (status === 408 || code === 'ECONNABORTED' || code === 'ETIMEDOUT' || read(failure, 'message') === 'Request deadline exceeded') choose('timeout', '请求超时', '未收到完整结果；已执行操作请先核对记录。')
  else if (business === 'UPSTREAM_ERROR' || (typeof status === 'number' && status >= 500 && status <= 599)) choose('upstream', '服务处理失败', '请稍后核对结果，需要重试时仅使用当前页面的明确入口。')
  else if (['ERR_NETWORK', 'ECONNRESET'].includes(code) || ['Network Error', 'Failed to fetch'].includes(read(failure, 'message'))) choose('network', '网络连接中断', '请检查连接；已提交的操作请先核对结果。')
  let diagnosticId
  try { diagnosticId = getTransportRequestId(failure) } catch { /* auxiliary only */ }
  return Object.freeze({ kind, title, description, allowRetry, diagnosticId })
}
