import test, { afterEach } from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import Module, { createRequire } from 'node:module'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Compile the real application modules only; Axios, Vue and all lease logic stay real.
// The only replaced effects are Antd's toast sink and the browser fixture's storage/location.
const web = process.env.TIKU_WEB_SOURCE || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const utils = process.env.FRONTEND_UTILS_ROOT || path.join(web, 'src/utils')
const require = createRequire(path.join(web, 'package.json'))
const { transformSync } = require('@babel/core')
const modulesPlugin = require.resolve('@babel/plugin-transform-modules-commonjs', { paths: [require.resolve('@vue/babel-preset-app')] })
const originalJS = Module._extensions['.js']
Module._extensions['.js'] = (module, filename) => {
  if (!filename.startsWith(utils + path.sep)) return originalJS(module, filename)
  module.paths = require.resolve.paths('vue')
  module._compile(transformSync(readFileSync(filename, 'utf8'), {
    filename, babelrc: false, configFile: false, plugins: [modulesPlugin],
  }).code, filename)
}
let user, request, helpers
try {
  user = require(path.join(utils, 'currentUser.js'))
  request = require(path.join(utils, 'request.js')).default
  helpers = require(path.join(utils, 'requestHelpers.mjs'))
} finally { Module._extensions['.js'] = originalJS }
const { effectScope } = require('vue')
const { message } = require('ant-design-vue')
const originalToast = message.error
const originalWindow = globalThis.window
const key = 'adapter_current_user'
const serverId = '0123456789abcdef0123456789abcdef'
const A = { id: 101, role: 'user', balance: 1 }
const B = { id: 202, role: 'user', balance: 2 }
let toasts = []
const hosts = []
class FixtureStorage {
  values = new Map()
  blockedRead = false
  blockedRemove = false
  getItem(name) { if (this.blockedRead) throw new Error('fixture read blocked'); return this.values.get(name) ?? null }
  setItem(name, value) { this.values.set(name, String(value)) }
  removeItem(name) { if (this.blockedRemove) throw new Error('fixture remove blocked'); this.values.delete(name) }
  clear() { this.values.clear() }
}
function newHost(storage = new FixtureStorage()) {
  const listeners = new Set()
  const host = {
    localStorage: storage, location: { href: '/fixture' }, listeners,
    addEventListener(type, fn) { if (type === 'storage') listeners.add(fn) },
    removeEventListener(type, fn) { if (type === 'storage') listeners.delete(fn) },
    storageEvent(eventKey = key, storageArea = storage) {
      for (const listener of [...listeners]) listener({ key: eventKey, storageArea })
    },
  }
  hosts.push(host)
  globalThis.window = host
  toasts = []
  message.error = value => { toasts.push(value) }
  return host
}
async function pending(config = {}) {
  let settle, actual
  const promise = request({ url: '/adapter-service/user/profile', method: 'get', retryPolicy: 'single', ...config,
    adapter(adapterConfig) {
      actual = adapterConfig
      return new Promise((resolve, reject) => { settle = { resolve, reject } })
    },
  })
  for (let i = 0; i < 10 && !settle; i++) await Promise.resolve()
  assert.ok(settle, 'the real Axios instance reached the provided no-network adapter')
  return {
    promise, config: actual,
    respond(status, data = { message: 'fixture auth rejection' }, headers = { 'x-request-id': serverId }) {
      settle.resolve({ status, data, headers, config: actual, statusText: 'fixture', request: {} })
    },
    reject(error) { settle.reject(error) },
  }
}
function assertPayload(value, status, data) {
  assert.equal(value.status, status)
  assert.equal(value.data, data)
  assert.deepEqual(Object.keys(value), ['data', 'status', 'message'])
  assert.equal(helpers.getTransportRequestId(value), serverId)
  assert.equal(JSON.stringify(value).includes(serverId), false)
}
function observed(host) {
  const raw = host.localStorage.values.get(key)
  return { id: raw ? JSON.parse(raw).id : null, toasts: toasts.length, href: host.location.href, listeners: host.listeners.size }
}
function assertRetired(host, expected, label) {
  const actual = observed(host)
  console.log('AUTH_LEASE_OBSERVED ' + label + ' ' + JSON.stringify(actual))
  assert.deepEqual(actual, { id: expected.id, toasts: 0, href: '/fixture', listeners: 0 })
  assert.deepEqual(JSON.parse(host.localStorage.values.get(key)), expected)
}
afterEach(() => {
  for (const host of hosts.splice(0)) assert.equal(host.listeners.size, 0, 'all request-scoped storage listeners released')
  message.error = originalToast
  globalThis.window = originalWindow
})

