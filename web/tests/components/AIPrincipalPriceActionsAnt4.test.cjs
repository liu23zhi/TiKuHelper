/* Actual Vue / Antd4 / currentUser price mutation ownership.
 * All actions use native buttons/inputs except explicitly captured mounted Antd
 * vnode callbacks (a retained real confirmation capability, never a VM handler).
 * Synthetic deferred CRUD/GET only; no provider, DB, network, or clipboard.
 * TIKU_WEB_SOURCE selects one complete source tree unchanged across B/M/R.
 */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs'), path = require('node:path'), Module = require('node:module'), crypto = require('node:crypto')
const web = path.resolve(process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..'))
const sha = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
const targets = ['src/pages/AISearchConfig/index.vue', 'src/pages/AISearchConfig/AIModelPriceTable.vue', 'src/pages/AISearchConfig/AIModelPriceEditorModal.vue', 'src/utils/useUnsavedDraft.js', 'src/utils/currentUser.js'].map(file => path.join(web, file))
const initialHashes = Object.fromEntries(targets.map(file => [file, sha(file)]))
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, h, defineComponent, toRaw } = require('vue')
const Antd = require('ant-design-vue'), zhCN = require('ant-design-vue/lib/locale/zh_CN').default
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 78551, username: 'SYNTHETIC_PRICE_ACTOR_A', role: 'admin', status: 'enabled', balance: 1.234567 }
const B = { id: 78552, username: 'SYNTHETIC_PRICE_ACTOR_B', role: 'admin', status: 'enabled', balance: 2.345678 }
const CONFIG = { enabled: true, baseUrl: '', apiKey: '', model: 'MODEL-A', manualModels: ['MODEL-A'], prompt: 'SYNTHETIC', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0, maxAIWebReads: 5, webReadPricePerCall: 0, enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: true, stagedTokenMultiplierPoints: [{ tokens: 0, multiplier: 2 }, { tokens: 1000, multiplier: 1 }] }
const ROWS = [{ id: 501, modelName: 'MODEL-A', displayName: 'SYNTHETIC_PRICE_ROW_A', inputPrice: 0.000001, cachedPrice: 0, cacheWritePrice: 0.000062, outputPrice: 1.234567, multiplier: 1.2345, status: 'enabled', remark: 'SYNTHETIC_A', isUserModified: true }, { id: 502, modelName: 'MODEL-B', displayName: 'SYNTHETIC_PRICE_ROW_B', contextWindowTokens: 128000, inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0, outputPrice: 0, multiplier: 1, status: 'enabled', remark: 'SYNTHETIC_B' }]
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const ok = data => ({ status: 200, data })
const calls = [], notices = [], wrappers = [], observations = [], forbidden = [], io = [], runtimeErrors = [], warnings = []
const reads = () => calls.filter(c => c.name === 'getAIModelPrices')
const writes = () => calls.filter(c => /^(create|update|delete|reset)AIModelPrice/.test(c.name))
function deny(name) { forbidden.push(name); throw new Error('FORBIDDEN_PRICE_ACTION_' + name) }
function denyIO(name) { io.push(name); throw new Error('FORBIDDEN_PRICE_IO_' + name) }
function deferred(name, args) {
  let yes, no
  const promise = new Promise((resolve, reject) => { yes = resolve; no = reject })
  const call = { name, actor: user.getStoredCurrentUser()?.id ?? null, payload: clone(args), completed: false, synthetic: true }
  call.resolve = value => { call.completed = true; call.outcome = value?.status === 200 ? 'HTTP200' : 'HTTP' + String(value?.status); yes(value) }
  call.reject = value => { call.completed = true; call.outcome = 'REJECT'; no(value) }
  calls.push(call); return promise
}
// Exactly the new readonly OCR status GET, never clear/write/provider.
let ocrStatusReads = 0
function syntheticOCRStatus(feedback) { assert.deepEqual(feedback, { errorFeedback: 'local' }); ocrStatusReads += 1; return Promise.resolve({ status: 200, data: { configured: false, entries: 0 } }) }
require('node:test').after(() => console.log('AI_OCR_STATUS_READS ' + JSON.stringify({ fixture: __filename, method: 'GET', count: ocrStatusReads, configured: false, entries: 0 })))
const methods = { getAIOCRCacheStatus: syntheticOCRStatus, getAISearchConfig: (...args) => { calls.push({ name: 'getAISearchConfig', actor: user.getStoredCurrentUser()?.id ?? null, payload: clone(args), completed: true, synthetic: true }); return Promise.resolve(ok(clone(CONFIG))) } }
for (const name of ['getAIModelPrices', 'createAIModelPrice', 'updateAIModelPrice', 'deleteAIModelPrice', 'resetAIModelPrices']) methods[name] = (...args) => deferred(name, args)
const api = new Proxy(methods, { get(value, name) { if (name === '__esModule' || name === 'then') return undefined; return name in value ? value[name] : () => deny('API_' + String(name)) } })
const originalLoad = Module._load
Module._load = function (request, parent, main) {
  let filename
  try { filename = Module._resolveFilename(request, parent, main) } catch { return originalLoad.call(this, request, parent, main) }
  if (typeof filename === 'string' && filename.startsWith(path.join(web, 'src/api') + path.sep)) {
    if (path.basename(filename) === 'TikuComponentApi.js') return api
    return new Proxy({}, { get(_, name) { if (name === '__esModule' || name === 'then') return undefined; return () => deny('OTHER_API_' + String(name)) } })
  }
  return originalLoad.call(this, request, parent, main)
}
for (const level of ['success', 'warning', 'error']) {
  const original = Antd.message[level].bind(Antd.message)
  Antd.message[level] = (...args) => { notices.push({ level, content: String(args[0]?.content || args[0] || ''), actor: user.getStoredCurrentUser()?.id ?? null }); return original(...args) }
}
window.XMLHttpRequest.prototype.open = () => denyIO('XHR')
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: () => deny('CLIPBOARD') } })
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
for (const name of ['node:net', 'node:tls']) { const transport = require(name); transport.connect = () => denyIO(name); if ('createConnection' in transport) transport.createConnection = () => denyIO(name) }
const Page = require(targets[0])
const hosts = new WeakMap()
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
const clean = value => compact(value).replace(/\s/g, '')
function visible(node) {
  if (!node?.isConnected) return false
  for (let p = node; p && p.nodeType === 1; p = p.parentElement) {
    const style = window.getComputedStyle(p)
    if (p.hidden || p.getAttribute('aria-hidden') === 'true' || style.display === 'none' || ['hidden', 'collapse'].includes(style.visibility) || style.opacity === '0') return false
  }
  return true
}
function button(root, label) {
  const matches = [...root.querySelectorAll('button')].filter(node => visible(node) && clean(node.textContent) === clean(label))
  assert.equal(matches.length, 1, 'ONE_REAL_NATIVE_BUTTON_' + label)
  assert.equal(matches[0].disabled, false, 'NATIVE_BUTTON_ENABLED_' + label)
  return matches[0]
}
const panel = w => w.element.querySelector('.pricing-grid')
const modalNodes = () => [...document.querySelectorAll('.ant-modal')].filter(node => visible(node) && /模型价格$/.test(clean(node.querySelector('.ant-modal-title')?.textContent)))
function modal() { const found = modalNodes(); assert.equal(found.length, 1, 'ONE_VISIBLE_REAL_ANTD_PRICE_MODAL'); return found[0] }
function field(root, label) { const item = [...root.querySelectorAll('.ant-form-item')].find(node => clean(node.querySelector('label')?.textContent) === clean(label)); assert.ok(item?.querySelector('input'), 'REAL_NATIVE_FIELD_' + label); return item.querySelector('input') }
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
async function input(root, label, value) { const node = field(root, label); node.value = String(value); for (const name of ['input', 'change', 'blur']) node.dispatchEvent(new Event(name, { bubbles: true })); await settle() }
const draft = w => clone(toRaw(w.vm.priceForm))
function check(result, value, label) { result.checks.push({ label, passed: Boolean(value) }) }
function snapshot(result, w, label) {
  const state = { label, actor: user.getStoredCurrentUser()?.id ?? null, modal: w.vm.priceModalVisible, renderedModals: modalNodes().length, busy: w.vm.priceSaving, resetBusy: w.vm.priceResetting, draft: draft(w), rows: clone(toRaw(w.vm.priceRows)), reads: reads().length, writes: writes().length, notices: clone(notices) }
  result.states.push(state); return state
}
async function start() {
  const ocrReadStart = ocrStatusReads
  user.setStoredCurrentUser(A)
  const Host = defineComponent({ render: () => h(Antd.ConfigProvider, { locale: zhCN, autoInsertSpaceInButton: false }, () => h(Page)) })
  const host = mount(Host, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => runtimeErrors.push(String(error.stack || error)), warnHandler: warning => warnings.push(String(warning)) } } })
  wrappers.push(host); await settle(); const w = host.findComponent(Page); hosts.set(w, host)
  assert.equal(reads().at(-1)?.name, 'getAIModelPrices'); reads().at(-1).resolve(ok(clone(ROWS))); await settle(); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return w
}
async function openEditor(w, action = 'update', row = 0) {
  if (action === 'create') button(panel(w), '新增模型价格').click()
  else button(w.element.querySelector('tr[data-row-key="' + ROWS[row].id + '"]'), '编辑').click()
  await settle(); return modal()
}
async function cancelEditor(w) { if (modalNodes().length) { button(modal(), '取消').click(); await settle() } assert.equal(w.vm.priceModalVisible, false, 'NATIVE_CANCEL_PRICE_EDITOR') }
async function save() { const count = writes().length; button(modal(), '确定').click(); await settle(); assert.equal(writes().length, count + 1, 'REAL_NATIVE_MODAL_SUBMIT_ONE_SYNTHETIC_WRITE'); return writes().at(-1) }
async function finish(call, outcome) {
  assert.ok(call, 'CAPTURED_ISSUED_SYNTHETIC_CALL')
  if (outcome === 'reject') call.reject(new Error('SYNTHETIC_PRICE_REJECT'))
  else call.resolve(outcome === 'success' ? ok({}) : { status: 409, message: 'SYNTHETIC_PRICE_FAILURE' })
  await settle()
}
async function currentRead(w) {
  const count = reads().length; button(panel(w), '刷新').click(); await settle()
  assert.equal(reads().length, count + 1, 'CURRENT_OWNER_EXPLICIT_NATIVE_PRICE_READ')
  reads().at(-1).resolve(ok(clone(ROWS))); await settle()
}
async function transition(w, kind) {
  if (kind === 'unmount') { const host = hosts.get(w); assert.ok(host, 'RECORDED_NATIVE_HOST'); host.unmount(); wrappers.splice(wrappers.indexOf(host), 1) }
  else if (kind === 'B') user.setStoredCurrentUser(B)
  else if (kind === 'ABA') { user.setStoredCurrentUser(B); user.setStoredCurrentUser(A) }
  else if (kind === 'role') user.setStoredCurrentUser({ ...A, role: 'user' })
  else if (kind === 'status') user.setStoredCurrentUser({ ...A, status: 'disabled' })
  else user.clearStoredCurrentUser()
  await settle()
}
async function newOwnerEditor(w, result) {
  await cancelEditor(w)
  user.setStoredCurrentUser(B); await settle(); await currentRead(w)
  await openEditor(w, 'update', 1); await input(modal(), '显示名称', 'SYNTHETIC_B_NEW_DRAFT')
  check(result, field(modal(), '模型名称').value === 'MODEL-B' && field(modal(), '上下文窗口').value === '128000', 'B_NATURALLY_EDITED_NEW_READ_ROW_NOT_REUSED_A_DRAFT')
}
function capturedModalOK(w) {
  const component = w.findAllComponents(Antd.Modal).find(item => item.props('open') && /模型价格$/.test(clean(item.props('title'))))
  const invoke = component?.vm.$.vnode.props.onOk
  assert.equal(typeof invoke, 'function', 'REAL_MOUNTED_ANTD_MODAL_ONOK_CAPTURE')
  return invoke
}
async function confirmation(w, action) {
  const title = action === 'delete' ? '确定删除该模型价格吗？' : '确定恢复内置默认价格表吗？自定义价格会被清空。'
  const component = w.findAllComponents(Antd.Popconfirm).find(item => item.props('title') === title)
  assert.ok(component, 'REAL_MOUNTED_ANTD_POPCONFIRM_' + action)
  const invoke = component.vm.$.vnode.props.onConfirm
  assert.equal(typeof invoke, 'function', 'CAPTURED_REAL_POPCONFIRM_ONCONFIRM_' + action)
  if (action === 'delete') button(w.element.querySelector('tr[data-row-key="501"]'), '删除').click()
  else button(panel(w), '恢复默认').click()
  await settle(); await new Promise(resolve => setTimeout(resolve, 90)); await settle()
  // Antd Trigger retains opacity:0 in jsdom because it has no alignment geometry.
  // Logical open state and actual untouched HTMLElement are evidence, not geometry.
  const popup = [...document.querySelectorAll('.ant-popover')].find(node => node.isConnected && !node.classList.contains('ant-popover-hidden') && clean(node.textContent).includes(clean(title)))
  assert.ok(popup, 'OPEN_REAL_NATIVE_POPCONFIRM_' + action + ' ' + JSON.stringify([...document.querySelectorAll('.ant-popover')].map(node => ({ text: compact(node.textContent), class: node.className, style: node.getAttribute('style'), visible: visible(node), html: node.outerHTML.slice(0, 1200) }))))
  const elements = [...popup.querySelectorAll('button')].filter(node => clean(node.textContent) === '确定' && !node.disabled)
  assert.equal(elements.length, 1, 'ONE_ACTUAL_LOGICALLY_OPEN_POPCONFIRM_CONFIRM_BUTTON')
  return { invoke, element: elements[0] }
}
function quiet(result, startNotices, startReads, label) {
  check(result, notices.length === startNotices, label + '_NO_STALE_TOAST')
  check(result, reads().length === startReads, label + '_NO_STALE_FOLLOWUP_PRICE_GET')
}
function scenario(label, run) {
  test('AI_PRINCIPAL_PRICE ' + label, async () => {
    const result = { label, checks: [], states: [], accepted: false, startCall: calls.length, startNotice: notices.length }; observations.push(result)
    try {
      await run(result)
      result.failures = result.checks.filter(item => !item.passed).map(item => item.label)
      console.log('AI_PRINCIPAL_PRICE_NATIVE ' + JSON.stringify({ label, checks: result.checks.length, failures: result.failures }))
      assert.deepEqual(result.failures, []); result.accepted = true
    } catch (error) { result.error = { name: error.name, message: error.message }; throw error }
  })
}
function record() {
  if (!process.env.AI_PRINCIPAL_PRICE_OBSERVATION_FILE) return
  fs.writeFileSync(process.env.AI_PRINCIPAL_PRICE_OBSERVATION_FILE, JSON.stringify({ fixture: __filename, fixtureSHA256: sha(__filename), web, targets, initialHashes, finalHashes: Object.fromEntries(targets.map(file => [file, sha(file)])), node: process.version, observations, calls: calls.map(call => { const value = { ...call }; delete value.resolve; delete value.reject; return value }), notices, forbidden, io, runtimeErrors, warnings, qualification: 'Actual Vue/Antd4/zhCN/currentUser; real DOM actions with synthetic deferred price CRUD/GET. Explicitly labelled retained mounted Antd callbacks are not VM methods. jsdom is not real browser geometry, server authorization, DB, or provider acceptance.' }, null, 2) + '\n')
}
afterEach(async () => {
  for (const w of wrappers.splice(0)) if (w.exists()) w.unmount()
  for (const call of calls) if (!call.completed && call.resolve) call.resolve({ status: 503 })
  await settle(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); user.clearStoredCurrentUser(); window.localStorage.clear(); document.body.replaceChildren(); record()
  assert.deepEqual(Object.fromEntries(targets.map(file => [file, sha(file)])), initialHashes, 'SOURCE_BYTES_UNCHANGED')
  assert.deepEqual(forbidden, []); assert.deepEqual(io, []); assert.deepEqual(runtimeErrors, [])
})
after(() => { record(); Module._load = originalLoad })

