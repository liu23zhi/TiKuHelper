/* Native Blacklist (BL-5) regression: real Vue 3 / Ant Design Vue 4 / currentUser helpers.
 * Mock boundaries are the deferred BlacklistApi module and Swal only; axios and the
 * non-mocked parts of the request wrapper stay real. All markers are synthetic.
 * No network, no UAPI, no database.
 */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(process.env.UI_TEST_HELPER || path.join(root, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createRouter, createMemoryHistory, RouterView } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))

const A = { id: 74601, role: 'admin', username: 'SYNTHETIC_BLACKLIST_A', balance: 1 }
const B = { id: 74602, role: 'admin', username: 'SYNTHETIC_BLACKLIST_B', balance: 2 }

const names = ['getBlacklistEntries', 'createBlacklistEntry', 'setBlacklistEntryEnabled', 'deleteBlacklistEntry', 'getBlacklistHits']
const calls = Object.fromEntries(names.map(name => [name, []]))
const swals = [], notices = [], errors = []
let wrappers = [], histories = [], deferSwal = false, popup = null

const clone = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
const ok = data => ({ status: 200, data })
const created = data => ({ status: 201, data })

function deferred() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject, completed: false }
}

function transport(name, payload) {
  const call = { ...deferred(), actor: user.getStoredCurrentUser()?.id, payload: clone(payload) }
  const resolve = call.resolve, reject = call.reject
  call.resolve = value => { call.completed = true; resolve(value) }
  call.reject = value => { call.completed = true; reject(value) }
  calls[name].push(call)
  return call.promise
}

// Real BlacklistApi module is replaced by a recorder that keeps the same call shapes.
const apiPath = path.join(root, 'src/api/BlacklistApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getBlacklistEntries: payload => transport('getBlacklistEntries', payload),
  createBlacklistEntry: payload => transport('createBlacklistEntry', payload),
  setBlacklistEntryEnabled: (id, enabled) => transport('setBlacklistEntryEnabled', { id, enabled }),
  deleteBlacklistEntry: id => transport('deleteBlacklistEntry', id),
  getBlacklistHits: payload => transport('getBlacklistHits', payload),
}
require.cache[apiPath] = api

const swalPath = require.resolve('sweetalert2')
const swal = new Module(swalPath)
swal.loaded = true
swal.exports = {
  fire(options) {
    popup = document.createElement('div')
    const pending = { ...deferred(), options, popup }
    swals.push(pending)
    if (!deferSwal) { pending.completed = true; pending.resolve({ isConfirmed: true }) }
    return pending.promise
  },
  getPopup: () => popup,
  close: () => { popup = null },
  isVisible: () => Boolean(popup),
}
require.cache[swalPath] = swal

for (const kind of ['success', 'error', 'warning']) {
  const original = Antd.message[kind].bind(Antd.message)
  Antd.message[kind] = (...args) => { notices.push({ kind, text: String(args[0]?.content || args[0] || '') }); return original(...args) }
}

const Page = require(path.join(root, 'src/pages/Blacklist/index.vue'))

// The server never returns `pattern` to the browser in the list payload the UI renders;
// the fixture deliberately also carries a sentinel `pattern` field so a regression that
// starts rendering it fails loudly.
const entrySeason = (tag = 'A', overrides = {}) => ({
  id: tag === 'A' ? 1 : 2,
  kind: 'ip',
  pattern: `SYNTHETIC_PATTERN_${tag}`,
  valueMasked: `SYNTHETIC_MASKED_${tag}`,
  valueHash: `SYNTHETIC_HASH_${tag}`,
  matchType: 'exact',
  reason: `SYNTHETIC_REASON_${tag}`,
  enabled: true,
  expiresAt: null,
  createdBy: 74601,
  createdAt: '2026-11-01T00:00:00Z',
  updatedAt: '2026-11-01T00:00:00Z',
  ...overrides,
})

const hitSeason = (tag = 'A', overrides = {}) => ({
  id: tag === 'A' ? 91 : 92,
  entryId: 1,
  kind: 'ip',
  valueMasked: `SYNTHETIC_HIT_MASKED_${tag}`,
  clientIp: `198.51.100.${tag === 'A' ? 11 : 12}`,
  path: `/adapter-service/SYNTHETIC_PATH_${tag}`,
  action: 'blocked',
  hitCount: 7,
  createdAt: '2026-11-02T00:00:00Z',
  ...overrides,
})

