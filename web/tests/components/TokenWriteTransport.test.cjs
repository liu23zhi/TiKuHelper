/* Real token APIs, request, Axios, retry/auth helpers; only the no-network Axios adapter and toast/browser sinks are fixtures. */
const { test, beforeEach, afterEach, after } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const axios = require('axios'), { message } = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const { getTransportRequestId } = require(path.join(root, 'src/utils/requestHelpers.mjs'))
const { buildRequestFailureView } = require(path.join(root, 'src/utils/requestFailureView.mjs'))
const originalCreate = axios.create, originalToast = message.error, originalWindow = globalThis.window
let requests = [], respond, toasts = [], host
axios.create = config => originalCreate({ ...config, adapter: async config => {
  requests.push(config)
  assert.equal(typeof respond, 'function', 'the synthetic adapter has no network fallback')
  return respond(config)
} })
let api
try { api = require(path.join(root, 'src/api/TikuComponentApi.js')) } finally { axios.create = originalCreate }
const ID = '0123456789abcdef0123456789abcdef'
const SECRET = 'SYNTHETIC_PRIVATE_TOKEN_URL_CONFIG'
const A = { id: 751, role: 'admin', username: 'synthetic-token-owner', balance: 1.234567 }
const B = { id: 752, role: 'admin', username: 'synthetic-later-owner', balance: 2.345678 }
const fixturePayload = { name: 'transport_token', totalQuotaLimit: 0.000062, remark: '' }
const methods = [
  { name: 'GET', method: 'get', url: '/adapter-service/access-tokens', invoke: () => api.getAccessTokens({}) },
  { name: 'POST', method: 'post', url: '/adapter-service/access-tokens', invoke: () => api.createAccessToken({ ...fixturePayload }) },
  { name: 'PUT', method: 'put', url: '/adapter-service/access-tokens/17', invoke: () => api.updateAccessToken({ id: 17, ...fixturePayload, status: 'enabled' }) },
  { name: 'DELETE', method: 'delete', url: '/adapter-service/access-tokens/17', invoke: () => api.deleteAccessToken({ id: 17 }) },
]
class FixtureStorage {
  values = new Map()
  getItem(key) { return this.values.get(key) ?? null }
  setItem(key, value) { this.values.set(key, String(value)) }
  removeItem(key) { this.values.delete(key) }
}
function newHost() {
  const listeners = new Set()
  return {
    localStorage: new FixtureStorage(), location: { href: '/fixture' }, listeners,
    addEventListener(type, fn) { if (type === 'storage') listeners.add(fn) },
    removeEventListener(type, fn) { if (type === 'storage') listeners.delete(fn) },
  }
}
const result = (config, status, data) => ({ config, status, statusText: String(status), headers: { 'X-Request-ID': ID }, data })
function assertPayload(value, status, body) {
  assert.equal(value.status, status)
  assert.strictEqual(value.data, body, 'local feedback never projects or clones the raw response body')
  assert.equal(value.message, body.detail || body.message || '')
  assert.deepEqual(Object.keys(value), ['data', 'status', 'message'])
  assert.equal(getTransportRequestId(value), ID, 'actual helper retains validated response-header diagnostics')
  assert.ok(!JSON.stringify(value).includes(ID), 'diagnostic identity remains outside enumerable payload')
}
function assertRequest(entry) {
  assert.equal(requests.length, 1, 'one invocation makes exactly one adapter call')
  const config = requests[0]
  assert.equal(config.url, entry.url)
  assert.equal(config.method, entry.method)
  assert.equal(config.baseURL, '/')
  assert.equal(config.withCredentials, true, 'same-origin cookie transport stays unchanged')
  assert.equal(config.auth, undefined)
  assert.equal(config.headers.get('Authorization'), undefined, 'no real credential fixture')
  assert.equal(config.headers.get('Cookie'), undefined, 'no real cookie fixture')
  assert.ok(!config.url.includes(SECRET))
}
beforeEach(() => {
  requests = []; toasts = []; respond = undefined
  host = newHost(); globalThis.window = host
  message.error = value => { toasts.push(value) }
  user.setStoredCurrentUser(A)
})
afterEach(() => {
  assert.equal(host.listeners.size, 0, 'request-scoped storage listeners are released')
  user.clearStoredCurrentUser()
  message.error = originalToast
  globalThis.window = originalWindow
})
after(() => { axios.create = originalCreate; message.error = originalToast; globalThis.window = originalWindow })