for (const action of ['create', 'update']) for (const context of ['omitted', 'zero', 'explicit']) scenario('current ' + action + ' exact canonical payload ' + context, async result => {
  const w = await start(), canonical = w.vm.priceForm
  const rowText = compact(w.element.querySelector('tr[data-row-key="501"]').textContent)
  check(result, rowText.includes('￥0.000001') && rowText.includes('￥0.000062') && rowText.includes('￥1.234567') && rowText.includes('×1.2345'), 'SIX_PLACE_PRICE_FOUR_PLACE_MULTIPLIER_RENDER')
  await openEditor(w, action)
  await input(modal(), '模型名称', '  MODEL-EXACT  '); await input(modal(), '显示名称', '  Exact  ')
  await input(modal(), '输入单价', 0); await input(modal(), '缓存输入单价', 0); await input(modal(), '缓存写入单价', 0.000062); await input(modal(), '输出单价', 0); await input(modal(), '倍率', 1.2345); await input(modal(), '备注', 'SYNTHETIC_EXACT')
  if (context !== 'omitted') await input(modal(), '上下文窗口', context === 'zero' ? '0' : '128000')
  const before = reads().length, n = notices.length, call = await save()
  const payload = { id: action === 'create' ? null : 501, modelName: 'MODEL-EXACT', displayName: 'Exact', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0.000062, outputPrice: 0, multiplier: 1.2345, remark: 'SYNTHETIC_EXACT' }
  if (action === 'create' || context !== 'omitted') payload.contextWindowTokens = context === 'explicit' ? 128000 : 0
  check(result, JSON.stringify(call.payload) === JSON.stringify([payload]) || assertPayload(call.payload, payload), 'EXACT_CANONICAL_PRICE_PAYLOAD_ZERO_OPTIONAL_CONTEXT')
  check(result, w.vm.priceForm === canonical && w.vm.priceSaving && button(modal(), '确定').classList.contains('ant-btn-loading'), 'CANONICAL_IDENTITY_REAL_CURRENT_BUSY')
  await finish(call, 'success')
  check(result, !w.vm.priceModalVisible && !w.vm.priceSaving && w.vm.priceForm === canonical && w.vm.priceForm.modelName === '' && w.vm.priceForm.cacheWritePrice === 0 && w.vm.priceForm.multiplier === 1, 'CURRENT_SUCCESS_CLOSE_RESET_SAME_CANONICAL')
  check(result, reads().length === before + 1 && notices.length === n + 1 && notices.at(-1).content === '保存成功', 'CURRENT_SUCCESS_ONE_TOAST_ONE_GET')
  snapshot(result, w, 'current success')
})
function assertPayload(args, payload) { try { assert.deepEqual(args, [payload]); return true } catch { return false } }

