/* Actual Vue/Antd/router native DOM. Only synthetic configuration/price GETs;
 * local edits/cancel only: no save/CRUD/test/provider/database/real network.
 * One unchanged fixture is applied to pristine, modified and rollback source.
 * jsdom label association/focus is not reader/contrast/zoom/layout certification.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'HTMLVideoElement', 'HTMLAudioElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, defineComponent, h } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const ACTOR = { id: 77931, username: 'SYNTHETIC_AI_ACCESSIBILITY_ADMIN', role: 'admin', balance: 9.876543 }
const CONFIG = {
  enabled: true, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC_NOT_REAL_AI_KEY', model: 'MODEL-ZERO', backupModel: 'MODEL-BACKUP', manualModels: ['MODEL-ZERO'],
  prompt: '只返回 JSON；合成 Prompt，不调用模型。', remark: 'SYNTHETIC_CONFIG_REMARK', temperature: 0, topP: 0,
  maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0.000062, tiku_search_price_per_call: 0.000061, maxAIWebReads: 5, webReadPricePerCall: 1.234567, imageOCRPricePerCall: 0,
  contextCompressionEnabled: true, useRemoteContextCompression: false, contextCompressionTriggerPercent: 80, contextCompressionTargetPercent: 50, contextCompressionMaxAttempts: 3,
  enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: true, stagedTokenMultiplierPoints: [{ tokens: 0, multiplier: 2 }, { tokens: 1000, multiplier: 1 }],
}
const PRICES = [{ id: 501, modelName: 'MODEL-ZERO', displayName: 'Zero fixture', contextWindowTokens: 128000, inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0.000062, outputPrice: 1.234567, multiplier: 1.2345, status: 'enabled', remark: 'SYNTHETIC_PRICE_REMARK', isUserModified: true }]
const LABELS = {
  connection: ['是否启用', '服务地址', 'API Key', '默认模型', '备用模型', '模型列表'],
  advanced: ['模型最大调用次数', '最大搜索次数', '每轮重试次数', '联网搜索单价（元/次）', '本地题库检索单价（元/次）', '网页读取次数', '网页读取单价（元/次）', '图片 OCR 单价（元/次）', '自动压缩上下文', '压缩执行顺序', '压缩触发比例', '压缩目标比例', '压缩最大尝试次数'],
  prompt: ['Prompt 设置', '温度', 'Top P'],
  stage: ['启用阶段计费', '平滑变化倍率'],
  price: ['模型名称', '显示名称', '上下文窗口', '输入单价', '缓存输入单价', '缓存写入单价', '输出单价', '倍率', '备注'],
}
const wrappers = [], routers = [], reads = [], mutations = [], network = [], runtimeErrors = []
const clone = value => JSON.parse(JSON.stringify(value)), compact = value => String(value || '').replace(/\s+/g, ' ').trim()
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_AI_FORM_IO_' + kind) }
function denyMutation(name) { mutations.push(name); throw new Error('FORBIDDEN_AI_FORM_ACTION_' + name) }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
// Upstream parent mounts with one readonly OCR status GET; never clear/write/provider.
let ocrStatusReads = 0
function syntheticOCRStatus(feedback) {
  assert.deepEqual(feedback, { errorFeedback: 'local' }, 'exact readonly OCR feedback contract')
  ocrStatusReads += 1
  return Promise.resolve({ status: 200, data: { configured: false, entries: 0 } })
}
require('node:test').after(() => console.log('AI_OCR_STATUS_READS ' + JSON.stringify({ fixture: __filename, method: 'GET', count: ocrStatusReads, configured: false, entries: 0 })))
api.exports = new Proxy({ getAIOCRCacheStatus: syntheticOCRStatus,
  getAISearchConfig: () => { reads.push({ name: 'getAISearchConfig', actor: user.getStoredCurrentUser()?.id }); return Promise.resolve({ status: 200, data: clone(CONFIG) }) },
  getAIModelPrices: () => { reads.push({ name: 'getAIModelPrices', actor: user.getStoredCurrentUser()?.id }); return Promise.resolve({ status: 200, data: clone(PRICES) }) },
}, { get(target, name) { if (Object.hasOwn(target, name)) return target[name]; if (name === '__esModule') return false; return () => denyMutation(String(name)) } })
require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return denyIO('XHR') }
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: () => denyIO('CLIPBOARD') } })
document.execCommand = () => denyIO('EXEC_COMMAND_COPY')
window.addEventListener('error', event => runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtimeErrors.push(String(event.reason?.stack || event.reason)))
const Page = require(path.join(web, 'src/pages/AISearchConfig/index.vue'))
const selector = 'input:not([type="hidden"]),textarea,select,button[role="switch"]'
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function until(check, label) {
  const start = process.hrtime.bigint()
  while (Number(process.hrtime.bigint() - start) / 1e6 < 1200) {
    const value = check(); if (value) return value
    await new Promise(resolve => setTimeout(resolve, 5)); await settle()
  }
  assert.fail(label + ' monotonicElapsedMs=' + Number(process.hrtime.bigint() - start) / 1e6)
}
function visible(node) {
  if (!node?.isConnected) return false
  for (let item = node; item && item.nodeType === 1; item = item.parentElement) {
    const style = window.getComputedStyle(item)
    if (item.hidden || item.inert || item.getAttribute('aria-hidden') === 'true' || style.display === 'none' || style.visibility === 'hidden') return false
  }
  return true
}
function labels(root) { return [...root.querySelectorAll('.ant-form-item-label > label')].filter(visible).map(label => compact(label.textContent)) }
function field(root, text) {
  const matches = [...root.querySelectorAll('.ant-form-item-label > label')].filter(label => compact(label.textContent) === text && visible(label))
  assert.equal(matches.length, 1, 'one original visible label: ' + text)
  const label = matches[0], item = label.closest('.ant-form-item')
  if (text === '压缩执行顺序') {
    const control = label.control, group = item.querySelector('.compression-order-group[role="group"]')
    assert.ok(control instanceof HTMLButtonElement && group?.contains(control) && visible(control), 'compound field has an actual native labelable focus entry')
    assert.equal(group.querySelectorAll('button').length, 9, 'three handles plus six actual reorder buttons are retained')
    const title = document.getElementById(group.getAttribute('aria-labelledby'))
    assert.ok(title && label.contains(title) && visible(title), 'group names the exact visible field-label span')
    return { label, control, group }
  }
  const controls = [...item.querySelectorAll(selector)].filter(control => control.closest('.ant-form-item') === item && visible(control))
  assert.equal(controls.length, 1, 'one actual native control: ' + text)
  return { label, control: controls[0] }
}
function associated(root, text) {
  const result = field(root, text), { label, control } = result
  const refs = String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean)
  const native = label.control === control && [...(control.labels || [])].includes(label)
  const aria = refs.some(id => document.getElementById(id) === label && visible(label))
  assert.ok(native || aria, 'exact visible label associates its own control, not name/placeholder/title/adjacency: ' + text)
  assert.ok(control.id, 'stable actual control ID: ' + text)
  if (label.htmlFor) { assert.equal(label.htmlFor, control.id); assert.equal(document.getElementById(label.htmlFor), control) }
  return result
}
function region(w, name) { return name === 'stage' ? w.element.querySelector('.staged-billing-settings') : w.element.querySelector('#' + w.vm.sectionIds[name]) }
function ids(root, name) { return Object.fromEntries(LABELS[name].map(text => [text, associated(root, text).control.id])) }
function type(control, value) { control.value = value; control.dispatchEvent(new Event('input', { bubbles: true })); control.dispatchEvent(new Event('change', { bubbles: true })) }
function button(root, text) {
  assert.ok(['编辑', '取消'].includes(text), 'only local editor open/cancel permitted')
  const matches = [...root.querySelectorAll('button')].filter(node => visible(node) && compact(node.textContent).replace(/\s/g, '') === text)
  assert.equal(matches.length, 1, 'one native local button: ' + text); return matches[0]
}
function dialogs() { return [...document.querySelectorAll('.ant-modal')].filter(node => visible(node) && compact(node.querySelector('.ant-modal-title')?.textContent) === '编辑模型价格') }
async function open(w) {
  const previous = new Set(dialogs()); button(w.element.querySelector('.price-table-wrap tr[data-row-key="501"]'), '编辑').click(); await settle()
  return until(() => dialogs().find(node => !previous.has(node)), 'native local price editor opens')
}
async function cancel(w, modal) { button(modal.querySelector('.ant-modal-footer'), '取消').click(); await until(() => !w.vm.priceModalVisible && !visible(modal), 'native cancel closes own price editor'); await settle() }
function noDuplicateIds() {
  const values = [...document.querySelectorAll('[id]')].map(node => node.id).filter(Boolean)
  assert.equal(new Set(values).size, values.length, 'same app/teleported native DOM has no duplicate IDs')
  for (const value of values) for (const fixture of [String(ACTOR.id), ACTOR.username, CONFIG.apiKey, CONFIG.model, PRICES[0].remark]) assert.ok(!value.includes(fixture), 'IDs are UI-only, not fixture/secret metadata')
}
async function start(count = 1) {
  const ocrReadStart = ocrStatusReads
  user.setStoredCurrentUser(ACTOR)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/ai-search-config', component: { render: () => null } }] })
  routers.push(router); await router.push('/adapter/ai-search-config'); await router.isReady()
  const Root = count === 1 ? Page : defineComponent({ render: () => h('main', null, [h(Page, { key: 'first' }), h(Page, { key: 'second' })]) })
  const root = mount(Root, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  wrappers.push(root); await settle(); const pages = count === 1 ? [root] : root.findAllComponents(Page)
  await until(() => pages.every(w => !w.vm.tabLoading && !w.vm.priceLoading && w.vm.priceRows.length === 1), 'synthetic config/price GETs render')
  assert.equal(pages.length, count); assert.equal(reads.length, count * 2)
  for (const read of reads) assert.ok(['getAISearchConfig', 'getAIModelPrices'].includes(read.name) && read.actor === ACTOR.id)
  assert.equal(ocrStatusReads, ocrReadStart + count, 'one initial readonly OCR status GET per parent instance')
  return pages
}
afterEach(async () => {
  for (const w of wrappers.splice(0)) if (w.exists()) w.unmount()
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
  for (const router of routers.splice(0)) router.options.history.destroy()
  user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('AI_FORM_NATIVE_SAFETY ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', reads, mutations, network, runtimeErrors }))
  assert.deepEqual(mutations, [], 'no save/CRUD/test-connection/provider action'); assert.deepEqual(network, [], 'no real IO'); assert.deepEqual(runtimeErrors, [], 'no swallowed runtime failures')
  reads.length = mutations.length = network.length = runtimeErrors.length = 0
})

test('positive: original30 plus3 upstream labels values secret masking readonly zero six-place price four-place multiplier and canonical forms', async () => {
  const [w] = await start(), canonical = w.vm.formState, initial = clone(canonical), canonicalPrice = w.vm.priceForm
  const compression = require(path.join(web, 'src/utils/aiSearchConfigHelpers.mjs')).buildAIContextCompressionSettings(canonical)
  const payload = { ...require(path.join(web, 'src/utils/aiSearchModelHelpers.mjs')).buildAISearchConfigSavePayload(canonical, canonical.baseUrl), ...compression }
  assert.equal(payload.useRemoteContextCompression, CONFIG.useRemoteContextCompression, 'legacy remote bool remains compatible in the real payload builders, not a removed UI checkbox')
  assert.deepEqual(clone(payload.contextCompressionOrder), clone(canonical.contextCompressionOrder))
  assert.equal(payload.temperature, 0); assert.equal(payload.topP, 0)
  assert.equal(payload.searchPricePerCall, CONFIG.searchPricePerCall); assert.equal(payload.tiku_search_price_per_call, CONFIG.tiku_search_price_per_call); assert.equal(payload.webReadPricePerCall, CONFIG.webReadPricePerCall)
  for (const name of ['connection', 'advanced', 'prompt', 'stage']) assert.deepEqual(labels(region(w, name)), LABELS[name])
  assert.equal(field(region(w, 'connection'), '服务地址').control.value, CONFIG.baseUrl)
  const key = field(region(w, 'connection'), 'API Key').control
  assert.equal(key.value, CONFIG.apiKey); assert.equal(key.type, 'password'); assert.equal(key.name, 'ai-service-api-key'); assert.equal(key.autocomplete, 'new-password')
  assert.equal(field(region(w, 'connection'), '服务地址').control.name, 'ai-service-endpoint')
  assert.equal(field(region(w, 'connection'), '模型列表').control.value, 'MODEL-ZERO')
  assert.equal(field(region(w, 'connection'), '备用模型').control.value, CONFIG.backupModel)
  for (const label of ['温度', 'Top P']) { const c = field(region(w, 'prompt'), label).control; assert.equal(c.value, '0'); assert.equal(c.disabled, true); assert.equal(c.readOnly, true) }
  for (const [label, value] of [['联网搜索单价（元/次）', '0.000062'], ['本地题库检索单价（元/次）', '0.000061'], ['网页读取单价（元/次）', '1.234567'], ['图片 OCR 单价（元/次）', '0.000000'], ['模型最大调用次数', '4'], ['最大搜索次数', '3'], ['每轮重试次数', '3'], ['网页读取次数', '5'], ['压缩触发比例', '80'], ['压缩目标比例', '50'], ['压缩最大尝试次数', '3']]) assert.equal(field(region(w, 'advanced'), label).control.value, value)
  const modal = await open(w); assert.deepEqual(labels(modal), LABELS.price); assert.equal(w.vm.priceForm, canonicalPrice)
  for (const [label, value] of [['模型名称', 'MODEL-ZERO'], ['显示名称', 'Zero fixture'], ['上下文窗口', '128000'], ['输入单价', '0.000000'], ['缓存输入单价', '0.000000'], ['缓存写入单价', '0.000062'], ['输出单价', '1.234567'], ['倍率', '1.2345'], ['备注', 'SYNTHETIC_PRICE_REMARK']]) assert.equal(field(modal, label).control.value, value)
  assert.equal(labels(w.element).length + labels(modal).length, 33)
  await cancel(w, modal); assert.equal(w.vm.formState, canonical); assert.deepEqual(clone(canonical), initial); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(w.vm.priceForm, canonicalPrice); assert.equal(canonicalPrice.id, null)
})

test('positive: four stage-table controls are outside visible FormItem labels and classified with actual headers, never counted as form-label gaps', async () => {
  const [w] = await start(), table = w.element.querySelector('.staged-points-editor table')
  const headers = [...table.querySelectorAll('thead th')].map(node => compact(node.textContent)), controls = [...table.querySelectorAll(selector)].filter(visible)
  assert.equal(controls.length, 4)
  const observed = controls.map(control => { const cell = control.closest('td'); return { classification: 'outside-visible-FormItem-label-scope', header: headers[cell.cellIndex], value: control.value, ownLabelCount: control.labels.length, ariaLabel: control.getAttribute('aria-label'), labelledby: control.getAttribute('aria-labelledby') } })
  assert.deepEqual(observed.map(({ header, value }) => ({ header, value })), [{ header: '总 token 起点', value: '0' }, { header: '倍率', value: '2.000000' }, { header: '总 token 起点', value: '1000' }, { header: '倍率', value: '1.000000' }])
  for (const control of controls) assert.equal(control.closest('.ant-form-item'), null)
  assert.deepEqual(clone(w.vm.formState.stagedTokenMultiplierPoints), [{ tokens: '0', multiplier: 2 }, { tokens: '1000', multiplier: 1 }])
  console.log('AI_STAGE_TABLE_CONTEXT ' + JSON.stringify(observed))
})

for (const name of ['connection', 'advanced', 'prompt', 'stage']) test('native ' + name + ': all ' + LABELS[name].length + ' Chinese visible labels target exact own controls', async () => {
  const [w] = await start(), root = region(w, name), failures = []
  for (const text of LABELS[name]) try { associated(root, text) } catch (error) { failures.push(error.message) }
  assert.deepEqual(failures, [], 'actual association gaps, not value/payload failures: ' + name)
})

test('native price editor: nine labels retain stable IDs across local cancel/reopen and canonical edits', async () => {
  const [w] = await start(), canonical = w.vm.priceForm, config = clone(w.vm.formState)
  let modal = await open(w); const first = ids(modal, 'price')
  type(associated(modal, '显示名称').control, '本地取消价格草稿'); await settle()
  assert.equal(w.vm.priceForm, canonical); assert.equal(canonical.displayName, '本地取消价格草稿'); assert.equal(w.vm.hasUnsavedChanges, false)
  await cancel(w, modal); modal = await open(w); assert.deepEqual(ids(modal, 'price'), first); assert.equal(canonical.displayName, 'Zero fixture'); assert.deepEqual(clone(w.vm.formState), config); noDuplicateIds(); await cancel(w, modal)
})

test('native labels focus all enabled own controls and activate only own switches; local switch reversion preserves models', async () => {
  const [w] = await start(), config = clone(w.vm.formState), modal = await open(w)
  let focused = 0, disabled = 0
  for (const [root, list] of [...['connection', 'advanced', 'prompt', 'stage'].map(name => [region(w, name), LABELS[name]]), [modal, LABELS.price]]) for (const text of list) {
    const { label, control } = associated(root, text)
    if (control.disabled) { assert.ok(['温度', 'Top P'].includes(text)); disabled++; continue }
    const checked = control.getAttribute('aria-checked')
    label.click(); await settle(); assert.equal(document.activeElement, control, 'actual label focuses own native control: ' + text); focused++
    if (control.getAttribute('role') === 'switch') {
      assert.equal(control.getAttribute('aria-checked'), String(checked !== 'true'), 'label activates its own switch: ' + text)
      label.click(); await settle(); assert.equal(control.getAttribute('aria-checked'), checked, 'second local label click restores its own switch: ' + text)
    }
  }
  assert.equal(focused, 31); assert.equal(disabled, 2); assert.deepEqual(clone(w.vm.formState), config); await cancel(w, modal)
})

test('same app: two page instances and simultaneous price editors have unique stable IDs and independent canonical drafts', async () => {
  const [a, b] = await start(2), canonicalA = a.vm.formState, canonicalB = b.vm.formState
  const aIds = Object.fromEntries(['connection', 'advanced', 'prompt', 'stage'].map(name => [name, ids(region(a, name), name)]))
  const bIds = Object.fromEntries(['connection', 'advanced', 'prompt', 'stage'].map(name => [name, ids(region(b, name), name)]))
  assert.notEqual(canonicalA, canonicalB); const aModal = await open(a), bModal = await open(b), priceA = ids(aModal, 'price'), priceB = ids(bModal, 'price'); noDuplicateIds()
  type(associated(region(a, 'connection'), '默认模型').control, 'MODEL-LOCAL-A'); type(associated(region(a, 'prompt'), 'Prompt 设置').control, '仅本实例本地草稿'); type(associated(aModal, '显示名称').control, '价格本地A'); await settle()
  assert.equal(a.vm.formState, canonicalA); assert.equal(b.vm.formState, canonicalB); assert.equal(canonicalA.model, 'MODEL-LOCAL-A'); assert.equal(canonicalB.model, CONFIG.model); assert.equal(canonicalB.prompt, CONFIG.prompt)
  assert.equal(a.vm.hasUnsavedChanges, true); assert.equal(b.vm.hasUnsavedChanges, false); assert.equal(a.vm.priceForm.displayName, '价格本地A'); assert.equal(b.vm.priceForm.displayName, 'Zero fixture')
  for (const name of ['connection', 'advanced', 'prompt', 'stage']) { assert.deepEqual(ids(region(a, name), name), aIds[name]); assert.deepEqual(ids(region(b, name), name), bIds[name]) }
  assert.deepEqual(ids(aModal, 'price'), priceA); assert.deepEqual(ids(bModal, 'price'), priceB); noDuplicateIds(); await cancel(a, aModal); await cancel(b, bModal)
})


test('compression order native visible label owns stable group and entry, all nine buttons and keyboard focus across two instances', async () => {
 const [a, b] = await start(2), originalA = clone(a.vm.formState), originalB = clone(b.vm.formState), readCount = reads.length
 function own(w) {
  const root = region(w, 'advanced'), prefix = w.vm.sectionIds.advanced + '-contextCompressionOrder'
  const matches = [...root.querySelectorAll('.ant-form-item-label > label')].filter(n => visible(n) && compact(n.textContent) === '压缩执行顺序')
  assert.equal(matches.length, 1, 'one actual visible field label')
  const label = matches[0], entry = document.getElementById(prefix + '-entry'), group = document.getElementById(prefix + '-group'), title = document.getElementById(prefix + '-label')
  assert.equal(label.htmlFor, prefix + '-entry', 'field label targets actual native focus entry, not an unlabelable div')
  assert.ok(entry instanceof HTMLButtonElement && visible(entry) && !entry.disabled)
  assert.equal(label.control, entry); assert.ok([...entry.labels].includes(label))
  assert.ok(group && group.getAttribute('role') === 'group' && root.contains(group) && group.contains(entry))
  assert.equal(group.getAttribute('aria-labelledby'), prefix + '-label')
  assert.ok(title && label.contains(title) && visible(title)); assert.equal(compact(title.textContent), '压缩执行顺序')
  const buttons = [...group.querySelectorAll('button')].filter(visible)
  assert.equal(buttons.length, 9, 'all three handles and six move buttons remain native and visible')
  assert.equal(group.querySelectorAll('.compression-order-handle').length, 3)
  const ids = [...root.querySelectorAll('[id]')].map(n => n.id).sort()
  return { label, entry, group, ids }
 }
 const firstA = own(a), firstB = own(b); noDuplicateIds(); assert.notEqual(firstA.entry.id, firstB.entry.id)
 firstA.label.click(); await settle(); assert.equal(document.activeElement, firstA.entry, 'real visible label click focuses its own native button')
 assert.deepEqual(clone(a.vm.formState), originalA, 'label focus does not reorder or toggle other fields')
 const index = originalA.contextCompressionOrder.indexOf('server'), down = index < 2 ? 'ArrowDown' : 'ArrowUp', step = index < 2 ? 1 : -1
 const expected = [...originalA.contextCompressionOrder]; [expected[index], expected[index + step]] = [expected[index + step], expected[index]]
 firstA.entry.dispatchEvent(new window.KeyboardEvent('keydown', { key: down, bubbles: true, cancelable: true })); await settle()
 assert.deepEqual(clone(a.vm.formState.contextCompressionOrder), expected)
 assert.equal(document.activeElement, firstA.entry, 'keyboard reorder retains focus on the same strategy control')
 assert.ok(firstA.group.querySelector('[role="status"]').textContent.includes('已移到第'))
 assert.deepEqual(own(a).ids, firstA.ids, 'all group/entry/strategy IDs stay stable after reordering')
 assert.deepEqual(clone(b.vm.formState), originalB, 'instance A reorder cannot change B')
 firstA.entry.dispatchEvent(new window.KeyboardEvent('keydown', { key: step === 1 ? 'ArrowUp' : 'ArrowDown', bubbles: true, cancelable: true })); await settle()
 assert.deepEqual(clone(a.vm.formState), originalA, 'opposite native arrow returns exact original draft and legacy fields')
 assert.equal(a.vm.hasUnsavedChanges, false)
 const rows = [...firstA.group.querySelectorAll('[data-strategy]')], firstRow = rows[0], strategy = firstRow.getAttribute('data-strategy')
 const downButton = [...firstRow.querySelectorAll('button')].find(n => n.getAttribute('aria-label')?.endsWith('下移'))
 assert.ok(downButton && !downButton.disabled); downButton.click(); await settle()
 assert.notDeepEqual(clone(a.vm.formState.contextCompressionOrder), originalA.contextCompressionOrder)
 const moved = firstA.group.querySelector('[data-strategy="' + strategy + '"]'), upButton = [...moved.querySelectorAll('button')].find(n => n.getAttribute('aria-label')?.endsWith('上移'))
 assert.ok(upButton && !upButton.disabled); upButton.click(); await settle()
 assert.deepEqual(clone(a.vm.formState), originalA); assert.equal(a.vm.hasUnsavedChanges, false)
 firstB.label.click(); await settle(); assert.equal(document.activeElement, firstB.entry)
 assert.deepEqual(own(a).ids, firstA.ids); assert.deepEqual(own(b).ids, firstB.ids); noDuplicateIds()
 assert.equal(reads.length, readCount); assert.deepEqual(mutations, []); assert.deepEqual(network, [])
})
