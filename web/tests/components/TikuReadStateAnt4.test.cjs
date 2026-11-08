/* Actual Vue/Antd/currentUser and product zhCN provider. Only the existing
 * readonly POST /adapter-service/questions/search and courses GET are synthetic.
 * Sparse B/M/R target SFCs resolve unchanged dependencies from the checkout.
 * No source rewriting, VM state assignment, real network/DB/provider, CRUD,
 * import/export or clipboard. jsdom is not browser layout or reader acceptance.
 */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const fallbackWeb = path.resolve(process.env.UI_TEST_WEB_FALLBACK || path.resolve(__dirname, '../..'))
const targetWeb = path.resolve(process.env.TIKU_WEB_SOURCE || fallbackWeb)
const target = path.join(targetWeb, 'src/pages/TikuComponent/index.vue')
const sha = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
const initialSHA = sha(target), observations = [], calls = [], courseCalls = [], wrappers = [], histories = [], runtimeErrors = [], vueWarnings = [], network = [], forbidden = [], notices = []
const sourceEnv = process.env.TIKU_WEB_SOURCE
process.env.NODE_PATH = [path.join(fallbackWeb, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
process.env.TIKU_WEB_SOURCE = fallbackWeb
const helper = path.join(fallbackWeb, 'tests/helpers/vue-dom.cjs')
require(helper)
if (sourceEnv === undefined) delete process.env.TIKU_WEB_SOURCE; else process.env.TIKU_WEB_SOURCE = sourceEnv
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, h, defineComponent } = require('vue')
const Antd = require('ant-design-vue'), zhCN = require('ant-design-vue/lib/locale/zh_CN').default
const { createRouter, createMemoryHistory } = require('vue-router')
const user = require(path.join(fallbackWeb, 'src/utils/currentUser.js'))
const { buildResponsePayload, captureTransportRequestError } = require(path.join(fallbackWeb, 'src/utils/requestHelpers.mjs'))
const A = { id: 77981, username: 'SYNTHETIC_TIKU_READER_A', role: 'admin', status: 'enabled' }
const B = { id: 77982, username: 'SYNTHETIC_TIKU_READER_B', role: 'admin', status: 'enabled' }
const EMPTY = '暂无数据', PENDING = '正在读取题目列表，请稍候。'
const FAILED = '暂时无法读取题目列表，请使用上方重试。'
const UNCONFIRMED = '题目列表尚未确认，请使用管理员账号登录。'
const PRIVATE = 'SYNTHETIC_PRIVATE_UPSTREAM_URL_CONFIG_QUESTION'
const DIAGNOSTIC = '1234567890abcdef1234567890abcdef'
const QUERY = { type: -1, pageSize: 10, pageNo: 0, question: '', source: 0, extra: '', onlyShowEmptyAnswer: false }
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const ok = rows => ({ status: 200, data: { items: rows, total: rows.length } })
const row = (question, id = 101, type = 4) => ({ id, question, type, course_name: 'SYNTHETIC课程', options: '[]', answer: '["自由文本答案","自由文本答案"]', source: 0, source_name: 'SYNTHETIC来源', extra: 'SYNTHETIC低频字段', fixture_zero: 0 })
function deny(kind) { forbidden.push(kind); throw new Error('FORBIDDEN_TIKU_READ_ACTION_' + kind) }
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_REAL_TIKU_IO_' + kind) }
function getQuestions(payload) {
  let yes, no
  const promise = new Promise((resolve, reject) => { yes = resolve; no = reject })
  const call = { name: 'getQuestions', method: 'POST', path: '/adapter-service/questions/search', semantics: 'readonly list', payload: clone(payload), actor: user.getStoredCurrentUser()?.id ?? null, synthetic: true, completed: false }
  call.resolve = value => { call.completed = true; call.outcome = value?.status === 200 ? 'HTTP200' : 'HTTP' + String(value?.status); yes(value) }
  call.reject = value => { call.completed = true; call.outcome = 'REJECT'; no(value) }
  calls.push(call); return promise
}
const apiSource = fs.readFileSync(path.join(fallbackWeb, 'src/api/TikuComponentApi.js'), 'utf8')
const actualAPI = apiSource.match(/export function getQuestions\(data\) \{([\s\S]*?)\n\}/)?.[1]
assert.match(actualAPI, /url: `\/adapter-service\/questions\/search`/)
assert.match(actualAPI, /method: 'post'/)
const api = new Proxy({ getQuestions, getCourses: () => { courseCalls.push({ name: 'getCourses', method: 'GET', path: '/adapter-service/courses', synthetic: true }); return Promise.resolve({ status: 200, data: [] }) } }, { get(value, name) { if (name === '__esModule' || name === 'then') return undefined; return name in value ? value[name] : () => deny('API_' + String(name)) } })
const originalLoad = Module._load
Module._load = function (request, parent, main) {
  // The only relative page imports are its existing OptionBox/AnswerBox; load
  // their exact unchanged checkout sources, not stubs or rewritten target code.
  if (parent?.filename === target && request.startsWith('.') && targetWeb !== fallbackWeb) return originalLoad.call(this, path.join(fallbackWeb, path.relative(targetWeb, path.resolve(path.dirname(target), request))), parent, main)
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
window.XMLHttpRequest.prototype.open = () => denyIO('XHR')
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
Object.defineProperty(window.navigator, 'sendBeacon', { configurable: true, value: () => denyIO('BEACON') })
Object.defineProperty(window.navigator, 'clipboard', { configurable: true, value: { writeText: () => deny('CLIPBOARD'), readText: () => deny('CLIPBOARD') } })
document.execCommand = () => deny('LEGACY_CLIPBOARD')
window.URL.createObjectURL = () => deny('EXPORT_BLOB')
window.HTMLAnchorElement.prototype.click = () => deny('DOWNLOAD')
window.open = () => deny('WINDOW_OPEN')
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
function nativeButton(w, label, enabled = true) {
  const buttons = [...w.element.querySelectorAll('button')].filter(node => visible(node) && visibleText(node).replace(/\s/g, '') === label.replace(/\s/g, ''))
  assert.equal(buttons.length, 1, 'ONE_VISIBLE_NATIVE_BUTTON_' + label)
  assert.ok(buttons[0].classList.contains('ant-btn'), 'REAL_ANTD_BUTTON_' + label)
  assert.equal(buttons[0].disabled, !enabled, 'NATIVE_BUTTON_PERMISSION_' + label)
  return buttons[0]
}
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(actor = A) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => h('div') } }] })
  await router.push('/'); await router.isReady(); histories.push(router.options.history)
  const Host = defineComponent({ render: () => h(Antd.ConfigProvider, { locale: zhCN, autoInsertSpaceInButton: false }, () => h(Page)) })
  const host = mount(Host, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)), warnHandler: warning => vueWarnings.push(String(warning)) } } })
  wrappers.push(host); await settle(); return host.findComponent(Page)
}
async function finish(call, response) { assert.ok(call, 'ISSUED_SYNTHETIC_READONLY_POST'); call.resolve(response); await settle() }
async function clickRead(w, label) {
  const before = calls.length; nativeButton(w, label).click(); await settle()
  assert.equal(calls.length, before + 1, 'ONE_READONLY_POST_FROM_NATIVE_' + label)
  assert.deepEqual(calls.at(-1).payload, QUERY, 'ORIGINAL_SEARCH_POST_FILTERS_PAGINATION')
  return calls.at(-1)
}
function http503() { return buildResponsePayload({ status: 503, headers: { 'X-Request-ID': DIAGNOSTIC }, data: { message: PRIVATE, detail: PRIVATE, config: { url: PRIVATE, question: PRIVATE } } }) }
function rejectedNetwork() { return captureTransportRequestError(Object.assign(new Error(PRIVATE), { code: 'ERR_NETWORK', config: { url: PRIVATE, question: PRIVATE } })) }
function snapshot(w, result, label) {
  const table = w.element.querySelector('.ant-table'), empty = [...w.element.querySelectorAll('.ant-table-placeholder .ant-empty')].filter(visible)
  const status = [...w.element.querySelectorAll('.ant-table-placeholder p[role="status"]')].filter(visible)
  const failure = [...w.element.querySelectorAll('.request-failure')].filter(visible)
  const state = { label, reads: calls.length - result.readStart, tableLoading: Boolean(w.vm.tabLoading), visibleEmptyCount: empty.length, visibleEmptyText: empty.map(node => visibleText(node.querySelector('.ant-empty-description'))), visibleStatus: status.map(visibleText), failureCount: failure.length, failureKind: failure[0]?.getAttribute('data-failure-kind') ?? null, failureText: failure.map(visibleText), visibleRows: [...(table?.querySelectorAll('tbody tr.ant-table-row') || [])].filter(visible).map(visibleText), selected: [...w.vm.selectedRowKeys], diagnostic: [...w.element.querySelectorAll('[data-request-diagnostic]')].filter(visible).map(visibleText), nativeRetryCount: [...w.element.querySelectorAll('button[data-action="retry-request"]')].filter(visible).length }
  result.states.push(state); return state
}
function expect(result, condition, label) { result.checks.push({ label, passed: Boolean(condition) }) }
function safeState(result, state, expected, label) {
  expect(result, state.visibleEmptyCount === 0 && !state.visibleEmptyText.includes(EMPTY), label + '_NO_CONFIRMED_EMPTY')
  expect(result, state.visibleStatus.length === 1 && state.visibleStatus[0] === expected, label + '_EXACT_SAFE_CHINESE_STATUS')
}
function confirmedEmpty(result, state, label) {
  expect(result, state.visibleEmptyCount === 1 && state.visibleEmptyText[0] === EMPTY, label + '_ORIGINAL_ZHCN_EMPTY')
  expect(result, state.visibleStatus.length === 0 && state.failureCount === 0 && !state.tableLoading, label + '_CONFIRMED_NOT_PENDING_OR_FAILED')
}
function scenario(label, run) {
  test('TIKU_READ_STATE ' + label, async () => {
    const result = { label, readStart: calls.length, states: [], checks: [], executed: false, accepted: false }; observations.push(result)
    try {
      await run(result)
      expect(result, !document.body.textContent.includes(PRIVATE) && !JSON.stringify(notices).includes(PRIVATE), 'NO_RAW_RESPONSE_ERROR_URL_CONFIG_OR_QUESTION')
      result.executed = true; result.failures = result.checks.filter(check => !check.passed).map(check => check.label)
      console.log('TIKU_READ_NATIVE ' + JSON.stringify({ label, states: result.states, failures: result.failures, syntheticReadonlyPosts: calls.length - result.readStart, forbidden: forbidden.length, realIO: network.length }))
      assert.deepEqual(result.failures, [], 'VISIBLE_TIKU_READ_STATE_CONTRACT'); result.accepted = true
    } catch (error) { result.failure = { name: error.name, message: error.message }; throw error }
  })
}
function record() {
  if (!process.env.TIKU_READ_OBSERVATION_FILE) return
  fs.writeFileSync(process.env.TIKU_READ_OBSERVATION_FILE, JSON.stringify({ target, targetSha256Before: initialSHA, targetSha256After: sha(target), fixture: __filename, fixtureSha256: sha(__filename), locale: 'actual Antd.ConfigProvider + zhCN', dependencies: { fallbackWeb, helper, currentUser: path.join(fallbackWeb, 'src/utils/currentUser.js') }, observations, calls: calls.map(call => { const copy = { ...call }; delete copy.resolve; delete copy.reject; return copy }), courseCalls, forbidden, network, runtimeErrors, vueWarnings, notices, qualification: 'Actual Vue/Antd/currentUser/zhCN in jsdom with ancestor visibility and native button clicks; only deferred readonly POST search and courses GET mocks; no real IO, CRUD, import/export, clipboard, DB/provider, viewport or reader acceptance' }, null, 2) + '\n')
}
afterEach(async () => {
  for (const w of wrappers.splice(0)) if (w.exists()) w.unmount()
  for (const call of calls) if (!call.completed) call.resolve({ status: 503 })
  await settle(); for (const history of histories.splice(0)) history.destroy()
  Antd.Modal.destroyAll(); Antd.message.destroy(); await settle()
  user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren(); record()
  assert.equal(sha(target), initialSHA, 'BOUND_TARGET_UNCHANGED_BY_FIXTURE')
  assert.deepEqual(forbidden, [], 'ZERO_CRUD_IMPORT_EXPORT_CLIPBOARD_OR_OTHER_API')
  assert.deepEqual(network, [], 'ZERO_REAL_NETWORK_DB_PROVIDER')
  assert.deepEqual(runtimeErrors, [], 'ZERO_HIDDEN_VUE_ANTD_RUNTIME_ERRORS')
  assert.deepEqual(vueWarnings, [], 'ZERO_VUE_WARNINGS')
})
after(() => { record(); Module._load = originalLoad })
scenario('successful admin empty preserves original Chinese Antd empty', async result => {
  const w = await open(), read = calls.at(-1)
  assert.deepEqual(read.payload, QUERY); assert.equal(read.actor, A.id)
  await finish(read, ok([])); confirmedEmpty(result, snapshot(w, result, 'HTTP200 empty'), 'SUCCESS')
  expect(result, calls.length - result.readStart === 1, 'NO_AUTOMATIC_EXTRA_POST')
})
scenario('initial pending is not confirmed absence and recovers empty', async result => {
  const w = await open(), read = calls.at(-1), pending = snapshot(w, result, 'initial pending')
  safeState(result, pending, PENDING, 'INITIAL_PENDING'); expect(result, pending.tableLoading && !pending.failureCount, 'INITIAL_NATIVE_BUSY')
  await finish(read, ok([])); confirmedEmpty(result, snapshot(w, result, 'initial success empty'), 'RECOVER_EMPTY')
})
scenario('native header refresh 503 and native failure retry recover empty', async result => {
  const w = await open(); await finish(calls.at(-1), ok([])); confirmedEmpty(result, snapshot(w, result, 'before refresh'), 'BEFORE')
  const refresh = await clickRead(w, '刷新'), count = calls.length
  nativeButton(w, '刷新').click(); await settle(); expect(result, calls.length === count, 'LOADING_BLOCKS_DOUBLE_REFRESH')
  safeState(result, snapshot(w, result, 'refresh pending'), PENDING, 'REFRESH')
  const error = http503(); await finish(refresh, error); const failed = snapshot(w, result, 'HTTP503')
  safeState(result, failed, FAILED, 'HTTP503')
  expect(result, failed.failureCount === 1 && failed.failureKind === 'upstream' && failed.nativeRetryCount === 1, 'ORIGINAL_DURABLE_ALERT_NATIVE_RETRY')
  expect(result, failed.diagnostic[0] === DIAGNOSTIC && w.findComponent({ name: 'RequestFailureAlert' }).props('failure') === error, 'ORIGINAL_SAFE_FAILURE_IDENTITY_DIAGNOSTIC')
  await settle(); expect(result, calls.length === count, 'NO_AUTOMATIC_POST_RETRY')
  const retry = await clickRead(w, '重试读取'); safeState(result, snapshot(w, result, 'retry pending'), PENDING, 'RETRY')
  await finish(retry, ok([])); confirmedEmpty(result, snapshot(w, result, 'retry empty'), 'RETRY_EMPTY')
})
scenario('rejected search remains unconfirmed and native retry recovers rows', async result => {
  const w = await open(); calls.at(-1).reject(rejectedNetwork()); await settle(); const failed = snapshot(w, result, 'rejected search')
  safeState(result, failed, FAILED, 'REJECTED'); expect(result, failed.failureCount === 1 && failed.failureKind === 'network', 'ORIGINAL_NETWORK_FAILURE')
  const retry = await clickRead(w, '重试读取'); safeState(result, snapshot(w, result, 'rejected retry pending'), PENDING, 'REJECT_RETRY')
  await finish(retry, ok([row('SYNTHETIC_RECOVERED_ROW')])); const recovered = snapshot(w, result, 'retry rows')
  expect(result, recovered.visibleRows.length === 1 && recovered.visibleRows[0].includes('SYNTHETIC_RECOVERED_ROW'), 'RECOVERED_ACTUAL_ROW')
  expect(result, !recovered.visibleEmptyCount && !recovered.visibleStatus.length && !recovered.failureCount && !recovered.tableLoading, 'ROWS_RETIRE_EMPTY_STATUS_FAILURE')
})
scenario('plain zero nonobjective repeated answers and long rows persist pending and 503', async result => {
  const longQuestion = 'SYNTHETIC_LONG_' + '长题面原文'.repeat(80)
  const rows = [{ ...row('SYNTHETIC_ZERO_ANSWER', 101, 0), options: '["0","1"]', answer: '["0"]' }, row(longQuestion, 102, 4)]
  const w = await open(); await finish(calls.at(-1), ok(rows)); const existing = snapshot(w, result, 'original rows')
  assert.deepEqual(clone(w.vm.data), rows, 'PLAIN_ROWS_ZERO_FREE_TEXT_DUPLICATES_LOW_FREQUENCY_PRESERVED')
  expect(result, existing.visibleRows.length === 2 && existing.visibleRows[0].includes('0') && existing.visibleRows[1].includes('自由文本答案'), 'ORIGINAL_ZERO_AND_NONOBJECTIVE_VISIBLE')
  expect(result, visibleText(w.element).includes(longQuestion) && existing.selected.length === 0, 'LONG_QUESTION_NO_IMPLICIT_SELECTION')
  const refresh = await clickRead(w, '刷新'), pending = snapshot(w, result, 'rows pending')
  expect(result, JSON.stringify(pending.visibleRows) === JSON.stringify(existing.visibleRows) && pending.tableLoading && !pending.visibleEmptyCount, 'OLD_ROWS_RETAINED_PENDING')
  await finish(refresh, http503()); const failed = snapshot(w, result, 'rows HTTP503')
  expect(result, JSON.stringify(failed.visibleRows) === JSON.stringify(existing.visibleRows) && failed.failureCount === 1 && !failed.visibleEmptyCount && !failed.visibleStatus.length, 'OLD_ROWS_RETAINED_FAILURE')
  assert.deepEqual(clone(w.vm.data), rows)
  const retry = await clickRead(w, '重试读取'); await finish(retry, ok([row('SYNTHETIC_NEW_ROW')]))
  const recovered = snapshot(w, result, 'replacement rows'); expect(result, recovered.visibleRows[0]?.includes('SYNTHETIC_NEW_ROW') && !recovered.failureCount && recovered.selected.length === 0, 'READONLY_RECOVERY_ROWS')
})
for (const actor of [null, { ...A, role: 'user' }]) scenario((actor ? 'ordinary role' : 'unknown actor') + ' zero reads and no confirmed empty', async result => {
  const w = await open(actor); safeState(result, snapshot(w, result, 'no permission'), UNCONFIRMED, 'NO_PERMISSION')
  nativeButton(w, '刷新', false).click(); await settle(); expect(result, calls.length === result.readStart, 'ZERO_POST_WITHOUT_ADMIN')
})
for (const transition of ['logout', 'role-revoked']) scenario(transition + ' retires rows pending failure and late old publication', async result => {
  const w = await open(); await finish(calls.at(-1), ok([row('SYNTHETIC_RETIRED_ROW')]))
  const old = await clickRead(w, '刷新'), count = calls.length
  if (transition === 'logout') user.clearStoredCurrentUser(); else user.setStoredCurrentUser({ ...A, role: 'user' })
  await settle(); const retired = snapshot(w, result, 'retired actor'); safeState(result, retired, UNCONFIRMED, 'RETIRED')
  expect(result, !retired.visibleRows.length && calls.length === count, 'OLD_ROWS_RETIRED_ZERO_NEW_POST')
  await finish(old, http503()); const late = snapshot(w, result, 'late old failure'); safeState(result, late, UNCONFIRMED, 'LATE')
  expect(result, !late.visibleRows.length && !late.failureCount && !late.tableLoading && calls.length === count, 'LATE_FAILURE_NO_PUBLICATION_OR_FINALLY')
})
scenario('admin replacement only own successful empty confirms absence', async result => {
  const w = await open(); await finish(calls.at(-1), ok([row('SYNTHETIC_OLD_ACTOR_ROW')]))
  const old = await clickRead(w, '刷新'), count = calls.length; user.setStoredCurrentUser(B); await settle()
  assert.equal(calls.length, count + 1); const own = calls.at(-1); assert.equal(own.actor, B.id); assert.deepEqual(own.payload, QUERY)
  const pending = snapshot(w, result, 'new actor pending'); safeState(result, pending, PENDING, 'NEW_ACTOR')
  expect(result, !pending.visibleRows.length && !visibleText(w.element).includes('SYNTHETIC_OLD_ACTOR_ROW'), 'OLD_ACTOR_ROWS_RETIRED')
  await finish(old, http503()); const ignored = snapshot(w, result, 'old failure ignored'); safeState(result, ignored, PENDING, 'OLD_COMPLETION')
  expect(result, !ignored.failureCount && ignored.tableLoading, 'OLD_FAILURE_CANNOT_RELEASE_NEW_BUSY')
  await finish(own, ok([])); confirmedEmpty(result, snapshot(w, result, 'own success empty'), 'NEW_ACTOR_SUCCESS')
})