for (const outcome of ['failure', 'reject']) scenario('current update ' + outcome + ' keeps native draft', async result => {
  const w = await start(); await openEditor(w); await input(modal(), '备注', 'SYNTHETIC_CURRENT_RETRY')
  const before = draft(w), count = reads().length, n = notices.length, call = await save(); await finish(call, outcome)
  check(result, w.vm.priceModalVisible && !w.vm.priceSaving && assertDraft(w, before), 'CURRENT_FAILURE_PRESERVES_DRAFT_AND_UNLOCKS')
  check(result, reads().length === count && notices.length === n + 1 && notices.at(-1).level === 'error', 'CURRENT_FAILURE_NO_REFETCH_ONE_ERROR')
  snapshot(result, w, 'current failed draft')
})
function assertDraft(w, expected) { return JSON.stringify(draft(w)) === JSON.stringify(expected) }

for (const outcome of ['success', 'failure', 'reject']) scenario('same-owner native close reopen and new busy survives old ' + outcome, async result => {
  const w = await start(); await openEditor(w); const old = await save()
  const cancel = button(modal(), '取消'); cancel.click()
  check(result, !w.vm.priceSaving, 'NATIVE_CANCEL_SYNCHRONOUSLY_RETIRES_OLD_BUSY'); await settle()
  await openEditor(w, 'update', 1); await input(modal(), '备注', 'SYNTHETIC_SAME_OWNER_NEW_DRAFT')
  const before = draft(w), count = reads().length, n = notices.length, fresh = await save()
  await finish(old, outcome)
  check(result, w.vm.priceModalVisible && w.vm.priceSaving && assertDraft(w, before) && button(modal(), '确定').classList.contains('ant-btn-loading'), 'REOPENED_CURRENT_DRAFT_MODAL_AND_BUSY_UNCHANGED')
  quiet(result, n, count, 'SAMEOWNER_OLD_COMPLETION'); await finish(fresh, 'success')
  check(result, !w.vm.priceModalVisible && !w.vm.priceSaving && reads().length === count + 1 && notices.length === n + 1, 'SAMEOWNER_NEW_REQUEST_NATURALLY_COMPLETES')
  snapshot(result, w, 'sameowner completion')
})

