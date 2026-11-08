// Real Vue/Antd + actual RequestFailureAlert; synthetic read-only APIs, no network/DB.
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const { buildResponsePayload, captureTransportRequestError } = require(path.join(web, 'src/utils/requestHelpers.mjs'))
const ADMIN = { id: 8261, username: 'synthetic-admin', role: 'admin', balance: 1.234567 }
const OTHER = { id: 8262, username: 'synthetic-other', role: 'admin', balance: 2.345678 }
const ID = '1234567890abcdef1234567890abcdef', OLD_ID = 'abcdef1234567890abcdef1234567890'
const SECRET = 'SYNTHETIC_PRIVATE_TOKEN_URL_CONFIG'
const expectInline = process.env.EXPECT_INLINE !== '0'
const targets = {
  users: process.env.TIKU_USERLIST_TARGET || path.join(web, 'src/pages/UserList/index.vue'),
  tokens: process.env.TIKU_TOKEN_TARGET || path.join(web, 'src/pages/AccessTokenManagement/index.vue'),
}
let wrappers = [], calls = [], queued = [], pending = [], hold = false
const ok = data => ({ status: 200, data })
const healthy = kind => kind === 'users' ? ok({ data: [{ id: 81, username: 'synthetic-row', role: 'user', status: 'enabled', balance: 1.234567 }], total: 1, page: 1, pageSize: 10 }) : ok([{ id: 82, name: 'synthetic-token', tokenValue: 'SYNTHETIC_ONLY_TOKEN', status: 'enabled', totalQuotaLimit: 0.000062, totalAmountUsed: 1.234567 }])
function failure(status = 503, id = ID, code = 'UPSTREAM_ERROR') {
  return buildResponsePayload({ status, headers: id ? { 'X-Request-ID': id } : {}, data: { code, message: SECRET, detail: SECRET, config: { url: SECRET, token: SECRET } } })
}
function read(kind, payload) {
  calls.push({ kind, payload })
  if (hold) return new Promise((resolve, reject) => pending.push({ kind, payload, resolve, reject }))
  if (queued.length) {
    const next = queued.shift()
    if (next instanceof Error) return Promise.reject(next)
    if (next?.__rejectFixture === true) return Promise.reject(next.value)
    return Promise.resolve(next)
  }
  return Promise.resolve(healthy(kind))
}
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
  getUserList: payload => read('users', payload),
  getAccessTokens: payload => read('tokens', payload),
  getSystemSettings: () => Promise.resolve(ok({ newUserBonusAmount: 0 })),
  getPhoneLocations: () => Promise.resolve(ok({})), getIpLocations: () => Promise.resolve(ok({})),
  ...Object.fromEntries(['addUser', 'updateUser', 'adjustUserBalance', 'resetUserPassword', 'delUser', 'createAccessToken', 'updateAccessToken', 'deleteAccessToken', 'createWechatLoginQRCode', 'resolveAdminWechatIDByScan', 'resolveAdminWechatIDByCode'].map(name => [name, () => { calls.push({ kind: 'FORBIDDEN_WRITE', name }); throw new Error('Read-only fixture forbids ' + name) }])),
}
require.cache[apiPath] = api
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function page(kind, account = ADMIN) {
  user.setStoredCurrentUser(account)
  const w = mount(require(targets[kind]), { attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(w); await settle(); return w
}
const alert = w => w.findComponent({ name: 'RequestFailureAlert' })
const button = (w, pattern) => alert(w).find(pattern.test('复制') ? '[data-action="copy-request-diagnostic"]' : '[data-action="retry-request"]')
const readCount = () => calls.filter(c => c.kind === 'users' || c.kind === 'tokens').length
function load(w, kind) { return kind === 'users' ? w.vm.handleUserList() : w.vm.handleLoad() }
function assertNoWrites() { assert.equal(calls.filter(c => c.kind === 'FORBIDDEN_WRITE').length, 0) }
afterEach(async () => {
  for (const w of wrappers) if (w.exists()) w.unmount()
  wrappers = []; for (const p of pending) p.resolve(healthy(p.kind)); await settle()
  calls = []; queued = []; pending = []; hold = false
  user.clearStoredCurrentUser(); localStorage.clear(); window.sessionStorage.clear(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
})

for (const kind of ['users', 'tokens']) test('same fixture ' + kind + ': durable diagnostic ID and explicit GET retry without writes', async () => {
  const original = failure(); queued.push(original)
  const w = await page(kind)
  const before = { inline: alert(w).exists(), idVisible: w.text().includes(ID), reads: readCount(), writes: calls.filter(c => c.kind === 'FORBIDDEN_WRITE').length }
  assert.equal(before.inline, expectInline); assert.equal(before.idVisible, expectInline); assert.equal(before.reads, 1); assertNoWrites()
  let copied = [], oldClipboard = navigator.clipboard
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async value => { copied.push(value) } } })
  try {
    if (expectInline) {
      assert.equal(alert(w).props('failure'), original, 'preserve original WeakMap key, not a Vue proxy or clone')
      assert.ok(!alert(w).text().includes(SECRET), 'raw response/error/config/URL/token is never rendered')
      assert.equal(alert(w).props('retryable'), true)
      await settle(); assert.equal(readCount(), 1, 'no automatic retry')
      await button(w, /复制/).trigger('click'); await settle(); assert.deepEqual(copied, [ID])
      await button(w, /重试/).trigger('click'); await settle()
      assert.equal(alert(w).exists(), false); assert.equal(readCount(), 2); assert.equal(w.vm.dataSource.length, 1)
    }
    assertNoWrites()
    console.log('USER_TOKEN_DIAGNOSTIC_OBSERVED ' + JSON.stringify({ arm: process.env.BMR_ARM || 'MODIFIED', kind, before, copied, after: { inline: alert(w).exists(), rows: w.vm.dataSource.length, reads: readCount(), writes: calls.filter(c => c.kind === 'FORBIDDEN_WRITE').length } }))
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: oldClipboard }) }
})