for (const entry of methods) {
  test('TOKEN_TRANSPORT ' + entry.name + ' HTTP403 is page-local without raw global toast or losing response identity', async () => {
    const body = { message: SECRET, detail: SECRET, extension: { retained: true } }
    respond = config => result(config, 403, body)
    const value = await entry.invoke()
    assertRequest(entry)
    assertPayload(value, 403, body)
    assert.deepEqual(user.getStoredCurrentUser(), A)
    assert.equal(host.location.href, '/fixture')
    console.log('TOKEN_TRANSPORT_403_OBSERVED ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', method: entry.name, requests: requests.length, toasts: toasts.length, rawToast: toasts.some(value => String(value).includes(SECRET)), payloadIdentity: value.data === body, diagnostic: getTransportRequestId(value) === ID }))
    assert.deepEqual(toasts, [], 'token HTTP403 feedback belongs to the page; transport must not expose arbitrary message/detail')
    assert.equal(requests[0].errorFeedback, 'local')
  })
  test('TOKEN_TRANSPORT ' + entry.name + ' local HTTP401 still clears its current login and navigates without a global toast', async () => {
    const body = { message: SECRET }
    const epoch = user.getCurrentUserLogoutEpoch()
    respond = config => result(config, 401, body)
    const value = await entry.invoke()
    assertRequest(entry)
    assertPayload(value, 401, body)
    assert.equal(user.getStoredCurrentUser(), null, 'local feedback must not bypass real auth cleanup')
    assert.equal(user.getCurrentUserLogoutEpoch(), epoch + 1)
    assert.equal(host.location.href, '/login')
    assert.deepEqual(toasts, [], 'local feedback suppresses notification, not logout handling')
  })
}
for (const entry of methods.filter(entry => entry.method !== 'get')) {
  test('TOKEN_TRANSPORT ' + entry.name + ' network unknown outcome issues one write and preserves the original cause without replay', async () => {
    let original
    respond = config => { original = new axios.AxiosError(SECRET, 'ERR_NETWORK', config); throw original }
    let caught
    await assert.rejects(entry.invoke(), error => { caught = error; return true })
    assertRequest(entry)
    assert.equal(caught.code, 'UNKNOWN_OUTCOME')
    assert.strictEqual(caught.cause, original, 'unknown wrapper must retain original error identity')
    assert.equal(buildRequestFailureView(caught).kind, 'unknown-outcome')
    assert.equal(buildRequestFailureView(caught).allowRetry, false)
    assert.equal(getTransportRequestId(caught), undefined, 'no HTTP response means no fabricated diagnostic')
    assert.deepEqual(toasts, [])
    console.log('TOKEN_TRANSPORT_UNKNOWN_OBSERVED ' + JSON.stringify({ method: entry.name, requests: requests.length, unknown: caught.code, originalCause: caught.cause === original, autoReplay: false }))
  })
}
test('TOKEN_TRANSPORT rejected token HTTP403 retains the exact original Error and its validated diagnostic ID', async () => {
  let original
  respond = config => {
    original = new axios.AxiosError(SECRET, 'ERR_BAD_REQUEST', config, undefined, result(config, 403, { message: SECRET }))
    throw original
  }
  let caught
  await assert.rejects(methods[1].invoke(), error => { caught = error; return true })
  assertRequest(methods[1])
  assert.strictEqual(caught, original)
  assert.equal(getTransportRequestId(caught), ID)
  assert.equal(buildRequestFailureView(caught).kind, 'forbidden')
  assert.deepEqual(user.getStoredCurrentUser(), A)
  assert.deepEqual(toasts, [])
})
test('TOKEN_TRANSPORT late old-owner GET401 cannot clear or redirect the newer login', async () => {
  let finish
  respond = config => new Promise(resolve => { finish = () => resolve(result(config, 401, { message: SECRET })) })
  const promise = methods[0].invoke()
  for (let attempt = 0; attempt < 10 && !finish; attempt++) await Promise.resolve()
  assert.equal(typeof finish, 'function')
  user.setStoredCurrentUser(B)
  finish()
  const value = await promise
  assertRequest(methods[0])
  assert.equal(value.status, 401)
  assert.deepEqual(user.getStoredCurrentUser(), B)
  assert.equal(host.location.href, '/fixture')
  assert.deepEqual(toasts, [])
})
test('TOKEN_TRANSPORT unrelated metrics HTTP403 retains its default auth feedback and unchanged raw payload', async () => {
  const body = { message: 'SYNTHETIC_DEFAULT_PERMISSION', extension: { preserved: true } }
  respond = config => result(config, 403, body)
  const value = await api.getSystemMetrics()
  assertRequest({ method: 'get', url: '/adapter-service/system-metrics' })
  assertPayload(value, 403, body)
  assert.equal(requests[0].errorFeedback, undefined)
  assert.deepEqual(toasts, ['SYNTHETIC_DEFAULT_PERMISSION'], 'other callers keep their existing default behavior')
  assert.deepEqual(user.getStoredCurrentUser(), A)
})
