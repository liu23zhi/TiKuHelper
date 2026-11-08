const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs'), path = require('node:path'), Module = require('node:module'), crypto = require('node:crypto')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick, defineComponent, h, toRaw } = require('vue')
const Antd = require('ant-design-vue')
const zhCN = require('ant-design-vue/lib/locale/zh_CN').default
const user = require(path.join(web, 'src/utils/currentUser.js'))
const { buildAISearchConfigSavePayload } = require(path.join(web, 'src/utils/aiSearchModelHelpers.mjs'))
const ADMIN_A = { id: 4701, role: 'admin', status: 'enabled', balance: 1 }
const ADMIN_B = { id: 4702, role: 'admin', status: 'enabled', balance: 2 }
const CONFIG = { enabled: true, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC_STAGE_KEY', model: 'MODEL-A', manualModels: ['MODEL-A'], prompt: 'SYNTHETIC_STAGE_PROMPT', remark: '', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0.000062, maxAIWebReads: 5, webReadPricePerCall: 1.234567, enableAIStagedTokenBilling: false, smoothAIStagedTokenMultiplier: false, stagedTokenMultiplierPoints: [{ tokens: '0', multiplier: 2 }, { tokens: '1000', multiplier: 1 }] }
const calls = [], traps = [], wrappers = [], notices = []
const ok = data => ({ status: 200, data })
function forbidden(name) { return (...args) => { traps.push({ name, count: args.length }); throw new Error('FORBIDDEN_STAGE_BOUNDARY_' + name) } }
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
// Exactly the new readonly OCR status GET, never clear/write/provider.
let ocrStatusReads = 0
function syntheticOCRStatus(feedback) { assert.deepEqual(feedback, { errorFeedback: 'local' }); ocrStatusReads += 1; return Promise.resolve({ status: 200, data: { configured: false, entries: 0 } }) }
require('node:test').after(() => console.log('AI_OCR_STATUS_READS ' + JSON.stringify({ fixture: __filename, method: 'GET', count: ocrStatusReads, configured: false, entries: 0 })))
api.exports = new Proxy({ getAIOCRCacheStatus: syntheticOCRStatus,
  getAISearchConfig: () => { calls.push('getAISearchConfig'); return Promise.resolve(ok(structuredClone(CONFIG))) },
  getAIModelPrices: () => { calls.push('getAIModelPrices'); return Promise.resolve(ok([])) },
}, { get(target, key) { if (key === '__esModule') return false; return key in target ? target[key] : forbidden(String(key)) } })
require.cache[apiPath] = api
// This fixture authorizes only the two synthetic readonly GETs and a pure payload projection.
globalThis.fetch = forbidden('fetch'); window.fetch = forbidden('window.fetch')
window.XMLHttpRequest.prototype.open = forbidden('XMLHttpRequest.open')
Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: forbidden('clipboard.writeText'), readText: forbidden('clipboard.readText') } })
for (const protocol of ['node:http', 'node:https']) for (const name of ['request', 'get']) require(protocol)[name] = forbidden(protocol + '.' + name)
for (const level of ['success', 'error', 'warning', 'info']) {
  const original = Antd.message[level]
  Antd.message[level] = (...args) => { notices.push({ level, content: args[0] }); return original(...args) }
}
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue'), Page = require(pagePath)
const tracked = ['src/pages/AISearchConfig/index.vue', 'src/utils/useUnsavedDraft.js', 'src/pages/AISearchConfig/AISearchTestDialog.vue']
const sourceHashes = () => Object.fromEntries(tracked.map(rel => [rel, crypto.createHash('sha256').update(fs.readFileSync(path.join(web, rel))).digest('hex')]))
const initialSourceHashes = sourceHashes()
console.log('AI_PRINCIPAL_STAGE_SOURCE ' + JSON.stringify({ web, node: process.version, sourceHashes: initialSourceHashes }))
const clone = value => JSON.parse(JSON.stringify(toRaw(value)))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
async function start() {
  const ocrReadStart = ocrStatusReads
  user.setStoredCurrentUser(ADMIN_A)
  const Host = defineComponent({ setup: () => () => h(Antd.ConfigProvider, { locale: zhCN }, { default: () => h(Page) }) })
  const root = mount(Host, { attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(root); await settle()
  const w = root.findComponent(Page)
  assert.equal(w.vm.formState.enableAIStagedTokenBilling, false, 'actual readonly configuration loaded with stage billing disabled')
  assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent')
  return w
}
function stageSwitch(w, name) {
  const node = w.element.querySelector('button[id$="-' + name + '"]')
  assert.ok(node && node.getAttribute('role') === 'switch', 'real native stage switch: ' + name)
  return node
}
async function writeElement(node, value) {
  assert.ok(node && node.tagName === 'INPUT', 'real native stage input')
  node.value = String(value)
  for (const type of ['input', 'change', 'blur']) node.dispatchEvent(new Event(type, { bubbles: true }))
  await settle()
}
function ledger(label, extra) {
  console.log('AI_PRINCIPAL_STAGE_LEDGER ' + JSON.stringify({ label, calls, traps, notices, unchangedSource: JSON.stringify(sourceHashes()) === JSON.stringify(initialSourceHashes), ...extra }))
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren()
  user.clearStoredCurrentUser()
  assert.deepEqual(traps, [], 'zero provider/network/clipboard/CRUD escapes')
  assert.deepEqual(notices, [], 'stage draft operations emit no toast')
  assert.deepEqual(sourceHashes(), initialSourceHashes, 'three production sources remain byte-exact during fixture')
  calls.length = traps.length = notices.length = 0
})

test('held native stage switch ArrowRight cannot edit B draft in same tick before Vue patches old A DOM', async () => {
  const w = await start(), held = stageSwitch(w, 'enableAIStagedTokenBilling')
  held.focus(); assert.equal(document.activeElement, held)
  assert.equal(held.getAttribute('aria-checked'), 'false')
  user.setStoredCurrentUser(ADMIN_B)
  const before = clone(w.vm.formState)
  assert.equal(held.isConnected, true, 'actual old native DOM is held before Vue patch')
  assert.equal(before.enableAIStagedTokenBilling, false)
  held.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', code: 'ArrowRight', keyCode: 39, which: 39, bubbles: true }))
  const after = clone(w.vm.formState)
  ledger('same-tick-old-switch-arrow', { before, after, heldConnectedBeforePatch: held.isConnected })
  assert.deepEqual(after, before, 'old A keydown may not write any B canonical draft field')
  await settle()
  assert.deepEqual(calls, ['getAISearchConfig', 'getAIModelPrices'], 'actor retirement and stale keydown issue no background GET or write')
})

test('same-owner real stage switches and inputs retain canonical int64-string and six-decimal payload with fixed sampling', async () => {
  const w = await start()
  stageSwitch(w, 'enableAIStagedTokenBilling').click(); await settle()
  assert.equal(w.vm.formState.enableAIStagedTokenBilling, true)
  stageSwitch(w, 'smoothAIStagedTokenMultiplier').click(); await settle()
  assert.equal(w.vm.formState.smoothAIStagedTokenMultiplier, true)
  const rows = w.element.querySelectorAll('.staged-points-editor tbody tr[data-row-key]')
  assert.equal(rows.length, 2)
  const maxInt64 = '9223372036854775807'
  await writeElement(rows[1].querySelector('input'), maxInt64)
  await writeElement(rows[1].querySelector('.ant-input-number-input'), '0.654321')
  const payload = buildAISearchConfigSavePayload(w.vm.formState, CONFIG.baseUrl)
  assert.deepEqual(payload.stagedTokenMultiplierPoints, [{ tokens: '0', multiplier: 2 }, { tokens: maxInt64, multiplier: 0.654321 }])
  assert.equal(payload.enableAIStagedTokenBilling, true); assert.equal(payload.smoothAIStagedTokenMultiplier, true)
  assert.equal(payload.temperature, 0); assert.equal(payload.topP, 0)
  assert.equal(payload.searchPricePerCall, 0.000062); assert.equal(payload.webReadPricePerCall, 1.234567)
  assert.deepEqual(calls, ['getAISearchConfig', 'getAIModelPrices'], 'payload is projected locally without any save/provider dispatch')
  ledger('same-owner-native-stage-payload', { payload })
})
