/* Real balance/list APIs, request, Axios, retry/auth and payload helpers. Only the no-network adapter and toast/browser sinks are fixtures. */
const { test, beforeEach, afterEach, after } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const axios = require('axios'), { message } = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const { buildUserBalanceAdjustmentPayload } = require(path.join(root, 'src/utils/userPortalHelpers.mjs'))
const { getTransportRequestId } = require(path.join(root, 'src/utils/requestHelpers.mjs'))
const { buildRequestFailureView } = require(path.join(root, 'src/utils/requestFailureView.mjs'))
const originalCreate = axios.create, originalToast = message.error, originalWindow = globalThis.window
let requests = [], respond, toasts = [], host, logicalWrites = 0
axios.create = config => originalCreate({ ...config, adapter: async config => {
  requests.push(config)
  assert.equal(typeof respond, 'function', 'the fixture adapter has no network fallback')
  return respond(config)
} })
let api
try { api = require(path.join(root, 'src/api/TikuComponentApi.js')) } finally { axios.create = originalCreate }
const ID = '0123456789abcdef0123456789abcdef'
const SECRET = 'SYNTHETIC_PRIVATE_BALANCE_URL_CONFIG'
const A = { id: 851, role: 'admin', username: 'synthetic-balance-owner', balance: 1.234567 }
const B = { id: 852, role: 'admin', username: 'synthetic-later-owner', balance: 2.345678 }
const form = { id: 71, mode: 'override', amount: '-0.000062', showInStatement: false, password: SECRET, balance: SECRET, balanceMode: SECRET }
const query = { keyword: 'synthetic-check', role: 'user', status: 'enabled', page: 2, pageSize: 10 }
function balancePayload() { return buildUserBalanceAdjustmentPayload(form) }
function writeBalance() { logicalWrites++; return api.adjustUserBalance(balancePayload()) }
const methods = [
  { name: 'GET', method: 'get', url: '/adapter-service/user', invoke: () => api.getUserList({ ...query }) },
  { name: 'POST', method: 'post', url: '/adapter-service/user/71/balance', invoke: writeBalance },
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
  assert.strictEqual(value.data, body, 'transport retains the original body instead of projecting or cloning it')
  assert.equal(value.message, body.detail || body.message || '')
  assert.deepEqual(Object.keys(value), ['data', 'status', 'message'])
  assert.equal(getTransportRequestId(value), ID, 'the real helper keeps the validated response-header diagnostic')
  assert.ok(!JSON.stringify(value).includes(ID), 'diagnostic identity remains outside the enumerable payload')
  assert.equal(getTransportRequestId({ ...value }), undefined, 'a clone must not impersonate the original diagnostic owner')
}
function assertRequest(entry, count = 1) {
  assert.equal(requests.length, count)
  for (const config of requests) {
    assert.equal(config.url, entry.url)
    assert.equal(config.method, entry.method)
    assert.equal(config.baseURL, '/')
    assert.equal(config.withCredentials, true)
    assert.equal(config.auth, undefined)
    assert.equal(config.headers.get('Authorization'), undefined, 'no credential fixture')
    assert.equal(config.headers.get('Cookie'), undefined, 'no cookie fixture')
    assert.ok(config.timeout > 0 && config.timeout <= 20_000, 'real retry attempt budget stays bounded')
    if (entry.method === 'get') {
      assert.deepEqual(config.params, query)
      assert.equal(config.data, undefined, 'reconciliation never becomes a mutation')
      assert.equal(config.headers.get('Idempotency-Key'), undefined)
    } else {
      assert.deepEqual(JSON.parse(config.data), { mode: 'override', amount: -0.000062, showInStatement: false }, 'real helper and API retain the exact six-decimal negative override and hidden bool, without profile fields')
      assert.equal(typeof config.headers.get('Idempotency-Key'), 'string')
      assert.ok(config.headers.get('Idempotency-Key').length > 0)
    }
  }
}
function assertOneKey() {
  assert.equal(logicalWrites, 1, 'transport attempts are not new logical submissions')
  const keys = requests.map(config => config.headers.get('Idempotency-Key'))
  assert.equal(new Set(keys).size, 1, 'all original keyed retries reuse one idempotency key')
  assert.equal(typeof keys[0], 'string')
  assert.ok(keys[0].length > 0)
  return keys[0]
}
function witness(label, extra = {}) {
  console.log('BALANCE_TRANSPORT_OBSERVED ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', label, attempts: requests.length, logicalWrites, toasts: toasts.length, rawToast: toasts.some(value => String(value).includes(SECRET)), ...extra }))
}
beforeEach(() => {
  requests = []; toasts = []; respond = undefined; logicalWrites = 0
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
  test('BALANCE_TRANSPORT ' + entry.name + ' HTTP403 is page-local without raw global toast or losing response identity', async () => {
    const body = { message: SECRET, detail: SECRET, extension: { retained: true } }
    respond = config => result(config, 403, body)
    const value = await entry.invoke()
    assertRequest(entry)
    assertPayload(value, 403, body)
    assert.deepEqual(user.getStoredCurrentUser(), A)
    assert.equal(host.location.href, '/fixture')
    witness(entry.name + '-403', { payloadIdentity: value.data === body, diagnostic: getTransportRequestId(value) === ID, local: requests[0].errorFeedback === 'local' })
    assert.deepEqual(toasts, [], 'balance write and GET reconciliation own feedback; no arbitrary message/detail global toast')
    assert.equal(requests[0].errorFeedback, 'local')
  })
  test('BALANCE_TRANSPORT ' + entry.name + ' local HTTP401 still clears its current login and redirects without global toast', async () => {
    const body = { message: SECRET }
    const epoch = user.getCurrentUserLogoutEpoch()
    respond = config => result(config, 401, body)
    const value = await entry.invoke()
    assertRequest(entry)
    assertPayload(value, 401, body)
    assert.equal(user.getStoredCurrentUser(), null, 'local ownership does not skip actual auth cleanup')
    assert.equal(user.getCurrentUserLogoutEpoch(), epoch + 1)
    assert.equal(host.location.href, '/login')
    witness(entry.name + '-401', { loggedOut: true, redirected: host.location.href === '/login' })
    assert.deepEqual(toasts, [], 'local suppresses notification only, not logout')
    assert.equal(requests[0].errorFeedback, 'local')
  })
}
for (const status of [200, 202]) {
  test('BALANCE_TRANSPORT POST HTTP' + status + ' retains raw body and diagnostic identity with original negative override helper projection', async () => {
    const body = status === 200 ? { id: 71, balance: -0.000062, extension: { retained: true } } : { status: 'pending', message: '请求仍在处理或结果待核对，请勿重复提交', extension: { retained: true } }
    respond = config => result(config, status, body)
    const value = await writeBalance()
    assertRequest(methods[1])
    assertPayload(value, status, body)
    assertOneKey()
    assert.deepEqual(balancePayload(), { id: 71, mode: 'override', amount: -0.000062, showInStatement: false })
    assert.deepEqual(toasts, [])
    assert.deepEqual(user.getStoredCurrentUser(), A, 'transport does not fabricate an account balance update')
    witness('POST-' + status, { payloadIdentity: value.data === body, diagnostic: getTransportRequestId(value) === ID, pending: value.data.status === 'pending' })
  })
}
test('BALANCE_TRANSPORT POST network unknown outcome uses the original three keyed attempts and exact original cause without a new logical submit', async () => {
  const errors = []
  respond = config => { const error = new axios.AxiosError(SECRET, 'ERR_NETWORK', config); errors.push(error); throw error }
  let caught
  await assert.rejects(writeBalance(), error => { caught = error; return true })
  assertRequest(methods[1], 3)
  assertOneKey()
  assert.equal(caught.code, 'UNKNOWN_OUTCOME')
  assert.strictEqual(caught.cause, errors[2], 'unknown-outcome wrapper keeps the last original Axios cause')
  assert.equal(buildRequestFailureView(caught).kind, 'unknown-outcome')
  assert.equal(buildRequestFailureView(caught).allowRetry, false)
  assert.equal(getTransportRequestId(caught), undefined, 'network failure cannot invent HTTP diagnostics')
  assert.deepEqual(toasts, [])
  witness('POST-UNKNOWN', { sameKey: true, originalCause: caught.cause === errors[2], newLogicalSubmit: false })
})
test('BALANCE_TRANSPORT POST HTTP503 retains the original three keyed attempts and last raw response without a new logical submit', async () => {
  const bodies = []
  respond = config => { const body = { message: SECRET, detail: SECRET, attempt: bodies.length + 1 }; bodies.push(body); return result(config, 503, body) }
  const value = await writeBalance()
  assertRequest(methods[1], 3)
  assertOneKey()
  assertPayload(value, 503, bodies[2])
  assert.equal(buildRequestFailureView(value).kind, 'upstream')
  assert.deepEqual(toasts, [])
  witness('POST-503', { sameKey: true, payloadIdentity: value.data === bodies[2], newLogicalSubmit: false })
})
test('BALANCE_TRANSPORT explicit GET reconciliation is a read only, retaining the query, raw rows and six-decimal balance', async () => {
  const body = { data: [{ id: 71, balance: -0.000062 }], total: 1, page: 2, pageSize: 10 }
  respond = config => result(config, 200, body)
  const value = await methods[0].invoke()
  assertRequest(methods[0])
  assertPayload(value, 200, body)
  assert.equal(value.data.data[0].balance, -0.000062)
  assert.equal(logicalWrites, 0)
  assert.deepEqual(toasts, [])
  witness('GET-reconcile', { mutation: false, payloadIdentity: value.data === body })
})
for (const entry of methods) {
  test('BALANCE_TRANSPORT late old-owner ' + entry.name + ' HTTP401 cannot clear or redirect the newer login', async () => {
    let finish
    respond = config => new Promise(resolve => { finish = () => resolve(result(config, 401, { message: SECRET })) })
    const promise = entry.invoke()
    for (let attempt = 0; attempt < 10 && !finish; attempt++) await Promise.resolve()
    assert.equal(typeof finish, 'function')
    user.setStoredCurrentUser(B)
    finish()
    const value = await promise
    assertRequest(entry)
    assert.equal(value.status, 401)
    assert.deepEqual(user.getStoredCurrentUser(), B)
    assert.equal(host.location.href, '/fixture')
    assert.deepEqual(toasts, [])
    witness(entry.name + '-late401', { newerOwnerRetained: true, redirected: false })
  })
}
test('BALANCE_TRANSPORT rejected POST403 retains the exact original Axios error and validated diagnostic ID', async () => {
  let original
  respond = config => { original = new axios.AxiosError(SECRET, 'ERR_BAD_REQUEST', config, undefined, result(config, 403, { message: SECRET })); throw original }
  let caught
  await assert.rejects(writeBalance(), error => { caught = error; return true })
  assertRequest(methods[1])
  assert.strictEqual(caught, original)
  assert.equal(getTransportRequestId(caught), ID)
  assert.equal(buildRequestFailureView(caught).kind, 'forbidden')
  assert.deepEqual(user.getStoredCurrentUser(), A)
  assert.deepEqual(toasts, [])
})
for (const status of [401, 403]) {
  test('BALANCE_TRANSPORT unrelated metrics HTTP' + status + ' retains its original default auth feedback and raw payload', async () => {
    const body = { message: 'SYNTHETIC_DEFAULT_PERMISSION', extension: { preserved: true } }
    respond = config => result(config, status, body)
    const value = await api.getSystemMetrics()
    assert.equal(requests.length, 1)
    assert.equal(requests[0].method, 'get')
    assert.equal(requests[0].url, '/adapter-service/system-metrics')
    assert.equal(requests[0].errorFeedback, undefined)
    assert.equal(logicalWrites, 0)
    assertPayload(value, status, body)
    assert.deepEqual(toasts, [status === 401 ? '当前登陆过期，请重新登陆。' : 'SYNTHETIC_DEFAULT_PERMISSION'])
    assert.deepEqual(user.getStoredCurrentUser(), status === 401 ? null : A)
    assert.equal(host.location.href, status === 401 ? '/login' : '/fixture')
    witness('default-' + status, { logout: user.getStoredCurrentUser() === null, unchangedDefault: true })
  })
}
