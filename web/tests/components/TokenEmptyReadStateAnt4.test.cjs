/* Token empty-read-state qualification: actual Vue/Antd/currentUser, request
 * ownership and page helpers; only GET transport is synthetic and deferred.
 * One unchanged fixture selects the sparse B/M/R target through TIKU_WEB_SOURCE;
 * unchanged dependencies resolve from UI_TEST_WEB_FALLBACK. No source rewriting,
 * VM state assignment, real token, clipboard, CRUD, provider, DB or real network.
 * jsdom DOM visibility is not browser viewport or server-authorization parity.
 */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const fallbackWeb = path.resolve(process.env.UI_TEST_WEB_FALLBACK || path.resolve(__dirname, '../..'))
const targetWeb = path.resolve(process.env.TIKU_WEB_SOURCE || fallbackWeb)
const target = path.join(targetWeb, 'src/pages/AccessTokenManagement/index.vue')
const sha = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
const initialSHA = sha(target), observations = [], calls = [], wrappers = [], runtimeErrors = [], network = [], forbidden = [], notices = []
const sourceEnv = process.env.TIKU_WEB_SOURCE
process.env.NODE_PATH = [path.join(fallbackWeb, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
// Compile the real global console primitives and unchanged helper imports from
// the checkout. The selected target SFC itself is still loaded from the B/M/R copy.
process.env.TIKU_WEB_SOURCE = fallbackWeb
const helper = process.env.UI_TEST_HELPER || path.join(fallbackWeb, 'tests/helpers/vue-dom.cjs')
require(helper)
if (sourceEnv === undefined) delete process.env.TIKU_WEB_SOURCE; else process.env.TIKU_WEB_SOURCE = sourceEnv
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(fallbackWeb, 'src/utils/currentUser.js'))
const { buildResponsePayload, captureTransportRequestError } = require(path.join(fallbackWeb, 'src/utils/requestHelpers.mjs'))
const A = { id: 77971, username: 'SYNTHETIC_EMPTY_READER_A', role: 'user', status: 'enabled', balance: 3.234567 }
const B = { id: 77972, username: 'SYNTHETIC_EMPTY_READER_B', role: 'user', status: 'enabled', balance: 4.345678 }
const ADMIN = { id: 77973, username: 'SYNTHETIC_EMPTY_READER_ADMIN', role: 'admin', status: 'enabled', balance: 5.456789 }
const EMPTY = '当前还没有访问令牌，可点击右上角新增令牌。'
const PENDING = '正在读取访问令牌，请稍候。'
const FAILED = '暂时无法读取访问令牌，请使用上方重试。'
const UNCONFIRMED = '访问令牌列表尚未确认，请登录后重试。'
const PRIVATE = 'SYNTHETIC_PRIVATE_UPSTREAM_URL_CONFIG_TOKEN'
const DIAGNOSTIC = '1234567890abcdef1234567890abcdef'
const SYNTHETIC_TOKEN = 'SYNTHETIC_ONLY_TOKEN_NOT_REAL_123456'
const MASKED_TOKEN = SYNTHETIC_TOKEN.slice(0, 8) + '******' + SYNTHETIC_TOKEN.slice(-6)
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const ok = rows => ({ status: 200, data: rows })
const row = name => ({ id: 371, name, tokenValue: SYNTHETIC_TOKEN, status: 'enabled', totalQuotaLimit: 0.000062, totalAmountUsed: 1.234567, remark: 'SYNTHETIC_EXISTING_REMARK' })
function deny(kind) { forbidden.push(kind); throw new Error('FORBIDDEN_TOKEN_EMPTY_ACTION_' + kind) }
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_REAL_TOKEN_EMPTY_IO_' + kind) }
function getAccessTokens(payload) {
  let yes, no
  const promise = new Promise((resolve, reject) => { yes = resolve; no = reject })
  const call = { name: 'getAccessTokens', payload: clone(payload), actor: user.getStoredCurrentUser()?.id ?? null, synthetic: true, completed: false }
  call.resolve = value => { call.completed = true; call.outcome = value?.status === 200 ? 'HTTP200' : 'HTTP' + String(value?.status); yes(value) }
  call.reject = value => { call.completed = true; call.outcome = 'REJECT'; no(value) }
  calls.push(call)
  return promise
}
const api = new Proxy({ getAccessTokens }, { get(value, name) { if (name === '__esModule' || name === 'then') return undefined; return name in value ? value[name] : () => deny('API_' + String(name)) } })
const originalLoad = Module._load
Module._load = function (request, parent, main) {
  let filename
  try { filename = Module._resolveFilename(request, parent, main) } catch { return originalLoad.call(this, request, parent, main) }
  if (typeof filename === 'string' && (filename.startsWith(path.join(fallbackWeb, 'src/api') + path.sep) || filename.startsWith(path.join(targetWeb, 'src/api') + path.sep))) {
    if (path.basename(filename) === 'TikuComponentApi.js') return api
    return new Proxy({}, { get(_, name) { if (name === '__esModule' || name === 'then') return undefined; return () => deny('OTHER_API_' + String(name)) } })
  }
  return originalLoad.call(this, request, parent, main)
}
for (const kind of ['success', 'warning', 'error']) {
  const original = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return original(...args) }
}
window.XMLHttpRequest.prototype.open = function () { return denyIO('XHR') }
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
Object.defineProperty(window.navigator, 'sendBeacon', { configurable: true, value: () => denyIO('BEACON') })
Object.defineProperty(window.navigator, 'clipboard', { configurable: true, value: { writeText: () => deny('CLIPBOARD') } })
document.execCommand = () => deny('LEGACY_CLIPBOARD')
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
for (const name of ['node:net', 'node:tls']) { const transport = require(name); transport.connect = () => denyIO(name); if ('createConnection' in transport) transport.createConnection = () => denyIO(name) }
window.addEventListener('error', event => runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtimeErrors.push(String(event.reason?.stack || event.reason)))
const Page = require(target)
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
function visible(node) {
  if (!node?.isConnected) return false
  for (let item = node; item && item.nodeType === 1; item = item.parentElement) {
    const style = window.getComputedStyle(item)
    if (item.hidden || item.inert || item.getAttribute('aria-hidden') === 'true' || style.display === 'none' || ['hidden', 'collapse'].includes(style.visibility) || style.opacity === '0') return false
  }
  return true
}
function visibleText(node) {
  if (!visible(node)) return ''
  const walker = document.createTreeWalker(node, window.NodeFilter.SHOW_TEXT), parts = []
  for (let text = walker.nextNode(); text; text = walker.nextNode()) if (visible(text.parentElement)) parts.push(text.nodeValue)
  return compact(parts.join(' '))
}
function nativeButton(w, label) {
  const buttons = [...w.element.querySelectorAll('button')].filter(node => visible(node) && visibleText(node).replace(/\s/g, '') === label.replace(/\s/g, ''))
  assert.equal(buttons.length, 1, 'ONE_VISIBLE_NATIVE_BUTTON_' + label)
  assert.ok(buttons[0].classList.contains('ant-btn'), 'REAL_ANTD_BUTTON_' + label)
  assert.equal(buttons[0].disabled, false, 'ENABLED_NATIVE_BUTTON_' + label)
  return buttons[0]
}
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(actor = A) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  wrappers.push(w); await settle(); return w
}
async function finish(call, response) { assert.ok(call, 'ISSUED_SYNTHETIC_GET'); call.resolve(response); await settle() }
async function clickRead(w, label) {
  const before = calls.length, node = nativeButton(w, label)
  node.click(); await settle()
  assert.equal(calls.length, before + 1, 'ONE_GET_FROM_NATIVE_' + label)
  assert.deepEqual(calls.at(-1).payload, {}, 'ORIGINAL_MINE_GET_PARAMS')
  return calls.at(-1)
}
function http503() { return buildResponsePayload({ status: 503, headers: { 'X-Request-ID': DIAGNOSTIC }, data: { message: PRIVATE, detail: PRIVATE, config: { url: PRIVATE, token: PRIVATE } } }) }
function rejectedNetwork() { return captureTransportRequestError(Object.assign(new Error(PRIVATE), { code: 'ERR_NETWORK', config: { url: PRIVATE, token: PRIVATE } })) }
function snapshot(w, result, label) {
  const table = w.element.querySelector('.ant-table'), empty = [...w.element.querySelectorAll('.ant-table-placeholder .ant-empty')].filter(visible)
  const status = [...w.element.querySelectorAll('.ant-table-placeholder p[role="status"]')].filter(visible)
  const failure = [...w.element.querySelectorAll('.request-failure')].filter(visible)
  const state = {
    label, reads: calls.length - result.readStart, tableLoading: Boolean(w.vm.tableLoading),
    visibleEmptyCount: empty.length, visibleEmptyText: empty.map(visibleText), visibleStatus: status.map(visibleText),
    failureCount: failure.length, failureKind: failure[0]?.getAttribute('data-failure-kind') ?? null, failureText: failure.map(visibleText),
    visibleRows: [...(table?.querySelectorAll('tbody tr.ant-table-row') || [])].filter(visible).map(visibleText),
    implicitCheckboxCount: table?.querySelectorAll('input[type="checkbox"]').length || 0,
    diagnostic: [...w.element.querySelectorAll('[data-request-diagnostic]')].filter(visible).map(visibleText),
    nativeRetryCount: [...w.element.querySelectorAll('button[data-action="retry-request"]')].filter(visible).length,
  }
  result.states.push(state)
  return state
}
function expect(result, condition, label) { result.checks.push({ label, passed: Boolean(condition) }) }
function safeState(result, state, expected, label) {
  expect(result, state.visibleEmptyCount === 0 && !state.visibleEmptyText.some(text => text.includes(EMPTY)), label + '_NO_CONFIRMED_EMPTY')
  expect(result, state.visibleStatus.length === 1 && state.visibleStatus[0] === expected, label + '_EXACT_FIXED_SAFE_CHINESE_STATUS')
}
function confirmedEmpty(result, state, label) {
  expect(result, state.visibleEmptyCount === 1 && state.visibleEmptyText[0] === EMPTY, label + '_ORIGINAL_SUCCESS_EMPTY')
  expect(result, state.visibleStatus.length === 0 && state.failureCount === 0 && state.tableLoading === false, label + '_CONFIRMED_NOT_PENDING_OR_FAILED')
}
function safeFeedback(result) {
  expect(result, !document.body.textContent.includes(PRIVATE) && !JSON.stringify(notices).includes(PRIVATE), 'NO_RAW_UPSTREAM_ERROR_BODY_URL_CONFIG_TOKEN')
}
function scenario(label, run) {
  test('TOKEN_EMPTY_READ_STATE ' + label, async () => {
    const result = { label, readStart: calls.length, states: [], checks: [], executed: false, accepted: false }
    observations.push(result)
    try {
      await run(result); safeFeedback(result); result.executed = true
      const failures = result.checks.filter(check => !check.passed).map(check => check.label)
      result.failures = failures
      console.log('TOKEN_EMPTY_NATIVE ' + JSON.stringify({ label, states: result.states, failures, syntheticGets: calls.length - result.readStart, forbidden: forbidden.length, realIO: network.length }))
      assert.deepEqual(failures, [], 'VISIBLE_READ_STATE_CONTRACT')
      result.accepted = true
    } catch (error) { result.failure = { name: error.name, message: error.message }; throw error }
  })
}
function record() {
  if (!process.env.TOKEN_EMPTY_OBSERVATION_FILE) return
  fs.writeFileSync(process.env.TOKEN_EMPTY_OBSERVATION_FILE, JSON.stringify({
    target, targetSha256Before: initialSHA, targetSha256After: sha(target), fixture: __filename, fixtureSha256: sha(__filename),
    dependencies: { fallbackWeb, helper, currentUser: path.join(fallbackWeb, 'src/utils/currentUser.js'), requestHelpers: path.join(fallbackWeb, 'src/utils/requestHelpers.mjs') },
    observations, calls: calls.map(call => { const copy = { ...call }; delete copy.resolve; delete copy.reject; return copy }), forbidden, network, runtimeErrors, notices,
    qualification: 'Real Vue/Antd/currentUser/helpers in jsdom with ancestor-based visibility and native button clicks; synthetic deferred GET only; no browser viewport, real token, clipboard, CRUD, DB, provider or server-authorization qualification',
  }, null, 2) + '\n')
}
afterEach(async () => {
  for (const w of wrappers.splice(0)) if (w.exists()) w.unmount()
  for (const call of calls) if (!call.completed) call.resolve({ status: 503 })
  await settle(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle()
  user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  record()
  assert.equal(sha(target), initialSHA, 'BOUND_TARGET_BYTES_UNCHANGED')
  assert.deepEqual(forbidden, [], 'ZERO_CREDENTIAL_COPY_CREATE_UPDATE_DELETE_OR_OTHER_API_ACTIONS')
  assert.deepEqual(network, [], 'ZERO_REAL_NETWORK_PROVIDER_OR_DATABASE_IO')
  assert.deepEqual(runtimeErrors, [], 'ZERO_HIDDEN_VUE_ANTD_RUNTIME_EXCEPTION')
})
after(() => { record(); Module._load = originalLoad })

for (const actor of [A, ADMIN]) scenario(actor.role + ' original successful empty remains the actual Antd Empty positive', async result => {
  const w = await open(actor), read = calls.at(-1)
  assert.deepEqual(read.payload, {}); assert.equal(read.actor, actor.id)
  await finish(read, ok([])); confirmedEmpty(result, snapshot(w, result, 'HTTP200 empty'), 'SUCCESS')
  expect(result, calls.length - result.readStart === 1, 'NO_AUTOMATIC_EXTRA_GET')
})
scenario('controlled initial GET pending is not a confirmed absence', async result => {
  const w = await open(), read = calls.at(-1)
  const pending = snapshot(w, result, 'initial GET controlled pending'); safeState(result, pending, PENDING, 'INITIAL_PENDING')
  expect(result, pending.tableLoading === true && pending.failureCount === 0, 'INITIAL_PENDING_NATIVE_BUSY_NO_FAILURE')
  await finish(read, ok([])); confirmedEmpty(result, snapshot(w, result, 'initial GET recovered empty'), 'RECOVER_EMPTY')
})
scenario('successful empty then native refresh HTTP503 shows fixed failure and original durable GET retry', async result => {
  const w = await open(); await finish(calls.at(-1), ok([]))
  confirmedEmpty(result, snapshot(w, result, 'confirmed empty before native refresh'), 'BEFORE_REFRESH')
  const refresh = await clickRead(w, '刷新'), count = calls.length
  nativeButton(w, '刷新').click(); await settle()
  expect(result, calls.length === count, 'ANTD_LOADING_BLOCKS_SECOND_NATIVE_REFRESH')
  safeState(result, snapshot(w, result, 'native refresh controlled pending'), PENDING, 'REFRESH_PENDING')
  const error = http503(); await finish(refresh, error)
  const failed = snapshot(w, result, 'native refresh HTTP503'); safeState(result, failed, FAILED, 'HTTP503')
  expect(result, failed.failureCount === 1 && failed.failureKind === 'upstream' && failed.nativeRetryCount === 1, 'ORIGINAL_VISIBLE_FAILURE_ALERT_AND_NATIVE_GET_RETRY')
  expect(result, failed.diagnostic[0] === DIAGNOSTIC, 'ORIGINAL_VALIDATED_TRANSPORT_DIAGNOSTIC_VISIBLE')
  expect(result, w.findComponent({ name: 'RequestFailureAlert' }).props('failure') === error, 'ORIGINAL_FAILURE_OBJECT_IDENTITY')
  const before = calls.length; await settle(); expect(result, calls.length === before, 'NO_AUTOMATIC_RETRY_AFTER_FAILURE')
  const retry = await clickRead(w, '重试读取'); safeState(result, snapshot(w, result, 'native retry controlled pending'), PENDING, 'RETRY_PENDING')
  await finish(retry, ok([])); confirmedEmpty(result, snapshot(w, result, 'native retry recovered empty'), 'RETRY_EMPTY')
})
scenario('rejected initial GET remains unconfirmed then native retry can recover nonempty rows', async result => {
  const w = await open(), read = calls.at(-1), error = rejectedNetwork()
  read.reject(error); await settle()
  const failed = snapshot(w, result, 'initial GET rejected'); safeState(result, failed, FAILED, 'REJECTED_GET')
  expect(result, failed.failureCount === 1 && failed.failureKind === 'network' && failed.nativeRetryCount === 1, 'REJECTION_ORIGINAL_VISIBLE_NETWORK_FAILURE_RETRY')
  const retry = await clickRead(w, '重试读取'); safeState(result, snapshot(w, result, 'retry from rejected GET pending'), PENDING, 'REJECT_RETRY_PENDING')
  await finish(retry, ok([row('SYNTHETIC_RECOVERED_ROW')]))
  const recovered = snapshot(w, result, 'retry from rejected GET recovered rows')
  expect(result, recovered.visibleRows.length === 1 && recovered.visibleRows[0].includes('SYNTHETIC_RECOVERED_ROW'), 'RETRY_CAN_RECOVER_ACTUAL_NONEMPTY_ROW')
  expect(result, recovered.visibleEmptyCount === 0 && recovered.visibleStatus.length === 0 && recovered.failureCount === 0 && !recovered.tableLoading, 'RECOVERED_ROWS_RETIRE_EMPTY_STATUS_AND_FAILURE')
})
scenario('existing rows persist through pending and HTTP503; six-decimal money masking and no implicit selection remain', async result => {
  const w = await open(); await finish(calls.at(-1), ok([row('SYNTHETIC_EXISTING_ROW')]))
  const existing = snapshot(w, result, 'original confirmed rows')
  expect(result, existing.visibleRows.length === 1 && existing.visibleRows[0].includes('SYNTHETIC_EXISTING_ROW'), 'ORIGINAL_VISIBLE_NONEMPTY_ROW')
  expect(result, existing.visibleRows[0].includes('¥ 0.000062') && existing.visibleRows[0].includes('¥ 1.234567'), 'ORIGINAL_QUOTA_AND_USED_AMOUNT_SIX_DECIMALS')
  expect(result, existing.visibleRows[0].includes(MASKED_TOKEN) && !visibleText(w.element).includes(SYNTHETIC_TOKEN), 'ORIGINAL_DEFAULT_CREDENTIAL_MASK')
  expect(result, existing.implicitCheckboxCount === 0, 'NO_IMPLICIT_TABLE_ROW_SELECTION_CHECKBOX')
  const refresh = await clickRead(w, '刷新'), pending = snapshot(w, result, 'original rows while refresh pending')
  expect(result, pending.visibleRows[0] === existing.visibleRows[0] && pending.tableLoading && pending.visibleEmptyCount === 0, 'ORIGINAL_ROWS_PRESERVED_WHILE_PENDING')
  await finish(refresh, http503()); const failed = snapshot(w, result, 'original rows after HTTP503')
  expect(result, failed.visibleRows[0] === existing.visibleRows[0] && failed.failureCount === 1 && failed.visibleEmptyCount === 0 && failed.visibleStatus.length === 0, 'ORIGINAL_ROWS_PRESERVED_WITH_DURABLE_FAILURE_NOT_FALSE_EMPTY')
  const retry = await clickRead(w, '重试读取'); await finish(retry, ok([row('SYNTHETIC_RECOVERED_ROW')]))
  const recovered = snapshot(w, result, 'native GET retry recovered rows')
  expect(result, recovered.visibleRows[0]?.includes('SYNTHETIC_RECOVERED_ROW') && recovered.failureCount === 0 && recovered.implicitCheckboxCount === 0, 'NATIVE_RETRY_RECOVERS_ROWS_WITHOUT_WRITES_OR_SELECTION')
})
scenario('unknown actor does not read or claim a confirmed empty list', async result => {
  const w = await open(null)
  expect(result, calls.length === result.readStart, 'UNKNOWN_ACTOR_ZERO_GET')
  safeState(result, snapshot(w, result, 'unknown actor initial view'), UNCONFIRMED, 'UNKNOWN_ACTOR')
  nativeButton(w, '刷新').click(); await settle()
  expect(result, calls.length === result.readStart, 'UNKNOWN_ACTOR_NATIVE_REFRESH_STILL_ZERO_GET')
  safeState(result, snapshot(w, result, 'unknown actor after native refresh'), UNCONFIRMED, 'UNKNOWN_REFRESH')
})
scenario('explicit logout retires old rows and pending read without a false empty or late old paint', async result => {
  const w = await open(); await finish(calls.at(-1), ok([row('SYNTHETIC_RETIRED_OLD_ROW')]))
  const old = await clickRead(w, '刷新'), count = calls.length
  user.clearStoredCurrentUser(); await settle()
  const retired = snapshot(w, result, 'explicit currentUser logout'); safeState(result, retired, UNCONFIRMED, 'LOGOUT')
  expect(result, retired.visibleRows.length === 0 && !visibleText(w.element).includes('SYNTHETIC_RETIRED_OLD_ROW') && calls.length === count, 'LOGOUT_RETIRES_OLD_ROWS_WITHOUT_NEW_GET')
  await finish(old, ok([row('SYNTHETIC_LATE_OLD_ROW')]))
  const late = snapshot(w, result, 'old GET completed after logout'); safeState(result, late, UNCONFIRMED, 'LATE_LOGOUT')
  expect(result, late.visibleRows.length === 0 && !visibleText(w.element).includes('SYNTHETIC_LATE_OLD_ROW') && calls.length === count, 'LATE_OLD_OWNER_CANNOT_PAINT_OR_READ')
})
scenario('actor replacement retains existing owner retirement and only its own successful empty is affirmative', async result => {
  const w = await open(); await finish(calls.at(-1), ok([row('SYNTHETIC_OLD_ACTOR_ROW')]))
  const old = await clickRead(w, '刷新'), count = calls.length
  user.setStoredCurrentUser(B); await settle()
  assert.equal(calls.length, count + 1, 'ORIGINAL_ACTOR_CHANGE_STARTS_OWN_READ')
  const own = calls.at(-1); assert.equal(own.actor, B.id); assert.deepEqual(own.payload, {})
  const pending = snapshot(w, result, 'new actor own GET pending'); safeState(result, pending, PENDING, 'NEW_ACTOR_PENDING')
  expect(result, pending.visibleRows.length === 0 && !visibleText(w.element).includes('SYNTHETIC_OLD_ACTOR_ROW'), 'OLD_ACTOR_ROWS_RETIRED')
  await finish(old, http503()); const ignored = snapshot(w, result, 'retired actor HTTP503 ignored')
  safeState(result, ignored, PENDING, 'OLD_ACTOR_COMPLETION')
  expect(result, ignored.failureCount === 0 && ignored.tableLoading, 'OLD_FAILURE_CANNOT_PAINT_OR_RELEASE_NEW_GET')
  await finish(own, ok([])); confirmedEmpty(result, snapshot(w, result, 'new actor own HTTP200 empty'), 'NEW_ACTOR_SUCCESS')
})
