const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), Module = require('node:module'), crypto = require('node:crypto')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, h } = require('vue'), Antd = require('ant-design-vue')
const { createRouter, createMemoryHistory, RouterView } = require('vue-router')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const CONFIG = { enabled: false, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC_KEY', model: 'MODEL-A', backupModel: '', manualModels: ['MODEL-A', 'MODEL-B'], prompt: 'SYNTHETIC_PROMPT', remark: 'PRESERVE', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0, maxAIWebReads: 5, webReadPricePerCall: 0, enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: false, stagedTokenMultiplierPoints: [{ tokens: 0, multiplier: 2 }, { tokens: 1000, multiplier: 1 }] }
const ROWS = [{ id: 501, modelName: 'MODEL-A', displayName: 'A', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0, outputPrice: 0, multiplier: 1, remark: '' }]
const calls = [], pendingModels = [], pendingConfig = [], wrappers = []; let deferredConfig = false, serverConfig = structuredClone(CONFIG), router
const ok = data => ({ status: 200, data }), apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
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
 getAISearchConfig: () => { calls.push(['read-config']); return deferredConfig ? new Promise(resolve => pendingConfig.push(resolve)) : Promise.resolve(ok(structuredClone(serverConfig))) },
 getAIModelPrices: () => { calls.push(['read-prices']); return Promise.resolve(ok(structuredClone(ROWS))) },
 getUAPISearchEngines: () => Promise.resolve(ok({})), getUAPIUsageStatus: () => Promise.resolve(ok({ endpoints: [] })),
 getAISearchModels: params => { calls.push(['fetch-models', structuredClone(params)]); return new Promise(resolve => pendingModels.push(resolve)) },
 updateAISearchConfig: payload => { calls.push(['save-config', structuredClone(payload)]); serverConfig = structuredClone(payload); return Promise.resolve(ok({})) },
 testAISearchConfig: payload => { calls.push(['test-provider', structuredClone(payload)]); return Promise.resolve(ok({ answers: ['SYNTHETIC'], requestBody: '{}', rawResponse: 'SYNTHETIC' })) }
}
require.cache[apiPath] = api
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue'), childPath = path.join(web, 'src/pages/AISearchConfig/AIConnectionModelSection.vue')
console.log('CONNECTION_SOURCE ' + JSON.stringify({ source: pagePath, sha256: crypto.createHash('sha256').update(fs.readFileSync(pagePath)).digest('hex'), node: process.version, qualification: 'native Antd4 jsdom; not real-browser or provider acceptance' }))
const clean = x => String(x || '').replace(/\s+/g, '')
const clone = x => JSON.parse(JSON.stringify(x))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
function button(root, text) { const e = [...root.querySelectorAll('button')].find(x => clean(x.textContent) === clean(text)); assert.ok(e, 'native button ' + text); return e }
function item(w, label) { const i = w.findAll('.ant-form-item').find(x => x.find('.ant-form-item-label').exists() && clean(x.find('.ant-form-item-label').text()) === clean(label)); assert.ok(i, 'field ' + label); return i }
function connection(w) { return w.findAll('section').find(x => x.find('h2').exists() && x.find('h2').text() === '连接与模型') }
function actions() { return calls.filter(x => ['fetch-models', 'test-provider', 'save-config'].includes(x[0])) }
async function start(route = false) {
 const ocrReadStart = ocrStatusReads
 user.setStoredCurrentUser({ id: 37, username: 'fixture-admin', role: 'admin', balance: 1 })
 let outer, w
 if (route) {
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/ai-search-config', component: require(pagePath) }, { path: '/adapter/user-usage', component: { render: () => h('p', 'Personal workspace') } }] })
  await router.push('/adapter/ai-search-config'); await router.isReady()
  outer = mount({ render: () => h(RouterView) }, { attachTo: document.body, global: { plugins: [router, Antd] } }); w = outer.findComponent(require(pagePath))
 } else outer = w = mount(require(pagePath), { attachTo: document.body, global: { plugins: [Antd] } })
 wrappers.push(outer); await settle(); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return w
}
afterEach(async () => {
 for (const w of wrappers.splice(0)) w.unmount()
 Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); router?.options.history.destroy(); router = undefined; document.body.replaceChildren(); user.setStoredCurrentUser(null)
 calls.length = pendingModels.length = pendingConfig.length = 0; deferredConfig = false; serverConfig = structuredClone(CONFIG)
})
test('positive native UI: five connection fields retain complete order, zero/bool values, attributes and readonly sampling', async () => {
 const w = await start(), canonical = w.vm.formState, snap = w.vm.savedConfigSnapshot
 assert.deepEqual(w.findAll('.ai-config-form .ant-form-item-label').map(x => x.text()), ['是否启用', '服务地址', 'API Key', '默认模型', '备用模型', '模型列表', '模型最大调用次数', '最大搜索次数', '每轮重试次数', '联网搜索单价（元/次）', '本地题库检索单价（元/次）', '网页读取次数', '网页读取单价（元/次）', '图片 OCR 单价（元/次）', '自动压缩上下文', '压缩执行顺序', '压缩触发比例', '压缩目标比例', '压缩最大尝试次数', 'Prompt 设置', '温度', 'Top P'])
 const region = connection(w); assert.equal(region.attributes('tabindex'), '-1'); assert.equal(region.attributes('aria-labelledby'), region.find('h2').attributes('id'))
 assert.equal(w.findAll('form.ai-config-form').length, 1)
 const endpoint = item(w, '服务地址').get('input'); assert.equal(endpoint.attributes('name'), 'ai-service-endpoint'); assert.equal(endpoint.attributes('type'), 'url'); assert.equal(endpoint.attributes('autocomplete'), 'off'); assert.equal(endpoint.attributes('autocapitalize'), 'none'); assert.equal(endpoint.attributes('spellcheck'), 'false')
 const key = item(w, 'API Key').get('input'); assert.equal(key.attributes('name'), 'ai-service-api-key'); assert.equal(key.attributes('autocomplete'), 'new-password'); assert.equal(key.attributes('autocapitalize'), 'none'); assert.equal(key.attributes('spellcheck'), 'false')
 assert.equal(item(w, '是否启用').get('button').attributes('aria-checked'), 'false')
 for (const label of ['温度', 'Top P']) { const e = item(w, label).get('input').element; assert.equal(e.value, '0'); assert.equal(e.readOnly, true); assert.equal(e.disabled, true) }
 assert.equal(w.vm.formState.searchPricePerCall, 0); assert.equal(w.vm.formState.smoothAIStagedTokenMultiplier, false); assert.equal(w.vm.formState, canonical); assert.equal(w.vm.savedConfigSnapshot, snap); assert.equal(w.vm.hasUnsavedChanges, false); assert.deepEqual(actions(), [])
 console.log('CONNECTION_POSITIVE ' + JSON.stringify({ fields: w.findAll('.ai-config-form .ant-form-item-label').length, connectionFields: connection(w).findAll('.ant-form-item-label').length, zero: w.vm.formState.searchPricePerCall, enabled: w.vm.formState.enabled, identity: w.vm.formState === canonical, snapshotsEqual: w.vm.currentConfigSnapshot === snap, actions: actions() }))
})
test('positive native UI: actual input and model-tag selection preserve canonical draft identity and dirty/revert', async () => {
 const w = await start(), canonical = w.vm.formState, before = clone(canonical), snap = w.vm.savedConfigSnapshot
 for (const [label, value, field] of [['服务地址', 'https://changed.invalid/v1', 'baseUrl'], ['API Key', 'SYNTHETIC_CHANGED', 'apiKey'], ['默认模型', 'MODEL-X', 'model'], ['备用模型', 'MODEL-B', 'backupModel'], ['模型列表', 'MODEL-A,MODEL-X', 'manualModelsText']]) {
  await item(w, label).get(label === '模型列表' ? 'textarea' : 'input').setValue(value); assert.equal(w.vm.formState[field], value); assert.equal(w.vm.formState, canonical)
 }
 await item(w, '是否启用').get('button').trigger('click'); assert.equal(w.vm.formState.enabled, true); assert.equal(w.vm.hasUnsavedChanges, true); assert.equal(w.vm.savedConfigSnapshot, snap)
 await connection(w).findAll('.ant-tag').find(x => x.text() === 'MODEL-A').trigger('click'); assert.equal(w.vm.formState.model, 'MODEL-A'); assert.equal(w.vm.formState.backupModel, 'MODEL-B', 'main selection cannot overwrite backup')
 for (const field of Object.keys(before).filter(x => !['enabled', 'baseUrl', 'apiKey', 'model', 'backupModel', 'manualModelsText'].includes(x))) assert.deepEqual(clone(canonical[field]), before[field], 'nonowned field ' + field)
 Object.assign(canonical, before); await settle(); assert.equal(w.vm.formState, canonical); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(w.vm.currentConfigSnapshot, snap); assert.deepEqual(actions(), [])
 console.log('CONNECTION_DRAFT ' + JSON.stringify({ identity: canonical === w.vm.formState, reverted: !w.vm.hasUnsavedChanges, actions: actions() }))
})
test('private extraction boundary: new controlled child exists and is mounted once (structural contract, not baseline product bug)', async () => {
 assert.equal(fs.existsSync(childPath), true, 'expected RED: private controlled connection child boundary is absent; baseline UI positives are separate')
 const w = await start(), Child = require(childPath)
 assert.equal(w.findAllComponents(Child).length, 1, 'parent mounts exactly one controlled section')
 assert.equal(w.findComponent(Child).props('formValue'), w.vm.formState)
})


