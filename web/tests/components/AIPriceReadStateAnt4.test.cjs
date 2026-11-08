/* Actual parent Vue/Antd/zhCN -> actual price child. Deferred synthetic GETs only.
 * One unchanged fixture is selected by TIKU_WEB_SOURCE for all B/M/R arms.
 * No real provider, network, DB, clipboard, CRUD or source rewriting occurs.
 * jsdom native click/visibility evidence is not full browser geometry or auth parity.
 */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const fallbackWeb = path.resolve(process.env.UI_TEST_WEB_FALLBACK || path.resolve(__dirname, '../..'))
const targetWeb = path.resolve(process.env.TIKU_WEB_SOURCE || fallbackWeb)
const targets = ['index.vue', 'AIModelPriceTable.vue', 'AIConnectionModelSection.vue'].map(file => path.join(targetWeb, 'src/pages/AISearchConfig', file))
const sha = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
const initialHashes = Object.fromEntries(targets.map(file => [file, sha(file)]))
const sourceEnv = process.env.TIKU_WEB_SOURCE
process.env.NODE_PATH = [path.join(fallbackWeb, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
process.env.TIKU_WEB_SOURCE = fallbackWeb
require(path.join(fallbackWeb, 'tests/helpers/vue-dom.cjs'))
if (sourceEnv === undefined) delete process.env.TIKU_WEB_SOURCE; else process.env.TIKU_WEB_SOURCE = sourceEnv
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, h, defineComponent, toRaw } = require('vue')
const Antd = require('ant-design-vue'), zhCN = require('ant-design-vue/lib/locale/zh_CN').default
const user = require(path.join(fallbackWeb, 'src/utils/currentUser.js'))
const { buildResponsePayload, captureTransportRequestError } = require(path.join(fallbackWeb, 'src/utils/requestHelpers.mjs'))
const A = { id: 77981, username: 'SYNTHETIC_PRICE_READER_A', role: 'admin', status: 'enabled', balance: 1.234567 }
const B = { id: 77982, username: 'SYNTHETIC_PRICE_READER_B', role: 'admin', status: 'enabled', balance: 2.345678 }
const CONFIG = { enabled: true, baseUrl: '', apiKey: '', model: 'SYNTHETIC_MODEL', manualModels: ['SYNTHETIC_MODEL'], prompt: '', temperature: 0, topP: 0 }
const PRIVATE = 'SYNTHETIC_PRIVATE_UPSTREAM_URL_CONFIG_KEY', DIAGNOSTIC = '1234567890abcdef1234567890abcdef'
const EMPTY = '暂无模型价格。', PENDING = '正在读取模型价格，请稍候。', FAILED = '暂时无法读取模型价格，请使用上方重试。', UNCONFIRMED = '模型价格表尚未确认，请使用上方刷新。'
const calls = [], wrappers = [], observations = [], forbidden = [], network = [], runtimeErrors = [], warnings = [], notices = []
const ok = data => ({ status: 200, data }), clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const row = label => ({ id: 501, modelName: 'SYNTHETIC_MODEL', displayName: label, inputPrice: 0.000001, cachedPrice: 0, cacheWritePrice: 0.000062, outputPrice: 1.234567, multiplier: 1.2345, status: 'enabled', remark: '', isUserModified: true })
function deny(name) { forbidden.push(name); throw new Error('FORBIDDEN_PRICE_READ_ACTION_' + name) }
function denyIO(name) { network.push(name); throw new Error('FORBIDDEN_PRICE_READ_IO_' + name) }
function getAIModelPrices(...args) {
  let yes, no
  const promise = new Promise((resolve, reject) => { yes = resolve; no = reject })
  const call = { name: 'getAIModelPrices', method: 'GET', payload: clone(args), actor: user.getStoredCurrentUser()?.id ?? null, completed: false }
  call.resolve = value => { call.completed = true; call.outcome = value?.status === 200 ? 'HTTP200' : 'HTTP' + String(value?.status); yes(value) }
  call.reject = value => { call.completed = true; call.outcome = 'REJECT'; no(value) }
  calls.push(call); return promise
}
// Upstream parent mounts with one readonly OCR status GET; never clear/write/provider.
let ocrStatusReads = 0
function syntheticOCRStatus(feedback) {
  assert.deepEqual(feedback, { errorFeedback: 'local' }, 'exact readonly OCR feedback contract')
  ocrStatusReads += 1
  return Promise.resolve({ status: 200, data: { configured: false, entries: 0 } })
}
require('node:test').after(() => console.log('AI_OCR_STATUS_READS ' + JSON.stringify({ fixture: __filename, method: 'GET', count: ocrStatusReads, configured: false, entries: 0 })))
const api = new Proxy({ getAIOCRCacheStatus: syntheticOCRStatus, getAIModelPrices, getAISearchConfig: (...args) => { calls.push({ name: 'getAISearchConfig', method: 'GET', payload: clone(args), completed: true }); return Promise.resolve(ok(clone(CONFIG))) } }, { get(value, name) { if (name === '__esModule' || name === 'then') return undefined; return name in value ? value[name] : () => deny('API_' + String(name)) } })
const originalLoad = Module._load
Module._load = function (request, parent, main) {
  let filename
  if (request.startsWith('.') && parent?.filename?.startsWith(targetWeb + path.sep)) {
    const requested = path.resolve(path.dirname(parent.filename), request)
    if (!fs.existsSync(requested)) request = path.join(fallbackWeb, path.relative(targetWeb, requested))
  }
  try { filename = Module._resolveFilename(request, parent, main) } catch { return originalLoad.call(this, request, parent, main) }
  if (typeof filename === 'string' && (filename.startsWith(path.join(fallbackWeb, 'src/api') + path.sep) || filename.startsWith(path.join(targetWeb, 'src/api') + path.sep))) {
    if (path.basename(filename) === 'TikuComponentApi.js') return api
    return new Proxy({}, { get(_, name) { if (name === '__esModule' || name === 'then') return undefined; return () => deny('OTHER_API_' + String(name)) } })
  }
  return originalLoad.call(this, request, parent, main)
}
for (const name of ['success', 'warning', 'error']) { const original = Antd.message[name].bind(Antd.message); Antd.message[name] = (...args) => { notices.push({ name, text: String(args[0]?.content || args[0] || '') }); return original(...args) } }
window.XMLHttpRequest.prototype.open = () => denyIO('XHR')
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: () => deny('CLIPBOARD') } })
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
for (const name of ['node:net', 'node:tls']) { const transport = require(name); transport.connect = () => denyIO(name); if ('createConnection' in transport) transport.createConnection = () => denyIO(name) }
const Page = require(targets[0])
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
const clean = value => compact(value).replace(/\s/g, '')
function visible(node) {
  if (!node?.isConnected) return false
  for (let p = node; p && p.nodeType === 1; p = p.parentElement) { const s = window.getComputedStyle(p); if (p.hidden || p.getAttribute('aria-hidden') === 'true' || s.display === 'none' || ['hidden', 'collapse'].includes(s.visibility) || s.opacity === '0') return false }
  return true
}
function button(scope, label) { const found = [...scope.querySelectorAll('button')].filter(node => visible(node) && clean(node.textContent) === clean(label)); assert.equal(found.length, 1, 'ONE_NATIVE_' + label); assert.equal(found[0].disabled, false); return found[0] }
function priceRoot(w) { return w.element.querySelector('.price-table-wrap') }
function panel(w) { return w.element.querySelector('.pricing-grid') }
const reads = () => calls.filter(call => call.name === 'getAIModelPrices')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(actor = A) {
  const ocrReadStart = ocrStatusReads
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const Host = defineComponent({ render: () => h(Antd.ConfigProvider, { locale: zhCN, autoInsertSpaceInButton: false }, () => h(Page)) })
  const host = mount(Host, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => runtimeErrors.push(String(error.stack || error)), warnHandler: warning => warnings.push(String(warning)) } } })
  wrappers.push(host); await settle(); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return host.findComponent(Page)
}
async function finish(call, value) { assert.ok(call, 'ISSUED_PRICE_GET'); call.resolve(value); await settle() }
async function clickRead(w, label = '刷新') { const count = reads().length; button(panel(w), label).click(); await settle(); assert.equal(reads().length, count + 1, 'ONE_GET_NATIVE_' + label); assert.deepEqual(reads().at(-1).payload, []); return reads().at(-1) }
function http503() { return buildResponsePayload({ status: 503, headers: { 'X-Request-ID': DIAGNOSTIC }, data: { message: PRIVATE, config: { key: PRIVATE, url: PRIVATE } } }) }
function rejected() { return captureTransportRequestError(Object.assign(new Error(PRIVATE), { code: 'ERR_NETWORK', config: { key: PRIVATE } })) }
function snapshot(w, result, label) {
  const root = priceRoot(w), empty = [...root.querySelectorAll('.ant-table-placeholder .ant-empty')].filter(visible), status = [...root.querySelectorAll('.ant-table-placeholder [role="status"]')].filter(visible), failure = [...panel(w).querySelectorAll('.request-failure')].filter(visible)
  const state = { label, loading: Boolean(w.vm.priceLoading), confirmed: w.vm.priceReadConfirmed ?? null, empty: empty.map(node => compact(node.textContent)), status: status.map(node => compact(node.textContent)), rows: [...root.querySelectorAll('tbody tr[data-row-key]')].filter(visible).map(node => compact(node.textContent)), failure: failure.map(node => compact(node.textContent)), failureKind: failure[0]?.getAttribute('data-failure-kind') ?? null, diagnostic: [...panel(w).querySelectorAll('[data-request-diagnostic]')].filter(visible).map(node => node.textContent), retryCount: panel(w).querySelectorAll('[data-action="retry-request"]').length }
  result.states.push(state); return state
}
function expect(result, condition, label) { result.checks.push({ label, passed: Boolean(condition) }) }
function safe(result, state, text, label) { expect(result, state.empty.length === 0 && state.status.length === 1 && state.status[0] === text, label + '_FIXED_STATUS_NOT_FALSE_EMPTY') }
function empty(result, state) { expect(result, state.empty.length === 1 && state.empty[0] === EMPTY && state.status.length === 0 && state.failure.length === 0 && !state.loading, 'ONLY_HTTP200_EMPTY_IS_CONFIRMED_ABSENCE') }
function scenario(label, run) { test('AI_PRICE_READ_STATE ' + label, async () => {
  const result = { label, states: [], checks: [], readStart: reads().length, noticeStart: notices.length, accepted: false }; observations.push(result)
  try { await run(result); expect(result, !document.body.textContent.includes(PRIVATE) && !JSON.stringify(notices.slice(result.noticeStart)).includes(PRIVATE), 'NO_RAW_ERROR_KEY_URL_BODY'); result.failures = result.checks.filter(c => !c.passed).map(c => c.label); console.log('AI_PRICE_NATIVE ' + JSON.stringify({ label, states: result.states, failures: result.failures, reads: reads().length - result.readStart })); assert.deepEqual(result.failures, []); result.accepted = true } catch (error) { result.error = { name: error.name, message: error.message }; throw error }
}) }
function record() { if (process.env.AI_PRICE_OBSERVATION_FILE) fs.writeFileSync(process.env.AI_PRICE_OBSERVATION_FILE, JSON.stringify({ targets, initialHashes, finalHashes: Object.fromEntries(targets.map(file => [file, sha(file)])), fixture: __filename, fixtureSHA256: sha(__filename), observations, calls: calls.map(call => { const copy = { ...call }; delete copy.resolve; delete copy.reject; return copy }), forbidden, network, runtimeErrors, warnings, notices, qualification: 'Actual Vue/Antd/zhCN/currentUser parent to real child; native DOM clicks under jsdom with synthetic deferred GET only, not browser geometry, server auth, DB or provider qualification.' }, null, 2) + '\n') }
afterEach(async () => {
  for (const w of wrappers.splice(0)) if (w.exists()) w.unmount()
  for (const call of reads()) if (!call.completed) call.resolve({ status: 503 })
  await settle(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); user.clearStoredCurrentUser(); window.localStorage.clear(); document.body.replaceChildren(); record()
  assert.deepEqual(Object.fromEntries(targets.map(file => [file, sha(file)])), initialHashes)
  assert.deepEqual(forbidden, []); assert.deepEqual(network, []); assert.deepEqual(runtimeErrors, [])
})
after(() => { record(); Module._load = originalLoad })
scenario('initial pending then confirmed empty with real Antd zhCN and child', async result => {
  const w = await open(); safe(result, snapshot(w, result, 'initial pending'), PENDING, 'INITIAL_PENDING'); expect(result, w.vm.priceLoading, 'REAL_PARENT_LOADING'); await finish(reads().at(-1), ok([])); empty(result, snapshot(w, result, 'initial HTTP200 empty'))
})
scenario('native refresh 503 durable diagnostic and native GET retry recover empty', async result => {
  const w = await open(); await finish(reads().at(-1), ok([])); empty(result, snapshot(w, result, 'before refresh'))
  const read = await clickRead(w), count = reads().length; button(panel(w), '刷新').click(); await settle(); expect(result, reads().length === count, 'NATIVE_BUSY_REFRESH_NO_DUPLICATE_GET'); safe(result, snapshot(w, result, 'refresh pending'), PENDING, 'REFRESH_PENDING')
  const error = http503(); await finish(read, error); const failed = snapshot(w, result, 'refresh503'); safe(result, failed, FAILED, 'HTTP503'); expect(result, failed.failure.length === 1 && failed.failureKind === 'upstream' && failed.retryCount === 1 && failed.diagnostic[0] === DIAGNOSTIC, 'DURABLE_SAFE_503_DIAGNOSTIC_RETRY'); expect(result, w.vm.priceReadFailure === error, 'EXACT_FAILURE_IDENTITY'); await settle(); expect(result, reads().length === count, 'NO_AUTO_RETRY'); const retry = await clickRead(w, '重试读取'); safe(result, snapshot(w, result, 'retry pending'), PENDING, 'RETRY_PENDING'); await finish(retry, ok([])); empty(result, snapshot(w, result, 'retry empty'))
})
scenario('rejected GET safe persistent error native retry recovers rows', async result => {
  const w = await open(); reads().at(-1).reject(rejected()); await settle(); const failed = snapshot(w, result, 'initial reject'); safe(result, failed, FAILED, 'REJECT'); expect(result, failed.failureKind === 'network' && failed.retryCount === 1, 'NETWORK_FAILURE_HAS_NATIVE_RETRY'); const retry = await clickRead(w, '重试读取'); await finish(retry, ok([row('SYNTHETIC_RECOVERED_ROW')])); const recovered = snapshot(w, result, 'recovered rows'); expect(result, recovered.rows.length === 1 && recovered.rows[0].includes('SYNTHETIC_RECOVERED_ROW') && recovered.failure.length === 0 && recovered.status.length === 0 && recovered.empty.length === 0, 'RECOVER_REAL_PRICE_ROW')
})
scenario('old rows and actual config and price native drafts preserved through failure recovery', async result => {
  const w = await open(); await finish(reads().at(-1), ok([row('SYNTHETIC_EXISTING_ROW')])); const before = snapshot(w, result, 'original rows'), canonical = w.vm.priceForm
  expect(result, before.rows[0].includes('￥0.000062') && before.rows[0].includes('￥1.234567') && before.rows[0].includes('×1.2345'), 'SIX_PRICE_FOUR_MULTIPLIER_PRECISION')
  const model = w.element.querySelector('input[id$="-model"]'); assert.ok(model); model.value = 'SYNTHETIC_CONFIG_DRAFT'; model.dispatchEvent(new Event('input', { bubbles: true })); await settle()
  const saved = w.vm.savedConfigSnapshot, read = await clickRead(w); expect(result, snapshot(w, result, 'old rows pending').rows[0] === before.rows[0], 'OLD_ROWS_PERSIST_PENDING')
  button(panel(w), '新增模型价格').click(); await settle(); const modal = [...document.querySelectorAll('.ant-modal')].find(node => visible(node) && clean(node.querySelector('.ant-modal-title')?.textContent).endsWith('模型价格')); assert.ok(modal)
  const item = [...modal.querySelectorAll('.ant-form-item')].find(node => clean(node.querySelector('label')?.textContent) === '模型名称'), input = item.querySelector('input'); input.value = 'SYNTHETIC_PRICE_DRAFT'; input.dispatchEvent(new Event('input', { bubbles: true })); await settle()
  const draft = clone(toRaw(w.vm.priceForm)); await finish(read, http503()); const failed = snapshot(w, result, 'rows503 and drafts'); expect(result, failed.rows[0] === before.rows[0] && failed.failure.length === 1 && failed.empty.length === 0, 'OLD_ROWS_PERSIST_FAILURE'); expect(result, w.vm.priceForm === canonical && JSON.stringify(toRaw(w.vm.priceForm)) === JSON.stringify(draft) && w.vm.formState.model === 'SYNTHETIC_CONFIG_DRAFT' && w.vm.hasUnsavedChanges && w.vm.savedConfigSnapshot === saved && !w.vm.priceSaving, 'READ_FAILURE_CANNOT_TOUCH_PRICE_CONFIG_CANONICAL_DRAFT_ACK'); button(modal, '取消').click(); await settle(); const retry = await clickRead(w, '重试读取'); await finish(retry, ok([row('SYNTHETIC_RECOVERED_ROW')])); expect(result, w.vm.formState.model === 'SYNTHETIC_CONFIG_DRAFT' && w.vm.savedConfigSnapshot === saved && w.vm.hasUnsavedChanges, 'GET_RECOVERY_DOES_NOT_RELOAD_CONFIG'); expect(result, calls.filter(c => c.name === 'getAISearchConfig').length === observations.length, 'ONLY_ORIGINAL_CONFIG_GET_PER_MOUNT')
})
scenario('actor replacement retires rows failure and stale native retry without auto GET', async result => {
  const w = await open(); await finish(reads().at(-1), ok([row('SYNTHETIC_A_ROW')])); await finish(await clickRead(w), http503()); const staleRetry = panel(w).querySelector('[data-action="retry-request"]'), count = reads().length
  user.setStoredCurrentUser(B); await settle(); const retired = snapshot(w, result, 'A to B'); safe(result, retired, UNCONFIRMED, 'ACTOR_RETIRED'); expect(result, retired.rows.length === 0 && retired.failure.length === 0 && !retired.loading && reads().length === count, 'OLD_OWNER_NO_ROWS_FAILURE_BUSY_OR_AUTO_GET'); staleRetry?.click(); await settle(); expect(result, reads().length === count, 'RETIRED_RETRY_CANNOT_ISSUE'); const current = await clickRead(w); await finish(current, ok([])); empty(result, snapshot(w, result, 'B confirmed empty'))
})
scenario('late A failure and finally cannot overwrite B pending or its recovered rows', async result => {
  const w = await open(), old = reads().at(-1); user.setStoredCurrentUser(B); await settle(); const current = await clickRead(w); await finish(old, http503()); const waiting = snapshot(w, result, 'B pending after A failure'); safe(result, waiting, PENDING, 'LATE_A_ERROR'); expect(result, waiting.loading && waiting.failure.length === 0, 'OLD_FINALLY_CANNOT_UNLOCK_B'); await finish(current, ok([row('SYNTHETIC_B_ROW')])); const settled = snapshot(w, result, 'B rows'); expect(result, settled.rows[0]?.includes('SYNTHETIC_B_ROW') && settled.failure.length === 0, 'B_ONLY_ROWS')
})
for (const transition of ['role', 'status', 'logout', 'ABA']) scenario(transition + ' retirement blocks late old success error and retry', async result => {
  const w = await open(); await finish(reads().at(-1), ok([row('SYNTHETIC_A_ROW')])); const old = await clickRead(w), count = reads().length
  if (transition === 'role') user.setStoredCurrentUser({ ...A, role: 'user' }); else if (transition === 'status') user.setStoredCurrentUser({ ...A, status: 'disabled' }); else if (transition === 'logout') user.clearStoredCurrentUser(); else { user.setStoredCurrentUser(B); user.setStoredCurrentUser(A) }
  await settle(); safe(result, snapshot(w, result, 'retired before late response'), UNCONFIRMED, 'RETIRED'); await finish(old, ok([row('SYNTHETIC_LATE_A_ROW')])); const late = snapshot(w, result, 'retired late success'); safe(result, late, UNCONFIRMED, 'LATE_SUCCESS'); expect(result, late.rows.length === 0 && late.failure.length === 0 && !late.loading && reads().length === count, 'RETIRED_LANE_NO_LATE_PAINT_OR_AUTOMATIC_IO')
})
scenario('same owner metadata and concurrent reads retain original completion order', async result => {
  const w = await open(), first = reads().at(-1); user.setStoredCurrentUser({ ...A, balance: 9.876543 }); await settle(); expect(result, reads().length - result.readStart === 1 && w.vm.priceLoading, 'SAME_OWNER_METADATA_NO_RETIRE_OR_GET')
  // Supplemental handler contract: loading native control does not offer concurrent input.
  w.vm.loadAIModelPrices(); await settle(); const second = reads().at(-1); assert.notEqual(first, second); await finish(second, ok([row('SYNTHETIC_SECOND_COMPLETED_FIRST')])); await finish(first, ok([row('SYNTHETIC_FIRST_COMPLETED_LAST')])); const state = snapshot(w, result, 'original completion ordering'); expect(result, state.rows[0]?.includes('SYNTHETIC_FIRST_COMPLETED_LAST') && state.failure.length === 0 && !state.loading, 'SAME_OWNER_COMPLETION_ORDER_NOT_LATEST_WINS')
})
scenario('unmount retires read success failure finally and captured read method', async result => {
  const w = await open(), old = reads().at(-1), vm = w.vm, load = w.vm.loadAIModelPrices, count = reads().length; wrappers.at(-1).unmount(); wrappers.pop(); const stopped = { rows: clone(vm.priceRows), loading: vm.priceLoading, failure: vm.priceReadFailure }; await finish(old, http503()); expect(result, JSON.stringify(vm.priceRows) === JSON.stringify(stopped.rows) && vm.priceLoading === stopped.loading && vm.priceReadFailure === stopped.failure, 'DISPOSED_OWNER_NO_CALLBACK_PAINT'); load(); await settle(); expect(result, reads().length === count && !document.body.textContent.includes(PRIVATE), 'DISPOSED_CAPTURED_GET_CANNOT_ISSUE')
})
scenario('unknown initial cache retains original first GET with no locally invented logout', async result => {
  const w = await open(null), read = reads().at(-1); expect(result, reads().length - result.readStart === 1 && read.actor === null, 'UNKNOWN_CACHE_ORIGINAL_FIRST_GET_PRESERVED'); safe(result, snapshot(w, result, 'unknown cache GET pending'), PENDING, 'UNKNOWN_PENDING'); await finish(read, ok([])); empty(result, snapshot(w, result, 'server fixture confirmed unknown cache empty'))
})
scenario('unknown blocked storage does not retire known owner or leave its GET busy forever', async result => {
  const w = await open(), read = reads().at(-1), prototype = Object.getPrototypeOf(window.localStorage), original = prototype.getItem
  try {
    prototype.getItem = function (key) { if (key === 'adapter_current_user') throw new Error('SYNTHETIC_STORAGE_UNAVAILABLE'); return original.call(this, key) }
    await finish(read, ok([row('SYNTHETIC_KNOWN_OWNER_ROW')]))
    const state = snapshot(w, result, 'known owner with unavailable storage'); expect(result, state.rows[0]?.includes('SYNTHETIC_KNOWN_OWNER_ROW') && !state.loading && state.failure.length === 0 && reads().length - result.readStart === 1, 'UNKNOWN_STORAGE_NOT_LOGOUT_OR_STUCK_PENDING')
  } finally { prototype.getItem = original }
})

