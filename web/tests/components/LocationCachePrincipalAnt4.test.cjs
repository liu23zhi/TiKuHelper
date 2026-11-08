/* Native LocationCache regression: actual Vue/Antd4/currentUser/unsaved-draft helper.
 * Mock boundaries are deferred API and Swal only. All markers are synthetic.
 * The same file selects baseline/modified/rollback source with TIKU_WEB_SOURCE,
 * and its actual compiler/DOM helper with UI_TEST_HELPER. No network, UAPI or DB.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(root, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createRouter, createMemoryHistory, RouterView } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const A = { id: 74501, role: 'admin', username: 'SYNTHETIC_LOCATION_A', balance: 1 }
const B = { id: 74502, role: 'admin', username: 'SYNTHETIC_LOCATION_B', balance: 2 }
const names = ['getLocationCacheList', 'manualLookupLocationCache', 'refreshLocationCache', 'refreshLocationCacheBatch', 'deleteLocationCache', 'deleteLocationCacheBatch']
const calls = Object.fromEntries(names.map(name => [name, []]))
const swals = [], confirms = [], errors = [], notices = []
let wrappers = [], histories = [], deferSwal = false, popup = null, closes = 0, globalDestroys = 0
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const ok = data => ({ status: 200, data })
function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject, completed: false }
}
function transport(name, payload) {
  const call = { ...deferred(), actor: user.getStoredCurrentUser()?.id, payload: clone(payload) }
  const resolve = call.resolve, reject = call.reject
  call.resolve = value => { call.completed = true; resolve(value) }
  call.reject = value => { call.completed = true; reject(value) }
  calls[name].push(call)
  return call.promise
}
const apiPath = path.join(root, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = Object.fromEntries(names.map(name => [name, payload => transport(name, payload)]))
require.cache[apiPath] = api
const swalPath = require.resolve('sweetalert2')
const swal = new Module(swalPath); swal.loaded = true
swal.exports = {
  fire(options) {
    popup = document.createElement('div')
    const pending = { ...deferred(), options, popup }
    swals.push(pending)
    if (!deferSwal) { pending.completed = true; pending.resolve({ isConfirmed: true }) }
    return pending.promise
  },
  getPopup: () => popup,
  close: () => { closes += 1; popup = null },
  isVisible: () => Boolean(popup),
}
require.cache[swalPath] = swal
const originalConfirm = Antd.Modal.confirm.bind(Antd.Modal)
Antd.Modal.confirm = options => { const handle = originalConfirm(options); confirms.push({ options, handle }); return handle }
const originalDestroyAll = Antd.Modal.destroyAll.bind(Antd.Modal)
Antd.Modal.destroyAll = () => { globalDestroys += 1; return originalDestroyAll() }
for (const kind of ['success', 'error', 'warning']) {
  const original = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return original(...args) }
}
const Page = require(path.join(root, 'src/pages/LocationCache/index.vue'))
const row = (tag = 'A', kind = 'ip', source = '') => ({
  id: tag === 'A' ? 1 : 2, kind, lookupKey: kind === 'phone' ? '13800000001' : '2001:db8::11', source,
  address: `SYNTHETIC_ADDRESS_${tag}`, rawJson: JSON.stringify({ private: `SYNTHETIC_RAW_${tag}` }),
  fetchedAt: '2026-10-29T00:00:00Z', updatedAt: '2026-10-29T00:00:00Z',
})
const listing = (rows = [row()], extra = {}) => ok({ data: rows, total: rows.length, page: 1, pageSize: 20, configured: true, summary: { total: rows.length, withAddress: rows.length, withoutAddress: 0 }, ...extra })
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function last(name = names[0]) { const c = calls[name].at(-1); assert.ok(c, `ACTUAL_API_${name}`); return c }
const reads = () => calls.getLocationCacheList.length
const writes = () => names.slice(1).reduce((total, name) => total + calls[name].length, 0)
function button(w, label) { return w.findAll('button').find(b => b.text().replace(/\s/g, '') === label.replace(/\s/g, '')) }
function click(w, label) { const b = button(w, label); assert.ok(b, `NATIVE_BUTTON_${label}`); b.element.click() }
function callback(component, name) { const cb = component.vm.$.vnode.props?.[name]; assert.equal(typeof cb, 'function', `NATIVE_CALLBACK_${name}`); return cb }
function componentByText(w, type, text) { const c = w.findAllComponents(type).find(c => c.text().replace(/\s/g, '') === text.replace(/\s/g, '')); assert.ok(c, `NATIVE_COMPONENT_${text}`); return c }
function dialog(title) { const d = [...document.querySelectorAll('.ant-modal')].find(d => d.querySelector('.ant-modal-title')?.textContent.includes(title) && d.closest('.ant-modal-wrap')?.style.display !== 'none'); assert.ok(d, `NATIVE_MODAL_${title}`); return d }
function modalButton(d, text) { const b = [...d.querySelectorAll('.ant-modal-footer button')].find(b => b.textContent.replace(/\s/g, '') === text.replace(/\s/g, '')); assert.ok(b, `NATIVE_MODAL_BUTTON_${text}`); return b }
async function input(element, value) { element.value = value; element.dispatchEvent(new Event('input', { bubbles: true })); await settle() }
async function page(actor = A, kind = 'ip', loaded = true) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/adapter/ip-location-cache', component: Page, meta: { locationKind: 'ip' } },
    { path: '/adapter/phone-location-cache', component: Page, meta: { locationKind: 'phone' } },
    { path: '/elsewhere', component: { render: () => h('p', 'SYNTHETIC_OTHER_ROUTE') } },
  ] })
  await router.push(`/adapter/${kind}-location-cache`); await router.isReady()
  const host = mount({ render: () => h(RouterView) }, { attachTo: document.body, global: { plugins: [router, Antd], config: { errorHandler: error => errors.push(String(error?.stack || error)) } } })
  wrappers.push(host); histories.push(router.options.history); await settle()
  const w = host.findComponent(Page)
  if (loaded && reads()) { last().resolve(listing([row(actor?.id === B.id ? 'B' : 'A', kind)])); await settle() }
  return { w, host, router }
}
function finish(tag = 'B', kind = 'ip', extra = {}) { const c = last(); if (!c.completed) c.resolve(listing([row(tag, kind)], extra)) }
function state(w) { return clone({ rows: w.vm.rows, total: w.vm.pagination.total, page: w.vm.pagination.current, configured: w.vm.configured, summary: w.vm.summary, selected: w.vm.selectedRowKeys, count: w.vm.selectedCount, keywordInput: w.vm.keywordInput, manualKey: w.vm.manualKey, manualVisible: w.vm.manualVisible, detailRecord: w.vm.detailRecord, detailVisible: w.vm.detailVisible, error: w.vm.errorMessage, loading: w.vm.loading }) }
async function openManual(w, value = '198.51.100.21') { click(w, '手动添加'); await settle(); const d = dialog('手动添加'); await input(d.querySelector('input'), value); return d }
function witness(name, w) { console.log('LOCATION_CACHE_NATIVE_OBSERVED ' + JSON.stringify({ name, state: state(w), counts: Object.fromEntries(names.map(n => [n, calls[n].length])), errors })) }
function danger() {
  const modal = [...confirms].reverse().find(c => /所选/.test(c.options.title))
  const alert = [...swals].reverse().find(c => /所选/.test(c.options.title))
  const c = modal || alert; assert.ok(c, 'ACTUAL_EXPLICIT_DANGER_CONFIRMATION'); return c
}
async function answerDanger(c = danger(), confirmed = true, native = false) {
  if (c.handle) {
    if (native) {
      const modal = [...document.querySelectorAll('.ant-modal-confirm')].filter(d => d.querySelector('.ant-modal-confirm-title')?.textContent === c.options.title).at(-1)
      assert.ok(modal, 'ACTUAL_ANTD_CONFIRM_DIALOG')
      const selector = confirmed ? '.ant-modal-confirm-btns .ant-btn-primary' : '.ant-modal-confirm-btns .ant-btn:not(.ant-btn-primary)'
      const button = modal.querySelector(selector); assert.ok(button, 'ACTUAL_ANTD_CONFIRM_BUTTON'); button.click()
    } else if (confirmed) c.options.onOk?.()
    else c.options.onCancel?.()
  } else { c.completed = true; c.resolve({ isConfirmed: confirmed }) }
  await settle()
}
async function acceptDangerIfNeeded() { if (danger().handle) await answerDanger(danger(), true, true) }
async function openPop(w, all = false) {
  click(w, all ? '全部删除' : '删除'); await settle(); await new Promise(resolve => setTimeout(resolve, 90)); await settle()
  const pop = w.findAllComponents(Antd.Popconfirm).find(c => String(c.props('title')).includes(all ? '删除当前已读取筛选范围的全部归属地缓存？' : '下次展示'))
  assert.ok(pop, 'ACTUAL_POPCONFIRM'); return pop
}
async function confirmPop(w, all = false) {
  await openPop(w, all)
  const title = all ? '删除当前已读取筛选范围的全部归属地缓存？' : '下次展示'
  const overlay = [...document.querySelectorAll('.ant-popover')].find(e => e.textContent.includes(title) && !e.classList.contains('ant-popover-hidden'))
  assert.ok(overlay, 'ACTUAL_NATIVE_POPCONFIRM_OVERLAY ' + JSON.stringify([...document.querySelectorAll('.ant-popover')].map(e => ({ text: e.textContent, class: e.className }))))
  const button = [...overlay.querySelectorAll('button')].find(b => b.textContent.replace(/\s/g, '') === (all ? '全部删除' : '删除'))
  assert.ok(button, 'ACTUAL_NATIVE_POPCONFIRM_CONFIRM_BUTTON'); button.click(); await settle()
}
afterEach(async () => {
  for (const host of wrappers) if (host.exists()) host.unmount()
  wrappers = []
  for (const name of names) for (const c of calls[name]) if (!c.completed) c.resolve(name === names[0] ? listing([]) : ok({}))
  for (const c of swals) if (!c.completed) { c.completed = true; c.resolve({ isDismissed: true }) }
  await settle()
  for (const history of histories) history.destroy()
  histories = []
  originalDestroyAll(); Antd.message.destroy(); await settle()
  user.clearStoredCurrentUser(); document.body.replaceChildren(); window.localStorage.clear(); window.sessionStorage.clear()
  names.forEach(name => { calls[name].length = 0 }); swals.length = 0; confirms.length = 0; notices.length = 0; errors.length = 0
  deferSwal = false; popup = null; closes = 0; globalDestroys = 0
})

// The following six positive contracts intentionally pass the archived original page.
for (const kind of ['ip', 'phone']) test(`LC_POSITIVE native ${kind} list, summary, source display and IPv6 identity retain original semantics`, async () => {
  const { w } = await page(A, kind, false)
  assert.deepEqual(last().payload, { kind, keyword: '', page: 1, pageSize: 20 })
  const rows = kind === 'ip' ? [row('A', kind, ''), row('COMMERCIAL', kind, 'commercial'), { ...row('LEGACY', kind, 'legacy'), address: '' }] : [row('A', kind)]
  last().resolve(listing(rows, { total: 23, summary: { total: 23, withAddress: 20, withoutAddress: 3 } })); await settle()
  assert.equal(w.findAll('.ant-table-tbody .ant-table-row').length, rows.length)
  assert.ok(w.text().includes('SYNTHETIC_ADDRESS_A'))
  assert.deepEqual(clone(w.vm.summary), { total: 23, withAddress: 20, withoutAddress: 3 })
  assert.equal(w.vm.recordIdentity(rows[0]), `${kind}:${rows[0].lookupKey}:default`)
  if (kind === 'ip') { assert.ok(w.text().includes('商业增强')); assert.ok(w.text().includes('历史（来源未记录）')); assert.equal(w.vm.recordIdentity(rows[1]), 'ip:2001:db8::11:commercial') }
  else assert.equal(w.vm.columns.some(c => c.key === 'source'), false)
  witness(`positive-${kind}`, w)
})

test('LC_POSITIVE missing and unknown configuration permit cached read but zero refresh/manual UAPI actions', async () => {
  const { w } = await page(A, 'ip', false)
  for (const configured of [false, undefined]) {
    if (configured === undefined) { w.vm.load(); await settle() }
    last().resolve(listing([row()], { configured })); await settle()
    const before = writes()
    assert.equal(button(w, '手动添加').attributes('disabled') !== undefined, true)
    w.vm.manualKey = '198.51.100.22'; w.vm.manualLookup(); w.vm.refresh(row()); w.vm.handleSelectAll(); w.vm.refreshSelected(); await settle()
    assert.equal(writes(), before, 'NO_UAPI_WRITE_WITHOUT_CONFIGURED_TRUE')
    if (configured === false) assert.ok(w.text().includes('未配置 UApiPro 密钥'))
  }
})

test('LC_POSITIVE manual and row refresh/delete preserve IPv6 commercial recordSource and exact API payloads', async () => {
  const { w } = await page()
  w.vm.setQuerySource('commercial'); await settle()
  const d = await openManual(w, '  2001:db8::33  ')
  modalButton(d, '查询并保存').click(); await settle()
  assert.deepEqual(last('manualLookupLocationCache').payload, { kind: 'ip', lookupKey: '2001:db8::33', source: 'commercial' })
  last('manualLookupLocationCache').resolve(ok({ configured: true, data: row('SAVED') })); await settle(); finish('SAVED'); await settle()
  click(w, '重新查询'); await settle()
  assert.deepEqual(last('refreshLocationCache').payload, { kind: 'ip', lookupKey: '2001:db8::11', source: 'commercial', recordSource: '' })
  last('refreshLocationCache').resolve(ok({ configured: true, data: row('REFRESHED', 'ip', 'commercial') })); await settle(); finish('REFRESHED'); await settle()
  await confirmPop(w)
  assert.deepEqual(last('deleteLocationCache').payload, { kind: 'ip', lookupKey: '2001:db8::11', source: '' })
})

test('LC_POSITIVE phone manual and refresh always send empty source independently of stored commercial choice', async () => {
  const { w } = await page(A, 'phone')
  w.vm.setQuerySource('commercial'); await settle()
  const d = await openManual(w, ' 13800000002 ')
  modalButton(d, '查询并保存').click(); await settle()
  assert.deepEqual(last('manualLookupLocationCache').payload, { kind: 'phone', lookupKey: '13800000002', source: '' })
  last('manualLookupLocationCache').resolve(ok({ configured: true })); await settle(); finish('PHONE', 'phone'); await settle()
  click(w, '重新查询'); await settle()
  assert.deepEqual(last('refreshLocationCache').payload, { kind: 'phone', lookupKey: '13800000001', source: '', recordSource: '' })
})

test('LC_POSITIVE cross-page all/except and source scope preserve IPv6 literal batch business payload', async () => {
  const { w } = await page(A, 'ip', false)
  const rows = [row('PLAIN'), row('COMMERCIAL', 'ip', 'commercial')]
  last().resolve(listing(rows, { total: 42 })); await settle()
  w.vm.keywordInput = '  SYNTHETIC_FILTER  '; w.vm.reload(); await settle(); last().resolve(listing(rows, { total: 42 })); await settle()
  w.vm.setQuerySource('commercial'); await settle(); click(w, '全选全部'); await settle()
  const selection = w.findComponent(Antd.Table).props('rowSelection')
  selection.onChange([w.vm.recordIdentity(rows[1])]); await settle()
  assert.equal(w.vm.selectedCount, 41)
  click(w, '重新查询所选'); await settle(); await acceptDangerIfNeeded()
  assert.deepEqual(last('refreshLocationCacheBatch').payload, { kind: 'ip', keyword: 'SYNTHETIC_FILTER', source: '', items: [], all: true, except: [{ lookupKey: '2001:db8::11', source: '' }], querySource: 'commercial' })
  last('refreshLocationCacheBatch').resolve(ok({ configured: true, data: { refreshed: 41, failed: 0 } })); await settle(); finish(); await settle()
  click(w, '全选本页'); await settle(); click(w, '删除所选'); await settle(); await acceptDangerIfNeeded()
  assert.deepEqual(last('deleteLocationCacheBatch').payload, { kind: 'ip', keyword: 'SYNTHETIC_FILTER', source: '', items: [{ lookupKey: '2001:db8::11', source: '' }], all: false, except: [] })
})

test('LC_PRINCIPAL no actor or invalid admin id or revoked role performs zero private read and write', async () => {
  for (const actor of [null, { role: 'admin', username: 'LABEL_ONLY' }, { ...A, id: 0 }, { ...A, id: '1e3' }, { ...A, role: 'user' }]) {
    const { w, host } = await page(actor, 'ip', false)
    assert.equal(reads(), 0, 'UNAUTHORIZED_ZERO_PRIVATE_READ')
    w.vm.load(); w.vm.reload(); w.vm.showDetails(row()); w.vm.manualKey = '198.51.100.23'; w.vm.manualLookup(); w.vm.refresh(row()); w.vm.remove(row()); w.vm.deleteAll(); await settle()
    assert.equal(reads(), 0); assert.equal(writes(), 0); assert.equal(w.vm.detailVisible, false)
    host.unmount()
  }
  const { w } = await page({ ...A, id: String(A.id) })
  assert.equal(reads(), 1, 'VALID_DECIMAL_STRING_ID_ALLOWED'); assert.equal(w.vm.rows.length, 1)
})

for (const kind of ['ip', 'phone']) test(`LC_PRINCIPAL ${kind} A-B-A late list cannot publish rows summary/config or clear new loading`, async () => {
  const { w } = await page(A, kind, false), originalA = last()
  const element = w.get('.location-page').element
  user.setStoredCurrentUser(B)
  assert.deepEqual(clone(w.vm.rows), [], 'SYNC_CLEAR_PRIVATE_ROWS')
  assert.equal(w.vm.configured, undefined)
  await settle(); assert.equal(reads(), 2); assert.equal(last().actor, B.id)
  const firstB = last()
  user.setStoredCurrentUser(A); await settle(); assert.equal(reads(), 3)
  const before = state(w)
  originalA.resolve(listing([row('A_ORIGINAL_LATE', kind)], { total: 900, configured: true })); firstB.reject(new Error('SYNTHETIC_B_LATE_RAW_SECRET'))
  await settle()
  assert.deepEqual(state(w), before, 'ABA_RETIRED_READ_NO_EFFECT'); assert.equal(w.vm.loading, true)
  assert.equal(w.get('.location-page').element, element, 'PAGE_NOT_REMOUNTED_ON_ACTOR_CHANGE')
  finish('A_CURRENT', kind); await settle()
  assert.equal(w.vm.rows[0].address, 'SYNTHETIC_ADDRESS_A_CURRENT'); assert.equal(w.vm.loading, false)
  assert.equal(document.body.textContent.includes('SYNTHETIC_ADDRESS_A_ORIGINAL_LATE'), false)
  witness(`ABA-${kind}`, w)
})

test('LC_PRINCIPAL balance-only change keeps selection detail dirty draft and scope without new GET', async () => {
  const { w } = await page(); click(w, '全选本页'); click(w, '查看详情'); await settle()
  await openManual(w, '198.51.100.24'); const before = state(w), count = reads()
  user.setStoredCurrentUser({ ...A, balance: 99.123456 }); await settle()
  assert.deepEqual(state(w), before); assert.equal(reads(), count); assert.equal(w.vm.manualVisible, true)
})

test('LC_PRINCIPAL legal Antd input then same-tick actor switch clears retained native private input/detail bytes without null-ref', async () => {
  const { w } = await page(); click(w, '查看详情'); await settle()
  const d = await openManual(w, '198.51.100.25'), oldInput = d.querySelector('input')
  oldInput.value = '198.51.100.26'; oldInput.dispatchEvent(new Event('input', { bubbles: true }))
  assert.equal(w.vm.manualKey, '198.51.100.26', 'LEGAL_NATIVE_INPUT_REACHED_ACTUAL_DRAFT')
  const detail = document.querySelector('.raw-response')
  assert.ok(detail?.textContent.includes('SYNTHETIC_RAW_A'))
  user.setStoredCurrentUser(B)
  assert.equal(oldInput.value, '', 'SYNC_CLEAR_RETAINED_PRIVATE_INPUT_BYTES')
  assert.equal(detail.textContent.includes('SYNTHETIC_RAW_A'), false, 'SYNC_CLEAR_RETAINED_PRIVATE_DETAIL_BYTES')
  assert.equal(w.vm.manualKey, ''); assert.equal(w.vm.manualVisible, false); assert.equal(w.vm.detailVisible, false)
  await settle(); finish(); await settle(); assert.deepEqual(errors, [], 'NO_ANTD_NULL_REF_ON_LEGAL_INPUT_SWITCH')
})

test('LC_PRINCIPAL retained native buttons popconfirm rowSelection modal update/ok/afterClose and Swal are inert after ownership', async () => {
  const { w } = await page(); click(w, '全选本页'); click(w, '查看详情'); await settle()
  const oldToolbar = callback(componentByText(w, Antd.Button, '刷新列表'), 'onClick')
  const oldRefresh = callback(componentByText(w, Antd.Button, '重新查询'), 'onClick')
  const oldPop = callback(await openPop(w), 'onConfirm')
  const oldSelect = w.findComponent(Antd.Table).props('rowSelection').onChange
  await openManual(w, '198.51.100.27')
  const manual = w.findAllComponents(Antd.Modal).find(c => String(c.props('title')).includes('手动添加'))
  const oldOk = callback(manual, 'onOk'), oldOpen = callback(manual, 'onUpdate:open')
  const oldAfterClose = manual.vm.$.vnode.props?.afterClose
  assert.equal(typeof oldAfterClose, 'function', 'ACTUAL_MANUAL_AFTER_CLOSE_CAPTURED')
  const oldInput = w.findAllComponents(Antd.Input).find(c => String(c.props('placeholder')).includes('公网'))
  const oldUpdate = callback(oldInput, 'onUpdate:value')
  deferSwal = true; click(w, '删除所选'); await settle()
  const pending = danger()
  user.setStoredCurrentUser(B); await settle(); finish(); await settle()
  await openManual(w, '198.51.100.28'); const before = state(w), count = reads(), writesBefore = writes()
  oldToolbar(); oldRefresh(); oldPop(); oldSelect(['ip:2001:db8::11:default']); oldOk(); oldOpen(false); oldUpdate('SYNTHETIC_OLD_MODEL_UPDATE'); oldAfterClose?.(); await answerDanger(pending); await settle()
  assert.deepEqual(state(w), before); assert.equal(reads(), count); assert.equal(writes(), writesBefore)
  assert.equal(w.vm.manualKey, '198.51.100.28'); assert.deepEqual(errors, [])
})

for (const name of ['manualLookupLocationCache', 'refreshLocationCache', 'deleteLocationCache', 'refreshLocationCacheBatch', 'deleteLocationCacheBatch']) test(`LC_PRINCIPAL pending ${name} old completion/finally cannot reload or clear B busy`, async () => {
  const { w } = await page()
  const invoke = async () => {
    if (name === 'manualLookupLocationCache') { const d = await openManual(w, '198.51.100.29'); modalButton(d, '查询并保存').click() }
    else if (name === 'refreshLocationCache') click(w, '重新查询')
    else if (name === 'deleteLocationCache') await confirmPop(w)
    else { click(w, '全选本页'); await settle(); click(w, name === 'refreshLocationCacheBatch' ? '重新查询所选' : '删除所选'); await settle(); await acceptDangerIfNeeded() }
    await settle(); return last(name)
  }
  const old = await invoke()
  user.setStoredCurrentUser(B); await settle(); finish(); await settle()
  const current = await invoke(), count = reads()
  const busy = () => name === 'manualLookupLocationCache' ? w.vm.manualSubmitting : name === 'refreshLocationCache' ? Object.values(w.vm.refreshing).some(Boolean) : name === 'deleteLocationCache' ? Object.values(w.vm.removing).some(Boolean) : name === 'refreshLocationCacheBatch' ? w.vm.batchRefreshing : w.vm.batchDeleting
  assert.equal(busy(), true)
  old.resolve(ok({ configured: true, data: row('A_WRITE_LATE'), deleted: 1 })); await settle()
  assert.equal(reads(), count, 'RETIRED_WRITE_NO_CONTINUATION_GET'); assert.equal(busy(), true, 'RETIRED_FINALLY_CANNOT_CLEAR_CURRENT_BUSY')
  assert.equal(w.vm.rows[0].address, 'SYNTHETIC_ADDRESS_B'); assert.equal(current.completed, false)
})

test('LC_PRINCIPAL explicit batch confirmations snapshot selection/query before await and reject cancel', async () => {
  const { w } = await page(); click(w, '全选本页'); await settle(); deferSwal = true
  click(w, '删除所选'); await settle()
  assert.equal(writes(), 0, 'DANGER_NEEDS_EXPLICIT_CONFIRM')
  const first = danger(); if (!first.handle) assert.equal(first.options.showCancelButton, true)
  await answerDanger(first, false, Boolean(first.handle)); assert.equal(writes(), 0)
  click(w, '删除所选'); await settle(); const pending = danger()
  w.vm.handleClearSelection(); w.vm.keywordInput = 'SYNTHETIC_NEW_QUERY_NOT_CONFIRMED'; w.vm.reload(); await settle()
  await answerDanger(pending)
  assert.equal(calls.deleteLocationCacheBatch.length, 0, 'QUERY_CHANGE_RETIRES_OLD_CONFIRMATION')
  finish('NEW_QUERY'); await settle(); click(w, '全选本页'); await settle(); click(w, '删除所选'); await settle()
  const sameScope = danger(); w.vm.handleClearSelection(); await answerDanger(sameScope)
  assert.deepEqual(last('deleteLocationCacheBatch').payload, { kind: 'ip', keyword: 'SYNTHETIC_NEW_QUERY_NOT_CONFIRMED', source: '', items: [{ lookupKey: '2001:db8::11', source: '' }], all: false, except: [] }, 'SAME_SCOPE_CONFIRM_USES_CAPTURED_SELECTION')
})

test('LC_PRINCIPAL two query rounds and two kind rounds retire old summary/config/rows including old true config', async () => {
  const { w, router } = await page(); w.vm.load(); await settle(); const oldIp = last()
  w.vm.keywordInput = 'SYNTHETIC_QUERY_1'; w.vm.reload(); await settle(); const oldQuery = last()
  w.vm.keywordInput = 'SYNTHETIC_QUERY_2'; w.vm.reload(); await settle(); const newest = last()
  newest.resolve(listing([row('Q2')], { configured: false, total: 2, summary: { total: 2, withAddress: 1, withoutAddress: 1 } })); await settle()
  const q2 = state(w); oldQuery.resolve(listing([row('Q1_LATE')], { configured: true, total: 991 })); oldIp.resolve(listing([row('IP_OLD')], { configured: true })); await settle()
  assert.deepEqual(state(w), q2); assert.equal(w.vm.isRefreshDisabled(), true)
  await router.push('/adapter/phone-location-cache'); await settle(); const phone = last()
  await router.push('/adapter/ip-location-cache'); await settle(); const ip = last()
  assert.equal(ip.payload.kind, 'ip'); phone.resolve(listing([row('PHONE_LATE', 'phone')], { configured: true, total: 999 })); await settle()
  assert.equal(w.vm.rows.length, 0); assert.equal(w.vm.loading, true); assert.equal(w.vm.configured, undefined)
  ip.resolve(listing([row('IP_CURRENT')], { configured: false })); await settle()
  assert.equal(w.vm.rows[0].address, 'SYNTHETIC_ADDRESS_IP_CURRENT'); assert.equal(w.vm.isRefreshDisabled(), true)
})

test('LC_PRINCIPAL refresh failure keeps known rows selection/draft and persistent generic error without raw server/key/query leak; real empty is not failure', async () => {
  const { w } = await page(); click(w, '全选本页'); await openManual(w, '198.51.100.30')
  const beforeRows = clone(w.vm.rows), selected = clone(w.vm.selectedRowKeys)
  w.vm.load(); await settle(); last().reject({ message: 'SYNTHETIC_SERVER_RAW_KEY_QUERY_198.51.100.30', response: { data: { configured: true, message: 'SYNTHETIC_SECRET_UAPI_KEY_198.51.100.30' } } }); await settle()
  assert.deepEqual(clone(w.vm.rows), beforeRows); assert.deepEqual(clone(w.vm.selectedRowKeys), selected); assert.equal(w.vm.manualKey, '198.51.100.30')
  assert.ok(w.vm.errorMessage); assert.ok(w.find('.ant-alert-error').exists()); assert.equal(w.vm.loading, false)
  assert.equal(w.vm.errorMessage.includes('SYNTHETIC'), false); assert.equal(w.vm.errorMessage.includes('198.51.100.30'), false)
  await settle(); assert.ok(w.vm.errorMessage, 'ERROR_PERSISTS_UNTIL_EXPLICIT_NEW_ACTION')
  w.vm.load(); await settle(); last().resolve(listing([], { total: 0, configured: false })); await settle()
  assert.equal(w.vm.rows.length, 0); assert.equal(w.vm.errorMessage, ''); assert.equal(w.find('.ant-alert-error').exists(), false)
})

test('LC_PRINCIPAL owner retirement closes only owned Swal/modal and does not globally destroy or steal focus', async () => {
  const { w } = await page(); click(w, '全选本页'); await settle(); deferSwal = true; click(w, '删除所选'); await settle()
  const ownedConfirmation = danger(), owned = ownedConfirmation.popup
  if (!ownedConfirmation.handle) assert.equal(popup, owned)
  const unrelated = Antd.Modal.confirm({ title: 'SYNTHETIC_UNRELATED_CONFIRM', content: 'outside page ownership', autoFocusButton: null, focusTriggerAfterClose: false })
  const outside = document.createElement('button'); outside.textContent = 'SYNTHETIC_FOCUS'; document.body.appendChild(outside); outside.focus()
  popup = document.createElement('div') // A later external Swal replaced the page-owned popup.
  user.setStoredCurrentUser(B); await settle()
  assert.equal(closes, 0, 'DO_NOT_CLOSE_EXTERNAL_SWAL')
  assert.equal(globalDestroys, 0, 'NO_GLOBAL_MODAL_DESTROY')
  assert.ok(document.body.textContent.includes('SYNTHETIC_UNRELATED_CONFIRM'))
  assert.equal(document.activeElement, outside, 'RETIRED_PAGE_DOES_NOT_STEAL_FOCUS')
  unrelated.destroy(); finish(); await settle()
})

test('LC_PRINCIPAL manual dirty draft prompts route leave, cancel preserves input, explicit leave has no null-ref', async () => {
  const { w, router } = await page(); await openManual(w, '198.51.100.31')
  const leaving = router.push('/elsewhere'); await settle()
  assert.equal(confirms.length, 1, 'ACTUAL_UNSAVED_DRAFT_GUARD_PROMPTS')
  assert.equal(router.currentRoute.value.path, '/adapter/ip-location-cache')
  confirms[0].options.onCancel(); await leaving; await settle()
  assert.equal(w.vm.manualKey, '198.51.100.31'); assert.equal(w.vm.manualVisible, true)
  const again = router.push('/elsewhere'); await settle(); confirms.at(-1).options.onOk(); await again; await settle()
  assert.equal(router.currentRoute.value.path, '/elsewhere'); assert.deepEqual(errors, [], 'NORMAL_ROUTE_LEAVE_NO_NULL_REF')
})
for (const transition of ['role', 'logout']) test(`LC_PRINCIPAL loaded ${transition} synchronously retires private data and old true configuration with zero new IO`, async () => {
  const { w } = await page(); click(w, '全选本页'); click(w, '查看详情'); await settle(); await openManual(w, '198.51.100.32')
  const count = reads(), written = writes()
  if (transition === 'role') user.setStoredCurrentUser({ ...A, role: 'user' }); else user.clearStoredCurrentUser()
  assert.deepEqual(clone(w.vm.rows), []); assert.equal(w.vm.selectedCount, 0); assert.equal(w.vm.manualKey, '')
  assert.equal(w.vm.manualVisible, false); assert.equal(w.vm.detailVisible, false); assert.deepEqual(clone(w.vm.detailRecord), {})
  assert.equal(w.vm.configured, undefined); assert.equal(w.vm.isRefreshDisabled(), true)
  w.vm.manualKey = '198.51.100.32'; w.vm.manualLookup(); w.vm.refresh(row()); w.vm.remove(row()); w.vm.deleteAll(); w.vm.load(); await settle()
  assert.equal(reads(), count); assert.equal(writes(), written)
  assert.equal(document.body.textContent.includes('SYNTHETIC_RAW_A'), false)
})

for (const scope of ['query', 'kind']) test(`LC_PRINCIPAL retained native table/dialog callbacks are inert across ${scope} scope without actor switch`, async () => {
  const { w, router } = await page(); click(w, '全选本页'); await openManual(w, '198.51.100.33')
  const oldRefresh = callback(componentByText(w, Antd.Button, '重新查询'), 'onClick')
  const oldRemove = callback(await openPop(w), 'onConfirm')
  const oldSelection = w.findComponent(Antd.Table).props('rowSelection').onChange
  const oldManual = w.findAllComponents(Antd.Modal).find(c => String(c.props('title')).includes('手动添加'))
  const oldOk = callback(oldManual, 'onOk'), oldOpen = callback(oldManual, 'onUpdate:open')
  if (scope === 'query') { w.vm.keywordInput = 'SYNTHETIC_NEW_SCOPE'; w.vm.reload(); await settle() }
  else { const leaving = router.push('/adapter/phone-location-cache'); await settle(); const guard = confirms.find(c => /未保存/.test(c.options.title)); guard?.options.onOk(); await leaving; await settle() }
  finish('NEW_SCOPE', scope === 'kind' ? 'phone' : 'ip'); await settle()
  await openManual(w, scope === 'kind' ? '13800000005' : '198.51.100.34')
  const before = state(w), readCount = reads(), writeCount = writes()
  oldRefresh(); oldRemove(); oldSelection(['ip:2001:db8::11:default']); oldOk(); oldOpen(false); await settle()
  assert.deepEqual(state(w), before); assert.equal(reads(), readCount); assert.equal(writes(), writeCount)
})
test('LC_PRINCIPAL latest missing/unknown configuration retires an earlier configured true without enabling UAPI', async () => {
  const { w } = await page()
  w.vm.load(); await settle(); last().resolve(listing([row('UNKNOWN')], { configured: undefined })); await settle()
  assert.equal(w.vm.configured, undefined, 'UNKNOWN_LATEST_CONFIG_IS_NOT_OLD_TRUE')
  assert.equal(w.vm.isRefreshDisabled(), true)
  const before = writes(); w.vm.manualKey = '198.51.100.35'; w.vm.manualLookup(); w.vm.refresh(w.vm.rows[0]); w.vm.handleSelectAll(); w.vm.refreshSelected(); await settle()
  assert.equal(writes(), before, 'UNKNOWN_CONFIG_ZERO_UAPI_ACTIONS')
})

test('LC_PRINCIPAL retired delete-all completion cannot clear B busy reset B page or continue list GET', async () => {
  const { w } = await page()
  const invoke = async () => { await confirmPop(w, true); return last('deleteLocationCacheBatch') }
  const old = await invoke(); assert.deepEqual(old.payload, { kind: 'ip', keyword: '', source: '', items: [], all: true, except: [] })
  user.setStoredCurrentUser(B); await settle(); finish(); await settle()
  w.vm.pagination.current = 3; w.vm.load(); await settle(); finish('B', 'ip', { page: 3, total: 60 }); await settle()
  assert.equal(w.vm.pagination.current, 3, 'B actual page3 read is confirmed before its original delete-all admission')
  const current = await invoke(), count = reads()
  assert.equal(w.vm.allDeleting, true)
  old.resolve(ok({ deleted: 9 })); await settle()
  assert.equal(w.vm.allDeleting, true); assert.equal(w.vm.pagination.current, 3); assert.equal(reads(), count)
  assert.equal(current.completed, false)
})
test('LC_FIDELITY source preference is future payload only; independent ordinary/commercial row queries remain concurrently busy', async () => {
  const { w } = await page(A, 'ip', false), rows = [row('ORDINARY'), row('COMMERCIAL', 'ip', 'commercial')]
  last().resolve(listing(rows)); await settle()
  const refreshButtons = () => w.findAll('button').filter(b => b.text().replace(/\s/g, '') === '重新查询')
  w.vm.setQuerySource(''); await settle(); refreshButtons()[0].element.click(); await settle(); const ordinary = last('refreshLocationCache')
  w.vm.setQuerySource('commercial'); await settle(); refreshButtons()[1].element.click(); await settle(); const commercial = last('refreshLocationCache')
  assert.deepEqual(ordinary.payload, { kind: 'ip', lookupKey: '2001:db8::11', source: '', recordSource: '' })
  assert.deepEqual(commercial.payload, { kind: 'ip', lookupKey: '2001:db8::11', source: 'commercial', recordSource: 'commercial' })
  assert.equal(w.vm.refreshing['ip:2001:db8::11:default'], true); assert.equal(w.vm.refreshing['ip:2001:db8::11:commercial'], true)
  ordinary.resolve(ok({ configured: true, data: row('ORDINARY_UPDATED') })); await settle()
  assert.equal(w.vm.refreshing['ip:2001:db8::11:commercial'], true, 'SOURCE_PREFERENCE_CHANGE_DID_NOT_RETIRE_INDEPENDENT_OPERATION')
  last().resolve(listing(rows)); await settle()
  commercial.resolve(ok({ configured: true, data: row('COMMERCIAL_UPDATED', 'ip', 'commercial') })); await settle(); last().resolve(listing(rows)); await settle()
  assert.equal(w.vm.refreshing['ip:2001:db8::11:default'], false); assert.equal(w.vm.refreshing['ip:2001:db8::11:commercial'], false)
})

test('LC_PRINCIPAL allowed local leave followed by downstream navigation cancellation retains live rows and manual draft', async () => {
  const { w, router } = await page(); await openManual(w, '198.51.100.36')
  const beforeRows = clone(w.vm.rows), beforeReads = reads()
  const removeGuard = router.beforeEach(to => to.path === '/elsewhere' ? false : true)
  const leaving = router.push('/elsewhere'); await settle()
  const guard = confirms.find(c => /未保存/.test(c.options.title)); assert.ok(guard, 'ACTUAL_LOCAL_DRAFT_LEAVE_GUARD')
  guard.options.onOk(); await leaving; await settle()
  assert.equal(router.currentRoute.value.path, '/adapter/ip-location-cache')
  assert.deepEqual(clone(w.vm.rows), beforeRows); assert.equal(w.vm.manualVisible, true); assert.equal(w.vm.manualKey, '198.51.100.36')
  const d = dialog('手动添加'); await input(d.querySelector('input'), '198.51.100.37')
  assert.equal(w.vm.manualKey, '198.51.100.37', 'CANCELLED_NAVIGATION_KEEPS_NATIVE_EDITOR_LIVE')
  assert.equal(reads(), beforeReads); assert.deepEqual(errors, []); removeGuard()
})
test('LC_PRINCIPAL same-owner list reload and native keyword filter keep one pending row query busy without duplicate or stale follow-up GET', async () => {
  const { w } = await page(); click(w, '重新查询'); await settle()
  const pending = last('refreshLocationCache'), identity = w.vm.recordIdentity(w.vm.rows[0])
  const originalPayload = clone(pending.payload)
  assert.equal(w.vm.refreshing[identity], true)
  click(w, '刷新列表'); await settle()
  assert.equal(w.vm.refreshing[identity], true, 'EXPLICIT_LIST_RELOAD_DOES_NOT_RELEASE_PENDING_QUERY_BUSY')
  last().resolve(listing([row('RELOADED')])); await settle()
  click(w, '重新查询'); await settle(); assert.equal(calls.refreshLocationCache.length, 1, 'RELOADED_SAME_ROW_CANNOT_DUPLICATE_PENDING_QUERY')
  await input(w.get('input[placeholder="查询值 / 归属地"]').element, 'SYNTHETIC_CURRENT_FILTER')
  w.get('.ant-input-search-button').element.click(); await settle()
  assert.equal(last().payload.keyword, 'SYNTHETIC_CURRENT_FILTER')
  last().resolve(listing([row('FILTER_CURRENT')])); await settle()
  assert.equal(w.vm.refreshing[identity], true, 'FILTERED_SAME_ROW_STILL_HAS_PENDING_QUERY_BUSY')
  click(w, '重新查询'); await settle(); assert.equal(calls.refreshLocationCache.length, 1)
  const readCount = reads()
  pending.resolve(ok({ configured: true, data: row('OLD_ROW_RESPONSE') })); await settle()
  assert.equal(reads(), readCount, 'OLD_QUERY_CANNOT_CONTINUE_GET_AFTER_NEW_COMMITTED_QUERY')
  assert.equal(w.vm.rows[0].address, 'SYNTHETIC_ADDRESS_FILTER_CURRENT'); assert.deepEqual(pending.payload, originalPayload)
  assert.equal(w.vm.refreshing[identity], false, 'OWN_PENDING_REQUEST_RELEASES_ITS_BUSY_WHEN_COMPLETE')
  click(w, '重新查询'); await settle(); assert.equal(calls.refreshLocationCache.length, 2, 'EXPLICIT_NEW_QUERY_ALLOWED_AFTER_ORIGINAL_COMPLETION')
})
test('LC_PRINCIPAL explicitly discarding pending manual dialog cannot dispatch twice or paint/toast/reload after close; real completion releases busy', async () => {
  const { w } = await page(), d = await openManual(w, '198.51.100.38')
  const oldOk = modalButton(d, '查询并保存'); oldOk.click(); await settle()
  const pending = last('manualLookupLocationCache'), readCount = reads(), noticeCount = notices.length
  modalButton(d, '取消').click(); await settle()
  const discard = confirms.find(c => /未保存的查询值/.test(c.options.title)); assert.ok(discard, 'EXPLICIT_DISCARD_PENDING_MANUAL_DRAFT')
  await answerDanger(discard, true, true)
  assert.equal(w.vm.manualVisible, false); assert.equal(w.vm.manualKey, ''); assert.equal(w.vm.manualSubmitting, true, 'PENDING_TRANSPORT_STILL_BUSY_AFTER_DIALOG_DISCARD')
  click(w, '手动添加'); oldOk.click(); await settle()
  assert.equal(calls.manualLookupLocationCache.length, 1, 'CLOSE_REOPEN_CANNOT_DUPLICATE_PENDING_TRANSPORT')
  assert.equal(w.vm.manualVisible, false)
  pending.resolve(ok({ configured: true, data: row('CLOSED_MANUAL_RESPONSE') })); await settle()
  assert.equal(reads(), readCount, 'CLOSED_DIALOG_RESPONSE_CANNOT_CONTINUE_GET'); assert.equal(notices.length, noticeCount, 'CLOSED_DIALOG_RESPONSE_CANNOT_TOAST')
  assert.equal(w.vm.rows[0].address, 'SYNTHETIC_ADDRESS_A'); assert.equal(w.vm.manualSubmitting, false)
  const current = await openManual(w, '198.51.100.39'); modalButton(current, '查询并保存').click(); await settle()
  assert.equal(calls.manualLookupLocationCache.length, 2, 'EXPLICIT_NEW_MANUAL_QUERY_ALLOWED_AFTER_REAL_COMPLETION')
  assert.deepEqual(last('manualLookupLocationCache').payload, { kind: 'ip', lookupKey: '198.51.100.39', source: '' })
})
test('LC_PRINCIPAL failed new keyword cannot authorize retained old-query rows/count for select-all batch; same-parameter failed refresh stays actionable', async () => {
  const { w } = await page(A, 'ip', false)
  const retained = row('LAST_SUCCESSFUL_FILTER')
  last().resolve(listing([retained], { total: 9, summary: { total: 9, withAddress: 9, withoutAddress: 0 } })); await settle()
  click(w, '刷新列表'); await settle()
  assert.equal(last().payload.keyword, '')
  last().reject({ response: { status: 503, data: { configured: true, message: 'SYNTHETIC_SAME_QUERY_FAILURE' } } }); await settle()
  assert.equal(w.vm.rows[0].address, retained.address)
  click(w, '全选全部'); await settle(); assert.equal(w.vm.selectedCount, 9, 'SAME_PARAMETERS_KEEP_LAST_SUCCESSFUL_DATA_ACTIONABLE')
  deferSwal = true; click(w, '删除所选'); await settle(); await answerDanger(danger(), false, Boolean(danger().handle))
  assert.equal(calls.deleteLocationCacheBatch.length, 0); click(w, '取消全部全选'); await settle()
  await input(w.get('input[placeholder="查询值 / 归属地"]').element, 'SYNTHETIC_FAILED_NEW_FILTER')
  w.get('.ant-input-search-button').element.click(); await settle()
  assert.equal(last().payload.keyword, 'SYNTHETIC_FAILED_NEW_FILTER')
  last().reject({ response: { status: 503, data: { configured: true, message: 'SYNTHETIC_NEW_QUERY_FAILURE' } } }); await settle()
  assert.equal(w.vm.rows[0].address, retained.address, 'KNOWN_ROWS_RETAINED_NOT_FAKE_EMPTY')
  assert.equal(w.vm.pagination.total, 9)
  const promptsBefore = confirms.length + swals.length
  click(w, '全选全部'); await settle(); click(w, '删除所选'); await settle()
  if (confirms.length + swals.length > promptsBefore) await answerDanger(danger(), true, Boolean(danger().handle))
  console.log('LOCATION_CACHE_QUERY_FAILURE_OBSERVED ' + JSON.stringify({ successfulKeyword: '', failedKeyword: 'SYNTHETIC_FAILED_NEW_FILTER', retainedRows: clone(w.vm.rows), retainedTotal: w.vm.pagination.total, selectedCount: w.vm.selectedCount, nativeRows: w.findAll('.ant-table-tbody .ant-table-row').map(r => r.text()), batchPayloads: calls.deleteLocationCacheBatch.map(c => c.payload) }))
  assert.equal(w.vm.selectedCount, 0, 'FAILED_NEW_QUERY_CANNOT_AUTHORIZE_OLD_ROWS_SELECTION')
  assert.equal(calls.deleteLocationCacheBatch.length, 0, 'FAILED_NEW_QUERY_CANNOT_SEND_BATCH_FOR_OLD_ROWS_AND_COUNT')
})