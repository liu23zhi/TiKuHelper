/* Actual Vue/Antd/BillingDetailDialog/currentUser/owners/request helpers. Only
 * external API boundaries are synthetic; no provider, server or database IO.
 * This identical fixture supports root-owned B/M/R via the two environment paths.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(web, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
for (const key of ['DOMParser', 'HTMLInputElement', 'HTMLButtonElement', 'HTMLDivElement', 'CustomEvent']) globalThis[key] = window[key]
window.scrollTo = () => {}
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const { toUserRequestError } = require(path.join(web, 'src/utils/requestRetry.mjs'))
const { buildResponsePayload, captureTransportRequestError, getTransportRequestId } = require(path.join(web, 'src/utils/requestHelpers.mjs'))
const ADMIN = { id: 78901, role: 'admin', username: 'SYNTHETIC_VISIBILITY_ADMIN', balance: 3.234567 }
const OTHER = { id: 78903, role: 'admin', username: 'SYNTHETIC_VISIBILITY_OTHER', balance: 2.345678 }
const CONSUMER = { id: 78902, role: 'user', username: 'SYNTHETIC_BILLING_OWNER', balance: 1.358023 }
const ROW = { id: 8091, userId: 78902, username: 'SYNTHETIC_BILLING_OWNER', subject: 'SYNTHETIC_BILLING_VISIBILITY_TARGET', type: 'balance_adjustment', direction: 'income', amount: 0.123456, showInStatement: true, balanceBefore: 1.234567, balanceAfter: 1.358023, adjustmentMode: 'increase', operatorId: 78901, operatorUsername: 'SYNTHETIC_VISIBILITY_ADMIN', clientIp: '', createdAt: '2026-10-29T00:00:00Z', feeDetails: [{ provider: 'ai', category: 'AI模型调用', callIndex: 1, amount: 0.123456, detail: 'model=SYNTHETIC_MODEL;finalMultiplier=1.25;totalTokens=62' }] }
const SECOND = { ...ROW, id: 8092, subject: 'SYNTHETIC_SECOND_BILLING_RECORD' }
const PRIVATE = 'SYNTHETIC_PRIVATE_BILLING_RESPONSE_URL_CONFIG'
const ID = '1234567890abcdef1234567890abcdef', OLD_ID = 'abcdef1234567890abcdef1234567890'
const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const ok = data => ({ status: 200, data })
const list = (rows = [ROW, SECOND]) => ok({ data: clone(rows), total: rows.length, page: 1, pageSize: 10, summary: { totalAmount: 0.123456, currentPageAmount: 0.123456, currentPageCacheHitRate: 0.1, totalCacheHitRate: 0.46 } })
const wrappers = [], calls = [], notices = [], network = [], runtimeErrors = []
let holdReads = false, readRows = [ROW, SECOND]
function deny(kind) { network.push(kind); throw new Error('FORBIDDEN_REAL_BILLING_NATIVE_IO_' + kind) }
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
  getBillingRecords: payload => {
    if (holdReads) return deferred('get', payload)
    calls.push({ name: 'get', payload: clone(payload), actor: user.getStoredCurrentUser()?.id, completed: true })
    return Promise.resolve(list(readRows))
  },
  updateBillingStatementVisibility: (id, showInStatement) => deferred('visibility', { id, showInStatement }),
  getCurrentUser: () => Promise.resolve(ok(user.getStoredCurrentUser())),
  getPaymentMethods: () => Promise.resolve(ok([])),
  getIpLocations: () => Promise.resolve(ok({})), getMyLocations: () => Promise.resolve(ok({})),
  createRechargeOrder: () => deny('UNEXPECTED_RECHARGE'),
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
const Page = require(path.join(web, 'src/pages/BillingRecordList/index.vue'))
const Detail = require(path.join(web, 'src/components/BillingDetailDialog.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function until(check, label) {
  const start = process.hrtime.bigint()
  while (Number(process.hrtime.bigint() - start) / 1e6 < 1000) {
    const value = check(); if (value) return value
    await new Promise(resolve => setTimeout(resolve, 5)); await settle()
  }
  assert.fail(label + ' monotonicElapsedMs=' + Number(process.hrtime.bigint() - start) / 1e6)
}
async function page(account = ADMIN, component = Page) {
  user.setStoredCurrentUser(account)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/billing', component: { render: () => null } }, { path: '/elsewhere', component: { render: () => null } }] })
  await router.push('/billing'); await router.isReady()
  const w = mount(component, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  w.fixtureRouter = router; wrappers.push(w); await settle(); return w
}
const compact = text => String(text).replace(/\s/g, '')
function button(root, label) {
  const node = [...root.querySelectorAll('button')].find(item => compact(item.textContent) === compact(label))
  assert.ok(node, 'ACTUAL_NATIVE_BUTTON_' + label); return node
}
function modal() {
  const node = [...document.querySelectorAll('.ant-modal')].find(item => item.querySelector('.ant-modal-title')?.textContent === '账单详情' && item.closest('.ant-modal-wrap')?.style.display !== 'none')
  assert.ok(node, 'ACTUAL_BILLING_DETAIL_MODAL'); return node
}
function switchNode() {
  const node = modal().querySelector('button[role="switch"][aria-label="显示在用户账单流水"]')
  assert.ok(node, 'ACTUAL_ADMIN_ANT_SWITCH'); return node
}
async function open(w, id = ROW.id) {
  const row = [...w.element.querySelectorAll('.ant-table-tbody tr')].find(node => node.getAttribute('data-row-key') === String(id))
  assert.ok(row, 'ACTUAL_NATIVE_BILLING_ROW_' + id)
  button(row, '查看详情').click(); await settle()
  await until(() => w.vm.billingDetailOpen && document.querySelector('.billing-detail'), 'native detail opens')
  assert.equal(w.findComponent({ name: 'BillingDetailDialog' }).props('record').id, id)
}
const readCount = () => calls.filter(call => call.name === 'get').length
const writeCount = () => calls.filter(call => call.name === 'visibility').length
const lastRead = () => calls.filter(call => call.name === 'get').at(-1)
const lastWrite = () => calls.filter(call => call.name === 'visibility').at(-1)
const region = () => document.querySelector('[data-billing-statement-feedback]')
const reconcileNode = () => { const node = region()?.querySelector('[data-action="reconcile-billing-statement"]'); assert.ok(node, 'ACTUAL_READ_ONLY_RECONCILIATION'); return node }
function readBusy(w, expected) {
  assert.equal(w.vm.statementReconciling, expected, 'issued reconciliation busy state')
  assert.equal(reconcileNode().classList.contains('ant-btn-loading'), expected, 'actual Antd loading state; loading is not a fabricated disabled attribute')
}
async function issue(w, show = !w.vm.selectedBillingRecord.showInStatement) {
  const before = writeCount(), node = switchNode()
  await until(() => !node.disabled, 'native statement switch becomes ready')
  assert.equal(node.getAttribute('aria-checked'), String(!show), 'issue uses the actual opposite-state native switch, not synthetic change')
  node.click(); await settle()
  assert.equal(writeCount(), before + 1, 'one native switch activation means one logical write')
  assert.deepEqual(lastWrite().payload, { id: w.vm.selectedBillingRecord.id, showInStatement: show })
  assert.equal(typeof lastWrite().payload.showInStatement, 'boolean'); return lastWrite()
}
function unknown() {
  const raw = Object.assign(new Error('Network Error'), { code: 'ERR_NETWORK', config: { url: PRIVATE }, privatePayload: PRIVATE })
  const error = toUserRequestError({ method: 'put', url: '/adapter-service/billing-records/8091/statement-visibility' }, raw)
  assert.equal(error.code, 'UNKNOWN_OUTCOME'); return error
}
function failure(status = 503, mode = 'rejected', id = ID) {
  const response = { status, headers: id ? { 'x-request-id': id } : {}, data: { message: PRIVATE, detail: PRIVATE, config: { token: PRIVATE, url: PRIVATE } } }
  return mode === 'fulfilled' ? buildResponsePayload(response) : captureTransportRequestError(Object.assign(new Error(PRIVATE), { response, config: { url: PRIVATE } }))
}
function alert(w, original) {
  const value = w.findAllComponents({ name: 'RequestFailureAlert' }).find(item => item.props('failure') === original)
  assert.ok(value, 'ACTUAL_DURABLE_ORIGINAL_BILLING_FAILURE'); return value
}
function safe(w, original, kind) {
  assert.ok(region(), 'durable billing statement feedback exists')
  const value = original !== null && ['object', 'function'].includes(typeof original)
    ? alert(w, original) : w.findAllComponents({ name: 'RequestFailureAlert' }).find(item => item.props('failure')?.code === 'MALFORMED_RESPONSE')
  assert.ok(value, 'safe primitive failure projection')
  assert.equal(value.props('retryable'), false, 'financial write never offers generic mutation retry')
  assert.equal(region().querySelector('[data-action="retry-request"]'), null)
  if (kind) assert.equal(region().querySelector('.request-failure').getAttribute('data-failure-kind'), kind)
  assert.ok(!document.body.textContent.includes(PRIVATE), 'raw response/error/config/private URL never renders')
  assert.ok(!notices.some(item => item.text.includes(PRIVATE)), 'raw error never escapes through toast')
  return value
}
async function fail(w, original = unknown()) {
  if (!w.vm.billingDetailOpen) await open(w)
  const call = await issue(w); call.reject(original); await settle(); safe(w, original); return original
}
function preserve(w, before = ROW) {
  const current = w.vm.selectedBillingRecord
  for (const key of ['id', 'amount', 'subject', 'balanceBefore', 'balanceAfter', 'operatorId', 'operatorUsername', 'adjustmentMode']) assert.equal(current[key], before[key], 'preserve original ' + key)
  assert.deepEqual(clone(current.feeDetails), before.feeDetails)
  assert.ok(modal().textContent.includes('0.123456'), 'amount retains six places')
  assert.ok(modal().textContent.includes('SYNTHETIC_MODEL'), 'administrator metadata retained')
}
function retire(w, reason) {
  if (reason === 'record') w.vm.handleShowFeeDetail(w.vm.dataSource.find(row => row.id === SECOND.id))
  else if (reason === 'dialog') button(modal().querySelector('.ant-modal-footer'), '关闭').click()
  else if (reason === 'entry') w.vm.handleShowFeeDetail(w.vm.dataSource.find(row => row.id === ROW.id))
  else if (reason === 'actor') user.setStoredCurrentUser(OTHER)
  else if (reason === 'role') user.setStoredCurrentUser({ ...ADMIN, role: 'user' })
  else if (reason === 'route') return w.fixtureRouter.push('/elsewhere')
  else if (reason === 'logout') user.clearStoredCurrentUser()
  else w.unmount()
}
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) {
    // The published billing page deliberately retains its module-level query
    // refs across mounts. Isolate fixture cases without pretending an unchanged
    // keyword assignment is a fingerprint transition in the next case.
    if (w.vm.$options.name === 'BillingRecordList') {
      w.vm.filters.keyword = ''; w.vm.filters.type = undefined; w.vm.keywordInput = ''
      w.vm.pagination.current = 1; w.vm.pagination.pageSize = 10
    }
    w.unmount()
  }
  for (const call of calls) if (!call.completed && call.resolve) call.resolve(call.name === 'get' ? list() : ok({}))
  Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
  user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); document.body.replaceChildren()
  console.log('BILLING_VISIBILITY_NATIVE_LEDGER ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', calls: calls.map(({ name, payload, actor }) => ({ name, payload, actor })), notices, network, runtimeErrors }))
  const io = network.splice(0), errors = runtimeErrors.splice(0)
  wrappers.length = calls.length = notices.length = 0; holdReads = false; readRows = [ROW, SECOND]
  assert.deepEqual(io, [], 'no real network/provider/database IO')
  assert.deepEqual(errors, [], 'native runtime errors must fail, never be swallowed')
})

test('native administrator false/true strict-bool writes preserve original 200 empty acknowledgement, six-place amount and administrator detail', async () => {
  const w = await page(); await open(w)
  for (const show of [false, true]) {
    const call = await issue(w, show); assert.deepEqual(Object.keys(call.payload), ['id', 'showInStatement'])
    const reads = readCount(); call.resolve(ok({})); await settle()
    assert.equal(w.vm.selectedBillingRecord.showInStatement, show); assert.equal(w.vm.dataSource[0].showInStatement, show)
    assert.equal(switchNode().getAttribute('aria-checked'), String(show)); assert.equal(switchNode().disabled, false)
    assert.equal(w.vm.visibilitySaving[ROW.id], false); assert.equal(readCount(), reads); assert.equal(region(), null); preserve(w)
  }
  assert.deepEqual(notices.filter(item => item.kind === 'success').map(item => item.text), ['已从用户账单流水中隐藏，管理员明细仍保留', '已在用户账单流水中显示'])
})

test('native pending same-record switch is really disabled and busy until the issued response settles', async () => {
  const w = await page(); await open(w); const call = await issue(w)
  assert.equal(switchNode().disabled, true); assert.equal(w.vm.visibilitySaving[ROW.id], true)
  await w.vm.handleStatementVisibilityChange({ id: ROW.id, showInStatement: false }); await settle()
  assert.equal(writeCount(), 1, 'existing busy gate, not an unreachable second confirmation')
  call.reject(failure()); await settle(); assert.equal(switchNode().disabled, false); assert.equal(w.vm.visibilitySaving[ROW.id], false)
})

test('ordinary user cannot issue statement writes and actual default consumer child never reveals administrator control or failure', async () => {
  const w = await page(CONSUMER); await open(w)
  assert.equal(modal().querySelector('[role="switch"]'), null)
  assert.equal(await w.vm.handleStatementVisibilityChange({ id: ROW.id, showInStatement: false }), false)
  assert.equal(writeCount(), 0); assert.equal(region(), null); assert.ok(!modal().textContent.includes('SYNTHETIC_MODEL'))
  w.unmount(); await settle()
  const child = mount(Detail, { attachTo: document.body, props: { open: true, record: clone(ROW) }, global: { plugins: [Antd], config: { errorHandler: error => runtimeErrors.push(String(error?.stack || error)) } } })
  wrappers.push(child); await settle()
  assert.equal(child.props('viewerScope'), 'self'); assert.equal(child.props('canEditStatement'), false)
  assert.equal(modal().querySelector('[role="switch"]'), null); assert.equal(region(), null)
  assert.equal(child.findComponent({ name: 'RequestFailureAlert' }).exists(), false)
})

test('actual UserUsage consumer retains its default no-feedback detail contract', async () => {
  const Usage = require(path.join(web, 'src/pages/UserUsage/index.vue'))
  const w = await page(CONSUMER, Usage)
  w.vm.handleShowFeeDetail(w.vm.dataSource[0]); await settle()
  assert.equal(modal().querySelector('[role="switch"]'), null); assert.equal(region(), null)
  assert.ok(!modal().textContent.includes('SYNTHETIC_MODEL')); assert.equal(writeCount(), 0)
})

test('UNKNOWN keeps the already-correct fixed warning, survives toast destruction and explicit GET cannot prove this write applied', async () => {
  const w = await page(), original = await fail(w)
  safe(w, original, 'unknown-outcome'); assert.equal(getTransportRequestId(original), undefined)
  assert.ok(notices.some(item => item.text === '网络中断，操作结果未确认。请先刷新核对，勿重复提交。'))
  assert.match(region().textContent, /结果.*(?:尚未确认|未确认)/); assert.match(region().textContent, /勿重复提交/)
  assert.equal(region().querySelector('[data-action="copy-request-diagnostic"]'), null)
  const before = calls.length; await settle(); assert.equal(calls.length, before, 'no automatic retry/read/write')
  readRows = [{ ...ROW, showInStatement: false, amount: 99, subject: 'SYNTHETIC_READ_MUST_NOT_REPLACE_DETAIL' }]
  reconcileNode().click(); await settle(); safe(w, original, 'unknown-outcome')
  assert.equal(readCount(), 2); assert.equal(writeCount(), 1); assert.equal(w.vm.selectedBillingRecord.showInStatement, false)
  assert.equal(notices.filter(item => item.kind === 'success').length, 0); preserve(w)
  assert.match(region().querySelector('[data-billing-statement-reconciliation]').textContent, /不是.*凭据|不能|不代表|不证明|仍.*未确认|仍.*待核对/)
  Antd.message.destroy(); await settle(); assert.ok(region().textContent.includes('未确认'))
})

for (const status of [401, 403, 429, 503]) test('HTTP ' + status + ' fulfilled/rejected preserve exact original diagnostics and never render raw payload', async () => {
  const w = await page(); await open(w)
  for (const mode of ['fulfilled', 'rejected']) {
    const call = await issue(w), original = failure(status, mode)
    if (mode === 'fulfilled') call.resolve(original); else call.reject(original)
    await settle(); const value = safe(w, original, { 401: 'unauthorized', 403: 'forbidden', 429: 'rate-limit', 503: 'upstream' }[status])
    assert.equal(value.props('failure'), original, 'exact original WeakMap key, not proxy/clone')
    assert.equal(getTransportRequestId(original), ID); assert.ok(value.text().includes(ID)); assert.equal(readCount(), 1)
    assert.equal(w.vm.selectedBillingRecord.showInStatement, true); assert.equal(switchNode().disabled, false); preserve(w)
  }
  assert.equal(writeCount(), 2); assert.equal(notices.filter(item => item.kind === 'success').length, 0)
})

for (const mode of ['fulfilled', 'rejected']) test(mode + ' primitive failures remain safely durable without fabricated diagnostics', async () => {
  const w = await page(); await open(w)
  for (const value of [null, undefined, false, 0, '', PRIVATE]) {
    const call = await issue(w)
    if (mode === 'fulfilled') call.resolve(value); else call.reject(value)
    await settle(); const item = safe(w, value)
    assert.equal(item.props('failure').code, 'MALFORMED_RESPONSE'); assert.equal(region().querySelector('[data-action="copy-request-diagnostic"]'), null)
    assert.equal(w.vm.selectedBillingRecord.showInStatement, true); assert.equal(w.vm.visibilitySaving[ROW.id], false)
  }
  assert.equal(writeCount(), 6); assert.equal(readCount(), 1)
})

for (const show of [false, true]) test('explicit valid boolean GET ' + show + ' updates only the read-only current record flag and never clears write warning', async () => {
  const w = await page(), original = await fail(w, failure())
  const rows = clone(w.vm.dataSource), pagination = clone(w.vm.pagination), summary = clone(w.vm.summary)
  readRows = [{ ...ROW, showInStatement: show, amount: 99, subject: 'SYNTHETIC_UNRELATED_READ' }]
  reconcileNode().click(); await settle()
  safe(w, original); assert.equal(w.vm.selectedBillingRecord.showInStatement, show); preserve(w)
  const otherRows = clone(w.vm.dataSource); rows[0].showInStatement = otherRows[0].showInStatement
  assert.deepEqual(otherRows, rows, 'no unrelated row fields replaced by dedicated GET')
  assert.deepEqual(clone(w.vm.pagination), pagination); assert.deepEqual(clone(w.vm.summary), summary)
  assert.equal(writeCount(), 1); assert.equal(readCount(), 2); assert.equal(notices.filter(item => item.kind === 'success').length, 0)
})

for (const initial of [false, true]) test('missing target, missing flag and nonboolean readback preserve actual ' + initial + ' without fabricating state', async () => {
  readRows = [{ ...ROW, showInStatement: initial }, SECOND]
  const w = await page(), original = await fail(w), noFlag = clone(ROW); delete noFlag.showInStatement
  for (const rows of [[], [{ ...ROW, id: 999 }], [noFlag], ...[null, 0, 1, 'false', 'true', {}, []].map(showInStatement => [{ ...ROW, showInStatement }])]) {
    readRows = rows; const before = readCount(); reconcileNode().click(); await settle()
    assert.equal(readCount(), before + 1); assert.equal(writeCount(), 1); assert.equal(w.vm.selectedBillingRecord.showInStatement, initial)
    safe(w, original); preserve(w); readBusy(w, false)
    assert.match(region().querySelector('[data-billing-statement-reconciliation]').textContent, /未.*(?:有效|返回|核对|找到)/)
  }
})

test('ordinary list GET and committed query change cannot clear the current operation warning', async () => {
  const w = await page(), original = await fail(w, failure())
  button(w.element, '刷新').click(); await settle(); safe(w, original)
  w.vm.keywordInput = 'SYNTHETIC_COMMITTED_QUERY'; w.vm.applyKeyword(); await settle(); safe(w, original)
  assert.equal(readCount(), 3); assert.equal(writeCount(), 1); assert.equal(w.vm.selectedBillingRecord.id, ROW.id)
  assert.ok(region().textContent.includes(ID))
})

test('explicit GET dispatch uses current committed query rather than the failed-write entry query', async () => {
  const w = await page(), original = await fail(w, failure()), callback = w.vm.reconcileStatementVisibility
  assert.equal(typeof callback, 'function')
  w.vm.filters.keyword = 'SYNTHETIC_CURRENT_QUERY'; w.vm.pagination.pageSize = 20; await settle()
  await callback(); await settle(); safe(w, original)
  assert.deepEqual(lastRead().payload, { keyword: 'SYNTHETIC_CURRENT_QUERY', page: 1, pageSize: 20 }); assert.equal(writeCount(), 1)
})

for (const outcome of ['success', 'failure']) test('query changed during dedicated GET ' + outcome + ' rejects obsolete readback and releases issued busy only after settlement', async () => {
  const w = await page(), original = await fail(w, failure())
  holdReads = true; reconcileNode().click(); await settle(); const old = lastRead()
  readBusy(w, true)
  w.vm.filters.keyword = 'SYNTHETIC_NEW_COMMITTED_QUERY'; await settle()
  readBusy(w, true)
  if (outcome === 'success') old.resolve(list([{ ...ROW, showInStatement: false }]))
  else old.reject(failure(503, 'rejected', OLD_ID))
  await settle(); safe(w, original); assert.equal(w.vm.selectedBillingRecord.showInStatement, true)
  readBusy(w, false)
  assert.ok(!document.body.textContent.includes(OLD_ID)); assert.equal(writeCount(), 1)
  assert.match(region().querySelector('[data-billing-statement-reconciliation]').textContent, /筛选|分页|变化|重新/)
  reconcileNode().click(); await settle(); const current = lastRead()
  assert.equal(current.payload.keyword, 'SYNTHETIC_NEW_COMMITTED_QUERY')
  current.resolve(list([{ ...ROW, showInStatement: false }])); await settle()
  safe(w, original); assert.equal(w.vm.selectedBillingRecord.showInStatement, false); readBusy(w, false)
})

test('dedicated GET failure keeps its own original diagnostic without replacing the financial-write warning', async () => {
  const w = await page(), original = await fail(w)
  holdReads = true; reconcileNode().click(); await settle(); const read = lastRead(), error = failure(503, 'rejected', OLD_ID)
  read.reject(error); await settle(); safe(w, original, 'unknown-outcome')
  assert.ok(alert(w, error).text().includes(OLD_ID)); readBusy(w, false)
  assert.equal(w.vm.selectedBillingRecord.showInStatement, true); assert.equal(writeCount(), 1)
})

for (const reason of ['record', 'dialog', 'entry', 'actor', 'role', 'route', 'logout', 'unmount']) test(reason + ' retires captured/native reconciliation and pending diagnostic-copy callbacks', async () => {
  const w = await page(), original = await fail(w, failure())
  const oldRead = reconcileNode(), oldCopy = region().querySelector('[data-action="copy-request-diagnostic"]'), callback = w.vm.reconcileStatementVisibility
  assert.equal(typeof callback, 'function'); assert.ok(oldCopy)
  let finishCopy; const copied = [], previousClipboard = navigator.clipboard
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: value => { copied.push(value); return new Promise(resolve => { finishCopy = resolve }) } } })
  try {
    oldCopy.click(); await settle(); assert.deepEqual(copied, [ID])
    const navigation = retire(w, reason); if (navigation) await navigation
    const reads = readCount(), writes = writeCount()
    oldRead.click(); oldCopy.click(); callback(); assert.equal(readCount(), reads, 'same-tick retired callbacks do not issue GET')
    await settle(); const after = readCount(); oldRead.click(); oldCopy.click(); callback(); finishCopy(); await settle()
    assert.equal(readCount(), after); assert.equal(writeCount(), writes); assert.deepEqual(copied, [ID])
    assert.ok(!document.body.textContent.includes(ID)); assert.ok(!document.body.textContent.includes('诊断编号已复制'))
    if (reason !== 'unmount') assert.notEqual(w.vm.statementFailure, original)
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: previousClipboard }) }
})

for (const outcome of ['success', 'failure']) for (const reason of ['record', 'dialog', 'entry', 'actor', 'role', 'route', 'logout', 'unmount']) test(reason + ' pending write ' + outcome + ' preserves original owned-200 semantics while retiring stale failure display', async () => {
  const w = await page(); await open(w); const old = await issue(w)
  assert.equal(switchNode().disabled, true)
  const navigation = retire(w, reason); if (navigation) await navigation
  await settle(); const count = notices.length
  if (outcome === 'success') old.resolve(ok({})); else old.reject(failure(503, 'rejected', OLD_ID))
  await settle()
  const sameWriteOwner = outcome === 'success' && ['record', 'dialog', 'entry'].includes(reason)
  if (sameWriteOwner) {
    // A real 200 ACK is not uncertain. Keep the original same-actor successful
    // write-to-captured-id behavior; only its old error/copy/GET display retired.
    assert.equal(notices.length, count + 1); assert.equal(notices.at(-1).kind, 'success')
    assert.equal(w.vm.dataSource.find(row => row.id === ROW.id).showInStatement, false)
    if (reason === 'record') { assert.equal(w.vm.selectedBillingRecord.id, SECOND.id); assert.equal(w.vm.selectedBillingRecord.showInStatement, true) }
    if (reason === 'dialog') assert.equal(w.vm.billingDetailOpen, false, 'success cannot reopen the closed dialog')
  } else assert.equal(notices.length, count)
  assert.ok(!document.body.textContent.includes(OLD_ID)); assert.equal(writeCount(), 1)
  if (reason !== 'unmount') assert.ok(!w.vm.visibilitySaving[ROW.id], 'settled retired request cannot strand busy')
})

for (const outcome of ['success', 'failure']) test('retired reconciliation ' + outcome + ' cannot repaint a new record or release its active reconciliation busy', async () => {
  const w = await page(); await fail(w); holdReads = true; reconcileNode().click(); await settle(); const old = lastRead()
  w.vm.handleShowFeeDetail(w.vm.dataSource.find(row => row.id === SECOND.id)); await settle()
  const error = await fail(w, failure()); reconcileNode().click(); await settle(); const current = lastRead()
  assert.notStrictEqual(old, current); readBusy(w, true)
  if (outcome === 'success') old.resolve(list([{ ...ROW, showInStatement: false }]))
  else old.reject(failure(503, 'rejected', OLD_ID))
  await settle(); safe(w, error); assert.equal(w.vm.selectedBillingRecord.id, SECOND.id)
  assert.equal(w.vm.selectedBillingRecord.showInStatement, true); readBusy(w, true)
  assert.ok(!document.body.textContent.includes(OLD_ID))
  current.resolve(list([{ ...SECOND, showInStatement: false }])); await settle()
  safe(w, error); assert.equal(w.vm.selectedBillingRecord.showInStatement, false); readBusy(w, false); assert.equal(writeCount(), 2)
})

test('same mounted logout then fresh B login recovers current feedback while A copied/native callbacks remain retired', async () => {
  const w = await page(); await fail(w, failure(503, 'rejected', OLD_ID))
  const oldRead = reconcileNode(), oldCopy = region().querySelector('[data-action="copy-request-diagnostic"]'), callback = w.vm.reconcileStatementVisibility
  user.clearStoredCurrentUser(); await settle(); assert.equal(region(), null)
  user.setStoredCurrentUser(OTHER); await settle(); const original = await fail(w, failure())
  safe(w, original); assert.equal(user.getStoredCurrentUser().id, OTHER.id); assert.equal(lastWrite().actor, OTHER.id)
  const copied = [], previousClipboard = navigator.clipboard
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async value => { copied.push(value) } } })
  try {
    const before = calls.length; oldRead.click(); oldCopy.click(); callback(); await settle()
    assert.equal(calls.length, before); assert.deepEqual(copied, [])
    region().querySelector('[data-action="copy-request-diagnostic"]').click(); await settle(); assert.deepEqual(copied, [ID])
    reconcileNode().click(); await settle(); safe(w, original); assert.equal(readCount(), 3); assert.equal(writeCount(), 2)
    assert.ok(!document.body.textContent.includes(OLD_ID))
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: previousClipboard }) }
})

test('same actor balance-only display update preserves the owned diagnostic and explicit read capability', async () => {
  const w = await page(), original = await fail(w, failure()), reads = readCount()
  user.setStoredCurrentUser({ ...ADMIN, balance: 99.000001 }); await settle(); safe(w, original)
  assert.equal(readCount(), reads); reconcileNode().click(); await settle(); safe(w, original)
  assert.equal(readCount(), reads + 1); assert.equal(writeCount(), 1)
})