const entryListing = (rows = [entrySeason()], extra = {}) => ok({
  data: rows, total: rows.length, page: 1, pageSize: 20, ...extra,
})
const hitListing = (rows = [hitSeason()], extra = {}) => ok({
  data: rows, total: rows.length, page: 1, pageSize: 20, ...extra,
})

async function settle() { await nextTick(); await flushPromises(); await nextTick() }

function last(name = names[0]) {
  const call = calls[name].at(-1)
  assert.ok(call, `ACTUAL_API_${name}`)
  return call
}

function button(w, label) {
  return w.findAll('button').find(b => b.text().replace(/\s/g, '') === label.replace(/\s/g, ''))
}

function click(w, label) {
  const target = button(w, label)
  assert.ok(target, `NATIVE_BUTTON_${label}`)
  target.element.click()
}

function state(w) {
  return clone({
    entries: w.vm.entries,
    hits: w.vm.hits,
    entriesTotal: w.vm.entriesTotal,
    hitsTotal: w.vm.hitsTotal,
    activeTab: w.vm.activeTab,
    createVisible: w.vm.createVisible,
    draft: w.vm.draft,
    entryFilters: w.vm.entryFilters,
    hitFilters: w.vm.hitFilters,
    appliedEntryFilters: w.vm.appliedEntryFilters,
    appliedHitFilters: w.vm.appliedHitFilters,
    entryError: w.vm.entryError,
    hitError: w.vm.hitError,
    createError: w.vm.createError,
    loadingEntries: w.vm.loadingEntries,
    loadingHits: w.vm.loadingHits,
    writing: w.vm.writing,
  })
}

async function page(actor = A, loaded = true) {
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/adapter/blacklist', component: Page },
      { path: '/elsewhere', component: { render: () => h('p', 'SYNTHETIC_OTHER_ROUTE') } },
    ],
  })
  await router.push('/adapter/blacklist')
  await router.isReady()
  const host = mount({ render: () => h(RouterView) }, {
    attachTo: document.body,
    global: {
      plugins: [router, Antd],
      config: { errorHandler: error => errors.push(String(error?.stack || error)) },
    },
  })
  wrappers.push(host)
  histories.push(router.options.history)
  await settle()
  const w = host.findComponent(Page)
  if (loaded && calls.getBlacklistEntries.length) {
    last('getBlacklistEntries').resolve(entryListing())
    await settle()
  }
  return { w, host, router }
}

function finishEntries(rows = [entrySeason('B')], extra = {}) {
  const call = calls.getBlacklistEntries.at(-1)
  if (call && !call.completed) call.resolve(entryListing(rows, extra))
}

afterEach(async () => {
  for (const host of wrappers) if (host.exists()) host.unmount()
  wrappers = []
  for (const name of names) {
    for (const call of calls[name]) {
      if (call.completed) continue
      call.resolve(name === 'getBlacklistEntries' ? entryListing([]) : name === 'getBlacklistHits' ? hitListing([]) : ok({}))
    }
  }
  for (const call of swals) if (!call.completed) { call.completed = true; call.resolve({ isDismissed: true }) }
  await settle()
  for (const history of histories) history.destroy()
  histories = []
  Antd.message.destroy()
  await settle()
  user.clearStoredCurrentUser()
  document.body.replaceChildren()
  window.localStorage.clear()
  window.sessionStorage.clear()
  names.forEach(name => { calls[name].length = 0 })
  swals.length = 0; notices.length = 0; errors.length = 0
  deferSwal = false; popup = null
})

