/* Actual Vue/Antd/Tiku parent; only API outcomes are synthetic. Not physical
 * Tab/AT, authentication or server-write qualification. Portable B/M input. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue'), { createRouter, createMemoryHistory } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const PAGE = path.join(web, 'src/pages/TikuComponent/index.vue')
const sha = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'), sourceSHA = sha(PAGE)
const row = { id: 9001, type: 2147483648, question: 'SYNTHETIC invalid legacy type', options: '["甲","乙"]', answer: '["甲"]', course_name: 'SYNTHETIC course', source: 7, source_name: 'SYNTHETIC source', extra: 'SYNTHETIC preserved field' }
let root, history, writes = 0, reads = 0
const io = [], runtime = [], checks = []
let completedFlow = false
function deny(kind) { io.push(kind); throw new Error('FORBIDDEN_EDITOR_DIALOG_IO_' + kind) }
window.XMLHttpRequest.prototype.open = () => deny('XHR')
globalThis.fetch = window.fetch = () => deny('FETCH')
window.open = () => deny('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', event => runtime.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtime.push(String(event.reason?.stack || event.reason)))
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = Object.fromEntries(['getCourses', 'getQuestions', 'createQuestions', 'updateQuestions', 'delQuestions', 'importQuestions'].map(name => [name, () => {
  if (name === 'getCourses') { reads++; return Promise.resolve({ status: 200, data: ['SYNTHETIC course'] }) }
  if (name === 'getQuestions') { reads++; return Promise.resolve({ status: 200, data: { items: [{ ...row }], total: 1 } }) }
  writes++; return deny('MUTATION_' + name)
}]))
require.cache[apiPath] = api
const swalPath = require.resolve('sweetalert2'), swal = new Module(swalPath); swal.loaded = true
swal.exports = { fire: () => deny('SWEETALERT'), getPopup: () => null, close: () => {} }; require.cache[swalPath] = swal
const Page = require(PAGE)
const compact = value => String(value || '').replace(/\s/g, '')
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 4)); await nextTick(); await flushPromises() }
async function until(predicate, label) { const start = performance.now(); while (!predicate() && performance.now() - start < 1200) { await new Promise(resolve => requestAnimationFrame(resolve)); await settle() } assert.ok(predicate(), label) }
function visible(node) { if (!node?.isConnected) return false; for (let p = node; p; p = p.parentElement) { const s = getComputedStyle(p); if (p.hidden || p.inert || p.getAttribute('aria-hidden') === 'true' || s.display === 'none' || s.visibility === 'hidden') return false } return true }
function check(label, passed) { checks.push({ label, qualified: true, passed: Boolean(passed) }) }
function pageButton(label) { const button = root.findAll('button').find(b => compact(b.text()) === label); assert.ok(button, 'actual page button ' + label); return button.element }
function modalAction(dialog, label) { const button = [...dialog.querySelectorAll('.ant-modal-footer button')].find(b => [label, label === '取消' ? 'Cancel' : 'OK'].includes(compact(b.textContent))); assert.ok(button, 'actual Modal action ' + label); return button }
function bulkPortal() { return [...document.querySelectorAll('.ant-modal')].find(n => visible(n) && compact(n.querySelector('.ant-modal-title')?.textContent) === '批量编辑课程名称') }
const callbackObservations = []
let capturedAfterClose
async function observe(predicate) { const start = performance.now(); while (!predicate() && performance.now() - start < 500) { await settle() } return Boolean(predicate()) }
async function openBulk(phase) {
  const opener = pageButton('批量编辑课程名'); assert.ok(!opener.disabled); opener.focus(); opener.click()
  // Installed synchronously before Vue's scheduled first render. Antd receives
  // this pass-through as its real afterClose prop; no lifecycle is invoked here.
  const owner = root.vm.bulkCourseOwner, original = owner?.afterClose; assert.equal(typeof original, 'function')
  let portal
  owner.afterClose = function (...args) {
    const active = document.activeElement
    callbackObservations.push({ phase, activeTag: active?.tagName || null, activeBody: active === document.body, activeOpener: active === opener, activeInOwnPortal: Boolean(portal?.contains(active)), portalConnected: Boolean(portal?.isConnected) })
    return Reflect.apply(original, this, args)
  }
  capturedAfterClose = original
  await settle(); await until(() => root.vm.bulkCourseVisible && bulkPortal(), 'actual bulk portal opens')
  portal = bulkPortal()
  return { owner, opener, portal, original }
}
function semantics(portal, phase) {
  const dialog = portal.closest('[role="dialog"]'); assert.ok(dialog, 'actual Antd role ancestor')
  check(phase + ' role dialog is modal=true', dialog.getAttribute('aria-modal') === 'true')
  const ids = String(dialog.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean)
  check(phase + ' visible native title is resolved', ids.some(id => { const n = document.getElementById(id); return n && visible(n) && compact(n.textContent) === '批量编辑课程名称' }))
  check(phase + ' only one real visible dialog', [...document.querySelectorAll('[role="dialog"]')].filter(visible).length === 1)
}
test('QO01 bulk native semantics and owned close focus; foreground guard and readonly detail remain intact', async () => {
  user.setStoredCurrentUser({ id: 1906201, username: 'SYNTHETIC_OVERLAY_ADMIN', role: 'admin', status: 'enabled' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: Page }] })
  history = router.options.history; await router.push('/adapter/component'); await router.isReady()
  root = mount(Page, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false }, config: { errorHandler: error => runtime.push(String(error?.stack || error)) } } })
  await settle(); await until(() => root.find('tr[data-row-key="9001"]').exists(), 'actual synthetic row read')
  check('zero selection disables bulk action', pageButton('批量编辑课程名').disabled)
  const checkbox = root.find('tr[data-row-key="9001"] input[type="checkbox"]').element; assert.ok(checkbox && !checkbox.disabled); checkbox.click(); await settle()
  check('actual row checkbox selects only9001 and enables bulk', root.vm.selectedRowKeys.map(String).join(',') === '9001' && !pageButton('批量编辑课程名').disabled)
  const readCount = reads, first = await openBulk('cancel')
  semantics(first.portal, 'initial')
  const label = [...first.portal.querySelectorAll('label')].find(n => compact(n.textContent) === '课程名称'), input = label?.control
  check('course field has actual native label control', Boolean(input && first.portal.contains(input) && !input.disabled))
  const described = String(input?.getAttribute('aria-describedby') || '').split(/\s+/).filter(Boolean)
  check('course hint belongs to visible current field', described.some(id => { const n = document.getElementById(id); return n && first.portal.contains(n) && visible(n) && n.textContent.includes('支持自由输入') }))
  const beforeCancel = callbackObservations.length, cancel = modalAction(first.portal, '取消'); cancel.focus(); assert.strictEqual(document.activeElement, cancel); cancel.click(); await settle()
  await until(() => callbackObservations.length > beforeCancel && !root.vm.bulkCourseVisible && !visible(first.portal), 'real Antd afterClose and native cancel finish')
  check('native Cancel returns to exact owned toolbar opener', await observe(() => document.activeElement === first.opener))
  check('Cancel preserves selection and zero API mutation', root.vm.selectedRowKeys.map(String).join(',') === '9001' && writes === 0 && reads === readCount)
  const foreground = root.find('input[placeholder="搜索问题"]').element; foreground.focus(); assert.strictEqual(document.activeElement, foreground)
  // Explicit stale-callback robustness, separate from the real native callback.
  capturedAfterClose(); await settle()
  check('old callback never steals legitimate foreground input focus', document.activeElement === foreground)
  const second = await openBulk('escape'); semantics(second.portal, 'reopen')
  const beforeEscape = callbackObservations.length, closeControl = modalAction(second.portal, '取消'); closeControl.focus()
  second.portal.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', keyCode: 27, which: 27, bubbles: true })); await settle()
  await until(() => callbackObservations.length > beforeEscape && !root.vm.bulkCourseVisible && !visible(second.portal), 'real Escape afterClose finishes')
  check('native Escape returns exact owned toolbar opener', await observe(() => document.activeElement === second.opener))
  check('bulk lifecycle adds no reads/writes or selection reset', reads === readCount && writes === 0 && root.vm.selectedRowKeys.map(String).join(',') === '9001')
  const detail = root.findAll('tr[data-row-key="9001"] button').find(b => compact(b.text()) === '详情').element; assert.ok(detail && !detail.disabled); detail.focus(); detail.click(); await settle()
  await until(() => root.vm.questionDetailBody?.isConnected, 'actual readonly Drawer body opens')
  const detailPortal = root.vm.questionDetailBody.closest('[role="dialog"]'); assert.ok(detailPortal && visible(detailPortal))
  check('existing detail Drawer stays modal=true and named', detailPortal.getAttribute('aria-modal') === 'true' && String(detailPortal.getAttribute('aria-labelledby') || '').split(/\s+/).some(id => compact(document.getElementById(id)?.textContent) === '题目详情'))
  check('detail remains readonly full record without extra API', detailPortal.textContent.includes('仅查看，不修改题目') && detailPortal.textContent.includes('2147483648') && reads === readCount && writes === 0)
  const detailClose = detailPortal.querySelector('button[data-question-detail-close]'); assert.ok(detailClose); detailClose.focus(); detailClose.click(); await settle()
  await until(() => !root.vm.questionDetailOpen && !visible(detailPortal), 'readonly detail native close finishes')
  check('existing detail returns exact row trigger focus', await observe(() => document.activeElement === detail))
  completedFlow = true
  assert.equal(checks.filter(x => !x.passed).length, 0, 'all qualified native observations pass')
})
afterEach(async () => {
  if (root?.exists()) root.unmount()
  await settle(); history?.destroy(); user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
  const ledger = { cases: [{ name: 'QO01', completedFlow, checks, observations: callbackObservations, passed: completedFlow && checks.every(x => x.passed) }], io: { reads, writes, realIO: io, runtimeCount: runtime.length }, cleanup: { rootsUnmounted: !root?.exists(), bodyEmpty: document.body.children.length === 0 }, sourceStable: sha(PAGE) === sourceSHA, physicalKeyboardOrATClaim: false }
  console.log('QUESTION_OVERLAY_NATIVE ' + JSON.stringify(ledger))
  if (process.env.QUESTION_OVERLAY_LEDGER) fs.writeFileSync(process.env.QUESTION_OVERLAY_LEDGER, JSON.stringify(ledger) + '\n')
  assert.equal(sha(PAGE), sourceSHA); assert.equal(writes, 0); assert.deepEqual(io, []); assert.deepEqual(runtime, [])
})
