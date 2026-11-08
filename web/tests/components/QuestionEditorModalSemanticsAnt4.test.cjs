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
function currentDialog() { const form = root.vm.questionDialogForm?.$el; const dialog = form?.closest('[role="dialog"]'); assert.ok(form?.isConnected && dialog && visible(dialog), 'actual owned form within Antd role dialog'); return { form, dialog } }
function pageButton(label) { const button = root.findAll('button').find(b => compact(b.text()) === label); assert.ok(button, 'actual page button ' + label); return button.element }
function action(dialog, label) { const button = [...dialog.querySelectorAll('.ant-modal-footer button')].find(b => [label, label === '取消' ? 'Cancel' : 'OK'].includes(compact(b.textContent))); assert.ok(button, 'actual dialog action ' + label); return button }
async function open() { pageButton('编辑').click(); await settle(); await until(() => Boolean(root.vm.questionDialogOwner?.isCurrent() && root.vm.questionDialogForm?.$el?.isConnected), 'current editor form admitted'); return currentDialog() }
function semantics(dialog, phase) {
  const names = String(dialog.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => document.getElementById(id)).filter(Boolean)
  check(phase + ' actual role ancestor is modal=true', dialog.getAttribute('aria-modal') === 'true')
  check(phase + ' labelledby resolves visible native title', names.some(n => visible(n) && compact(n.textContent) === '详情内容'))
  const dialogs = [...document.querySelectorAll('[role="dialog"]')].filter(visible)
  check(phase + ' exactly one real role, not duplicate surrogate', dialogs.length === 1 && dialogs[0] === dialog)
}
test('QD01 real editor Modal has named modal semantics; invalid type and owned reopen remain unchanged', async () => {
  user.setStoredCurrentUser({ id: 1906201, username: 'SYNTHETIC_EDITOR_ADMIN', role: 'admin', status: 'enabled' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: Page }] })
  history = router.options.history; await router.push('/adapter/component'); await router.isReady()
  root = mount(Page, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false }, config: { errorHandler: error => runtime.push(String(error?.stack || error)) } } })
  await settle(); await until(() => root.findAll('tbody tr[data-row-key]').length === 1, 'actual synthetic row read')
  const first = await open(); semantics(first.dialog, 'initial')
  const input = first.dialog.querySelector('input[role="combobox"]'); assert.ok(input, 'actual type input')
  action(first.dialog, '确定').focus(); action(first.dialog, '确定').click(); await settle()
  await until(() => input.getAttribute('aria-invalid') === 'true', 'existing invalid type state')
  const ids = [...new Set((String(input.getAttribute('aria-describedby') || '') + ' ' + String(input.getAttribute('aria-errormessage') || '')).split(/\s+/).filter(Boolean))]
  check('existing invalid type description belongs to current modal', ids.some(id => { const n = document.getElementById(id); return n && first.dialog.contains(n) && visible(n) && n.textContent.includes('题型必须为32位整数') }))
  await until(() => document.activeElement === input, 'existing invalid submit owned focus')
  check('invalid type remains keyboard reachable without write', !input.disabled && input.tabIndex >= 0 && writes === 0)
  action(first.dialog, '取消').click(); await settle()
  await until(() => !root.vm.visible && !visible(first.dialog), 'original close settles')
  const second = await open(); semantics(second.dialog, 'reopen')
  check('reopen has a fresh owned form', second.form !== first.form && root.vm.questionDialogOwner.isCurrent())
  check('close retires old inline type error', second.dialog.querySelector('input[role="combobox"]').getAttribute('aria-invalid') !== 'true')
  action(second.dialog, '取消').click(); await settle(); await until(() => !root.vm.visible, 'second native close')
  check('all actions preserve zero API mutation', writes === 0)
  completedFlow = true
  assert.equal(checks.filter(x => !x.passed).length, 0, 'all qualified native DOM checks pass')
})
afterEach(async () => {
  if (root?.exists()) root.unmount()
  await settle(); history?.destroy(); user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
  const ledger = { cases: [{ name: 'QD01', completedFlow, checks, passed: completedFlow && checks.every(x => x.passed) }], io: { reads, writes, realIO: io, runtimeCount: runtime.length }, cleanup: { rootsUnmounted: !root?.exists(), bodyEmpty: document.body.children.length === 0 }, sourceStable: sha(PAGE) === sourceSHA, physicalKeyboardOrATClaim: false }
  console.log('EDITOR_MODAL_NATIVE ' + JSON.stringify(ledger))
  if (process.env.EDITOR_MODAL_LEDGER) fs.writeFileSync(process.env.EDITOR_MODAL_LEDGER, JSON.stringify(ledger) + '\n')
  assert.equal(sha(PAGE), sourceSHA); assert.equal(writes, 0); assert.deepEqual(io, []); assert.deepEqual(runtime, [])
})
