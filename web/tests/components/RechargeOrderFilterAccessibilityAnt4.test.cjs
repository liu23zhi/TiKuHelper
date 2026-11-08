/* Actual RechargeOrderList + Vue/Antd filter labels and captured read handlers.
 * Only synthetic GET/router transports; all writes/payment/provider IO trapped.
 * Unchanged fixture accepts TIKU_WEB_SOURCE/UI_TEST_HELPER for native B/M/R.
 * DOM/actual Antd label activation is not reader, device, or layout certification. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const name of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[name] = window[name]
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, reactive, defineComponent, h } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 49101, username: 'SYNTHETIC_FILTER_A', role: 'user', status: 'enabled' }
const B = { id: 49102, username: 'SYNTHETIC_FILTER_B', role: 'user', status: 'enabled' }
const labels = ['关键词', '状态', '支付渠道'], calls = [], wrappers = [], network = [], writes = [], errors = []
let activeRoute, holdNext = false
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
function trap(kind) { network.push(kind); throw new Error('FORBIDDEN_FILTER_IO_' + kind) }
function row(actor, marker = 'SYNTHETIC_FILTER_ORDER') { return { id: 17, userId: actor.id, username: actor.username, orderNo: marker, amount: 12.34, status: 'paid', paymentProvider: 'epay', createdAt: '2026-10-28T01:02:03Z' } }
function reply(actor = A, marker) { return { status: 200, data: { data: [row(actor, marker)], total: 1, page: 1, pageSize: 10, summary: { totalAmount: 12.34, currentPageAmount: 12.34 } } } }
function read(params) {
  const actor = clone(user.getStoredCurrentUser()), call = { params: clone(params), actor, completed: false }
  calls.push(call)
  const promise = new Promise(resolve => { call.resolve = value => { call.completed = true; resolve(value) } })
  if (holdNext) holdNext = false
  else call.resolve(reply(actor))
  return promise
}
function mock(filename, exports) { const module = new Module(filename); module.loaded = true; module.exports = exports; require.cache[filename] = module }
mock(path.join(web, 'src/api/TikuComponentApi.js'), {
  getRechargeOrders: read,
  ...Object.fromEntries(['createRechargeOrder', 'createQuestions', 'updateUser', 'adjustUserBalance', 'updateSystemSettings'].map(name => [name, () => { writes.push(name); throw new Error('FORBIDDEN_FILTER_WRITE_' + name) }])),
})
mock(require.resolve('vue-router'), {
  useRouter: () => ({ currentRoute: { get value() { return activeRoute } }, push() { return trap('NAVIGATION') } }),
  useRoute: () => activeRoute,
  onBeforeRouteLeave: () => {},
})
window.XMLHttpRequest.prototype.open = function () { return trap('XHR') }
globalThis.fetch = window.fetch = () => trap('FETCH')
window.open = () => trap('CHECKOUT_WINDOW')
globalThis.WebSocket = window.WebSocket = class { constructor() { trap('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => trap(name); transport.get = () => trap(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
const file = path.join(web, 'src/pages/RechargeOrderList/index.vue'), Page = require(file)
const sourceHash = () => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
const initialSourceHash = sourceHash()
let computedName, nameLibrary = 'explicit DOM naming-source observation (not a full AccName implementation)'
try { computedName = require('dom-accessibility-api').computeAccessibleName; nameLibrary = 'installed dom-accessibility-api' } catch (error) { if (error.code !== 'MODULE_NOT_FOUND') throw error }
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function field(page, text) {
  const found = [...page.element.querySelectorAll('.filter-form .ant-form-item-label > label')].filter(label => compact(label.textContent) === text)
  assert.equal(found.length, 1, 'one exact visible filter label: ' + text)
  const label = found[0], item = label.closest('.ant-form-item'), inputs = [...item.querySelectorAll('input')].filter(input => input.type !== 'hidden')
  assert.equal(inputs.length, 1, 'one actual input/combobox, not wrapper: ' + text)
  return { label, input: inputs[0], item }
}
function associated(page, text) {
  const value = field(page, text), { label, input } = value
  assert.ok(input.id, 'actual filter input has a stable nonempty ID: ' + text)
  assert.equal(label.htmlFor, input.id, 'visible Chinese label targets its native input: ' + text)
  assert.equal(label.control, input, 'HTMLLabelElement.control resolves actual input: ' + text)
  assert.ok([...input.labels].includes(label)); assert.equal(document.getElementById(input.id), input)
  return value
}
function callback(component, name) { const fn = component.vm.$.vnode.props?.[name]; assert.equal(typeof fn, 'function'); return fn }
function button(page, text) { const value = [...page.element.querySelectorAll('button')].find(node => compact(node.textContent).replace(/\s/g, '') === text.replace(/\s/g, '')); assert.ok(value, 'actual native button: ' + text); return value }
async function open(twins = false) {
  user.setStoredCurrentUser(A); activeRoute = reactive({ path: '/recharge-orders', fullPath: '/recharge-orders', query: {} })
  const root = mount(twins ? defineComponent({ setup: () => () => h('section', [h(Page, { key: 'first' }), h(Page, { key: 'second' })]) }) : Page, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => errors.push(String(error?.stack || error)) } } })
  wrappers.push(root); await settle(); const pages = twins ? root.findAllComponents(Page) : [root]
  assert.equal(pages.length, twins ? 2 : 1)
  for (const page of pages) {
    assert.equal(page.vm.tableLoading, false); assert.equal(page.vm.dataSource.length, 1)
    console.log('RECHARGE_FILTER_NATIVE_DOM ' + JSON.stringify({ fields: labels.map(text => { const { label, input } = field(page, text); return { text, labelFor: label.htmlFor, inputId: input.id, role: input.getAttribute('role'), labelControlMatches: label.control === input } }), query: clone(calls.at(-1).params) }))
  }
  return pages
}
afterEach(async () => {
  for (const page of wrappers.splice(0)) if (page.exists()) page.unmount()
  for (const call of calls) if (!call.completed) call.resolve({ status: 503 })
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('RECHARGE_FILTER_NATIVE_SAFETY ' + JSON.stringify({ sourceSHA256: sourceHash(), GETReads: calls.length, writes, realIO: network, runtimeErrors: errors }))
  assert.equal(sourceHash(), initialSourceHash); assert.deepEqual(writes, []); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  calls.length = writes.length = network.length = errors.length = 0; activeRoute = undefined; holdNext = false
})
for (const text of labels) test('visible ' + text + ' label associates with its actual native Antd filter control', async () => {
  const [page] = await open(); const { input } = associated(page, text)
  if (text === '状态') assert.equal(input.getAttribute('role'), 'combobox')
})
for (const text of labels) test('actual Antd ' + text + ' label click focuses its own native filter input without issuing GET', async () => {
  const [page] = await open(), { label, input } = associated(page, text), count = calls.length, before = clone(page.vm.filters)
  document.activeElement?.blur(); label.click(); await settle()
  assert.equal(document.activeElement, input, 'real Antd label activation focuses actual input without a test shim')
  assert.equal(calls.length, count); assert.deepEqual(clone(page.vm.filters), before)
})
test('two RechargeOrderList instances in one Vueapp have distinct IDs, own focus, and preserve provider blur reads', async () => {
  const pages = await open(true), controls = pages.flatMap(page => labels.map(text => associated(page, text))), values = controls.map(value => value.input.id)
  assert.equal(new Set(values).size, 6)
  const all = [...document.querySelectorAll('[id]')].map(value => value.id).filter(Boolean); assert.equal(new Set(all).size, all.length)
  for (const id of values) for (const privateValue of [A.username, B.username, String(A.id), String(B.id)]) assert.ok(!id.includes(privateValue))
  for (const page of pages) for (const text of labels) { const { label, input } = associated(page, text); label.click(); await settle(); assert.equal(document.activeElement, input) }
  assert.equal(calls.length, 3, 'moving focus away from the first provider preserves its original blur-triggered read')
  assert.deepEqual(calls.map(call => call.params), [{ page: 1, pageSize: 10 }, { page: 1, pageSize: 10 }, { page: 1, pageSize: 10 }])
  assert.ok(calls.every(call => call.actor.id === A.id), 'focus never changes query owner or parameters')
})
test('actual search icon button retains a nonempty programmatic naming source without inventing a missing Chinese label', async () => {
  const [page] = await open(), control = page.element.querySelector('button.ant-input-search-button'); assert.ok(control)
  const referenceNames = String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => document.getElementById(id)?.textContent || '').join(' ')
  const icon = control.querySelector('[role="img"][aria-label]:not([aria-hidden="true"])')
  const sources = { ariaLabel: control.getAttribute('aria-label') || '', ariaLabelledby: compact(referenceNames), title: control.getAttribute('title') || '', text: compact(control.textContent), exposedImageLabel: icon?.getAttribute('aria-label') || '' }
  const name = computedName ? computedName(control) : compact(sources.ariaLabel || sources.ariaLabelledby || sources.text || sources.exposedImageLabel || sources.title)
  console.log('RECHARGE_FILTER_SEARCH_NAME ' + JSON.stringify({ method: nameLibrary, name, sources, imageAriaHidden: icon?.getAttribute('aria-hidden') || null }))
  assert.ok(name, 'actual button has an explicit native/ARIA naming source; do not fake it from InputSearch attrs or hidden SVG title')
  assert.equal(calls.length, 1)
})
test('original keyword draft apply status provider trim and reset keep exact read-only GET parameters', async () => {
  const [page] = await open(); assert.deepEqual(calls[0].params, { page: 1, pageSize: 10 })
  const keyword = page.get('input[placeholder="订单号 / 第三方单号 / 用户 / 支付方式"]')
  await keyword.setValue('  SYNTHETIC_草稿%_  '); await settle(); assert.equal(calls.length, 1); assert.equal(page.vm.filters.keyword, '')
  page.get('button.ant-input-search-button').element.click(); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_草稿%_', page: 1, pageSize: 10 })
  const select = page.findComponent(Antd.Select); select.vm.$emit('update:value', 'paid'); select.vm.$emit('change', 'paid'); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_草稿%_', status: 'paid', page: 1, pageSize: 10 })
  const provider = page.get('input[placeholder="例如 epay"]'); await provider.setValue('  epay  '); await provider.trigger('blur'); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_草稿%_', status: 'paid', paymentProvider: 'epay', page: 1, pageSize: 10 })
  button(page, '重置筛选').click(); await settle(); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 10 }); assert.equal(keyword.element.value, ''); assert.equal(page.vm.filters.status, undefined); assert.equal(page.vm.filters.paymentProvider, '')
  await keyword.setValue('SYNTHETIC_CLEAR'); await settle(); page.get('button.ant-input-search-button').element.click(); await settle(); const clear = page.element.querySelector('.ant-input-clear-icon'); assert.ok(clear); clear.click(); await settle(); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 10 }); assert.equal(page.vm.keywordInput, '')
})
test('original actor retirement rejects old captured filter callbacks and a late GET while new owned data can load', async () => {
  const [page] = await open(); holdNext = true; button(page, '刷新').click(); await settle(); const old = calls.at(-1), input = field(page, '关键词').input, oldSearch = callback(page.findComponent(Antd.InputSearch), 'onSearch')
  user.setStoredCurrentUser(B); assert.equal(page.vm.dataSource.length, 0); assert.equal(input.value, '')
  const before = calls.length; oldSearch('SYNTHETIC_RETIRED_FILTER'); assert.equal(calls.length, before); await settle(); assert.equal(page.vm.dataSource[0].userId, B.id)
  const count = calls.length; old.resolve(reply(A, 'SYNTHETIC_LATE_OLD_ORDER')); await settle(); assert.equal(calls.length, count); assert.equal(page.vm.dataSource[0].userId, B.id); assert.ok(!page.text().includes('SYNTHETIC_LATE_OLD_ORDER'))
})
test('original route retirement clears private filter state and rejects captured read handlers and late GET', async () => {
  const [page] = await open(); await page.get('input[placeholder="订单号 / 第三方单号 / 用户 / 支付方式"]').setValue('SYNTHETIC_ROUTE_DRAFT'); holdNext = true; button(page, '刷新').click(); await settle(); const old = calls.at(-1), oldSearch = callback(page.findComponent(Antd.InputSearch), 'onSearch')
  activeRoute.path = '/ordinary-next'; activeRoute.fullPath = '/ordinary-next'; assert.equal(page.vm.dataSource.length, 0); assert.equal(field(page, '关键词').input.value, '')
  const count = calls.length; oldSearch('SYNTHETIC_RETIRED_ROUTE'); old.resolve(reply(A, 'SYNTHETIC_LATE_ROUTE_ORDER')); await settle(); assert.equal(calls.length, count); assert.equal(page.vm.tableLoading, false); assert.ok(!page.text().includes('SYNTHETIC_LATE_ROUTE_ORDER'))
})