test('real request late A401 cannot clear or notify newly logged-in B without any composable', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const operation = await pending()
  user.setStoredCurrentUser(B)
  const data = { message: 'expired A' }; operation.respond(401, data)
  const value = await operation.promise
  assertPayload(value, 401, data)
  assertRetired(host, B, 'A-pending-B-login-A401')
})
test('real request retires A to B to A even when final JSON matches its original snapshot', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const operation = await pending()
  user.setStoredCurrentUser(B); user.setStoredCurrentUser(A)
  operation.respond(401); await operation.promise
  assertRetired(host, A, 'A-B-A')
})
test('real request retires logout then same-ID same-profile login', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const operation = await pending()
  user.clearStoredCurrentUser(); user.setStoredCurrentUser(A)
  operation.respond(401); await operation.promise
  assertRetired(host, A, 'logout-same-id-login')
})
test('real request conservatively retires same-ID profile updates and identical helper writes', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  for (const next of [{ ...A, balance: 3 }, { ...A, balance: 3 }]) {
    const operation = await pending(); user.setStoredCurrentUser(next)
    operation.respond(401); await operation.promise
    assertRetired(host, next, 'same-id-profile-write')
  }
})
test('real request suppresses old 403 toast but returns its original forbidden payload', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const operation = await pending(); user.setStoredCurrentUser(B)
  const data = { detail: 'fixture old A forbidden' }; operation.respond(403, data)
  assertPayload(await operation.promise, 403, data)
  assertRetired(host, B, 'A-pending-B-login-A403')
})
test('real current 401 still clears login, toasts once and redirects; current 403 never logs out', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  let operation = await pending(); const expired = { message: 'JWT expired' }
  operation.respond(401, expired); assertPayload(await operation.promise, 401, expired)
  assert.deepEqual(observed(host), { id: null, toasts: 1, href: '/login', listeners: 0 })
  host.location.href = '/fixture'; toasts = []; user.setStoredCurrentUser(A)
  operation = await pending(); const denied = { detail: 'fixture current forbidden' }
  operation.respond(403, denied); assertPayload(await operation.promise, 403, denied)
  assert.deepEqual(observed(host), { id: A.id, toasts: 1, href: '/fixture', listeners: 0 })
  assert.equal(toasts[0], denied.detail)
  console.log('AUTH_LEASE_OBSERVED current401=clear+toast+login current403=retain+toast')
})
test('real public WeChat-register 401 and current quota/balance/captcha exceptions remain unchanged', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const publicRequest = await pending({ url: '/adapter-service/public/wechat-register/session' })
  const publicData = { message: 'registration fixture' }; publicRequest.respond(401, publicData)
  assertPayload(await publicRequest.promise, 401, publicData)
  assertRetired(host, A, 'public-wechat-register-401')
  for (const data of [{ code: 'ACCESS_TOKEN_QUOTA_EXCEEDED' }, { code: 'INSUFFICIENT_BALANCE' }, { detail: '请先完成人机验证' }]) {
    toasts = []; const operation = await pending(); operation.respond(401, data)
    assertPayload(await operation.promise, 401, data)
    assert.deepEqual(observed(host), { id: A.id, toasts: 1, href: '/fixture', listeners: 0 })
  }
})
test('real request rejects changed global window and changed localStorage object, even with identical raw JSON', async () => {
  const first = newHost(); user.setStoredCurrentUser(A)
  let operation = await pending(); const second = newHost(); user.setStoredCurrentUser(B)
  operation.respond(401); await operation.promise
  assertRetired(second, B, 'replaced-window')
  assert.equal(first.listeners.size, 0)
  operation = await pending(); const replacement = new FixtureStorage()
  replacement.setItem(key, JSON.stringify(B)); second.localStorage = replacement
  operation.respond(401); await operation.promise
  assertRetired(second, B, 'replaced-storage-same-json')
})
test('real no-consumer request observes cross-tab storage ABA and clear events without permanent listeners', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const operation = await pending()
  host.localStorage.setItem(key, JSON.stringify(B)); host.storageEvent()
  host.localStorage.setItem(key, JSON.stringify(A)); host.storageEvent()
  operation.respond(401); await operation.promise
  assertRetired(host, A, 'external-storage-ABA')
  const cleared = await pending(); host.localStorage.clear(); host.storageEvent(null)
  host.localStorage.setItem(key, JSON.stringify(A)); host.storageEvent()
  cleared.respond(401); await cleared.promise
  assertRetired(host, A, 'external-clear-same-id-login')
})
test('real request detects direct changed storage without events and ignores unrelated storage events', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  let operation = await pending(); host.localStorage.setItem(key, JSON.stringify(B))
  operation.respond(401); await operation.promise
  assertRetired(host, B, 'direct-storage-change')
  operation = await pending(); host.storageEvent('unrelated-key'); host.storageEvent(key, new FixtureStorage())
  operation.respond(403); await operation.promise
  assert.deepEqual(observed(host), { id: B.id, toasts: 1, href: '/fixture', listeners: 0 })
})
test('malformed or blocked storage cannot block real requests or authorize effects against a later valid session', async () => {
  const host = newHost()
  for (const broken of ['malformed', 'blocked-read', 'blocked-property']) {
    const storage = new FixtureStorage(); storage.setItem(key, broken === 'malformed' ? '{fixture-malformed' : JSON.stringify(A))
    host.localStorage = storage
    if (broken === 'blocked-read') storage.blockedRead = true
    if (broken === 'blocked-property') Object.defineProperty(host, 'localStorage', { configurable: true, get() { throw Error('fixture storage property blocked') } })
    const operation = await pending()
    Object.defineProperty(host, 'localStorage', { configurable: true, writable: true, value: storage })
    storage.blockedRead = false; user.setStoredCurrentUser(B)
    operation.respond(401); await operation.promise
    assertRetired(host, B, broken + '-then-B')
  }
})
test('malformed or blocked reads arriving at response time retire effects, and failed cleanup does not reject payload', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  let operation = await pending(); host.localStorage.setItem(key, '{fixture-malformed')
  operation.respond(401); assert.equal((await operation.promise).status, 401)
  assert.equal(host.localStorage.values.get(key), '{fixture-malformed'); assert.equal(toasts.length, 0)
  user.setStoredCurrentUser(B); operation = await pending(); host.localStorage.blockedRead = true
  operation.respond(401); assert.equal((await operation.promise).status, 401)
  host.localStorage.blockedRead = false; assertRetired(host, B, 'blocked-read-at-response')
  operation = await pending(); host.localStorage.blockedRemove = true
  operation.respond(401); assert.equal((await operation.promise).status, 401)
  assert.equal(host.listeners.size, 0)
  assert.equal(user.getStoredCurrentUser().id, B.id, 'failed storage removal cannot be falsely reported as persisted logout')
})
test('request and composable share one bounded listener and release independently; failures preserve error identity and diagnostic ID', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const scope = effectScope(); scope.run(() => user.useCurrentUserDisplay())
  const first = await pending(); const second = await pending()
  try { assert.equal(host.listeners.size, 1) } finally { scope.stop() }
  first.respond(200, { ok: true }); await first.promise
  assert.equal(host.listeners.size, 1, 'the remaining pending request still tracks cross-tab mutations')
  const error = new Error('fixture adapter failure'); error.response = { status: 500, headers: { 'x-request-id': serverId }, data: {} }
  second.reject(error)
  await assert.rejects(second.promise, actual => { assert.equal(actual, error); assert.equal(helpers.getTransportRequestId(actual), serverId); return true })
  assert.equal(host.listeners.size, 0)
})
test('lease leaves actual Axios headers/cookies defaults, status handling and consuming request count unchanged', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const operation = await pending({ url: '/adapter-service/uapi/search', method: 'post', headers: { 'X-Fixture': 'lease-contract' } })
  assert.equal(operation.config.withCredentials, true)
  assert.equal(operation.config.timeout, 60000)
  assert.equal(operation.config.headers.get('X-Fixture'), 'lease-contract')
  assert.equal(operation.config.validateStatus(401), true)
  assert.equal(operation.config.validateStatus(599), true)
  assert.equal(operation.config.validateStatus(600), false)
  const data = { code: 'INSUFFICIENT_BALANCE', requestId: 'business-fixture' }; operation.respond(401, data)
  const value = await operation.promise; assertPayload(value, 401, data)
  assert.equal(value.data.requestId, 'business-fixture')
  assert.equal(host.listeners.size, 0)
})
for (const [method, url] of [['get', '/adapter-service/user/profile'], ['post', '/adapter-service/recharge-orders']]) {
  test('real ' + method + ' backoff never redispatches retired A intent with the later B cookie session', async () => {
    const host = newHost(); user.setStoredCurrentUser(A)
    let attempts = 0, changed, mutationTimer
    const promise = request({ method, url, adapter(config) {
      attempts += 1
      if (attempts === 1) mutationTimer = setTimeout(() => { user.setStoredCurrentUser(B); changed = true }, 5)
      return Promise.resolve({ status: attempts === 1 ? 429 : 200, data: { fixture: 'retry' },
        headers: { 'retry-after': '0.03', 'x-request-id': serverId }, config, request: {}, statusText: 'fixture' })
    } })
    const result = await promise.then(value => ({ value }), error => ({ error }))
    clearTimeout(mutationTimer)
    console.log('AUTH_RETRY_OBSERVED ' + method + ' ' + JSON.stringify({ attempts, changed: Boolean(changed), outcome: result.error?.code || 'fulfilled' }))
    assert.equal(changed, true, 'B login happened during the real retry backoff')
    assert.equal(attempts, 1, 'the next adapter call must never dispatch under B')
    assert.equal(result.error?.code, 'ERR_CANCELED')
    assert.equal(result.error?.name, 'AbortError')
    assertRetired(host, B, method + '-backoff-retired')
  })
}
test('unknown unreadable ownership never blocks first dispatch or otherwise-valid retry', async () => {
  const host = newHost(); user.setStoredCurrentUser(A); host.localStorage.blockedRead = true
  let attempts = 0
  const value = await request({ method: 'get', url: '/adapter-service/user/profile', adapter(config) {
    attempts += 1
    return Promise.resolve({ status: attempts === 1 ? 429 : 200, data: { fixture: 'unknown-storage' },
      headers: { 'retry-after': '0.001', 'x-request-id': serverId }, config, request: {}, statusText: 'fixture' })
  } })
  host.localStorage.blockedRead = false
  assert.equal(value.status, 200); assert.equal(attempts, 2)
  assertRetired(host, A, 'unknown-storage-legitimate-retry')
})
test('same-owner real keyed retry retains one Idempotency-Key and the original operation budget', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  const attempts = []
  const value = await request({ method: 'post', url: '/adapter-service/recharge-orders', timeout: 10000, adapter(config) {
    attempts.push(config)
    return Promise.resolve({ status: attempts.length === 1 ? 429 : 200, data: { fixture: 'same-owner-keyed' },
      headers: { 'retry-after': '0.001', 'x-request-id': serverId }, config, request: {}, statusText: 'fixture' })
  } })
  assert.equal(value.status, 200); assert.equal(attempts.length, 2)
  const firstKey = attempts[0].headers.get('Idempotency-Key')
  assert.equal(typeof firstKey, 'string'); assert.ok(firstKey.length > 0)
  assert.equal(attempts[1].headers.get('Idempotency-Key'), firstKey)
  assert.ok(attempts[1].timeout <= attempts[0].timeout && attempts[0].timeout <= 10000)
  assertRetired(host, A, 'same-owner-keyed-retry')
})