test('BL_LIST renders masked rows with reason, kind label and no raw pattern', async () => {
  const { w } = await page(A, false)
  assert.deepEqual(last('getBlacklistEntries').payload, { page: 1, pageSize: 20 })
  last('getBlacklistEntries').resolve(entryListing([
    entrySeason('A', { kind: 'ip', matchType: 'exact' }),
    entrySeason('B', {
      id: 2, kind: 'phone', matchType: 'exact', valueMasked: '138****2222',
      reason: 'SYNTHETIC_PHONE_REASON', pattern: 'SYNTHETIC_PHONE_PATTERN',
    }),
  ], { total: 2 }))
  await settle()

  assert.equal(w.findAll('.ant-table-tbody .ant-table-row').length, 2)
  const text = w.text()
  assert.ok(text.includes('SYNTHETIC_MASKED_A'), 'masked value must be rendered')
  assert.ok(text.includes('SYNTHETIC_REASON_A'), 'reason must be rendered')
  assert.ok(text.includes('138****2222'), 'phone masked value must be rendered')
  assert.ok(text.includes('手机号'), 'kind label must be rendered')
  assert.equal(w.vm.entriesTotal, 2)

  // 46-*.md §3.2：**绝不渲染 Pattern 原文**。fixture 里刻意塞了 pattern 字段，
  // 任何把整行 dump 出来的改动都会让这两条断言变红。
  assert.equal(text.includes('SYNTHETIC_PATTERN_A'), false, 'raw pattern must never reach the DOM')
  assert.equal(text.includes('SYNTHETIC_PHONE_PATTERN'), false, 'raw pattern must never reach the DOM')
  assert.equal(text.includes('SYNTHETIC_HASH_A'), false, 'valueHash must never reach the DOM')

  console.log('BLACKLIST_LIST_OBSERVED ' + JSON.stringify({
    rows: w.findAll('.ant-table-tbody .ant-table-row').map(r => r.text()),
    total: w.vm.entriesTotal,
    errors,
  }))
})

test('BL_CREATE submits camelCase payload with untouched raw pattern and closes on success', async () => {
  const { w } = await page()
  click(w, '新增规则')
  await settle()
  assert.equal(w.vm.createVisible, true)

  // 直接驱动表单模型：真实控件已经挂载（Modal 已渲染），
  // 但输入框定位依赖 Ant Design 的 v-model 事件，这里用组件实例保证稳定。
  w.vm.draft.kind = 'ip'
  w.vm.draft.pattern = '  203.0.113.0/24  '
  w.vm.draft.matchType = ''
  w.vm.draft.reason = '  SYNTHETIC_CREATE_REASON  '
  w.vm.draft.expiresAt = '2026-12-31'
  w.vm.draft.enabled = true
  await settle()
  w.vm.submitCreate()
  await settle()

  const payload = last('createBlacklistEntry').payload
  assert.deepEqual(payload, {
    kind: 'ip',
    // pattern 只做 trim：归一化（CIDR 主机位清零、IPv6 收敛）一律由后端做，
    // 前端预归一化会让「哪一层算错了」无法分辨。
    pattern: '203.0.113.0/24',
    matchType: '',
    reason: 'SYNTHETIC_CREATE_REASON',
    expiresAt: '2026-12-31',
    enabled: true,
  }, 'create payload must be camelCase and carry the raw pattern')

  last('createBlacklistEntry').resolve(created(entrySeason('NEW', { id: 33 })))
  await settle()
  assert.equal(w.vm.createVisible, false, 'modal must close after successful create')
  assert.ok(calls.getBlacklistEntries.length >= 2, 'a successful create must reload the list')
  finishEntries([entrySeason('AFTER_CREATE', { id: 33 })])
  await settle()
  assert.ok(w.text().includes('SYNTHETIC_MASKED_AFTER_CREATE'))
  assert.ok(notices.some(n => n.kind === 'success'), 'create success must toast')
})

test('BL_CREATE rejects blank pattern or reason locally without any transport call', async () => {
  const { w } = await page()
  const before = calls.createBlacklistEntry.length
  click(w, '新增规则')
  await settle()

  w.vm.draft.pattern = '   '
  w.vm.draft.reason = 'SYNTHETIC_REASON'
  w.vm.submitCreate()
  await settle()
  assert.equal(calls.createBlacklistEntry.length, before, 'blank pattern must not hit the API')
  assert.ok(w.vm.createError)
  assert.equal(w.vm.createVisible, true, 'validation failure must keep the modal open')

  w.vm.draft.pattern = '203.0.113.7'
  w.vm.draft.reason = '   '
  w.vm.submitCreate()
  await settle()
  assert.equal(calls.createBlacklistEntry.length, before, 'blank reason must not hit the API')
  assert.ok(w.vm.createError)
  assert.equal(w.vm.createVisible, true)
})

test('BL_CREATE surfaces the server 400 message instead of a generic failure', async () => {
  const { w } = await page()
  click(w, '新增规则')
  await settle()
  w.vm.draft.pattern = 'SYNTHETIC_NOT_AN_IP'
  w.vm.draft.reason = 'SYNTHETIC_REASON'
  w.vm.submitCreate()
  await settle()
  last('createBlacklistEntry').resolve({
    status: 400,
    data: { message: '黑名单匹配模式不合法：SYNTHETIC_NOT_AN_IP 不是合法 IP' },
  })
  await settle()
  assert.ok(w.vm.createError.includes('不是合法 IP'), 'server detail must be shown, not swallowed')
  assert.equal(w.vm.createVisible, true, 'failed create must keep the modal open')
})

