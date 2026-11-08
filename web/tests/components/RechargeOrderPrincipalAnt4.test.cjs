/* RechargeOrderList read/display contract: real Vue, Antd, currentUser and request
 * ownership. Only API/router transports and CSS loading are synthetic. The same
 * fixture selects complete B/M/R web sources through TIKU_WEB_SOURCE and
 * UI_TEST_HELPER. No payment, order creation, provider, wallet, DB or real HTTP.
 * This is frontend display qualification, not server/payment/auth acceptance.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), path.resolve(__dirname, '../../node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLDivElement', 'CustomEvent']) globalThis[key] = window[key]
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, reactive } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 76811, role: 'user', status: 'enabled', username: 'SYNTHETIC_RECHARGE_A', balance: 0.123456 }
const B = { id: 76812, role: 'user', status: 'enabled', username: 'SYNTHETIC_RECHARGE_B' }
const ADMIN = { id: 76813, role: 'admin', status: 'enabled', username: 'SYNTHETIC_RECHARGE_ADMIN' }
const PRIVATE = 'SYNTHETIC_PRIVATE_CALLBACK_OR_UPSTREAM'
const calls = [], wrappers = [], errors = [], network = [], notices = [], leaves = []
let activeRoute
function clone(value) { return value === undefined ? undefined : JSON.parse(JSON.stringify(value)) }
function deferred(payload) {
  let yes, no
  const promise = new Promise((resolve, reject) => { yes = resolve; no = reject })
  const call = { payload: clone(payload), selectedActor: clone(user.getStoredCurrentUser()), completed: false, promise }
  call.resolve = value => { call.completed = true; yes(value) }
  call.reject = value => { call.completed = true; no(value) }
  calls.push(call); return promise
}
function mock(filename, exports) { const m = new Module(filename); m.loaded = true; m.exports = exports; require.cache[filename] = m }
mock(path.join(web, 'src/api/TikuComponentApi.js'), { getRechargeOrders: deferred })
mock(require.resolve('vue-router'), {
  useRoute: () => activeRoute,
  useRouter: () => ({ currentRoute: { get value() { return activeRoute } }, push() { throw new Error('UNEXPECTED_RECHARGE_NAVIGATION') } }),
  onBeforeRouteLeave: fn => leaves.push({ route: activeRoute, fn }),
})
for (const kind of ['success', 'warning', 'error']) {
  const native = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return native(...args) }
}
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_REAL_RECHARGE_IO_' + kind) }
window.XMLHttpRequest.prototype.open = function () { return deny('XHR') }
globalThis.fetch = window.fetch = () => deny('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const api = require(name); api.request = () => deny(name); api.get = () => deny(name) }
const Page = require(path.join(web, 'src/pages/RechargeOrderList/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 5)); await nextTick(); await flushPromises() }
async function open(actor = A, setActor = true) {
  if (setActor) { if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser() }
  const route = reactive({ path: '/recharge-orders', fullPath: '/recharge-orders', query: {} })
  activeRoute = route
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => errors.push(String(error?.stack || error)) } } })
  w.testRoute = route; wrappers.push(w); await settle(); return w
}
function order(marker = 'SYNTHETIC_CURRENT_ORDER', actor = A, overrides = {}) {
  return { id: 101, userId: actor.id, username: actor.username, orderNo: marker, paymentProvider: 'epay', paymentMethodNameSnapshot: 'SYNTHETIC_METHOD', amount: 12.34, status: 'paid', thirdPartyOrderNo: 'SYNTHETIC_THIRD_PARTY', paidAt: '2026-10-28T01:02:03Z', createdAt: '2026-10-27T01:02:03Z', updatedAt: '2026-10-28T01:03:04Z', ...overrides }
}
function reply(marker = 'SYNTHETIC_CURRENT_ORDER', actor = A, options = {}) {
  return { status: 200, data: { data: [order(marker, actor)], total: 23, page: 1, pageSize: 10, summary: { totalAmount: 98.76, currentPageAmount: 12.34 }, ...options } }
}
async function finish(call = calls.at(-1), result = reply()) { assert.ok(call, 'EXPECTED_READ'); call.resolve(result); await settle() }
function button(w, label) { const item = w.findAll('button').find(node => node.text().replace(/\s/g, '') === label.replace(/\s/g, '')); assert.ok(item, 'ACTUAL_BUTTON_' + label); return item }
async function refresh(w) { button(w, '刷新').element.click(); await settle() }
async function search(w, value) { await w.get('input[placeholder="订单号 / 第三方单号 / 用户 / 支付方式"]').setValue(value); w.get('button.ant-input-search-button').element.click(); await settle() }
function callback(component, name) { const fn = component.vm.$.vnode.props?.[name]; assert.equal(typeof fn, 'function', 'ACTUAL_ANTD_CALLBACK_' + name); return fn }
async function page(w, current, pageSize) { callback(w.findComponent(Antd.Table), 'onChange')({ current, pageSize }); await settle() }
function statistic(w, title) {
  const node = [...w.element.querySelectorAll('.ant-statistic')].find(item => item.querySelector('.ant-statistic-title')?.textContent === title)
  assert.ok(node, 'ACTUAL_STATISTIC_' + title); return node.querySelector('.ant-statistic-content').textContent.replace(/\s/g, '')
}
function amountUnknown(w) {
  for (const title of ['累计充值金额（当前筛选，全部页）', '当前页累计充值金额']) {
    const text = statistic(w, title)
    assert.doesNotMatch(text, /(?:^|¥)0(?:\.00)?$/, 'UNKNOWN_AMOUNT_IS_NOT_FALSE_ZERO_' + title)
    assert.match(text, /-|未知|待/, 'UNKNOWN_AMOUNT_EXPLICIT_' + title)
  }
}
function failure(w) { return w.findAllComponents(Antd.Alert).find(item => item.props('type') === 'error' || item.props('type') === 'warning') }
function noPrivateFeedback() { assert.ok(!document.body.textContent.includes(PRIVATE)); assert.ok(!JSON.stringify(notices).includes(PRIVATE)) }
function view(w) { return { rows: clone(w.vm.dataSource), summary: clone(w.vm.summary), pagination: { current: w.vm.pagination.current, pageSize: w.vm.pagination.pageSize, total: w.vm.pagination.total }, loading: w.vm.tableLoading } }
function emptyState(w) { assert.equal(w.vm.dataSource.length, 0); assert.equal(w.vm.pagination.total, 0) }
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  wrappers.length = 0; Antd.Modal.destroyAll(); Antd.message.destroy()
  for (const call of calls) if (!call.completed) call.resolve({ status: 503, message: PRIVATE })
  await settle(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle()
  user.clearStoredCurrentUser(); document.body.replaceChildren(); calls.length = notices.length = leaves.length = 0; activeRoute = undefined
  assert.deepEqual(network.splice(0), [], 'NO_REAL_PAYMENT_ORDER_PROVIDER_WALLET_DATABASE_OR_HTTP')
  assert.deepEqual(errors.splice(0), [], 'NO_HIDDEN_VUE_ANTD_RUNTIME_EXCEPTION')
})

test('RECHARGE existing ordinary first GET and server totals retain legal full order fields and two-decimal recharge rendering', async () => {
  const w = await open(); assert.equal(calls.length, 1); assert.deepEqual(calls[0].payload, { page: 1, pageSize: 10 })
  assert.equal(w.vm.tableLoading, true); await finish()
  assert.deepEqual(w.vm.dataSource[0], order()); assert.equal(w.vm.summary.total, 23); assert.equal(w.vm.summary.currentPageCount, 1)
  assert.equal(w.vm.summary.totalAmount, 98.76); assert.equal(w.vm.summary.currentPageAmount, 12.34)
  assert.equal(statistic(w, '累计充值金额（当前筛选，全部页）'), '¥98.76'); assert.equal(statistic(w, '当前页累计充值金额'), '¥12.34')
  assert.match(w.text(), /SYNTHETIC_THIRD_PARTY/); assert.match(w.text(), /SYNTHETIC_METHOD/); assert.match(w.text(), /已支付/)
  assert.match(w.text(), /2026-10-28\s+09:02:03/); assert.equal(w.vm.formatAmount(2.5), '2.50'); assert.equal(w.vm.tableLoading, false)
})
test('RECHARGE invalid amount leaves are unknown rather than coerced false zero; legitimate zero remains two decimals', async () => {
  const w = await open(); await finish()
  for (const value of [false, true, [], {}, null, undefined, '', '  ', 'not-money']) {
    assert.equal(w.vm.formatAmount(value), '--', 'MALFORMED_AMOUNT_NOT_ZERO_' + typeof value)
  }
  assert.equal(w.vm.formatAmount(0), '0.00'); assert.equal(w.vm.formatAmount('0'), '0.00'); assert.equal(w.vm.formatAmount('2.50'), '2.50')
})
test('RECHARGE existing username-only ordinary cache does not acquire a new admin-or-id prerequisite', async () => {
  const actor = { username: 'SYNTHETIC_LEGACY_READER', role: 'user' }
  const w = await open(actor); assert.equal(calls.length, 1, 'ORIGINAL_USERNAME_ONLY_FIRST_READ_REMAINS_ALLOWED')
  const legacy = order('SYNTHETIC_LEGACY_ORDER', { id: 76814, username: actor.username })
  await finish(calls[0], reply(undefined, A, { data: [legacy] })); assert.equal(w.vm.dataSource[0]?.orderNo, legacy.orderNo)
  assert.equal(w.vm.dataSource[0].thirdPartyOrderNo, legacy.thirdPartyOrderNo)
})
test('RECHARGE existing sparse actor metadata and legacy rows without ownership metadata remain server-authorized display inputs', async () => {
  const w = await open({ id: A.id, username: A.username }); assert.equal(calls.length, 1, 'MISSING_ROLE_STATUS_DO_NOT_INVENT_A_NEW_FIRST_GET_POLICY')
  const legacy = order('SYNTHETIC_LEGACY_SPARSE_ORDER'); delete legacy.userId; delete legacy.username
  await finish(calls[0], reply(undefined, A, { data: [legacy] })); assert.equal(w.vm.dataSource[0]?.orderNo, legacy.orderNo)
  assert.equal(w.vm.dataSource[0].thirdPartyOrderNo, legacy.thirdPartyOrderNo); assert.equal(w.vm.summary.totalAmount, 98.76)
  assert.match(w.text(), /SYNTHETIC_LEGACY_SPARSE_ORDER/)
})
test('RECHARGE existing draft keyword is not committed until native search; filters reset page and preserve exact GET params', async () => {
  const w = await open(); await finish(); const input = w.get('input[placeholder="订单号 / 第三方单号 / 用户 / 支付方式"]')
  await input.setValue('  SYNTHETIC_草稿%_  '); await settle(); assert.equal(calls.length, 1); assert.equal(w.vm.filters.keyword, '')
  await search(w, '  SYNTHETIC_草稿%_  '); assert.deepEqual(calls.at(-1).payload, { keyword: 'SYNTHETIC_草稿%_', page: 1, pageSize: 10 }); await finish()
  const select = w.findComponent(Antd.Select); select.vm.$emit('update:value', 'paid'); select.vm.$emit('change', 'paid'); await settle()
  assert.deepEqual(calls.at(-1).payload, { keyword: 'SYNTHETIC_草稿%_', status: 'paid', page: 1, pageSize: 10 }); await finish()
  await w.get('input[placeholder="例如 epay"]').setValue('  epay  '); await w.get('input[placeholder="例如 epay"]').trigger('blur'); await settle()
  assert.deepEqual(calls.at(-1).payload, { keyword: 'SYNTHETIC_草稿%_', status: 'paid', paymentProvider: 'epay', page: 1, pageSize: 10 }); await finish()
  await page(w, 3, 20); assert.deepEqual(calls.at(-1).payload, { keyword: 'SYNTHETIC_草稿%_', status: 'paid', paymentProvider: 'epay', page: 3, pageSize: 20 })
  await finish(calls.at(-1), reply(undefined, A, { page: 3, pageSize: 20 })); assert.equal(w.vm.pagination.current, 3); assert.equal(w.vm.pagination.pageSize, 20)
  button(w, '重置筛选').element.click(); await settle(); assert.deepEqual(calls.at(-1).payload, { page: 1, pageSize: 20 }); assert.equal(w.vm.keywordInput, '')
})
test('RECHARGE administrator preserves every existing DTO field and native unknown/closed status and date fallback', async () => {
  const w = await open(ADMIN), closed = order('SYNTHETIC_ADMIN_CLOSED', B, { id: 202, status: 'closed', amount: '2.50', paidAt: null }), unknown = order('SYNTHETIC_ADMIN_UNKNOWN', A, { id: 203, status: 'SYNTHETIC_LEGACY_STATUS', createdAt: 'SYNTHETIC_LEGACY_DATE' })
  await finish(calls[0], reply(undefined, ADMIN, { data: [closed, unknown], total: '2', summary: { totalAmount: '14.84', currentPageAmount: '14.84' } }))
  assert.deepEqual(clone(w.vm.dataSource), [closed, unknown]); assert.equal(w.vm.summary.total, 2); assert.equal(w.vm.summary.totalAmount, 14.84)
  assert.match(w.text(), /SYNTHETIC_RECHARGE_B/); assert.match(w.text(), /SYNTHETIC_ADMIN_CLOSED/); assert.match(w.text(), /SYNTHETIC_THIRD_PARTY/); assert.match(w.text(), /已关闭/)
  assert.match(w.text(), /SYNTHETIC_LEGACY_STATUS/); assert.match(w.text(), /SYNTHETIC_LEGACY_DATE/); assert.equal(statistic(w, '当前页累计充值金额'), '¥14.84')
})
test('RECHARGE ordinary projection rejects explicit foreign ownership and opaque callback extras without trusting mixed-owner summaries', async () => {
  const w = await open(), own = order('SYNTHETIC_OWN_ORDER', A, { callbackPayload: PRIVATE, apiKey: PRIVATE }), foreign = order('SYNTHETIC_FOREIGN_ORDER', B, { id: 204 })
  await finish(calls[0], reply(undefined, A, { data: [own, foreign], total: 2, summary: { totalAmount: 24.68, currentPageAmount: 24.68 } }))
  assert.ok(!w.vm.dataSource.some(item => item.userId === B.id)); assert.ok(!JSON.stringify(w.vm.dataSource).includes(PRIVATE)); assert.ok(!w.text().includes(foreign.orderNo)); assert.ok(!w.text().includes(B.username)); amountUnknown(w); noPrivateFeedback()
  assert.ok(failure(w), 'MIXED_OWNER_RESPONSE_IS_NOT_A_TRUSTED_SUCCESS'); await refresh(w)
  await finish(calls.at(-1), reply('SYNTHETIC_OWN_RECOVERED', A, { data: [own] })); assert.equal(w.vm.dataSource[0]?.orderNo, own.orderNo); assert.equal(w.vm.dataSource[0].thirdPartyOrderNo, own.thirdPartyOrderNo)
  assert.ok(!JSON.stringify(w.vm.dataSource).includes(PRIVATE)); assert.equal(Boolean(failure(w)), false)
})
test('RECHARGE mount-local rows filters pagination summary and loading cannot leak into another simultaneous mount', async () => {
  const one = await open(); await finish(calls[0], reply('SYNTHETIC_MOUNT_ONE')); await search(one, 'SYNTHETIC_ONE_FILTER'); await finish(calls.at(-1), reply('SYNTHETIC_ONE_FILTERED', A, { page: 3, pageSize: 20 }))
  const observed = view(one), two = await open(A, false)
  assert.deepEqual(view(one), observed, 'SECOND_SETUP_DOES_NOT_CLEAR_FIRST_MOUNT'); assert.equal(two.vm.dataSource.length, 0); assert.equal(two.vm.filters.keyword, ''); assert.equal(two.vm.pagination.current, 1); assert.equal(two.vm.pagination.pageSize, 10); assert.equal(two.vm.tableLoading, true)
  assert.deepEqual(calls.at(-1).payload, { page: 1, pageSize: 10 }); assert.ok(!two.text().includes('SYNTHETIC_ONE_FILTERED'))
  await finish(calls.at(-1), reply('SYNTHETIC_MOUNT_TWO', A, { total: 1, summary: { totalAmount: 9.87, currentPageAmount: 9.87 } }))
  assert.deepEqual(view(one), observed); assert.equal(two.vm.dataSource[0].orderNo, 'SYNTHETIC_MOUNT_TWO')
  two.unmount(); assert.deepEqual(view(one), observed, 'UNMOUNT_DISPOSES_ONLY_ITS_OWN_VIEW')
})
for (const outcome of ['success', 'error']) test('RECHARGE same-actor old ' + outcome + '/finally cannot paint or release a later committed query', async () => {
  const w = await open(), initial = calls[0]; await search(w, 'SYNTHETIC_OLDER_QUERY'); const older = calls.at(-1)
  await search(w, 'SYNTHETIC_CURRENT_QUERY'); const current = calls.at(-1), before = view(w), noticeCount = notices.length
  if (outcome === 'success') older.resolve(reply('SYNTHETIC_RETIRED_ORDER', A, { page: 7, pageSize: 100, summary: { totalAmount: 999, currentPageAmount: 999 } })); else older.reject(new Error(PRIVATE))
  await settle(); assert.deepEqual(view(w), before, 'OLD_SETTLEMENT_LEAVES_CURRENT_ROWS_SUMMARY_PAGINATION_BUSY'); assert.equal(w.vm.tableLoading, true); assert.equal(notices.length, noticeCount); assert.equal(Boolean(failure(w)), false)
  await finish(current, reply('SYNTHETIC_CURRENT_QUERY_ORDER', A, { total: 31, summary: { totalAmount: 33.33, currentPageAmount: 12.34 } })); const confirmed = view(w)
  await finish(initial, reply('SYNTHETIC_LATE_INITIAL_ORDER', A, { page: 9, pageSize: 100, summary: { totalAmount: 111, currentPageAmount: 111 } }))
  assert.deepEqual(view(w), confirmed); assert.equal(w.vm.dataSource[0].orderNo, 'SYNTHETIC_CURRENT_QUERY_ORDER'); assert.equal(w.vm.summary.totalAmount, 33.33); noPrivateFeedback()
})
test('RECHARGE same-actor old page and refresh results cannot overwrite latest server pagination or clear its busy', async () => {
  const w = await open(); await finish(); await page(w, 2, 20); const oldPage = calls.at(-1)
  await page(w, 3, 50); const current = calls.at(-1); await finish(oldPage, reply('SYNTHETIC_OLD_PAGE', A, { page: 2, pageSize: 20 }))
  assert.equal(w.vm.pagination.current, 3); assert.equal(w.vm.pagination.pageSize, 50); assert.equal(w.vm.tableLoading, true)
  await finish(current, reply('SYNTHETIC_THIRD_PAGE', A, { page: 3, pageSize: 50 })); await refresh(w); const oldRefresh = calls.at(-1)
  await search(w, 'SYNTHETIC_NEW_QUERY'); const next = calls.at(-1); oldRefresh.reject(new Error(PRIVATE)); await settle()
  assert.equal(w.vm.tableLoading, true); assert.equal(w.vm.filters.keyword, 'SYNTHETIC_NEW_QUERY'); await finish(next, reply('SYNTHETIC_NEW_QUERY_ORDER')); assert.equal(w.vm.dataSource[0].orderNo, 'SYNTHETIC_NEW_QUERY_ORDER'); noPrivateFeedback()
})
for (const transition of ['actor', 'ABA', 'role', 'status', 'logout']) test('RECHARGE synchronous ' + transition + ' retirement erases private state/native DOM and rejects old render/load results', async () => {
  const w = await open(); await finish(calls[0], reply('SYNTHETIC_PRIVATE_OLD_ORDER')); await search(w, 'SYNTHETIC_PRIVATE_FILTER'); await finish(calls.at(-1), reply('SYNTHETIC_PRIVATE_OLD_ORDER'))
  await refresh(w); const old = calls.at(-1), oldSearch = callback(w.findComponent(Antd.InputSearch), 'onSearch'), oldPage = callback(w.findComponent(Antd.Table), 'onChange'), oldRefresh = button(w, '刷新').element, input = w.get('input[placeholder="订单号 / 第三方单号 / 用户 / 支付方式"]').element
  const actor = transition === 'actor' || transition === 'ABA' ? B : transition === 'role' ? { ...A, role: 'admin' } : transition === 'status' ? { ...A, status: 'disabled' } : null
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  assert.equal(w.vm.dataSource.length, 0, 'SYNC_STATE_ERASE'); assert.ok(!w.element.textContent.includes('SYNTHETIC_PRIVATE_OLD_ORDER'), 'SYNC_NATIVE_DOM_ERASE'); assert.equal(input.value, '', 'SYNC_PRIVATE_NATIVE_INPUT_ERASE')
  const count = calls.length; oldSearch('SYNTHETIC_STALE_RENDER'); oldPage({ current: 8, pageSize: 100 }); oldRefresh.click(); assert.equal(calls.length, count, 'RETIRED_NATIVE_CALLBACKS_CANNOT_READ_FOR_NEW_ACTOR')
  if (transition === 'ABA') user.setStoredCurrentUser(A)
  await settle(); const current = calls.at(-1), before = view(w), noticeCount = notices.length
  old.resolve(reply('SYNTHETIC_RETIRED_ACTOR_ORDER', A, { total: 999, page: 9, summary: { totalAmount: 999, currentPageAmount: 999 } })); await settle()
  assert.deepEqual(view(w), before); assert.equal(notices.length, noticeCount); assert.ok(!w.text().includes('SYNTHETIC_RETIRED_ACTOR_ORDER'))
  if (transition === 'logout') { assert.equal(calls.length, count, 'LOGOUT_CANNOT_START_NEW_READ'); emptyState(w) }
  else if (current !== old) { await finish(current, reply('SYNTHETIC_NEW_ACTOR_ORDER', transition === 'ABA' ? A : actor)); assert.equal(w.vm.tableLoading, false) }
})
test('RECHARGE same-actor metadata preserves current rows draft and pending GET without a replacement authentication read', async () => {
  const w = await open(); await finish(); await refresh(w); const issued = calls.at(-1)
  await w.get('input[placeholder="订单号 / 第三方单号 / 用户 / 支付方式"]').setValue('SYNTHETIC_UNSAVED_DRAFT'); const before = view(w), count = calls.length
  user.setStoredCurrentUser({ ...A, balance: 0.987654, nickname: 'SYNTHETIC_UPDATED_DISPLAY' }); await settle()
  assert.equal(calls.length, count); assert.equal(w.vm.keywordInput, 'SYNTHETIC_UNSAVED_DRAFT'); assert.deepEqual(view(w), before)
  await finish(issued, reply('SYNTHETIC_METADATA_VALID_ORDER')); assert.equal(w.vm.dataSource[0].orderNo, 'SYNTHETIC_METADATA_VALID_ORDER'); assert.equal(w.vm.tableLoading, false)
})
for (const outcome of ['success', 'error']) test('RECHARGE route retirement rejects late ' + outcome + ' without navigation or stale native reads', async () => {
  const w = await open(); await finish(calls[0], reply('SYNTHETIC_PRIVATE_ROUTE_ORDER')); await refresh(w); const old = calls.at(-1), oldSearch = callback(w.findComponent(Antd.InputSearch), 'onSearch'), input = w.get('input[placeholder="订单号 / 第三方单号 / 用户 / 支付方式"]').element
  w.testRoute.path = '/ordinary-next'; w.testRoute.fullPath = '/ordinary-next'
  assert.equal(w.vm.dataSource.length, 0); assert.ok(!w.element.textContent.includes('SYNTHETIC_PRIVATE_ROUTE_ORDER')); assert.equal(input.value, '')
  const count = calls.length; oldSearch('SYNTHETIC_STALE_ROUTE'); assert.equal(calls.length, count)
  if (outcome === 'success') old.resolve(reply('SYNTHETIC_ROUTE_RETIRED_ORDER')); else old.reject(new Error(PRIVATE))
  await settle(); emptyState(w); assert.equal(w.vm.tableLoading, false); assert.equal(calls.length, count); noPrivateFeedback()
  w.unmount(); await settle(); assert.equal(calls.length, count)
})
for (const outcome of ['success', 'error']) test('RECHARGE unmounted instance late ' + outcome + '/finally cannot contaminate a new mounted instance', async () => {
  const oldView = await open(), old = calls[0], oldSearch = callback(oldView.findComponent(Antd.InputSearch), 'onSearch'), oldPage = callback(oldView.findComponent(Antd.Table), 'onChange')
  oldView.unmount(); await settle(); const currentView = await open(A, false), current = calls.at(-1), before = view(currentView), count = calls.length, noticeCount = notices.length
  oldSearch('SYNTHETIC_UNMOUNTED_QUERY'); oldPage({ current: 9, pageSize: 100 }); assert.equal(calls.length, count)
  if (outcome === 'success') old.resolve(reply('SYNTHETIC_UNMOUNTED_ORDER', A, { page: 9, pageSize: 100, summary: { totalAmount: 999, currentPageAmount: 999 } })); else old.reject(new Error(PRIVATE))
  await settle(); assert.deepEqual(view(currentView), before); assert.equal(currentView.vm.tableLoading, true); assert.equal(notices.length, noticeCount); assert.ok(!document.body.textContent.includes('SYNTHETIC_UNMOUNTED_ORDER')); noPrivateFeedback()
  await finish(current, reply('SYNTHETIC_CURRENT_MOUNT_ORDER')); assert.equal(currentView.vm.dataSource[0].orderNo, 'SYNTHETIC_CURRENT_MOUNT_ORDER'); assert.equal(currentView.vm.tableLoading, false)
})
for (const outcome of ['response', 'transport']) test('RECHARGE current GET ' + outcome + ' failure remains safe and persistent until explicit native retry succeeds', async () => {
  const w = await open(), read = calls[0]
  if (outcome === 'response') read.resolve({ status: 503, message: PRIVATE, data: { callbackPayload: PRIVATE } }); else read.reject(Object.assign(new Error(PRIVATE), { response: { status: 503, data: { message: PRIVATE } } }))
  await settle(); assert.ok(failure(w), 'PERSISTENT_NATIVE_FAILURE_ALERT'); assert.match(failure(w).text(), /充值记录|读取|加载/); assert.equal(w.vm.tableLoading, false); noPrivateFeedback()
  assert.ok(!w.text().includes('当前还没有充值记录。'), 'FAILED_GET_IS_NOT_SUCCESSFUL_EMPTY')
  await settle(); assert.equal(calls.length, 1, 'NO_AUTOMATIC_RETRY'); assert.ok(failure(w))
  const retry = w.get('[data-action="retry-request"]'); assert.equal(retry.element.type, 'button'); retry.element.click(); await settle(); assert.equal(calls.length, 2); assert.deepEqual(calls[1].payload, calls[0].payload); assert.equal(w.vm.tableLoading, true)
  await finish(calls[1], reply('SYNTHETIC_RECOVERED_READ')); assert.equal(Boolean(failure(w)), false); assert.equal(w.vm.dataSource[0].orderNo, 'SYNTHETIC_RECOVERED_READ'); assert.equal(w.vm.tableLoading, false)
})
test('RECHARGE failed newer filter keeps recovery explanation and never relabels old successful rows as an empty result', async () => {
  const w = await open(); await finish(); await search(w, 'SYNTHETIC_FAILED_QUERY'); calls.at(-1).resolve({ status: 500, message: PRIVATE }); await settle()
  assert.ok(failure(w)); assert.equal(w.vm.filters.keyword, 'SYNTHETIC_FAILED_QUERY'); assert.ok(!w.text().includes('当前还没有充值记录。')); noPrivateFeedback()
  const count = calls.length; await refresh(w); assert.equal(calls.length, count + 1); assert.deepEqual(calls.at(-1).payload, { keyword: 'SYNTHETIC_FAILED_QUERY', page: 1, pageSize: 10 })
  await finish(calls.at(-1), reply('SYNTHETIC_FILTER_RECOVERED')); assert.equal(Boolean(failure(w)), false); assert.equal(w.vm.dataSource[0].orderNo, 'SYNTHETIC_FILTER_RECOVERED')
})
test('RECHARGE same-query refresh failure explicitly labels any retained rows and totals as the previous successful result', async () => {
  const w = await open(); await finish(calls[0], reply('SYNTHETIC_LAST_CONFIRMED_ORDER')); const confirmed = view(w)
  await refresh(w); calls.at(-1).reject(new Error(PRIVATE)); await settle(); assert.ok(failure(w)); assert.equal(w.vm.tableLoading, false)
  if (w.vm.dataSource.length) {
    assert.deepEqual(view(w), confirmed, 'RETAINED_ROWS_SUMMARY_PAGE_ARE_THE_LAST_CONFIRMED_RESULT')
    assert.match(failure(w).text(), /上次/); assert.match(failure(w).text(), /刷新.*(?:未成功|失败)/)
  }
  assert.ok(!w.text().includes('当前还没有充值记录。')); noPrivateFeedback(); const count = calls.length; await settle(); assert.equal(calls.length, count, 'REFRESH_FAILURE_DOES_NOT_AUTO_REISSUE_GET')
  w.get('[data-action="retry-request"]').element.click(); await settle(); assert.equal(calls.length, count + 1)
  await finish(calls.at(-1), reply('SYNTHETIC_CURRENT_CONFIRMED_ORDER')); assert.equal(Boolean(failure(w)), false); assert.equal(w.vm.dataSource[0].orderNo, 'SYNTHETIC_CURRENT_CONFIRMED_ORDER')
})
test('RECHARGE confirmed empty succeeds with real zero totals and no failure; missing/malformed summaries stay explicitly unknown', async () => {
  const w = await open(); await finish(calls[0], reply(undefined, A, { data: [], total: 0, summary: { totalAmount: 0, currentPageAmount: 0 } }))
  emptyState(w); assert.ok(w.text().includes('当前还没有充值记录。')); assert.equal(Boolean(failure(w)), false); assert.equal(statistic(w, '累计充值金额（当前筛选，全部页）'), '¥0.00')
  for (const summary of [undefined, null, {}, { totalAmount: null, currentPageAmount: null }, { totalAmount: 'not-money', currentPageAmount: {} }, { totalAmount: -1, currentPageAmount: -1 }]) {
    await refresh(w); await finish(calls.at(-1), reply('SYNTHETIC_VALID_ROW_UNKNOWN_SUMMARY', A, { summary })); assert.equal(w.vm.dataSource[0]?.orderNo, 'SYNTHETIC_VALID_ROW_UNKNOWN_SUMMARY'); amountUnknown(w)
  }
})
test('RECHARGE malformed successful list structure is a recoverable failure rather than fabricated empty success', async () => {
  const w = await open()
  for (const data of [undefined, null, {}, { data: PRIVATE, total: 0, summary: { totalAmount: 0, currentPageAmount: 0 } }]) {
    if (calls.at(-1).completed) await refresh(w)
    await finish(calls.at(-1), { status: 200, data }); assert.ok(failure(w), 'MALFORMED_LIST_IS_EXPLICIT_FAILURE'); assert.ok(!w.text().includes('当前还没有充值记录。')); noPrivateFeedback()
  }
  await refresh(w); await finish(calls.at(-1), reply('SYNTHETIC_MALFORMED_RECOVERED')); assert.equal(Boolean(failure(w)), false); assert.equal(w.vm.dataSource[0].orderNo, 'SYNTHETIC_MALFORMED_RECOVERED')
})
test('RECHARGE explicit logout with denied cache removal cannot reissue a GET from still-readable stale A or paint late success/error', async () => {
  const w = await open(), oldSuccess = calls[0]
  await search(w, 'SYNTHETIC_PRIVATE_LOGOUT_FILTER'); await finish(calls.at(-1), reply('SYNTHETIC_PRIVATE_LOGOUT_ORDER'))
  await refresh(w); const oldError = calls.at(-1), oldSearch = callback(w.findComponent(Antd.InputSearch), 'onSearch'), oldPage = callback(w.findComponent(Antd.Table), 'onChange'), oldRefresh = button(w, '刷新').element, input = w.get('input[placeholder="订单号 / 第三方单号 / 用户 / 支付方式"]').element
  const storagePrototype = Object.getPrototypeOf(window.localStorage), original = Object.getOwnPropertyDescriptor(storagePrototype, 'removeItem'), logoutEpoch = user.getCurrentUserLogoutEpoch(), count = calls.length
  assert.equal(typeof original?.value, 'function', 'ACTUAL_NATIVE_STORAGE_REMOVE_DESCRIPTOR')
  try {
    Object.defineProperty(storagePrototype, 'removeItem', { ...original, value(key) { if (key === 'adapter_current_user') throw new Error('SYNTHETIC_STORAGE_REMOVE_DENIED'); return original.value.call(this, key) } })
    user.clearStoredCurrentUser()
    assert.equal(user.getStoredCurrentUser()?.id, A.id, 'REMOVAL_DENIED_BUT_STALE_A_REMAINS_READABLE')
    assert.equal(user.getCurrentUserLogoutEpoch(), logoutEpoch + 1, 'EXPLICIT_LOGOUT_SIGNAL_IS_STILL_AUTHORITATIVE')
    emptyState(w); assert.equal(w.vm.tableLoading, false); assert.ok(!w.element.textContent.includes('SYNTHETIC_PRIVATE_LOGOUT_ORDER'), 'SYNC_LOGOUT_ERASES_PRIVATE_NATIVE_DOM'); assert.equal(input.value, '')
    oldSearch('SYNTHETIC_STALE_LOGOUT_SEARCH'); oldPage({ current: 9, pageSize: 100 }); oldRefresh.click(); assert.equal(calls.length, count, 'RETIRED_LOGOUT_CALLBACKS_CANNOT_DISPATCH')
    await settle(); assert.equal(calls.length, count, 'EXPLICIT_LOGOUT_MUST_NOT_AUTO_RELOAD_STALE_READABLE_A'); emptyState(w); assert.equal(w.vm.tableLoading, false)
    oldSuccess.resolve(reply('SYNTHETIC_LOGOUT_LATE_SUCCESS', A, { total: 999, summary: { totalAmount: 999, currentPageAmount: 999 } })); oldError.reject(new Error(PRIVATE))
    await settle(); emptyState(w); assert.equal(w.vm.tableLoading, false); assert.equal(calls.length, count); assert.equal(Boolean(failure(w)), false); assert.ok(!document.body.textContent.includes('SYNTHETIC_LOGOUT_LATE_SUCCESS')); noPrivateFeedback()
  } finally {
    Object.defineProperty(storagePrototype, 'removeItem', original)
  }
})
