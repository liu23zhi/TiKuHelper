/* UserList empty-read-state qualification: actual Vue/Antd/currentUser, request
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
const target = path.join(targetWeb, 'src/pages/UserList/index.vue')
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
const A = { id: 77971, username: 'SYNTHETIC_EMPTY_READER_A', role: 'admin', status: 'enabled', balance: 3.234567 }
const B = { id: 77972, username: 'SYNTHETIC_EMPTY_READER_B', role: 'admin', status: 'enabled', balance: 4.345678 }
const ORDINARY = { id: 77973, username: 'SYNTHETIC_ORDINARY_READER', role: 'user', status: 'enabled', balance: 5.456789 }
const EMPTY = '当前还没有用户记录，点击右上角新增用户即可开始。'
const MOBILE_EMPTY = '当前还没有用户记录'
const PENDING = '正在读取用户列表，请稍候。'
const FAILED = '暂时无法读取用户列表，请使用上方重试。'
const UNCONFIRMED = '用户列表尚未确认，请使用具有用户管理权限的账号重试。'
const PRIVATE = 'SYNTHETIC_PRIVATE_UPSTREAM_URL_CONFIG_TOKEN'
const DIAGNOSTIC = '1234567890abcdef1234567890abcdef'
const otherReads = []
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const ok = (rows, payload = { page: 1, pageSize: 10 }, total = rows.length) => ({ status: 200, data: { data: rows, total, page: payload.page, pageSize: payload.pageSize } })
const row = name => ({ id: 371, username: name, nickname: '合成用户', role: 'user', status: 'enabled', balance: -1.234567, phone: '', wechatId: '', registerSource: 'admin', createdAt: '2026-10-29T00:00:00Z' })
function deny(kind) { forbidden.push(kind); throw new Error('FORBIDDEN_USERLIST_READ_ACTION_' + kind) }
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_REAL_USERLIST_READ_IO_' + kind) }
function getUserList(payload) {
  let yes, no
  const promise = new Promise((resolve, reject) => { yes = resolve; no = reject })
  const call = { name: 'getUserList', payload: clone(payload), actor: user.getStoredCurrentUser()?.id ?? null, synthetic: true, completed: false }
  call.resolve = value => { call.completed = true; call.outcome = value?.status === 200 ? 'HTTP200' : 'HTTP' + String(value?.status); yes(value) }
  call.reject = value => { call.completed = true; call.outcome = 'REJECT'; no(value) }
  calls.push(call)
  return promise
}
const api = new Proxy({ getUserList, getSystemSettings: () => { otherReads.push({ name: 'getSystemSettings', synthetic: true }); return Promise.resolve({ status: 200, data: { newUserBonusAmount: 0 } }) } }, { get(value, name) { if (name === '__esModule' || name === 'then') return undefined; return name in value ? value[name] : () => deny('API_' + String(name)) } })
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
  assert.deepEqual(calls.at(-1).payload, { page: 1, pageSize: 10 }, 'ORIGINAL_LIST_GET_PARAMS')
  return calls.at(-1)
}
function http503() { return buildResponsePayload({ status: 503, headers: { 'X-Request-ID': DIAGNOSTIC }, data: { message: PRIVATE, detail: PRIVATE, config: { url: PRIVATE, token: PRIVATE } } }) }
function rejectedNetwork() { return captureTransportRequestError(Object.assign(new Error(PRIVATE), { code: 'ERR_NETWORK', config: { url: PRIVATE, token: PRIVATE } })) }
function snapshot(w, result, label) {
  const table = w.element.querySelector('.ant-table'), empty = [...w.element.querySelectorAll('.ant-table-placeholder .ant-empty')].filter(visible)
  const status = [...w.element.querySelectorAll('.ant-table-placeholder p[role="status"]')].filter(visible)
  const failure = [...w.element.querySelectorAll('.request-failure')].filter(visible)
  const mobileEmpty = [...w.element.querySelectorAll('.mobile-user-list .ant-empty')].filter(visible)
  const mobileStatus = [...w.element.querySelectorAll('.mobile-user-list p[role="status"]')].filter(visible)
  const state = {
    label, reads: calls.length - result.readStart, tableLoading: Boolean(w.vm.tabLoading), canManageUsers: Boolean(w.vm.canManageUsers),
    mobileEmptyCount: mobileEmpty.length, mobileEmptyText: mobileEmpty.map(visibleText), mobileStatus: mobileStatus.map(visibleText), mobileRows: [...w.element.querySelectorAll('.mobile-user-card')].map(visibleText), rows: clone(w.vm.dataSource), summary: clone(w.vm.summary), filters: clone(w.vm.filters), pagination: { current: w.vm.pagination.current, pageSize: w.vm.pagination.pageSize, total: w.vm.pagination.total },
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
  expect(result, state.mobileEmptyCount === 0 && state.mobileStatus.length === 1 && state.mobileStatus[0] === expected, label + '_MOBILE_STRUCTURE_SAME_SAFE_STATUS_NOT_MEDIA_VISIBILITY')
}
function confirmedEmpty(result, state, label) {
  expect(result, state.visibleEmptyCount === 1 && state.visibleEmptyText[0] === EMPTY, label + '_ORIGINAL_SUCCESS_EMPTY')
  expect(result, state.visibleStatus.length === 0 && state.failureCount === 0 && state.tableLoading === false, label + '_CONFIRMED_NOT_PENDING_OR_FAILED')
  expect(result, state.mobileEmptyCount === 1 && state.mobileEmptyText[0] === MOBILE_EMPTY && state.mobileStatus.length === 0, label + '_ORIGINAL_MOBILE_SUCCESS_EMPTY_STRUCTURE')
}
function safeFeedback(result) {
  expect(result, !document.body.textContent.includes(PRIVATE) && !JSON.stringify(notices).includes(PRIVATE), 'NO_RAW_UPSTREAM_ERROR_BODY_URL_CONFIG_TOKEN')
}
function scenario(label, run) {
  test('USERLIST_READ_READ_STATE ' + label, async () => {
    const result = { label, readStart: calls.length, states: [], checks: [], executed: false, accepted: false }
    observations.push(result)
    try {
      await run(result); safeFeedback(result); result.executed = true
      const failures = result.checks.filter(check => !check.passed).map(check => check.label)
      result.failures = failures
      console.log('USERLIST_READ_NATIVE ' + JSON.stringify({ label, states: result.states, failures, syntheticGets: calls.length - result.readStart, forbidden: forbidden.length, realIO: network.length }))
      assert.deepEqual(failures, [], 'VISIBLE_READ_STATE_CONTRACT')
      result.accepted = true
    } catch (error) { result.failure = { name: error.name, message: error.message }; throw error }
  })
}
function record() {
  if (!process.env.USERLIST_READ_OBSERVATION_FILE) return
  fs.writeFileSync(process.env.USERLIST_READ_OBSERVATION_FILE, JSON.stringify({
    target, targetSha256Before: initialSHA, targetSha256After: sha(target), fixture: __filename, fixtureSha256: sha(__filename),
    dependencies: { fallbackWeb, helper, currentUser: path.join(fallbackWeb, 'src/utils/currentUser.js'), requestHelpers: path.join(fallbackWeb, 'src/utils/requestHelpers.mjs') },
    observations, otherReads, calls: calls.map(call => { const copy = { ...call }; delete copy.resolve; delete copy.reject; return copy }), forbidden, network, runtimeErrors, notices,
    qualification: 'Real Vue/Antd/currentUser/helpers in jsdom with ancestor-based visibility and native button clicks; synthetic deferred GET only; SFC CSS is not loaded, desktop and mobile branches structurally exercised but no browser viewport/media/layout, screen reader, real credentials, clipboard, CRUD, DB, provider or server-authorization qualification',
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

scenario('admin original successful empty remains desktop and mobile positive', async result => {
  const w = await open(), read = calls.at(-1)
  assert.deepEqual(read.payload, { page: 1, pageSize: 10 }); assert.equal(read.actor, A.id)
  await finish(read, ok([])); confirmedEmpty(result, snapshot(w, result, 'HTTP200 empty'), 'SUCCESS')
  expect(result, calls.length - result.readStart === 1, 'NO_AUTOMATIC_EXTRA_GET')
})
scenario('original nonempty rows summary and negative six-decimal balance remain', async result => {
  const w = await open(), original = row('SYNTHETIC_ORIGINAL_ROW')
  await finish(calls.at(-1), ok([original])); const state = snapshot(w, result, 'HTTP200 original rows')
  expect(result, state.visibleRows.length === 1 && state.visibleRows[0].includes(original.username), 'ORIGINAL_DESKTOP_ROW')
  expect(result, state.mobileRows.length === 1 && state.mobileRows[0].includes(original.username), 'ORIGINAL_MOBILE_CARD_STRUCTURE')
  expect(result, state.visibleRows[0].includes('¥ -1.234567') && state.mobileRows[0].includes('¥ -1.234567'), 'NEGATIVE_BALANCE_SIX_DECIMALS')
  expect(result, state.summary.totalBalance === -1.234567 && state.summary.total === 1 && state.summary.enabledCount === 1, 'ORIGINAL_SUMMARY')
  assert.deepEqual(state.rows, [original], 'ORIGINAL_DATA_NOT_DECORATED_OR_MUTATED')
  expect(result, state.visibleEmptyCount === 0 && state.mobileEmptyCount === 0 && state.visibleStatus.length === 0 && state.mobileStatus.length === 0, 'ROWS_NOT_EMPTY')
})
scenario('initial controlled GET pending is not a confirmed absence', async result => {
  const w = await open(), read = calls.at(-1)
  const pending = snapshot(w, result, 'initial GET controlled pending'); safeState(result, pending, PENDING, 'INITIAL_PENDING')
  expect(result, pending.tableLoading && pending.failureCount === 0, 'NATIVE_BUSY_NO_FAILURE')
  await finish(read, ok([])); confirmedEmpty(result, snapshot(w, result, 'recovered empty'), 'RECOVER')
})
scenario('native refresh 503 keeps original diagnostic and GET retry then recovers empty', async result => {
  const w = await open(); await finish(calls.at(-1), ok([]))
  confirmedEmpty(result, snapshot(w, result, 'confirmed before refresh'), 'BEFORE')
  const refresh = await clickRead(w, '刷新'), count = calls.length
  nativeButton(w, '刷新').click(); await settle(); expect(result, calls.length === count, 'LOADING_BLOCKS_SECOND_NATIVE_REFRESH')
  safeState(result, snapshot(w, result, 'refresh pending'), PENDING, 'REFRESH_PENDING')
  const error = http503(); await finish(refresh, error)
  const failed = snapshot(w, result, 'HTTP503'); safeState(result, failed, FAILED, 'HTTP503')
  expect(result, failed.failureCount === 1 && failed.failureKind === 'upstream' && failed.nativeRetryCount === 1, 'ORIGINAL_DURABLE_FAILURE_NATIVE_RETRY')
  expect(result, failed.diagnostic[0] === DIAGNOSTIC, 'VALIDATED_TRANSPORT_DIAGNOSTIC')
  expect(result, w.findComponent({ name: 'RequestFailureAlert' }).props('failure') === error, 'ORIGINAL_FAILURE_IDENTITY')
  const before = calls.length; await settle(); expect(result, calls.length === before, 'NO_AUTOMATIC_RETRY')
  const retry = await clickRead(w, '重试读取'); safeState(result, snapshot(w, result, 'native retry pending'), PENDING, 'RETRY_PENDING')
  await finish(retry, ok([])); confirmedEmpty(result, snapshot(w, result, 'retry HTTP200 empty'), 'RETRY_EMPTY')
})
scenario('rejected initial GET stays unconfirmed and native retry recovers rows', async result => {
  const w = await open(); calls.at(-1).reject(rejectedNetwork()); await settle()
  const failed = snapshot(w, result, 'initial reject'); safeState(result, failed, FAILED, 'REJECTED')
  expect(result, failed.failureCount === 1 && failed.failureKind === 'network' && failed.nativeRetryCount === 1, 'ORIGINAL_NETWORK_FAILURE_RETRY')
  const retry = await clickRead(w, '重试读取'); safeState(result, snapshot(w, result, 'retry pending'), PENDING, 'REJECT_RETRY')
  await finish(retry, ok([row('SYNTHETIC_RECOVERED_ROW')]))
  const recovered = snapshot(w, result, 'retry rows')
  expect(result, recovered.visibleRows[0]?.includes('SYNTHETIC_RECOVERED_ROW') && recovered.mobileRows[0]?.includes('SYNTHETIC_RECOVERED_ROW'), 'RETRY_RECOVERS_DESKTOP_AND_MOBILE_ROWS')
  expect(result, !recovered.visibleEmptyCount && !recovered.mobileEmptyCount && !recovered.visibleStatus.length && !recovered.mobileStatus.length && !recovered.failureCount && !recovered.tableLoading, 'ROWS_RETIRE_FAILURE_EMPTY_AND_STATUS')
})
scenario('known rows summary filters and pagination persist during pending and failed refresh', async result => {
  const w = await open(), original = row('SYNTHETIC_PRESERVED_ROW')
  await finish(calls.at(-1), ok([original])); const existing = snapshot(w, result, 'known rows')
  const refresh = await clickRead(w, '刷新'), pending = snapshot(w, result, 'rows while pending')
  expect(result, pending.visibleRows[0] === existing.visibleRows[0] && pending.mobileRows[0] === existing.mobileRows[0] && pending.tableLoading && !pending.visibleEmptyCount && !pending.mobileEmptyCount, 'ROWS_PERSIST_PENDING')
  await finish(refresh, http503()); const failed = snapshot(w, result, 'known rows with failed refresh')
  expect(result, failed.visibleRows[0] === existing.visibleRows[0] && failed.mobileRows[0] === existing.mobileRows[0] && failed.failureCount === 1, 'ROWS_PERSIST_FAILURE')
  for (const key of ['rows', 'summary', 'filters', 'pagination']) assert.deepEqual(failed[key], existing[key], 'ORIGINAL_' + key.toUpperCase() + '_UNCHANGED')
  expect(result, !failed.visibleEmptyCount && !failed.mobileEmptyCount && !failed.visibleStatus.length && !failed.mobileStatus.length, 'KNOWN_ROWS_NOT_FALSE_EMPTY')
  const retry = await clickRead(w, '重试读取'); await finish(retry, ok([original])); expect(result, !snapshot(w, result, 'retry rows').failureCount, 'NATIVE_GET_RECOVERY')
})
for (const actor of [null, ORDINARY]) scenario((actor ? 'ordinary' : 'unknown') + ' principal cannot read or assert successful emptiness', async result => {
  const w = await open(actor)
  expect(result, calls.length === result.readStart, 'UNAUTHORIZED_OR_UNKNOWN_ZERO_GET')
  safeState(result, snapshot(w, result, 'initial no permission or unconfirmed'), UNCONFIRMED, 'NOT_ADMIN')
  expect(result, visibleText(w.element).includes('当前账号无用户管理权限。'), 'EXISTING_PERMISSION_ALERT')
  nativeButton(w, '刷新').click(); await settle(); expect(result, calls.length === result.readStart, 'NATIVE_REFRESH_STILL_ZERO_GET')
  safeState(result, snapshot(w, result, 'after refused refresh'), UNCONFIRMED, 'REFUSED_REFRESH')
})
scenario('same actor admin role loss retires rows and pending GET without late paint', async result => {
  const w = await open(); await finish(calls.at(-1), ok([row('SYNTHETIC_ADMIN_ROW')]))
  const old = await clickRead(w, '刷新'), count = calls.length
  user.setStoredCurrentUser({ ...A, role: 'user' }); await settle()
  const retired = snapshot(w, result, 'role loss'); safeState(result, retired, UNCONFIRMED, 'ROLE_LOSS')
  expect(result, !retired.visibleRows.length && !retired.mobileRows.length && calls.length === count && !retired.canManageUsers, 'ROLE_LOSS_RETIRES_ROWS_NO_READ')
  await finish(old, http503()); const late = snapshot(w, result, 'retired admin 503'); safeState(result, late, UNCONFIRMED, 'RETIRED_ROLE')
  expect(result, !late.failureCount && !late.tableLoading && calls.length === count, 'RETIRED_FAILURE_CANNOT_PAINT')
})
scenario('explicit logout retires rows and pending GET without false empty or late paint', async result => {
  const w = await open(); await finish(calls.at(-1), ok([row('SYNTHETIC_OLD_LOGOUT_ROW')]))
  const old = await clickRead(w, '刷新'), count = calls.length
  user.clearStoredCurrentUser(); await settle()
  const retired = snapshot(w, result, 'explicit logout'); safeState(result, retired, UNCONFIRMED, 'LOGOUT')
  expect(result, !retired.visibleRows.length && !retired.mobileRows.length && calls.length === count, 'LOGOUT_RETIRES_ROWS_NO_GET')
  await finish(old, ok([row('SYNTHETIC_LATE_LOGOUT_ROW')])); const late = snapshot(w, result, 'late old GET'); safeState(result, late, UNCONFIRMED, 'LATE_LOGOUT')
  expect(result, !late.visibleRows.length && !late.mobileRows.length && calls.length === count, 'LATE_OWNER_NO_PAINT_OR_READ')
})
scenario('admin actor replacement only confirms its own empty GET and ignores old failure', async result => {
  const w = await open(); await finish(calls.at(-1), ok([row('SYNTHETIC_OLD_ACTOR_ROW')]))
  const old = await clickRead(w, '刷新'), count = calls.length
  user.setStoredCurrentUser(B); await settle(); assert.equal(calls.length, count + 1, 'NEW_ADMIN_ONE_OWN_GET')
  const own = calls.at(-1); assert.equal(own.actor, B.id); assert.deepEqual(own.payload, { page: 1, pageSize: 10 })
  const pending = snapshot(w, result, 'new actor pending'); safeState(result, pending, PENDING, 'NEW_ACTOR')
  expect(result, !pending.visibleRows.length && !pending.mobileRows.length, 'OLD_ROWS_RETIRED')
  await finish(old, http503()); const ignored = snapshot(w, result, 'old failure ignored'); safeState(result, ignored, PENDING, 'OLD_COMPLETION')
  expect(result, !ignored.failureCount && ignored.tableLoading, 'OLD_FAILURE_CANNOT_PAINT_OR_RELEASE_BUSY')
  await finish(own, ok([])); confirmedEmpty(result, snapshot(w, result, 'own HTTP200 empty'), 'OWN_SUCCESS')
})
