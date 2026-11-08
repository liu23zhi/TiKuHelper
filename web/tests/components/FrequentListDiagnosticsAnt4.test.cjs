/* Real Vue/Antd; synthetic GET responses only. No provider, network, payment or database IO. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const { buildResponsePayload, captureTransportRequestError, getTransportRequestId } = require(path.join(root, 'src/utils/requestHelpers.mjs'))
const first = { id: 8261, role: 'admin', username: 'synthetic-list-a' }
const second = { id: 8262, role: 'admin', username: 'synthetic-list-b' }
const id = '0123456789abcdef0123456789abcdef'
const secret = 'SYNTHETIC_TOKEN_PRIVATE_CONFIG_URL_PAYLOAD'
let pending = [], writes = [], copies = [], wrapper, router
Object.defineProperty(navigator, 'clipboard', { configurable:true, value:{ writeText:value => { copies.push(value); return Promise.resolve() } } })
const apiPath = path.join(root, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
const read = (kind, query) => new Promise((resolve, reject) => pending.push({ kind, query: { ...query }, resolve, reject }))
const denyWrite = (...args) => { writes.push(args); throw new Error('UNEXPECTED_MUTATION') }
api.exports = {
 getBillingRecords: q => read('billing', q), getLogList: q => read('log', q), getQuestions: q => read('tiku', q),
 getCourses: () => Promise.resolve({ status:200, data:[] }),
 getIpLocations: () => Promise.resolve({ status:200, data:{} }), getMyLocations: () => Promise.resolve({ status:200, data:{} }),
 updateBillingStatementVisibility: denyWrite, delQuestions: denyWrite, updateQuestions: denyWrite,
 createQuestions: denyWrite, importQuestions: denyWrite,
}
require.cache[apiPath] = api
const pages = [
 { kind:'billing', file:'BillingRecordList', method:'handleLoad', message:'获取账单列表失败', success: { status:200, data:{ data:[], total:0, summary:{ totalAmount:0, currentPageAmount:0 } } } },
 { kind:'log', file:'LogList', method:'handleLogList', message:'获取操作日志失败，请重试。', success: { status:200, data:{ data:[], total:0 } } },
 { kind:'tiku', file:'TikuComponent', method:'fetchData', message:'获取题目列表失败', success: { status:200, data:{ items:[], total:0 } } },
]
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function fixture(page) {
 pending = []; writes = []; copies = []; user.setStoredCurrentUser(first)
 const pagePath = path.join(root, 'src/pages', page.file, 'index.vue')
 delete require.cache[pagePath]
 const component = require(pagePath)
 router = createRouter({ history:createMemoryHistory(), routes:[{ path:'/', component }] })
 await router.push('/'); await router.isReady()
 wrapper = mount(component, { attachTo:document.body, global:{ plugins:[router, Antd] } })
 await settle()
 assert.equal(pending.length, 1, 'exactly one initial list GET')
 return pending[0]
}
function alert() { return wrapper.findComponent({ name:'RequestFailureAlert' }) }
function search(page, keyword) {
 if (page.kind === 'tiku') { wrapper.vm.searchValue.question = keyword; wrapper.vm.onSearch() }
 else { wrapper.vm.keywordInput = keyword; wrapper.vm.applyKeyword() }
}
function failPayload(status=403, header=id) {
 return buildResponsePayload({ status, headers: header === undefined ? {} : { 'x-request-id':header }, data:{ message:secret, detail:secret, config:secret, requestId:'NOT_TRANSPORT_ID' } })
}
function failureError(header=id) {
 const error = new Error(secret)
 error.config = { url:secret, headers:{ Authorization:secret } }
 error.response = { status:503, headers: header === undefined ? {} : { 'x-request-id':header }, data:{ message:secret, payload:secret } }
 return captureTransportRequestError(error)
}
function assertFailure(page, failure, expected=id) {
 assert.ok(alert().exists(), 'persistent real RequestFailureAlert renders for ' + page.kind)
 assert.equal(alert().props('failure'), failure, 'raw object identity survives WeakMap transport diagnostics')
 assert.equal(alert().props('message'), page.message)
 assert.equal(alert().props('retryable'), true, 'only explicitly requested list GET retries')
 assert.equal(getTransportRequestId(alert().props('failure')), expected || undefined)
 assert.ok(alert().find('[data-failure-kind]').exists(), 'safe classified failure remains inline')
 if (expected) assert.ok(alert().text().includes(expected), 'validated transport ID visibly rendered')
 assert.ok(!alert().text().includes(secret), 'no Error/config/payload or URL content displayed')
 assert.ok(!alert().text().includes('NOT_TRANSPORT_ID'), 'business requestId is not a diagnostic header')
}
afterEach(async () => {
 wrapper?.unmount(); wrapper = undefined
 router?.options.history.destroy(); router = undefined
 await settle(); Antd.message.destroy(); Antd.Modal.destroyAll(); document.body.replaceChildren(); user.clearStoredCurrentUser()
 assert.deepEqual(writes, [], 'read-only recovery never replays a write, charge, import, or delete')
})
for (const page of pages) {
 test(page.kind + ': non200 retains original transport payload and explicit retry reuses the exact GET query', async () => {
  const initial = await fixture(page)
  const failure = failPayload(); initial.resolve(failure); await settle()
  assertFailure(page, failure)
  assert.equal(pending.length,1, 'failure does not automatically retry')
  assert.deepEqual(copies, [], 'ID is never copied without explicit action')
  await alert().find('[data-action="copy-request-diagnostic"]').trigger('click'); await settle()
  assert.deepEqual(copies,[id], 'only the verified ID is copied')
  const retry = alert().find('[data-action="retry-request"]')
  assert.ok(retry.exists(), 'real retry button'); await retry.trigger('click'); await settle()
  assert.equal(pending.length,2); assert.deepEqual(pending[1].query,initial.query)
  assert.equal(alert().exists(), false, 'new list request retires old diagnostics immediately')
  pending[1].resolve(page.success); await settle()
  assert.equal(alert().exists(), false, 'successful recovery removes old diagnosis')
  console.log('LIST_DIAGNOSTIC_WITNESS '+JSON.stringify({ page:page.kind, case:'raw-non200-retry', initialID:getTransportRequestId(failure), getCount:pending.length, writes:writes.length, recovered:true }))
 })
 test(page.kind + ': caught Error preserves verified header; newer query and unmount discard stale failures', async () => {
  const initial = await fixture(page)
  const failure = failureError(); initial.reject(failure); await settle(); assertFailure(page, failure)
  const staleRetry = alert().find('[data-action="retry-request"]').element
  const staleCopy = alert().find('[data-action="copy-request-diagnostic"]').element
  search(page,'SYNTHETIC_OLD_QUERY')
  const sameTickReads=pending.length; staleRetry.click(); staleCopy.click()
  assert.equal(pending.length,sameTickReads,'same-tick retired query DOM retry cannot issue another GET')
  assert.deepEqual(copies,[],'same-tick retired query DOM copy cannot publish retired ID')
  await settle(); const old = pending.at(-1)
  search(page,'SYNTHETIC_CURRENT_QUERY'); await settle(); const current = pending.at(-1)
  assert.notDeepEqual(old.query,current.query, 'actual submitted query changed')
  const readsBeforeStaleClick=pending.length; staleRetry.click(); staleCopy.click(); await settle()
  assert.equal(pending.length,readsBeforeStaleClick,'retired DOM retry cannot start another GET')
  assert.deepEqual(copies,[],'retired DOM copy cannot copy a retired ID')
  current.resolve(page.success); await settle(); old.reject(failureError()); await settle()
  assert.equal(alert().exists(), false, 'retired query error cannot reopen diagnostics after success')
  wrapper.vm[page.method](); await settle(); const unmounted = pending.at(-1)
  wrapper.unmount(); unmounted.reject(failureError()); await settle()
  assert.ok(!document.body.textContent.includes(id), 'unmounted failure leaves no ID or feedback')
 })
 test(page.kind + ': identity retirement clears displayed failure and suppresses old pending diagnostics', async () => {
  const initial = await fixture(page)
  const failure=failPayload(); initial.resolve(failure); await settle(); assertFailure(page,failure)
  wrapper.vm[page.method](); await settle(); const old = pending.at(-1)
  user.setStoredCurrentUser(second); await settle()
  assert.equal(alert().exists(), false, 'owner change clears diagnostic synchronously')
  old.reject(failureError()); await settle()
  assert.equal(alert().exists(), false, 'old identity failure cannot publish into the new scope')
  if (pending.at(-1) !== old) { pending.at(-1).resolve(page.success); await settle() }
  wrapper.vm[page.method](); await settle(); const current=pending.at(-1)
  const fresh = failPayload(429); current.resolve(fresh); await settle(); assertFailure(page,fresh)
  user.setStoredCurrentUser({ ...second,role:'user' }); await settle()
  assert.equal(alert().exists(), false, 'privilege retirement clears diagnostic')
  if (pending.at(-1) !== current) { pending.at(-1).resolve(page.success); await settle() }
 })
 test(page.kind + ': same-tick principal and privilege retirement reject retained native retry and copy actions', async () => {
  const initial = await fixture(page)
  initial.resolve(failPayload()); await settle()
  let oldRetry=alert().find('[data-action="retry-request"]').element
  let oldCopy=alert().find('[data-action="copy-request-diagnostic"]').element
  user.setStoredCurrentUser(second)
  let reads=pending.length; oldRetry.click(); oldCopy.click()
  assert.equal(pending.length,reads,'same-tick retired principal cannot retry through old native DOM')
  assert.deepEqual(copies,[],'same-tick retired principal cannot copy through old native DOM')
  await settle(); assert.equal(alert().exists(),false)
  if (pending.at(-1)!==initial) { pending.at(-1).resolve(page.success); await settle() }
  wrapper.vm[page.method](); await settle(); pending.at(-1).resolve(failPayload()); await settle()
  oldRetry=alert().find('[data-action="retry-request"]').element
  oldCopy=alert().find('[data-action="copy-request-diagnostic"]').element
  user.setStoredCurrentUser({ ...second,role:'user' })
  reads=pending.length; oldRetry.click(); oldCopy.click()
  assert.equal(pending.length,reads,'same-tick retired privilege cannot retry through old native DOM')
  assert.deepEqual(copies,[],'same-tick retired privilege cannot copy through old native DOM')
  await settle(); assert.equal(alert().exists(),false)
 })
 test(page.kind + ': malformed non-object response and thrown primitive remain persistent safe failures without fabricated IDs', async () => {
  const initial = await fixture(page)
  initial.resolve(null); await settle()
  assert.ok(alert().exists(),'null response remains a persistent error rather than silently disappearing')
  assert.equal(alert().props('failure').code,'MALFORMED_RESPONSE')
  assert.equal(getTransportRequestId(alert().props('failure')),undefined)
  assert.ok(!alert().text().includes(id)); assert.equal(pending.length,1)
  await settle(); assert.ok(alert().exists(),'malformed response remains available after render settles')
  await alert().find('[data-action="retry-request"]').trigger('click'); await settle()
  assert.equal(pending.length,2); assert.deepEqual(pending.at(-1).query,initial.query)
  pending.at(-1).resolve(page.success); await settle(); assert.equal(alert().exists(),false)
  wrapper.vm[page.method](); await settle(); const thrown=pending.at(-1)
  thrown.reject(secret); await settle()
  assert.ok(alert().exists(),'thrown primitive remains a persistent safe error')
  assert.equal(alert().props('failure').code,'MALFORMED_RESPONSE')
  assert.equal(getTransportRequestId(alert().props('failure')),undefined)
  assert.ok(!alert().text().includes(secret)); assert.ok(!alert().text().includes(id))
  assert.equal(pending.length,3,'malformed failure never automatically retries')
  console.log('LIST_MALFORMED_WITNESS '+JSON.stringify({ page:page.kind, nullResponsePersistent:true, primitiveThrowPersistent:true, fabricatedDiagnostic:false, automaticRetry:false, writes:writes.length }))
 })
 test(page.kind + ': absent or invalid diagnostic header never forges an ID, exposes payload, or starts another GET', async () => {
  const initial = await fixture(page)
  const failure = failPayload(500, 'BAD_HEADER\n'+secret); initial.resolve(failure); await settle()
  assertFailure(page,failure,null)
  assert.equal(pending.length,1)
  wrapper.vm[page.method](); await settle()
  const error = new Error(secret); captureTransportRequestError(error); pending.at(-1).reject(error); await settle()
  assertFailure(page,error,null); assert.equal(pending.length,2)
  assert.ok(!alert().text().includes(id))
 })
}