function visibleText(node) {
  const walker = document.createTreeWalker(node, window.NodeFilter.SHOW_TEXT), parts = []
  for (let text = walker.nextNode(); text; text = walker.nextNode()) if (visible(text.parentElement)) parts.push(text.nodeValue)
  return compact(parts.join(' '))
}
function pricingView(w, result, label) {
  const connection = w.element.querySelector('section[id="' + w.vm.sectionIds.connection + '"]'), tags = [...connection.querySelectorAll('.model-tags .ant-tag')].filter(visible)
  const state = { label, pricingKnown: w.vm.pricingKnown ?? null, topWarnings: [...w.element.querySelectorAll('.ant-alert')].filter(node => visible(node) && /当前默认模型还没有配置价格|模型列表中有模型还没有配置价格/.test(node.textContent)).map(node => compact(node.textContent)), text: visibleText(w.element), tags: tags.map(node => ({ text: compact(node.textContent), class: node.className })) }
  result.states.push(state); return state
}
function uncertainPrices(result, state, label) {
  expect(result, state.topWarnings.length === 0 && !/未定价|按 0 元计费|尚未配置价格|还没有配置价格|缺少价格/.test(state.text), label + '_NO_DEFINITE_UNPRICED_OR_ZERO_BILLING_CLAIM')
  expect(result, state.text.includes('价格待确认') && state.tags.length === 2 && state.tags.every(tag => tag.text.includes('价格待确认') && !tag.class.includes('ant-tag-orange') && !tag.class.includes('ant-tag-blue')), label + '_DEFAULT_AND_MANUAL_TAGS_NEUTRAL_PENDING_CONFIRMATION')
}
async function nativeManualModels(w, value) {
  const input = w.element.querySelector('textarea[id$="-manualModelsText"]'); assert.ok(input); input.value = value; input.dispatchEvent(new Event('input', { bubbles: true })); await settle()
}
scenario('price-dependent initial pending and 503 are neutral; confirmed empty restores original unpriced warnings', async result => {
  const w = await open(), read = reads().at(-1)
  await nativeManualModels(w, 'SYNTHETIC_MODEL,SYNTHETIC_MANUAL_UNPRICED')
  uncertainPrices(result, pricingView(w, result, 'initial configured and manual model while price GET pending'), 'INITIAL_PENDING')
  await finish(read, http503()); uncertainPrices(result, pricingView(w, result, 'configured and manual model after price GET503'), 'INITIAL503')
  await finish(await clickRead(w, '重试读取'), ok([])); const known = pricingView(w, result, 'HTTP200 empty restores original configured model and tags')
  expect(result, known.topWarnings.length === 1 && known.topWarnings[0].includes('当前默认模型还没有配置价格') && known.text.includes('按 0 元计费') && known.tags.every(tag => tag.text.includes('未定价') && tag.class.includes('ant-tag-orange')), 'CONFIRMED_EMPTY_RESTORES_ORIGINAL_CONFIGURED_UNPRICED_AND_TAGS')
  const model = w.element.querySelector('input[id$="-model"]'); model.value = ''; model.dispatchEvent(new Event('input', { bubbles: true })); await settle(); const manual = pricingView(w, result, 'confirmed empty manual-only warning')
  expect(result, manual.topWarnings.length === 1 && manual.topWarnings[0].includes('模型列表中有模型还没有配置价格'), 'CONFIRMED_MANUAL_ONLY_ORIGINAL_UNPRICED_WARNING')
})
scenario('price-dependent stale rows remain visible after refresh503 but never certify current pricing gaps', async result => {
  const w = await open(); await finish(reads().at(-1), ok([row('SYNTHETIC_EXISTING_PRICED_ROW')]))
  await nativeManualModels(w, 'SYNTHETIC_MODEL,SYNTHETIC_MANUAL_UNPRICED'); const known = pricingView(w, result, 'confirmed old rows with actual manual gap'), original = snapshot(w, result, 'confirmed old price row')
  expect(result, known.topWarnings.length === 1 && known.topWarnings[0].includes('模型列表中有模型还没有配置价格') && known.tags[0].class.includes('ant-tag-blue') && known.tags[1].class.includes('ant-tag-orange'), 'CONFIRMED_ROWS_ORIGINAL_MATCHED_AND_UNPRICED_POSITIVE')
  const refresh = await clickRead(w); uncertainPrices(result, pricingView(w, result, 'stale old rows refresh pending'), 'OLD_ROWS_PENDING'); await finish(refresh, http503()); uncertainPrices(result, pricingView(w, result, 'stale old rows after503'), 'OLD_ROWS503'); const failed = snapshot(w, result, 'old row retained without current pricing assertion')
  expect(result, failed.rows[0] === original.rows[0] && failed.failure.length === 1, 'OLD_ROW_BYTES_PRESERVED_WITH_SAFE_FAILURE')
  const currentRows = [row('SYNTHETIC_RECOVERED_PRICED_ROW'), { ...row('SYNTHETIC_RECOVERED_MANUAL_ROW'), id: 502, modelName: 'SYNTHETIC_MANUAL_UNPRICED' }]
  await finish(await clickRead(w, '重试读取'), ok(currentRows)); const recovered = pricingView(w, result, 'confirmed rows restore original matched tags')
  expect(result, recovered.topWarnings.length === 0 && recovered.tags.every(tag => !tag.text.includes('价格待确认') && !tag.text.includes('未定价') && tag.class.includes('ant-tag-blue')), 'CONFIRMED_RECOVERY_RESTORES_ORIGINAL_PRICED_MATCHING')
})

