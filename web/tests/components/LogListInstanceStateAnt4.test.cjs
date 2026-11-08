/* Actual LogList/Vue/Antd/currentUser. Only synthetic read transports are mocked.
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
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, defineComponent, ref, h } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 97001, username: 'SYNTHETIC_LOG_ADMIN_A', role: 'admin' }, B = { id: 97002, username: 'SYNTHETIC_LOG_ADMIN_B', role: 'admin' }, ordinary = { id: 97003, username: 'SYNTHETIC_LOG_USER', role: 'user' }
const IP = '198.51.100.7', roots = [], calls = [], ipCalls = [], writes = [], network = [], errors = []
let Page, autoLogs = true, autoIps = true
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const compact = value => String(value || '').replace(/\s+/g, ' ').trim()
const file = path.join(web, 'src/pages/LogList/index.vue'), initialSHA = crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
function sha() { return crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex') }
function trap(kind) { network.push(kind); throw new Error('FORBIDDEN_LOG_NATIVE_IO_' + kind) }
function row(marker, id = 101) { return { id, module: 'recharge', action: 30, username: 'SYNTHETIC_LOG_OPERATOR', description: marker, clientIp: IP, createTime: '2026-10-29T01:02:03Z', status: 'success' } }
function response(marker, params = {}, total = 100) { return { status: 200, data: { data: [row(marker)], total, page: params.page || 1, pageSize: params.pageSize || 10 } } }
function deferredRecord(list, params) { const record = { params: clone(params), actor: clone(user.getStoredCurrentUser()), completed: false }; const promise = new Promise((resolve, reject) => { record.resolve = value => { record.completed = true; resolve(value) }; record.reject = value => { record.completed = true; reject(value) } }); list.push(record); return { record, promise } }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
  getLogList(params) { const { record, promise } = deferredRecord(calls, params); if (autoLogs) record.resolve(response('SYNTHETIC_DEFAULT_ROW_' + calls.length, params)); return promise },
  getIpLocations(params) { const { record, promise } = deferredRecord(ipCalls, params); if (autoIps) record.resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_DEFAULT_LOCATION' } } }); return promise },
  ...Object.fromEntries(['createRechargeOrder', 'createQuestions', 'addUser', 'updateUser', 'delUser', 'adjustUserBalance', 'updateSystemSettings'].map(name => [name, () => { writes.push(name); throw new Error('FORBIDDEN_LOG_WRITE_' + name) }])),
}; require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return trap('XHR') }; globalThis.fetch = window.fetch = () => trap('FETCH'); window.open = () => trap('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { trap('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => trap(name); transport.get = () => trap(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function snapshot(page) { return { rows: clone(page.vm.dataSource), keywordInput: page.vm.keywordInput, filters: clone(page.vm.filters), page: page.vm.pagination.current, pageSize: page.vm.pagination.pageSize, total: page.vm.pagination.total, loading: page.vm.tabLoading, error: page.vm.loadError, locations: clone(page.vm.ipLocations), nativeKeyword: page.element.querySelector('input[placeholder="说明 / 用户名 / IP"]')?.value, tableText: page.element.querySelector('.desktop-log-table')?.textContent, mobileText: page.element.querySelector('.mobile-log-list')?.textContent } }
function observe(label, pages) { console.log('LOG_INSTANCE_NATIVE_DOM ' + JSON.stringify({ label, pages: pages.map(snapshot), GET: calls.map(x => ({ params: x.params, actorID: x.actor?.id, completed: x.completed })), IPGET: ipCalls.map(x => ({ params: x.params, actorID: x.actor?.id, completed: x.completed })) })) }
function button(page, text) { const found = [...page.element.querySelectorAll('button')].find(node => compact(node.textContent).replace(/\s/g, '') === text.replace(/\s/g, '')); assert.ok(found, 'actual native button ' + text); return found }
function keyword(page) { return page.get('input[placeholder="说明 / 用户名 / IP"]') }
async function open(twins = false) {
  const showFirst = ref(true)
  const Root = twins ? defineComponent({ setup() { return () => h('section', [h('button', { onClick: () => { showFirst.value = false }, 'data-fixture-unmount-first': '' }, 'Unmount first fixture view'), ...(showFirst.value ? [h(Page, { key: 'first' })] : []), h(Page, { key: 'second' })]) } }) : Page
  const root = mount(Root, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); roots.push(root); await settle()
  const pages = twins ? root.findAllComponents(Page) : [root]; assert.equal(pages.length, twins ? 2 : 1)
  return { root, pages }
}
beforeEach(() => { autoLogs = autoIps = true; delete require.cache[file]; Page = require(file); user.setStoredCurrentUser(A) })
afterEach(async () => {
  for (const root of roots.splice(0)) if (root.exists()) root.unmount()
  for (const call of calls) if (!call.completed) call.resolve({ status: 503 })
  for (const call of ipCalls) if (!call.completed) call.resolve({ status: 200, data: {} })
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('LOG_INSTANCE_NATIVE_SAFETY ' + JSON.stringify({ sourceSHA256: sha(), GETReads: calls.length, IPReads: ipCalls.length, writes, realIO: network, runtimeErrors: errors }))
  assert.equal(sha(), initialSHA); assert.deepEqual(writes, []); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  calls.length = ipCalls.length = writes.length = network.length = errors.length = 0
})
test('one owned twin response only publishes its own actual rows and keeps pending sibling loading', async () => {
  autoLogs = false; const { pages: [first, second] } = await open(true); assert.equal(calls.length, 2)
  calls[0].resolve(response('SYNTHETIC_FIRST_OWNED_ROWS', calls[0].params)); await settle(); observe('first response while sibling pending', [first, second])
  assert.ok(first.text().includes('SYNTHETIC_FIRST_OWNED_ROWS')); assert.ok(!second.text().includes('SYNTHETIC_FIRST_OWNED_ROWS'), 'first response must not appear in sibling DOM')
  assert.equal(first.vm.tabLoading, false); assert.equal(second.vm.tabLoading, true); assert.ok(second.element.querySelector('.desktop-log-table .ant-spin-spinning'))
  calls[1].resolve(response('SYNTHETIC_SECOND_OWNED_ROWS', calls[1].params)); await settle(); assert.ok(second.text().includes('SYNTHETIC_SECOND_OWNED_ROWS')); assert.ok(!first.text().includes('SYNTHETIC_SECOND_OWNED_ROWS'))
})
test('pending sibling loading remains visible after an independent first request finishes', async () => {
  autoLogs = false; const { pages: [first, second] } = await open(true); calls[0].resolve(response('SYNTHETIC_FIRST_LOADING_DONE')); await settle(); observe('independent busy flags', [first, second])
  assert.equal(second.vm.tabLoading, true); assert.ok(second.element.querySelector('.desktop-log-table .ant-spin-spinning')); assert.equal(first.vm.tabLoading, false)
})
test('typing native draft in one view does not change sibling input or submit a read', async () => {
  const { pages: [first, second] } = await open(true); const count = calls.length
  await keyword(first).setValue('SYNTHETIC_FIRST_PRIVATE_DRAFT'); await settle(); observe('independent draft', [first, second])
  assert.equal(keyword(first).element.value, 'SYNTHETIC_FIRST_PRIVATE_DRAFT'); assert.equal(keyword(second).element.value, ''); assert.equal(second.vm.keywordInput, ''); assert.equal(calls.length, count)
})
test('native module selection only changes the selected view query and default sibling remains unchanged', async () => {
  const { pages: [first, second] } = await open(true), select = first.findAllComponents(Antd.Select).find(value => value.element.closest('.filter-form'))
  select.vm.$emit('update:value', 'recharge'); select.vm.$emit('change', 'recharge'); await settle(); observe('independent module filter', [first, second])
  assert.deepEqual(calls.at(-1).params, { module: 'recharge', page: 1, pageSize: 10 }); assert.equal(second.vm.filters.module, undefined); assert.equal(second.vm.activeFilterCount, 0)
})
test('actual mobile pagination event changes one view page and pageSize without changing sibling pagination', async () => {
  const { pages: [first, second] } = await open(true); const pagination = first.find('.mobile-log-pagination').findComponent(Antd.Pagination)
  pagination.vm.$emit('change', 3, 20); await settle(); observe('independent page', [first, second])
  assert.deepEqual(calls.at(-1).params, { page: 3, pageSize: 20 }); assert.equal(first.vm.pagination.current, 3); assert.equal(first.vm.pagination.pageSize, 20); assert.equal(second.vm.pagination.current, 1); assert.equal(second.vm.pagination.pageSize, 10)
})
test('resetting first view cannot erase second applied native query and actual rows', async () => {
  const { pages: [first, second] } = await open(true); await keyword(second).setValue('SYNTHETIC_SECOND_APPLIED'); second.get('button.ant-input-search-button').element.click(); await settle()
  const marker = second.vm.dataSource[0].description; button(first, '重置筛选').click(); await settle(); observe('independent reset', [first, second])
  assert.equal(keyword(second).element.value, 'SYNTHETIC_SECOND_APPLIED'); assert.equal(second.vm.filters.keyword, 'SYNTHETIC_SECOND_APPLIED'); assert.ok(second.text().includes(marker)); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 10 })
})
test('actually unmounting first twin cannot clear surviving sibling rendered rows or location map', async () => {
  const { root, pages: [, second] } = await open(true); const marker = second.vm.dataSource[0].description; assert.ok(second.text().includes(marker)); assert.equal(second.vm.ipLocations[IP], 'SYNTHETIC_DEFAULT_LOCATION')
  root.get('[data-fixture-unmount-first]').element.click(); await settle(); observe('survivor after sibling unmount', [second])
  assert.ok(second.text().includes(marker), 'surviving real DOM rows must remain'); assert.equal(second.vm.ipLocations[IP], 'SYNTHETIC_DEFAULT_LOCATION'); assert.equal(second.vm.pagination.total, 100); assert.equal(second.vm.tabLoading, false)
})
test('a first view deferred IP response cannot publish its address into sibling location display', async () => {
  autoIps = false; const { pages: [first, second] } = await open(true); assert.equal(ipCalls.length, 2)
  ipCalls[0].resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_FIRST_LOCATION_ONLY' } } }); await settle(); observe('independent IP result', [first, second])
  assert.ok(first.text().includes('SYNTHETIC_FIRST_LOCATION_ONLY')); assert.ok(!second.text().includes('SYNTHETIC_FIRST_LOCATION_ONLY')); assert.equal(second.vm.ipLocations[IP], undefined)
  ipCalls[1].resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_SECOND_LOCATION_ONLY' } } }); await settle(); assert.ok(second.text().includes('SYNTHETIC_SECOND_LOCATION_ONLY')); assert.ok(!first.text().includes('SYNTHETIC_SECOND_LOCATION_ONLY'))
})
test('one view failure cannot erase healthy sibling rows or display its load error there', async () => {
  const { pages: [first, second] } = await open(true); const marker = second.vm.dataSource[0].description; autoLogs = false; button(first, '刷新').click(); await settle(); calls.at(-1).reject(new Error('SYNTHETIC_FIRST_READ_FAILURE')); await settle(); observe('independent failure', [first, second])
  assert.equal(first.vm.loadError, '获取操作日志失败，请重试。'); assert.equal(second.vm.loadError, ''); assert.ok(second.text().includes(marker)); assert.equal(second.vm.pagination.total, 100)
})
test('A query and page are not inherited after all A LogList views unmount and B mounts same Page module', async () => {
  const { root, pages: [first] } = await open(); await keyword(first).setValue('SYNTHETIC_PRIVATE_A_FILTER'); first.get('button.ant-input-search-button').element.click(); await settle(); first.find('.mobile-log-pagination').findComponent(Antd.Pagination).vm.$emit('change', 3, 20); await settle(); assert.equal(calls.at(-1).params.keyword, 'SYNTHETIC_PRIVATE_A_FILTER')
  root.unmount(); user.setStoredCurrentUser(B); const before = calls.length; const { pages: [second] } = await open(); observe('unmounted A to B handoff', [second])
  assert.equal(calls.length, before + 1); assert.equal(calls.at(-1).actor.id, B.id); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 10 }); assert.equal(keyword(second).element.value, ''); assert.equal(second.vm.filters.keyword, ''); assert.ok(!second.text().includes('SYNTHETIC_PRIVATE_A_FILTER'))
})
test('ordinary account makes no log or location read and keeps existing denial UI', async () => {
  user.setStoredCurrentUser(ordinary); const { pages: [page] } = await open(); observe('ordinary no read', [page]); assert.equal(calls.length, 0); assert.equal(ipCalls.length, 0); assert.equal(page.vm.isAdmin, false); assert.ok(page.text().includes('当前账号无日志管理权限。'))
})
test('single admin default first GET options and native apply reset clear pagination contract remain', async () => {
  const { pages: [page] } = await open(); assert.deepEqual(calls[0].params, { page: 1, pageSize: 10 }); assert.equal(page.vm.tabLoading, false); assert.equal(page.vm.dataSource.length, 1)
  for (const key of ['sms', 'recharge', 'access_token']) assert.ok(page.vm.moduleOptions.some(value => value.value === key))
  await keyword(page).setValue('  SYNTHETIC_SINGLE%_  '); assert.equal(calls.length, 1); page.get('button.ant-input-search-button').element.click(); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_SINGLE%_', page: 1, pageSize: 10 }); assert.equal(keyword(page).element.value, 'SYNTHETIC_SINGLE%_')
  const selects = page.findAllComponents(Antd.Select).filter(value => value.element.closest('.filter-form')); selects[0].vm.$emit('update:value', 'recharge'); selects[0].vm.$emit('change', 'recharge'); await settle(); selects[1].vm.$emit('update:value', 30); selects[1].vm.$emit('change', 30); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_SINGLE%_', module: 'recharge', action: 30, page: 1, pageSize: 10 })
  button(page, '重置筛选').click(); await settle(); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 10 }); assert.equal(keyword(page).element.value, ''); observe('single original query positive', [page])
})
test('single location rejection is fail-soft and does not alter successful rows or show list error', async () => {
  autoIps = false; const { pages: [page] } = await open(); const marker = page.vm.dataSource[0].description; ipCalls[0].reject(new Error('SYNTHETIC_LOCATION_UNAVAILABLE')); await settle(); observe('location fail soft', [page]); assert.ok(page.text().includes(marker)); assert.ok(page.text().includes(IP)); assert.equal(page.vm.loadError, ''); assert.equal(page.vm.tabLoading, false); assert.deepEqual(clone(page.vm.ipLocations), {})
})
test('single latest request ownership ignores earlier read and IP completion after a newer refresh', async () => {
  autoIps = false; const { pages: [page] } = await open(); const oldIp = ipCalls[0]; autoLogs = false; button(page, '刷新').click(); await settle(); const older = calls.at(-1); button(page, '刷新').click(); await settle(); const latest = calls.at(-1)
  latest.resolve(response('SYNTHETIC_LATEST_ROWS')); await settle(); older.resolve(response('SYNTHETIC_LATE_OLDER_ROWS')); oldIp.resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_LATE_OLD_ADDRESS' } } }); await settle(); observe('single latest guard', [page]); assert.ok(page.text().includes('SYNTHETIC_LATEST_ROWS')); assert.ok(!page.text().includes('SYNTHETIC_LATE_OLDER_ROWS')); assert.ok(!page.text().includes('SYNTHETIC_LATE_OLD_ADDRESS')); assert.equal(page.vm.tabLoading, false)
})
test('single live identity change retires A query and late rows while B can load with original defaults', async () => {
  const { pages: [page] } = await open(); await keyword(page).setValue('SYNTHETIC_LIVE_A_QUERY'); autoLogs = false; page.get('button.ant-input-search-button').element.click(); await settle(); const old = calls.at(-1); user.setStoredCurrentUser(B); await settle(); const fresh = calls.at(-1); assert.equal(fresh.actor.id, B.id); assert.deepEqual(fresh.params, { page: 1, pageSize: 10 }); fresh.resolve(response('SYNTHETIC_LIVE_B_ROWS')); await settle(); old.resolve(response('SYNTHETIC_LIVE_A_LATE')); await settle(); observe('existing live identity guard', [page]); assert.ok(page.text().includes('SYNTHETIC_LIVE_B_ROWS')); assert.ok(!page.text().includes('SYNTHETIC_LIVE_A_LATE')); assert.equal(keyword(page).element.value, '')
})
test('late response after actual unmount remains retired and cannot publish stored identity data', async () => {
  autoLogs = false; const { root, pages: [page] } = await open(); const request = calls[0]; root.unmount(); request.resolve(response('SYNTHETIC_AFTER_UNMOUNT_ROWS')); await settle(); assert.ok(!document.body.textContent.includes('SYNTHETIC_AFTER_UNMOUNT_ROWS')); assert.equal(user.getStoredCurrentUser().id, A.id); assert.equal(page.vm.dataSource.length, 0)
})
