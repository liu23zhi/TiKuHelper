/* Native Vue/Antd write feedback qualification. Only API transports and external
 * IO are synthetic. The actual owner/currentUser, helpers, dialogs and alerts run.
 * TIKU_WEB_SOURCE/UI_TEST_HELPER deliberately support the unchanged B/M/R fixture.
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
const Antd = require('ant-design-vue'), Swal = require('sweetalert2')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const { toUserRequestError } = require(path.join(web, 'src/utils/requestRetry.mjs'))
const { buildResponsePayload, captureTransportRequestError, getTransportRequestId } = require(path.join(web, 'src/utils/requestHelpers.mjs'))
const ACTOR = { id: 76931, role: 'admin', username: 'SYNTHETIC_TOKEN_ACTOR', balance: 1.234567 }
const OTHER = { id: 76932, role: 'admin', username: 'SYNTHETIC_TOKEN_OTHER', balance: 2.345678 }
const ID = '1234567890abcdef1234567890abcdef', OLD_ID = 'abcdef1234567890abcdef1234567890'
const PRIVATE = 'SYNTHETIC_PRIVATE_WRITE_URL_CONFIG_TOKEN'
const LEGACY = '历史 名称'
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const row = (id = 301, name = LEGACY) => ({ id, name, tokenValue: 'SYNTHETIC_ONLY_TOKEN_NOT_REAL_123456', status: 'enabled', totalQuotaLimit: 0.000062, totalAmountUsed: 1.234567, remark: 'native existing remark' })
const ok = data => ({ status: 200, data })
const wrappers = [], calls = [], notices = [], network = [], runtimeErrors = []
let holdReads = false, readRows = [row()]
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_REAL_TOKEN_WRITE_IO_' + kind) }
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
  getAccessTokens: payload => {
    if (holdReads) return deferred('get', payload)
    calls.push({ name: 'get', payload: clone(payload), actor: user.getStoredCurrentUser()?.id, completed: true })
    return Promise.resolve(ok(clone(readRows)))
  },
  ...Object.fromEntries(['create', 'update', 'delete'].map(name => [name + 'AccessToken', payload => deferred(name, payload)])),
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
window.addEventListener('error', event => { runtimeErrors.push(String(event.error?.stack || event.message)) })
window.addEventListener('unhandledrejection', event => { runtimeErrors.push(String(event.reason?.stack || event.reason)) })
const Page = require(path.join(web, 'src/pages/AccessTokenManagement/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function waitFor(check, label) {
  const startWall = Date.now(), startMono = process.hrtime.bigint()
  // A wall-clock adjustment must not expire the unchanged 1000ms UI budget.
  const elapsedMs = () => Number(process.hrtime.bigint() - startMono) / 1e6
  while (elapsedMs() < 1000) {
    const value = check()
    if (value) return value
    await new Promise(resolve => setTimeout(resolve, 5)); await settle()
  }
  assert.fail(label + ' ' + JSON.stringify({
    wallElapsedMs: Date.now() - startWall, monotonicElapsedMs: elapsedMs(),
    popconfirmCount: document.querySelectorAll('.ant-popconfirm').length,
  }))
}
async function page(account = ACTOR) {
  user.setStoredCurrentUser(account)
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  wrappers.push(w); await settle(); return w
}
const readCount = () => calls.filter(call => call.name === 'get').length
const writeCount = () => calls.filter(call => call.name !== 'get').length
const writes = operation => calls.filter(call => call.name === operation)
const compact = text => String(text).replace(/\s/g, '')
function button(root, label) {
  const node = [...root.querySelectorAll('button')].find(item => compact(item.textContent) === compact(label))
  assert.ok(node, 'ACTUAL_NATIVE_BUTTON_' + label); return node
}
function modal() {
  const node = [...document.querySelectorAll('.ant-modal')].find(item => item.closest('.ant-modal-wrap')?.style.display !== 'none')
  assert.ok(node, 'ACTUAL_OPEN_ANTD_MODAL'); return node
}
function typeNow(root, selector, value) {
  const node = root.querySelector(selector); assert.ok(node, 'ACTUAL_NATIVE_INPUT_' + selector)
  node.value = value; node.dispatchEvent(new Event('input', { bubbles: true })); node.dispatchEvent(new Event('change', { bubbles: true }))
}
async function type(root, selector, value) { typeNow(root, selector, value); await settle() }
async function prepare(w, operation, name = 'native_created') {
  if (operation === 'delete') return
  button(w.element, operation === 'create' ? '新增令牌' : '编辑').click(); await settle()
  const dialog = modal()
  if (operation === 'create') await type(dialog, 'input[placeholder="请输入令牌名称"]', '  ' + name + '  ')
  await type(dialog, 'input[placeholder="0 表示不单独限制"]', '0.000062')
  await type(dialog, 'textarea[placeholder="可选，例如调用来源、用途说明等"]', '  native synthetic remark  ')
}
async function issue(w, operation) {
  const before = writes(operation).length
  if (operation === 'delete') {
    button(w.element, '删除').click(); await settle()
    assert.equal(writes(operation).length, before, 'native delete trigger must wait for confirmation')
    const pop = await waitFor(() => [...document.querySelectorAll('.ant-popconfirm')].find(node => node.textContent.includes('确定删除该令牌吗？')), 'ACTUAL_ANTD_DELETE_CONFIRMATION')
    button(pop, '确定').click()
  } else button(modal().querySelector('.ant-modal-footer'), '确定').click()
  await settle(); assert.equal(writes(operation).length, before + 1, 'one native confirmation issues one synthetic write')
  return writes(operation).at(-1)
}
async function cancel(w) {
  if (w.vm.visible) { button(modal().querySelector('.ant-modal-footer'), '取消').click(); await settle() }
  Antd.message.destroy(); await settle()
}
function lane(operation) { return operation === 'delete' ? 'delete' : 'editor' }
function region(operation) { return document.querySelector('[data-token-write-feedback="' + lane(operation) + '"]') }
function alertFor(w, failure) { return w.findAllComponents({ name: 'RequestFailureAlert' }).find(alert => alert.props('failure') === failure) }
function onlyWriteAlert(w, operation) {
  const key = operation === 'delete' ? 'deleteFailure' : 'editorFailure'
  const alert = alertFor(w, w.vm[key]); assert.ok(alert, 'actual durable write alert'); return alert
}
function fixedSafe(w, operation, original, expectedKind) {
  const alert = onlyWriteAlert(w, operation)
  if (original !== undefined && original !== null && ['object', 'function'].includes(typeof original)) assert.equal(alert.props('failure'), original, 'keep the exact transport WeakMap key, never a proxy or clone')
  assert.equal(alert.props('retryable'), false, 'write feedback must not expose a generic resubmit/retry')
  assert.equal(region(operation).querySelector('[data-action="retry-request"]'), null)
  if (expectedKind) assert.equal(region(operation).querySelector('.request-failure').getAttribute('data-failure-kind'), expectedKind)
  assert.ok(!document.body.textContent.includes(PRIVATE), 'raw upstream error/body/config/URL/token cannot render')
  assert.ok(!notices.some(notice => notice.text.includes(PRIVATE)), 'raw upstream message cannot escape as a transient notice')
  assert.ok(!notices.some(notice => notice.kind === 'success'), 'unconfirmed writes cannot claim success')
  return alert
}
function unknown(operation) {
  const original = Object.assign(new Error('Network Error'), { code: 'ERR_NETWORK', config: { url: PRIVATE }, privatePayload: PRIVATE })
  const error = toUserRequestError({ method: operation === 'delete' ? 'delete' : operation === 'update' ? 'put' : 'post', url: '/adapter-service/access-tokens' }, original)
  assert.equal(error.code, 'UNKNOWN_OUTCOME', 'actual write helper creates uncertain outcome')
  assert.equal(error.cause, original)
  return error
}
function httpFailure(status, mode, id = ID) {
  const response = { status, headers: { 'x-request-id': id }, data: { message: PRIVATE, detail: PRIVATE, config: { token: PRIVATE, url: PRIVATE } } }
  return mode === 'fulfilled' ? buildResponsePayload(response) : captureTransportRequestError(Object.assign(new Error(PRIVATE), { response, config: { url: PRIVATE } }))
}
function retire(w, operation, retirement) {
  if (retirement === 'draft') typeNow(modal(), 'textarea', 'NEW_DRAFT_AFTER_FAILURE')
  else if (retirement === 'cancel-reopen') { button(modal().querySelector('.ant-modal-footer'), '取消').click(); button(w.element, '新增令牌').click() }
  else if (retirement === 'account') user.setStoredCurrentUser(OTHER)
  else if (retirement === 'role') user.setStoredCurrentUser({ ...ACTOR, role: 'user' })
  else if (retirement === 'logout') user.clearStoredCurrentUser()
  else if (retirement === 'view') button(w.element, '显示全部令牌').click()
  else if (retirement === 'dismiss') region(operation).querySelector('[data-action="dismiss-token-delete-failure"]').click()
  else w.unmount()
}
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  for (const call of calls) if (!call.completed && call.resolve) call.resolve(ok(call.name === 'get' ? [] : {}))
  Swal.close(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
  user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('TOKEN_WRITE_NATIVE_LEDGER ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', calls: calls.map(({ name, payload, actor }) => ({ name, payload, actor })), notices, network, runtimeErrors }))
  const observedNetwork = network.splice(0), observedErrors = runtimeErrors.splice(0)
  wrappers.length = calls.length = notices.length = 0; holdReads = false; readRows = [row()]
  assert.deepEqual(observedNetwork, [], 'no real network/provider/database/token IO')
  assert.deepEqual(observedErrors, [], 'native runtime exceptions must fail, never silently pass')
})

test('native ordinary-user create/update/delete preserve six places, whitelist, unchanged legacy name, status and confirmation', async () => {
  const w = await page({ ...ACTOR, role: 'user' })
  await prepare(w, 'create')
  w.vm.formState.id = 999; w.vm.formState.status = 'disabled'; w.vm.formState.totalAmountUsed = 999; w.vm.formState.tokenValue = PRIVATE
  const created = await issue(w, 'create')
  assert.deepEqual(created.payload, { name: 'native_created', totalQuotaLimit: 0.000062, remark: 'native synthetic remark' })
  created.resolve(ok({ token: 'SYNTHETIC_CREATED_TOKEN_NATIVE' })); await settle()
  assert.equal(w.vm.visible, false); assert.equal(w.vm.submitLoading, false); assert.equal(readCount(), 2)
  const popup = Swal.getPopup(); assert.ok(popup, 'actual created-token dialog'); assert.equal(popup.querySelector('[data-token-value]').textContent, 'SYNTHETIC_CREATED_TOKEN_NATIVE')
  button(popup, '我已保存').click(); await settle()
  await prepare(w, 'update')
  const select = modal().querySelector('.ant-select-selector'); assert.ok(select); select.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })); await settle()
  const disabledOption = document.querySelector('.ant-select-item-option[title="禁用"]'); assert.ok(disabledOption, 'actual native Antd status option'); disabledOption.click(); await settle()
  w.vm.formState.totalAmountUsed = 999; w.vm.formState.tokenValue = PRIVATE
  const updated = await issue(w, 'update')
  assert.deepEqual(updated.payload, { id: 301, name: LEGACY, status: 'disabled', totalQuotaLimit: 0.000062, remark: 'native synthetic remark' })
  updated.resolve(ok({})); await settle(); assert.equal(w.vm.visible, false); assert.equal(readCount(), 3)
  const removed = await issue(w, 'delete'); assert.deepEqual(removed.payload, { id: 301 })
  removed.resolve(ok({})); await settle(); assert.equal(readCount(), 4); assert.equal(writeCount(), 3); assert.equal(w.vm.showAllTokens, false)
  assert.deepEqual(notices.filter(n => n.kind === 'success').map(n => n.text), ['创建令牌成功', '更新令牌成功', '删除令牌成功'])
})

for (const operation of ['create', 'update', 'delete']) test(operation + ': actual UNKNOWN remains durable; explicit same-name reconciliation reads only and never cleans draft or proves success', async () => {
  const w = await page(); await prepare(w, operation, 'native_uncertain')
  const call = await issue(w, operation), error = unknown(operation), beforeDraft = clone(w.vm.formState)
  call.reject(error); await settle()
  const alert = fixedSafe(w, operation, error, 'unknown-outcome')
  assert.match(alert.text(), /结果.*(?:尚未确认|未确认)/); assert.match(alert.text(), /勿重复提交/)
  assert.ok(!notices.some(n => /(?:创建|更新|删除)令牌失败/.test(n.text)), 'UNKNOWN is not a definite failed write')
  assert.equal(getTransportRequestId(error), undefined, 'network uncertainty does not invent diagnostic identity')
  assert.equal(region(operation).querySelector('[data-action="copy-request-diagnostic"]'), null)
  const before = { reads: readCount(), writes: writeCount(), calls: calls.length }
  await settle(); assert.equal(calls.length, before.calls, 'idle feedback performs no implicit retry')
  readRows = [row(999, operation === 'create' ? 'native_uncertain' : LEGACY)]
  button(region(operation), '刷新令牌核对').click(); await settle()
  assert.equal(readCount(), before.reads + 1); assert.equal(writeCount(), before.writes)
  fixedSafe(w, operation, error, 'unknown-outcome')
  assert.equal(w.vm.dataSource[0].id, 999)
  if (operation !== 'delete') { assert.equal(w.vm.visible, true); assert.deepEqual(clone(w.vm.formState), beforeDraft, 'readback must not clean or replace unconfirmed draft') }
  Antd.message.destroy(); await settle(); assert.ok(region(operation).textContent.includes('尚未确认'), 'durable feedback outlives transient notices')
})

for (const operation of ['create', 'update', 'delete']) for (const status of [401, 403, 429, 503]) test(operation + ': HTTP ' + status + ' fulfilled and rejected preserve original diagnostic key with fixed public feedback', async () => {
  const w = await page()
  for (const mode of ['fulfilled', 'rejected']) {
    await prepare(w, operation); const call = await issue(w, operation), original = httpFailure(status, mode)
    if (mode === 'fulfilled') call.resolve(original); else call.reject(original)
    await settle()
    const kind = { 401: 'unauthorized', 403: 'forbidden', 429: 'rate-limit', 503: 'upstream' }[status]
    const alert = fixedSafe(w, operation, original, kind)
    assert.equal(getTransportRequestId(original), ID); assert.ok(alert.text().includes(ID))
    assert.equal(readCount(), 1, 'failed write never auto-refreshes or auto-resubmits')
    if (operation !== 'delete') await cancel(w)
    else { region(operation).querySelector('[data-action="dismiss-token-delete-failure"]').click(); await settle() }
  }
  assert.equal(writeCount(), 2)
})

for (const operation of ['create', 'update', 'delete']) test(operation + ': fulfilled null and primitive replies show safe persistent malformed feedback, never raw values', async () => {
  const w = await page()
  for (const value of [null, undefined, false, 0, '', PRIVATE]) {
    await prepare(w, operation); const call = await issue(w, operation); call.resolve(value); await settle()
    const alert = fixedSafe(w, operation, value)
    assert.equal(alert.props('failure').code, 'MALFORMED_RESPONSE')
    assert.equal(region(operation).querySelector('[data-action="copy-request-diagnostic"]'), null)
    if (operation !== 'delete') await cancel(w)
    else { region(operation).querySelector('[data-action="dismiss-token-delete-failure"]').click(); await settle() }
  }
  assert.equal(writeCount(), 6); assert.equal(readCount(), 1)
})

for (const operation of ['create', 'update']) test(operation + ': draft typed during pending write keeps original busy lane and suppresses obsolete late diagnostic', async () => {
  const w = await page(); await prepare(w, operation); const call = await issue(w, operation)
  await type(modal(), 'textarea', 'TYPED_AFTER_ISSUED')
  assert.equal(w.vm.submitLoading, true, 'draft edit cannot pretend to cancel issued write')
  call.reject(httpFailure(503, 'rejected', OLD_ID)); await settle()
  assert.equal(w.vm.submitLoading, false); assert.equal(w.vm.visible, true); assert.equal(w.vm.formState.remark, 'TYPED_AFTER_ISSUED')
  assert.equal(region(operation), null); assert.ok(!document.body.textContent.includes(OLD_ID)); assert.equal(writeCount(), 1); assert.equal(readCount(), 1)
})

for (const operation of ['create', 'update', 'delete']) for (const outcome of ['success', 'failure']) test(operation + ': late old ' + outcome + ' cannot overwrite the newer native attempt diagnostic', async () => {
  const w = await page(); await prepare(w, operation); const old = await issue(w, operation)
  if (operation !== 'delete') { await cancel(w); await prepare(w, operation, 'new_editor') }
  else {
    // A native pending Popconfirm correctly blocks a second confirmation. Retire
    // its real view before confirming a new action; never bypass that busy guard.
    button(w.element, '显示全部令牌').click(); await settle()
  }
  const current = await issue(w, operation), failure = httpFailure(503, 'rejected')
  current.reject(failure); await settle(); fixedSafe(w, operation, failure, 'upstream')
  const before = calls.length
  if (outcome === 'success') old.resolve(ok({ token: 'SYNTHETIC_RETIRED_CREATED_TOKEN' }))
  else old.reject(httpFailure(429, 'rejected', OLD_ID))
  await settle(); fixedSafe(w, operation, failure, 'upstream')
  assert.equal(calls.length, before, 'late write cannot trigger refresh'); assert.ok(!document.body.textContent.includes(OLD_ID)); assert.equal(Swal.getPopup(), null)
})

for (const operation of ['create', 'delete']) for (const retirement of ['account', 'role', 'logout', 'view', 'unmount', ...(operation === 'create' ? ['draft', 'cancel-reopen'] : ['dismiss'])]) test(operation + ': ' + retirement + ' retires stale native diagnostic copy and read-only reconciliation callbacks', async () => {
  const w = await page(); await prepare(w, operation); const call = await issue(w, operation), error = httpFailure(503, 'rejected')
  call.reject(error); await settle(); fixedSafe(w, operation, error, 'upstream')
  const oldRegion = region(operation), oldRead = oldRegion.querySelector('[data-action="reconcile-token-write"]'), oldCopy = oldRegion.querySelector('[data-action="copy-request-diagnostic"]')
  const oldCallback = operation === 'delete' ? w.vm.reconcileDeletedToken : w.vm.reconcileEditorToken
  const oldClipboard = navigator.clipboard, copied = []
  let finishCopy
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: value => { copied.push(value); return new Promise(resolve => { finishCopy = resolve }) } } })
  try {
    oldCopy.click(); await settle(); assert.deepEqual(copied, [ID])
    retire(w, operation, retirement)
    const readsBefore = readCount(), writesBefore = writeCount()
    oldRead.click(); oldCopy.click(); oldCallback()
    assert.equal(readCount(), readsBefore, 'same-tick retired native/captured callback cannot issue GET')
    assert.equal(writeCount(), writesBefore); assert.deepEqual(copied, [ID], 'same-tick stale copy cannot touch new owner clipboard')
    await settle(); const afterRetire = readCount()
    oldRead.click(); oldCopy.click(); oldCallback(); finishCopy(); await settle()
    assert.equal(readCount(), afterRetire); assert.equal(writeCount(), writesBefore); assert.deepEqual(copied, [ID])
    assert.ok(!document.body.textContent.includes(ID)); assert.ok(!document.body.textContent.includes('诊断编号已复制'))
    if (retirement === 'cancel-reopen') { assert.equal(w.vm.visible, true); assert.equal(w.vm.formState.name, '') }
    if (retirement === 'draft') assert.equal(w.vm.formState.remark, 'NEW_DRAFT_AFTER_FAILURE')
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: oldClipboard }) }
})

for (const operation of ['create', 'update', 'delete']) for (const outcome of ['success', 'failure']) test(operation + ': retired reconciliation ' + outcome + ' cannot publish rows, diagnostics or clear a newer GET busy state', async () => {
  const w = await page(); await prepare(w, operation); const call = await issue(w, operation), error = unknown(operation)
  call.reject(error); await settle(); fixedSafe(w, operation, error, 'unknown-outcome')
  holdReads = true; button(region(operation), '刷新令牌核对').click(); await settle(); const old = calls.at(-1); assert.equal(old.name, 'get')
  retire(w, operation, operation === 'delete' ? 'dismiss' : 'draft'); await settle()
  button(w.element, '显示全部令牌').click(); await settle(); const current = calls.at(-1); assert.notEqual(current, old)
  if (outcome === 'success') old.resolve(ok([row(999, 'RETIRED_READ_ONLY')]))
  else old.reject(httpFailure(503, 'rejected', OLD_ID))
  await settle()
  assert.equal(w.vm.tableLoading, true); assert.equal(w.vm.listFailure, null); assert.ok(!document.body.textContent.includes(OLD_ID)); assert.equal(writeCount(), 1)
  current.resolve(ok([row(930, 'CURRENT_READ_ONLY')])); await settle()
  assert.equal(w.vm.tableLoading, false); assert.equal(w.vm.dataSource[0].id, 930); assert.equal(region(operation), null)
})

for (const operation of ['create', 'update', 'delete']) test(operation + ': exact HTTP/business pairs retain actionable fixed reasons while UNKNOWN and wrong statuses stay generic', async () => {
  const w = await page()
  const cases = [
    [400, '令牌名称不能为空', '令牌名称不能为空，请填写后再提交。'],
    [400, '令牌名称只能使用中文、英文、数字、下划线和@，且长度不能超过128个字符', '令牌名称仅支持中文、英文、数字、下划线和@，且不能超过128个字符，请检查后再提交。'],
    [404, '访问令牌不存在', '访问令牌不存在，请刷新列表核对。'],
    [403, '无权限操作访问令牌', '当前账号无权操作该访问令牌，请确认账号权限。'],
  ]
  for (const [status, message, expected] of cases) for (const mode of ['fulfilled', 'rejected']) {
    await prepare(w, operation); const call = await issue(w, operation)
    const response = { status, headers: { 'x-request-id': ID }, data: { message, detail: PRIVATE } }
    const failure = mode === 'fulfilled' ? buildResponsePayload(response) : captureTransportRequestError(Object.assign(new Error(PRIVATE), { response }))
    if (mode === 'fulfilled') call.resolve(failure); else call.reject(failure)
    await settle(); const alert = fixedSafe(w, operation, failure); assert.ok(alert.text().includes(expected), 'only fixed allowlisted HTTP/business pair becomes public reason')
    assert.equal(getTransportRequestId(failure), ID)
    if (operation !== 'delete') await cancel(w)
    else { region(operation).querySelector('[data-action="dismiss-token-delete-failure"]').click(); await settle() }
  }
  for (const kind of ['wrong-status', 'unknown']) {
    await prepare(w, operation); const call = await issue(w, operation)
    const failure = kind === 'unknown' ? unknown(operation) : buildResponsePayload({ status: 503, data: { message: '令牌名称不能为空' } })
    if (kind === 'unknown') failure.data = { message: '令牌名称不能为空' }
    call.reject(failure); await settle(); const alert = fixedSafe(w, operation, failure)
    assert.ok(!alert.text().includes('令牌名称不能为空，请填写后再提交。'))
    if (kind === 'unknown') assert.equal(region(operation).querySelector('.request-failure').getAttribute('data-failure-kind'), 'unknown-outcome')
    if (operation !== 'delete') await cancel(w)
    else { region(operation).querySelector('[data-action="dismiss-token-delete-failure"]').click(); await settle() }
  }
})

for (const operation of ['create', 'delete']) test(operation + ': explicit logout with refused storage removal retires new feedback callbacks even if display cache stays A', async () => {
  const w = await page(); await prepare(w, operation); const call = await issue(w, operation), error = httpFailure(503, 'rejected')
  call.reject(error); await settle(); fixedSafe(w, operation, error)
  const oldRead = region(operation).querySelector('[data-action="reconcile-token-write"]'), oldCopy = region(operation).querySelector('[data-action="copy-request-diagnostic"]')
  const callback = operation === 'delete' ? w.vm.reconcileDeletedToken : w.vm.reconcileEditorToken
  const storagePrototype = Object.getPrototypeOf(localStorage), remove = storagePrototype.removeItem
  const oldClipboard = navigator.clipboard, copied = []
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async value => { copied.push(value) } } })
  storagePrototype.removeItem = function () { throw new Error('SYNTHETIC_STORAGE_REMOVE_REFUSED') }
  try {
    user.clearStoredCurrentUser()
    const readsBefore = readCount(), writesBefore = writeCount()
    oldRead.click(); oldCopy.click(); callback(); await settle()
    assert.equal(readCount(), readsBefore); assert.equal(writeCount(), writesBefore); assert.deepEqual(copied, [])
    assert.equal(region(operation), null); assert.ok(!document.body.textContent.includes(ID))
  } finally { storagePrototype.removeItem = remove; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: oldClipboard }) }
})


test('TOKEN_WRITE_REVIEW_52 same native mount restores new-login feedback after explicit logout without reviving old actor callbacks', async () => {
  const w = await page()
  await prepare(w, 'create', 'actor_a_first')
  const firstA = await issue(w, 'create'), oldFailure = httpFailure(503, 'rejected', OLD_ID)
  firstA.reject(oldFailure); await settle(); fixedSafe(w, 'create', oldFailure, 'upstream')
  const oldRead = region('create').querySelector('[data-action="reconcile-token-write"]')
  const oldCopy = region('create').querySelector('[data-action="copy-request-diagnostic"]')
  const oldReconcile = w.vm.reconcileEditorToken
  // This is an explicit native retry while A is current, not an automatic resend.
  // Keep its issued result pending across logout so it cannot paint the later B.
  const pendingA = await issue(w, 'create')
  const oldClipboard = navigator.clipboard, copied = []
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async value => { copied.push(value) } } })
  try {
    user.clearStoredCurrentUser(); await settle()
    assert.equal(region('create'), null)
    const beforeLoginReads = readCount()
    user.setStoredCurrentUser(OTHER); await settle()
    assert.equal(user.getStoredCurrentUser().id, OTHER.id)
    assert.equal(w.vm.canLoadForCurrentUser(), true, 'actual request scope restores the newly logged-in actor')
    assert.equal(readCount(), beforeLoginReads + 1, 'same mounted page performs its normal new-login GET')
    assert.equal(w.vm.tableLoading, false)
    assert.equal(wrappers.length, 1); assert.equal(wrappers[0], w, 'do not remount to bypass login restoration')
    const beforeStale = { reads: readCount(), writes: writeCount() }
    oldRead.click(); oldCopy.click(); oldReconcile(); await settle()
    assert.deepEqual({ reads: readCount(), writes: writeCount() }, beforeStale); assert.deepEqual(copied, [])

    await prepare(w, 'create', 'actor_b_current')
    const currentB = await issue(w, 'create'), currentFailure = unknown('create')
    assert.equal(currentB.actor, OTHER.id)
    currentB.reject(currentFailure); await settle()
    assert.ok(region('create'), 'NEW_LOGIN_WRITE_FEEDBACK_RESTORED_WITHOUT_REMOUNT')
    fixedSafe(w, 'create', currentFailure, 'unknown-outcome')
    const currentDraft = clone(w.vm.formState)
    pendingA.reject(httpFailure(503, 'rejected', OLD_ID)); await settle()
    fixedSafe(w, 'create', currentFailure, 'unknown-outcome')
    assert.ok(!document.body.textContent.includes(OLD_ID), 'late A failure cannot overwrite new B feedback')
    const beforeReconcile = { reads: readCount(), writes: writeCount() }
    oldRead.click(); oldCopy.click(); oldReconcile(); await settle()
    assert.deepEqual({ reads: readCount(), writes: writeCount() }, beforeReconcile); assert.deepEqual(copied, [])
    button(region('create'), '刷新令牌核对').click(); await settle()
    assert.equal(readCount(), beforeReconcile.reads + 1)
    assert.equal(writeCount(), beforeReconcile.writes, 'B reconciliation is GET only')
    assert.equal(calls.at(-1).actor, OTHER.id)
    fixedSafe(w, 'create', currentFailure, 'unknown-outcome')
    assert.equal(w.vm.visible, true); assert.deepEqual(clone(w.vm.formState), currentDraft)
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: oldClipboard }) }
})
