const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.resolve(__dirname, '../helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick, toRaw, h } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const config = { enabled: true, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC', model: 'MODEL-ZERO', manualModels: ['MODEL-ZERO'], prompt: 'SYNTHETIC_PROMPT', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0, maxAIWebReads: 5, webReadPricePerCall: 0, enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: true, stagedTokenMultiplierPoints: [{ tokens: 0, multiplier: 2 }, { tokens: 1000, multiplier: 1 }] }
const rows = [
 { id: 501, modelName: 'MODEL-ZERO', displayName: 'Zero fixture', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0.000062, outputPrice: 1.234567, multiplier: 1.2345, status: 'enabled', remark: 'SYNTHETIC_ROW', isUserModified: true },
 { id: 502, modelName: 'MODEL-DEFAULT', displayName: '', inputPrice: 0.000001, cachedPrice: 0, cacheWritePrice: 0, outputPrice: 0, multiplier: 0, status: 'disabled', remark: '', isUserModified: false },
]
const calls = [], ok = data => ({ status: 200, data }), apiPath = path.join(web, 'src/api/TikuComponentApi.js'), api = new Module(apiPath)
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
 getAISearchConfig: () => Promise.resolve(ok(structuredClone(config))), getAIModelPrices: () => Promise.resolve(ok(structuredClone(rows))), getUAPISearchEngines: () => Promise.resolve(ok({})), getUAPIUsageStatus: () => Promise.resolve(ok({ endpoints: [] })),
 createAIModelPrice: payload => { calls.push(['create', structuredClone(payload)]); return Promise.resolve(ok({})) },
 updateAIModelPrice: payload => { calls.push(['update', structuredClone(payload)]); return Promise.resolve(ok({})) },
 deleteAIModelPrice: payload => { calls.push(['delete', structuredClone(payload)]); return Promise.resolve(ok({})) },
 resetAIModelPrices: () => { calls.push(['reset']); return Promise.resolve(ok({})) },
}
require.cache[apiPath] = api
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue'), wrappers = []
const clean = x => String(x || '').replace(/\s+/g, '')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
async function mounted(Component, props = {}) { const w = mount(Component, { props, attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); return w }
async function start() { const ocrReadStart = ocrStatusReads; user.setStoredCurrentUser({ id: 37, username: 'fixture-admin', role: 'admin', balance: 1 }); const w = await mounted(require(pagePath)); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return w }
afterEach(async () => { for (const w of wrappers.splice(0)) w.unmount(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren(); user.setStoredCurrentUser(null); calls.length = 0 })
function button(root, text) { const b = [...root.querySelectorAll('button')].find(x => clean(x.textContent) === clean(text)); assert.ok(b, 'native button ' + text); return b }
function priceModal() { const m = [...document.querySelectorAll('.ant-modal')].find(x => /模型价格$/.test(clean(x.querySelector('.ant-modal-title')?.textContent)) && x.style.display !== 'none'); assert.ok(m, 'visible real Antd4 price editor'); return m }
function field(root, label) { const item = [...root.querySelectorAll('.ant-form-item')].find(x => clean(x.querySelector('.ant-form-item-label')?.textContent) === clean(label)); assert.ok(item, 'editor field ' + label); const input = item.querySelector('input'); assert.ok(input); return input }
async function input(root, label, value) { const el = field(root, label); el.value = String(value); el.dispatchEvent(new Event('input', { bubbles: true })); el.dispatchEvent(new Event('change', { bubbles: true })); el.dispatchEvent(new Event('blur', { bubbles: true })); await settle() }
async function confirmPopup(text) { await new Promise(resolve => setTimeout(resolve, 35)); await settle(); const popup = [...document.querySelectorAll('.ant-popover')].find(x => x.textContent.includes(text) && !x.classList.contains('ant-popover-hidden')); assert.ok(popup, 'real Antd4 confirmation ' + text); button(popup, '确定').click(); await settle() }
const headers = ['ID', '模型 ID', '显示名称', '上下文窗口（Token）', '输入（￥/M）', '缓存输入（￥/M）', '缓存写入（￥/M）', '输出（￥/M）', '倍率', '备注', '操作']
function tableLedger(root) {
 const table = root.querySelector('.price-table-wrap .ant-table'); assert.ok(table, 'actual price table')
 assert.deepEqual([...table.querySelectorAll('thead th')].map(x => x.textContent.trim()), headers)
 const body = [...table.querySelectorAll('tbody tr[data-row-key]')]
 assert.deepEqual(body.map(x => x.getAttribute('data-row-key')), ['501', '502'])
 assert.ok(body[0].textContent.includes('￥0.000062')); assert.ok(body[0].textContent.includes('￥1.234567')); assert.ok(body[0].textContent.includes('×1.2345')); assert.ok(body[0].textContent.includes('已改'))
 assert.ok(body[1].textContent.includes('￥0.000001')); assert.ok(body[1].textContent.includes('×1')); assert.ok(!body[1].textContent.includes('已改'))
 return body.map(row => [...row.querySelectorAll('td')].map(x => clean(x.textContent)))
}
test('mapped: real current price table preserves exact columns six-digit zero multiplier and edit/delete identity', async () => {
 const w = await start(), ledger = tableLedger(w.element), table = w.element.querySelector('.price-table-wrap')
 const first = table.querySelector('tr[data-row-key="501"]'); button(first, '编辑').click(); await settle()
 assert.equal(w.vm.priceForm.id, 501); assert.equal(w.vm.priceForm.cacheWritePrice, 0.000062); assert.equal(w.vm.priceForm.inputPrice, 0); assert.equal(w.vm.priceForm.multiplier, 1.2345)
 button(priceModal(), '取消').click(); await settle(); assert.equal(w.vm.priceModalVisible, false)
 button(first, '删除').click(); await settle(); assert.equal(calls.length, 0, 'delete remains confirmed, not eager'); await confirmPopup('确定删除该模型价格吗？')
 assert.deepEqual(calls, [['delete', { id: 501 }]])
 console.log('PRICE_TABLE_LEDGER ' + JSON.stringify({ ledger, calls, statuses: w.vm.priceRows.map(x => x.status) }))
})
test('mapped: edit native fields retain canonical parent form and exact update payload including cacheWritePrice and zeros', async () => {
 const w = await start(), canonical = w.vm.priceForm
 const row = w.element.querySelector('.price-table-wrap tr[data-row-key="501"]'); button(row, '编辑').click(); await settle()
 const m = priceModal(); await input(m, '显示名称', '  Edited fixture  '); await input(m, '缓存写入单价', 0); await input(m, '输出单价', 0); await input(m, '备注', 'SYNTHETIC_EDIT')
 assert.equal(w.vm.priceForm, canonical, 'controlled patches retain canonical reactive object'); assert.equal(w.vm.priceForm.cacheWritePrice, 0)
 button(m, '确定').click(); await settle()
 const expected = { id: 501, modelName: 'MODEL-ZERO', displayName: 'Edited fixture', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0, outputPrice: 0, multiplier: 1.2345, remark: 'SYNTHETIC_EDIT' }
 assert.deepEqual(calls, [['update', expected]]); assert.equal(w.vm.priceModalVisible, false); assert.equal(w.vm.priceSaving, false); assert.equal(w.vm.hasUnsavedChanges, false, 'price editor is not the config draft')
 console.log('PRICE_UPDATE_LEDGER ' + JSON.stringify(calls))
})
test('mapped: add/cancel/default path preserves empty-model guard default multiplier zero prices and exact create payload', async () => {
 const w = await start(); button(w.element, '新增模型价格').click(); await settle()
 let m = priceModal(); button(m, '确定').click(); await settle(); assert.deepEqual(calls, [], 'empty model cannot call API')
 await input(m, '模型名称', ' MODEL-CREATE '); await input(m, '缓存写入单价', 0.123456); await input(m, '备注', 'SYNTHETIC_CREATE')
 button(m, '确定').click(); await settle()
 const expected = { id: null, contextWindowTokens: 0, modelName: 'MODEL-CREATE', displayName: '', inputPrice: 0, cachedPrice: 0, cacheWritePrice: 0.123456, outputPrice: 0, multiplier: 1, remark: 'SYNTHETIC_CREATE' }
 assert.deepEqual(calls, [['create', expected]])
 button(w.element, '新增模型价格').click(); await settle(); m = priceModal(); assert.equal(Number(field(m, '缓存写入单价').value), 0); assert.equal(Number(field(m, '倍率').value), 1); await input(m, '模型名称', 'SYNTHETIC_CANCEL'); button(m, '取消').click(); await settle(); assert.equal(w.vm.priceForm.modelName, ''); assert.equal(calls.length, 1)
 button(w.element, '恢复默认').click(); await settle(); assert.equal(calls.length, 1); await confirmPopup('确定恢复内置默认价格表吗？'); assert.deepEqual(calls[1], ['reset'])
 console.log('PRICE_CREATE_DEFAULT_LEDGER ' + JSON.stringify(calls))
})
function child(name) { const filename = path.join(web, 'src/pages/AISearchConfig', name + '.vue'); assert.ok(fs.existsSync(filename), 'required new pure component exists: ' + name); return require(filename) }
test('boundary: pure table uses rows/loading and emits original row intent only through real controls', async () => {
 const Table = child('AIModelPriceTable'), w = await mounted(Table, { rows: structuredClone(rows), loading: false }), before = JSON.stringify(w.props('rows'))
 tableLedger(w.element)
 const first = w.element.querySelector('tr[data-row-key="501"]'); button(first, '编辑').click(); await settle(); assert.equal(toRaw(w.emitted('edit')[0][0]), toRaw(w.props('rows')[0]))
 button(first, '删除').click(); await settle(); assert.equal(w.emitted('delete'), undefined); await confirmPopup('确定删除该模型价格吗？'); assert.equal(toRaw(w.emitted('delete')[0][0]), toRaw(w.props('rows')[0]))
 assert.equal(JSON.stringify(w.props('rows')), before); assert.deepEqual(calls, [], 'child emits intent but never calls APIs')
 await w.setProps({ loading: true }); await new Promise(resolve => setTimeout(resolve, 35)); await settle(); assert.ok(w.find('.ant-spin-spinning').exists())
 const table = w.findComponent(Antd.Table); assert.equal(table.props('rowKey'), 'id'); assert.deepEqual(table.props('pagination'), { pageSize: 8, size: 'small' }); assert.equal(table.props('tableLayout'), 'fixed'); assert.deepEqual(table.props('scroll'), { x: table.props('columns').reduce((sum, column) => sum + Number(column.width || 0), 0) }); assert.ok(w.find('.console-table-shell[role="region"][aria-label="模型价格表"]').exists(), 'upstream table shell remains the named scrolling boundary')
 const statusSlot = table.vm.$slots.status; assert.equal(typeof statusSlot, 'function', 'retain current enabled/disabled status slot contract')
 const statuses = await mounted({ render: () => h('div', [statusSlot({ record: rows[0] }), statusSlot({ record: rows[1] })]) }); assert.deepEqual(statuses.findAll('.ant-tag').map(x => x.text()), ['启用', '停用']); assert.ok(statuses.findAll('.ant-tag')[0].classes().includes('ant-tag-green')); assert.ok(statuses.findAll('.ant-tag')[1].classes().includes('ant-tag-default'))
})
test('boundary: controlled modal clones every changed field, never mutates props, and only emits explicit lifecycle intent', async () => {
 const Editor = child('AIModelPriceEditorModal'), original = Object.freeze({ id: 501, modelName: 'MODEL-ZERO', displayName: '', inputPrice: 0, cachedPrice: 0.01, cacheWritePrice: 0, outputPrice: 0, multiplier: 1, remark: '' }), before = JSON.stringify(original)
 const w = await mounted(Editor, { idPrefix: 'ai-price-boundary-editor', open: true, formValue: original, confirmLoading: false, labelCol: { span: 6 }, wrapperCol: { span: 18 } })
 const form = w.findComponent(Antd.Form); assert.deepEqual(form.props('labelCol'), { span: 6 }); assert.deepEqual(form.props('wrapperCol'), { span: 18 })
 const numbers = w.findAllComponents(Antd.InputNumber); assert.equal(numbers.length, 5); for (const number of numbers.slice(0, 4)) { assert.equal(number.props('min'), 0); assert.equal(number.props('precision'), 6); assert.equal(number.props('step'), 0.01) } assert.equal(numbers[4].props('min'), 0.0001); assert.equal(numbers[4].props('precision'), 4); assert.equal(numbers[4].props('step'), 0.1)
 const names = [['模型名称', 'modelName', 'MODEL-CONTROLLED'], ['显示名称', 'displayName', 'Controlled'], ['上下文窗口', 'contextWindowTokens', '131072'], ['输入单价', 'inputPrice', 0.000001], ['缓存输入单价', 'cachedPrice', 0], ['缓存写入单价', 'cacheWritePrice', 0.000062], ['输出单价', 'outputPrice', 1.234567], ['倍率', 'multiplier', 1.2345], ['备注', 'remark', 'SYNTHETIC_CONTROLLED']]
 let accepted = { ...original }
 for (const [label, key, value] of names) {
  await input(priceModal(), label, value)
  const patches = w.emitted('update:form-value'); assert.ok(patches?.length, 'explicit immutable update for ' + key)
  const next = patches.at(-1)[0]; assert.notEqual(next, accepted); assert.deepEqual(next, { ...accepted, [key]: value }); assert.equal(JSON.stringify(original), before)
  accepted = next; await w.setProps({ formValue: accepted }); await settle()
 }
 assert.equal(w.emitted('submit'), undefined); button(priceModal(), '确定').click(); await settle(); assert.equal(w.emitted('submit').length, 1); assert.equal(w.emitted('update:open'), undefined, 'submit does not self-close before owner success')
 await w.setProps({ confirmLoading: true }); await settle(); assert.ok(priceModal().querySelector('.ant-modal-footer .ant-btn-loading'))
 await w.setProps({ confirmLoading: false }); await settle(); button(priceModal(), '取消').click(); await settle(); assert.equal(w.emitted('cancel').length, 1); assert.deepEqual(w.emitted('update:open').at(-1), [false]); assert.deepEqual(calls, [])
 const eventCount = w.emitted('update:form-value').length; await w.setProps({ open: false }); await settle(); assert.equal(w.emitted('update:form-value').length, eventCount, 'owner close cannot reset form'); assert.equal(JSON.stringify(original), before)
 w.unmount(); wrappers.splice(wrappers.indexOf(w), 1); await settle()
 const remount = await mounted(Editor, { open: true, formValue: original, confirmLoading: false }); assert.equal(Number(field(priceModal(), '缓存写入单价').value), 0); assert.equal(remount.emitted('update:form-value'), undefined, 'mount/remount is readonly to its owner')
 console.log('CONTROLLED_MODAL_LEDGER ' + JSON.stringify({ fields: names.map(x => x[1]), accepted, original, submit: 1, cancel: 1, remountUpdates: 0 }))
})