test('positive native UI: tags/pricing CTA/test/fetch delegate only on explicit clicks with exact parent requests', async () => {
 const w = await start(), canonical = w.vm.formState
 const tags = connection(w).findAll('.ant-tag')
 assert.equal(tags[0].text(), 'MODEL-A'); assert.ok(tags[0].classes().includes('ant-tag-blue'))
 assert.equal(tags[1].text(), 'MODEL-B（未定价）'); assert.ok(tags[1].classes().includes('ant-tag-orange'))
 await tags[1].trigger('click'); assert.equal(canonical.model, 'MODEL-B'); assert.deepEqual(actions(), [])
 const pricing = connection(w).findAll('a').find(x => x.text() === '去设置价格'); assert.ok(pricing)
 await pricing.trigger('click'); await settle(); assert.equal(w.vm.priceModalVisible, true); assert.equal(w.vm.priceForm.modelName, 'MODEL-B'); assert.deepEqual(actions(), [])
 const priceModal = [...document.querySelectorAll('.ant-modal')].find(x => /模型价格$/.test(clean(x.querySelector('.ant-modal-title')?.textContent)))
 button(priceModal, '取消').click(); await settle()
 await item(w, '服务地址').get('input').setValue(' https://fixture.changed.invalid/v1/// ')
 await item(w, 'API Key').get('input').setValue(' SYNTHETIC_TRIM ')
 button(connection(w).element, '获取远程模型列表').click(); await settle()
 assert.deepEqual(actions(), [['fetch-models', { url: 'https://fixture.changed.invalid/v1', key: 'SYNTHETIC_TRIM' }]])
 assert.equal(w.vm.fetchingModels, true); assert.ok(button(connection(w).element, '获取远程模型列表').classList.contains('ant-btn-loading'))
 pendingModels.shift()(ok({ models: ['MODEL-C', 'MODEL-D'] })); await settle()
 assert.equal(w.vm.fetchingModels, false); assert.equal(w.vm.formState, canonical); assert.deepEqual(w.vm.modelOptions.map(x => x.value), ['MODEL-C', 'MODEL-D'])
 button(connection(w).element, '测试 AI、联网与 OCR 工具').click(); await settle(); assert.equal(w.vm.testModalVisible, true)
 assert.equal(actions().filter(x => x[0] === 'test-provider').length, 0, 'opening test must not contact provider')
 for (const [key, value] of Object.entries({ baseUrl: 'https://fixture.changed.invalid/v1///', apiKey: 'SYNTHETIC_TRIM', model: 'MODEL-B', prompt: CONFIG.prompt })) assert.equal(w.findComponent(require(path.join(web, 'src/pages/AISearchConfig/AISearchTestDialog.vue'))).vm.form[key], value)
 const testModal = [...document.querySelectorAll('.ant-modal')].find(x => clean(x.querySelector('.ant-modal-title')?.textContent) === '测试AI、联网与OCR工具')
 button(testModal, '发送 hello 测试').click(); await settle()
 assert.deepEqual(actions().filter(x => x[0] === 'test-provider'), [['test-provider', { baseUrl: 'https://fixture.changed.invalid/v1///', apiKey: 'SYNTHETIC_TRIM', model: 'MODEL-B', prompt: CONFIG.prompt }]])
 if (fs.existsSync(childPath)) {
  const child = w.findComponent(require(childPath))
  for (const event of ['select-model', 'price-for-configured-model', 'fetch-models', 'test']) assert.equal(child.emitted(event).length, 1, event + ' delegated once')
  assert.deepEqual(child.emitted('select-model'), [['MODEL-B']])
 }
 console.log('CONNECTION_EXPLICIT_ACTIONS ' + JSON.stringify({ calls: actions(), canonical: w.vm.formState === canonical, noAutoProvider: true }))
})
test('positive native UI: saved connection values and untouched budgets reconcile without replacing canonical draft', async () => {
 const w = await start(), canonical = w.vm.formState, before = clone(canonical)
 await item(w, 'API Key').get('input').setValue('SYNTHETIC_SAVED')
 await item(w, '是否启用').get('button').trigger('click')
 button(w.element, '保存配置').click(); await settle()
 const save = actions().find(x => x[0] === 'save-config'); assert.ok(save)
 assert.equal(save[1].apiKey, 'SYNTHETIC_SAVED'); assert.equal(save[1].enabled, true); assert.deepEqual(save[1].manualModels, ['MODEL-A', 'MODEL-B'])
 for (const field of ['searchPricePerCall', 'webReadPricePerCall', 'temperature', 'topP', 'maxAISearchModelCalls', 'maxAISearches', 'maxAISearchRetries', 'maxAIWebReads', 'enableAIStagedTokenBilling', 'smoothAIStagedTokenMultiplier']) assert.equal(save[1][field], before[field], field)
 assert.equal(w.vm.formState, canonical); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(w.vm.currentConfigSnapshot, w.vm.savedConfigSnapshot); assert.equal(actions().filter(x => x[0] === 'save-config').length, 1)
 console.log('CONNECTION_SAVE ' + JSON.stringify({ payload: save[1], canonical: w.vm.formState === canonical, dirty: w.vm.hasUnsavedChanges }))
})
test('positive native UI: dirty connection refresh cancel retains edit and confirm reloads using same canonical state', async () => {
 const w = await start(), canonical = w.vm.formState
 await item(w, 'API Key').get('input').setValue('SYNTHETIC_UNSAVED')
 let reads = calls.filter(x => x[0] === 'read-config').length
 button(w.element, '刷新').click(); await settle()
 let modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); assert.ok(modal)
 button(modal, '保留编辑').click(); await settle(); assert.equal(canonical.apiKey, 'SYNTHETIC_UNSAVED'); assert.equal(calls.filter(x => x[0] === 'read-config').length, reads)
 button(w.element, '刷新').click(); await settle(); modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); button(modal, '放弃并刷新').click(); await settle()
 assert.equal(w.vm.formState, canonical); assert.equal(canonical.apiKey, CONFIG.apiKey); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(calls.filter(x => x[0] === 'read-config').length, reads + 1); assert.deepEqual(actions(), [])
})
test('positive native UI: dirty connection route leave cancel retains edit, explicit discard leaves', async () => {
 const w = await start(true), canonical = w.vm.formState
 await item(w, 'API Key').get('input').setValue('SYNTHETIC_ROUTE')
 let leaving = router.push('/adapter/user-usage'); await settle()
 let modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); assert.ok(modal); button(modal, '保留编辑').click(); await leaving; await settle()
 assert.equal(router.currentRoute.value.path, '/adapter/ai-search-config'); assert.equal(w.vm.formState, canonical); assert.equal(canonical.apiKey, 'SYNTHETIC_ROUTE')
 leaving = router.push('/adapter/user-usage'); await settle(); modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); button(modal, '放弃并离开').click(); await leaving; await settle()
 assert.equal(router.currentRoute.value.path, '/adapter/user-usage'); assert.deepEqual(actions(), [])
})
function freezeDeep(value) { Object.freeze(value); for (const x of Object.values(value)) if (x && typeof x === 'object' && !Object.isFrozen(x)) freezeDeep(x); return value }
function childProps(id = 'isolated-connection') {
 return { sectionId: id, formValue: freezeDeep({ ...structuredClone(CONFIG), manualModelsText: 'MODEL-A\nMODEL-B', preservedUnknown: { nested: ['PRESERVE', 0, false] } }), fetchingModels: false, modelOptions: [{ label: 'A', value: 'MODEL-A' }, { label: 'B', value: 'MODEL-B' }], priceRows: structuredClone(ROWS), pricingGaps: { configuredModel: 'MODEL-B', configuredModelPriced: false, unresolvedModels: ['MODEL-B'] } }
}
async function isolated(props) { const w = mount(require(childPath), { props, attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); return w }
test('controlled child: frozen props emit full independent object with changed owned field, preserving every nonowned value', { skip: !fs.existsSync(childPath) && 'baseline private child is intentionally absent; explicit structural RED above' }, async () => {
 const props = childProps(), before = clone(props.formValue), w = await isolated(props), requestCount = calls.length
 assert.equal(w.attributes('id'), props.sectionId); assert.equal(w.attributes('tabindex'), '-1'); assert.equal(w.attributes('aria-labelledby'), props.sectionId + '-heading')
 for (const [label, value, field] of [['服务地址', 'https://isolated.invalid/v1', 'baseUrl'], ['API Key', 'SYNTHETIC_CHILD', 'apiKey'], ['默认模型', 'MODEL-NEW', 'model'], ['备用模型', 'MODEL-BACKUP', 'backupModel'], ['模型列表', 'MODEL-C,MODEL-D', 'manualModelsText']]) {
  const previous = (w.emitted('update:form-value') || []).length
  // VTU setValue dispatches input+change; frozen controlled props deliberately do not echo updates.
  // Drive one native input event, matching the one-update contract rather than two synthetic events.
  const input = item(w, label).get(label === '模型列表' ? 'textarea' : 'input'); input.element.value = value; await input.trigger('input')
  const emitted = w.emitted('update:form-value'); assert.equal(emitted.length, previous + 1)
  const payload = emitted.at(-1)[0]; assert.notEqual(payload, props.formValue); assert.deepEqual(payload, { ...before, [field]: value }); assert.deepEqual(props.formValue, before)
 }
 await item(w, '是否启用').get('button').trigger('click'); assert.deepEqual(w.emitted('update:form-value').at(-1)[0], { ...before, enabled: true }); assert.deepEqual(props.formValue, before); assert.equal(calls.length, requestCount, 'standalone inputs cannot issue requests')
})
test('controlled child: parent prop changes update empty/loading/tags/pricing, actions emit once and two instances isolate', { skip: !fs.existsSync(childPath) && 'baseline private child is intentionally absent; explicit structural RED above' }, async () => {
 const leftProps = childProps('isolated-left'), rightProps = childProps('isolated-right')
 const left = await isolated(leftProps), right = await isolated(rightProps), count = calls.length
 for (const [event, text] of [['test', '测试 AI、联网与 OCR 工具'], ['fetch-models', '获取远程模型列表']]) { button(left.element, text).click(); await settle(); assert.deepEqual(left.emitted(event), [[]]); assert.equal(right.emitted(event), undefined) }
 await left.findAll('a').find(x => x.text() === '去设置价格').trigger('click'); assert.deepEqual(left.emitted('price-for-configured-model'), [[]])
 await left.findAll('.ant-tag')[1].trigger('click'); assert.deepEqual(left.emitted('select-model'), [['MODEL-B']]); assert.equal(right.emitted('select-model'), undefined)
 assert.deepEqual(leftProps.formValue, rightProps.formValue); assert.equal(left.emitted('update:form-value'), undefined, 'tag selection delegates rather than mutating form')
 await left.setProps({ modelOptions: [], fetchingModels: true, pricingGaps: { configuredModel: '', configuredModelPriced: false, unresolvedModels: [] } }); await settle()
 assert.ok(left.text().includes('暂无模型列表，请先手动填写或获取远程模型。')); assert.equal(left.findAll('.ant-tag').length, 0); assert.ok(button(left.element, '获取远程模型列表').classList.contains('ant-btn-loading'))
 assert.equal(left.findAll('a').length, 0); assert.equal(right.findAll('.ant-tag').length, 2)
 await left.setProps({ modelOptions: [{ label: 'Replacement', value: 'MODEL-C' }], priceRows: [{ modelName: 'MODEL-C' }], fetchingModels: false }); await settle()
 assert.equal(left.find('.ant-tag').text(), 'Replacement'); assert.ok(left.find('.ant-tag').classes().includes('ant-tag-blue')); assert.equal(calls.length, count, 'standalone mount, props and explicit delegated clicks issue zero API calls')
 assert.equal(new Set([left.attributes('id'), right.attributes('id'), left.find('h2').attributes('id'), right.find('h2').attributes('id')]).size, 4)
 console.log('CONNECTION_CHILD ' + JSON.stringify({ emits: left.emitted(), noNetwork: calls.length === count, rightIsolated: !right.emitted('test') }))
})


test('native Antd4 jsdom security: API key eye toggles password/text/password without draft or request changes', async () => {
 const w = await start(), canonical = w.vm.formState, draft = clone(canonical), saved = w.vm.savedConfigSnapshot, current = w.vm.currentConfigSnapshot, apiCalls = clone(calls)
 const password = item(w, 'API Key'), input = password.get('input[name="ai-service-api-key"]')
 assert.equal(input.attributes('type'), 'password'); assert.equal(input.element.value, CONFIG.apiKey)
 assert.equal(input.attributes('autocomplete'), 'new-password'); assert.equal(input.attributes('autocapitalize'), 'none'); assert.equal(input.attributes('spellcheck'), 'false')
 const eye = password.get('.ant-input-password-icon')
 for (const type of ['text', 'password']) {
  await eye.trigger('click'); await settle()
  assert.equal(input.attributes('type'), type); assert.equal(input.element.value, CONFIG.apiKey)
  assert.equal(w.vm.formState, canonical); assert.deepEqual(clone(canonical), draft)
  assert.equal(w.vm.savedConfigSnapshot, saved); assert.equal(w.vm.currentConfigSnapshot, current); assert.equal(w.vm.hasUnsavedChanges, false)
  assert.deepEqual(calls, apiCalls, 'visibility-only UI must not trigger config/model/provider requests')
 }
 console.log('CONNECTION_PASSWORD_REVEAL ' + JSON.stringify({ qualification: 'native Antd4 jsdom; not real-browser', types: ['password', 'text', 'password'], canonical: w.vm.formState === canonical, snapshotUnchanged: w.vm.currentConfigSnapshot === current, requestsAdded: calls.length - apiCalls.length }))
})


test('native main and backup intents use the actual shared test-model select without changing canonical connection or issuing automatic provider requests', async () => {
 const w = await start(), canonical = w.vm.formState, original = clone(canonical)
 await item(w, '备用模型').get('input').setValue('MODEL-B'); await settle()
 assert.equal(canonical.model, original.model); assert.equal(canonical.baseUrl, original.baseUrl); assert.equal(canonical.apiKey, original.apiKey); assert.equal(w.vm.formState, canonical)
 assert.deepEqual(actions(), [], 'editing backup is local controlled state only')
 button(connection(w).element, '测试 AI、联网与 OCR 工具').click(); await settle()
 const dialog = w.findComponent(require(path.join(web, 'src/pages/AISearchConfig/AISearchTestDialog.vue')))
 assert.equal(dialog.vm.form.model, 'MODEL-A'); assert.deepEqual(dialog.props('modelOptions').map(x => x.value), ['MODEL-A', 'MODEL-B'])
 assert.deepEqual(actions(), [], 'opening the actual dialog does not contact provider')
 const root = [...document.querySelectorAll('.ant-modal')].find(x => clean(x.querySelector('.ant-modal-title')?.textContent) === '测试AI、联网与OCR工具')
 button(root, '发送 hello 测试').click(); await settle()
 assert.deepEqual(actions().filter(x => x[0] === 'test-provider').map(x => x[1].model), ['MODEL-A'])
 const select = dialog.findComponent(Antd.Select); await select.get('.ant-select-selector').trigger('mousedown'); await settle()
 const popupID = select.get('input[role="combobox"]').attributes('aria-controls')
 assert.ok(popupID, 'actual test-model combobox owns a specific popup ID')
 const popup = document.getElementById(popupID)?.closest('.ant-select-dropdown')
 assert.ok(popup, 'only the test-model select popup, never a leftover connection AutoComplete')
 const option = [...popup.querySelectorAll('.ant-select-item-option')].find(x => x.getAttribute('title') === 'MODEL-B')
 assert.ok(option, 'exact native backup model option MODEL-B in its owned popup'); option.click(); await settle()
 assert.equal(dialog.vm.form.model, 'MODEL-B'); assert.equal(canonical.model, 'MODEL-A'); assert.equal(canonical.backupModel, 'MODEL-B')
 button(root, '发送 hello 测试').click(); await settle()
 assert.deepEqual(actions().filter(x => x[0] === 'test-provider').map(x => x[1].model), ['MODEL-A', 'MODEL-B'])
 assert.equal(actions().filter(x => x[0] !== 'test-provider').length, 0, 'no save, model discovery or other write')
 assert.deepEqual(w.findComponent(require(childPath)).emitted('test'), [[]], 'one exact entry event, not invented primary/backup buttons')
 assert.equal(w.vm.formState, canonical); assert.equal(canonical.baseUrl, original.baseUrl); assert.equal(canonical.apiKey, original.apiKey)
})