scenario('price-dependent malformed HTTP200 null and object never certify empty prices or zero billing', async result => {
  const w = await open(); await nativeManualModels(w, 'SYNTHETIC_MODEL,SYNTHETIC_MANUAL_UNPRICED'); await finish(reads().at(-1), ok([row('SYNTHETIC_VALID_OLD_ROW')]))
  const original = snapshot(w, result, 'valid array row before malformed responses')
  for (const [index, malformed] of [null, {}].entries()) {
    const read = await clickRead(w, index === 0 ? '刷新' : '重试读取'), response = ok(malformed); await finish(read, response)
    const state = snapshot(w, result, 'malformed HTTP200 ' + (malformed === null ? 'null' : 'object')); uncertainPrices(result, pricingView(w, result, 'malformed response pricing must stay unknown'), 'MALFORMED200_' + index)
    expect(result, state.rows[0] === original.rows[0] && state.failure.length === 1 && state.failureKind === 'unknown' && state.diagnostic.length === 0 && !state.confirmed && !state.loading && w.vm.priceReadFailure === response, 'MALFORMED200_PRESERVES_ROWS_SAFE_UNKNOWN_FAILURE_WITHOUT_FABRICATED_HTTP')
  }
  await finish(await clickRead(w, '重试读取'), ok([])); empty(result, snapshot(w, result, 'legal HTTP200 empty array recovery')); const recovered = pricingView(w, result, 'legal array confirms original unpriced warnings')
  expect(result, recovered.pricingKnown === true && recovered.topWarnings.length === 1 && recovered.topWarnings[0].includes('当前默认模型还没有配置价格') && recovered.tags.every(tag => tag.text.includes('未定价')), 'ONLY_LEGAL_ARRAY_RECOVERY_RESTORES_UNPRICED_HINTS')
})
