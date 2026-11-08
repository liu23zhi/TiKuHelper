/* Actual BillingRecordList/Vue/Antd/currentUser. Only synthetic GET transports are mocked.
 * A Page module is fresh per independent case, but twins and unmount/handoff
 * deliberately use the SAME loaded Page module. No production source shim.
 * TIKU_WEB_SOURCE/UI_TEST_HELPER bind unchanged native B/M/R source versions.
 */
const { test, beforeEach, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const name of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[name] = window[name]
const { createRouter, createMemoryHistory } = require('vue-router')
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, defineComponent, ref, h } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 98001, username: 'SYNTHETIC_BILLING_ADMIN_A', role: 'admin' }, B = { id: 98002, username: 'SYNTHETIC_BILLING_ADMIN_B', role: 'admin' }, ordinary = { id: 98003, username: 'SYNTHETIC_BILLING_USER', role: 'user' }
const IP = '198.51.100.7', roots = [], calls = [], ipCalls = [], writes = [], network = [], errors = []
let Page, autoBills = true, autoIps = true
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
const file = path.join(web, 'src/pages/BillingRecordList/index.vue'), initialSHA = crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
function sha() { return crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex') }
function trap(kind) { network.push(kind); throw new Error('FORBIDDEN_BILLING_NATIVE_IO_' + kind) }
function row(marker, id = 101, amount = 0.123456, shown = false) { return { id, userId: A.id, username: 'SYNTHETIC_BILLING_OWNER', type: 'normal_search', direction: 'expense', amount, questionText: marker, subject: marker, provider: 'ai', sourceLabel: 'SYNTHETIC_PROVIDER', clientIp: IP, showInStatement: shown, requestId: 'SYNTHETIC_REQUEST_' + id, createdAt: '2026-10-29T01:02:03Z', feeDetails: [{ provider: 'ai', category: 'AI模型调用', amount, detail: 'SYNTHETIC_DETAIL' }] } }
function response(marker, params = {}, total = 100, amount = 0.123456) { return { status: 200, data: { data: [row(marker, 101, amount)], total, page: params.page || 1, pageSize: params.pageSize || 10, summary: { totalAmount: amount, currentPageAmount: amount, currentPageCacheHitRate: 0.125, totalCacheHitRate: 0.375 } } } }
function deferredRecord(list, params) { const record = { params: clone(params), actor: clone(user.getStoredCurrentUser()), completed: false }; const promise = new Promise((resolve, reject) => { record.resolve = value => { record.completed = true; resolve(value) }; record.reject = value => { record.completed = true; reject(value) } }); list.push(record); return { record, promise } }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
  getBillingRecords(params) { const { record, promise } = deferredRecord(calls, params); if (autoBills) record.resolve(response('SYNTHETIC_DEFAULT_ROW_' + calls.length, params)); return promise },
  getIpLocations(params) { const { record, promise } = deferredRecord(ipCalls, params); record.kind = 'admin'; if (autoIps) record.resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_DEFAULT_LOCATION' } } }); return promise },
  getMyLocations(params) { const { record, promise } = deferredRecord(ipCalls, params); record.kind = 'self'; if (autoIps) record.resolve({ status: 200, data: { ip: { [IP]: { address: 'SYNTHETIC_SELF_LOCATION' } } } }); return promise },
  ...Object.fromEntries(['updateBillingStatementVisibility', 'createRechargeOrder', 'createQuestions', 'addUser', 'updateUser', 'delUser', 'adjustUserBalance', 'updateSystemSettings'].map(name => [name, () => { writes.push(name); throw new Error('FORBIDDEN_BILLING_WRITE_' + name) }])),
}; require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return trap('XHR') }; globalThis.fetch = window.fetch = () => trap('FETCH'); window.open = () => trap('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { trap('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => trap(name); transport.get = () => trap(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function snapshot(page) { return { rows: clone(page.vm.dataSource), summary: clone(page.vm.summary), keywordInput: page.vm.keywordInput, filters: clone(page.vm.filters), page: page.vm.pagination.current, pageSize: page.vm.pagination.pageSize, total: page.vm.pagination.total, loading: page.vm.tableLoading, failure: Boolean(page.vm.listFailure), locations: clone(page.vm.ipLocations), nativeKeyword: page.element.querySelector('input[placeholder="题目 / 请求 ID / 用户 / 渠道"]')?.value, tableText: page.element.querySelector('.desktop-billing-table')?.textContent, mobileText: page.element.querySelector('.mobile-billing-list')?.textContent } }
function observe(label, pages) { console.log('BILLING_INSTANCE_NATIVE_DOM ' + JSON.stringify({ label, pages: pages.map(snapshot), GET: calls.map(x => ({ params: x.params, actorID: x.actor?.id, completed: x.completed })), IPGET: ipCalls.map(x => ({ params: x.params, actorID: x.actor?.id, completed: x.completed })) })) }
function button(page, text) { const found = [...page.element.querySelectorAll('button')].find(node => compact(node.textContent).replace(/\s/g, '') === text.replace(/\s/g, '')); assert.ok(found, 'actual native button ' + text); return found }
function keyword(page) { return page.get('input[placeholder="题目 / 请求 ID / 用户 / 渠道"]') }
async function open(twins = false) {
  const showFirst = ref(true)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/billing', component: { render: () => null } }, { path: '/other', component: { render: () => null } }] }); await router.push('/billing'); await router.isReady()
  const Root = twins ? defineComponent({ setup() { return () => h('section', [h('button', { onClick: () => { showFirst.value = false }, 'data-fixture-unmount-first': '' }, 'Unmount first fixture view'), ...(showFirst.value ? [h(Page, { key: 'first' })] : []), h(Page, { key: 'second' })]) } }) : Page
  const root = mount(Root, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); root.fixtureRouter = router; roots.push(root); await settle()
  const pages = twins ? root.findAllComponents(Page) : [root]; assert.equal(pages.length, twins ? 2 : 1)
  return { root, pages }
}
beforeEach(() => { autoBills = autoIps = true; delete require.cache[file]; Page = require(file); user.setStoredCurrentUser(A) })
afterEach(async () => {
  for (const root of roots.splice(0)) { if (root.exists()) root.unmount(); root.fixtureRouter?.options.history.destroy() }
  for (const call of calls) if (!call.completed) call.resolve({ status: 503 })
  for (const call of ipCalls) if (!call.completed) call.resolve({ status: 200, data: {} })
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('BILLING_INSTANCE_NATIVE_SAFETY ' + JSON.stringify({ sourceSHA256: sha(), GETReads: calls.length, IPReads: ipCalls.length, writes, realIO: network, runtimeErrors: errors }))
  assert.equal(sha(), initialSHA); assert.deepEqual(writes, []); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  calls.length = ipCalls.length = writes.length = network.length = errors.length = 0
})
test('one owned twin response only publishes its own rows and monetary summary while sibling stays busy', async () => {
  autoBills = false; const { pages: [first, second] } = await open(true); assert.equal(calls.length, 2)
  calls[0].resolve(response('SYNTHETIC_FIRST_BILLING_ROWS', calls[0].params, 100, 1.234567)); await settle(); observe('first response isolated', [first, second])
  assert.ok(first.text().includes('SYNTHETIC_FIRST_BILLING_ROWS')); assert.ok(!second.text().includes('SYNTHETIC_FIRST_BILLING_ROWS'), 'first rows cannot appear in sibling real DOM')
  assert.equal(first.vm.summary.totalAmount, 1.234567); assert.equal(second.vm.summary.totalAmount, 0); assert.equal(second.vm.tableLoading, true)
  calls[1].resolve(response('SYNTHETIC_SECOND_BILLING_ROWS', calls[1].params, 200, 2.345678)); await settle(); assert.ok(!first.text().includes('SYNTHETIC_SECOND_BILLING_ROWS')); assert.equal(first.vm.summary.totalAmount, 1.234567); assert.equal(second.vm.summary.totalAmount, 2.345678)
})
test('finishing one twin read cannot release sibling actual loading spinner', async () => {
  autoBills = false; const { pages: [first, second] } = await open(true); calls[0].resolve(response('SYNTHETIC_DONE')); await settle(); observe('independent busy', [first, second])
  assert.equal(first.vm.tableLoading, false); assert.equal(second.vm.tableLoading, true); assert.ok(second.element.querySelector('.desktop-billing-table .ant-spin-spinning'))
})
test('native draft typed in first billing view does not appear in sibling or trigger a GET', async () => {
  const { pages: [first, second] } = await open(true); const count = calls.length; await keyword(first).setValue('SYNTHETIC_FIRST_PRIVATE_DRAFT'); await settle(); observe('independent draft', [first, second])
  assert.equal(keyword(first).element.value, 'SYNTHETIC_FIRST_PRIVATE_DRAFT'); assert.equal(keyword(second).element.value, ''); assert.equal(second.vm.keywordInput, ''); assert.equal(calls.length, count)
})
test('actual type filter event only changes one accepted query', async () => {
  const { pages: [first, second] } = await open(true); const select = first.findAllComponents(Antd.Select).find(value => value.element.closest('.filter-form')); select.vm.$emit('update:value', 'recharge'); select.vm.$emit('change', 'recharge'); await settle(); observe('independent type filter', [first, second])
  assert.deepEqual(calls.at(-1).params, { type: 'recharge', page: 1, pageSize: 10 }); assert.equal(second.vm.filters.type, undefined); assert.equal(second.vm.activeFilterCount, 0)
})
test('actual mobile pagination event changes only its own current page and pageSize', async () => {
  const { pages: [first, second] } = await open(true); first.find('.mobile-billing-pagination').findComponent(Antd.Pagination).vm.$emit('change', 3, 20); await settle(); observe('independent paging', [first, second])
  assert.deepEqual(calls.at(-1).params, { page: 3, pageSize: 20 }); assert.equal(first.vm.pagination.current, 3); assert.equal(first.vm.pagination.pageSize, 20); assert.equal(second.vm.pagination.current, 1); assert.equal(second.vm.pagination.pageSize, 10)
})
test('resetting first view cannot erase sibling applied query rows or totals', async () => {
  const { pages: [first, second] } = await open(true); await keyword(second).setValue('SYNTHETIC_SECOND_APPLIED_QUERY'); second.get('button.ant-input-search-button').element.click(); await settle(); const marker = second.vm.dataSource[0].questionText; const amount = second.vm.summary.totalAmount
  button(first, '重置筛选').click(); await settle(); observe('independent reset', [first, second])
  assert.equal(keyword(second).element.value, 'SYNTHETIC_SECOND_APPLIED_QUERY'); assert.equal(second.vm.filters.keyword, 'SYNTHETIC_SECOND_APPLIED_QUERY'); assert.ok(second.text().includes(marker)); assert.equal(second.vm.summary.totalAmount, amount); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 10 })
})
test('actually unmounting first twin cannot clear survivor rows monetary totals and locations', async () => {
  const { root, pages: [, second] } = await open(true); const marker = second.vm.dataSource[0].questionText; assert.ok(second.text().includes(marker)); assert.equal(second.vm.ipLocations[IP], 'SYNTHETIC_DEFAULT_LOCATION')
  root.get('[data-fixture-unmount-first]').element.click(); await settle(); observe('survivor after actual sibling unmount', [second])
  assert.ok(second.text().includes(marker)); assert.equal(second.vm.summary.totalAmount, 0.123456); assert.equal(second.vm.summary.total, 100); assert.equal(second.vm.summary.currentPageCount, 1); assert.equal(second.vm.ipLocations[IP], 'SYNTHETIC_DEFAULT_LOCATION')
})
test('a first deferred IP result cannot update sibling location display or map', async () => {
  autoIps = false; const { pages: [first, second] } = await open(true); assert.equal(ipCalls.length, 2); ipCalls[0].resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_FIRST_ONLY_LOCATION' } } }); await settle(); observe('independent location callback', [first, second])
  assert.ok(first.text().includes('SYNTHETIC_FIRST_ONLY_LOCATION')); assert.ok(!second.text().includes('SYNTHETIC_FIRST_ONLY_LOCATION')); assert.equal(second.vm.ipLocations[IP], undefined)
  ipCalls[1].resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_SECOND_ONLY_LOCATION' } } }); await settle(); assert.ok(!first.text().includes('SYNTHETIC_SECOND_ONLY_LOCATION')); assert.ok(second.text().includes('SYNTHETIC_SECOND_ONLY_LOCATION'))
})
test('a first page reload cannot replace an unchanged sibling monetary summary', async () => {
  const { pages: [first, second] } = await open(true); const original = clone(second.vm.summary); autoBills = false; button(first, '刷新').click(); await settle(); calls.at(-1).resolve(response('SYNTHETIC_FIRST_NEW_TOTAL', {}, 250, 7.654321)); await settle(); observe('independent six-digit summary', [first, second])
  assert.equal(first.vm.summary.totalAmount, 7.654321); assert.deepEqual(clone(second.vm.summary), original); assert.ok(!second.text().includes('SYNTHETIC_FIRST_NEW_TOTAL'))
})
test('first failed read feedback does not change healthy sibling totals rows or busy status', async () => {
  const { pages: [first, second] } = await open(true); const original = snapshot(second); autoBills = false; button(first, '刷新').click(); await settle(); calls.at(-1).reject(new Error('SYNTHETIC_FIRST_GET_FAILURE')); await settle(); observe('independent read failure', [first, second])
  assert.ok(first.vm.listFailure); assert.equal(second.vm.listFailure, null); assert.deepEqual(snapshot(second), original); assert.ok(!second.element.querySelector('[data-action="retry-request"]'))
})
test('A query type page and pageSize never enter freshly mounted B after all A views unmount in same Page module', async () => {
  const { root, pages: [first] } = await open(); await keyword(first).setValue('SYNTHETIC_PRIVATE_A_BILLING_QUERY'); first.get('button.ant-input-search-button').element.click(); await settle(); const select = first.findAllComponents(Antd.Select).find(value => value.element.closest('.filter-form')); select.vm.$emit('update:value', 'balance_adjustment'); select.vm.$emit('change', 'balance_adjustment'); await settle(); first.find('.mobile-billing-pagination').findComponent(Antd.Pagination).vm.$emit('change', 3, 20); await settle(); assert.equal(calls.at(-1).params.keyword, 'SYNTHETIC_PRIVATE_A_BILLING_QUERY')
  root.unmount(); user.setStoredCurrentUser(B); const count = calls.length; const { pages: [second] } = await open(); observe('all-unmounted owner handoff', [second])
  assert.equal(calls.length, count + 1); assert.equal(calls.at(-1).actor.id, B.id); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 10 }); assert.equal(keyword(second).element.value, ''); assert.equal(second.vm.filters.keyword, ''); assert.equal(second.vm.filters.type, undefined)
})
test('single admin first GET hidden row six-digit summary and original filters/pagination remain', async () => {
  const { pages: [page] } = await open(); assert.deepEqual(calls[0].params, { page: 1, pageSize: 10 }); assert.equal(page.vm.dataSource[0].showInStatement, false); assert.equal(page.vm.summary.totalAmount, 0.123456); assert.equal(page.vm.summary.currentPageAmount, 0.123456); assert.equal(page.vm.summary.totalCacheHitRate, 0.375); assert.ok(page.text().includes('0.123456')); assert.ok(page.text().includes('管理员始终可查看全部账单')); assert.equal(page.vm.tableLoading, false)
  for (const key of ['normal_search', 'recharge', 'registration_bonus', 'balance_adjustment']) assert.ok(page.vm.typeOptions.some(value => value.value === key))
  await keyword(page).setValue('  SYNTHETIC_SINGLE%_  '); assert.equal(calls.length, 1); page.get('button.ant-input-search-button').element.click(); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_SINGLE%_', page: 1, pageSize: 10 }); assert.equal(keyword(page).element.value, 'SYNTHETIC_SINGLE%_')
  const select = page.findAllComponents(Antd.Select).find(value => value.element.closest('.filter-form')); select.vm.$emit('update:value', 'normal_search'); select.vm.$emit('change', 'normal_search'); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_SINGLE%_', type: 'normal_search', page: 1, pageSize: 10 }); page.find('.mobile-billing-pagination').findComponent(Antd.Pagination).vm.$emit('change', 2, 20); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_SINGLE%_', type: 'normal_search', page: 2, pageSize: 20 })
  button(page, '重置筛选').click(); await settle(); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 20 }); assert.equal(keyword(page).element.value, ''); observe('single six-digit query positive', [page])
})
test('ordinary own-view GET uses self location endpoint and no admin columns or visibility write control', async () => {
  user.setStoredCurrentUser(ordinary); autoBills = false; const { pages: [page] } = await open(); assert.equal(calls.length, 1); assert.equal(calls[0].actor.id, ordinary.id); assert.deepEqual(calls[0].params, { page: 1, pageSize: 10 })
  const own = response('SYNTHETIC_ORDINARY_OWN_ROW'); own.data.data[0].userId = ordinary.id; own.data.data[0].username = ordinary.username; own.data.data[0].showInStatement = true; calls[0].resolve(own); await settle(); observe('ordinary read-only view', [page])
  assert.equal(page.vm.isAdmin, false); assert.ok(page.text().includes('我的账单')); assert.ok(page.text().includes('SYNTHETIC_ORDINARY_OWN_ROW')); assert.deepEqual(ipCalls.map(value => value.kind), ['self']); assert.equal(page.vm.ipLocations[IP], 'SYNTHETIC_SELF_LOCATION'); assert.ok(!page.vm.columns.some(value => ['effectiveMultiplier', 'clientIp'].includes(value.key))); assert.equal(await page.vm.handleStatementVisibilityChange({ id: 101, showInStatement: false }), false); assert.equal(writes.length, 0)
})
test('unconfirmed identity makes no billing or location GET and renders no private records', async () => {
  user.clearStoredCurrentUser(); const { pages: [page] } = await open(); observe('unconfirmed no read', [page]); assert.equal(calls.length, 0); assert.equal(ipCalls.length, 0); assert.equal(page.vm.dataSource.length, 0); assert.equal(page.vm.summary.totalAmount, 0); assert.equal(page.vm.tableLoading, false)
})
test('selected billing detail remains the opened record when a later read replaces list rows', async () => {
  const { pages: [page] } = await open(); const opened = clone(page.vm.dataSource[0]); const nativeRow = page.element.querySelector('.desktop-billing-table tr[data-row-key="101"]'); assert.ok(nativeRow); const detailButton = [...nativeRow.querySelectorAll('button')].find(value => compact(value.textContent) === '查看详情'); assert.ok(detailButton); detailButton.click(); await settle(); assert.equal(page.vm.billingDetailOpen, true)
  const detail = page.findComponent({ name: 'BillingDetailDialog' }); assert.ok(detail.exists()); assert.equal(detail.props('viewerScope'), 'admin'); assert.equal(detail.props('canEditStatement'), true); assert.equal(detail.props('record').showInStatement, false); assert.ok(document.querySelector('.billing-detail')?.textContent.includes(opened.questionText))
  autoBills = false; button(page, '刷新').click(); await settle(); calls.at(-1).resolve(response('SYNTHETIC_NEW_LIST_NOT_SELECTED', {}, 12, 4.654321)); await settle(); observe('selected old snapshot after list refresh', [page])
  assert.equal(page.vm.dataSource[0].questionText, 'SYNTHETIC_NEW_LIST_NOT_SELECTED'); assert.deepEqual(clone(page.vm.selectedBillingRecord), opened); assert.equal(detail.props('record').amount, 0.123456); assert.ok(!document.querySelector('.billing-detail')?.textContent.includes('SYNTHETIC_NEW_LIST_NOT_SELECTED')); assert.equal(writes.length, 0)
})
test('invalid nonboolean visibility input remains rejected locally before any write', async () => {
  const { pages: [page] } = await open(); const before = clone(page.vm.dataSource)
  for (const value of ['false', 0, undefined, null]) assert.equal(await page.vm.handleStatementVisibilityChange({ id: 101, showInStatement: value }), false)
  assert.equal(writes.length, 0); assert.deepEqual(clone(page.vm.dataSource), before); assert.equal(page.vm.statementFailure, null)
})
test('single location rejection remains fail-soft without changing rows monetary summary or list failure', async () => {
  autoIps = false; const { pages: [page] } = await open(); const before = clone(page.vm.summary); const marker = page.vm.dataSource[0].questionText; ipCalls[0].reject(new Error('SYNTHETIC_IP_LOOKUP_UNAVAILABLE')); await settle(); observe('location fail soft', [page]); assert.ok(page.text().includes(marker)); assert.ok(page.text().includes(IP)); assert.deepEqual(clone(page.vm.summary), before); assert.equal(page.vm.listFailure, null); assert.equal(page.vm.tableLoading, false); assert.deepEqual(clone(page.vm.ipLocations), {})
})
test('latest query keeps rows monetary totals and busy guard against older completion and IP result', async () => {
  autoIps = false; const { pages: [page] } = await open(); const oldIp = ipCalls[0]; autoBills = false; button(page, '刷新').click(); await settle(); const old = calls.at(-1); await keyword(page).setValue('SYNTHETIC_LATEST_QUERY'); page.get('button.ant-input-search-button').element.click(); await settle(); const fresh = calls.at(-1)
  fresh.resolve(response('SYNTHETIC_LATEST_BILLING_ROW', fresh.params, 9, 3.456789)); await settle(); old.resolve(response('SYNTHETIC_OLD_BILLING_ROW', old.params, 99, 8.765432)); oldIp.resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_OLD_IP_ADDRESS' } } }); await settle(); observe('single latest read guard', [page]); assert.ok(page.text().includes('SYNTHETIC_LATEST_BILLING_ROW')); assert.ok(!page.text().includes('SYNTHETIC_OLD_BILLING_ROW')); assert.ok(!page.text().includes('SYNTHETIC_OLD_IP_ADDRESS')); assert.equal(page.vm.summary.totalAmount, 3.456789); assert.equal(page.vm.pagination.total, 9); assert.equal(page.vm.tableLoading, false)
})
test('live actor change clears A query and ignores old row and monetary completion while B reads defaults', async () => {
  const { pages: [page] } = await open(); await keyword(page).setValue('SYNTHETIC_LIVE_A_QUERY'); autoBills = false; page.get('button.ant-input-search-button').element.click(); await settle(); const old = calls.at(-1); user.setStoredCurrentUser(B); await settle(); const fresh = calls.at(-1); assert.equal(fresh.actor.id, B.id); assert.deepEqual(fresh.params, { page: 1, pageSize: 10 }); fresh.resolve(response('SYNTHETIC_LIVE_B_BILL', fresh.params, 3, 1.111111)); await settle(); old.resolve(response('SYNTHETIC_LIVE_A_LATE', old.params, 99, 9.999999)); await settle(); observe('live owner existing guard', [page]); assert.ok(page.text().includes('SYNTHETIC_LIVE_B_BILL')); assert.ok(!page.text().includes('SYNTHETIC_LIVE_A_LATE')); assert.equal(page.vm.summary.totalAmount, 1.111111); assert.equal(keyword(page).element.value, '')
})
test('late response after actual unmount remains retired and cannot publish rows or summary', async () => {
  autoBills = false; const { root, pages: [page] } = await open(); const request = calls[0]; root.unmount(); request.resolve(response('SYNTHETIC_AFTER_UNMOUNT_BILL', {}, 999, 9.999999)); await settle(); assert.ok(!document.body.textContent.includes('SYNTHETIC_AFTER_UNMOUNT_BILL')); assert.equal(page.vm.dataSource.length, 0); assert.equal(page.vm.summary.totalAmount, 0); assert.equal(user.getStoredCurrentUser().id, A.id)
})