for (const kind of ['B', 'ABA', 'role', 'status', 'logout', 'unmount']) for (const action of ['create', 'update']) for (const outcome of ['success', 'failure', 'reject']) scenario(kind + ' retires ' + action + ' pending then old ' + outcome, async result => {
  const w = await start(); await openEditor(w, action); if (action === 'create') await input(modal(), '模型名称', 'SYNTHETIC_CREATED_A')
  await input(modal(), '显示名称', 'SYNTHETIC_A_PRIVATE_PRICE_DRAFT')
  const capturedOK = capturedModalOK(w), oldSubmit = button(modal(), '确定'), old = await save(), count = reads().length
  await transition(w, kind); const retired = snapshot(result, w, kind + ' retired')
  check(result, !retired.modal && !retired.busy && retired.draft.modelName === '' && retired.draft.displayName === '' && retired.renderedModals === 0, 'ACTOR_DISPOSE_RETIRES_PRIVATE_PRICE_DRAFT_MODAL_BUSY')
  check(result, reads().length === count, 'RETIREMENT_DOES_NOT_AUTO_READ')
  const writeCount = writes().length, n = notices.length
  oldSubmit.click(); capturedOK(new MouseEvent('click', { bubbles: true })); await settle()
  check(result, writes().length === writeCount, 'RETIRED_REAL_MODAL_HTML_AND_CAPTURED_ONOK_CANNOT_DISPATCH')
  quiet(result, n, count, 'RETIRED_MODAL_CAPABILITY')
  if (kind === 'unmount') {
    const before = draft(w), busy = w.vm.priceSaving, shown = w.vm.priceModalVisible
    await finish(old, outcome)
    check(result, assertDraft(w, before) && w.vm.priceSaving === busy && w.vm.priceModalVisible === shown, 'UNMOUNTED_COMPLETION_PUBLISHES_NOTHING')
    quiet(result, n, count, 'UNMOUNTED_OLD_COMPLETION'); return
  }
  await newOwnerEditor(w, result)
  const before = draft(w), beforeReads = reads().length, beforeNotices = notices.length, fresh = await save()
  check(result, fresh.actor === B.id && w.vm.priceSaving, 'B_CURRENT_NATIVE_SAVE_ISSUED_WITH_OWN_BUSY')
  await finish(old, outcome)
  check(result, w.vm.priceModalVisible && w.vm.priceSaving && assertDraft(w, before) && button(modal(), '确定').classList.contains('ant-btn-loading'), 'OLD_COMPLETION_CANNOT_CLOSE_CLEAR_OR_UNLOCK_B')
  quiet(result, beforeNotices, beforeReads, 'OLD_' + kind + '_' + outcome); snapshot(result, w, 'B untouched by old completion')
  await finish(fresh, 'success')
  check(result, !w.vm.priceModalVisible && !w.vm.priceSaving && reads().length === beforeReads + 1 && notices.length === beforeNotices + 1 && notices.at(-1).actor === B.id, 'B_NEW_REQUEST_NATURALLY_COMPLETES_ONE_TOAST_GET')
})

