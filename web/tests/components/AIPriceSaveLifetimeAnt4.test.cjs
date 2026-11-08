const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), Module = require('node:module'), crypto = require('node:crypto')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, toRaw } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const CONFIG = { enabled: true, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC', model: 'MODEL-A', manualModels: ['MODEL-A'], prompt: 'SYNTHETIC', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0, maxAIWebReads: 5, webReadPricePerCall: 0, enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: true, stagedTokenMultiplierPoints: [{ tokens: 0, multiplier: 2 }, { tokens: 1000, multiplier: 1 }] }
const ROWS = [{ id: 501, modelName: 'MODEL-A', displayName: 'A', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0.000062, outputPrice: 0, multiplier: 1.2345, remark: 'SYNTHETIC_A' }, { id: 502, modelName: 'MODEL-B', displayName: 'B', inputPrice: 0, cachedPrice: 0.12, cacheWritePrice: 0, outputPrice: 1.234567, multiplier: 1, remark: 'SYNTHETIC_B' }]
const pending = [], calls = [], notices = [], wrappers = []; let reads = 0
const ok = data => ({ status: 200, data })
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
api.exports = { getAIOCRCacheStatus: syntheticOCRStatus, getAISearchConfig: () => Promise.resolve(ok(structuredClone(CONFIG))), getAIModelPrices: () => { reads++; return Promise.resolve(ok(structuredClone(ROWS))) }, getUAPISearchEngines: () => Promise.resolve(ok({})), getUAPIUsageStatus: () => Promise.resolve(ok({ endpoints: [] })) }
for (const action of ['create', 'update']) api.exports[action + 'AIModelPrice'] = payload => { calls.push({ action, payload: structuredClone(payload) }); return new Promise((resolve, reject) => pending.push({ resolve, reject })) }
require.cache[apiPath] = api
for (const level of ['success', 'error', 'warning']) { const original = Antd.message[level]; Antd.message[level] = (...args) => { notices.push({ level, content: args[0] }); return original(...args) } }
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue')
console.log('PRICE_SAVE_SOURCE ' + JSON.stringify({ source: pagePath, sha256: crypto.createHash('sha256').update(fs.readFileSync(pagePath)).digest('hex') }))
const clean = x => String(x || '').replace(/\s+/g, '')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
function button(root, text) { const b = [...root.querySelectorAll('button')].find(x => clean(x.textContent) === clean(text)); assert.ok(b, 'real native button ' + text); return b }
function modal(id) { const m = [...document.querySelectorAll('.ant-modal')].find(x => /模型价格$/.test(clean(x.querySelector('.ant-modal-title')?.textContent)) && x.style.display !== 'none' && (!id || [...x.querySelectorAll('.ant-form-item')].some(i => clean(i.querySelector('.ant-form-item-label')?.textContent) === '模型名称' && i.querySelector('input')?.value === id))); assert.ok(m, 'visible real Antd4 price modal ' + (id || '')); return m }
function field(root, label) { const i = [...root.querySelectorAll('.ant-form-item')].find(x => clean(x.querySelector('.ant-form-item-label')?.textContent) === clean(label)); assert.ok(i, 'real field ' + label); return i.querySelector('input') }
async function input(root, label, value) { const e = field(root, label); e.value = String(value); for (const name of ['input', 'change', 'blur']) e.dispatchEvent(new Event(name, { bubbles: true })); await settle() }
async function start() { const ocrReadStart = ocrStatusReads; user.setStoredCurrentUser({ id: 37, username: 'fixture-admin', role: 'admin', balance: 1 }); const w = mount(require(pagePath), { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return w }
async function open(w, index = 0) { button(w.element.querySelector('tr[data-row-key="' + ROWS[index].id + '"]'), '编辑').click(); await settle(); return modal(ROWS[index].modelName) }
async function save(m) { button(m, '确定').click(); await settle(); assert.ok(pending.length, 'actual submit issued synthetic API'); return pending.shift() }
function draft(w) { return structuredClone(toRaw(w.vm.priceForm)) }
function finish(p, outcome) { if (outcome === 'reject') p.reject(new Error('SYNTHETIC_REJECT_A')); else p.resolve(outcome === 'success' ? ok({}) : { status: 409, message: 'SYNTHETIC_FAILED_A' }) }
async function close(w, m = modal()) { button(m, '取消').click(); await settle(); assert.equal(w.vm.priceModalVisible, false) }
function quiet(readCount) { assert.deepEqual(notices, [], 'stale save cannot publish success/failure toast'); assert.equal(reads, readCount, 'stale save cannot refetch') }
function ledger(label, w) { console.log('PRICE_SAVE_LEDGER ' + JSON.stringify({ label, calls, reads, notices, visible: w?.vm.priceModalVisible, busy: w?.vm.priceSaving, draft: w ? draft(w) : null })) }
afterEach(async () => { for (const w of wrappers.splice(0)) w.unmount(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren(); user.setStoredCurrentUser(null); pending.length = calls.length = notices.length = 0; reads = 0 })
for (const action of ['create', 'update']) test('current ' + action + ' preserves exact payload with optional context window and success closes resets refetches once', async () => {
 const w = await start(), canonical = w.vm.priceForm
 if (action === 'create') { button(w.element, '新增模型价格').click(); await settle() } else await open(w)
 let m = modal(); await input(m, '模型名称', '  MODEL-EXACT  '); await input(m, '显示名称', '  Exact  '); await input(m, '缓存写入单价', 0.000062); await input(m, '输入单价', 0); await input(m, '缓存输入单价', 0); await input(m, '输出单价', 0); await input(m, '倍率', 1.2345); await input(m, '备注', 'SYNTHETIC_EXACT')
 const readCount = reads, p = await save(modal()); assert.equal(w.vm.priceSaving, true); assert.equal(w.vm.priceForm, canonical)
 const expected = { ...(action === 'create' ? { contextWindowTokens: 0 } : {}), id: action === 'create' ? null : 501, modelName: 'MODEL-EXACT', displayName: 'Exact', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0.000062, outputPrice: 0, multiplier: 1.2345, remark: 'SYNTHETIC_EXACT' }
 assert.deepEqual(calls, [{ action, payload: expected }]); finish(p, 'success'); await settle(); assert.equal(w.vm.priceModalVisible, false); assert.equal(w.vm.priceSaving, false); assert.equal(reads, readCount + 1); assert.equal(w.vm.priceForm.modelName, ''); assert.equal(w.vm.priceForm.cacheWritePrice, 0); assert.equal(w.vm.priceForm.multiplier, 1); assert.deepEqual(notices, [{ level: 'success', content: '保存成功' }]); assert.equal(w.vm.hasUnsavedChanges, false); ledger(action, w)
})
for (const outcome of ['failure', 'reject']) test('current ' + outcome + ' preserves native draft and unlocks without refetch', async () => {
 const w = await start(), m = await open(w); await input(m, '备注', 'SYNTHETIC_RETRY'); const before = draft(w), count = reads, p = await save(modal()); finish(p, outcome); await settle(); assert.equal(w.vm.priceModalVisible, true); assert.equal(w.vm.priceSaving, false); assert.deepEqual(draft(w), before); assert.equal(reads, count); assert.equal(notices.length, 1); assert.equal(notices[0].level, 'error'); assert.ok(!button(modal(), '确定').classList.contains('ant-btn-loading')); ledger(outcome, w)
})
test('empty model name retains warning and zero API calls', async () => { const w = await start(); button(w.element, '新增模型价格').click(); await settle(); button(modal(), '确定').click(); await settle(); assert.deepEqual(calls, []); assert.equal(w.vm.priceSaving, false); assert.equal(w.vm.priceModalVisible, true); assert.deepEqual(notices, [{ level: 'warning', content: '请填写模型名称' }]) })
test('native cancel synchronously retires pending save busy state', async () => { const w = await start(), p = await save(await open(w)); assert.equal(w.vm.priceSaving, true); const cancel = button(modal(), '取消'); assert.ok(!cancel.disabled); cancel.click(); assert.equal(w.vm.priceSaving, false, 'close must retire busy synchronously before any old promise settles'); finish(p, 'failure'); await settle() })
for (const outcome of ['success', 'failure', 'reject']) test('close/reopen B retains draft; old A ' + outcome + ' cannot close reset toast refetch or change busy', async () => {
 const w = await start(), a = await save(await open(w)); await close(w); const bModal = await open(w, 1); await input(bModal, '备注', 'SYNTHETIC_B_DRAFT'); const before = draft(w), count = reads; notices.length = 0
 finish(a, outcome); await settle(); assert.equal(w.vm.priceModalVisible, true, 'old A cannot close B'); assert.deepEqual(draft(w), before, 'old A cannot reset B'); assert.equal(w.vm.priceSaving, false); quiet(count); ledger('stale-A-' + outcome, w)
})
for (const outcome of ['success', 'failure', 'reject']) test('reopened B native save gets independent busy lease; old A ' + outcome + ' cannot release it', async () => {
 const w = await start(), a = await save(await open(w)); await close(w); await open(w, 1); await input(modal(), '备注', 'SYNTHETIC_B_ACTIVE'); const before = draft(w), count = reads; notices.length = 0; const b = await save(modal()); assert.equal(calls.length, 2, 'new B API must issue through actual native button'); assert.equal(w.vm.priceSaving, true)
 finish(a, outcome); await settle(); assert.equal(w.vm.priceSaving, true, 'old A finally cannot release B busy'); assert.equal(w.vm.priceModalVisible, true); assert.deepEqual(draft(w), before); quiet(count); assert.ok(button(modal(), '确定').classList.contains('ant-btn-loading'))
 finish(b, 'success'); await settle(); assert.equal(w.vm.priceSaving, false); assert.equal(w.vm.priceModalVisible, false); assert.equal(w.vm.priceForm.modelName, ''); assert.equal(reads, count + 1); assert.deepEqual(notices, [{ level: 'success', content: '保存成功' }]); ledger('B-current-after-' + outcome, w)
})
for (const outcome of ['success', 'failure', 'reject']) test('unmount suppresses late ' + outcome + ' UI notices refetch and finally publication', async () => {
 const w = await start(), a = await save(await open(w)), vm = w.vm, count = reads; w.unmount(); wrappers.splice(wrappers.indexOf(w), 1); const stoppedBusy = vm.priceSaving, before = structuredClone(toRaw(vm.priceForm)), stoppedVisible = vm.priceModalVisible; notices.length = 0
 finish(a, outcome); await settle(); assert.equal(vm.priceSaving, stoppedBusy, 'unmounted save cannot publish finally'); assert.equal(vm.priceModalVisible, stoppedVisible); assert.deepEqual(structuredClone(toRaw(vm.priceForm)), before); quiet(count); assert.ok(!document.body.textContent.includes('SYNTHETIC_FAILED_A'))
})
test('two mounted owners keep independent save leases and drafts', async () => {
 const left = await start(), right = await start(), a = await save(await open(left)); const rightModal = await open(right, 1), b = await save(rightModal), rightBefore = draft(right), count = reads
 await close(left, modal('MODEL-A')); notices.length = 0; finish(a, 'success'); await settle(); assert.equal(right.vm.priceSaving, true); assert.equal(right.vm.priceModalVisible, true); assert.deepEqual(draft(right), rightBefore); quiet(count)
 finish(b, 'success'); await settle(); assert.equal(right.vm.priceSaving, false); assert.equal(right.vm.priceModalVisible, false); assert.equal(reads, count + 1); assert.equal(left.vm.priceModalVisible, false); assert.equal(calls.length, 2); ledger('two-mounted', right)
})
test('extra direct display contract, not a UI click: visible VM retarget retires old save ownership', async () => {
 const w = await start(), a = await save(await open(w)); w.vm.openPriceModal(ROWS[1]); await settle(); const before = draft(w), count = reads; notices.length = 0; finish(a, 'success'); await settle(); assert.equal(w.vm.priceModalVisible, true); assert.deepEqual(draft(w), before); assert.equal(w.vm.priceSaving, false); quiet(count)
})

test('supplemental handler contract, not native click: duplicate same-visible save while busy issues only one API', async () => {
 const w = await start(); await open(w); w.vm.handleSaveModelPrice(); w.vm.handleSaveModelPrice()
 assert.equal(calls.length, 1, 'duplicate handler invocation must not issue second API'); assert.equal(pending.length, 1); assert.equal(w.vm.priceSaving, true)
 finish(pending.shift(), 'success'); await settle(); assert.equal(w.vm.priceSaving, false); assert.equal(w.vm.priceModalVisible, false); assert.deepEqual(notices, [{ level: 'success', content: '保存成功' }]); ledger('duplicate-handler', w)
})
test('supplemental direct defensive contract, not native click: hidden valid draft method invocation issues zero API and toast', async () => {
 const w = await start(); w.vm.updatePriceForm({ ...ROWS[0] }); const before = draft(w), count = reads
 assert.equal(w.vm.priceModalVisible, false); w.vm.handleSaveModelPrice(); await settle(); assert.deepEqual(calls, [], 'hidden editor method cannot start save'); assert.equal(w.vm.priceSaving, false); assert.deepEqual(draft(w), before); quiet(count); ledger('hidden-handler', w)
})
test('supplemental captured VM defensive contract, not native click: invocation after unmount issues zero API and toast', async () => {
 const w = await start(); await open(w); const invoke = w.vm.handleSaveModelPrice, vm = w.vm, count = reads
 w.unmount(); wrappers.splice(wrappers.indexOf(w), 1)
 const before = structuredClone(toRaw(vm.priceForm))
 assert.equal(before.id, null, 'retirement scrubs the old private draft before any later handler')
 assert.equal(before.modelName, ''); assert.equal(before.displayName, '')
 invoke(); await settle(); assert.deepEqual(calls, [], 'disposed editor captured handler cannot start save'); assert.equal(vm.priceSaving, false); assert.deepEqual(structuredClone(toRaw(vm.priceForm)), before); quiet(count)
})
test('supplemental configured-model direct VM contract, not UI click behind active modal: retarget retires A and initializes configured draft', async () => {
 const w = await start(); w.vm.formState.model = 'MODEL-CONFIGURED-UNPRICED'; await settle(); assert.ok(button(w.element, '去设置价格'), 'real native configured-model CTA exists before modal opens')
 const a = await save(await open(w)), count = reads; w.vm.openPriceModalForConfiguredModel(); await settle()
 const expected = { id: null, contextWindowTokens: 0, modelName: 'MODEL-CONFIGURED-UNPRICED', displayName: 'MODEL-CONFIGURED-UNPRICED', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0, outputPrice: 0, multiplier: 1, remark: '' }
 assert.deepEqual(draft(w), expected); assert.equal(w.vm.priceSaving, false, 'configured-model opener synchronously retires prior lease'); assert.equal(w.vm.priceModalVisible, true); notices.length = 0; finish(a, 'success'); await settle(); assert.equal(w.vm.priceModalVisible, true); assert.equal(w.vm.priceSaving, false); assert.deepEqual(draft(w), expected); quiet(count); ledger('configured-model-VM-retarget', w)
})
