/* Actual UserUsage + Vue/Antd amount associations; only synthetic GET APIs.
 * No checkout/order mutation or real IO. Same fixture is used unchanged for B/M/R.
 * jsdom native DOM associations/Antd focus are not reader/layout certification. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs'), path = require('node:path'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[key] = window[key]
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, defineComponent, h } = require('vue')
const Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const USER = { id: 701, username: 'SYNTHETIC_USAGE_AMOUNT', role: 'user', balance: 1.000062, minRechargeAmount: 1 }
const METHOD = { id: 17, name: '合成支付方式', enabled: true, sort: 0, epayType: 'fixture', iconSvg: '' }
const state = { reads: [], mutations: [], network: [], runtimeErrors: [], paymentRows: [METHOD], min: 1 }
const wrappers = [], observations = []
const clone = value => JSON.parse(JSON.stringify(value))
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
function forbid(kind) { state.network.push(kind); throw new Error('FORBIDDEN_USAGE_AMOUNT_IO_' + kind) }
function read(name, data) { state.reads.push(name); return Promise.resolve({ status: 200, data: clone(data) }) }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
api.exports = {
  getCurrentUser: () => read('getCurrentUser', { ...USER, minRechargeAmount: state.min }),
  getBillingRecords: () => read('getBillingRecords', { data: [], total: 0, page: 1, pageSize: 10 }),
  getPaymentMethods: () => read('getPaymentMethods', state.paymentRows),
  createRechargeOrder: () => { state.mutations.push('createRechargeOrder'); throw new Error('NO_USAGE_AMOUNT_ORDER_AUTHORIZED') },
}
require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return forbid('XHR') }
globalThis.fetch = window.fetch = () => forbid('FETCH')
window.open = () => forbid('CHECKOUT_WINDOW')
globalThis.WebSocket = window.WebSocket = class { constructor() { forbid('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => forbid(name); transport.get = () => forbid(name) }
window.addEventListener('error', event => state.runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => state.runtimeErrors.push(String(event.reason?.stack || event.reason)))
const pagePath = path.join(web, 'src/pages/UserUsage/index.vue'), Page = require(pagePath)
const sourceSHA256 = () => crypto.createHash('sha256').update(fs.readFileSync(pagePath)).digest('hex')
const startSourceHash = sourceSHA256()
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function amountField(page) {
  const labels = [...page.element.querySelectorAll('.recharge-form .ant-form-item-label > label')].filter(label => compact(label.textContent) === '充值金额')
  assert.equal(labels.length, 1, 'exact original visible amount label')
  const label = labels[0], item = label.closest('.ant-form-item'), controls = [...item.querySelectorAll('input')].filter(input => input.type !== 'hidden')
  assert.equal(controls.length, 1, 'actual Antd native amount input, not a wrapper')
  const hint = item.querySelector('.hint'); assert.ok(hint, 'original visible minimum/two-decimal hint retained')
  return { label, input: controls[0], hint }
}
function associatedAmount(page) {
  const field = amountField(page), { label, input } = field
  assert.ok(input.id, 'actual amount input has a stable nonempty ID')
  assert.equal(label.htmlFor, input.id, 'visible Chinese label for targets actual native input')
  assert.equal(label.control, input, 'HTMLLabelElement.control resolves actual amount input')
  assert.ok([...input.labels].includes(label), 'native input.labels includes exact visible label')
  assert.equal(document.getElementById(input.id), input)
  return field
}
function amountDescription(page) {
  const field = amountField(page), { input, hint } = field
  assert.ok(hint.id, 'existing minimum/two-decimal hint has an ID')
  const references = String(input.getAttribute('aria-describedby') || '').split(/\s+/).filter(Boolean)
  assert.ok(references.includes(hint.id), 'amount describes actual visible hint, not an unrelated message')
  assert.equal(document.getElementById(hint.id), hint)
  assert.ok(compact(hint.textContent).includes('最低充值金额：¥ ' + page.vm.formatMoneyValue(page.vm.minRechargeAmount)))
  assert.ok(hint.textContent.includes('金额最多支持两位小数'))
  return field
}
function orderButton(page) {
  const buttons = [...page.element.querySelectorAll('button')].filter(node => compact(node.textContent).replace(/\s/g, '') === '创建充值订单')
  assert.equal(buttons.length, 1); return buttons[0]
}
async function page({ twins = false, rows = [METHOD], min = 1 } = {}) {
  state.paymentRows = rows; state.min = min
  user.setStoredCurrentUser(USER)
  const component = twins ? defineComponent({ setup: () => () => h('section', [h(Page, { key: 'first' }), h(Page, { key: 'second' })]) }) : Page
  const root = mount(component, { attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(root); await settle()
  const pages = twins ? root.findAllComponents(Page) : [root]
  assert.equal(pages.length, twins ? 2 : 1)
  for (const entry of pages) {
    assert.equal(entry.vm.paymentMethodLoading, false); assert.equal(entry.vm.tableLoading, false)
    const { label, input, hint } = amountField(entry)
    console.log('USAGE_AMOUNT_NATIVE_DOM ' + JSON.stringify({ inputId: input.id, value: input.value, labelFor: label.htmlFor, labelControlMatches: label.control === input, hintId: hint.id, describedBy: input.getAttribute('aria-describedby'), balanceText: entry.find('.balance-value').text(), minimum: entry.vm.minRechargeAmount }))
  }
  return pages
}
afterEach(async () => {
  for (const root of wrappers.splice(0)) if (root.exists()) root.unmount()
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
  user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  const value = { sourceSHA256: sourceSHA256(), reads: state.reads.length, mutations: state.mutations.slice(), realIO: state.network.slice(), runtimeErrors: state.runtimeErrors.slice() }
  observations.push(value); console.log('USAGE_AMOUNT_NATIVE_SAFETY ' + JSON.stringify(value))
  assert.equal(sourceSHA256(), startSourceHash, 'source remained immutable during this native run')
  assert.deepEqual(state.mutations, [], 'no order confirmation or other mutation was invoked')
  assert.deepEqual(state.network, [], 'no real network/provider/checkout/database IO')
  assert.deepEqual(state.runtimeErrors, [], 'actual runtime errors fail, never get swallowed')
  state.reads.length = state.mutations.length = state.network.length = state.runtimeErrors.length = 0
})

test('visible recharge amount label associates with the actual native Antd number input', async () => {
  const [p] = await page(); const { input } = associatedAmount(p)
  assert.equal(input.getAttribute('role'), 'spinbutton'); assert.equal(Number(input.value), 10)
})
test('existing minimum and two-place hint is the actual input description and follows server minimum', async () => {
  const [p] = await page({ min: 12.5 }); const { hint } = amountDescription(p)
  assert.equal(p.vm.minRechargeAmount, 12.5); assert.equal(p.vm.rechargeAmount, 12.5)
  assert.ok(hint.textContent.includes('12.5'))
})
test('two real UserUsage instances within one Vue app have unique label/control/hint IDs with no private metadata', async () => {
  const pages = await page({ twins: true }), fields = pages.map(associatedAmount)
  for (const p of pages) amountDescription(p)
  const ids = fields.flatMap(({ input, hint }) => [input.id, hint.id])
  assert.equal(new Set(ids).size, 4)
  const all = [...document.querySelectorAll('[id]')].map(node => node.id).filter(Boolean)
  assert.equal(new Set(all).size, all.length, 'all actual same-app IDs unique, not just tested fields')
  for (const id of ids) for (const privateValue of [USER.username, String(USER.id), '1.000062']) assert.ok(!id.includes(privateValue), 'field ID contains no account or balance')
})
test('clicking the real Chinese amount label focuses its own actual native input without changing amount or posting', async () => {
  const [p] = await page(); const { label, input } = associatedAmount(p), value = p.vm.rechargeAmount
  document.activeElement?.blur(); label.click(); await settle()
  assert.equal(document.activeElement, input, 'actual Antd label activation focuses its native control without a test shim')
  assert.equal(p.vm.rechargeAmount, value); assert.deepEqual(state.mutations, [])
})
test('same-app second amount label focuses only its own field while both amounts remain unchanged', async () => {
  const pages = await page({ twins: true }), fields = pages.map(associatedAmount)
  fields[0].label.click(); await settle(); assert.equal(document.activeElement, fields[0].input)
  fields[1].label.click(); await settle(); assert.equal(document.activeElement, fields[1].input)
  assert.equal(pages[0].vm.rechargeAmount, 10); assert.equal(pages[1].vm.rechargeAmount, 10)
})
test('existing six-place balance and two-place amount/parser/formatter/keydown behavior stay intact', async () => {
  const [p] = await page(); const { input } = amountField(p)
  assert.equal(p.vm.currentUserBalance, 1.000062); assert.ok(p.find('.balance-value').text().includes('1.000062'))
  const numeric = p.findComponent(Antd.InputNumber)
  assert.equal(numeric.props('precision'), 2); assert.equal(numeric.props('step'), 1); assert.equal(numeric.props('min'), 1)
  assert.equal(p.vm.parseRechargeAmountInput('25.509'), '25.50'); assert.equal(p.vm.formatRechargeAmountInput('25.509'), '25.50')
  assert.equal(p.vm.parseRechargeAmountInput('12.'), '12.'); assert.equal(p.vm.formatRechargeAmountInput('12.'), '12.')
  input.value = '25.50'
  const blocked = new KeyboardEvent('keydown', { key: '9', bubbles: true, cancelable: true }); input.dispatchEvent(blocked); await settle(); assert.equal(blocked.defaultPrevented, true)
  const backspace = new KeyboardEvent('keydown', { key: 'Backspace', bubbles: true, cancelable: true }); input.dispatchEvent(backspace); await settle(); assert.equal(backspace.defaultPrevented, false)
  p.vm.rechargeAmount = 25.5; await settle(); assert.equal(input.value, '25.5', 'original custom formatter preserves value without imposing new zero padding')
  assert.equal(orderButton(p).disabled, false); assert.deepEqual(state.mutations, [])
})
test('empty and disabled payment methods preserve disabled order state, without any order click or IO', async () => {
  const [p] = await page({ rows: [{ ...METHOD, enabled: false }] })
  assert.equal(p.vm.paymentMethods.length, 0); assert.equal(orderButton(p).disabled, true)
  assert.ok(p.text().includes('暂无可用支付方式，请联系管理员。'))
  assert.equal(amountField(p).input.getAttribute('role'), 'spinbutton')
})
test('explicit identity retirement clears payment state while retaining nonprivate amount field semantics', async () => {
  const [p] = await page(); const before = amountField(p).input
  assert.equal(p.vm.paymentMethods.length, 1); user.clearStoredCurrentUser(); await settle()
  assert.equal(p.vm.paymentMethods.length, 0); assert.equal(p.vm.dataSource.length, 0); assert.equal(p.vm.selectedPaymentMethodId, null)
  assert.equal(orderButton(p).disabled, true); assert.equal(p.vm.currentUserBalance, 0)
  assert.equal(amountField(p).input, before, 'this UI-only association change does not remount or change existing amount-owner policy')
})
