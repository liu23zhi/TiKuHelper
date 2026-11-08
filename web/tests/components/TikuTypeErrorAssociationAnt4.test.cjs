/* Actual Vue/Antd/currentUser/Form/Select/widgets. Only API outcomes and external
 * IO are synthetic. No added type rule/dropdown option or browser/reader claim.
 * TIKU_WEB_SOURCE/UI_TEST_HELPER keep this exact fixture portable for B/M/R. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils'), { nextTick, defineComponent, h } = require('vue'), { createRouter, createMemoryHistory } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js')), { toUserRequestError } = require(path.join(web, 'src/utils/requestRetry.mjs'))
const PAGE = path.join(web, 'src/pages/TikuComponent/index.vue'), sha = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'), sourceSHA = sha(PAGE)
const A = { id: 9751, username: 'SYNTHETIC_TYPE_A', role: 'admin' }, B = { id: 9752, username: 'SYNTHETIC_TYPE_B', role: 'admin' }
const ERROR = '题型必须为32位整数，请重新选择。', DRAFT = 'SYNTHETIC 类型纠正草稿', clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value)), compact = value => String(value || '').replace(/\s/g, '')
const row = type => ({ id: 101, type, question: 'SYNTHETIC 原始题面', options: '["甲","乙"]', answer: '["甲","甲"]', course_name: 'SYNTHETIC课程', source: 7, source_name: 'SYNTHETIC来源', extra: 'SYNTHETIC低频字段', custom_fixture_field: 0 })
let rows = [row(4)], calls = [], roots = [], histories = [], errors = [], network = [], rejectWrite = null
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_TYPE_ASSOCIATION_IO_' + kind) }
window.XMLHttpRequest.prototype.open = () => deny('XHR'); globalThis.fetch = window.fetch = () => deny('FETCH'); window.open = () => deny('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = Object.fromEntries(['getCourses', 'getQuestions', 'createQuestions', 'updateQuestions', 'delQuestions', 'importQuestions'].map(name => [name, payload => {
  calls.push({ name, payload: clone(payload), actor: user.getStoredCurrentUser()?.id })
  if (name === 'getQuestions') return Promise.resolve({ status: 200, data: { items: clone(rows), total: rows.length } })
  if (name === 'getCourses') return Promise.resolve({ status: 200, data: ['SYNTHETIC课程'] })
  if (!['updateQuestions', 'createQuestions'].includes(name)) return deny('UNEXPECTED_MUTATION_' + name)
  return rejectWrite ? Promise.reject(rejectWrite) : Promise.resolve({ status: 200, data: {} })
}]))
require.cache[apiPath] = api
const swalPath = require.resolve('sweetalert2'), swal = new Module(swalPath); swal.loaded = true; swal.exports = { fire: () => deny('SWEETALERT'), getPopup: () => null, close: () => {} }; require.cache[swalPath] = swal
const Page = require(PAGE), OptionBox = require(path.join(web, 'src/components/OptionBox/index.vue')), AnswerBox = require(path.join(web, 'src/components/AnswerBox/index.vue'))
const writes = () => calls.filter(c => ['updateQuestions', 'createQuestions'].includes(c.name))
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 4)); await nextTick(); await flushPromises() }
async function until(check, label) { const start = performance.now(); while (!check() && performance.now() - start < 1200) { await new Promise(resolve => requestAnimationFrame(resolve)); await settle() } assert.ok(check(), label) }
async function click(node) { assert.ok(node, 'actual native control'); node.click(); await settle() }
function named(w, label) { const b = w.findAll('button').find(b => compact(b.text()) === compact(label)); assert.ok(b, 'actual native button ' + label); return b.element }
function dialog(w) { const form = w.vm.questionDialogForm, d = (form?.$el || form)?.closest?.('.ant-modal'); assert.ok(d && d.isConnected && !d.hidden && d.style.display !== 'none', 'current owner actual editor Modal'); return d }
function modalButton(w, label) { const labels = label === '确定' ? ['确定', '确认', 'OK'] : ['取消', 'Cancel']; const b = [...dialog(w).querySelectorAll('.ant-modal-footer button')].find(b => labels.includes(compact(b.textContent))); assert.ok(b, 'actual Modal action ' + label); return b }
function typeInput(w) { const node = dialog(w).querySelector('input[role="combobox"]'); assert.ok(node, 'actual owned Antd type combobox'); return node }
function visible(node) { if (!node?.isConnected) return false; for (let p = node; p; p = p.parentElement) { const s = getComputedStyle(p); if (p.hidden || p.inert || p.getAttribute('aria-hidden') === 'true' || s.display === 'none' || s.visibility === 'hidden') return false } return true }
function errorNodes(w) { const input = typeInput(w), ids = [...new Set((String(input.getAttribute('aria-describedby') || '') + ' ' + String(input.getAttribute('aria-errormessage') || '')).split(/\s+/).filter(Boolean))]; return ids.map(id => document.getElementById(id)).filter(node => node && dialog(w).contains(node) && visible(node) && node.textContent.includes(ERROR)) }
async function invalid(w) { await until(() => typeInput(w).getAttribute('aria-invalid') === 'true' && errorNodes(w).length > 0, 'native combobox invalid state and current visible Chinese description are associated'); const input = typeInput(w); await until(() => document.activeElement === input, 'invalid submit returns native keyboard focus to this exact type field'); assert.ok(!input.disabled && input.tabIndex >= 0, 'field remains keyboard reachable'); return { input, errors: errorNodes(w) } }
function noError(w) { assert.notEqual(typeInput(w).getAttribute('aria-invalid'), 'true'); assert.equal(errorNodes(w).length, 0) }
function capturedTypeUpdate(w) { const select = w.findAllComponents(Antd.Select).find(c => dialog(w).contains(c.element)); assert.ok(select, 'actual type Select'); const handler = select.vm.$.vnode.props['onUpdate:value']; assert.equal(typeof handler, 'function', 'existing render-bound owned Select update callback'); return handler }
async function page(type = 4, twins = false, reuse = false) {
  rows = [row(type)]; if (!reuse) user.setStoredCurrentUser(A)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: Page }] }); await router.push('/adapter/component'); await router.isReady(); histories.push(router.options.history)
  const Root = twins ? defineComponent({ render: () => h('section', [h(Page, { key: 'first' }), h(Page, { key: 'second' })]) }) : Page
  const root = mount(Root, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false }, config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); roots.push(root); await settle(); return { root, pages: twins ? root.findAllComponents(Page) : [root] }
}
async function ready(w) { await until(() => { const form = w.vm.questionDialogForm?.$el, option = w.findComponent(OptionBox), answer = w.findComponent(AnswerBox), child = option.exists() ? option : answer; return Boolean(w.vm.questionDialogOwner?.isCurrent() && form?.isConnected && child.exists() && form.contains(child.element) && child.findAll('input.ant-input, textarea').every((input, i) => input.element.value === String(child.vm.data[i].value ?? ''))) }, 'actual current form/widget inputs ready') }
async function open(w) { await click(named(w, '编辑')); await ready(w); await until(() => dialog(w).contains(document.activeElement), 'actual Antd dialog enter focus is ready before user interaction') }
async function question(w, value = DRAFT) { const input = dialog(w).querySelector('textarea'); assert.ok(input); input.value = value; await new DOMWrapper(input).trigger('input'); await settle(); assert.equal(w.vm.formData.question, value) }
async function chooseZero(w) { dialog(w).querySelector('.ant-select-selector').dispatchEvent(new MouseEvent('mousedown', { bubbles: true })); await settle(); const option = [...document.querySelectorAll('.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option')].find(n => n.textContent.trim() === '单选题'); assert.ok(option, 'original real dropdown zero type option'); await click(option); await ready(w); assert.equal(w.vm.formData.type, 0) }
function snapshot(w) { const c = w.findComponent(OptionBox).exists() ? w.findComponent(OptionBox) : w.findComponent(AnswerBox); return { form: clone(w.vm.formData), child: clone(c.vm.data) } }
function witness(label, w) { const input = w?.vm.visible ? typeInput(w) : null; console.log('TYPE_ERROR_NATIVE ' + JSON.stringify({ label, type: w?.vm.formData.type, mutations: writes().length, invalid: input?.getAttribute('aria-invalid') ?? null, describedby: input?.getAttribute('aria-describedby') ?? null, errorIDs: input ? errorNodes(w).map(n => n.id) : [], nativeFocusIsType: Boolean(input && document.activeElement === input), network, runtimeErrors: errors, nativeDeviceClaim: false })) }
afterEach(async () => {
  for (const root of roots) if (root.exists()) root.unmount()
  await settle(); for (const history of histories) history.destroy(); user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
  console.log('TYPE_ERROR_SAFETY ' + JSON.stringify({ sourceSHA256: sha(PAGE), mockMutationCount: writes().length, network, runtimeErrors: errors })); assert.equal(sha(PAGE), sourceSHA); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  rows = [row(4)]; calls = []; roots = []; histories = []; errors = []; network = []; rejectWrite = null
})

for (const type of ['abc', null, 2147483648]) test('TA01 invalid legacy type ' + JSON.stringify(type) + ' makes zero writes, keeps draft and associates/focuses real type error', async () => {
  const { pages: [w] } = await page(type); await open(w); await question(w); const before = snapshot(w); await click(modalButton(w, '确定')); witness('invalid-' + JSON.stringify(type), w); assert.equal(writes().length, 0); assert.deepEqual(snapshot(w), before); await invalid(w)
})
test('TA02 native original zero selection clears only type error, preserves draft and original objective typed payload', async () => {
  const { pages: [w] } = await page('abc'); await open(w); await question(w); await click(modalButton(w, '确定')); await invalid(w); assert.equal(writes().length, 0); await chooseZero(w); await until(() => typeInput(w).getAttribute('aria-invalid') !== 'true' && errorNodes(w).length === 0, 'valid native zero clears owned type error'); assert.equal(w.vm.formData.question, DRAFT); assert.ok(w.findComponent(OptionBox).exists()); await click(modalButton(w, '确定')); assert.equal(writes().length, 1); assert.deepEqual(writes()[0].payload, { ...row(0), question: DRAFT, answer: '["甲"]' }); assert.equal(w.vm.visible, false); witness('native-zero-correction', w)
})
test('TA03 valid legacy int32 upper boundary uses original free-text widget and native save without adding a dropdown option', async () => {
  const { pages: [w] } = await page(2147483647); await open(w); await question(w); noError(w); assert.ok(w.findComponent(AnswerBox).exists()); await click(modalButton(w, '确定')); assert.equal(writes().length, 1); assert.deepEqual(writes()[0].payload, { ...row(2147483647), question: DRAFT }); assert.equal(w.vm.visible, false); witness('legacy-int32-boundary', w)
})
test('TA04 same-app error IDs are unique and close/newdialog/actorABA/logout/unmount retire old error events', async () => {
  const { root, pages: [first, second] } = await page('abc', true); await open(first); await click(modalButton(first, '确定')); const a = await invalid(first); await open(second); await click(modalButton(second, '确定')); const b = await invalid(second); const ids = [...a.errors, ...b.errors].map(n => n.id); assert.ok(ids.every(Boolean)); assert.equal(new Set(ids).size, ids.length, 'simultaneous actual help IDs do not collide'); const all = [...document.querySelectorAll('[id]')].map(n => n.id).filter(Boolean); assert.equal(new Set(all).size, all.length)
  const oldFirst = capturedTypeUpdate(first), oldSecond = capturedTypeUpdate(second); await click(modalButton(first, '取消')); rows = [row(4)]; await click(named(first, '刷新')); await open(first); noError(first); assert.ok(errorNodes(second).length > 0, 'one editor close does not clear sibling error'); const outside = document.createElement('button'); document.body.appendChild(outside); outside.focus(); oldFirst('abc'); await settle(); assert.equal(first.vm.formData.type, 4); noError(first); assert.strictEqual(document.activeElement, outside)
  user.setStoredCurrentUser(B); user.setStoredCurrentUser(A); oldFirst('abc'); oldSecond('abc'); await settle(); await open(first); noError(first); assert.equal(first.vm.formData.type, 4)
  const beforeLogout = capturedTypeUpdate(first); user.clearStoredCurrentUser(); beforeLogout('abc'); await settle(); user.setStoredCurrentUser(A); await settle(); await open(first); noError(first); const beforeUnmount = capturedTypeUpdate(first); root.unmount(); await settle(); const { pages: [current] } = await page(4, false, true); await open(current); beforeUnmount('abc'); oldSecond('abc'); await settle(); noError(current); assert.equal(current.vm.formData.type, 4); assert.equal(writes().length, 0); witness('retired-field-events', current)
})
test('TA05 unknown write remains first and locked across native valid type change; association does not acknowledge or resubmit', async () => {
  const { pages: [w] } = await page(4); await open(w); const original = Object.assign(new Error('Network Error'), { code: 'ERR_NETWORK' }); rejectWrite = toUserRequestError({ method: 'put', url: '/adapter-service/questions/101' }, original); assert.equal(rejectWrite.code, 'UNKNOWN_OUTCOME'); await click(modalButton(w, '确定')); assert.equal(writes().length, 1); assert.ok(dialog(w).querySelector('.request-failure[data-failure-kind="unknown-outcome"]')); await chooseZero(w); noError(w); await w.vm.confirm(); await click(modalButton(w, '确定')); assert.equal(writes().length, 1); assert.equal(modalButton(w, '确定').disabled, true); assert.ok(dialog(w).querySelector('.request-failure[data-failure-kind="unknown-outcome"]')); witness('unknown-priority', w)
})