test('BL_TOGGLE disables through the enabled endpoint and reloads the list', async () => {
  const { w } = await page()
  const toggle = w.findAllComponents(Antd.Switch)[0]
  assert.ok(toggle, 'NATIVE_SWITCH')
  assert.equal(toggle.props('checked'), true)
  w.vm.toggleEnabled(w.vm.entries[0], false)
  await settle()

  assert.deepEqual(last('setBlacklistEntryEnabled').payload, { id: 1, enabled: false },
    'toggle must send the explicit target state')
  last('setBlacklistEntryEnabled').resolve(ok(entrySeason('A', { enabled: false })))
  await settle()
  assert.ok(calls.getBlacklistEntries.length >= 2, 'successful toggle must reload the list')
  finishEntries([entrySeason('A', { enabled: false })])
  await settle()
  assert.equal(w.vm.entries[0].enabled, false)
  assert.ok(notices.some(n => n.text.includes('已停用')))
})

test('BL_DELETE requires an explicit Swal confirmation before any transport call', async () => {
  const { w } = await page()
  deferSwal = true
  const before = calls.deleteBlacklistEntry.length
  click(w, '删除')
  await settle()

  const prompt = swals.at(-1)
  assert.ok(prompt, 'ACTUAL_EXPLICIT_DELETE_CONFIRMATION')
  assert.ok(/确认删除/.test(prompt.options.title))
  assert.equal(prompt.options.showCancelButton, true)
  assert.equal(calls.deleteBlacklistEntry.length, before, 'delete must not fire before confirmation')

  // 取消 → 零写。
  prompt.completed = true
  prompt.resolve({ isDismissed: true })
  await settle()
  assert.equal(calls.deleteBlacklistEntry.length, before, 'cancelled confirm must not delete')

  // 确认 → 发一次删除，id 正确。
  click(w, '删除')
  await settle()
  const confirmed = swals.at(-1)
  confirmed.completed = true
  confirmed.resolve({ isConfirmed: true })
  await settle()
  assert.equal(calls.deleteBlacklistEntry.length, before + 1)
  assert.equal(last('deleteBlacklistEntry').payload, 1, 'delete must target the row id')

  last('deleteBlacklistEntry').resolve(ok({ message: 'ok', id: 1 }))
  await settle()
  assert.ok(calls.getBlacklistEntries.length >= 2, 'successful delete must reload the list')
  finishEntries([])
  await settle()
  assert.equal(w.vm.entries.length, 0)
})

test('BL_HITS tab loads only when opened and orders payload by explicit filters', async () => {
  const { w } = await page()
  // 进页面只读规则列表，命中记录一次都不读。
  assert.equal(calls.getBlacklistHits.length, 0, 'hits must not be read before the tab is opened')

  w.vm.activeTab = 'hits'
  await settle()
  assert.equal(calls.getBlacklistHits.length, 1)
  assert.deepEqual(last('getBlacklistHits').payload, { page: 1, pageSize: 20 })
  last('getBlacklistHits').resolve(hitListing([
    hitSeason('A', { action: 'blocked', hitCount: 7 }),
    hitSeason('B', { id: 92, action: 'logged_only', hitCount: 1, entryId: 2 }),
  ], { total: 2 }))
  await settle()
  const text = w.text()
  assert.ok(text.includes('198.51.100.11'))
  assert.ok(text.includes('/adapter-service/SYNTHETIC_PATH_A'))
  assert.ok(text.includes('已拦截'))
  assert.ok(text.includes('仅留痕'))
  assert.equal(w.vm.hitsTotal, 2)

  // 规则行上的「命中记录」必须带 entryId 筛选，而不是只切 tab。
  w.vm.activeTab = 'entries'
  await settle()
  w.vm.openHitsFor({ id: 2 })
  await settle()
  assert.equal(w.vm.activeTab, 'hits')
  assert.deepEqual(last('getBlacklistHits').payload, { page: 1, pageSize: 20, entryId: '2' },
    'row-level hits action must scope the query to that rule')
})

