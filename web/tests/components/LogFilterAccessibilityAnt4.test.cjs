/* Actual LogList/Vue/Antd filter associations, focus and existing read state.
 * Only synthetic GET APIs. Every write/provider/real transport is trapped.
 * Unchanged fixture accepts TIKU_WEB_SOURCE/UI_TEST_HELPER for native B/M/R.
 * DOM naming sources and jsdom Antd focus are not reader/device certification. */
const { test, beforeEach, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const name of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[name] = window[name]
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, defineComponent, ref, h } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const A = { id: 51101, username: 'SYNTHETIC_LOG_FILTER_A', role: 'admin' }, B = { id: 51102, username: 'SYNTHETIC_LOG_FILTER_B', role: 'admin' }, ordinary = { id: 51103, username: 'SYNTHETIC_LOG_FILTER_USER', role: 'user' }
const IP = '198.51.100.7', labels = ['关键词', '模块', '操作类型'], calls = [], ipCalls = [], roots = [], writes = [], network = [], errors = []
const file = path.join(web, 'src/pages/LogList/index.vue'), sourceHash = () => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'), initialSHA = sourceHash()
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value)), compact = value => String(value || '').replace(/\s+/g, ' ').trim()
let Page, autoLogs = true
function trap(kind) { network.push(kind); throw new Error('FORBIDDEN_LOG_FILTER_IO_' + kind) }
function reply(marker, params = {}) { return { status: 200, data: { data: [{ id: 17, module: 'recharge', action: 30, username: 'SYNTHETIC_OPERATOR', description: marker, clientIp: IP, createTime: '2026-10-29T01:02:03Z', status: 'success' }], total: 100, page: params.page || 1, pageSize: params.pageSize || 10 } } }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
  getLogList(params) {
    const call = { params: clone(params), actor: clone(user.getStoredCurrentUser()), completed: false }; calls.push(call)
    const promise = new Promise(resolve => { call.resolve = value => { call.completed = true; resolve(value) } })
    if (autoLogs) call.resolve(reply('SYNTHETIC_LOG_FILTER_ROW_' + calls.length, params))
    return promise
  },
  getIpLocations(params) { ipCalls.push(clone(params)); return Promise.resolve({ status: 200, data: { [IP]: { address: 'SYNTHETIC_LOG_FILTER_LOCATION' } } }) },
  ...Object.fromEntries(['createRechargeOrder', 'createQuestions', 'addUser', 'updateUser', 'delUser', 'adjustUserBalance', 'updateSystemSettings', 'updateBillingStatementVisibility'].map(name => [name, () => { writes.push(name); throw new Error('FORBIDDEN_LOG_FILTER_WRITE_' + name) }])),
}; require.cache[apiPath] = api
window.XMLHttpRequest.prototype.open = function () { return trap('XHR') }; globalThis.fetch = window.fetch = () => trap('FETCH'); window.open = () => trap('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { trap('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => trap(name); transport.get = () => trap(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
let computedName, nameLibrary = 'explicit DOM naming-source observation (not a full AccName implementation)'
try { computedName = require('dom-accessibility-api').computeAccessibleName; nameLibrary = 'installed dom-accessibility-api' } catch (error) { if (error.code !== 'MODULE_NOT_FOUND') throw error }
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function field(page, text) {
  const found = [...page.element.querySelectorAll('.filter-form .ant-form-item-label > label')].filter(label => compact(label.textContent) === text)
  assert.equal(found.length, 1, 'one exact visible Chinese label: ' + text)
  const label = found[0], item = label.closest('.ant-form-item'), inputs = [...item.querySelectorAll('input')].filter(input => input.type !== 'hidden')
  assert.equal(inputs.length, 1, 'one real native input/combobox, not wrapper: ' + text)
  return { label, input: inputs[0], item }
}
function associated(page, text) {
  const result = field(page, text), { label, input } = result
  assert.ok(input.id, 'actual native input has stable nonempty ID: ' + text)
  assert.equal(label.htmlFor, input.id, 'visible label for targets actual control: ' + text)
  assert.equal(label.control, input); assert.ok([...input.labels].includes(label)); assert.equal(document.getElementById(input.id), input)
  return result
}
function keyword(page) { return page.get('input[placeholder="说明 / 用户名 / IP"]') }
function button(page, text) { const value = [...page.element.querySelectorAll('button')].find(node => compact(node.textContent).replace(/\s/g, '') === text.replace(/\s/g, '')); assert.ok(value, 'actual native button: ' + text); return value }
async function open(twins = false) {
  const showFirst = ref(true), Root = twins ? defineComponent({ setup: () => () => h('section', [h('button', { 'data-fixture-unmount-first': '', onClick: () => { showFirst.value = false } }, 'Unmount fixture first'), ...(showFirst.value ? [h(Page, { key: 'first' })] : []), h(Page, { key: 'second' })]) }) : Page
  const root = mount(Root, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); roots.push(root); await settle()
  const pages = twins ? root.findAllComponents(Page) : [root]; assert.equal(pages.length, twins ? 2 : 1)
  for (const page of pages) console.log('LOG_FILTER_NATIVE_DOM ' + JSON.stringify({ fields: labels.map(text => { const { label, input } = field(page, text); return { text, labelFor: label.htmlFor, inputId: input.id, role: input.getAttribute('role'), labelControlMatches: label.control === input } }), params: clone(calls.at(-1)?.params), rows: page.vm.dataSource.length, loading: page.vm.tabLoading }))
  return { root, pages }
}
beforeEach(() => { autoLogs = true; delete require.cache[file]; Page = require(file); user.setStoredCurrentUser(A) })
afterEach(async () => {
  for (const root of roots.splice(0)) if (root.exists()) root.unmount()
  for (const call of calls) if (!call.completed) call.resolve({ status: 503 })
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('LOG_FILTER_NATIVE_SAFETY ' + JSON.stringify({ sourceSHA256: sourceHash(), GETReads: calls.length, IPReads: ipCalls.length, writes, realIO: network, runtimeErrors: errors }))
  assert.equal(sourceHash(), initialSHA); assert.deepEqual(writes, []); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  calls.length = ipCalls.length = writes.length = network.length = errors.length = 0
})
for (const text of labels) test('visible log ' + text + ' label associates with actual native Antd control', async () => {
  const { pages: [page] } = await open(), { input } = associated(page, text)
  if (text !== '关键词') assert.equal(input.getAttribute('role'), 'combobox')
})
for (const text of labels) test('actual Antd log ' + text + ' label click focuses exact input without new read or query mutation', async () => {
  const { pages: [page] } = await open(), { label, input } = associated(page, text), count = calls.length, before = clone(page.vm.filters)
  document.activeElement?.blur(); label.click(); await settle()
  assert.equal(document.activeElement, input, 'actual Antd activation, no focus shim'); assert.equal(calls.length, count); assert.deepEqual(clone(page.vm.filters), before)
})
test('same Vueapp twin log views have six unique anonymous field IDs and each native label focuses only itself', async () => {
  const { pages } = await open(true), controls = pages.flatMap(page => labels.map(text => associated(page, text))), ids = controls.map(value => value.input.id)
  assert.equal(new Set(ids).size, 6)
  const all = [...document.querySelectorAll('[id]')].map(node => node.id).filter(Boolean); assert.equal(new Set(all).size, all.length)
  for (const id of ids) for (const secret of [A.username, B.username, String(A.id), String(B.id)]) assert.ok(!id.includes(secret))
  for (const { label, input } of controls) { label.click(); await settle(); assert.equal(document.activeElement, input) }
  assert.equal(calls.length, 2); assert.ok(calls.every(call => call.actor.id === A.id)); assert.ok(calls.every(call => JSON.stringify(call.params) === JSON.stringify({ page: 1, pageSize: 10 })))
})
test('default log search icon button has an existing explicit naming source without inventing an empty Chinese-name bug', async () => {
  const { pages: [page] } = await open(), control = page.element.querySelector('button.ant-input-search-button'); assert.ok(control)
  const refs = String(control.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => document.getElementById(id)?.textContent || '').join(' '), icon = control.querySelector('[role="img"][aria-label]:not([aria-hidden="true"])')
  const sources = { ariaLabel: control.getAttribute('aria-label') || '', ariaLabelledby: compact(refs), title: control.getAttribute('title') || '', text: compact(control.textContent), exposedImageLabel: icon?.getAttribute('aria-label') || '' }
  const name = computedName ? computedName(control) : compact(sources.ariaLabel || sources.ariaLabelledby || sources.text || sources.exposedImageLabel || sources.title)
  console.log('LOG_FILTER_SEARCH_NAME ' + JSON.stringify({ method: nameLibrary, name, sources, imageAriaHidden: icon?.getAttribute('aria-hidden') || null })); assert.ok(name); assert.equal(calls.length, 1)
})
test('original native keyword apply module action pagination reset and clear keep exact readonly GET parameters', async () => {
  const { pages: [page] } = await open(); assert.deepEqual(calls[0].params, { page: 1, pageSize: 10 })
  await keyword(page).setValue('  SYNTHETIC_LOG_草稿%_  '); await settle(); assert.equal(calls.length, 1); assert.equal(page.vm.filters.keyword, '')
  page.get('button.ant-input-search-button').element.click(); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_LOG_草稿%_', page: 1, pageSize: 10 })
  const selects = page.findAllComponents(Antd.Select).filter(value => value.element.closest('.filter-form')); assert.equal(selects.length, 2)
  selects[0].vm.$emit('update:value', 'recharge'); selects[0].vm.$emit('change', 'recharge'); await settle(); selects[1].vm.$emit('update:value', 30); selects[1].vm.$emit('change', 30); await settle()
  assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_LOG_草稿%_', module: 'recharge', action: 30, page: 1, pageSize: 10 })
  page.find('.mobile-log-pagination').findComponent(Antd.Pagination).vm.$emit('change', 3, 20); await settle(); assert.deepEqual(calls.at(-1).params, { keyword: 'SYNTHETIC_LOG_草稿%_', module: 'recharge', action: 30, page: 3, pageSize: 20 })
  button(page, '重置筛选').click(); await settle(); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 20 }); assert.equal(keyword(page).element.value, '')
  await keyword(page).setValue('SYNTHETIC_LOG_CLEAR'); page.get('button.ant-input-search-button').element.click(); await settle(); const clear = page.element.querySelector('.ant-input-clear-icon'); assert.ok(clear); clear.click(); await settle(); assert.deepEqual(calls.at(-1).params, { page: 1, pageSize: 20 }); assert.equal(page.vm.keywordInput, '')
})
test('ordinary log account retains original noGET denial and disabled refresh despite field presence', async () => {
  user.setStoredCurrentUser(ordinary); const { pages: [page] } = await open(); assert.equal(calls.length, 0); assert.equal(ipCalls.length, 0); assert.equal(page.vm.isAdmin, false); assert.ok(page.text().includes('当前账号无日志管理权限。')); assert.ok(button(page, '刷新').disabled)
})
test('already delivered twin live state and actual sibling unmount remain isolated', async () => {
  const { root, pages: [first, second] } = await open(true), secondMarker = second.vm.dataSource[0].description
  await keyword(first).setValue('SYNTHETIC_FIRST_ONLY_DRAFT'); await settle(); assert.equal(keyword(second).element.value, ''); assert.equal(calls.length, 2)
  first.get('button.ant-input-search-button').element.click(); await settle(); assert.equal(first.vm.filters.keyword, 'SYNTHETIC_FIRST_ONLY_DRAFT'); assert.equal(second.vm.filters.keyword, ''); assert.ok(second.text().includes(secondMarker))
  root.get('[data-fixture-unmount-first]').element.click(); await settle(); assert.ok(second.text().includes(secondMarker)); assert.equal(second.vm.pagination.total, 100); assert.equal(second.vm.ipLocations[IP], 'SYNTHETIC_LOG_FILTER_LOCATION')
})
test('existing live identity and late-read guards retain B defaults and never publish a delayed A row', async () => {
  const { pages: [page] } = await open(); await keyword(page).setValue('SYNTHETIC_OLD_ACTOR_QUERY'); autoLogs = false; page.get('button.ant-input-search-button').element.click(); await settle(); const old = calls.at(-1)
  user.setStoredCurrentUser(B); assert.equal(page.vm.dataSource.length, 0); assert.equal(page.vm.keywordInput, ''); await settle(); assert.equal(keyword(page).element.value, ''); const fresh = calls.at(-1); assert.equal(fresh.actor.id, B.id); assert.deepEqual(fresh.params, { page: 1, pageSize: 10 })
  fresh.resolve(reply('SYNTHETIC_B_CURRENT_ROW', fresh.params)); await settle(); old.resolve(reply('SYNTHETIC_A_RETIRED_ROW', old.params)); await settle(); assert.ok(page.text().includes('SYNTHETIC_B_CURRENT_ROW')); assert.ok(!page.text().includes('SYNTHETIC_A_RETIRED_ROW')); assert.equal(page.vm.tabLoading, false)
})