for (const kind of ['users', 'tokens']) test(kind + ': original rejected Error retains validated ID and hides raw diagnostics', async () => {
  const error = new Error(SECRET); error.config = { url: SECRET, headers: { Authorization: SECRET } }; error.response = { status: 429, headers: { 'x-request-id': ID }, data: { message: SECRET } }
  captureTransportRequestError(error); queued.push(error)
  const w = await page(kind)
  assert.equal(alert(w).props('failure'), error); assert.ok(alert(w).text().includes(ID)); assert.ok(!alert(w).text().includes(SECRET)); assertNoWrites()
})

for (const kind of ['users', 'tokens']) test(kind + ': malformed primitive responses and rejection values remain visible without a fabricated ID', async () => {
  const w = await page(kind)
  for (const mode of ['response', 'rejection']) for (const value of [null, undefined, false, 0, '', SECRET]) {
    queued.push(mode === 'rejection' ? { __rejectFixture: true, value } : value)
    await load(w, kind); await settle()
    assert.equal(alert(w).exists(), true, mode + ': ' + String(value))
    assert.equal(alert(w).props('failure').code, 'MALFORMED_RESPONSE')
    assert.ok(!alert(w).text().includes(SECRET)); assert.ok(!alert(w).text().includes(ID))
    assert.equal(button(w, /复制/).exists(), false, 'no transport ID means no copy control')
    assertNoWrites()
  }
  console.log('MALFORMED_LIST_FAILURE_OBSERVED ' + JSON.stringify({ kind, responseCases: 6, rejectionCases: 6, visible: true, fabricatedId: false, writes: 0 }))
})

for (const kind of ['users', 'tokens']) test(kind + ': no header never invents ID; GET start and success retire the previous failure', async () => {
  queued.push(failure(403, null)); const w = await page(kind)
  assert.equal(alert(w).exists(), true); assert.ok(!alert(w).text().includes(ID)); assert.equal(user.getStoredCurrentUser().id, ADMIN.id)
  queued.push(failure()); await load(w, kind); await settle(); assert.ok(alert(w).text().includes(ID))
  const staleRetry = kind === 'users' ? w.vm.retryUserList : w.vm.retryTokenList
  const oldNode = button(w, /重试/).element, oldCopy = button(w, /复制/).element
  let copies = 0; const oldClipboard = navigator.clipboard
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async () => { copies++ } } })
  try {
    hold = true; load(w, kind); const started = readCount(); oldNode.click(); oldCopy.click(); assert.equal(readCount(), started); assert.equal(copies, 0, 'same-tick retired copy cannot touch clipboard')
    await settle(); assert.equal(alert(w).exists(), false)
    const count = readCount(); staleRetry(); oldNode.click(); await settle(); assert.equal(readCount(), count, 'retired callback/native DOM cannot load again')
    pending.at(-1).resolve(healthy(kind)); await settle(); assert.equal(alert(w).exists(), false); assert.equal(w.vm.dataSource.length, 1); assertNoWrites()
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: oldClipboard }) }
})

