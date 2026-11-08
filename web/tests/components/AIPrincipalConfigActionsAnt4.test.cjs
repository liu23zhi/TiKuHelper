const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs'), path = require('node:path'), Module = require('node:module'), crypto = require('node:crypto')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
// Use the helper's pseudo-element-free native jsdom style surface.
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, defineComponent, h, toRaw } = require('vue')
const { createRouter, createMemoryHistory, RouterView, RouterLink } = require('vue-router')
const Antd = require('ant-design-vue')
const zhCN = require('ant-design-vue/lib/locale/zh_CN').default
const user = require(path.join(web, 'src/utils/currentUser.js'))
const ADMIN_A = { id: 3701, username: 'SYNTHETIC_ADMIN_A', role: 'admin', status: 'enabled', balance: 1 }
const ADMIN_B = { id: 3702, username: 'SYNTHETIC_ADMIN_B', role: 'admin', status: 'enabled', balance: 2 }
const CONFIG = { enabled: true, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC_KEY_ONLY', model: 'MODEL-A', backupModel: '', manualModels: ['MODEL-A'], prompt: 'SYNTHETIC_PROMPT', remark: '', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0.000062, tiku_search_price_per_call: 0.000061, maxAIWebReads: 5, webReadPricePerCall: 1.234567, imageOCRPricePerCall: 0, enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: true, stagedTokenMultiplierPoints: [{ tokens: '0', multiplier: 2 }, { tokens: '1000', multiplier: 1 }] }
const ok = data => ({ status: 200, data })
const pending = { config: [], save: [], models: [], engines: [], usage: [], test: [] }
const calls = [], notices = [], confirms = [], traps = [], wrappers = []
let restoreStorage = null
function deferred(lane, payload) {
  const record = { lane, payload: structuredClone(payload), settled: false }
  calls.push({ lane, payload: record.payload })
  record.promise = new Promise((resolve, reject) => {
    record.resolve = value => { record.settled = true; resolve(value) }
    record.reject = value => { record.settled = true; reject(value) }
  })
  pending[lane].push(record)
  return record.promise
}
function forbidden(name) { return (...args) => { traps.push({ name, count: args.length }); throw new Error('FORBIDDEN_SYNTHETIC_BOUNDARY_' + name) } }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
// Upstream parent mounts with one readonly OCR status GET; never clear/write/provider.
let ocrStatusReads = 0
function syntheticOCRStatus(feedback) {
  assert.deepEqual(feedback, { errorFeedback: 'local' }, 'exact readonly OCR feedback contract')
  ocrStatusReads += 1
  return Promise.resolve({ status: 200, data: { configured: false, entries: 0 } })
}
require('node:test').after(() => console.log('AI_OCR_STATUS_READS ' + JSON.stringify({ fixture: __filename, method: 'GET', count: ocrStatusReads, configured: false, entries: 0 })))
api.exports = new Proxy({ getAIOCRCacheStatus: syntheticOCRStatus,
  getAISearchConfig: () => deferred('config', null),
  updateAISearchConfig: payload => deferred('save', payload),
  getAISearchModels: payload => deferred('models', payload),
  testAISearchConfig: payload => deferred('test', payload),
  getUAPISearchEngines: () => deferred('engines', null),
  getUAPIUsageStatus: () => deferred('usage', null),
  getAIModelPrices: () => Promise.resolve(ok([{ id: 50, modelName: 'MODEL-A', displayName: 'Fixture', inputPrice: 0, outputPrice: 0, multiplier: 1 }])),
}, { get(target, key) { if (key === '__esModule') return false; return key in target ? target[key] : forbidden(String(key)) } })
require.cache[apiPath] = api
// No provider, network, clipboard or other API write is authorized by this fixture.
globalThis.fetch = forbidden('fetch')
window.fetch = forbidden('window.fetch')
window.XMLHttpRequest.prototype.open = forbidden('XMLHttpRequest.open')
Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: forbidden('clipboard.writeText'), readText: forbidden('clipboard.readText') } })
for (const name of ['request', 'get']) for (const protocol of ['node:http', 'node:https']) require(protocol)[name] = forbidden(protocol + '.' + name)
for (const level of ['success', 'error', 'warning', 'info']) {
  const original = Antd.message[level]
  Antd.message[level] = (...args) => { notices.push({ level, content: args[0] }); return original(...args) }
}
const originalConfirm = Antd.Modal.confirm
Antd.Modal.confirm = options => {
  const instance = originalConfirm(options)
  const record = { options, instance, destroys: 0 }
  const destroy = instance.destroy
  instance.destroy = (...args) => { record.destroys++; return destroy(...args) }
  confirms.push(record)
  return instance
}
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue')
const Page = require(pagePath)
console.log('AI_PRINCIPAL_CONFIG_SOURCE ' + JSON.stringify({ web, node: process.version, files: [pagePath, path.join(web, 'src/utils/useUnsavedDraft.js'), path.join(web, 'src/utils/currentUser.js')].map(file => ({ file, sha256: crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex') })) }))
const clean = value => String(value || '').replace(/\s+/g, '')
const clone = value => JSON.parse(JSON.stringify(toRaw(value)))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
function button(root, label) {
  const node = [...root.querySelectorAll('button')].find(item => clean(item.textContent) === clean(label))
  assert.ok(node, 'real native button: ' + label)
  return node
}
async function click(root, label) { button(root, label).click(); await settle() }
function field(w, name) {
  const node = w.element.querySelector('input[id$="-' + name + '"],textarea[id$="-' + name + '"]')
  assert.ok(node && ['INPUT', 'TEXTAREA'].includes(node.tagName), 'real native field: ' + name)
  return node
}
async function writeElement(node, value) { node.value = String(value); for (const type of ['input', 'change', 'blur']) node.dispatchEvent(new Event(type, { bubbles: true })); await settle() }
async function input(w, name, value) { await writeElement(field(w, name), value) }
const pageState = w => ({ form: clone(w.vm.formState), saved: w.vm.savedConfigSnapshot, loading: w.vm.tabLoading, saving: w.vm.saving, fetching: w.vm.fetchingModels, capabilityBusy: w.vm.searchEngineLoading, capabilityOpen: w.vm.capabilityModalVisible, capabilityError: w.vm.capabilityError, engine: clone(w.vm.searchEngineInfo), usage: clone(w.vm.usageInfo) })
function checks(label) {
  const failures = []
  return {
    equal(actual, expected, name) { try { assert.deepEqual(actual, expected) } catch { failures.push({ name, actual, expected }) } },
    finish(w, extra = {}) {
      console.log('AI_PRINCIPAL_CONFIG_LEDGER ' + JSON.stringify({ label, failures, calls, notices, confirms: confirms.map(item => ({ title: item.options.title, destroys: item.destroys })), traps, state: w ? pageState(w) : null, ...extra }))
      assert.equal(failures.length, 0, label + ': ' + JSON.stringify(failures))
    },
  }
}
function latest(lane, exclude) { return pending[lane].filter(item => !item.settled && item !== exclude).at(-1) }
function finish(record, outcome, value = { ...CONFIG, model: 'SYNTHETIC_STALE_A' }) {
  assert.ok(record, 'synthetic issued API promise exists')
  if (outcome === 'reject') record.reject(new Error('SYNTHETIC_STALE_A_SECRET'))
  else if (outcome === 'failure') record.resolve({ status: 409, message: 'SYNTHETIC_STALE_A_SECRET' })
  else record.resolve(ok(structuredClone(value)))
}
async function start({ deferConfig = false, actor = ADMIN_A, router = false, blocked = false } = {}) {
  const ocrReadStart = ocrStatusReads
  if (actor) user.setStoredCurrentUser(actor); else user.clearStoredCurrentUser()
  if (blocked) blockStorage()
  let r, root
  if (router) {
    r = createRouter({ history: createMemoryHistory(), routes: [{ path: '/ai', component: Page }, { path: '/away', component: { template: '<p>SYNTHETIC_DESTINATION</p>' } }] })
    await r.push('/ai'); await r.isReady()
    const Host = defineComponent({ setup: () => () => h(Antd.ConfigProvider, { locale: zhCN }, { default: () => [h(RouterLink, { to: '/away' }, { default: () => 'SYNTHETIC_LEAVE' }), h(RouterView)] }) })
    root = mount(Host, { attachTo: document.body, global: { plugins: [Antd, r] } })
  } else {
    const Host = defineComponent({ setup: () => () => h(Antd.ConfigProvider, { locale: zhCN }, { default: () => h(Page) }) })
    root = mount(Host, { attachTo: document.body, global: { plugins: [Antd] } })
  }
  wrappers.push(root)
  const w = root.findComponent(Page)
  w.fixtureRoot = root; w.fixtureRouter = r
  await settle()
  if (!deferConfig && latest('config')) { latest('config').resolve(ok(structuredClone(CONFIG))); await settle() }
  assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent')
  return w
}
function blockStorage() {
  const descriptor = Object.getOwnPropertyDescriptor(window, 'localStorage')
  Object.defineProperty(window, 'localStorage', { configurable: true, get() { throw new Error('SYNTHETIC_BLOCKED_CACHE') } })
  restoreStorage = () => { Object.defineProperty(window, 'localStorage', descriptor); restoreStorage = null }
}
async function actorB() { user.setStoredCurrentUser(ADMIN_B); await settle() }
async function completeCurrentConfig(w, model = 'MODEL-B') {
  let read = latest('config')
  if (!read) {
    const count = confirms.length
    await click(w.element.querySelector('.console-page-heading') || w.element, '刷新')
    const currentRefresh = confirms.slice(count).find(item => item.options.title === '有未保存的更改，确认刷新？')
    if (currentRefresh) { currentRefresh.options.onOk(); await settle() }
    read = latest('config')
  }
  if (read) { read.resolve(ok({ ...structuredClone(CONFIG), model })); await settle() }
}
function captured(title) { const value = [...confirms].reverse().find(item => item.options.title === title); assert.ok(value, 'real captured Antd confirmation: ' + title); return value }
function modal(title) { return [...document.querySelectorAll('.ant-modal')].find(node => clean(node.querySelector('.ant-modal-title')?.textContent) === clean(title) && node.style.display !== 'none') }
afterEach(async () => {
  if (restoreStorage) restoreStorage()
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren()
  user.clearStoredCurrentUser()
  for (const lane of Object.keys(pending)) pending[lane].length = 0
  calls.length = notices.length = confirms.length = traps.length = 0
})

const transitions = {
  B: () => user.setStoredCurrentUser(ADMIN_B),
  ABA: () => { user.setStoredCurrentUser(ADMIN_B); user.setStoredCurrentUser(ADMIN_A) },
  role: () => user.setStoredCurrentUser({ ...ADMIN_A, role: 'user' }),
  status: () => user.setStoredCurrentUser({ ...ADMIN_A, status: 'disabled' }),
  logout: () => user.clearStoredCurrentUser(),
  unmount: w => { w.fixtureRoot.unmount(); wrappers.splice(wrappers.indexOf(w.fixtureRoot), 1) },
}
for (const [transition, change] of Object.entries(transitions)) for (const outcome of ['success', 'failure', 'reject']) {
  test('native config read ' + transition + ' suppresses stale ' + outcome + ' form snapshot busy and notice', async () => {
    const w = await start({ deferConfig: true }), a = latest('config'), c = checks('read-' + transition + '-' + outcome)
    change(w); await settle()
    const before = pageState(w); notices.length = 0
    finish(a, outcome); await settle()
    c.equal(pageState(w), before, 'old success/error/finally may not publish after retirement')
    c.equal(notices, [], 'old read may not toast'); c.equal(traps, [], 'forbidden boundaries untouched'); c.finish(w)
  })
}
test('native B refresh owns busy while old A read success is pending', async () => {
  const w = await start({ deferConfig: true }), a = latest('config'), c = checks('read-B-independent')
  await actorB(); await click(w.element.querySelector('.console-page-heading') || w.element, '刷新')
  const b = latest('config', a)
  c.equal(Boolean(b), true, 'B real refresh issues independent GET')
  const before = pageState(w); notices.length = 0
  finish(a, 'success'); await settle(); c.equal(pageState(w), before, 'old A cannot mutate B loading/form/snapshot')
  if (b) { b.resolve(ok({ ...structuredClone(CONFIG), model: 'MODEL-B' })); await settle(); c.equal(w.vm.formState.model, 'MODEL-B', 'current B read renders') }
  c.equal(notices, [], 'stale A no notice'); c.finish(w)
})
for (const mode of ['metadata', 'blocked-cache', 'initial-unknown']) test('legitimate config first read survives ' + mode, async () => {
  const w = await start({ deferConfig: true, actor: mode === 'initial-unknown' ? null : ADMIN_A, blocked: mode === 'initial-unknown' }), c = checks('read-preserved-' + mode), a = latest('config')
  c.equal(Boolean(a), true, 'unknown initial cache preserves original readonly first GET')
  if (mode === 'metadata') user.setStoredCurrentUser({ ...ADMIN_A, balance: 99, username: 'SYNTHETIC_RENAMED', nickname: 'META_ONLY' })
  if (mode === 'blocked-cache') blockStorage()
  user.getStoredCurrentUser(); await settle()
  if (a) { a.resolve(ok({ ...structuredClone(CONFIG), model: 'MODEL-PRESERVED' })); await settle() }
  c.equal(w.vm.formState.model, 'MODEL-PRESERVED', 'same principal or unknown cache retains own result')
  c.equal(w.vm.tabLoading, false, 'own finally releases load'); c.equal(notices, [], 'no false retirement error'); c.equal(pending.config.length, 1, 'metadata no extra GET'); c.finish(w)
})

for (const choice of ['onOk', 'onCancel']) test('native save /v1 ' + choice + ' preserves exact same-owner payload and sync closure', async () => {
  const w = await start(), c = checks('save-positive-' + choice)
  await input(w, 'baseUrl', '  https://fixture.invalid/custom///  ')
  await input(w, 'manualModelsText', 'MODEL-A，MODEL-B\nMODEL-A\n')
  await input(w, 'prompt', 'SYNTHETIC_EXACT_PROMPT')
  const stageRows = w.element.querySelectorAll('.staged-points-editor tbody tr[data-row-key]')
  await writeElement(stageRows[1].querySelector('input'), '9223372036854775807')
  await writeElement(stageRows[1].querySelectorAll('input')[1], '0.123456')
  await click(w.element, '保存配置')
  const confirm = captured('地址未包含 /v1')
  c.equal(typeof confirm.options.onCancel, 'function', '保持原样 remains a save action')
  confirm.options[choice](); await settle()
  const a = latest('save')
  c.equal(Boolean(a), true, 'real save confirmation dispatches authorized synthetic write')
  if (a) {
    c.equal(a.payload, { ...structuredClone(CONFIG), baseUrl: 'https://fixture.invalid/custom' + (choice === 'onOk' ? '/v1' : ''), manualModels: ['MODEL-A', 'MODEL-B'], prompt: 'SYNTHETIC_EXACT_PROMPT', temperature: 0, topP: 0, stagedTokenMultiplierPoints: [{ tokens: '0', multiplier: 2 }, { tokens: '9223372036854775807', multiplier: 0.123456 }], contextCompressionEnabled: true, useRemoteContextCompression: false, contextCompressionOrder: ['server', 'remote', 'local'], contextCompressionTriggerPercent: 80, contextCompressionTargetPercent: 50, contextCompressionMaxAttempts: 3 }, 'exact payload retains staged int64 strings fixed sampling and six-place amounts')
    a.resolve(ok({})); await settle()
    c.equal(w.vm.savedConfigSnapshot, null, 'write success remains sync-pending until actual GET')
    const read = latest('config'); c.equal(Boolean(read), true, 'write success follows with one config GET')
    if (read) { read.resolve(ok(a.payload)); await settle() }
    c.equal(w.vm.hasUnsavedChanges, false, 'same-owner matching draft closes clean only after GET')
    c.equal(w.vm.saving, false, 'same-owner save releases own busy')
  }
  c.equal(traps, [], 'no provider or other write'); c.finish(w)
})
test('native same-owner edited draft survives save completion and server sync', async () => {
  const w = await start(), c = checks('save-new-draft-preserved')
  await input(w, 'prompt', 'SYNTHETIC_SUBMITTED_A'); await click(w.element, '保存配置'); const a = latest('save')
  await input(w, 'prompt', 'SYNTHETIC_NEW_DRAFT_B')
  a.resolve(ok({})); await settle(); const read = latest('config'); if (read) { read.resolve(ok({ ...structuredClone(CONFIG), prompt: 'SYNTHETIC_SUBMITTED_A' })); await settle() }
  c.equal(w.vm.formState.prompt, 'SYNTHETIC_NEW_DRAFT_B', 'save/read cannot overwrite later same-owner draft')
  c.equal(w.vm.hasUnsavedChanges, true, 'new draft stays dirty'); c.finish(w)
})
for (const outcome of ['success', 'failure', 'reject']) test('native A save ' + outcome + ' cannot mark saved unlock or refetch B after B draft and save', async () => {
  const w = await start(), c = checks('save-retired-' + outcome)
  await input(w, 'prompt', 'SYNTHETIC_A_SAVE'); await click(w.element, '保存配置'); const a = latest('save')
  await actorB(); await completeCurrentConfig(w)
  await input(w, 'prompt', 'SYNTHETIC_B_SAVE'); await click(w.element, '保存配置'); const b = latest('save', a)
  c.equal(Boolean(b), true, 'B native save owns an independent request')
  const before = pageState(w), reads = pending.config.length; notices.length = 0
  finish(a, outcome, {}); await settle()
  c.equal(pageState(w), before, 'old A cannot mutate B form saved marker or busy'); c.equal(pending.config.length, reads, 'old A cannot continue B GET'); c.equal(notices, [], 'old A cannot toast')
  if (b) { b.resolve(ok({})); await settle(); const read = latest('config'); if (read) { read.resolve(ok({ ...structuredClone(CONFIG), prompt: 'SYNTHETIC_B_SAVE', model: w.vm.formState.model })); await settle() } c.equal(w.vm.saving, false, 'B own finally releases own busy') }
  c.finish(w)
})
for (const outcome of ['success', 'failure', 'reject']) test('native A save ' + outcome + ' cannot interfere with B pending read and later edited draft', async () => {
  const w = await start(), c = checks('save-retired-B-read-' + outcome)
  await input(w, 'prompt', 'SYNTHETIC_A_SUBMISSION'); await click(w.element, '保存配置'); const a = latest('save')
  await actorB(); await click(w.element.querySelector('.console-page-heading') || w.element, '刷新')
  const currentRefresh = [...confirms].reverse().find(item => item.options.title === '有未保存的更改，确认刷新？')
  if (currentRefresh) { currentRefresh.options.onOk(); await settle() }
  const b = latest('config'); c.equal(Boolean(b), true, 'B current native refresh dispatches own GET')
  await input(w, 'prompt', 'SYNTHETIC_B_EDIT_DURING_READ')
  const before = pageState(w), reads = pending.config.length; notices.length = 0
  finish(a, outcome, {}); await settle()
  c.equal(pageState(w), before, 'retired A save cannot alter B dirty snapshot read busy or form'); c.equal(pending.config.length, reads, 'retired A save cannot initiate an extra B GET'); c.equal(notices, [], 'retired A no notice')
  if (b) { b.resolve(ok({ ...structuredClone(CONFIG), prompt: 'SYNTHETIC_B_SERVER' })); await settle(); c.equal(w.vm.formState.prompt, 'SYNTHETIC_B_EDIT_DURING_READ', 'B own read still preserves later native draft'); c.equal(w.vm.hasUnsavedChanges, true, 'B new native draft stays dirty') }
  c.finish(w)
})
for (const choice of ['onOk', 'onCancel']) for (const scenario of ['actor', 'draft']) test('captured real /v1 ' + choice + ' cannot borrow later ' + scenario + ' fields', async () => {
  const w = await start(), c = checks('confirm-' + choice + '-' + scenario)
  await input(w, 'baseUrl', 'https://fixture.invalid/original'); await input(w, 'prompt', 'SYNTHETIC_CONFIRM_A'); await click(w.element, '保存配置')
  const confirm = captured('地址未包含 /v1')
  const unrelated = Antd.Modal.confirm({ title: 'SYNTHETIC_UNRELATED_B', content: 'B owned modal must remain', onOk: () => {} })
  const unrelatedRecord = captured('SYNTHETIC_UNRELATED_B')
  if (scenario === 'actor') { await actorB(); await completeCurrentConfig(w) }
  await input(w, 'baseUrl', 'https://fixture.invalid/later-B'); await input(w, 'prompt', 'SYNTHETIC_LATER_B')
  if (scenario === 'actor') c.equal(confirm.destroys > 0, true, 'owned confirmation already closed before captured callback replay')
  notices.length = 0; confirm.options[choice](); await settle()
  const saves = calls.filter(item => item.lane === 'save')
  if (scenario === 'actor') c.equal(saves.length, 0, 'old actor confirmation cannot save as B')
  else c.equal(saves.some(item => item.payload.prompt === 'SYNTHETIC_LATER_B'), false, 'old same-owner confirmation cannot read new fields')
  c.equal(unrelatedRecord.destroys, 0, 'retirement cannot destroy unrelated B modal')
  if (scenario === 'actor') c.equal(confirm.destroys > 0, true, 'old owned confirmation closes on actor retirement')
  if (scenario === 'actor') c.equal(notices, [], 'retired actor callback cannot toast')
  else c.equal(notices.every(item => item.level === 'warning' && item.content === '配置草稿已变化，请重新确认后保存'), true, 'same-owner changed-draft callback may only request reconfirmation with fixed warning')
  c.finish(w); unrelated.destroy()
})
test('captured real dirty-refresh confirmation cannot start B reads or lose B draft', async () => {
  const w = await start(), c = checks('refresh-confirm-retired')
  await input(w, 'prompt', 'SYNTHETIC_DIRTY_A'); await click(w.element.querySelector('.console-page-heading') || w.element, '刷新')
  const confirm = captured('有未保存的更改，确认刷新？')
  await actorB(); await completeCurrentConfig(w); await input(w, 'prompt', 'SYNTHETIC_KEEP_B')
  c.equal(confirm.destroys > 0, true, 'owned dirty refresh already closed at actor retirement')
  const before = pageState(w), count = calls.length; notices.length = 0
  confirm.options.onOk(); await settle()
  c.equal(calls.length, count, 'old refresh confirmation cannot invoke B actions'); c.equal(pageState(w), before, 'old refresh cannot change B draft or busy'); c.equal(confirm.destroys > 0, true, 'owned dirty refresh closes'); c.equal(notices, [], 'no stale toast'); c.finish(w)
})
for (const choice of ['onOk', 'onCancel']) test('real RouterLink leave ' + choice + ' denies retired navigation while preserving B modal', async () => {
  const w = await start({ router: true }), c = checks('leave-confirm-retired-' + choice), router = w.fixtureRouter
  await input(w, 'prompt', 'SYNTHETIC_DIRTY_A')
  const leave = document.querySelector('a[href="/away"]'); assert.ok(leave, 'real RouterLink navigation control'); leave.click(); await settle()
  const confirm = captured('有未保存的更改，确认离开？')
  const unrelated = Antd.Modal.confirm({ title: 'SYNTHETIC_LEAVE_UNRELATED_B', onOk: () => {} }), other = captured('SYNTHETIC_LEAVE_UNRELATED_B')
  await actorB(); await completeCurrentConfig(w); await input(w, 'prompt', 'SYNTHETIC_B_ROUTE_DRAFT')
  c.equal(confirm.destroys > 0, true, 'owned leave modal already closed before old decision callback')
  confirm.options[choice](); await settle()
  c.equal(router.currentRoute.value.path, '/ai', 'retired leave decision cannot authorize old navigation')
  c.equal(w.vm.formState.prompt, 'SYNTHETIC_B_ROUTE_DRAFT', 'route remains with B draft')
  c.equal(confirm.destroys > 0, true, 'owned leave confirm closes at retirement'); c.equal(other.destroys, 0, 'unrelated B modal untouched'); c.finish(w); unrelated.destroy()
})
for (const outcome of ['success', 'failure', 'reject']) test('native model fetch retired A ' + outcome + ' cannot overwrite B list or unlock B fetch', async () => {
  const w = await start(), c = checks('models-retired-' + outcome)
  await click(w.element, '获取远程模型列表'); const a = latest('models')
  await actorB(); await completeCurrentConfig(w); await input(w, 'baseUrl', 'https://fixture.invalid/B/v1'); await input(w, 'apiKey', 'SYNTHETIC_KEY_B'); await input(w, 'manualModelsText', 'MODEL-B-DRAFT')
  await click(w.element, '获取远程模型列表'); const b = latest('models', a)
  c.equal(Boolean(b), true, 'B model control can issue while A still pending')
  if (b) c.equal(b.payload, { url: 'https://fixture.invalid/B/v1', key: 'SYNTHETIC_KEY_B' }, 'B exact synthetic model request params')
  const before = pageState(w); notices.length = 0; finish(a, outcome, { models: ['SYNTHETIC_STALE_A'] }); await settle()
  c.equal(pageState(w), before, 'old A cannot publish list busy or form'); c.equal(notices, [], 'old A model response cannot toast')
  if (b) { b.resolve(ok({ models: ['MODEL-B', 'MODEL-B', 'image-ignored', 'MODEL-openai-compact'] })); await settle(); c.equal(w.vm.formState.manualModelsText, 'MODEL-B', 'current B preserves filtering/dedup'); c.equal(w.vm.fetchingModels, false, 'current B finally releases') }
  c.finish(w)
})
test('native current same-owner remote models preserve exact params filter and feedback', async () => {
  const w = await start(), c = checks('models-positive')
  await click(w.element, '获取远程模型列表'); const a = latest('models')
  c.equal(a.payload, { url: CONFIG.baseUrl, key: CONFIG.apiKey }, 'same-owner exact model params')
  user.setStoredCurrentUser({ ...ADMIN_A, balance: 99 }); a.resolve(ok({ models: ['MODEL-NEW', 'MODEL-NEW', 'image-test', 'x-openai-compact'] })); await settle()
  c.equal(w.vm.formState.manualModelsText, 'MODEL-NEW', 'same actor metadata does not retire fetch'); c.equal(w.vm.fetchingModels, false, 'own busy releases'); c.equal(notices, [{ level: 'success', content: '已获取 1 个模型，保存后生效' }], 'legitimate feedback preserved'); c.finish(w)
})
for (const scenario of ['actor', 'reopen']) for (const outcome of ['success', 'failure', 'reject']) test('native capability ' + scenario + ' rejects stale ' + outcome + ' from both controlled APIs', async () => {
  const w = await start(), c = checks('capability-' + scenario + '-' + outcome)
  await click(w.element, '查看深度思考能力'); const a = { engines: latest('engines'), usage: latest('usage') }
  if (scenario === 'actor') { await actorB(); await completeCurrentConfig(w) }
  else { const m = modal('深度思考能力'); assert.ok(m, 'real native capability modal'); m.querySelector('button.ant-modal-close').click(); await settle() }
  await click(w.element, '查看深度思考能力'); const b = { engines: latest('engines', a.engines), usage: latest('usage', a.usage) }
  c.equal(Boolean(b.engines && b.usage), true, 'new capability display gets independent two-API lease')
  const before = pageState(w); notices.length = 0
  finish(a.usage, outcome, { endpoints: [{ path: '/api/search/aggregate', count: 777 }] }); await settle()
  finish(a.engines, outcome, { engine: { name: 'SYNTHETIC_STALE_A', available: true }, limits: {} }); await settle()
  c.equal(pageState(w), before, 'old engines usage errors and finally cannot paint reopened B'); c.equal(notices, [], 'old capability no toast')
  if (b.engines && b.usage) { b.usage.resolve(ok({ endpoints: [{ path: '/api/search/aggregate', count: 3 }] })); await settle(); b.engines.resolve(ok({ engine: { name: 'SYNTHETIC_ENGINE_B', display_name: 'SYNTHETIC_ENGINE_B', available: true, features: ['SYNTHETIC_FEATURE'] }, limits: { default: 3, max: 20 } })); await settle(); c.equal(w.vm.searchEngineInfo.engine.name, 'SYNTHETIC_ENGINE_B', 'B current engine result'); c.equal(w.vm.usageInfo.endpoints[0].count, 3, 'B current usage result'); c.equal(w.vm.searchEngineLoading, false, 'B current finally'); c.equal(w.vm.capabilityError, '', 'B current no error') }
  c.finish(w)
})
test('native current capability two-API partial failure retains own successful usage and fixed error', async () => {
  const w = await start(), c = checks('capability-positive-partial')
  await click(w.element, '查看深度思考能力')
  latest('usage').resolve(ok({ endpoints: [{ path: '/api/web/tomarkdown', count: 6 }] })); latest('engines').reject(new Error('SYNTHETIC_NOT_FOR_UI')); await settle()
  c.equal(w.vm.searchEngineInfo, null, 'own failed engine remains empty'); c.equal(w.vm.usageInfo.endpoints[0].count, 6, 'own successful usage preserved'); c.equal(w.vm.searchEngineLoading, false, 'own finally releases'); c.equal(w.vm.capabilityError, '搜索引擎状态获取失败，请检查 UApiPro 密钥或服务状态', 'fixed partial failure reason'); c.equal(traps, [], 'forbidden write/provider/network boundaries untouched'); c.finish(w)
})

function testDialog(w) { return w.findComponent(require(path.join(web, 'src/pages/AISearchConfig/AISearchTestDialog.vue'))) }
function testState(w) { const vm = testDialog(w).vm; return { form: { baseUrl: vm.form.baseUrl, apiKey: vm.form.apiKey, model: vm.form.model, prompt: vm.form.prompt }, busy: vm.submitting, result: clone(vm.result), error: vm.error, open: w.vm.testModalVisible } }
function testResponse(label) { return { endpoint: 'https://fixture.invalid/v1/chat/completions', answers: [label], requestBody: JSON.stringify({ fixture: label }), rawResponse: 'SYNTHETIC_RAW_' + label } }
test('real stale Teleport OK clicked in same actor-switch tick cannot issue even mocked provider API', async () => {
  const w = await start(), c = checks('portal-same-tick-entry')
  await click(w.element, '测试 AI、联网与 OCR 工具'); const m = modal('测试 AI、联网与 OCR 工具'); assert.ok(m, 'real native Teleported test dialog')
  const send = button(m, '发送 hello 测试')
  user.setStoredCurrentUser(ADMIN_B)
  // Deliberately do not await Vue patch: the old portal still owns this real DOM button.
  send.click(); await settle()
  c.equal(pending.test.length, 0, 'synchronously retired old portal cannot invoke provider API wrapper')
  c.equal(traps, [], 'no provider tool or network escaped mocks'); c.finish(w)
})
for (const outcome of ['success', 'failure', 'reject']) test('issued test A ' + outcome + ' cannot publish result error finally or notice into new B test', async () => {
  const w = await start(), c = checks('portal-issued-' + outcome)
  await click(w.element, '测试 AI、联网与 OCR 工具'); await click(modal('测试 AI、联网与 OCR 工具'), '发送 hello 测试'); const a = latest('test')
  c.equal(Boolean(a), true, 'real A send button issued only controlled synthetic mock')
  await actorB(); await completeCurrentConfig(w); await input(w, 'model', 'MODEL-B'); await input(w, 'baseUrl', CONFIG.baseUrl); await input(w, 'apiKey', 'SYNTHETIC_KEY_B')
  await click(w.element, '测试 AI、联网与 OCR 工具'); const bModal = modal('测试 AI、联网与 OCR 工具'); assert.ok(bModal, 'real current B test dialog')
  await click(bModal, '发送 hello 测试'); const b = latest('test', a)
  c.equal(Boolean(b), true, 'B real send issues independent mock')
  const before = testState(w); notices.length = 0; finish(a, outcome, testResponse('SYNTHETIC_STALE_A')); await settle()
  c.equal(testState(w), before, 'old issued A cannot paint B result error or finally busy'); c.equal(notices, [], 'old issued A cannot toast')
  if (b) { b.resolve(ok(testResponse('SYNTHETIC_ANSWER_B'))); await settle(); c.equal(testDialog(w).vm.result.answers, ['SYNTHETIC_ANSWER_B'], 'B current success still renders'); c.equal(testDialog(w).vm.submitting, false, 'B own finally releases') }
  c.equal(traps, [], 'provider network tools other API writes remain trapped'); c.finish(w)
})
test('real same-owner hello dialog remains usable with exact synthetic request and parsed result', async () => {
  const w = await start(), c = checks('portal-positive')
  await click(w.element, '测试 AI、联网与 OCR 工具'); await click(modal('测试 AI、联网与 OCR 工具'), '发送 hello 测试'); const a = latest('test')
  c.equal(a.payload, { baseUrl: CONFIG.baseUrl, apiKey: CONFIG.apiKey, model: CONFIG.model, prompt: CONFIG.prompt }, 'native hello exact payload')
  a.resolve(ok(testResponse('SYNTHETIC_HELLO'))); await settle()
  c.equal(testDialog(w).vm.result.answers, ['SYNTHETIC_HELLO'], 'native result parser preserved'); c.equal(testDialog(w).vm.submitting, false, 'own finally releases'); c.equal(notices, [{ level: 'success', content: 'AI 搜索测试完成' }], 'own completion feedback preserved'); c.equal(traps, [], 'actual provider/network untouched'); c.finish(w)
})