test('BL_HITS trailing DOM order is newest-first and hitCount is rendered verbatim', async () => {
  const { w } = await page()
  w.vm.activeTab = 'hits'
  await settle()
  // 后端按 created_at desc 返回；前端必须原样保留顺序，不做任何 re-sort。
  last('getBlacklistHits').resolve(hitListing([
    hitSeason('NEWEST', { id: 93, hitCount: 42, createdAt: '2026-11-03T00:00:00Z' }),
    hitSeason('OLDER', { id: 91, hitCount: 1, createdAt: '2026-11-01T00:00:00Z' }),
  ], { total: 2 }))
  await settle()
  // 两个 tab 的表格同时挂在 DOM 里（Ant Design 的 tab 默认不销毁未激活面板），
  // 所以必须按 `.hits-table` 定位，不能用全局 `.ant-table-row` 数行数。
  const hitsTable = w.find('.hits-table')
  assert.ok(hitsTable.exists(), 'ACTUAL_HITS_TABLE_SCOPE')
  const rows = hitsTable.findAll('.ant-table-tbody .ant-table-row').map(r => r.text())
  assert.equal(rows.length, 2)
  assert.ok(rows[0].includes('42'), 'newest row must stay first with its aggregated hitCount')
  assert.ok(rows[1].includes('2026/11/1') || rows[1].includes('2026/11/01'), 'older row must stay second')
  assert.deepEqual(clone(w.vm.hits.map(h => h.id)), [93, 91], 'DOM order must follow server order')
})

test('BL_LIST keeps last successful rows and shows an error when a refresh fails', async () => {
  const { w } = await page()
  const retained = clone(w.vm.entries)
  w.vm.loadEntries()
  await settle()
  last('getBlacklistEntries').reject({ response: { status: 503, data: { message: 'SYNTHETIC_RAW_SECRET' } } })
  await settle()
  // 失败不能把已知行清空成「无数据」——那会让运营以为规则被清了。
  assert.deepEqual(clone(w.vm.entries), retained, 'known rows must survive a failed refresh')
  assert.ok(w.vm.entryError)
  assert.ok(w.find('.ant-alert-error').exists())
  assert.equal(w.vm.entryError.includes('SYNTHETIC_RAW_SECRET'), false, 'raw server text must not leak')
  assert.equal(w.vm.loadingEntries, false)
})

test('BL_PRINCIPAL no actor or non-admin performs zero private read and zero write', async () => {
  for (const actor of [null, { id: 74603, role: 'user', username: 'SYNTHETIC_PLAIN' }]) {
    const { w, host } = await page(actor, false)
    assert.equal(calls.getBlacklistEntries.length, 0, 'UNAUTHORIZED_ZERO_PRIVATE_READ')
    w.vm.loadEntries(); w.vm.loadHits(); w.vm.openCreate(); w.vm.reloadEntries(); w.vm.activeTab = 'hits'
    await settle()
    assert.equal(calls.getBlacklistEntries.length, 0)
    assert.equal(calls.getBlacklistHits.length, 0)
    assert.equal(calls.createBlacklistEntry.length, 0)
    assert.equal(w.vm.createVisible, false)
    host.unmount()
  }
})

test('BL_PRINCIPAL actor switch retires pending reads without publishing late rows', async () => {
  const { w } = await page(A, false)
  const pendingA = last('getBlacklistEntries')
  user.setStoredCurrentUser(B)
  await settle()
  assert.deepEqual(clone(w.vm.entries), [], 'SYNC_CLEAR_PRIVATE_ROWS')
  assert.equal(calls.getBlacklistEntries.length, 2, 'new actor must trigger a fresh read')
  const pendingB = last('getBlacklistEntries')
  // 采集 B 的「读未完成」快照：迟到的 A 响应必须让整份状态**逐字段不变**，
  // 不只是 entries 为空 —— 否则迟到响应仍可能污染 total / 筛选 / 错误态。
  const beforeLateA = state(w)

  pendingA.resolve(entryListing([entrySeason('A_LATE')], { total: 900 }))
  await settle()
  assert.equal(w.vm.entries.length, 0, 'retired read must not publish rows')
  assert.equal(w.vm.entriesTotal, 0)
  assert.deepEqual(state(w), beforeLateA, 'RETIRED_READ_MUST_NOT_CHANGE_ANY_STATE')

  pendingB.resolve(entryListing([entrySeason('B_CURRENT')]))
  await settle()
  assert.ok(w.text().includes('SYNTHETIC_MASKED_B_CURRENT'))
  assert.equal(document.body.textContent.includes('SYNTHETIC_MASKED_A_LATE'), false)
})
