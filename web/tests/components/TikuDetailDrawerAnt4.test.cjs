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
function modal() { const d = [...document.querySelectorAll('.ant-modal')].find(node => visible(node) && node.querySelector('.ant-modal-title')?.textContent.includes('详情内容')); assert.ok(d, 'original editable Antd Modal'); return d }
function nativeText(element, value) { element.value = value; element.dispatchEvent(new Event('input', { bubbles: true })) }
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

test('TD00 original overflow expansion preserves full escaped question without becoming a Drawer or issuing another read', async () => {
  const [w] = await page(), before = readCount(), toggle = w.find('.overflow-table-text__toggle'); assert.ok(toggle.exists(), 'actual overflow action activated by declared synthetic geometry'); await click(toggle.element)
  assert.equal(toggle.attributes('aria-expanded'), 'true'); assert.ok(w.find('.overflow-table-text__content--expanded').text().includes(long)); assert.equal(drawers().length, 0); assert.equal(readCount(), before)
  assert.equal(w.findAll('.overflow-table-text__content img, .overflow-table-text__content script').length, 0); witness('original-expanded', null)
})
test('TD01 original editor remains an independent editable Modal and Cancel keeps row/read/CRUD unchanged', async () => {
  const [w] = await page(), before = readCount(), edit = button(w, '编辑', true); buttonHandler(w, edit); await click(edit); const d = modal(), input = d.querySelector('textarea'); assert.ok(input); assert.equal(input.value, long); assert.equal(drawers().length, 0)
  nativeText(input, 'SYNTHETIC未保存编辑'); await settle(); assert.equal(w.vm.data[0].question, long)
  const cancel = [...d.querySelectorAll('.ant-modal-footer button')].find(b => ['取消', 'Cancel'].includes(compact(b.textContent))); assert.ok(cancel); await click(cancel); assert.equal(readCount(), before); assert.deepEqual(writes(), []); witness('original-edit-cancel', null)
})
test('TD02 row detail click opens read-only full fields, literal safe long content and no additional read or CRUD', async () => {
  const [w] = await page(), before = readCount(), { dom: d } = await open(w), text = d.textContent
  for (const value of ['101', '单选题', long, 'SYNTHETIC选项甲', 'SYNTHETIC选项乙', 'SYNTHETIC课程甲', '731', 'SYNTHETIC来源甲', 'SYNTHETIC_HASH_甲', 'SYNTHETIC额外原字段', 'SYNTHETIC拓展甲', '第二行']) assert.ok(text.includes(value), 'read-only complete visible field: ' + value.slice(0, 50))
  for (const label of ['问题', '选项', '答案', '课程', '来源', '拓展']) assert.ok(text.includes(label), 'visible field purpose ' + label)
  assert.equal(d.querySelectorAll('input,textarea,select,[contenteditable="true"]').length, 0); assert.equal(d.querySelectorAll('img,script,iframe').length, 0); assert.equal(document.querySelectorAll('.ant-modal textarea').length, 0); assert.equal(readCount(), before); witness('readonly-all-fields', d)
})
test('TD03 real close returns focus to this detail trigger and subsequent open remains usable without reads', async () => {
  const [w] = await page(), before = readCount(), { dom, trigger } = await open(w), close = closeButton(dom); assert.ok(close); await click(close)
  await until(() => drawers().length === 0, 'actual Drawer closes'); await until(() => document.activeElement === trigger, 'native close returns exact opener focus'); await open(w); assert.equal(readCount(), before); witness('close-reopen', drawer())
})
test('TD04 actual Escape key closes the Drawer and restores its native opener focus', async () => {
  const [w] = await page(), { dom, trigger } = await open(w), target = closeButton(dom); target.focus(); target.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', keyCode: 27, which: 27, bubbles: true })); await settle()
  await until(() => drawers().length === 0, 'Antd keyboard Escape closes'); await until(() => document.activeElement === trigger, 'Escape returns exact opener focus'); witness('escape-close', null)
})
test('TD05 same Vue app page instances own separate Drawers; closing one cannot close or steal the other focus', async () => {
  const [first, second] = await page(true), firstOpen = await open(first), before = readCount(); await click(button(second, '详情', true)); assert.equal(drawers().length, 2); const other = drawers().find(d => d !== firstOpen.dom); assert.ok(other)
  const otherClose = closeButton(other); otherClose.focus(); await click(closeButton(firstOpen.dom)); await until(() => drawers().length === 1, 'only first Drawer closes'); assert.strictEqual(drawers()[0], other); assert.strictEqual(document.activeElement, otherClose); assert.equal(readCount(), before); witness('same-app-twins', other)
})
for (const transition of ['admin-B', 'logout', 'role', 'status', 'ABA']) test('TD06 retired detail owner hides synchronously and stale DOM/close/after callbacks cannot affect a subsequent owner: ' + transition, async () => {
  const [w] = await page(), old = await open(w), stale = callbacks(old.component)
  if (transition === 'logout') user.clearStoredCurrentUser()
  else if (transition === 'role') user.setStoredCurrentUser({ ...A, role: 'user' })
  else if (transition === 'status') user.setStoredCurrentUser({ ...A, status: 0 })
  else { user.setStoredCurrentUser(B); if (transition === 'ABA') user.setStoredCurrentUser(A) }
  assert.equal(visible(old.dom), false, 'retired owner content is not visible before Vue patches')
  old.trigger.click(); stale.close(new Event('click')); stale.after(false); await settle(); assert.equal(drawers().length, 0, 'old native/VNode callbacks cannot reopen retired snapshot')
  if (['logout', 'role', 'status'].includes(transition)) { user.setStoredCurrentUser(B); await settle() }
  const current = await open(w), before = readCount(), outside = document.createElement('button'); outside.textContent = 'SYNTHETIC外部焦点'; document.body.appendChild(outside); outside.focus()
  stale.close(new Event('click')); stale.after(false); old.triggerHandler(new Event('click')); await settle(); assert.equal(drawers().length, 1); assert.ok(visible(current.dom)); assert.strictEqual(document.activeElement, outside); assert.equal(readCount(), before); witness('retired-' + transition, current.dom)
})
test('TD07 a prior same-owner close callback cannot close a later open or steal focus outside its retired Drawer', async () => {
  const [w] = await page(), old = await open(w), stale = callbacks(old.component); await click(closeButton(old.dom)); await until(() => drawers().length === 0, 'first close completes'); const current = await open(w), before = readCount(), outside = document.createElement('button'); document.body.appendChild(outside); outside.focus()
  stale.close(new Event('click')); stale.after(false); await settle(); assert.equal(drawers().length, 1); assert.ok(visible(current.dom)); assert.strictEqual(document.activeElement, outside); assert.equal(readCount(), before); witness('same-owner-stale-close', current.dom)
})
test('TD08 old unmounted VNodes and Drawer callbacks cannot affect same-module re-entry', async () => {
  const [oldPage] = await page(), old = await open(oldPage), stale = callbacks(old.component); oldPage.unmount(); await settle(); const [currentPage] = await page(false, true), current = await open(currentPage), before = readCount(), outside = document.createElement('button'); document.body.appendChild(outside); outside.focus()
  old.trigger.click(); old.triggerHandler(new Event('click')); stale.close(new Event('click')); stale.after(false); await settle(); assert.equal(drawers().length, 1); assert.ok(visible(current.dom)); assert.strictEqual(document.activeElement, outside); assert.equal(readCount(), before); witness('unmounted-reentry', current.dom)
})

