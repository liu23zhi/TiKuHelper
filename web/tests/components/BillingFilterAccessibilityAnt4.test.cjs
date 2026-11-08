/* Actual BillingRecordList/Vue/Antd/currentUser/router filter associations.
 * Synthetic GET only, all writes/providers/real transports trapped. Same fixture
 * accepts TIKU_WEB_SOURCE/UI_TEST_HELPER for B/M/R source versions. Native Antd
 * label activation and DOM name sources are not reader/device certification. */
const { test, beforeEach, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const name of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[name] = window[name]
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, defineComponent, ref, h } = require('vue'), Antd = require('ant-design-vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 52101, username: 'SYNTHETIC_BILL_FILTER_A', role: 'admin' }, B = { id: 52102, username: 'SYNTHETIC_BILL_FILTER_B', role: 'admin' }, ordinary = { id: 52103, username: 'SYNTHETIC_BILL_FILTER_USER', role: 'user' }
const IP = '198.51.100.7', labels = ['关键词', '类型'], calls = [], ipCalls = [], roots = [], writes = [], network = [], errors = []
const file = path.join(web, 'src/pages/BillingRecordList/index.vue'), sourceHash = () => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'), initialSHA = sourceHash()
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value)), compact = value => String(value || '').replace(/\s+/g, ' ').trim()
let Page, autoBills = true
function trap(kind) { network.push(kind); throw new Error('FORBIDDEN_BILL_FILTER_IO_' + kind) }
function response(marker, params = {}, actor = A) { return { status: 200, data: { data: [{ id: 101, userId: actor.id, username: actor.username, type: 'normal_search', direction: 'expense', amount: 0.123456, questionText: marker, subject: marker, provider: 'ai', sourceLabel: 'SYNTHETIC_PROVIDER', clientIp: IP, showInStatement: actor.role !== 'admin', requestId: 'SYNTHETIC_REQUEST_101', createdAt: '2026-10-29T01:02:03Z', feeDetails: [{ provider: 'ai', category: 'AI模型调用', amount: 0.123456, detail: 'SYNTHETIC_DETAIL' }] }], total: 100, page: params.page || 1, pageSize: params.pageSize || 10, summary: { totalAmount: 0.123456, currentPageAmount: 0.123456, currentPageCacheHitRate: 0.125, totalCacheHitRate: 0.375 } } } }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
  getBillingRecords(params) {
    const call = { params: clone(params), actor: clone(user.getStoredCurrentUser()), completed: false }; calls.push(call)
    const promise = new Promise(resolve => { call.resolve = value => { call.completed = true; resolve(value) } })
    if (autoBills) call.resolve(response('SYNTHETIC_BILL_FILTER_ROW_' + calls.length, params, call.actor))
    return promise
  },
  getIpLocations(params) { ipCalls.push({ kind: 'admin', params: clone(params) }); return Promise.resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_ADMIN_LOCATION' } } }) },
  getMyLocations(params) { ipCalls.push({ kind: 'self', params: clone(params) }); return Promise.resolve({ status: 200, data: { ip: { [IP]: { address: 'SYNTHETIC_SELF_LOCATION' } } } }) },
  ...Object.fromEntries(['updateBillingStatementVisibility', 'createRechargeOrder', 'createQuestions', 'addUser', 'updateUser', 'delUser', 'adjustUserBalance', 'updateSystemSettings'].map(name => [name, () => { writes.push(name); throw new Error('FORBIDDEN_BILL_FILTER_WRITE_' + name) }])),
}; require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return trap('XHR') }; globalThis.fetch = window.fetch = () => trap('FETCH'); window.open = () => trap('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { trap('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => trap(name); transport.get = () => trap(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function field(page, text) {
  const found = [...page.element.querySelectorAll('.filter-form .ant-form-item-label > label')].filter(label => compact(label.textContent) === text)
  assert.equal(found.length, 1, 'one exact visible Chinese label: ' + text)
  const label = found[0], item = label.closest('.ant-form-item'), inputs = [...item.querySelectorAll('input')].filter(input => input.type !== 'hidden')
  assert.equal(inputs.length, 1, 'one actual native input/combobox, not wrapper: ' + text)
  return { label, input: inputs[0], item }
}
function associated(page, text) {
  const result = field(page, text), { label, input } = result
  assert.ok(input.id, 'actual native control has stable nonempty ID: ' + text)
  assert.equal(label.htmlFor, input.id, 'visible label targets actual control: ' + text)
  assert.equal(label.control, input); assert.ok([...input.labels].includes(label)); assert.equal(document.getElementById(input.id), input)
  return result
}
function keyword(page) { return page.get('input[placeholder="题目 / 请求 ID / 用户 / 渠道"]') }
function button(page, text) { const node = [...page.element.querySelectorAll('button')].find(value => compact(value.textContent).replace(/\s/g, '') === text.replace(/\s/g, '')); assert.ok(node, 'actual native button ' + text); return node }
async function open(twins = false) {
  const showFirst = ref(true), router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/billing', component: { render: () => null } }, { path: '/other', component: { render: () => null } }] }); await router.push('/billing'); await router.isReady()
  const Root = twins ? defineComponent({ setup: () => () => h('section', [h('button', { 'data-fixture-unmount-first': '', onClick: () => { showFirst.value = false } }, 'Unmount fixture first'), ...(showFirst.value ? [h(Page, { key: 'first' })] : []), h(Page, { key: 'second' })]) }) : Page
  const root = mount(Root, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); root.fixtureRouter = router; roots.push(root); await settle()
  const pages = twins ? root.findAllComponents(Page) : [root]; assert.equal(pages.length, twins ? 2 : 1)
  for (const page of pages) console.log('BILL_FILTER_NATIVE_DOM ' + JSON.stringify({ fields: labels.map(text => { const { label, input } = field(page, text); return { text, labelFor: label.htmlFor, inputId: input.id, role: input.getAttribute('role'), labelControlMatches: label.control === input } }), params: clone(calls.at(-1)?.params), rows: page.vm.dataSource.length, loading: page.vm.tableLoading, summary: clone(page.vm.summary) }))
  return { root, pages }
}
beforeEach(() => { autoBills = true; delete require.cache[file]; Page = require(file); user.setStoredCurrentUser(A) })
afterEach(async () => {
  for (const root of roots.splice(0)) { if (root.exists()) root.unmount(); root.fixtureRouter?.options.history.destroy() }
  for (const call of calls) if (!call.completed) call.resolve({ status: 503 })
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('BILL_FILTER_NATIVE_SAFETY ' + JSON.stringify({ sourceSHA256: sourceHash(), GETReads: calls.length, IPReads: ipCalls.length, writes, realIO: network, runtimeErrors: errors }))
  assert.equal(sourceHash(), initialSHA); assert.deepEqual(writes, []); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  calls.length = ipCalls.length = writes.length = network.length = errors.length = 0
})
for (const text of labels) test('visible billing ' + text + ' label associates with actual native Antd control', async () => {
  const { pages: [page] } = await open(), { input } = associated(page, text)
  if (text === '类型') assert.equal(input.getAttribute('role'), 'combobox')
})
for (const text of labels) test('actual Antd billing ' + text + ' label click focuses exact input without new read or query mutation', async () => {
  const { pages: [page] } = await open(), { label, input } = associated(page, text), count = calls.length, before = clone(page.vm.filters)
  document.activeElement?.blur(); label.click(); await settle()
  assert.equal(document.activeElement, input, 'actual Antd activation, no focus shim'); assert.equal(calls.length, count); assert.deepEqual(clone(page.vm.filters), before)
})
test('same Vueapp twin billing views have four unique anonymous IDs and each label focuses its own native control', async () => {
  const { pages } = await open(true), controls = pages.flatMap(page => labels.map(text => associated(page, text))), ids = controls.map(value => value.input.id)
  assert.equal(new Set(ids).size, 4); const all = [...document.querySelectorAll('[id]')].map(node => node.id).filter(Boolean); assert.equal(new Set(all).size, all.length)
  for (const id of ids) for (const secret of [A.username, B.username, String(A.id), String(B.id)]) assert.ok(!id.includes(secret))
  for (const { label, input } of controls) { label.click(); await settle(); assert.equal(document.activeElement, input) }
  assert.equal(calls.length, 2); assert.ok(calls.every(call => call.actor.id === A.id)); assert.ok(calls.every(call => JSON.stringify(call.params) === JSON.stringify({ page: 1, pageSize: 10 })))
})
test('default billing search icon already has an exposed naming source without manufacturing an empty-name defect', async () => {
  const { pages: [page] } = await open(), control = page.element.querySelector('button.ant-input-search-button'); assert.ok(control)
  const refs = String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => document.getElementById(id)?.textContent || '').join(' '), icon = control.querySelector('[role="img"][aria-label]:not([aria-hidden="true"])')
  const sources = { ariaLabel: control.getAttribute('aria-label') || '', ariaLabelledby: compact(refs), title: control.getAttribute('title') || '', text: compact(control.textContent), exposedImageLabel: icon?.getAttribute('aria-label') || '' }
  const name = compact(sources.ariaLabel || sources.ariaLabelledby || sources.text || sources.exposedImageLabel || sources.title)
  console.log('BILL_FILTER_SEARCH_NAME ' + JSON.stringify({ method: 'explicit DOM naming-source observation,not fullAccName implementation', name, sources, imageAriaHidden: icon?.getAttribute('aria-hidden') || null })); assert.ok(name); assert.equal(calls.length, 1)
})
test('original native keyword type page reset and clear preserve exact readonly billing GET parameters and six-digit totals', async () => {
  const { pages: [page] } = await open(); assert.deepEqual(calls[0].params, { page: 1, pageSize: 10 }); assert.equal(page.vm.summary.totalAmount, 0.123456); assert.ok(page.text().includes('0.123456'))
  await keyword(page).setValue('  SYNTHETIC_BILL_草稿%_  '); await settle(); assert.equal(calls.length, 1); assert.equal(page.vm.filters.keyword, '')
  page.get('button.ant-input-search-button').element.click(); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_BILL_草稿%_', page: 1, pageSize: 10 })
  const select = page.findAllComponents(Antd.Select).find(value => value.element.closest('.filter-form')); assert.ok(select); select.vm.$emit('update:value', 'recharge'); select.vm.$emit('change', 'recharge'); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_BILL_草稿%_', type: 'recharge', page: 1, pageSize: 10 })
  page.find('.mobile-billing-pagination').findComponent(Antd.Pagination).vm.$emit('change', 3, 20); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_BILL_草稿%_', type: 'recharge', page: 3, pageSize: 20 })
  button(page, '重置筛选').click(); await settle(); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 20 }); assert.equal(keyword(page).element.value, '')
  await keyword(page).setValue('SYNTHETIC_BILL_CLEAR'); page.get('button.ant-input-search-button').element.click(); await settle(); const clear = page.element.querySelector('.ant-input-clear-icon'); assert.ok(clear); clear.click(); await settle(); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 20 }); assert.equal(page.vm.keywordInput, '')
})
test('ordinary billing account retains own GET and self-location route with no admin columns or visibility control', async () => {
  user.setStoredCurrentUser(ordinary); const { pages: [page] } = await open(); assert.equal(calls.length, 1); assert.equal(calls[0].actor.id, ordinary.id); assert.deepEqual(calls[0].params, { page: 1, pageSize: 10 }); assert.equal(page.vm.isAdmin, false)
  assert.ok(page.text().includes('我的账单')); assert.ok(page.text().includes('SYNTHETIC_BILL_FILTER_ROW_1')); assert.ok(!page.vm.columns.some(value => ['effectiveMultiplier', 'clientIp'].includes(value.key))); assert.deepEqual(ipCalls.map(value => value.kind), ['self']); assert.equal(page.vm.dataSource[0].showInStatement, true); assert.equal(writes.length, 0)
})
test('already delivered twin live rows summaries and query remain isolated after actual sibling unmount', async () => {
  const { root, pages: [first, second] } = await open(true), marker = second.vm.dataSource[0].questionText, summary = clone(second.vm.summary)
  await keyword(first).setValue('SYNTHETIC_FIRST_ONLY_DRAFT'); await settle(); assert.equal(keyword(second).element.value, ''); assert.equal(calls.length, 2)
  first.get('button.ant-input-search-button').element.click(); await settle(); assert.equal(first.vm.filters.keyword, 'SYNTHETIC_FIRST_ONLY_DRAFT'); assert.equal(second.vm.filters.keyword, ''); assert.ok(second.text().includes(marker))
  root.get('[data-fixture-unmount-first]').element.click(); await settle(); assert.ok(second.text().includes(marker)); assert.deepEqual(clone(second.vm.summary), summary); assert.equal(second.vm.ipLocations[IP], 'SYNTHETIC_ADMIN_LOCATION')
})
test('existing billing live identity and late-read guards keep B defaults and retire delayed A rows and totals', async () => {
  const { pages: [page] } = await open(); await keyword(page).setValue('SYNTHETIC_OLD_BILL_ACTOR_QUERY'); autoBills = false; page.get('button.ant-input-search-button').element.click(); await settle(); const old = calls.at(-1)
  user.setStoredCurrentUser(B); assert.equal(page.vm.dataSource.length, 0); assert.equal(page.vm.keywordInput, ''); await settle(); assert.equal(keyword(page).element.value, ''); const fresh = calls.at(-1); assert.equal(fresh.actor.id, B.id); assert.deepEqual(fresh.params, { page: 1, pageSize: 10 })
  fresh.resolve(response('SYNTHETIC_B_CURRENT_BILL', fresh.params, fresh.actor)); await settle(); old.resolve(response('SYNTHETIC_A_RETIRED_BILL', old.params, old.actor)); await settle(); assert.ok(page.text().includes('SYNTHETIC_B_CURRENT_BILL')); assert.ok(!page.text().includes('SYNTHETIC_A_RETIRED_BILL')); assert.equal(page.vm.summary.totalAmount, 0.123456); assert.equal(page.vm.tableLoading, false)
})
test('admin six-digit amount hidden-row display and nonboolean visibility rejection retain original read-only positives', async () => {
  const { pages: [page] } = await open(), before = clone(page.vm.dataSource); assert.equal(page.vm.isAdmin, true); assert.equal(page.vm.dataSource[0].showInStatement, false); assert.ok(page.text().includes('管理员始终可查看全部账单')); assert.equal(page.vm.summary.currentPageAmount, 0.123456); assert.ok(page.text().includes('0.123456'))
  for (const value of ['false', 0, undefined, null]) assert.equal(await page.vm.handleStatementVisibilityChange({ id: 101, showInStatement: value }), false)
  assert.equal(writes.length, 0); assert.deepEqual(clone(page.vm.dataSource), before); assert.equal(page.vm.statementFailure, null)
})
