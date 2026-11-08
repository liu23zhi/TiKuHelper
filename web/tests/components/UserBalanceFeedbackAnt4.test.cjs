/* Real Vue/Antd/currentUser/owner/helpers and RequestFailureAlert. Only the API
 * boundary is synthetic: no server, provider, database or financial IO is used.
 * This unchanged fixture also runs against root-owned B/M/R source copies.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'HTMLVideoElement', 'HTMLAudioElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue'), Swal = require('sweetalert2')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const { toUserRequestError } = require(path.join(web, 'src/utils/requestRetry.mjs'))
const { buildResponsePayload, captureTransportRequestError, getTransportRequestId } = require(path.join(web, 'src/utils/requestHelpers.mjs'))
const ADMIN = { id: 77911, role: 'admin', username: 'SYNTHETIC_BALANCE_ADMIN', balance: 3.234567 }
const OTHER = { id: 77912, role: 'admin', username: 'SYNTHETIC_BALANCE_OTHER', balance: 2.345678 }
const ROW = { id: 81, username: 'SYNTHETIC_BALANCE_TARGET', nickname: 'Synthetic balance target', role: 'user', status: 'enabled', balance: 1.234567, phone: '', wechatId: '', perms: 'search', registerSource: 'admin' }
const ID = '1234567890abcdef1234567890abcdef', OLD_ID = 'abcdef1234567890abcdef1234567890'
const PRIVATE = 'SYNTHETIC_PRIVATE_BALANCE_RESPONSE_URL_CONFIG'
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const ok = data => ({ status: 200, data })
const list = (rows = [ROW]) => ok({ data: clone(rows), total: rows.length, page: 1, pageSize: 10 })
const wrappers = [], calls = [], notices = [], network = [], runtimeErrors = []
let holdReads = false, readRows = [ROW]
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_REAL_BALANCE_NATIVE_IO_' + kind) }
function deferred(name, payload) {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  const call = { name, payload: clone(payload), actor: user.getStoredCurrentUser()?.id, completed: false }
  call.resolve = value => { call.completed = true; resolve(value) }
  call.reject = value => { call.completed = true; reject(value) }
  calls.push(call); return promise
}
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
api.exports = {
  getUserList: payload => {
    if (holdReads) return deferred('get', payload)
    calls.push({ name: 'get', payload: clone(payload), actor: user.getStoredCurrentUser()?.id, completed: true })
    return Promise.resolve(list(readRows))
  },
  getSystemSettings: () => Promise.resolve(ok({ newUserBonusAmount: 0 })),
  getPhoneLocations: () => Promise.resolve(ok({})), getIpLocations: () => Promise.resolve(ok({})),
  adjustUserBalance: payload => deferred('adjust', payload),
  ...Object.fromEntries(['addUser', 'updateUser', 'resetUserPassword', 'delUser', 'createWechatLoginQRCode', 'resolveAdminWechatIDByScan', 'resolveAdminWechatIDByCode'].map(name => [name, () => deny('UNEXPECTED_API_' + name)])),
}
require.cache[apiPath] = api
for (const kind of ['success', 'warning', 'error']) {
  const native = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return native(...args) }
}
window.XMLHttpRequest.prototype.open = function () { return deny('XHR') }
globalThis.fetch = window.fetch = () => deny('FETCH')
globalThis.WebSocket = window.WebSocket = class { constructor() { deny('WEBSOCKET') } }
for (const name of ['node:http', 'node:https']) { const transport = require(name); transport.request = () => deny(name); transport.get = () => deny(name) }
window.addEventListener('error', event => runtimeErrors.push(String(event.error?.stack || event.message)))
window.addEventListener('unhandledrejection', event => runtimeErrors.push(String(event.reason?.stack || event.reason)))
const Page = require(path.join(web, 'src/pages/UserList/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function until(check, label) {
  const wall = Date.now(), start = process.hrtime.bigint()
  const elapsed = () => Number(process.hrtime.bigint() - start) / 1e6
  while (elapsed() < 1000) {
    const value = check(); if (value) return value
    await new Promise(resolve => setTimeout(resolve, 5)); await settle()
  }
  assert.fail(label + ' ' + JSON.stringify({ wallElapsedMs: Date.now() - wall, monotonicElapsedMs: elapsed() }))
}
async function page(account = ADMIN) {
  user.setStoredCurrentUser(account)
  // Install the actual router before setup. VTU global.mocks is injected only in
  // beforeCreate and cannot model setup-time $route dependency tracking.
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/users', component: { render: () => null } }, { path: '/elsewhere', component: { render: () => null } }] })
  await router.push('/users'); await router.isReady()
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  w.fixtureRouter = router; wrappers.push(w); await settle(); return w
}
const compact = text => String(text).replace(/\s/g, '')
function button(root, label) {
  const node = [...root.querySelectorAll('button')].find(item => compact(item.textContent) === compact(label))
  assert.ok(node, 'ACTUAL_NATIVE_BUTTON_' + label); return node
}
function modal(title) {
  const node = [...document.querySelectorAll('.ant-modal')].find(item => item.querySelector('.ant-modal-title')?.textContent === title && item.closest('.ant-modal-wrap')?.style.display !== 'none')
  assert.ok(node, 'ACTUAL_NATIVE_MODAL_' + title); return node
}
function typeNow(root, selector, value) {
  const node = root.querySelector(selector); assert.ok(node, 'ACTUAL_NATIVE_INPUT_' + selector)
  node.value = value; node.dispatchEvent(new Event('input', { bubbles: true })); node.dispatchEvent(new Event('change', { bubbles: true }))
}
async function type(root, selector, value) { typeNow(root, selector, value); await settle() }
async function prepare(w, mode = 'increase', amount = '0.000062', show = false) {
  if (!w.vm.visible) {
    button(w.element, '编辑').click()
    await until(() => w.vm.visible && document.querySelector('.ant-modal .balance-adjust-button'), 'native editor opens')
  }
  button(modal('编辑用户'), '修改余额').click()
  await until(() => w.vm.balanceVisible && [...document.querySelectorAll('.ant-modal-title')].some(n => n.textContent === '修改用户余额'), 'native balance modal opens')
  const dialog = modal('修改用户余额')
  if (mode !== 'increase') {
    dialog.querySelector('.ant-select-selector').dispatchEvent(new MouseEvent('mousedown', { bubbles: true })); await settle()
    const option = await until(() => [...document.querySelectorAll('.ant-select-item-option')].find(node => node.getAttribute('title') === (mode === 'override' ? '覆盖' : '减少')), 'native balance mode option')
    option.click(); await settle()
  }
  assert.equal(w.vm.balanceForm.mode, mode)
  await type(dialog, 'input[placeholder="请输入金额，最多 6 位小数"]', amount)
  const check = dialog.querySelector('input[type="checkbox"]'); assert.ok(check)
  if (check.checked !== show) { check.click(); await settle() }
  assert.equal(check.checked, show)
}
const readCount = () => calls.filter(call => call.name === 'get').length
const writeCount = () => calls.filter(call => call.name === 'adjust').length
const lastWrite = () => calls.filter(call => call.name === 'adjust').at(-1)
const lastRead = () => calls.filter(call => call.name === 'get').at(-1)
async function issue() {
  const before = writeCount()
  const confirm = button(modal('修改用户余额').querySelector('.ant-modal-footer'), '确认修改')
  await until(() => !confirm.disabled, 'native balance confirm becomes ready')
  confirm.click(); await settle()
  assert.equal(writeCount(), before + 1, 'one native confirmation means one logical UI submission, not transport attempts')
  return lastWrite()
}
async function cancel() {
  button(modal('修改用户余额').querySelector('.ant-modal-footer'), '取消').click(); await settle()
  Antd.message.destroy(); await settle()
}
const region = () => document.querySelector('[data-user-balance-feedback]')
function balanceAlert(w, original = w.vm.balanceFailure) {
  const value = w.findAllComponents({ name: 'RequestFailureAlert' }).find(alert => alert.props('failure') === original)
  assert.ok(value, 'ACTUAL_DURABLE_BALANCE_FAILURE'); return value
}
function safe(w, original, kind) {
  const alert = balanceAlert(w)
  assert.ok(region(), 'durable balance region')
  if (original !== null && ['object', 'function'].includes(typeof original)) assert.equal(alert.props('failure'), original, 'exact original WeakMap key is retained, not cloned/proxied')
  assert.equal(alert.props('retryable'), false, 'a write failure never exposes generic mutation retry')
  assert.equal(region().querySelector('[data-action="retry-request"]'), null)
  if (kind) assert.equal(region().querySelector('.request-failure').getAttribute('data-failure-kind'), kind)
  assert.ok(!document.body.textContent.includes(PRIVATE), 'raw upstream error/body/config/private URL must not render')
  assert.ok(!notices.some(n => n.text.includes(PRIVATE)), 'raw upstream message must not escape via toast')
  assert.ok(!notices.some(n => n.kind === 'success'), 'unconfirmed adjustment cannot claim financial success')
  return alert
}
function unknown() {
  const raw = Object.assign(new Error('Network Error'), { code: 'ERR_NETWORK', config: { url: PRIVATE }, privatePayload: PRIVATE })
  const error = toUserRequestError({ method: 'post', url: '/adapter-service/user/81/balance' }, raw)
  assert.equal(error.code, 'UNKNOWN_OUTCOME'); return error
}
function failure(status = 503, mode = 'rejected', id = ID) {
  const response = { status, headers: id ? { 'x-request-id': id } : {}, data: { message: PRIVATE, detail: PRIVATE, config: { token: PRIVATE, url: PRIVATE } } }
  return mode === 'fulfilled' ? buildResponsePayload(response) : captureTransportRequestError(Object.assign(new Error(PRIVATE), { response, config: { url: PRIVATE } }))
}
async function fail(w, original = unknown()) {
  await prepare(w); const call = await issue(); call.reject(original); await settle(); safe(w, original); return original
}
function retire(w, reason) {
  if (reason === 'draft') w.vm.balanceForm.amount = '0.000063'
  else if (reason === 'target') w.vm.formState.id = 82
  else if (reason === 'dialog') { button(modal('修改用户余额').querySelector('.ant-modal-footer'), '取消').click(); w.vm.openBalanceModal() }
  else if (reason === 'actor') user.setStoredCurrentUser(OTHER)
  else if (reason === 'role') user.setStoredCurrentUser({ ...ADMIN, role: 'user' })
  else if (reason === 'route') return w.fixtureRouter.push('/elsewhere')
  else if (reason === 'entry') w.vm.openBalanceModal()
  else if (reason === 'logout') user.clearStoredCurrentUser()
  else w.unmount()
}
function snapshot(w) {
  return JSON.stringify({ rows: w.vm.dataSource, balance: w.vm.formState.balance, draft: w.vm.balanceForm, target: w.vm.formState.id, visible: w.vm.balanceVisible, busy: w.vm.balanceSubmitting, reconciling: w.vm.balanceReconciling, failure: w.vm.balanceFailure, reconciliation: w.vm.balanceReconciliation, listFailure: w.vm.listFailure })
}
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  for (const call of calls) if (!call.completed && call.resolve) call.resolve(call.name === 'get' ? list() : ok({ balance: 1.234567 }))
  Swal.close(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
  user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('USER_BALANCE_NATIVE_LEDGER ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', calls: calls.map(({ name, payload, actor }) => ({ name, payload, actor })), notices, network, runtimeErrors }))
  const io = network.splice(0), errors = runtimeErrors.splice(0)
  wrappers.length = calls.length = notices.length = 0; holdReads = false; readRows = [ROW]
  assert.deepEqual(io, [], 'no real network/provider/database IO')
  assert.deepEqual(errors, [], 'native runtime errors must fail, never be silently swallowed')
})

test('native legal increase/decrease/signed override preserve six-place payload mode and both statement flags', async () => {
  const w = await page()
  for (const [mode, amount, show, result] of [['increase', '0.000062', false, 1.234629], ['decrease', '0.000062', true, 1.234505], ['override', '-0.000062', false, -0.000062], ['override', '0', true, 0]]) {
    await prepare(w, mode, amount, show); const call = await issue()
    assert.deepEqual(call.payload, { id: 81, mode, amount: Number(amount), showInStatement: show })
    const reads = readCount(); call.resolve(ok({ balance: result })); await settle()
    assert.equal(w.vm.formState.balance, result); assert.equal(w.vm.balanceVisible, false); assert.equal(w.vm.balanceSubmitting, false); assert.equal(readCount(), reads + 1)
    assert.equal(region(), null)
  }
  assert.equal(writeCount(), 4); assert.deepEqual(notices.filter(n => n.kind === 'success').map(n => n.text), Array(4).fill('余额修改成功，已记录日志与账单明细'))
})

test('actual parameter validator blocks invalid modes amounts precision and negative increase/decrease without API IO', async () => {
  const w = await page(); await prepare(w)
  for (const [mode, amount] of [['invalid', '0.000062'], ['increase', ''], ['increase', '-0.000062'], ['decrease', '-0.000062'], ['override', '0.0000001'], ['override', '1000000000000'], ['override', 'Infinity']]) {
    // Antd disables/normalizes some inputs. Deliberate external-state corruption
    // reaches the actual submit validator; it does not pretend a disabled UI did IO.
    w.vm.balanceForm.mode = mode; w.vm.balanceForm.amount = amount
    await w.vm.submitBalanceAdjustment(); await settle()
    assert.equal(writeCount(), 0, mode + '/' + amount); assert.equal(w.vm.balanceSubmitting, false); assert.equal(w.vm.balanceVisible, true)
  }
  assert.equal(readCount(), 1); assert.ok(notices.some(n => n.kind === 'warning'))
})

test('actual UNKNOWN retains its existing caution and durable read-only follow-up never cleans draft or proves arrival', async () => {
  const w = await page(), error = await fail(w), beforeDraft = clone(w.vm.balanceForm)
  safe(w, error, 'unknown-outcome'); assert.equal(getTransportRequestId(error), undefined)
  assert.ok(notices.some(n => n.text === '网络中断，操作结果未确认。请先刷新核对，勿重复提交。'), 'preserve the already-correct original UNKNOWN warning')
  assert.match(region().textContent, /结果.*(?:尚未确认|未确认)/); assert.match(region().textContent, /勿重复提交/)
  assert.equal(region().querySelector('[data-action="copy-request-diagnostic"]'), null)
  const before = { reads: readCount(), writes: writeCount(), calls: calls.length }
  await settle(); assert.equal(calls.length, before.calls, 'no automatic logical resubmission or GET')
  readRows = [{ ...ROW, balance: Number(w.vm.balanceForm.amount) }]
  region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle()
  assert.equal(readCount(), before.reads + 1); assert.equal(writeCount(), before.writes); safe(w, error, 'unknown-outcome')
  assert.equal(w.vm.formState.balance, Number(w.vm.balanceForm.amount), 'only the current target read-only balance display is updated, never treated as a receipt')
  assert.equal(w.vm.dataSource[0].balance, ROW.balance, 'dedicated GET does not replace table rows or pagination')
  assert.deepEqual(clone(w.vm.balanceForm), beforeDraft); assert.equal(w.vm.balanceVisible, true)
  assert.match(region().querySelector('[data-user-balance-reconciliation]').textContent, /不是.*入账凭据|不能|不代表|不证明|仍.*未确认|仍.*待核对/)
  Antd.message.destroy(); await settle(); assert.ok(region().textContent.includes('未确认'), 'durable warning outlives transient notice')
})

for (const status of [401, 403, 429, 503]) test('HTTP ' + status + ' fulfilled/rejected preserve the actual diagnostic key and fixed public reason', async () => {
  const w = await page()
  for (const mode of ['fulfilled', 'rejected']) {
    await prepare(w); const call = await issue(), original = failure(status, mode)
    if (mode === 'fulfilled') call.resolve(original); else call.reject(original)
    await settle(); const alert = safe(w, original, { 401: 'unauthorized', 403: 'forbidden', 429: 'rate-limit', 503: 'upstream' }[status])
    assert.equal(getTransportRequestId(original), ID); assert.ok(alert.text().includes(ID)); assert.equal(readCount(), 1)
    await cancel()
  }
  assert.equal(writeCount(), 2)
})

for (const mode of ['fulfilled', 'rejected']) test(mode + ' primitive failures are durable safe malformed feedback without fabricated IDs', async () => {
  const w = await page()
  for (const value of [null, undefined, false, 0, '', PRIVATE]) {
    await prepare(w); const call = await issue()
    if (mode === 'fulfilled') call.resolve(value); else call.reject(value)
    await settle(); const alert = safe(w, value); assert.equal(alert.props('failure').code, 'MALFORMED_RESPONSE')
    assert.equal(region().querySelector('[data-action="copy-request-diagnostic"]'), null); assert.equal(w.vm.formState.balance, ROW.balance)
    await cancel()
  }
  assert.equal(readCount(), 1); assert.equal(writeCount(), 6)
})

for (const value of [undefined, null, false, {}, NaN]) test('HTTP200 malformed balance ' + String(value) + ' stays uncertain and preserves original diagnostic rather than assigning fake zero', async () => {
  const w = await page(); await prepare(w); const call = await issue()
  const data = value === undefined ? {} : { balance: value }
  const original = buildResponsePayload({ status: 200, headers: { 'x-request-id': ID }, data })
  call.resolve(original); await settle(); const alert = safe(w, original)
  assert.equal(getTransportRequestId(original), ID); assert.ok(alert.text().includes(ID)); assert.equal(w.vm.formState.balance, ROW.balance); assert.equal(w.vm.balanceVisible, true)
  assert.match(region().textContent, /核对|未确认/); const before = writeCount()
  region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle(); assert.equal(writeCount(), before); safe(w, original)
})

test('original HTTP202 remains status-pending with original automatic GET and explicit additional GET only', async () => {
  const w = await page(); await prepare(w); const beforeDraft = clone(w.vm.balanceForm), call = await issue()
  const original = buildResponsePayload({ status: 202, headers: { 'x-request-id': ID }, data: { message: PRIVATE } })
  call.resolve(original); await settle(); safe(w, original)
  assert.ok(notices.some(n => n.kind === 'warning' && n.text === '余额调整状态待核对，请刷新账户信息确认，勿重复提交'))
  assert.equal(readCount(), 2, 'keep the pre-existing 202 automatic read'); assert.equal(writeCount(), 1)
  assert.equal(w.vm.balanceVisible, true); assert.equal(w.vm.formState.balance, ROW.balance); assert.deepEqual(clone(w.vm.balanceForm), beforeDraft)
  region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle(); assert.equal(readCount(), 3); assert.equal(writeCount(), 1); safe(w, original)
})

test('read-only reconciliation has its own safe diagnostic and does not erase adjustment warning or draft', async () => {
  const w = await page(), error = await fail(w), beforeDraft = clone(w.vm.balanceForm)
  holdReads = true; region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle(); const call = lastRead()
  assert.equal(w.vm.balanceReconciling, true); assert.equal(w.vm.balanceSubmitting, false)
  const readError = failure(503, 'rejected', OLD_ID); call.reject(readError); await settle()
  safe(w, error, 'unknown-outcome'); const readAlert = balanceAlert(w, readError); assert.ok(readAlert.text().includes(OLD_ID))
  assert.equal(w.vm.balanceReconciling, false); assert.deepEqual(clone(w.vm.balanceForm), beforeDraft); assert.equal(writeCount(), 1); assert.equal(readCount(), 2)
})

test('two mounted UserList owners keep independent balance failure and draft state', async () => {
  const a = await page(), b = await page(), error = await fail(a)
  safe(a, error); assert.equal(b.vm.balanceFailure, null); assert.notStrictEqual(a.vm.balanceForm, b.vm.balanceForm)
  b.vm.openEditModal({ ...ROW, id: 82 }); b.vm.openBalanceModal(); b.vm.balanceForm.amount = '0.000063'
  assert.equal(Number(a.vm.balanceForm.amount), 0.000062); assert.equal(a.vm.formState.id, 81); assert.equal(b.vm.formState.id, 82)
})

test('same actor display-only balance update does not retire an owned warning or explicit GET capability', async () => {
  const w = await page(), error = await fail(w)
  const reads = readCount(); user.setStoredCurrentUser({ ...ADMIN, balance: 99.000001 }); await settle()
  safe(w, error); assert.equal(readCount(), reads)
  region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle(); assert.equal(readCount(), reads + 1); assert.equal(writeCount(), 1)
})

for (const reason of ['draft', 'target', 'dialog', 'actor', 'role', 'route', 'entry', 'logout', 'unmount']) test(reason + ' retires stale native/captured reconciliation and pending diagnostic copy callbacks', async () => {
  const w = await page(), error = await fail(w, failure())
  const oldRead = region().querySelector('[data-action="reconcile-user-balance"]'), oldCopy = region().querySelector('[data-action="copy-request-diagnostic"]'), callback = w.vm.reconcileUserBalance
  assert.equal(typeof callback, 'function'); assert.ok(oldCopy)
  let finishCopy; const copied = [], originalClipboard = navigator.clipboard
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: value => { copied.push(value); return new Promise(resolve => { finishCopy = resolve }) } } })
  try {
    oldCopy.click(); await settle(); assert.deepEqual(copied, [ID])
    const navigation = retire(w, reason)
    if (navigation) await navigation // only after an actual committed router transition
    const reads = readCount(), writes = writeCount()
    oldRead.click(); oldCopy.click(); callback()
    assert.equal(readCount(), reads, 'same-tick retired callbacks cannot issue GET'); assert.equal(writeCount(), writes); assert.deepEqual(copied, [ID])
    await settle(); const afterRetire = readCount(); oldRead.click(); oldCopy.click(); callback(); finishCopy(); await settle()
    assert.equal(readCount(), afterRetire); assert.equal(writeCount(), writes); assert.deepEqual(copied, [ID])
    assert.ok(!document.body.textContent.includes(ID)); assert.ok(!document.body.textContent.includes('诊断编号已复制'))
    if (reason !== 'unmount') assert.notEqual(w.vm.balanceFailure, error)
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: originalClipboard }) }
})

for (const outcome of ['success', 'failure']) test('pending draft change keeps issued busy until real ' + outcome + ' while preserving original success semantics', async () => {
  const w = await page(); await prepare(w); const call = await issue()
  w.vm.balanceForm.amount = '0.000063'; await settle()
  assert.equal(w.vm.balanceSubmitting, true, 'external draft edit cannot pretend issued financial work was canceled')
  await w.vm.submitBalanceAdjustment(); assert.equal(writeCount(), 1, 'existing busy gate prevents duplicate logical submit')
  if (outcome === 'success') call.resolve(ok({ balance: 99.000062 })); else call.reject(failure(503, 'rejected', OLD_ID))
  await settle(); assert.equal(w.vm.balanceSubmitting, false); assert.equal(w.vm.balanceForm.amount, '0.000063')
  assert.equal(region(), null); assert.ok(!document.body.textContent.includes(OLD_ID))
  if (outcome === 'failure') { assert.equal(w.vm.balanceVisible, true); assert.equal(w.vm.formState.balance, ROW.balance); assert.equal(readCount(), 1); assert.equal(notices.length, 0) }
  else { assert.equal(w.vm.balanceVisible, false); assert.equal(w.vm.formState.balance, 99.000062); assert.equal(readCount(), 2); assert.equal(notices.at(-1).text, '余额修改成功，已记录日志与账单明细') }
})

for (const outcome of ['success', 'failure']) test('old ' + outcome + ' cannot publish or release a newer target dialog issued busy', async () => {
  const w = await page(); await prepare(w); const old = await issue()
  // Native in-flight financial Cancel is intentionally disabled. A real external
  // target/editor replacement retires that lane; no disabled button is bypassed.
  assert.equal(button(modal('修改用户余额').querySelector('.ant-modal-footer'), '取消').disabled, true)
  w.vm.openEditModal({ ...ROW, id: 82 }); await settle(); await prepare(w); const current = await issue()
  const before = calls.length
  if (outcome === 'success') old.resolve(ok({ balance: 99.000062 })); else old.reject(failure(429, 'rejected', OLD_ID))
  await settle(); assert.equal(w.vm.balanceSubmitting, true); assert.equal(w.vm.formState.id, 82); assert.equal(w.vm.formState.balance, ROW.balance); assert.equal(calls.length, before); assert.equal(region(), null)
  const currentError = failure(); current.reject(currentError); await settle(); safe(w, currentError); assert.equal(w.vm.balanceSubmitting, false); assert.equal(writeCount(), 2)
})

for (const outcome of ['success', 'failure']) test('retired reconciliation ' + outcome + ' cannot repaint rows feedback or release a newer GET busy', async () => {
  const w = await page(); await fail(w); holdReads = true
  region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle(); const oldRead = lastRead()
  w.vm.balanceForm.amount = '0.000063'; await settle(); const call = await issue(), currentError = failure()
  call.reject(currentError); await settle(); safe(w, currentError)
  region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle(); const currentRead = lastRead()
  assert.notStrictEqual(oldRead, currentRead); const before = snapshot(w)
  if (outcome === 'success') oldRead.resolve(list([{ ...ROW, id: 999, nickname: 'SYNTHETIC_RETIRED_READ' }]))
  else oldRead.reject(failure(503, 'rejected', OLD_ID))
  await settle(); assert.equal(snapshot(w), before); assert.equal(w.vm.balanceReconciling, true); assert.ok(!document.body.textContent.includes(OLD_ID)); assert.equal(writeCount(), 2)
  currentRead.resolve(list([{ ...ROW, balance: 8.000062, nickname: 'SYNTHETIC_CURRENT_READ' }])); await settle()
  assert.equal(w.vm.balanceReconciling, false); assert.equal(w.vm.formState.balance, 8.000062); assert.equal(w.vm.dataSource[0].nickname, ROW.nickname); safe(w, currentError)
})

test('same mounted page logout then fresh B login restores new feedback without resurrecting A callbacks', async () => {
  const w = await page(); await fail(w, failure(503, 'rejected', OLD_ID))
  const oldRead = region().querySelector('[data-action="reconcile-user-balance"]'), oldCopy = region().querySelector('[data-action="copy-request-diagnostic"]'), callback = w.vm.reconcileUserBalance
  user.clearStoredCurrentUser(); await settle(); assert.equal(region(), null)
  user.setStoredCurrentUser(OTHER); await settle(); const error = await fail(w, failure())
  safe(w, error); assert.equal(w.vm.formState.id, ROW.id); assert.equal(user.getStoredCurrentUser().id, OTHER.id)
  let copied = []; const originalClipboard = navigator.clipboard
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async value => { copied.push(value) } } })
  try {
    const before = { reads: readCount(), writes: writeCount() }; oldRead.click(); oldCopy.click(); callback(); await settle()
    assert.equal(readCount(), before.reads); assert.equal(writeCount(), before.writes); assert.deepEqual(copied, [])
    region().querySelector('[data-action="copy-request-diagnostic"]').click(); await settle(); assert.deepEqual(copied, [ID])
    region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle(); assert.equal(readCount(), before.reads + 1); assert.equal(writeCount(), before.writes); safe(w, error)
    assert.ok(!document.body.textContent.includes(OLD_ID)); assert.equal(lastWrite().actor, OTHER.id)
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: originalClipboard }) }
})

test('explicit logout with refused storage removal still retires diagnostic and reconciliation callbacks', async () => {
  const w = await page(); await fail(w, failure())
  const oldRead = region().querySelector('[data-action="reconcile-user-balance"]'), oldCopy = region().querySelector('[data-action="copy-request-diagnostic"]'), callback = w.vm.reconcileUserBalance
  const prototype = Object.getPrototypeOf(localStorage), originalRemove = prototype.removeItem, originalClipboard = navigator.clipboard, copied = []
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async value => { copied.push(value) } } })
  prototype.removeItem = function () { throw new Error('SYNTHETIC_STORAGE_REMOVE_REFUSED') }
  try {
    user.clearStoredCurrentUser(); const before = calls.length
    oldRead.click(); oldCopy.click(); callback(); await settle()
    assert.equal(calls.length, before); assert.deepEqual(copied, []); assert.equal(region(), null); assert.ok(!document.body.textContent.includes(ID))
  } finally { prototype.removeItem = originalRemove; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: originalClipboard }) }
})

test('explicit GET missing target or invalid typed balance stays unverified and never fabricates zero or cleans the draft', async () => {
  const w = await page(), error = await fail(w), draft = clone(w.vm.balanceForm)
  for (const rows of [[{ ...ROW, id: 999 }], [{ ...ROW, balance: null }], [{ ...ROW, balance: false }], [{ ...ROW, balance: {} }], []]) {
    readRows = rows; const reads = readCount()
    region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle()
    assert.equal(readCount(), reads + 1); assert.equal(writeCount(), 1); assert.equal(w.vm.formState.balance, ROW.balance)
    assert.deepEqual(clone(w.vm.balanceForm), draft); safe(w, error, 'unknown-outcome')
    assert.match(region().querySelector('[data-user-balance-reconciliation]').textContent, /未返回.*有效余额|未.*核对/)
  }
})

test('query changed before explicit reconciliation preserves warning and uses current filters through the owned callback', async () => {
  const w = await page(), error = await fail(w, failure()), draft = clone(w.vm.balanceForm)
  const callback = w.vm.reconcileUserBalance
  w.vm.filters.keyword = 'SYNTHETIC_CURRENT_QUERY'; w.vm.pagination.pageSize = 20; await settle()
  safe(w, error); assert.deepEqual(clone(w.vm.balanceForm), draft)
  const before = readCount(); await callback(); await settle()
  assert.equal(readCount(), before + 1); assert.deepEqual(lastRead().payload, { keyword: 'SYNTHETIC_CURRENT_QUERY', page: 1, pageSize: 20 })
  assert.equal(writeCount(), 1); safe(w, error); assert.deepEqual(clone(w.vm.balanceForm), draft)
})

for (const outcome of ['success', 'failure']) test('query changed during pending reconciliation ' + outcome + ' cannot publish obsolete balance or read diagnostic and does not release issued busy early', async () => {
  const w = await page(), error = await fail(w, failure()), draft = clone(w.vm.balanceForm)
  holdReads = true; region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle()
  const oldRead = lastRead(); assert.equal(w.vm.balanceReconciling, true)
  w.vm.filters.keyword = 'SYNTHETIC_NEW_COMMITTED_QUERY'; await settle()
  assert.equal(w.vm.balanceReconciling, true, 'query edit does not cancel an already-issued GET')
  safe(w, error); assert.deepEqual(clone(w.vm.balanceForm), draft)
  const reads = readCount()
  if (outcome === 'success') oldRead.resolve(list([{ ...ROW, balance: 88.000062 }]))
  else oldRead.reject(failure(503, 'rejected', OLD_ID))
  await settle(); assert.equal(w.vm.balanceReconciling, false); assert.equal(w.vm.formState.balance, ROW.balance)
  assert.equal(readCount(), reads); assert.equal(writeCount(), 1); assert.ok(!document.body.textContent.includes(OLD_ID)); safe(w, error)
  assert.match(region().querySelector('[data-user-balance-reconciliation]').textContent, /筛选或分页已变化.*重新读取/)
  assert.deepEqual(clone(w.vm.balanceForm), draft)
  region().querySelector('[data-action="reconcile-user-balance"]').click(); await settle(); const currentRead = lastRead()
  assert.equal(currentRead.payload.keyword, 'SYNTHETIC_NEW_COMMITTED_QUERY')
  currentRead.resolve(list([{ ...ROW, balance: 9.000062 }])); await settle()
  assert.equal(w.vm.formState.balance, 9.000062); assert.equal(w.vm.balanceReconciling, false); safe(w, error); assert.deepEqual(clone(w.vm.balanceForm), draft)
})
