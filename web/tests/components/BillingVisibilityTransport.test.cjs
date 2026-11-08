/* Real billing APIs, request, Axios, retry/auth and diagnostic helpers. Only the no-network adapter and toast/browser sinks are fixtures. */
const { test, beforeEach, afterEach, after } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const axios = require('axios'), { message } = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const { resolveRequestPolicy } = require(path.join(root, 'src/utils/requestRetry.mjs'))
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
const SECRET = 'SYNTHETIC_PRIVATE_BILLING_URL_CONFIG'
const A = { id: 951, role: 'admin', username: 'synthetic-billing-owner', balance: 1.234567 }
const B = { id: 952, role: 'admin', username: 'synthetic-later-owner', balance: 2.345678 }
const query = { keyword: 'synthetic-billing-check', type: 'normal_search', page: 2, pageSize: 10 }
function writeVisibility(show = false) { logicalWrites++; return api.updateBillingStatementVisibility(8091, show) }
const methods = [
  { name: 'GET', method: 'get', url: '/adapter-service/billing-records', invoke: () => api.getBillingRecords({ ...query }) },
  { name: 'PUT', method: 'put', url: '/adapter-service/billing-records/8091/statement-visibility', invoke: () => writeVisibility(false) },
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
  assert.strictEqual(value.data, body, 'transport retains the original body, including all six-decimal money and row references')
  assert.equal(value.message, body.detail || body.message || '')
  assert.deepEqual(Object.keys(value), ['data', 'status', 'message'])
  assert.equal(getTransportRequestId(value), ID, 'the actual helper retains validated response-header diagnostics')
  assert.ok(!JSON.stringify(value).includes(ID), 'diagnostic identity stays outside enumerable payload')
  assert.equal(getTransportRequestId({ ...value }), undefined, 'a clone cannot impersonate the original diagnostic owner')
}
function assertRequest(entry, count = 1, show = false) {
  assert.equal(requests.length, count)
  for (const config of requests) {
    assert.equal(config.url, entry.url)
    assert.equal(config.method, entry.method)
    assert.equal(config.baseURL, '/')
    assert.equal(config.withCredentials, true)
    assert.equal(config.auth, undefined)
    assert.equal(config.headers.get('Authorization'), undefined, 'no real credential fixture')
    assert.equal(config.headers.get('Cookie'), undefined, 'no real cookie fixture')
    assert.equal(config.headers.get('Idempotency-Key'), undefined, 'visibility is not the keyed balance-adjustment POST')
    assert.ok(config.timeout > 0 && config.timeout <= 60_000, 'the original request budget remains bounded')
    if (entry.method === 'get') {
      assert.deepEqual(config.params, query)
      assert.equal(config.data, undefined, 'explicit reconciliation never becomes a mutation')
      assert.equal(resolveRequestPolicy(config), 'read')
      assert.ok(config.timeout <= 20_000, 'read attempts retain their original budget')
    } else {
      assert.deepEqual(JSON.parse(config.data), { showInStatement: show }, 'PUT contains only the original strict bool, not amount, balance, owner or profile data')
      assert.equal(typeof JSON.parse(config.data).showInStatement, 'boolean')
      assert.equal(config.params, undefined)
      assert.equal(resolveRequestPolicy(config), 'single')
    }
  }
}
function witness(label, extra = {}) {
  console.log('BILLING_VISIBILITY_TRANSPORT_OBSERVED ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', label, attempts: requests.length, logicalWrites, toasts: toasts.length, rawToast: toasts.some(value => String(value).includes(SECRET)), ...extra }))
}
beforeEach(() => {
  requests = []; toasts = []; respond = undefined; logicalWrites = 0
  host = newHost(); globalThis.window = host
  message.error = value => { toasts.push(value) }
  user.setStoredCurrentUser(A)
})
afterEach(() => {
  assert.equal(host.listeners.size, 0, 'all request-scoped storage listeners are released')
  user.clearStoredCurrentUser()
  message.error = originalToast
  globalThis.window = originalWindow
})
after(() => { axios.create = originalCreate; message.error = originalToast; globalThis.window = originalWindow })

