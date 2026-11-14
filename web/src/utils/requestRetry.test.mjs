import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { createIdempotencyKey, executeWithRetry, resolveRequestPolicy, toUserRequestError } from './requestRetry.mjs'

test('request policy excludes billed reads and one-time flows', () => {
  assert.equal(resolveRequestPolicy({ method: 'get', url: '/api/courses' }), 'read')
  assert.equal(resolveRequestPolicy({ method: 'get', url: '/api/uapi/phoneinfo' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'get', url: '/api/uapi/status/usage' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'get', url: '/api/ai-search-config/models' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'get', url: '/api/public/wechat-login/status' }), 'poll')
  assert.equal(resolveRequestPolicy({ method: 'get', url: '/api/user/wechat/scan-id' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'post', url: '/api/recharge-orders' }), 'keyed')
  assert.equal(resolveRequestPolicy({ method: 'post', url: '/api/user/7/balance' }), 'keyed')
  assert.equal(resolveRequestPolicy({ method: 'post', url: '/api/questions' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'post', url: '/api/questions/search' }), 'read')
  assert.equal(resolveRequestPolicy({ method: 'put', url: '/api/system-settings' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'put', url: '/api/questions/7' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'put', url: '/api/user/7/reset-password' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'put', url: '/api/user/7' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'put', url: '/api/user/phone' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'delete', url: '/api/questions/7' }), 'single')
  assert.equal(resolveRequestPolicy({ method: 'post', url: '/sqp/api/parse' }), 'single')
})

test('retry occurs before exposing a transient HTTP response', async () => {
  const statuses = [503, 429, 200]
  const seen = []
  const delays = []
  const response = await executeWithRetry(async (config) => {
    seen.push(config)
    return { status: statuses.shift(), headers: {} }
  }, { method: 'get', url: '/api/courses' }, {
    now: () => 0,
    sleep: async (ms) => { delays.push(ms) },
    random: () => 0.5,
  })

  assert.equal(response.status, 200)
  assert.equal(seen.length, 3)
  assert.deepEqual(delays, [500, 1000])
})

test('Retry-After is honored within the operation deadline', async () => {
  let clock = 0
  const delays = []
  let calls = 0
  const response = await executeWithRetry(async () => {
    calls++
    return calls === 1
      ? { status: 429, headers: { 'retry-after': '2' } }
      : { status: 200, headers: {} }
  }, { method: 'get', url: '/api/courses', timeout: 3000 }, {
    now: () => clock,
    sleep: async (ms) => { delays.push(ms); clock += ms },
  })

  assert.equal(response.status, 200)
  assert.deepEqual(delays, [2000])
  assert.equal(calls, 2)
})

test('Retry-After is also honored when Axios rejects a 429 response', async () => {
  let calls = 0
  const delays = []
  await executeWithRetry(async () => {
    calls++
    if (calls === 1) {
      const error = new Error('Too Many Requests')
      error.response = { status: 429, headers: { 'retry-after': '3' } }
      throw error
    }
    return { status: 200 }
  }, { method: 'get', url: '/api/courses' }, {
    now: () => 0,
    sleep: async (ms) => { delays.push(ms) },
  })
  assert.deepEqual(delays, [3000])
})

test('programming TypeErrors are not mistaken for connection failures', async () => {
  let calls = 0
  await assert.rejects(() => executeWithRetry(async () => {
    calls++
    throw new TypeError('Cannot read properties of undefined')
  }, { method: 'get', url: '/api/courses' }))
  assert.equal(calls, 1)
})

test('nontransient responses and deliberate cancellation are never replayed', async () => {
  let calls = 0
  const response = await executeWithRetry(async () => {
    calls++
    return { status: 403, headers: {} }
  }, { method: 'get', url: '/api/courses' })
  assert.equal(response.status, 403)
  assert.equal(calls, 1)

  const controller = new AbortController()
  controller.abort()
  await assert.rejects(() => executeWithRetry(async () => {
    calls++
    throw new Error('should not run')
  }, { method: 'get', url: '/api/courses', signal: controller.signal }), { name: 'AbortError' })
  assert.equal(calls, 1)
})

test('keyed mutation keeps the same idempotency key across response loss', async () => {
  const keys = []
  let calls = 0
  const response = await executeWithRetry(async (config) => {
    keys.push(config.headers['Idempotency-Key'])
    calls++
    if (calls === 1) throw new TypeError('Network Error')
    return { status: 200, headers: {} }
  }, { method: 'post', url: '/api/recharge-orders', data: { amount: 10 } }, {
    now: () => 0,
    sleep: async () => {},
    random: () => 0.5,
    newIdempotencyKey: () => 'stable-key',
  })

  assert.equal(response.status, 200)
  assert.deepEqual(keys, ['stable-key', 'stable-key'])
})

test('idempotency keys work when randomUUID is unavailable', () => {
  const fakeCrypto = { getRandomValues(bytes) { bytes.fill(10); return bytes } }
  assert.match(createIdempotencyKey(fakeCrypto), /^[0-9a-f]{32}$/)
})

test('an unprotected mutation is sent once even on a network error', async () => {
  let calls = 0
  await assert.rejects(() => executeWithRetry(async () => {
    calls++
    throw new TypeError('Network Error')
  }, { method: 'post', url: '/api/questions' }))
  assert.equal(calls, 1)
})

test('single-attempt costed reads keep their existing unlimited timeout', async () => {
  let sentConfig
  await executeWithRetry(async (config) => {
    sentConfig = config
    return { status: 200 }
  }, { method: 'get', url: '/api/uapi/phoneinfo', timeout: 0 })
  assert.equal(sentConfig.timeout, 0)
})

test('unknown mutation outcomes get a safe final error without hiding read failures', () => {
  const networkError = new Error('Network Error')
  const mutation = toUserRequestError({ method: 'post', url: '/api/questions' }, networkError)
  assert.equal(mutation.code, 'UNKNOWN_OUTCOME')
  assert.match(mutation.message, /结果未确认/)
  assert.equal(mutation.cause, networkError)
  assert.equal(toUserRequestError({ method: 'get', url: '/api/courses' }, networkError), networkError)
  const unauthorized = Object.assign(new Error('Unauthorized'), { response: { status: 401 } })
  assert.equal(toUserRequestError({ method: 'post', url: '/api/questions' }, unauthorized), unauthorized)
})

test('both Axios entry points retry before displaying final responses', () => {
  const main = readFileSync(new URL('./request.js', import.meta.url), 'utf8')
  const parser = readFileSync(new URL('../api/api.js', import.meta.url), 'utf8')
  assert.match(main, /await executeWithRetry\(/)
  assert.match(parser, /await executeWithRetry\(/)
  assert.match(parser, /retryPolicy: 'single'/)
})
