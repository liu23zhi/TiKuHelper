const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), Module = require('node:module'), crypto = require('node:crypto')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const CONFIG = { enabled: false, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC_KEY', model: 'MODEL-A', manualModels: ['MODEL-A', 'MODEL-B'], prompt: 'SYNTHETIC_PROMPT', remark: 'PRESERVE', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0, tikuSearchPricePerCall: 0, maxAIWebReads: 5, webReadPricePerCall: 0, imageOCRPricePerCall: 0, enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: false, stagedTokenMultiplierPoints: [{ tokens: 0, multiplier: 2 }, { tokens: 1000, multiplier: 1 }] }
const ROWS = [{ id: 501, modelName: 'MODEL-A', displayName: 'A', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0, outputPrice: 0, multiplier: 1, remark: '' }]
const fields = ['maxAISearchModelCalls', 'maxAISearches', 'maxAISearchRetries', 'searchPricePerCall', 'tikuSearchPricePerCall', 'maxAIWebReads', 'webReadPricePerCall', 'imageOCRPricePerCall']
const priceFields = new Set(['searchPricePerCall', 'tikuSearchPricePerCall', 'webReadPricePerCall', 'imageOCRPricePerCall'])
const labels = ['模型最大调用次数', '最大搜索次数', '每轮重试次数', '联网搜索单价（元/次）', '本地题库检索单价（元/次）', '网页读取次数', '网页读取单价（元/次）', '图片 OCR 单价（元/次）']
const limits = Object.freeze({ minModelCalls: 1, maxBudget: 20, minSearches: 1, minRetries: 1, maxRetries: 10, minWebReads: 1, maxWebReads: 20 })
const calls = [], pendingCapability = [], wrappers = []; let serverConfig = structuredClone(CONFIG)
const clone = x => JSON.parse(JSON.stringify(x)), ok = data => ({ status: 200, data })
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
api.exports = { getAIOCRCacheStatus: syntheticOCRStatus,
 getAISearchConfig: () => { calls.push(['read-config']); return Promise.resolve(ok(structuredClone(serverConfig))) },
 getAIModelPrices: () => { calls.push(['read-prices']); return Promise.resolve(ok(structuredClone(ROWS))) },
 getUAPISearchEngines: () => { calls.push(['capability-engines']); return new Promise(resolve => pendingCapability.push(resolve)) },
 getUAPIUsageStatus: () => { calls.push(['capability-usage']); return new Promise(resolve => pendingCapability.push(resolve)) },
 updateAISearchConfig: payload => { calls.push(['save-config', structuredClone(payload)]); serverConfig = structuredClone(payload); return Promise.resolve(ok({})) },
 getAISearchModels: () => { throw Error('unexpected provider fixture call') }, testAISearchConfig: () => { throw Error('unexpected provider fixture call') }
}
require.cache[apiPath] = api
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue'), childPath = path.join(web, 'src/pages/AISearchConfig/AIAdvancedSettingsSection.vue')
console.log('ADVANCED_SOURCE ' + JSON.stringify({ source: pagePath, sha256: crypto.createHash('sha256').update(fs.readFileSync(pagePath)).digest('hex'), node: process.version, qualification: 'native Antd4 jsdom only; no real-browser/CSS/provider acceptance' }))
const clean = x => String(x || '').replace(/\s+/g, '')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
function button(root, text) { const e = [...root.querySelectorAll('button')].find(x => clean(x.textContent) === clean(text)); assert.ok(e, 'native button ' + text); return e }
function item(w, label) { const i = w.findAll('.ant-form-item').find(x => x.find('.ant-form-item-label').exists() && clean(x.find('.ant-form-item-label').text()) === clean(label)); assert.ok(i, 'field ' + label); return i }
function advanced(w) { return w.findAll('section').find(x => x.find('h2').exists() && x.find('h2').text() === '高级能力') }
function mutations() { return calls.filter(x => x[0] !== 'read-config' && x[0] !== 'read-prices') }
async function start() { const ocrReadStart = ocrStatusReads; user.setStoredCurrentUser({ id: 37, username: 'fixture-admin', role: 'admin', balance: 1 }); const w = mount(require(pagePath), { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return w }
afterEach(async () => { for (const w of wrappers.splice(0)) w.unmount(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren(); user.setStoredCurrentUser(null); calls.length = pendingCapability.length = 0; serverConfig = structuredClone(CONFIG) })

test('positive native UI: eight advanced fields retain exact order, bounds, parser, hints, zero prices and readonly others', async () => {
 const w = await start(), region = advanced(w), canonical = w.vm.formState
 assert.deepEqual(w.findAll('.ai-config-form .ant-form-item-label').map(x => x.text()), ['是否启用', '服务地址', 'API Key', '默认模型', '备用模型', '模型列表', ...labels, '自动压缩上下文', '压缩执行顺序', '压缩触发比例', '压缩目标比例', '压缩最大尝试次数', 'Prompt 设置', '温度', 'Top P'])
 assert.deepEqual(region.findAll('.ant-form-item-label').map(x => x.text()), [...labels, '自动压缩上下文', '压缩执行顺序', '压缩触发比例', '压缩目标比例', '压缩最大尝试次数'])
 assert.equal(w.findAll('form.ai-config-form').length, 1); assert.equal(region.findAll('form').length, 0)
 assert.equal(region.attributes('tabindex'), '-1'); assert.equal(region.attributes('aria-labelledby'), region.find('h2').attributes('id'))
 const numbers = region.findAllComponents(Antd.InputNumber); assert.equal(numbers.length, 11)
 for (let i = 0; i < fields.length; i++) { const p = numbers[i].props(), integer = !priceFields.has(fields[i]); assert.equal(p.value, CONFIG[fields[i]]); assert.equal(p.min, integer ? 1 : 0); assert.equal(p.precision, integer ? 0 : 6); assert.equal(p.step, integer ? 1 : 0.01); if (integer) { assert.equal(p.max, i === 2 ? 10 : 20); assert.equal(p.parser, w.vm.parseIntegerInput) } assert.ok(item(region, labels[i]).find('.hint').text().length > 20) }
 assert.deepEqual(numbers.filter((_, i) => priceFields.has(fields[i])).map(n => n.get('input').element.value), ['0.000000', '0.000000', '0.000000', '0.000000'])
 for (const label of ['温度', 'Top P']) { const input = item(w, label).get('input').element; assert.equal(input.value, '0'); assert.equal(input.readOnly, true); assert.equal(input.disabled, true) }
 assert.equal(canonical.enabled, false); assert.equal(canonical.smoothAIStagedTokenMultiplier, false); assert.equal(w.vm.hasUnsavedChanges, false); assert.deepEqual(mutations(), [])
 console.log('ADVANCED_POSITIVE ' + JSON.stringify({ order: labels, values: fields.map(f => canonical[f]), noRequests: mutations(), parser: numbers[0].props('parser') === w.vm.parseIntegerInput }))
})

test('positive native UI: all eight edits preserve canonical identity, nonowned values, snapshots and dirty/revert', async () => {
 const w = await start(), canonical = w.vm.formState, before = clone(canonical), snapshot = w.vm.savedConfigSnapshot, values = [7, 8, 6, 0.123456, 0.000061, 9, 0.654321, 0.321001]
 for (let i = 0; i < fields.length; i++) { await item(w, labels[i]).get('input').setValue(String(values[i])); assert.equal(canonical[fields[i]], values[i]); assert.equal(w.vm.formState, canonical) }
 for (const field of Object.keys(before).filter(f => !fields.includes(f))) assert.deepEqual(clone(canonical[field]), before[field], 'preserve ' + field)
 assert.equal(w.vm.hasUnsavedChanges, true); assert.equal(w.vm.savedConfigSnapshot, snapshot); assert.deepEqual(mutations(), [])
 Object.assign(canonical, before); await settle(); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(w.vm.formState, canonical); assert.equal(w.vm.currentConfigSnapshot, snapshot)
 console.log('ADVANCED_DRAFT ' + JSON.stringify({ canonical: w.vm.formState === canonical, dirtyAfterRevert: w.vm.hasUnsavedChanges, noRequests: mutations() }))
})

test('private extraction boundary: controlled advanced child exists and mounts once; expected structural RED only', async () => {
 assert.equal(fs.existsSync(childPath), true, 'expected RED: private controlled advanced child boundary is absent; not a baseline product bug')
 const w = await start(), Child = require(childPath), child = w.findComponent(Child)
 assert.equal(w.findAllComponents(Child).length, 1); assert.equal(child.props('formValue'), w.vm.formState); assert.deepEqual(child.props('limits'), limits); assert.equal(child.props('integerParser'), w.vm.parseIntegerInput)
})

test('positive native UI: eight advanced values save once through parent with untouched canonical fields and clean snapshot', async () => {
 const w = await start(), canonical = w.vm.formState, before = clone(canonical), values = [20, 19, 10, 0.123456, 0.000061, 18, 0.654321, 0.222222]
 for (let i = 0; i < fields.length; i++) await item(w, labels[i]).get('input').setValue(String(values[i]))
 button(w.element, '保存配置').click(); await settle()
 const saves = mutations().filter(x => x[0] === 'save-config'); assert.equal(saves.length, 1); const payload = saves[0][1]
 for (let i = 0; i < fields.length; i++) assert.equal(payload[fields[i] === 'tikuSearchPricePerCall' ? 'tiku_search_price_per_call' : fields[i]], values[i])
 for (const field of ['enabled', 'baseUrl', 'apiKey', 'model', 'prompt', 'temperature', 'topP', 'enableAIStagedTokenBilling', 'smoothAIStagedTokenMultiplier']) assert.deepEqual(payload[field], before[field], field)
 assert.deepEqual(payload.manualModels, CONFIG.manualModels); assert.equal(w.vm.formState, canonical); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(w.vm.currentConfigSnapshot, w.vm.savedConfigSnapshot)
 console.log('ADVANCED_SAVE ' + JSON.stringify({ payload, canonical: w.vm.formState === canonical, dirty: w.vm.hasUnsavedChanges }))
})

test('positive native UI: dirty advanced refresh cancel retains edits; explicit discard reloads canonical state', async () => {
 const w = await start(), canonical = w.vm.formState
 await item(w, labels[0]).get('input').setValue('12'); await item(w, labels[fields.indexOf('webReadPricePerCall')]).get('input').setValue('0.777777')
 const reads = calls.filter(x => x[0] === 'read-config').length
 button(w.element, '刷新').click(); await settle(); let modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); assert.ok(modal)
 button(modal, '保留编辑').click(); await settle(); assert.equal(canonical.maxAISearchModelCalls, 12); assert.equal(canonical.webReadPricePerCall, 0.777777); assert.equal(calls.filter(x => x[0] === 'read-config').length, reads)
 button(w.element, '刷新').click(); await settle(); modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); button(modal, '放弃并刷新').click(); await settle()
 assert.equal(w.vm.formState, canonical); assert.equal(canonical.maxAISearchModelCalls, CONFIG.maxAISearchModelCalls); assert.equal(canonical.webReadPricePerCall, 0); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(calls.filter(x => x[0] === 'read-config').length, reads + 1); assert.deepEqual(mutations(), [])
})

test('positive native UI: capability is explicit-only; parent owns two synthetic API requests, loading and modal', async () => {
 const w = await start(), canonical = w.vm.formState, before = clone(canonical), snapshot = w.vm.savedConfigSnapshot
 assert.deepEqual(mutations(), []); assert.equal(w.vm.capabilityModalVisible, false)
 button(advanced(w).element, '查看深度思考能力').click(); await settle()
 assert.deepEqual(mutations(), [['capability-engines'], ['capability-usage']]); assert.equal(w.vm.searchEngineLoading, true); assert.equal(w.vm.capabilityModalVisible, true)
 assert.ok(button(advanced(w).element, '查看深度思考能力').classList.contains('ant-btn-loading')); assert.ok([...document.querySelectorAll('.ant-modal-title')].some(x => x.textContent === '深度思考能力'))
 if (fs.existsSync(childPath)) { const child = w.findComponent(require(childPath)); assert.equal(child.emitted('view-capability').length, 1); assert.equal(child.props('searchEngineLoading'), true) }
 pendingCapability.shift()(ok({})); pendingCapability.shift()(ok({ endpoints: [] })); await settle()
 assert.equal(w.vm.searchEngineLoading, false); assert.equal(w.vm.capabilityError, ''); assert.deepEqual(clone(canonical), before); assert.equal(w.vm.formState, canonical); assert.equal(w.vm.savedConfigSnapshot, snapshot); assert.equal(w.vm.hasUnsavedChanges, false)
 console.log('ADVANCED_CAPABILITY ' + JSON.stringify({ requests: mutations(), loading: w.vm.searchEngineLoading, modal: w.vm.capabilityModalVisible, canonical: w.vm.formState === canonical }))
})

test('positive parent ownership: invalid numeric budgets and prices never reach save fixture; exact valid zero saves', async () => {
 const w = await start(), canonical = w.vm.formState, before = clone(canonical)
 for (const [field, value] of [['maxAISearchModelCalls', 0], ['maxAISearchModelCalls', 21], ['maxAISearches', 1.5], ['maxAISearchRetries', 11], ['maxAIWebReads', 0], ['maxAIWebReads', 21], ['searchPricePerCall', -0.01], ['tikuSearchPricePerCall', -0.01], ['webReadPricePerCall', -0.01], ['imageOCRPricePerCall', -0.01]]) {
  Object.assign(canonical, before, { [field]: value }); await settle(); button(w.element, '保存配置').click(); await settle(); assert.deepEqual(mutations(), [], field + '=' + value)
 }
 Object.assign(canonical, before, { maxAISearchModelCalls: 1, maxAISearches: 1, maxAISearchRetries: 1, maxAIWebReads: 1, searchPricePerCall: 0, webReadPricePerCall: 0, imageOCRPricePerCall: 0 }); await settle(); button(w.element, '保存配置').click(); await settle()
 const save = mutations().find(x => x[0] === 'save-config'); assert.ok(save); assert.equal(save[1].searchPricePerCall, 0); assert.equal(save[1].tiku_search_price_per_call, 0); assert.equal(save[1].webReadPricePerCall, 0); assert.equal(save[1].imageOCRPricePerCall, 0); assert.equal(w.vm.formState, canonical)
})

function freezeDeep(value) { Object.freeze(value); for (const x of Object.values(value)) if (x && typeof x === 'object' && !Object.isFrozen(x)) freezeDeep(x); return value }

test('fresh compression order uses the existing controlled advanced section, native keyboard reorder and one explicit parent save', async () => {
 const w = await start(), canonical = w.vm.formState, before = clone(canonical), snapshot = w.vm.savedConfigSnapshot
 const Order = require(path.join(web, 'src/pages/AISearchConfig/AICompressionOrder.vue'))
 const order = w.findComponent(Order)
 assert.equal(order.exists(), true); assert.equal(w.findAllComponents(Order).length, 1)
 assert.deepEqual(order.props('modelValue'), before.contextCompressionOrder)
 const rows = order.findAll('[data-strategy]'), first = before.contextCompressionOrder[0]
 assert.equal(rows.length, 3); assert.equal(rows[0].attributes('data-strategy'), first)
 await rows[0].get('.compression-order-handle').trigger('keydown', { key: 'ArrowDown' }); await settle()
 const expected = [...before.contextCompressionOrder]; [expected[0], expected[1]] = [expected[1], expected[0]]
 assert.deepEqual(clone(canonical.contextCompressionOrder), expected)
 assert.equal(w.vm.formState, canonical); assert.equal(w.vm.savedConfigSnapshot, snapshot); assert.equal(w.vm.hasUnsavedChanges, true)
 assert.deepEqual(mutations(), [], 'reordering remains a draft, never calls a provider or save automatically')
 for (const field of Object.keys(before).filter(f => f !== 'contextCompressionOrder')) assert.deepEqual(clone(canonical[field]), before[field], 'preserve '+field)
 button(w.element, '保存配置').click(); await settle()
 const saved = calls.filter(x => x[0] === 'save-config'); assert.equal(saved.length, 1)
 assert.deepEqual(saved[0][1].contextCompressionOrder, expected)
 assert.deepEqual(clone(w.vm.savedCompressionOrder), expected)
 assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(w.vm.formState, canonical)
 user.clearStoredCurrentUser(); await settle()
 const defaults = require(path.join(web, 'src/utils/aiSearchConfigHelpers.mjs')).buildAIContextCompressionSettings().contextCompressionOrder
 assert.deepEqual(clone(w.vm.savedCompressionOrder), defaults, 'new saved-order state retires with actual explicit logout, not the next actor')
})
const parseInteger = value => String(value ?? '').replace(/[^\d]/g, '')
function childProps(id = 'isolated-advanced') { return { sectionId: id, formValue: freezeDeep({ ...structuredClone(CONFIG), manualModelsText: 'MODEL-A\nMODEL-B', preservedUnknown: { nested: ['PRESERVE', 0, false] } }), limits, integerParser: parseInteger, searchEngineLoading: false } }
async function isolated(props) { const w = mount(require(childPath), { props, attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); return w }
const absentChild = !fs.existsSync(childPath) && 'baseline private child is intentionally absent; explicit structural ASSERTION RED above'

test('controlled child: frozen props emit exactly one full independent clone per native numeric input, including zero', { skip: absentChild }, async () => {
 const props = childProps(), before = clone(props.formValue), w = await isolated(props), requestCount = calls.length, values = [7, 8, 6, 0.123456, 0.000061, 9, 0.654321, 0.321001]
 assert.equal(w.attributes('id'), props.sectionId); assert.equal(w.attributes('tabindex'), '-1'); assert.equal(w.attributes('aria-labelledby'), props.sectionId + '-heading'); assert.equal(w.find('h2').attributes('id'), props.sectionId + '-heading'); assert.equal(w.findAll('form').length, 0)
 for (let i = 0; i < fields.length; i++) {
  const previous = (w.emitted('update:form-value') || []).length, input = item(w, labels[i]).get('input')
  input.element.value = String(values[i]); await input.trigger('input'); await settle()
  const emitted = w.emitted('update:form-value'); assert.equal(emitted.length, previous + 1, fields[i] + ' exactly one native input update'); const payload = emitted.at(-1)[0]
  assert.notEqual(payload, props.formValue); assert.deepEqual(payload, { ...before, [fields[i]]: values[i] }); assert.deepEqual(props.formValue, before)
 }
 await w.setProps({ formValue: freezeDeep({ ...before, searchPricePerCall: 0.5, tikuSearchPricePerCall: 0.625, webReadPricePerCall: 0.75, imageOCRPricePerCall: 0.875 }) }); await settle()
 for (const i of fields.map((field, index) => priceFields.has(field) ? index : -1).filter(index => index >= 0)) { const previous = w.emitted('update:form-value').length, input = item(w, labels[i]).get('input'); input.element.value = '0'; await input.trigger('input'); await settle(); assert.equal(w.emitted('update:form-value').length, previous + 1); assert.equal(w.emitted('update:form-value').at(-1)[0][fields[i]], 0) }
 assert.equal(calls.length, requestCount, 'standalone controlled child cannot request APIs')
})

test('controlled child: parser/limits/loading and form props refresh; two instances and explicit capability event isolate', { skip: absentChild }, async () => {
 const leftProps = childProps('advanced-left'), rightProps = childProps('advanced-right'), left = await isolated(leftProps), right = await isolated(rightProps), requests = calls.length
 assert.notEqual(left.attributes('id'), right.attributes('id')); assert.notEqual(left.find('h2').attributes('id'), right.find('h2').attributes('id'))
 button(left.element, '查看深度思考能力').click(); await settle(); assert.deepEqual(left.emitted('view-capability'), [[]]); assert.equal(right.emitted('view-capability'), undefined); assert.equal(calls.length, requests)
 const parser = value => String(value ?? '').replace(/x/g, ''), nextLimits = { minModelCalls: 2, maxBudget: 18, minSearches: 3, minRetries: 2, maxRetries: 8, minWebReads: 4, maxWebReads: 17 }, nextForm = freezeDeep({ ...clone(leftProps.formValue), maxAISearchModelCalls: 12, maxAISearches: 13, maxAISearchRetries: 7, maxAIWebReads: 16, searchPricePerCall: 0.333333, webReadPricePerCall: 0 })
 await left.setProps({ formValue: nextForm, limits: nextLimits, integerParser: parser, searchEngineLoading: true }); await settle()
 const numbers = left.findAllComponents(Antd.InputNumber)
 for (let i = 0; i < fields.length; i++) { assert.equal(numbers[i].props('value'), nextForm[fields[i]]); if (!priceFields.has(fields[i])) assert.equal(numbers[i].props('parser'), parser) }
 assert.deepEqual(numbers.filter((_, i) => !priceFields.has(fields[i])).map(x => [x.props('min'), x.props('max')]), [[2, 18], [3, 18], [2, 8], [4, 17]])
 assert.ok(button(left.element, '查看深度思考能力').classList.contains('ant-btn-loading')); assert.ok(!button(right.element, '查看深度思考能力').classList.contains('ant-btn-loading'))
 assert.deepEqual(right.findAllComponents(Antd.InputNumber).map(x => x.props('value')), fields.map(f => CONFIG[f])); assert.equal(right.emitted('update:form-value'), undefined); assert.deepEqual(rightProps.formValue, childProps('unused').formValue)
 assert.equal(calls.length, requests); assert.equal(document.querySelectorAll('.ant-modal').length, 0, 'standalone action is event-only, no parent modal')
})