test('users: latest query failure owns diagnostics; query mutation clears inline immediately', async () => {
  const w = await page('users'); hold = true
  w.vm.keywordInput = 'OLD_PRIVATE_QUERY'; w.vm.applyKeyword(); const old = pending.at(-1)
  w.vm.keywordInput = 'CURRENT_PRIVATE_QUERY'; w.vm.applyKeyword(); const current = pending.at(-1)
  current.resolve(failure()); await settle(); assert.ok(alert(w).text().includes(ID))
  old.resolve(failure(503, OLD_ID)); await settle(); assert.ok(alert(w).text().includes(ID)); assert.ok(!alert(w).text().includes(OLD_ID))
  const staleRetry = w.vm.retryUserList, oldNode = button(w, /重试/).element
  w.vm.filters.role = 'admin'; await settle(); assert.equal(alert(w).exists(), false)
  const count = readCount(); staleRetry(); oldNode.click(); await settle(); assert.equal(readCount(), count)
  assertNoWrites()
})

test('tokens: mine/all query retires the old failure and late response without escalating ordinary users', async () => {
  queued.push(failure()); const w = await page('tokens')
  const staleRetry = w.vm.retryTokenList, oldNode = button(w, /重试/).element
  hold = true; w.vm.toggleAllTokens(); const all = pending.at(-1); await settle(); assert.equal(alert(w).exists(), false); assert.deepEqual(all.payload, { scope: 'all' })
  w.vm.toggleAllTokens(); const mine = pending.at(-1)
  all.resolve(failure(503, OLD_ID)); await settle(); assert.equal(alert(w).exists(), false); assert.equal(w.vm.tableLoading, true)
  mine.resolve(failure()); await settle(); assert.ok(alert(w).text().includes(ID))
  const count = readCount(); staleRetry(); oldNode.click(); await settle(); assert.equal(readCount(), count)
  user.setStoredCurrentUser({ ...ADMIN, role: 'user' }); await settle()
  assert.equal(alert(w).exists(), false); assert.equal(w.vm.showAllTokens, false); const ordinary = pending.at(-1); assert.deepEqual(ordinary.payload, {})
  const ordinaryCount = readCount(); w.vm.toggleAllTokens(); await settle(); assert.equal(readCount(), ordinaryCount); ordinary.resolve(healthy('tokens')); await settle(); assertNoWrites()
})

for (const kind of ['users', 'tokens']) for (const retirement of ['account', 'role', 'logout', 'unmount']) test(kind + ': ' + retirement + ' retires inline, stale native retry/copy and pending clipboard feedback', async () => {
  queued.push(failure()); const w = await page(kind)
  const oldRetry = button(w, /重试/).element, oldCopy = button(w, /复制/).element
  const retry = kind === 'users' ? w.vm.retryUserList : w.vm.retryTokenList
  const oldClipboard = navigator.clipboard; let resolveCopy, copies = []
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: value => { copies.push(value); return new Promise(resolve => { resolveCopy = resolve }) } } })
  try {
    oldCopy.click(); await settle(); assert.deepEqual(copies, [ID]); hold = true
    if (retirement === 'account') user.setStoredCurrentUser(OTHER)
    else if (retirement === 'role') user.setStoredCurrentUser({ ...ADMIN, role: 'user' })
    else if (retirement === 'logout') user.clearStoredCurrentUser()
    else w.unmount()
    const sameTickCount = readCount(); oldRetry.click(); oldCopy.click(); retry()
    assert.equal(readCount(), sameTickCount, 'same-tick stale retry cannot load new scope'); assert.deepEqual(copies, [ID], 'same-tick stale copy cannot cross scope')
    await settle(); assert.equal(alert(w).exists(), false)
    const count = readCount(); retry(); oldRetry.click(); oldCopy.click(); resolveCopy(); await settle()
    assert.equal(readCount(), count); assert.deepEqual(copies, [ID], 'retired native copy cannot copy into new owner')
    assert.ok(!document.body.textContent.includes(ID)); assert.ok(!document.body.textContent.includes('已复制')); assertNoWrites()
    if (kind === 'users' && retirement === 'role') { w.vm.handleUserList(); w.vm.openCreateModal(); w.vm.onDelete(81); assert.equal(readCount(), count); assertNoWrites() }
  } finally { Object.defineProperty(navigator, 'clipboard', { configurable: true, value: oldClipboard }) }
})

for (const kind of ['users', 'tokens']) test(kind + ': late old-owner failure cannot replace current diagnostic or busy state', async () => {
  const w = await page(kind); hold = true; load(w, kind); const old = pending.at(-1)
  user.setStoredCurrentUser(OTHER); await settle(); const current = pending.at(-1)
  current.resolve(failure()); await settle(); const text = alert(w).text()
  old.reject(captureTransportRequestError(Object.assign(new Error(SECRET), { response: { status: 503, headers: { 'x-request-id': OLD_ID } } }))); await settle()
  assert.equal(alert(w).text(), text); assert.ok(text.includes(ID)); assert.ok(!text.includes(OLD_ID)); assertNoWrites()
})
