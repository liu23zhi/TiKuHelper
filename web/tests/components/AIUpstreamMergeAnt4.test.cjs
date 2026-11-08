const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const compressionHelpers = require(path.join(web, 'src/utils/aiSearchConfigHelpers.mjs'))
const CONFIG = { enabled: true, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC_KEY', model: 'MODEL-A', manualModels: ['MODEL-A'], prompt: 'SYNTHETIC_PROMPT', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, maxAIWebReads: 5, contextCompressionEnabled: true, useRemoteContextCompression: false, contextCompressionTriggerPercent: 80, contextCompressionTargetPercent: 50, contextCompressionMaxAttempts: 3 }
const ROWS = [{ id: 7, modelName: 'MODEL-A', displayName: 'A', contextWindowTokens: 131072, inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0, outputPrice: 1, multiplier: 1 }, { id: 8, modelName: 'LEGACY', inputPrice: 0, multiplier: 1 }]
let serverConfig, serverWindow, wrapper
const calls = [], pending = [], ok = data => ({ status: 200, data })
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
// Exactly the new readonly OCR status GET, never clear/write/provider.
let ocrStatusReads = 0
function syntheticOCRStatus(feedback) { assert.deepEqual(feedback, { errorFeedback: 'local' }); ocrStatusReads += 1; return Promise.resolve({ status: 200, data: { configured: false, entries: 0 } }) }
require('node:test').after(() => console.log('AI_OCR_STATUS_READS ' + JSON.stringify({ fixture: __filename, method: 'GET', count: ocrStatusReads, configured: false, entries: 0 })))
api.exports = { getAIOCRCacheStatus: syntheticOCRStatus,
  getAISearchConfig: () => Promise.resolve(ok(structuredClone(serverConfig))),
  getAIModelPrices: () => Promise.resolve(ok(structuredClone(ROWS))),
  updateAISearchConfig: payload => { calls.push(['save-config', structuredClone(payload)]); serverConfig = structuredClone(payload); return Promise.resolve(ok({})) },
  updateAIModelPrice: payload => { calls.push(['save-price', structuredClone(payload)]); if (Object.hasOwn(payload, 'contextWindowTokens')) serverWindow = payload.contextWindowTokens; return Promise.resolve(ok({})) },
  testAISearchConfig: payload => { calls.push(['hello', structuredClone(payload)]); return new Promise((resolve, reject) => pending.push({ resolve, reject })) },
  testAIToolCall: payload => { calls.push(['tool', structuredClone(payload)]); return new Promise((resolve, reject) => pending.push({ resolve, reject })) },
}
require.cache[apiPath] = api
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue'), dialogPath = path.join(web, 'src/pages/AISearchConfig/AISearchTestDialog.vue')
const clean = value => String(value || '').replace(/\s+/g, '')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
async function start(overrides = {}) { const ocrReadStart = ocrStatusReads; serverConfig = { ...structuredClone(CONFIG), ...overrides }; serverWindow = 262144; user.setStoredCurrentUser({ id: 37, username: 'fixture-admin', role: 'admin', balance: 1 }); wrapper = mount(require(pagePath), { attachTo: document.body, global: { plugins: [Antd] } }); await settle(); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return wrapper }
function button(root, text) { const node = [...root.querySelectorAll('button')].find(x => clean(x.textContent) === clean(text)); assert.ok(node, 'native button ' + text); return node }
function modal() { const node = [...document.querySelectorAll('.ant-modal')].find(x => clean(x.querySelector('.ant-modal-title')?.textContent) === '测试AI、联网与OCR工具' && x.style.display !== 'none'); assert.ok(node); return node }
function priceModal() { const node = [...document.querySelectorAll('.ant-modal')].find(x => /模型价格$/.test(clean(x.querySelector('.ant-modal-title')?.textContent)) && x.style.display !== 'none'); assert.ok(node); return node }
function field(root, label) { const item = [...root.querySelectorAll('.ant-form-item')].find(x => clean(x.querySelector('.ant-form-item-label')?.textContent) === clean(label)); assert.ok(item, 'native form item ' + label); return item }
async function input(root, label, value) { const node = field(root, label).querySelector('input, textarea'); assert.ok(node); node.value = String(value); for (const event of ['input', 'change', 'blur']) node.dispatchEvent(new Event(event, { bubbles: true })); await settle() }
function dialog() { return wrapper.findComponent(require(dialogPath)).vm }
async function openMode(mode) { button(wrapper.element.querySelector('.connection-grid').closest('section'), '测试 AI、联网与 OCR 工具').click(); await settle(); const radio = modal().querySelector('input[type="radio"][value="' + mode + '"]'); assert.ok(radio); radio.click(); await settle(); return dialog() }
function send() { const node = modal().querySelector('.ant-modal-footer .ant-btn-primary'); assert.ok(node); node.click() }
function result(label, withNetwork = true) { return ok({ success: true, message: 'SYNTHETIC_SUCCESS', requestId: 'SYNTHETIC_REQUEST', answers: [label], testPrompt: 'SYNTHETIC_PREVIEW', toolResult: withNetwork ? [{ tool: 'web_search', query: 'actual fixture', response: { results: [{ title: 'SYNTHETIC_SOURCE', url: 'https://fixture.invalid/source', snippet: 'SYNTHETIC_SNIPPET' }] } }] : [], compression: { success: true, strategy: 'local', beforeTokens: 900, afterTokens: 400, summary: 'SYNTHETIC_SUMMARY' }, trace: [{ phase: 'synthetic' }], requestBody: '{}', rawResponse: 'SYNTHETIC_RAW' }) }
async function fillRequiredTarget(mode, suffix) {
  if (mode === 'direct') await input(modal(), '搜索词', 'SYNTHETIC_DIRECT_' + suffix)
  if (mode === 'ai') await input(modal(), '关键词或网页地址', 'SYNTHETIC_AI_' + suffix)
}
function assertCompressionWarning() {
  const text = modal().textContent
  assert.ok(text.includes('仍会请求当前模型'), 'local summary still calls the current model')
  assert.ok(text.includes('不是零外呼处理'), 'summary is not represented as zero outbound')
  assert.ok(text.includes('按实际 Token 计费'), 'remote/model-summary actual-token charging remains visible')
  assert.ok(text.includes('管理员') && text.includes('账单'), 'administrator billing warning remains visible')
}
afterEach(async () => { wrapper?.unmount(); wrapper = undefined; Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren(); user.setStoredCurrentUser(null); calls.length = pending.length = 0 })

test('merged native context window is editable/displayed; omitted legacy window preserves concurrent fill and explicit zero clears', async () => {
  const w = await start(), table = w.element.querySelector('.price-table-wrap')
  assert.ok(table.textContent.includes('上下文窗口（Token）')); assert.ok(table.textContent.includes('131,072'))
  button(table.querySelector('tr[data-row-key="7"]'), '编辑').click(); await settle()
  assert.equal(field(priceModal(), '上下文窗口').querySelector('input').value, '131072')
  await input(priceModal(), '上下文窗口', '1048576'); button(priceModal(), '确定').click(); await settle()
  assert.equal(calls.at(-1)[1].contextWindowTokens, 1048576)
  button(table.querySelector('tr[data-row-key="8"]'), '编辑').click(); await settle()
  assert.equal(w.vm.priceForm.contextWindowTokens, undefined)
  button(priceModal(), '确定').click(); await settle()
  assert.equal(Object.hasOwn(calls.at(-1)[1], 'contextWindowTokens'), false); assert.equal(serverWindow, 1048576, 'legacy edit cannot clear another admin window')
  button(table.querySelector('tr[data-row-key="8"]'), '编辑').click(); await settle()
  await input(priceModal(), '上下文窗口', '0'); button(priceModal(), '确定').click(); await settle()
  assert.equal(calls.at(-1)[1].contextWindowTokens, 0); assert.equal(serverWindow, 0)
  console.log('UPSTREAM_WINDOW_LEDGER ' + JSON.stringify(calls))
})

test('compression native controls use the existing controlled child/canonical draft and save all five fields with fixed sampling', async () => {
  const w = await start(), canonical = w.vm.formState
  assert.equal(w.findAllComponents(require(path.join(web, 'src/pages/AISearchConfig/AIAdvancedSettingsSection.vue'))).length, 1)
  const orderField = field(w.element, '压缩执行顺序'), remoteHandle = orderField.querySelector('[data-strategy="remote"] .compression-order-handle')
  assert.ok(remoteHandle && !remoteHandle.disabled, 'actual controlled Order native keyboard entry')
  remoteHandle.focus(); assert.equal(document.activeElement, remoteHandle)
  remoteHandle.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', code: 'ArrowUp', bubbles: true, cancelable: true })); await settle()
  const expectedOrder = ['remote', 'server', 'local']
  assert.deepEqual(w.vm.formState.contextCompressionOrder, expectedOrder, 'native ArrowUp creates a nondefault full order')
  assert.deepEqual([...orderField.querySelectorAll('[data-strategy]')].map(node => node.dataset.strategy), expectedOrder)
  await input(w.element, '压缩触发比例', 85); await input(w.element, '压缩目标比例', 45); await input(w.element, '压缩最大尝试次数', 4)
  assert.equal(w.vm.formState, canonical); assert.equal(w.vm.hasUnsavedChanges, true); assert.equal(calls.length, 0)
  user.setStoredCurrentUser({ id: 37, username: 'fixture-admin', role: 'admin', balance: 9.123456, nickname: 'SYNTHETIC_METADATA' }); await settle()
  assert.equal(w.vm.formState, canonical); assert.deepEqual(w.vm.formState.contextCompressionOrder, expectedOrder); assert.equal(w.vm.hasUnsavedChanges, true); assert.equal(calls.length, 0, 'same-actor metadata does not save, test or overwrite draft')
  assert.ok(field(w.element, '自动压缩上下文').textContent.includes('额外请求当前模型'))
  assert.ok(orderField.textContent.includes('本地模型摘要仍会外呼当前模型')); assert.ok(orderField.textContent.includes('按实际 Token 计费'))
  button(w.element, '保存配置').click(); await settle()
  const payload = calls.at(-1)[1]
  assert.deepEqual(Object.fromEntries(['contextCompressionEnabled','useRemoteContextCompression','contextCompressionTriggerPercent','contextCompressionTargetPercent','contextCompressionMaxAttempts','temperature','topP'].map(key => [key, payload[key]])), { contextCompressionEnabled: true, useRemoteContextCompression: false, contextCompressionTriggerPercent: 85, contextCompressionTargetPercent: 45, contextCompressionMaxAttempts: 4, temperature: 0, topP: 0 })
  assert.deepEqual(payload.contextCompressionOrder, expectedOrder); assert.deepEqual(serverConfig.contextCompressionOrder, expectedOrder); assert.deepEqual(w.vm.savedCompressionOrder, expectedOrder, 'successful save reads back the actual nondefault order')
  assert.equal(w.vm.formState, canonical); assert.equal(w.vm.hasUnsavedChanges, false)
  field(w.element, '自动压缩上下文').querySelector('button').click(); await settle()
  assert.equal(w.vm.formState.contextCompressionEnabled, false); assert.equal([...w.element.querySelectorAll('.ant-form-item-label')].some(x => clean(x.textContent) === '压缩执行顺序'), false)
  console.log('UPSTREAM_COMPRESSION_LEDGER ' + JSON.stringify(payload))
})

test('supplemental invalid-value boundary: confirmed budgets and compression validation reject without API or dirty-state loss', async () => {
  const w = await start()
  for (const [key, bad] of [['maxAISearchModelCalls',21],['maxAISearches',21],['maxAISearchRetries',11],['maxAIWebReads',21],['contextCompressionTargetPercent',80]]) {
    const previous = w.vm.formState[key]; w.vm.formState[key] = bad; w.vm.submitSave(CONFIG.baseUrl); await settle(); assert.equal(calls.length, 0, key); assert.equal(w.vm.formState[key], bad); w.vm.formState[key] = previous
  }
})

for (const mode of ['hello','direct','ai','compression','ai_compression']) test('upstream native ' + mode + ' mode is explicit-only and returns mock diagnostics', async () => {
  await start(); const d = await openMode(mode); assert.equal(calls.length, 0, 'mount/open/mode selection never calls a provider')
  assert.equal(d.testType, mode); if (mode.includes('compression')) assertCompressionWarning()
  await fillRequiredTarget(mode, 'CURRENT'); assert.equal(calls.length, 0, 'typing a valid target never submits automatically')
  if (mode === 'ai_compression') { assert.equal(d.form.tool, 'none'); assert.equal(d.form.compressionMode, 'configured') }
  send(); await settle(); assert.equal(calls.length, 1); assert.equal(pending.length, 1, 'one actual admitted API promise'); assert.equal(d.submitting, true)
  assert.equal(calls[0][0], mode === 'hello' ? 'hello' : 'tool')
  if (mode !== 'hello') assert.equal(calls[0][1].mode, mode === 'direct' ? 'direct' : mode)
  if (mode === 'direct') assert.equal(calls[0][1].query, 'SYNTHETIC_DIRECT_CURRENT')
  if (mode === 'ai') assert.equal(calls[0][1].target, 'SYNTHETIC_AI_CURRENT')
  if (mode.includes('compression')) {
    assert.equal(calls[0][1].compressionMode, 'configured'); assert.equal(calls[0][1].history, d.form.history.trim()); assert.equal(calls[0][1].model, 'MODEL-A')
    if (mode === 'ai_compression') { assert.equal(calls[0][1].tool, 'none'); assert.equal(calls[0][1].query, ''); assert.equal(calls[0][1].url, '') }
  }
  const withNetwork = ['direct', 'ai'].includes(mode)
  pending.shift().resolve(result('SYNTHETIC_' + mode, withNetwork)); await settle()
  assert.equal(d.submitting, false); assert.ok(d.result); assert.deepEqual(d.result.answers, ['SYNTHETIC_' + mode]); assert.ok(modal().textContent.includes('SYNTHETIC_RAW'))
  if (mode.includes('compression')) assert.ok(modal().textContent.includes('SYNTHETIC_SUMMARY'))
  if (['direct','ai'].includes(mode)) assert.ok(wrapper.findComponent(require(path.join(web, 'src/pages/AISearchConfig/AIToolTestResults.vue'))).exists())
  if (mode === 'ai_compression') { assert.deepEqual(d.result.toolCalls, []); assert.equal(wrapper.findComponent(require(path.join(web, 'src/pages/AISearchConfig/AIToolTestResults.vue'))).exists(), false, 'default none does not fabricate a networking tool result') }
})

for (const mode of ['direct','ai','compression','ai_compression']) for (const outcome of ['resolve','reject']) test('upstream ' + mode + ': reopened B owns busy/result after old A ' + outcome, async () => {
  await start(); const d = await openMode(mode); await fillRequiredTarget(mode, 'A'); send(); await settle()
  assert.equal(calls.length, 1); assert.equal(pending.length, 1, 'A is a genuine admitted pending API request'); assert.equal(d.submitting, true)
  const a = pending[0]
  button(modal(), '关闭').click(); assert.equal(d.submitting, false, 'native close retires busy synchronously'); await settle()
  await openMode(mode); await fillRequiredTarget(mode, 'B'); send(); await settle()
  assert.equal(calls.length, 2); assert.equal(pending.length, 2, 'A and B both reached actual API admission before settling A'); const b = pending[1]; assert.equal(d.submitting, true)
  if (mode === 'direct') { assert.equal(calls[0][1].query, 'SYNTHETIC_DIRECT_A'); assert.equal(calls[1][1].query, 'SYNTHETIC_DIRECT_B') }
  if (mode === 'ai') { assert.equal(calls[0][1].target, 'SYNTHETIC_AI_A'); assert.equal(calls[1][1].target, 'SYNTHETIC_AI_B') }
  if (outcome === 'resolve') a.resolve(result('SYNTHETIC_SECRET_A', !mode.includes('compression'))); else a.reject(new Error('SYNTHETIC_SECRET_A'))
  await settle(); assert.equal(d.submitting, true); assert.equal(d.result, null); assert.equal(d.error, ''); assert.ok(!document.body.textContent.includes('SYNTHETIC_SECRET_A')); assert.equal(calls.length, 2, 'old completion does not replay calls')
  b.resolve(result('SYNTHETIC_B', !mode.includes('compression'))); await settle(); assert.equal(d.submitting, false); assert.deepEqual(d.result.answers, ['SYNTHETIC_B']); assert.equal(calls.length, 2)
})

test('legacy remote true and false remain helper/save-payload compatible while saved nondefault order remains authoritative', async () => {
  const order = ['local', 'server', 'remote']
  for (const legacy of [true, false]) {
    const settings = compressionHelpers.buildAIContextCompressionSettings({ ...CONFIG, useRemoteContextCompression: legacy, contextCompressionOrder: order })
    assert.equal(settings.useRemoteContextCompression, legacy); assert.deepEqual(settings.contextCompressionOrder, order)
    const w = await start({ useRemoteContextCompression: legacy, contextCompressionOrder: order }), count = calls.length
    assert.equal(w.vm.formState.useRemoteContextCompression, legacy); assert.deepEqual(w.vm.formState.contextCompressionOrder, order)
    await input(w.element, '压缩触发比例', legacy ? 81 : 82); button(w.element, '保存配置').click(); await settle()
    assert.equal(calls.length, count + 1); assert.equal(calls.at(-1)[0], 'save-config'); assert.equal(calls.at(-1)[1].useRemoteContextCompression, legacy); assert.deepEqual(calls.at(-1)[1].contextCompressionOrder, order); assert.equal(calls.at(-1)[1].temperature, 0); assert.equal(calls.at(-1)[1].topP, 0); assert.equal(w.vm.hasUnsavedChanges, false)
    w.unmount(); wrapper = undefined; await settle()
  }
  assert.equal(calls.length, 2, 'two explicit saves, no automatic model tests')
  for (const compressionMode of ['configured', 'server', 'remote', 'local']) {
    const payload = compressionHelpers.buildAIToolTestPayload({ mode: 'ai_compression', tool: 'none', history: 'SYNTHETIC_HISTORY', model: 'MODEL-A', compressionMode }, CONFIG)
    assert.equal(payload.compressionMode, compressionMode); assert.equal(payload.tool, 'none'); assert.equal(payload.query, ''); assert.equal(payload.url, ''); assert.equal(payload.history, 'SYNTHETIC_HISTORY'); assert.equal(payload.model, 'MODEL-A')
  }
  assert.throws(() => compressionHelpers.buildAIContextCompressionSettings({ ...CONFIG, contextCompressionOrder: ['server', 'server', 'local'] }), /不能重复/)
})

for (const mode of ['direct', 'ai']) test('upstream native ' + mode + ' empty/invalid target is rejected before API admission', async () => {
  await start(); const d = await openMode(mode), values = mode === 'direct' ? ['', 'x'.repeat(121)] : ['', 'x'.repeat(121), 'https://synthetic:invalid@fixture.invalid/page', 'https://fixture.invalid/page#fragment']
  for (const value of values) {
    await input(modal(), mode === 'direct' ? '搜索词' : '关键词或网页地址', value); send(); await settle()
    assert.equal(calls.length, 0); assert.equal(pending.length, 0); assert.equal(d.submitting, false); assert.equal(d.result, null); assert.ok(d.error, 'existing explicit client-validation error remains visible')
  }
})

test('AI compression networking is separate explicit native tool and target, not the safe default history reread', async () => {
  await start(); const d = await openMode('ai_compression'); assert.equal(d.form.tool, 'none'); assert.equal(d.form.compressionMode, 'configured'); assert.equal(calls.length, 0); assertCompressionWarning()
  const radio = field(modal(), '测试工具').querySelector('input[type="radio"][value="web_search"]'); assert.ok(radio); radio.click(); await settle()
  await input(modal(), '关键词或网页地址', 'SYNTHETIC_EXPLICIT_NETWORK_TARGET'); assert.equal(calls.length, 0); send(); await settle()
  assert.equal(calls.length, 1); assert.equal(pending.length, 1); assert.equal(d.submitting, true)
  assert.equal(calls[0][1].mode, 'ai_compression'); assert.equal(calls[0][1].tool, 'web_search'); assert.equal(calls[0][1].target, 'SYNTHETIC_EXPLICIT_NETWORK_TARGET'); assert.equal(calls[0][1].compressionMode, 'configured'); assert.equal(calls[0][1].history, d.form.history.trim())
  pending.shift().resolve(result('SYNTHETIC_EXPLICIT_NETWORK', true)); await settle()
  assert.equal(d.submitting, false); assert.ok(wrapper.findComponent(require(path.join(web, 'src/pages/AISearchConfig/AIToolTestResults.vue'))).exists()); assert.equal(d.result.toolCalls.length, 1); assert.ok(modal().textContent.includes('SYNTHETIC_SUMMARY')); assert.deepEqual(d.result.answers, ['SYNTHETIC_EXPLICIT_NETWORK'])
})
