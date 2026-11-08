/* Real Vue/Antd parent, native scheduler/Transition. API is inert.
 * Prepaint semantic handoff, NOT painted leak, physical Tab or full AT proof. */
const { test } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const PAGE = path.join(web, 'src/pages/TikuComponent/index.vue')
const sha = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'), sourceSHA = sha(PAGE)
const A = { id: 1906201, username: 'SYNTHETIC_OPENING_A', role: 'admin', status: 'enabled' }, B = { ...A, id: 1906202, username: 'SYNTHETIC_OPENING_B' }
const row = { id: 9001, type: 2147483648, question: 'SYNTHETIC row', options: '["甲","乙"]', answer: '["甲"]', course_name: 'SYNTHETIC course', source: 7, source_name: 'SYNTHETIC source', extra: 'SYNTHETIC low frequency' }
const cases = []
let cur, root, history, other
function deny(kind) { cur.io.realIO.push(kind); throw new Error('FORBIDDEN_OPENING_IO_' + kind) }
window.XMLHttpRequest.prototype.open = () => deny('XHR'); globalThis.fetch = window.fetch = () => deny('FETCH')
window.open = () => deny('WINDOW_OPEN'); globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', () => { if (cur) cur.io.runtimeCount++ })
window.addEventListener('unhandledrejection', () => { if (cur) cur.io.runtimeCount++ })
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = Object.fromEntries(['getCourses', 'getQuestions', 'createQuestions', 'updateQuestions', 'delQuestions', 'importQuestions'].map(name => [name, () => {
  if (name === 'getCourses') { cur.io.reads++; return Promise.resolve({ status: 200, data: ['SYNTHETIC course'] }) }
  if (name === 'getQuestions') { cur.io.reads++; return Promise.resolve({ status: 200, data: { items: [{ ...row }], total: 1 } }) }
  cur.io.writes++; return deny('MUTATION_' + name)
}]))
require.cache[apiPath] = api
const swalPath = require.resolve('sweetalert2'), swal = new Module(swalPath); swal.loaded = true
swal.exports = { fire: () => deny('SWEETALERT'), getPopup: () => null, close: () => {} }; require.cache[swalPath] = swal
const Page = require(PAGE), originalOpen = Page.methods.openBulkCourseModal
const compact = x => String(x || '').replace(/\s/g, '')
function visible(node) { if (!node?.isConnected) return false; for (let p = node; p; p = p.parentElement) { const s = getComputedStyle(p); if (p.hidden || p.inert || p.getAttribute('aria-hidden') === 'true' || s.display === 'none' || s.visibility === 'hidden') return false } return true }
function role(vm) { return vm.$refs.bulkCourseDialogForm?.$el?.closest('[role="dialog"]') }
function state(vm, owner) {
  const dialog = role(vm), panel = dialog?.querySelector('.ant-modal'), active = document.activeElement
  return { ownerCurrent: Boolean(owner?.isCurrent()), connected: Boolean(dialog?.isConnected), visible: visible(dialog), tabIndexAttribute: dialog?.getAttribute('tabindex') ?? null, classes: String(panel?.className || ''), entering: /-(?:enter|appear)-(?:active|from|prepare|start)\b/.test(String(panel?.className || '')), activeCategory: active === document.body ? 'body' : active === owner?.opener ? 'opener' : dialog?.contains(active) ? 'own' : 'outside', activeIsRole: active === dialog, activeTag: active?.tagName || null }
}
// Forward every public VM read/write to its original receiver. Only scheduling
// is observed; the original production method and real Vue nextTick still run.
Page.methods.openBulkCourseModal = function (...args) {
  const vm = this
  const forwarding = new Proxy(vm, {
    get(target, key) {
      if (key !== '$nextTick') return Reflect.get(target, key, target)
      const actual = Reflect.get(target, key, target)
      return function (callback, ...rest) {
        const owner = vm.bulkCourseOwner, entry = { owner, callback, before: null, after: null, ran: false }
        cur.scheduled.push(entry)
        return Reflect.apply(actual, vm, [function (...values) {
          entry.before = state(vm, owner)
          const result = Reflect.apply(callback, this, values)
          entry.after = state(vm, owner); entry.ran = true
          return result
        }, ...rest])
      }
    },
    set(target, key, value) { return Reflect.set(target, key, value, target) }
  })
  return Reflect.apply(originalOpen, forwarding, args)
}
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 4)); await nextTick(); await flushPromises() }
async function until(predicate, label) { const start = performance.now(); while (!predicate() && performance.now() - start < 1500) { await new Promise(resolve => requestAnimationFrame(resolve)); await settle() } assert.ok(predicate(), label) }
function check(label, passed, qualified = true) { cur.checks.push({ label, qualified, passed: qualified ? Boolean(passed) : null }) }
function button(text) { const b = root.findAll('button').find(n => compact(n.text()) === text); assert.ok(b, text); return b.element }
function field() { const n = root.find('input[placeholder="搜索问题"]').element; assert.ok(n); return n }
async function setup() {
  user.setStoredCurrentUser(A)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: Page }] })
  history = router.options.history; await router.push('/adapter/component'); await router.isReady()
  root = mount(Page, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false }, config: { errorHandler: () => cur.io.runtimeCount++ } } })
  await settle(); await until(() => root.find('tr[data-row-key="9001"] input[type="checkbox"]').exists(), 'synthetic row ready')
  root.find('tr[data-row-key="9001"] input[type="checkbox"]').element.click(); await settle()
  assert.deepEqual(root.vm.selectedRowKeys.map(String), ['9001'])
}
async function open() {
  const before = cur.scheduled.length, opener = button('批量编辑课程名'); assert.ok(!opener.disabled); opener.focus(); assert.strictEqual(document.activeElement, opener); opener.click()
  await until(() => cur.scheduled.length > before && cur.scheduled.at(-1).ran, 'real native Vue nextTick callback executed')
  const entry = cur.scheduled.at(-1), dialog = role(root.vm); assert.ok(dialog && visible(dialog))
  return { entry, dialog, opener }
}
async function entered(dialog) { await until(() => !/-(?:enter|appear)-(?:active|from|prepare|start)\b/.test(String(dialog.querySelector('.ant-modal')?.className || '')), 'original Transition naturally completes') }
async function close(dialog) { const b = [...dialog.querySelectorAll('.ant-modal-footer button')].find(n => ['取消', 'Cancel'].includes(compact(n.textContent))); assert.ok(b); b.focus(); b.click(); await settle(); await until(() => !root.vm.bulkCourseVisible && !visible(dialog), 'original native Cancel closes') }
async function cleanup() {
  if (other?.exists()) other.unmount()
  if (root?.exists()) root.unmount()
  await settle(); history?.destroy(); user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
  cur.cleanup = { rootsUnmounted: !root?.exists() && !other?.exists(), bodyEmpty: document.body.children.length === 0 }; root = other = history = null
}
function write() {
  const out = { cases: cases.map(c => ({ name: c.name, completedFlow: c.completedFlow, checks: c.checks, phaseQualified: c.phaseQualified, observations: c.scheduled.map(e => ({ ran: e.ran, before: e.before, after: e.after })), io: c.io, cleanup: c.cleanup })), sourceStable: sha(PAGE) === sourceSHA, physicalTabOrPaintClaim: false }
  console.log('BULK_OPENING_NATIVE ' + JSON.stringify(out))
  if (process.env.BULK_OPENING_LEDGER) fs.writeFileSync(process.env.BULK_OPENING_LEDGER, JSON.stringify(out) + '\n')
}
function register(name, fn) {
  test(name, { timeout: 25000 }, async () => {
    cur = { name, completedFlow: false, checks: [], scheduled: [], io: { reads: 0, writes: 0, realIO: [], runtimeCount: 0 }, phaseQualified: null }; cases.push(cur)
    try {
      await fn(); cur.completedFlow = true
      check('zero real IO/runtime and API writes', cur.io.writes === 0 && cur.io.realIO.length === 0 && cur.io.runtimeCount === 0)
      assert.equal(cur.checks.filter(c => !c.qualified).length, 0, 'QUALIFICATION_ONLY: beforeAfterEnter stage must be observable, otherwise NQ not product RED')
      assert.equal(cur.checks.filter(c => c.qualified && !c.passed).length, 0, 'qualified owned-focus observations')
    } finally { await cleanup(); write(); assert.equal(sha(PAGE), sourceSHA); assert.deepEqual(cur.io.realIO, []); assert.equal(cur.io.runtimeCount, 0); assert.equal(cur.io.writes, 0) }
  })
}
register('OP01 actual preAfterEnter modal handoff and subsequent own course focus', async () => {
  await setup()
  const opener = button('批量编辑课程名'), calibration = document.createElement('div'); calibration.tabIndex = -1; document.body.appendChild(calibration); calibration.focus()
  check('independent tabindex-1 native focus is supported', document.activeElement === calibration); calibration.remove(); opener.focus(); check('calibration phase restores native opener', document.activeElement === opener)
  const { entry, dialog } = await open(), before = entry.before
  cur.phaseQualified = Boolean(before.ownerCurrent && before.connected && before.visible && before.tabIndexAttribute !== null && before.entering && ['body', 'opener'].includes(before.activeCategory))
  check('first semantic wrapper handoff occurs before natural AfterEnter', entry.after.activeCategory === 'own', cur.phaseQualified)
  const input = dialog.querySelector('input'); assert.ok(input && !input.disabled); input.focus(); check('own current course input remains focusable', document.activeElement === input)
  await entered(dialog); await settle()
  check('natural AfterEnter contains guard leaves current course input alone', document.activeElement === input)
  check('selection and original course value are unchanged', root.vm.selectedRowKeys.map(String).join(',') === '9001' && root.vm.bulkCourseName === '' && cur.io.reads === 2)
  await close(dialog)
})
register('OP02 scheduled callback respects own input, foreground, other dialog, close and actor retirement', async () => {
  await setup(); const { entry, dialog, opener } = await open(); await entered(dialog); await settle()
  const input = dialog.querySelector('input'); assert.ok(input); input.focus(); entry.callback()
  check('replayed callback does not replace current own course input', document.activeElement === input)
  const foreground = field(); foreground.focus(); entry.callback()
  check('replayed callback does not steal legitimate foreground input', document.activeElement === foreground)
  opener.focus()
  other = mount(Antd.Modal, { attachTo: document.body, props: { open: true, title: 'SYNTHETIC_OTHER_DIALOG', focusTriggerAfterClose: false }, global: { plugins: [Antd], stubs: { transition: false, 'transition-group': false } } })
  await until(() => [...document.querySelectorAll('[role="dialog"]')].some(n => n !== dialog && visible(n)), 'second real Antd modal admitted')
  opener.focus(); entry.callback()
  check('other visible real dialog prevents early owned handoff', document.activeElement === opener)
  other.unmount(); other = null; await settle(); await close(dialog); foreground.focus(); entry.callback()
  check('closed owner scheduled callback cannot focus retired dialog', !entry.owner.isCurrent() && document.activeElement === foreground)
  const count = cur.scheduled.length; opener.focus(); opener.click(); user.setStoredCurrentUser(B)
  await until(() => cur.scheduled.length > count && cur.scheduled.at(-1).ran, 'actual queued callback runs after actor retirement')
  const retired = cur.scheduled.at(-1)
  check('actor before native tick retires original owner', retired.before.ownerCurrent === false && retired.after.ownerCurrent === false && retired.before.activeCategory === retired.after.activeCategory && retired.before.activeTag === retired.after.activeTag)
  check('guard challenges never dispatch business writes', cur.io.writes === 0)
})