for (const action of ['delete', 'reset']) for (const kind of ['B', 'ABA', 'role', 'status', 'logout', 'unmount']) scenario('opened ' + action + ' old native confirmation ' + kind, async result => {
  const w = await start(), retained = await confirmation(w, action), before = writes().length, count = reads().length, n = notices.length
  await transition(w, kind); retained.element.click(); retained.invoke(new MouseEvent('click', { bubbles: true })); await settle()
  check(result, writes().length === before, 'RETIRED_OPEN_CONFIRM_HTML_AND_MOUNTED_CALLBACK_CANNOT_DISPATCH_' + action.toUpperCase())
  quiet(result, n, count, 'OLD_CONFIRM_' + kind); snapshot(result, w, 'retired confirmation')
})

for (const action of ['delete', 'reset']) for (const kind of ['B', 'ABA', 'unmount']) for (const outcome of ['success', 'failure', 'reject']) scenario('issued ' + action + ' ' + kind + ' late ' + outcome, async result => {
  const w = await start(), confirmationA = await confirmation(w, action), before = writes().length
  confirmationA.element.click(); await settle(); assert.equal(writes().length, before + 1, 'CURRENT_NATIVE_CONFIRM_ISSUES_ONE_' + action)
  const old = writes().at(-1), count = reads().length, n = notices.length
  check(result, old.actor === A.id && (action !== 'delete' || assertPayload(old.payload, { id: 501 })), 'ORIGINAL_OWNED_CONFIRM_PAYLOAD')
  await transition(w, kind)
  if (kind === 'unmount') {
    const busy = w.vm.priceResetting; await finish(old, outcome)
    check(result, w.vm.priceResetting === busy, 'OLD_UNMOUNTED_RESET_FINALLY_DOES_NOT_PUBLISH'); quiet(result, n, count, 'UNMOUNTED_' + action); return
  }
  user.setStoredCurrentUser(B); await settle(); await currentRead(w)
  const confirmationB = await confirmation(w, 'reset'); confirmationB.element.click(); await settle(); const fresh = writes().at(-1)
  assert.equal(fresh.name, 'resetAIModelPrices'); assert.equal(fresh.actor, B.id)
  const beforeReads = reads().length, beforeNotices = notices.length
  check(result, w.vm.priceResetting, 'B_NATIVE_RESET_BUSY')
  await finish(old, outcome)
  check(result, w.vm.priceResetting && button(panel(w), '恢复默认').classList.contains('ant-btn-loading'), 'OLD_MUTATION_CANNOT_CLEAR_B_RESET_BUSY')
  quiet(result, beforeNotices, beforeReads, 'ISSUED_OLD_' + action); snapshot(result, w, 'B reset protected')
  await finish(fresh, 'success')
  check(result, !w.vm.priceResetting && reads().length === beforeReads + 1 && notices.length === beforeNotices + 1 && notices.at(-1).content === '已恢复内置默认价格表', 'B_CURRENT_RESET_COMPLETES_ONE_TOAST_GET')
})
