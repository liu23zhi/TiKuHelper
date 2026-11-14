const TRANSIENT_STATUSES = new Set([408, 425, 429])
const POLL_PATHS = new Set([
  '/api/public/wechat-login/status',
  '/api/public/wechat-register/status',
])
const COSTED_OR_CONSUMING_READ_PATHS = new Set([
  '/api/user/wechat/scan-id',
  '/api/public/wechat/user',
  '/api/public/captcha/remote-gen',
  '/api/ai-search-config/models',
])
const KEYED_MUTATION_PATHS = new Set([
  '/api/recharge-orders',
  '/api/public/sms-code',
  '/api/user/phone/sms-code',
])

function pathOf(url) {
  try {
    return new URL(url, 'http://local.invalid').pathname
  } catch {
    return ''
  }
}

export function resolveRequestPolicy(config) {
  if (config.retryPolicy) return config.retryPolicy
  const method = String(config.method || 'get').toLowerCase()
  const path = pathOf(config.url)
  if (method === 'get') {
    if (POLL_PATHS.has(path)) return 'poll'
    return path.startsWith('/api/uapi/') || COSTED_OR_CONSUMING_READ_PATHS.has(path)
      ? 'single' : 'read'
  }
  if (method === 'post') {
    if (path === '/api/questions/search') return 'read'
    if (KEYED_MUTATION_PATHS.has(path) || /^\/api\/user\/[^/]+\/balance$/.test(path)) {
      return 'keyed'
    }
    return 'single'
  }
  return 'single'
}

function isTransientStatus(status) {
  return TRANSIENT_STATUSES.has(status) || (status >= 500 && status <= 599)
}

function isTransientError(error) {
  if (error?.name === 'AbortError' || error?.code === 'ERR_CANCELED') return false
  if (error?.response) return isTransientStatus(error.response.status)
  if (['ECONNABORTED', 'ETIMEDOUT', 'ERR_NETWORK', 'ECONNRESET'].includes(error?.code)) return true
  return /^(Network Error|Failed to fetch)$/i.test(error?.message || '')
}

export function toUserRequestError(config, error) {
  const method = String(config.method || 'get').toLowerCase()
  if (method === 'get' || config.signal?.aborted || error?.response || !isTransientError(error)) return error
  const uncertain = new Error('网络中断，操作结果未确认。请先刷新核对，勿重复提交。', { cause: error })
  uncertain.code = 'UNKNOWN_OUTCOME'
  return uncertain
}

function retryAfterMs(response, now) {
  const raw = response?.headers?.['retry-after'] ?? response?.headers?.get?.('retry-after')
  if (raw === undefined || raw === null || raw === '') return null
  const seconds = Number(raw)
  if (Number.isFinite(seconds) && seconds >= 0) return seconds * 1000
  const date = Date.parse(raw)
  return Number.isFinite(date) ? Math.max(0, date - now()) : null
}

function abortError() {
  const error = new Error('Request canceled')
  error.name = 'AbortError'
  return error
}

function defaultSleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(abortError())
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    function onAbort() {
      clearTimeout(timer)
      signal.removeEventListener('abort', onAbort)
      reject(abortError())
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

export function createIdempotencyKey(cryptoLike = globalThis.crypto) {
  if (cryptoLike?.randomUUID) return cryptoLike.randomUUID()
  if (!cryptoLike?.getRandomValues) throw new Error('Secure random generator unavailable')
  const bytes = cryptoLike.getRandomValues(new Uint8Array(16))
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
}

export async function executeWithRetry(send, config, options = {}) {
  const policy = resolveRequestPolicy(config)
  const maxAttempts = policy === 'read' || policy === 'idempotent' || policy === 'keyed' ? 3 : 1
  if (config.signal?.aborted) throw abortError()
  if (maxAttempts === 1) return send(config)
  const now = options.now || Date.now
  const sleep = options.sleep || defaultSleep
  const random = options.random || Math.random
  const operationTimeout = Number(config.timeout) > 0 ? Number(config.timeout) : 60_000
  const deadline = now() + operationTimeout
  const headers = { ...(config.headers || {}) }
  if (policy === 'keyed' && !headers['Idempotency-Key']) {
    headers['Idempotency-Key'] = (options.newIdempotencyKey || createIdempotencyKey)()
  }

  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    if (config.signal?.aborted) throw abortError()
    const remaining = deadline - now()
    if (remaining <= 0) throw new Error('Request deadline exceeded')
    const attemptConfig = { ...config, headers, timeout: Math.min(20_000, remaining) }
    let response
    let error
    try {
      response = await send(attemptConfig)
      if (!isTransientStatus(response.status)) return response
    } catch (caught) {
      error = caught
      if (!isTransientError(error) || config.signal?.aborted) throw error
    }
    if (attempt === maxAttempts) {
      if (error) throw error
      return response
    }

    const remainingAfterRequest = deadline - now()
    const retryResponse = response || error?.response
    const retryAfter = retryResponse?.status === 429 ? retryAfterMs(retryResponse, now) : null
    const delay = retryAfter ?? Math.round(500 * 2 ** (attempt - 1) * (0.8 + random() * 0.4))
    if (delay >= remainingAfterRequest) {
      if (error) throw error
      return response
    }
    await sleep(delay, config.signal)
  }
}