for (const entry of methods) {
  test('BILLING_VISIBILITY_TRANSPORT ' + entry.name + ' HTTP403 is page-local without raw global toast or losing original response identity', async () => {
    const body = { message: SECRET, detail: SECRET, extension: { retained: true } }
    respond = config => result(config, 403, body)
    const value = await entry.invoke()
    assertRequest(entry)
    assertPayload(value, 403, body)
    assert.deepEqual(user.getStoredCurrentUser(), A)
    assert.equal(host.location.href, '/fixture')
    witness(entry.name + '-403', { payloadIdentity: value.data === body, diagnostic: getTransportRequestId(value) === ID, local: requests[0].errorFeedback === 'local' })
    assert.deepEqual(toasts, [], 'visibility write and its explicit GET reconciliation own feedback; no arbitrary detail/message global toast')
    assert.equal(requests[0].errorFeedback, 'local')
  })
  test('BILLING_VISIBILITY_TRANSPORT ' + entry.name + ' local HTTP401 still clears current login and redirects without raw global toast', async () => {
    const body = { message: SECRET }
    const epoch = user.getCurrentUserLogoutEpoch()
    respond = config => result(config, 401, body)
    const value = await entry.invoke()
    assertRequest(entry)
    assertPayload(value, 401, body)
    assert.equal(user.getStoredCurrentUser(), null, 'local feedback does not bypass actual auth cleanup')
    assert.equal(user.getCurrentUserLogoutEpoch(), epoch + 1)
    assert.equal(host.location.href, '/login')
    witness(entry.name + '-401', { loggedOut: true, redirected: true, local: requests[0].errorFeedback === 'local' })
    assert.deepEqual(toasts, [], 'local feedback suppresses notification only, never logout')
    assert.equal(requests[0].errorFeedback, 'local')
  })
}
for (const show of [false, true]) {
  test('BILLING_VISIBILITY_TRANSPORT PUT HTTP200 keeps strict ' + show + ' bool-only payload and exact raw ack diagnostics', async () => {
    const body = { id: 8091, showInStatement: show, extension: { amount: -0.000062, balanceBefore: 1.234567, balanceAfter: 1.234567 } }
    respond = config => result(config, 200, body)
    const value = await writeVisibility(show)
    assertRequest(methods[1], 1, show)
    assertPayload(value, 200, body)
    assert.equal(logicalWrites, 1)
    assert.equal(value.data.extension.amount, -0.000062)
    assert.deepEqual(user.getStoredCurrentUser(), A, 'visibility transport never fabricates a wallet adjustment')
    assert.deepEqual(toasts, [])
    witness('PUT-200-' + show, { payloadIdentity: value.data === body, diagnostic: getTransportRequestId(value) === ID, strictBool: true, moneyUnchanged: true })
  })
}
test('BILLING_VISIBILITY_TRANSPORT PUT unknown outcome makes one unkeyed write, retains exact original cause and never adopts balance keyed retries', async () => {
  let original
  respond = config => { original = new axios.AxiosError(SECRET, 'ERR_NETWORK', config); throw original }
  let caught
  await assert.rejects(writeVisibility(false), error => { caught = error; return true })
  assertRequest(methods[1])
  assert.equal(logicalWrites, 1)
  assert.equal(caught.code, 'UNKNOWN_OUTCOME')
  assert.strictEqual(caught.cause, original)
  assert.equal(buildRequestFailureView(caught).kind, 'unknown-outcome')
  assert.equal(buildRequestFailureView(caught).allowRetry, false)
  assert.equal(getTransportRequestId(caught), undefined, 'network failure cannot fabricate an HTTP diagnostic')
  assert.deepEqual(toasts, [])
  witness('PUT-UNKNOWN', { originalCause: caught.cause === original, unknown: caught.code, autoReplay: false, keyed: false })
})
test('BILLING_VISIBILITY_TRANSPORT PUT HTTP503 stays single and unkeyed while preserving exact raw response and diagnostic', async () => {
  const body = { message: SECRET, detail: SECRET, amount: 0.123456 }
  respond = config => result(config, 503, body)
  const value = await writeVisibility(false)
  assertRequest(methods[1])
  assertPayload(value, 503, body)
  assert.equal(logicalWrites, 1)
  assert.equal(buildRequestFailureView(value).kind, 'upstream')
  assert.deepEqual(toasts, [])
  witness('PUT-503', { payloadIdentity: value.data === body, diagnostic: getTransportRequestId(value) === ID, autoReplay: false, keyed: false })
})
test('BILLING_VISIBILITY_TRANSPORT explicit GET reconciliation is read-only and retains exact query, row references and six-decimal money', async () => {
  const rows = [{ id: 8091, amount: 0.123456, balanceBefore: 1.234567, balanceAfter: 1.111111, showInStatement: false }]
  const body = { data: rows, total: 1, page: 2, pageSize: 10, summary: { totalAmount: 0.123456 } }
  respond = config => result(config, 200, body)
  const value = await methods[0].invoke()
  assertRequest(methods[0])
  assertPayload(value, 200, body)
  assert.strictEqual(value.data.data, rows)
  assert.strictEqual(value.data.data[0], rows[0])
  assert.equal(value.data.data[0].amount, 0.123456)
  assert.equal(logicalWrites, 0)
  assert.deepEqual(toasts, [])
  witness('GET-reconcile', { mutation: false, payloadIdentity: value.data === body, rowIdentity: value.data.data === rows })
})
for (const entry of methods) {
  test('BILLING_VISIBILITY_TRANSPORT late old-owner ' + entry.name + ' HTTP401 cannot clear or redirect newer login', async () => {
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
  test('BILLING_VISIBILITY_TRANSPORT rejected ' + entry.name + ' HTTP403 retains exact original Axios Error and diagnostic', async () => {
    let original
    respond = config => { original = new axios.AxiosError(SECRET, 'ERR_BAD_REQUEST', config, undefined, result(config, 403, { message: SECRET })); throw original }
    let caught
    await assert.rejects(entry.invoke(), error => { caught = error; return true })
    assertRequest(entry)
    assert.strictEqual(caught, original)
    assert.equal(getTransportRequestId(caught), ID)
    assert.equal(buildRequestFailureView(caught).kind, 'forbidden')
    assert.deepEqual(user.getStoredCurrentUser(), A)
    assert.deepEqual(toasts, [])
  })
}
test('BILLING_VISIBILITY_TRANSPORT separate balance POST retains its original three keyed attempts rather than redefining PUT policy', async () => {
  const payload = { id: 71, mode: 'override', amount: -0.000062, showInStatement: false }
  const bodies = []
  respond = config => { const body = { message: SECRET, attempt: bodies.length + 1 }; bodies.push(body); return result(config, 503, body) }
  const value = await api.adjustUserBalance(payload)
  assert.equal(requests.length, 3)
  const keys = requests.map(config => config.headers.get('Idempotency-Key'))
  assert.equal(new Set(keys).size, 1)
  assert.equal(typeof keys[0], 'string')
  assert.ok(keys[0].length > 0)
  for (const config of requests) {
    assert.equal(config.method, 'post')
    assert.equal(config.url, '/adapter-service/user/71/balance')
    assert.equal(resolveRequestPolicy(config), 'keyed')
    assert.deepEqual(JSON.parse(config.data), { mode: 'override', amount: -0.000062, showInStatement: false })
  }
  assertPayload(value, 503, bodies[2])
  assert.deepEqual(toasts, [])
  witness('separate-balance-POST-503', { keyed: true, originalThreeAttempts: true, sameKey: true })
})
for (const status of [401, 403]) {
  test('BILLING_VISIBILITY_TRANSPORT unrelated metrics HTTP' + status + ' keeps original default auth feedback and raw payload', async () => {
    const body = { message: 'SYNTHETIC_DEFAULT_PERMISSION', extension: { retained: true } }
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
