const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), Module = require('node:module'), crypto = require('node:crypto')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const { buildAISearchConfigSavePayload } = require(path.join(web, 'src/utils/aiSearchModelHelpers.mjs'))
const CONFIG = { enabled: false, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC_KEY', model: 'MODEL-A', manualModels: ['MODEL-A', 'MODEL-B'], prompt: 'SYNTHETIC_PROMPT', remark: 'PRESERVE', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0, maxAIWebReads: 5, webReadPricePerCall: 0, enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: false, stagedTokenMultiplierPoints: [{ tokens: 0, multiplier: 2 }, { tokens: 1000, multiplier: 1 }] }
const ROWS = [{ id: 501, modelName: 'MODEL-A', displayName: 'A', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0, outputPrice: 0, multiplier: 1, remark: '' }]
const HINTS = ['AI 只应输出 JSON，格式固定为 {"answers":["答案1","答案2"]}，系统会自动解析并转换为当前题库接口兼容的答案结构。', '建议保留“只返回 JSON、信息不足返回不确定、优先返回与选项一致的答案文本”这一类约束，避免输出额外说明影响后续处理。', '如果你清空 Prompt 保存，后端会自动回退到内置默认 Prompt；下次加载时，页面会直接显示当前生效的默认内容。', '该输出还需要兼容现有题库聚合结构以及 OCSJS 的后续判断逻辑，所以不建议改成自然语言长段回答。', '固定为 0，用于降低随机性，让相同题目的返回结果更稳定。', '固定为 0，与温度一起保持低随机性，不开放自定义调整。']
const calls = [], wrappers = []; let serverConfig = structuredClone(CONFIG)
const clone = x => JSON.parse(JSON.stringify(x)), ok = data => ({ status: 200, data })
const apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
api.loaded = true
// Exactly the new readonly OCR status GET, never clear/write/provider.
let ocrStatusReads = 0
function syntheticOCRStatus(feedback) { assert.deepEqual(feedback, { errorFeedback: 'local' }); ocrStatusReads += 1; return Promise.resolve({ status: 200, data: { configured: false, entries: 0 } }) }
require('node:test').after(() => console.log('AI_OCR_STATUS_READS ' + JSON.stringify({ fixture: __filename, method: 'GET', count: ocrStatusReads, configured: false, entries: 0 })))
api.exports = { getAIOCRCacheStatus: syntheticOCRStatus,
 getAISearchConfig: () => { calls.push(['read-config']); return Promise.resolve(ok(structuredClone(serverConfig))) },
 getAIModelPrices: () => { calls.push(['read-prices']); return Promise.resolve(ok(structuredClone(ROWS))) },
 updateAISearchConfig: payload => { calls.push(['save-config', structuredClone(payload)]); serverConfig = { ...structuredClone(payload), prompt: payload.prompt || 'SYNTHETIC_EFFECTIVE_DEFAULT' }; return Promise.resolve(ok({})) },
 getAISearchModels: () => { throw Error('unexpected provider fixture call') }, testAISearchConfig: () => { throw Error('unexpected provider fixture call') },
 getUAPISearchEngines: () => { throw Error('unexpected capability fixture call') }, getUAPIUsageStatus: () => { throw Error('unexpected capability fixture call') }
}
require.cache[apiPath] = api
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue'), childPath = path.join(web, 'src/pages/AISearchConfig/AIPromptSettingsSection.vue')
console.log('PROMPT_SOURCE ' + JSON.stringify({ source: pagePath, sha256: crypto.createHash('sha256').update(fs.readFileSync(pagePath)).digest('hex'), node: process.version, qualification: 'native Antd4 jsdom only; no real-browser/CSS/provider/backend-fallback acceptance' }))
const clean = x => String(x || '').replace(/\s+/g, '')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
function button(root, text) { const e = [...root.querySelectorAll('button')].find(x => clean(x.textContent) === clean(text)); assert.ok(e, 'native button ' + text); return e }
function item(w, label) { const i = w.findAll('.ant-form-item').find(x => x.find('.ant-form-item-label').exists() && clean(x.find('.ant-form-item-label').text()) === clean(label)); assert.ok(i, 'field ' + label); return i }
function prompt(w) { return w.findAll('section').find(x => x.find('h2').exists() && x.find('h2').text() === 'Prompt') }
function mutations() { return calls.filter(x => !['read-config', 'read-prices'].includes(x[0])) }
async function input(w, value) { const el = item(w, 'Prompt 设置').get('textarea'); el.element.value = value; await el.trigger('input'); await settle() }
async function start() { const ocrReadStart = ocrStatusReads; user.setStoredCurrentUser({ id: 37, username: 'fixture-admin', role: 'admin', balance: 1 }); const w = mount(require(pagePath), { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return w }
afterEach(async () => { for (const w of wrappers.splice(0)) w.unmount(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren(); user.setStoredCurrentUser(null); calls.length = 0; serverConfig = structuredClone(CONFIG) })

test('positive native UI: Prompt keeps exact hints, IDs, order, textarea rows and readonly zero controls', async () => {
 const w = await start(), region = prompt(w), id = w.vm.sectionIds.prompt
 assert.equal(region.attributes('id'), id); assert.equal(region.attributes('tabindex'), '-1'); assert.equal(region.attributes('aria-labelledby'), id + '-heading'); assert.equal(region.find('h2').attributes('id'), id + '-heading')
 assert.deepEqual(w.findAll('nav.ai-section-nav a').map(x => x.text()), ['连接与模型', '高级能力', 'Prompt', '模型价格表']); assert.equal(w.get('nav.ai-section-nav a[href="#' + id + '"]').text(), 'Prompt')
 assert.equal(w.findAll('form.ai-config-form').length, 1); assert.equal(region.findAll('form').length, 0)
 assert.deepEqual(region.findAll('.ant-form-item-label').map(x => x.text()), ['Prompt 设置', '温度', 'Top P']); assert.deepEqual(region.findAll('.hint').map(x => x.text()), HINTS)
 const textarea = region.findComponent(Antd.Textarea); assert.deepEqual(textarea.props('autoSize'), { minRows: 10, maxRows: 18 }); assert.equal(textarea.props('placeholder'), '留空时将自动使用系统内置 Prompt'); assert.equal(textarea.get('textarea').element.value, CONFIG.prompt)
 for (const label of ['温度', 'Top P']) { const el = item(region, label).get('input').element; assert.equal(el.value, '0'); assert.equal(el.readOnly, true); assert.equal(el.disabled, true) }
 assert.equal(w.vm.hasUnsavedChanges, false); assert.deepEqual(mutations(), [])
 console.log('PROMPT_POSITIVE ' + JSON.stringify({ id, hints: HINTS, readonly: ['0', '0'], mutations: mutations() }))
})

test('positive native input: Prompt updates canonical state only, keeps snapshot, and reverts cleanly', async () => {
 const w = await start(), canonical = w.vm.formState, before = clone(canonical), snapshot = w.vm.savedConfigSnapshot
 await input(w, '编辑 Prompt\n{"answers":["A"]}')
 assert.equal(w.vm.formState, canonical); assert.equal(canonical.prompt, '编辑 Prompt\n{"answers":["A"]}'); assert.equal(w.vm.hasUnsavedChanges, true); assert.equal(w.vm.savedConfigSnapshot, snapshot)
 for (const field of Object.keys(before).filter(f => f !== 'prompt')) assert.deepEqual(clone(canonical[field]), before[field], 'preserve ' + field)
 await input(w, CONFIG.prompt); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(w.vm.currentConfigSnapshot, snapshot); assert.deepEqual(mutations(), [])
 console.log('PROMPT_DRAFT ' + JSON.stringify({ canonical: w.vm.formState === canonical, dirtyAfterRevert: w.vm.hasUnsavedChanges, mutations: mutations() }))
})

test('private extraction boundary: controlled Prompt child exists once; expected structural RED only', async () => {
 assert.equal(fs.existsSync(childPath), true, 'expected RED: private controlled Prompt child boundary is absent; not a baseline product bug')
 const w = await start(), Child = require(childPath), child = w.findComponent(Child)
 assert.equal(w.findAllComponents(Child).length, 1); assert.equal(child.props('formValue'), w.vm.formState); assert.equal(child.props('sectionId'), w.vm.sectionIds.prompt); assert.equal(child.props('fixedTemperatureText'), '0'); assert.equal(child.props('fixedTopPText'), '0')
})

test('positive parent save: canonical payload is unchanged and empty Prompt reloads synthetic effective default', async () => {
 const w = await start(), canonical = w.vm.formState
 for (const value of ['  saved Prompt  ', '']) {
  await input(w, value); const expected = clone({ ...buildAISearchConfigSavePayload(canonical, canonical.baseUrl), ...require(path.join(web, 'src/utils/aiSearchConfigHelpers.mjs')).buildAIContextCompressionSettings(canonical) }), count = mutations().length
  button(w.element, '保存配置').click(); await settle(); assert.equal(mutations().length, count + 1); assert.deepEqual(mutations().at(-1), ['save-config', expected]); assert.equal(w.vm.formState, canonical); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(w.vm.currentConfigSnapshot, w.vm.savedConfigSnapshot)
  assert.equal(item(w, 'Prompt 设置').get('textarea').element.value, value ? expected.prompt : 'SYNTHETIC_EFFECTIVE_DEFAULT')
 }
 assert.equal(mutations().at(-1)[1].prompt, ''); console.log('PROMPT_SAVE ' + JSON.stringify({ saves: mutations(), reload: canonical.prompt, backend: 'synthetic fixture only' }))
})

test('positive parent refresh: cancel retains Prompt and explicit discard reloads same canonical object', async () => {
 const w = await start(), canonical = w.vm.formState; await input(w, 'UNSAVED_PROMPT'); const reads = calls.filter(x => x[0] === 'read-config').length
 button(w.element, '刷新').click(); await settle(); let modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); button(modal, '保留编辑').click(); await settle(); assert.equal(canonical.prompt, 'UNSAVED_PROMPT'); assert.equal(calls.filter(x => x[0] === 'read-config').length, reads)
 button(w.element, '刷新').click(); await settle(); modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); button(modal, '放弃并刷新').click(); await settle(); assert.equal(w.vm.formState, canonical); assert.equal(canonical.prompt, CONFIG.prompt); assert.equal(w.vm.hasUnsavedChanges, false); assert.equal(calls.filter(x => x[0] === 'read-config').length, reads + 1); assert.deepEqual(mutations(), [])
})

test('positive parent leave: dirty Prompt cancel/discard decisions and login bypass preserve existing ownership', async () => {
 const w = await start(), canonical = w.vm.formState; assert.equal(w.vm.requestRouteLeave({ path: '/other' }), true); await input(w, 'LEAVE_PROMPT')
 let decision = w.vm.requestRouteLeave({ path: '/other' }); await settle(); let modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); button(modal, '保留编辑').click(); await settle(); assert.equal(await decision, false); assert.equal(canonical.prompt, 'LEAVE_PROMPT')
 decision = w.vm.requestRouteLeave({ path: '/other' }); await settle(); modal = [...document.querySelectorAll('.ant-modal-confirm')].at(-1); button(modal, '放弃并离开').click(); await settle(); assert.equal(await decision, true); assert.equal(w.vm.formState, canonical); assert.equal(w.vm.requestRouteLeave({ path: '/login' }), true); assert.deepEqual(mutations(), [])
})

function freezeDeep(value) { Object.freeze(value); for (const x of Object.values(value)) if (x && typeof x === 'object' && !Object.isFrozen(x)) freezeDeep(x); return value }
function childProps(id = 'isolated-prompt') { return { sectionId: id, formValue: freezeDeep({ ...structuredClone(CONFIG), manualModelsText: 'MODEL-A\nMODEL-B', preservedUnknown: { nested: ['PRESERVE', 0, false] } }), fixedTemperatureText: '0', fixedTopPText: '0' } }
async function isolated(props) { const w = mount(require(childPath), { props, attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); return w }
const absentChild = !fs.existsSync(childPath) && 'baseline private child intentionally absent; explicit structural ASSERTION RED above'

test('controlled child: frozen props emit exactly one full independent clone with only Prompt changed per native input', { skip: absentChild }, async () => {
 const props = childProps(), before = clone(props.formValue), w = await isolated(props), requests = calls.length
 for (const value of ['独立 Prompt\n{"answers":["B"]}', '']) {
  const count = (w.emitted('update:form-value') || []).length; await input(w, value); const emitted = w.emitted('update:form-value'); assert.equal(emitted.length, count + 1); const payload = emitted.at(-1)[0]; assert.notEqual(payload, props.formValue); assert.deepEqual(payload, { ...before, prompt: value }); assert.deepEqual(props.formValue, before)
 }
 assert.equal(calls.length, requests); assert.equal(w.findAll('form').length, 0); assert.deepEqual(Object.keys(w.emitted()).filter(x => x.startsWith('update:')), ['update:form-value'])
 console.log('PROMPT_CLONE ' + JSON.stringify({ updates: w.emitted('update:form-value').length, frozenUnchanged: JSON.stringify(props.formValue) === JSON.stringify(before), requests: calls.length - requests }))
})

test('controlled child: readonly fixed controls cannot mutate props or emit; two instances and prop refresh stay isolated', { skip: absentChild }, async () => {
 const leftProps = childProps('prompt-left'), rightProps = childProps('prompt-right'), left = await isolated(leftProps), right = await isolated(rightProps), requests = calls.length
 assert.notEqual(left.attributes('id'), right.attributes('id')); assert.equal(left.attributes('aria-labelledby'), 'prompt-left-heading'); assert.equal(right.find('h2').attributes('id'), 'prompt-right-heading')
 for (const label of ['温度', 'Top P']) { const el = item(left, label).get('input'); assert.equal(el.element.readOnly, true); assert.equal(el.element.disabled, true); el.element.value = '9'; await el.trigger('input'); await el.trigger('change') }
 await settle(); assert.equal(left.emitted('update:form-value'), undefined); assert.deepEqual(leftProps.formValue, childProps().formValue)
 const nextForm = freezeDeep({ ...clone(leftProps.formValue), prompt: 'PROP_REFRESH' }); await left.setProps({ formValue: nextForm, sectionId: 'prompt-updated', fixedTemperatureText: 'FIXED-T', fixedTopPText: 'FIXED-P' }); await settle()
 assert.equal(item(left, 'Prompt 设置').get('textarea').element.value, 'PROP_REFRESH'); assert.equal(item(left, '温度').get('input').element.value, 'FIXED-T'); assert.equal(item(left, 'Top P').get('input').element.value, 'FIXED-P'); assert.equal(left.attributes('id'), 'prompt-updated'); assert.equal(left.find('h2').attributes('id'), 'prompt-updated-heading')
 await input(left, 'LEFT_ONLY'); assert.equal(left.emitted('update:form-value').length, 1); assert.equal(left.emitted('update:form-value')[0][0].prompt, 'LEFT_ONLY'); assert.equal(right.emitted('update:form-value'), undefined); assert.equal(item(right, 'Prompt 设置').get('textarea').element.value, CONFIG.prompt); assert.deepEqual(rightProps.formValue, childProps().formValue); assert.equal(calls.length, requests)
})
