/* RechargeResult records bridge: actual Vue/Antd/currentUser and a real memory
 * router. Only destination components and compiled-target reload are synthetic.
 * TIKU_WEB_SOURCE selects the target page; missing source dependencies and the
 * DOM helper fall back to this checkout. No production source is rewritten.
 * This is frontend navigation/display qualification, NOT production router
 * guard, server authorization, payment, settlement, viewport or reader parity.
 */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const fallbackWeb = process.env.UI_TEST_WEB_FALLBACK || path.resolve(__dirname, '../..')
const targetWeb = path.resolve(process.env.TIKU_WEB_SOURCE || fallbackWeb)
const target = path.join(targetWeb, 'src/pages/RechargeResult/index.vue')
const sha = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
const initialSHA = sha(target), observations = [], runtimeErrors = [], network = [], apiAttempts = [], reloadCalls = []
const wrappers = []
const sourceEnv = process.env.TIKU_WEB_SOURCE
process.env.NODE_PATH = [path.join(fallbackWeb, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
// vue-dom compiles actual global console primitives, with real action slots.
process.env.TIKU_WEB_SOURCE = fallbackWeb
require(process.env.UI_TEST_HELPER || path.join(fallbackWeb, 'tests/helpers/vue-dom.cjs'))
if (sourceEnv === undefined) delete process.env.TIKU_WEB_SOURCE; else process.env.TIKU_WEB_SOURCE = sourceEnv
require.extensions['.css'] = module => { module.exports = {} }
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(fallbackWeb, 'src/utils/currentUser.js'))
const USER = { id: 77961, role: 'user', status: 'enabled', username: 'SYNTHETIC_RESULT_USER', balance: 3.234567 }
const ADMIN = { id: 77962, role: 'admin', status: 'enabled', username: 'SYNTHETIC_RESULT_ADMIN', balance: 4.345678 }
const NOTICE = '支付结果以异步通知为准；当前页面只负责展示回跳信息，不会触发结算。'
const PRIVATE = 'SYNTHETIC_RETURN_TOKEN_NOT_AUTHORITY'
const cases = [
  { name: 'success', query: { out_trade_no: 'SYNTHETIC_RETURN_SUCCESS', trade_status: ' trade_success ', money: ' 88.123456 ', token: PRIVATE, callbackToken: PRIVATE }, status: 'success', title: '支付已完成', subtitle: '页面上的成功状态仅表示回跳成功，最终入账仍以异步通知为准。', order: 'SYNTHETIC_RETURN_SUCCESS', trade: 'TRADE_SUCCESS', money: '88.123456', details: true },
  { name: 'waiting', query: { orderNo: ' SYNTHETIC_RETURN_WAIT ', tradeStatus: 'wait_buyer_pay', money: '2.50', token: PRIVATE, callbackToken: PRIVATE }, status: 'warning', title: '支付处理中', subtitle: '当前交易还未完成，请稍后刷新订单列表查看最终结果。', order: 'SYNTHETIC_RETURN_WAIT', trade: 'WAIT_BUYER_PAY', money: '2.50', details: true },
  { name: 'unknown', query: { out_trade_no: 'SYNTHETIC_RETURN_UNKNOWN', trade_status: 'synthetic_untrusted', money: 'not-a-paid-proof', token: PRIVATE, callbackToken: PRIVATE }, status: 'error', title: '支付状态：SYNTHETIC_UNTRUSTED', subtitle: '如果回跳信息不完整或状态未更新，请稍后刷新页面或查看订单列表。', order: 'SYNTHETIC_RETURN_UNKNOWN', trade: 'SYNTHETIC_UNTRUSTED', money: 'not-a-paid-proof', details: true },
  { name: 'no-query', query: {}, status: 'info', title: '已返回支付结果页', subtitle: '如果回跳信息不完整或状态未更新，请稍后刷新页面或查看订单列表。', order: '', trade: '', money: '', details: false },
]
function denyIO(kind) { network.push(kind); throw new Error('FORBIDDEN_RECHARGE_RESULT_IO_' + kind) }
function denyAPI(name) { apiAttempts.push(String(name)); throw new Error('FORBIDDEN_RECHARGE_RESULT_API_' + String(name)) }
const rejectAPI = new Proxy({}, { get(_, name) { if (name === '__esModule' || name === 'then') return undefined; return () => denyAPI(name) } })
const originalLoad = Module._load
Module._load = function (request, parent, main) {
  let filename
  try { filename = Module._resolveFilename(request, parent, main) } catch { return originalLoad.call(this, request, parent, main) }
  if (typeof filename === 'string' && (filename.startsWith(path.join(fallbackWeb, 'src/api') + path.sep) || filename.startsWith(path.join(targetWeb, 'src/api') + path.sep))) return rejectAPI
  return originalLoad.call(this, request, parent, main)
}
window.XMLHttpRequest.prototype.open = function () { return denyIO('XHR') }
globalThis.fetch = window.fetch = () => denyIO('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { denyIO('WEBSOCKET') } }
Object.defineProperty(window.navigator, 'sendBeacon', { configurable: true, value: () => denyIO('BEACON') })
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => denyIO(name); transport.get = () => denyIO(name) }
for (const name of ['node:net', 'node:tls']) { const transport = require(name); transport.connect = () => denyIO(name); if ('createConnection' in transport) transport.createConnection = () => denyIO(name) }
window.addEventListener('error', event => runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtimeErrors.push(String(event.reason?.stack || event.reason)))
globalThis.__RECHARGE_RESULT_TEST_WINDOW__ = Object.freeze({ location: Object.freeze({ reload() { reloadCalls.push({ mocked: true, realReload: false }) } }) })
// Sparse B/M/R copies contain only the bound page. Their relative dependencies
// resolve to unchanged actual checkout modules, rather than synthesized helpers.
const originalResolve = Module._resolveFilename
Module._resolveFilename = function (request, parent, ...args) {
  if (targetWeb !== fallbackWeb && parent?.filename?.startsWith(path.join(targetWeb, 'src') + path.sep) && request.startsWith('.')) {
    const candidate = path.resolve(path.dirname(parent.filename), request)
    if (![candidate, candidate + '.js', candidate + '.mjs', candidate + '.vue', path.join(candidate, 'index.js')].some(file => fs.existsSync(file))) {
      const fallback = path.join(fallbackWeb, path.relative(targetWeb, candidate))
      return originalResolve.call(this, fallback, parent, ...args)
    }
  }
  return originalResolve.call(this, request, parent, ...args)
}
const originalCompile = Module.prototype._compile
Module.prototype._compile = function (code, filename) {
  // Lexical injection affects only the compiled target, never source bytes or
  // currentUser's real window/storage; the actual refresh handler still runs.
  if (filename === target) code = 'const window = globalThis.__RECHARGE_RESULT_TEST_WINDOW__;\n' + code
  return originalCompile.call(this, code, filename)
}
let Page
try { Page = require(target) } finally { Module.prototype._compile = originalCompile; Module._resolveFilename = originalResolve }
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
function visible(node) {
  if (!node?.isConnected) return false
  for (let item = node; item && item.nodeType === 1; item = item.parentElement) {
    const style = window.getComputedStyle(item)
    if (item.hidden || item.inert || item.getAttribute('aria-hidden') === 'true' || style.display === 'none' || style.visibility === 'hidden') return false
  }
  return true
}
function nativeButton(wrapper, text) {
  const list = [...wrapper.element.querySelectorAll('button')].filter(node => visible(node) && compact(node.textContent).replace(/\s/g, '') === text)
  assert.equal(list.length, 1, 'VISIBLE_NATIVE_BUTTON_' + text)
  assert.equal(list[0].disabled, false, 'ENABLED_NATIVE_BUTTON_' + text)
  assert.ok(list[0].classList.contains('ant-btn'), 'REAL_ANTD_BUTTON_' + text)
  return list[0]
}
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function record() {
  if (!process.env.RECHARGE_RECORDS_OBSERVATION_FILE) return
  fs.writeFileSync(process.env.RECHARGE_RECORDS_OBSERVATION_FILE, JSON.stringify({
    target, targetSha256Before: initialSHA, targetSha256After: sha(target), fixture: __filename, fixtureSha256: sha(__filename),
    dependencies: { fallbackWeb, domHelper: process.env.UI_TEST_HELPER || path.join(fallbackWeb, 'tests/helpers/vue-dom.cjs'), currentUser: path.join(fallbackWeb, 'src/utils/currentUser.js') },
    observations, network, apiAttempts, runtimeErrors, reloadCalls, realReloadCount: 0,
    qualification: 'Actual Vue/Antd/currentUser DOM and named navigation in a memory router; no production router guard or service/payment/auth qualification',
  }, null, 2) + '\n')
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) if (wrapper.exists()) wrapper.unmount()
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
  user.clearStoredCurrentUser(); document.body.replaceChildren()
  record()
  assert.equal(sha(target), initialSHA, 'BOUND_TARGET_BYTES_UNCHANGED')
  assert.deepEqual(network, [], 'ZERO_REAL_NETWORK_PROVIDER_DATABASE_OR_ORDER_IO')
  assert.deepEqual(apiAttempts, [], 'ZERO_API_ORDER_READ_WRITE_CREATE_RETRY_SETTLEMENT')
  assert.deepEqual(runtimeErrors, [], 'ZERO_HIDDEN_VUE_ANTD_RUNTIME_EXCEPTIONS')
})
after(() => { record(); Module._load = originalLoad })
for (const actor of [USER, ADMIN]) for (const input of cases) test('RECHARGE_RESULT ' + actor.role + ' / ' + input.name + ' visible records bridge is read-only named navigation; home refresh and asynchronous notice remain', async () => {
  const result = { role: actor.role, case: input.name, input: input.query, completed: false }
  observations.push(result)
  try {
    user.setStoredCurrentUser(actor)
    const storedBefore = window.localStorage.getItem('adapter_current_user')
    const home = user.resolveHomePath(user.getStoredCurrentUser())
    assert.equal(home, actor.role === 'admin' ? '/adapter/component' : '/adapter/user-usage')
    const sentinel = { render: () => null }
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/adapter/recharge/result', name: 'recharge-result', component: sentinel },
      { path: '/adapter/recharge-orders', name: 'recharge-orders', component: sentinel },
      { path: home, name: 'synthetic-existing-home', component: sentinel },
    ] })
    const push = router.push.bind(router), navigations = []
    await push({ name: 'recharge-result', query: input.query }); await router.isReady()
    router.push = destination => { navigations.push(JSON.parse(JSON.stringify(destination))); return push(destination) }
    const wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
    wrappers.push(wrapper); await settle()
    result.initialActions = [...wrapper.element.querySelectorAll('button')].filter(visible).map(node => compact(node.textContent))
    result.display = { status: wrapper.vm.resultStatus, title: wrapper.vm.title, order: wrapper.vm.orderNo, trade: wrapper.vm.tradeStatus, money: wrapper.vm.money, hasQuery: wrapper.vm.hasQuery }
    assert.equal(wrapper.vm.resultStatus, input.status); assert.equal(wrapper.vm.title, input.title)
    assert.equal(wrapper.vm.orderNo, input.order); assert.equal(wrapper.vm.tradeStatus, input.trade); assert.equal(wrapper.vm.money, input.money)
    assert.equal(wrapper.vm.hasQuery, input.details); assert.equal(wrapper.find('.details').exists(), input.details)
    const renderedResult = wrapper.findComponent(Antd.Result)
    assert.equal(renderedResult.props('status'), input.status); assert.equal(renderedResult.props('title'), input.title); assert.equal(renderedResult.props('subTitle'), input.subtitle)
    assert.ok(wrapper.text().includes(input.title)); assert.ok(wrapper.text().includes(input.subtitle)); assert.ok(wrapper.text().includes(NOTICE))
    assert.ok(wrapper.findAllComponents(Antd.Alert).some(alert => alert.props('message') === NOTICE && alert.props('type') === 'info'))
    if (input.details) { assert.ok(wrapper.text().includes(input.order)); assert.ok(wrapper.text().includes(input.money)) }
    assert.ok(!wrapper.text().includes(PRIVATE), 'RETURN_TOKENS_NOT_DISPLAYED')
    const reloadCount = reloadCalls.length
    nativeButton(wrapper, '刷新页面').click(); await settle()
    assert.equal(reloadCalls.length, reloadCount + 1); assert.deepEqual(navigations, [])
    nativeButton(wrapper, '返回首页').click(); await settle()
    assert.deepEqual(navigations, [home]); assert.equal(router.currentRoute.value.path, home); assert.deepEqual(router.currentRoute.value.query, {})
    result.originalPositives = { home, refreshHandlerMockCalls: 1, actualReloadCount: 0, asyncNoticePreserved: true, originalStatusOrderMoneyPreserved: true }
    await push({ name: 'recharge-result', query: input.query }); await settle()
    nativeButton(wrapper, '查看充值记录').click(); await settle()
    assert.deepEqual(navigations, [home, { name: 'recharge-orders' }], 'ONLY_EXISTING_NAMED_ROUTE_NO_CALLBACK_OR_AUTHORITY_PAYLOAD')
    assert.equal(router.currentRoute.value.name, 'recharge-orders'); assert.equal(router.currentRoute.value.path, '/adapter/recharge-orders')
    assert.deepEqual(router.currentRoute.value.query, {}); assert.deepEqual(router.currentRoute.value.params, {}); assert.equal(router.currentRoute.value.hash, '')
    assert.equal(window.localStorage.getItem('adapter_current_user'), storedBefore, 'NO_WALLET_ACTOR_OR_SESSION_MUTATION')
    assert.deepEqual(user.getStoredCurrentUser(), actor)
    assert.deepEqual(network, []); assert.deepEqual(apiAttempts, [])
    result.navigations = navigations; result.records = { name: router.currentRoute.value.name, query: {}, params: {}, hash: '' }; result.completed = true
    console.log('RECHARGE_RESULT_NATIVE ' + JSON.stringify({ role: actor.role, case: input.name, status: input.status, records: result.records, home, refreshMock: 1, api: apiAttempts.length, realIO: network.length, realReload: 0 }))
  } catch (error) { result.failure = { name: error.name, message: error.message }; throw error }
})
