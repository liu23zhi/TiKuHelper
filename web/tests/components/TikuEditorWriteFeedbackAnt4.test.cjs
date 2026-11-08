/* Actual Vue/Antd/currentUser and real OptionBox/AnswerBox. Only API outcomes,
 * clipboard and external IO boundaries are synthetic. Same fixture for B/M/R.
 * No backend commit, browser/device, cross-dialog idempotency or refund claim. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), fs = require('node:fs'), crypto = require('node:crypto'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter); Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises, DOMWrapper } = require('@vue/test-utils'), { nextTick } = require('vue'), { createRouter, createMemoryHistory } = require('vue-router'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const { toUserRequestError } = require(path.join(web, 'src/utils/requestRetry.mjs'))
const { buildResponsePayload, captureTransportRequestError, getTransportRequestId } = require(path.join(web, 'src/utils/requestHelpers.mjs'))
const PAGE = path.join(web, 'src/pages/TikuComponent/index.vue'), sha = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'), sourceSHA = sha(PAGE)
const A = { id: 9641, username: 'SYNTHETIC_EDITOR_A', role: 'admin' }, B = { id: 9642, username: 'SYNTHETIC_EDITOR_B', role: 'admin' }
const PRIVATE = 'SYNTHETIC_PRIVATE_ERROR_URL_BODY_DIAGNOSTIC', ID = '1234567890abcdef1234567890abcdef', DRAFT = 'SYNTHETIC 未确认题目草稿'
const copy = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value)), compact = value => String(value || '').replace(/\s/g, '')
const row = () => ({ id: 101, type: 4, question: 'SYNTHETIC 原始题面', options: '[]', answer: '["SYNTHETIC答案甲","SYNTHETIC答案甲"]', course_name: 'SYNTHETIC课程', source: 7, source_name: 'SYNTHETIC来源', extra: 'SYNTHETIC低频字段', custom_fixture_field: 0 })
const ok = data => ({ status: 200, data })
let rows = [row()], calls = [], wrappers = [], histories = [], errors = [], network = [], notices = [], copied = [], autoFailure = null, nextReadFailure = null
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_QUESTION_WRITE_IO_' + kind) }
window.XMLHttpRequest.prototype.open = function () { return deny('XHR') }; globalThis.fetch = window.fetch = () => deny('FETCH'); window.open = () => deny('WINDOW_OPEN')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', event => errors.push(String(event.error?.stack || event.message))); window.addEventListener('unhandledrejection', event => errors.push(String(event.reason?.stack || event.reason)))
Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async text => { copied.push(text) } } })
for (const kind of ['success', 'warning', 'error']) { const native = Antd.message[kind].bind(Antd.message); Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return native(...args) } }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = Object.fromEntries(['getCourses', 'getQuestions', 'createQuestions', 'updateQuestions', 'delQuestions', 'importQuestions'].map(name => [name, payload => {
  const call = { name, payload: copy(payload), actor: user.getStoredCurrentUser()?.id, completed: false }; calls.push(call)
  if (name === 'getQuestions' || name === 'getCourses') { call.completed = true; if (name === 'getQuestions' && nextReadFailure) { const error = nextReadFailure; nextReadFailure = null; return Promise.reject(error) } return Promise.resolve(name === 'getQuestions' ? ok({ items: copy(rows), total: rows.length }) : ok(['SYNTHETIC课程'])) }
  if (!['createQuestions', 'updateQuestions'].includes(name)) return deny('UNEXPECTED_MUTATION_' + name)
  if (autoFailure) { call.completed = true; return Promise.reject(autoFailure) }
  return new Promise((resolve, reject) => { call.resolve = value => { call.completed = true; resolve(value) }; call.reject = value => { call.completed = true; reject(value) } })
}]))
require.cache[apiPath] = api
const swalPath = require.resolve('sweetalert2'), swal = new Module(swalPath); swal.loaded = true; swal.exports = { fire: () => deny('SWEETALERT'), getPopup: () => null, close: () => {} }; require.cache[swalPath] = swal
const Page = require(PAGE), OptionBox = require(path.join(web, 'src/components/OptionBox/index.vue')), AnswerBox = require(path.join(web, 'src/components/AnswerBox/index.vue'))
const writes = () => calls.filter(c => ['createQuestions', 'updateQuestions'].includes(c.name)), reads = () => calls.filter(c => c.name === 'getQuestions').length
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 3)); await nextTick(); await flushPromises() }
async function click(node) { assert.ok(node, 'actual native control exists'); node.click(); await settle() }
function named(w, name) { const b = w.findAll('button').find(b => compact(b.text()) === compact(name)); assert.ok(b, 'actual native button ' + name); return b.element }
function dialog(w) { const form = w.vm.questionDialogForm, d = (form?.$el || form)?.closest?.('.ant-modal'); assert.ok(d && d.isConnected && !d.hidden && d.style.display !== 'none', 'current owner visible Antd editor Modal'); return d }
function modalButton(w, label) { const labels = label === '确定' ? ['确定', '确认', 'OK'] : ['取消', 'Cancel']; const b = [...dialog(w).querySelectorAll('.ant-modal-footer button')].find(b => labels.includes(compact(b.textContent))); assert.ok(b, 'actual Modal ' + label); return b }
async function page(reuse = false) {
  if (!reuse) user.setStoredCurrentUser(A)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: Page }] }); await router.push('/adapter/component'); await router.isReady(); histories.push(router.options.history)
  const w = mount(Page, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false }, config: { errorHandler: error => errors.push(String(error?.stack || error)) } } }); wrappers.push(w); await settle(); return w
}
async function waitEditor(w) {
  const deadline = Date.now() + 1100
  function ready() { const child = w.findComponent(OptionBox).exists() ? w.findComponent(OptionBox) : w.findComponent(AnswerBox); const form = w.vm.questionDialogForm?.$el; return Boolean(w.vm.questionDialogOwner?.isCurrent() && form?.isConnected && child.exists() && form.contains(child.element) && child.findAll('input.ant-input, textarea').every((input, index) => input.element.value === String(child.vm.data[index].value ?? ''))) }
  while (!ready() && Date.now() < deadline) { await new Promise(resolve => requestAnimationFrame(resolve)); await settle() }
  assert.ok(ready(), 'owned native editor and child controls ready')
}
async function typeQuestion(w, value) {
  const input = dialog(w).querySelector('textarea'); assert.ok(input)
  let control
  const deadline = Date.now() + 1000
  while (Date.now() < deadline) { control = w.findAllComponents(Antd.Textarea).find(c => c.element === input || c.element.contains(input)); if (control && input.isConnected && input.value === String(control.props('value') ?? '') && typeof control.vm.$.vnode.props['onUpdate:value'] === 'function') break; await settle() }
  assert.ok(control, 'actual controlled textarea'); input.value = value; await new DOMWrapper(input).trigger('input'); await settle(); assert.equal(control.props('value'), value)
}
async function open(w, operation = 'update') { await click(named(w, operation === 'create' ? '添加' : '编辑')); await waitEditor(w); await typeQuestion(w, DRAFT) }
async function issue(w) { const before = writes().length; await click(modalButton(w, '确定')); assert.equal(writes().length, before + 1, 'one current native OK admits one mock mutation'); return writes().at(-1) }
function unknown(operation) { const original = Object.assign(new Error('Network Error'), { code: 'ERR_NETWORK', config: { url: PRIVATE }, detail: PRIVATE }); const error = toUserRequestError({ method: operation === 'create' ? 'post' : 'put', url: '/adapter-service/questions' }, original); error.diagnosticId = PRIVATE; assert.equal(error.code, 'UNKNOWN_OUTCOME'); assert.equal(getTransportRequestId(error), undefined); return error }
function httpFailure(status, fulfilled) { const response = { status, headers: { 'x-request-id': ID }, data: { message: PRIVATE, detail: PRIVATE, diagnosticId: PRIVATE, ...(status === 400 ? { error: { code: 'INVALID_INPUT' } } : {}) } }; return fulfilled ? buildResponsePayload(response) : captureTransportRequestError(Object.assign(new Error(PRIVATE), { response, config: { url: PRIVATE } })) }
function draft(w) { const c = w.findComponent(OptionBox).exists() ? w.findComponent(OptionBox) : w.findComponent(AnswerBox); return { form: copy(w.vm.formData), child: copy(c.vm.data), files: copy(w.vm.fileList) } }
function safe(w, original, kind) {
  const d = dialog(w), section = d.querySelector('.request-failure'); assert.ok(section, 'durable failure is rendered inside the owned editor, not only a toast')
  if (kind) assert.equal(section.getAttribute('data-failure-kind'), kind)
  const alert = w.findAllComponents({ name: 'RequestFailureAlert' }).find(c => ['object', 'function'].includes(typeof original) && c.props('failure') === original)
  if (original && ['object', 'function'].includes(typeof original)) { assert.ok(alert, 'exact original failure object preserves transport WeakMap identity'); assert.equal(alert.props('retryable'), false, 'write feedback never exposes generic automatic resubmit') }
  assert.equal(d.querySelector('[data-action="retry-request"]'), null); assert.ok(!document.body.textContent.includes(PRIVATE)); assert.ok(!notices.some(n => n.text.includes(PRIVATE))); assert.ok(!notices.some(n => n.kind === 'success'), 'unconfirmed/rejected write cannot claim success')
  return section
}
function checkButton(w) { const node = dialog(w).querySelector('button[data-question-write-check]'); assert.ok(node, 'explicit readonly result-check native control'); return node }
function capturedClick(w, node) { const button = w.findAllComponents(Antd.Button).find(b => b.element === node); assert.ok(button, 'actual Antd check button'); const fn = button.props('onClick'); assert.equal(typeof fn, 'function'); return fn }
function witness(label, w) { console.log('QUESTION_WRITE_OBSERVED ' + JSON.stringify({ label, visible: w?.vm.visible ?? false, mutations: writes().map(c => ({ name: c.name, actor: c.actor, completed: c.completed })), readonlySearches: reads(), notices, network, runtimeErrors: errors, browserClaim: false })) }
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  for (const c of calls) if (!c.completed && c.resolve) c.resolve(ok({}))
  await settle(); for (const h of histories) h.destroy(); user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
  console.log('QUESTION_WRITE_SAFETY ' + JSON.stringify({ sourceSHA256: sha(PAGE), mockMutationCount: writes().length, network, runtimeErrors: errors }))
  assert.equal(sha(PAGE), sourceSHA); assert.deepEqual(network, []); assert.deepEqual(errors, [])
  rows = [row()]; calls = []; wrappers = []; histories = []; errors = []; network = []; notices = []; copied = []; autoFailure = null; nextReadFailure = null
})

for (const operation of ['create', 'update']) test('QW01 unknown ' + operation + ' remains durable, keeps draft and blocks both repeated native OK and direct confirm', async () => {
  const w = await page(); await open(w, operation); const before = draft(w), c = await issue(w), error = unknown(operation); c.reject(error); await settle(); autoFailure = error
  await click(modalButton(w, '确定')); await w.vm.confirm(); await settle(); witness('unknown-' + operation, w)
  assert.equal(writes().length, 1, 'unknown outcome locks only this dialog against native and direct duplicate mutation'); assert.deepEqual(draft(w), before); assert.equal(w.vm.visible, true)
  const alert = safe(w, error, 'unknown-outcome'); assert.ok(alert.textContent.includes('操作结果尚未确认') && alert.textContent.includes('请先刷新核对结果，勿重复提交或支付。')); assert.equal(alert.querySelector('[data-request-diagnostic]'), null, 'no fake diagnostic from enumerable Error fields'); assert.equal(modalButton(w, '确定').disabled, true)
})
test('QW02 readonly check refreshes only original list read, never acknowledges unknown, and retired check cannot affect new dialog', async () => {
  const w = await page(); await open(w); const c = await issue(w), error = unknown('update'); c.reject(error); await settle(); safe(w, error, 'unknown-outcome'); const before = draft(w), check = checkButton(w), stale = capturedClick(w, check), r = reads(), m = writes().length
  nextReadFailure = httpFailure(503, false); await click(check); assert.equal(reads(), r + 1); assert.equal(writes().length, m); assert.deepEqual(draft(w), before); safe(w, error, 'unknown-outcome'); assert.equal(modalButton(w, '确定').disabled, true); rows = [{ ...row(), question: DRAFT }]; await click(checkButton(w)); assert.equal(reads(), r + 2); assert.equal(writes().length, m); assert.deepEqual(draft(w), before); safe(w, error, 'unknown-outcome'); assert.equal(modalButton(w, '确定').disabled, true); await w.vm.confirm(); assert.equal(writes().length, m)
  await click(modalButton(w, '取消')); await open(w); const fresh = draft(w), currentReads = reads(); stale(new Event('click')); await settle(); assert.equal(reads(), currentReads); assert.equal(writes().length, m); assert.deepEqual(draft(w), fresh); assert.equal(dialog(w).querySelector('.request-failure'), null); assert.equal(modalButton(w, '确定').disabled, false); witness('readonly-check-no-ack-and-new-dialog', w)
})
for (const [status, fulfilled, kind] of [[400, true, 'input'], [403, false, 'forbidden'], [500, false, 'upstream']]) test('QW03 confirmed HTTP ' + status + ' is durable and safe, preserves real diagnostic identity, and retries only explicit native OK', async () => {
  const w = await page(); await open(w); const before = draft(w), c = await issue(w), failure = httpFailure(status, fulfilled); if (fulfilled) c.resolve(failure); else c.reject(failure); await settle()
  assert.equal(writes().length, 1); assert.deepEqual(draft(w), before); const alert = safe(w, failure, kind); assert.equal(getTransportRequestId(failure), ID); assert.equal(alert.querySelector('[data-request-diagnostic]')?.textContent, ID); await click(alert.querySelector('[data-action="copy-request-diagnostic"]')); assert.deepEqual(copied, [ID]); assert.equal(writes().length, 1)
  assert.equal(modalButton(w, '确定').disabled, false); const second = await issue(w); assert.equal(writes().length, 2); second.resolve(ok({})); await settle(); assert.equal(w.vm.visible, false); assert.ok(notices.some(n => n.kind === 'success')); witness('confirmed-' + status + '-explicit-retry', w)
})
test('QW04 malformed fulfilled result keeps a safe durable draft and does not auto-retry or invent diagnostics', async () => {
  const w = await page(); await open(w); const before = draft(w), c = await issue(w); c.resolve(0); await settle(); assert.equal(writes().length, 1); assert.deepEqual(draft(w), before); const alert = safe(w); assert.ok(['unknown', 'unknown-outcome'].includes(alert.getAttribute('data-failure-kind'))); assert.equal(alert.querySelector('[data-request-diagnostic]'), null); witness('malformed', w)
})
for (const transition of ['actor-B', 'role-ABA', 'logout-return']) test('QW05 late retired write failure cannot publish into a new editor: ' + transition, async () => {
  const w = await page(); await open(w); const old = await issue(w)
  if (transition === 'actor-B') user.setStoredCurrentUser(B)
  else if (transition === 'role-ABA') { user.setStoredCurrentUser({ ...A, role: 'user' }); user.setStoredCurrentUser(A) }
  else { user.clearStoredCurrentUser(); user.setStoredCurrentUser(A) }
  await settle(); await open(w); await typeQuestion(w, 'SYNTHETIC 新owner草稿'); const before = draft(w), r = reads(); old.reject(unknown('update')); await settle(); assert.deepEqual(draft(w), before); assert.equal(w.vm.visible, true); assert.equal(dialog(w).querySelector('.request-failure'), null); assert.equal(reads(), r); assert.equal(writes().length, 1); assert.ok(!notices.some(n => n.kind === 'success')); witness('late-' + transition, w)
})
test('QW06 old unmounted mutation failure cannot affect same-module re-entry editor', async () => {
  const oldPage = await page(); await open(oldPage); const old = await issue(oldPage); oldPage.unmount(); await settle(); const w = await page(true); await open(w); await typeQuestion(w, 'SYNTHETIC 重入草稿'); const before = draft(w), r = reads(); old.reject(unknown('update')); await settle(); assert.deepEqual(draft(w), before); assert.equal(dialog(w).querySelector('.request-failure'), null); assert.equal(reads(), r); assert.equal(writes().length, 1); witness('late-unmounted', w)
})