test('initial unreadable ownership never authorizes a 401 logout even after reads recover without helper writes', async () => {
  const host = newHost(); user.setStoredCurrentUser(A)
  host.localStorage.blockedRead = true
  const operation = await pending()
  host.localStorage.blockedRead = false
  const data = { message: 'fixture unknown session' }; operation.respond(401, data)
  assertPayload(await operation.promise, 401, data)
  assertRetired(host, A, 'initial-unknown-recovered-401')
})
test('malformed cleanup and blocked getter remain safe without a composable and never create persistent lease fields', () => {
  const host = newHost(); host.localStorage.setItem(key, '{fixture-malformed')
  assert.equal(user.getStoredCurrentUser(), null)
  assert.equal(host.localStorage.values.has(key), false)
  host.localStorage.setItem(key, '{fixture-malformed'); host.localStorage.blockedRemove = true
  assert.equal(user.getStoredCurrentUser(), null)
  host.localStorage.blockedRead = true
  assert.equal(user.getStoredCurrentUser(), null)
  host.localStorage.blockedRead = false; host.localStorage.blockedRemove = false
  user.setStoredCurrentUser(A)
  assert.deepEqual([...host.localStorage.values.keys()], [key])
  assert.equal(host.localStorage.values.get(key), JSON.stringify(A))
  assert.equal(host.listeners.size, 0)
})
