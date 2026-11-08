/* Actual Tiku Vue/Antd4 page. Synthetic reads only, real IO and CRUD trapped.
 * TIKU_WEB_SOURCE/UI_TEST_HELPER permit the same frozen fixture on B/M/R copies.
 * Observes real DOM/Antd public events, not private detail-owner field names.
 * jsdom long-text/style/focus observations are not device/reader certification. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
require.extensions['.css'] = module => { module.exports = {} }
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, defineComponent, h } = require('vue'), { createRouter, createMemoryHistory } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const PAGE = path.join(web, 'src/pages/TikuComponent/index.vue'), sha = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex'), initialSHA = sha(PAGE)
const A = { id: 9511, username: 'synthetic-detail-admin-a', role: 'admin', status: 1 }, B = { id: 9512, username: 'synthetic-detail-admin-b', role: 'admin', status: 1 }
const long = ('SYNTHETIC长题面甲乙\n').repeat(180) + '<img src=x onerror="globalThis.__DETAIL_EXECUTED__=true"><script>globalThis.__DETAIL_EXECUTED__=true</script>'
const question = (actor = 'A') => actor === 'B' ? 'SYNTHETIC_B_CURRENT_QUESTION' : long
const row = (actor = 'A', id = 101) => ({ id, type: 0, question: question(actor), options: '["SYNTHETIC选项甲","SYNTHETIC选项乙"]', answer: '["SYNTHETIC选项乙"]', course_name: 'SYNTHETIC课程甲', source: 731, source_name: 'SYNTHETIC来源甲', hash: 'SYNTHETIC_HASH_甲', extra: 'SYNTHETIC拓展甲\n第二行', custom_fixture_field: 'SYNTHETIC额外原字段' })
const copy = value => JSON.parse(JSON.stringify(value)), compact = value => String(value || '').replace(/\s/g, '')
let roots = [], histories = [], calls = [], realIO = [], errors = [], rowOverrides = null
function trap(kind) { realIO.push(kind); throw new Error('FORBIDDEN_DETAIL_DRAWER_IO_' + kind) }
window.XMLHttpRequest.prototype.open = function () { return trap('XHR') }; globalThis.fetch = window.fetch = () => trap('FETCH'); window.open = () => trap('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { trap('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => trap(name); transport.get = () => trap(name) }
window.URL.createObjectURL = () => trap('BLOB_EXPORT'); window.HTMLAnchorElement.prototype.click = function () { trap('ANCHOR_DOWNLOAD') }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = Object.fromEntries(['getQuestions', 'getCourses', 'updateQuestions', 'createQuestions', 'delQuestions', 'importQuestions'].map(kind => [kind, payload => {
  const actor = user.getStoredCurrentUser()?.id === B.id ? 'B' : 'A'; calls.push({ kind, payload: copy(payload || {}), actor })
  if (kind !== 'getQuestions' && kind !== 'getCourses') return Promise.reject(new Error('FORBIDDEN_DETAIL_CRUD_' + kind))
  return Promise.resolve({ status: 200, data: kind === 'getCourses' ? ['SYNTHETIC课程甲'] : { items: [{ ...row(actor), ...(rowOverrides || {}) }], total: 1 } })
}]))
require.cache[apiPath] = api
const swalPath = require.resolve('sweetalert2'), swal = new Module(swalPath); swal.loaded = true; swal.exports = { fire: () => trap('SWEETALERT'), getPopup: () => null, close: () => {} }; require.cache[swalPath] = swal
// Bounded geometry fixture activates the EXISTING overflow toggle. This is not
// measured viewport/layout evidence; all clicks still use actual native nodes.
for (const [property, value] of [['scrollHeight', 800], ['clientHeight', 40]]) {
  const original = Object.getOwnPropertyDescriptor(window.HTMLElement.prototype, property)
  Object.defineProperty(window.HTMLElement.prototype, property, { configurable: true, get() { return this.classList?.contains('overflow-table-text__content') ? value : original?.get?.call(this) || 0 } })
}
const Tiku = require(PAGE)
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 8)); await nextTick(); await flushPromises() }
async function until(check, message) { const limit = Date.now() + 900; while (!check() && Date.now() < limit) await settle(); assert.ok(check(), message) }
const readCount = () => calls.filter(call => ['getQuestions', 'getCourses'].includes(call.kind)).length
const writes = () => calls.filter(call => !['getQuestions', 'getCourses'].includes(call.kind))
function visible(node) { if (!node?.isConnected) return false; for (let p = node; p; p = p.parentElement) { const style = getComputedStyle(p); if (p.hidden || p.getAttribute('aria-hidden') === 'true' || style.display === 'none' || style.visibility === 'hidden') return false } return true }
function drawers() { return [...document.querySelectorAll('.ant-drawer-open .ant-drawer-content')].filter(visible) }
function closeButton(d) { const button = [...d.querySelectorAll('button')].find(button => compact(button.getAttribute('aria-label')) === '关闭详情' || compact(button.textContent) === '关闭详情'); assert.ok(button, 'actual Chinese close-detail action'); return button }
function drawer() { const ds = drawers(); assert.equal(ds.length, 1, 'one actual visible read-only Antd Drawer'); return ds[0] }
function button(w, text, rowOnly = false) { const found = w.findAll(rowOnly ? '.row-actions button' : 'button').find(b => compact(b.text()) === compact(text)); assert.ok(found, 'actual native ' + (rowOnly ? 'row ' : '') + text + ' action exists; old table expansion/editor is not a detail Drawer'); return found.element }
async function click(element) { element.click(); await settle() }
function buttonHandler(w, element) { const component = w.findAllComponents(Antd.Button).find(button => button.element === element); assert.ok(component, 'actual Antd Button component for native node'); const handler = component.props('onClick'); assert.equal(typeof handler, 'function', 'actual captured render-bound Button onClick'); return handler }
async function open(w) { const trigger = button(w, '详情', true), triggerHandler = buttonHandler(w, trigger); trigger.focus(); await click(trigger); const d = drawer(); assert.ok(d.querySelector('.ant-drawer-title')?.textContent.includes('题目详情')); const dialog = d.closest('[role="dialog"]') || d.querySelector('[role="dialog"]'); assert.ok(dialog, 'actual Drawer dialog semantics'); assert.equal(dialog.getAttribute('aria-modal'), 'true'); const titleId = dialog.getAttribute('aria-labelledby'); assert.ok(titleId && document.getElementById(titleId)?.textContent.includes('题目详情'), 'actual independent title association'); return { trigger, triggerHandler, dom: d, component: w.findComponent(Antd.Drawer) } }
function callbacks(component) { assert.ok(component.exists(), 'actual Antd Drawer component'); const close = component.props('onClose'), after = component.props('onAfterOpenChange'); assert.equal(typeof close, 'function', 'actual Drawer close listener'); assert.equal(typeof after, 'function', 'actual Drawer transition/focus listener'); return { close, after } }
async function page(twins = false, reusePrincipal = false) {
  if (!reusePrincipal) user.setStoredCurrentUser(A)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: Tiku }] }); await router.push('/adapter/component'); await router.isReady(); histories.push(router.options.history)
  const Root = twins ? defineComponent({ render: () => h('section', [h(Tiku, { key: 'first' }), h(Tiku, { key: 'second' })]) }) : Tiku
  const root = mount(Root, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false }, config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); roots.push(root); await settle()
  const pages = twins ? root.findAllComponents(Tiku) : [root]; assert.equal(pages.length, twins ? 2 : 1); return pages
}
function witness(stage, d) { console.log('TIKU_DETAIL_DOM ' + JSON.stringify({ stage, visibleDrawers: drawers().length, textLength: d?.textContent.length || 0, fullQuestionPresent: Boolean(d?.textContent.includes(long)), readonlyNativeControls: d ? d.querySelectorAll('input,textarea,select,[contenteditable="true"]').length : 0, bodyOverflowY: d ? getComputedStyle(d.querySelector('.ant-drawer-body') || d).overflowY : '', reads: readCount(), writes: writes().length, realIO, nativeDeviceClaim: false })) }
afterEach(async () => {
  for (const root of roots.splice(0)) if (root.exists()) root.unmount()
  await settle(); for (const history of histories.splice(0)) history.destroy()
  user.clearStoredCurrentUser(); window.localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
  console.log('TIKU_DETAIL_SAFETY ' + JSON.stringify({ sourceSHA256: sha(PAGE), calls, realIO, runtimeErrors: errors, dangerousHTMLExecuted: globalThis.__DETAIL_EXECUTED__ === true }))
  assert.equal(sha(PAGE), initialSHA); assert.deepEqual(writes(), [], 'detail/editor cancel sends no CRUD'); assert.deepEqual(realIO, []); assert.deepEqual(errors, []); assert.notEqual(globalThis.__DETAIL_EXECUTED__, true)
  calls = []; realIO = []; errors = []; rowOverrides = null; delete globalThis.__DETAIL_EXECUTED__
})


// Additional focused public-DOM probes; original e8e5 frozen B/M/R remains unchanged.
test('RS01 same-actor status1-to0-to1 ABA retires prior row, portal and callbacks without additional reads', async () => {
  const [w] = await page(), old = await open(w), stale = callbacks(old.component), before = readCount()
  user.setStoredCurrentUser({ ...A, status: 0 }); user.setStoredCurrentUser({ ...A, status: 1 })
  assert.equal(visible(old.dom), false, 'old private detail hides synchronously during status ABA')
  old.trigger.click(); old.triggerHandler(new Event('click')); stale.close(new Event('click')); stale.after(true); stale.after(false)
  await settle(); assert.equal(drawers().length, 0); assert.equal(readCount(), before)
  const current = await open(w), outside = document.createElement('button'); document.body.appendChild(outside); outside.focus()
  old.triggerHandler(new Event('click')); stale.after(true); stale.after(false); stale.close(new Event('click')); await settle()
  assert.equal(drawers().length, 1); assert.ok(visible(current.dom)); assert.strictEqual(document.activeElement, outside); assert.equal(readCount(), before)
  witness('review-status-ABA', current.dom)
})
test('RS02 explicit same-id logout with denied removeItem hides and cannot reopen old cached detail on that mount', async () => {
  const [w] = await page(), old = await open(w), stale = callbacks(old.component), before = readCount()
  const original = window.Storage.prototype.removeItem
  window.Storage.prototype.removeItem = function (key) { if (key === 'adapter_current_user') throw new Error('SYNTHETIC_STORAGE_REMOVE_DENIED'); return original.call(this, key) }
  try {
    user.clearStoredCurrentUser(); assert.equal(user.getStoredCurrentUser()?.id, A.id, 'synthetic cache remains A because removeItem was denied')
    assert.equal(visible(old.dom), false, 'explicit logout hides private content before Vue patches')
    old.trigger.click(); old.triggerHandler(new Event('click')); stale.after(true); stale.after(false); stale.close(new Event('click')); await settle()
    assert.equal(drawers().length, 0, 'old producer cannot reopen')
    await click(button(w, '详情', true))
    assert.equal(drawers().length, 0, 'fresh row on old logged-out mount must not expose retained cached record')
    assert.equal(readCount(), before); witness('review-blocked-remove-logout', null)
  } finally { window.Storage.prototype.removeItem = original }
})
test('RS03 balance-only display update preserves current Drawer and normal close focus without reads', async () => {
  const [w] = await page(), current = await open(w), before = readCount(), text = current.dom.textContent
  user.setStoredCurrentUser({ ...A, balance: '25.000001' }); await settle()
  assert.ok(visible(current.dom)); assert.strictEqual(drawers()[0], current.dom); assert.equal(current.dom.textContent, text); assert.equal(readCount(), before)
  await click(closeButton(current.dom)); await until(() => drawers().length === 0, 'balance-updated detail closes'); await until(() => document.activeElement === current.trigger, 'same-scope ordinary close restores opener'); assert.equal(readCount(), before)
  witness('review-balance-preserved', null)
})
test('RS04 old and current late afterOpenChange true cannot steal subsequent outside focus or replace newer Drawer', async () => {
  const [w] = await page(), old = await open(w), stale = callbacks(old.component)
  await click(closeButton(old.dom)); await until(() => drawers().length === 0, 'old ordinary close')
  const current = await open(w), currentCallbacks = callbacks(current.component), before = readCount(), outside = document.createElement('button'); document.body.appendChild(outside); outside.focus()
  stale.after(true); stale.after(false); stale.close(new Event('click')); currentCallbacks.after(true); await settle()
  assert.equal(drawers().length, 1); assert.strictEqual(drawers()[0], current.dom); assert.strictEqual(document.activeElement, outside); assert.equal(readCount(), before)
  witness('review-late-open-outside-focus', current.dom)
})
test('RS05 own prototype-like additional keys and zero false values show literal labels without prototype mutation', async () => {
  rowOverrides = JSON.parse('{"__proto__":{"SYNTHETIC_PROTO_PAYLOAD":"safe"},"constructor":"SYNTHETIC_CONSTRUCTOR","toString":"SYNTHETIC_TOSTRING","hasOwnProperty":false,"zeroValue":0,"falseValue":false}')
  assert.equal(Object.prototype.SYNTHETIC_PROTO_PAYLOAD, undefined)
  const [w] = await page(), before = readCount(), { dom } = await open(w)
  const field = key => [...dom.querySelectorAll('.question-detail-field')].find(node => node.querySelector('h3')?.textContent === '其他字段（' + key + '）')
  for (const key of ['__proto__', 'constructor', 'toString', 'hasOwnProperty', 'zeroValue', 'falseValue']) assert.ok(field(key), 'literal own-key field label ' + key)
  assert.ok(field('__proto__').textContent.includes('SYNTHETIC_PROTO_PAYLOAD')); assert.ok(field('constructor').textContent.includes('SYNTHETIC_CONSTRUCTOR')); assert.ok(field('toString').textContent.includes('SYNTHETIC_TOSTRING'))
  assert.equal(field('zeroValue').querySelector('pre').textContent, '0'); assert.equal(field('falseValue').querySelector('pre').textContent, 'false'); assert.equal(field('hasOwnProperty').querySelector('pre').textContent, 'false')
  assert.equal(Object.prototype.SYNTHETIC_PROTO_PAYLOAD, undefined); assert.equal(readCount(), before); witness('review-prototype-like-own-keys', dom)
})
test('RS06 malformed raw options and answers plus unknown integer type are preserved as escaped readonly text', async () => {
  rowOverrides = { type: 999, options: 'SYNTHETIC_BAD_OPTIONS{<svg onload="globalThis.__DETAIL_EXECUTED__=true">', answer: 'SYNTHETIC_BAD_ANSWER[<img src=x>', course_name: '' }
  const [w] = await page(), before = readCount(), { dom } = await open(w), text = dom.textContent
  assert.ok(text.includes('999')); assert.ok(text.includes(rowOverrides.options)); assert.ok(text.includes(rowOverrides.answer)); assert.ok(text.includes('（空）'))
  const values = [...dom.querySelectorAll('.question-detail-values li')].map(node => node.textContent)
  assert.ok(values.includes(rowOverrides.options)); assert.ok(values.includes(rowOverrides.answer)); assert.equal(dom.querySelectorAll('svg,img,script,iframe,input,textarea,select').length, 0); assert.equal(readCount(), before)
  witness('review-malformed-and-unknown-type', dom)
})
test('RS07 initial actual open takes owned Chinese close focus without extra reads and callbacks retain dialog title relationship', async () => {
  const [w] = await page(), before = readCount(), current = await open(w), close = closeButton(current.dom)
  await until(() => document.activeElement === close, 'initial open focuses owned Chinese close action')
  const portal = current.dom.closest('[role="dialog"]'); assert.ok(portal); assert.equal(portal.getAttribute('aria-modal'), 'true'); const titleId = portal.getAttribute('aria-labelledby'); assert.ok(document.getElementById(titleId)?.textContent.includes('题目详情'))
  assert.equal(readCount(), before); witness('review-initial-owned-focus', current.dom)
})