test('TD09 raw values retain source2 answer content, zero false null object repetitions and malformed text without HTML execution', async () => {
  rowOverrides = { source: 2, options: '[0,false,null,{"SYNTHETIC键":"SYNTHETIC值"},"重复","重复"]', answer: '["SYNTHETIC源2答案","SYNTHETIC源2答案",0,false,null,{"SYNTHETIC答案键":"SYNTHETIC答案值"}]', extra: '<svg onload="globalThis.__DETAIL_EXECUTED__=true">SYNTHETIC_SAFE_SVG</svg>', custom_fixture_field: 'SYNTHETIC_RAW_EXTRA' }
  const [w] = await page(), before = readCount(), { dom: d } = await open(w), text = d.textContent
  for (const value of ['SYNTHETIC源2答案', 'false', 'null', 'SYNTHETIC键', 'SYNTHETIC值', 'SYNTHETIC答案键', 'SYNTHETIC答案值', rowOverrides.options, rowOverrides.answer, rowOverrides.extra, 'SYNTHETIC_RAW_EXTRA']) assert.ok(text.includes(value), 'raw and parsed visible value retained: ' + value)
  assert.ok(text.split('SYNTHETIC源2答案').length - 1 >= 2, 'repeated raw answers are not deduplicated or converted to source2 indexes'); assert.equal(d.querySelectorAll('svg,script,img,iframe').length, 0); assert.equal(readCount(), before); witness('heterogeneous-raw-values', d)
})
test('TD10 actual Drawer mask closes without reads or CRUD and restores the active detail opener', async () => {
  const [w] = await page(), before = readCount(), { dom, trigger } = await open(w), mask = dom.closest('.ant-drawer')?.querySelector('.ant-drawer-mask'); assert.ok(mask, 'actual Antd mask'); await click(mask)
  await until(() => drawers().length === 0, 'actual mask closes the Drawer'); await until(() => document.activeElement === trigger, 'mask close returns exact opener focus'); assert.equal(readCount(), before); witness('mask-close', null)
})
