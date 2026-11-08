const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.resolve(__dirname, '../helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const config = { enabled: true, baseUrl: 'https://fixture.invalid/v1', apiKey: 'SYNTHETIC', model: 'MODEL-ZERO', manualModels: ['MODEL-ZERO'], prompt: 'SYNTHETIC_PROMPT', remark: '', temperature: 0, topP: 0, maxAISearchModelCalls: 4, maxAISearches: 3, maxAISearchRetries: 3, searchPricePerCall: 0, maxAIWebReads: 5, webReadPricePerCall: 0, enableAIStagedTokenBilling: true, smoothAIStagedTokenMultiplier: true, stagedTokenMultiplierPoints: [{ tokens: 0, multiplier: 2 }, { tokens: 1000, multiplier: 1 }] }
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
api.exports = { getAIOCRCacheStatus: syntheticOCRStatus, getAISearchConfig: () => Promise.resolve(ok(structuredClone(config))), getAIModelPrices: () => Promise.resolve(ok([])), getUAPISearchEngines: () => Promise.resolve(ok({})), getUAPIUsageStatus: () => Promise.resolve(ok({ endpoints: [] })) }
require.cache[apiPath] = api
const pagePath = path.join(web, 'src/pages/AISearchConfig/index.vue'), wrappers = []
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
async function start() { const ocrReadStart = ocrStatusReads; user.setStoredCurrentUser({ id: 37, username: 'fixture-admin', role: 'admin', balance: 1 }); const w = mount(require(pagePath), { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(w); await settle(); assert.equal(ocrStatusReads, ocrReadStart + 1, 'one initial readonly OCR status GET per parent'); return w }
afterEach(async () => { for (const w of wrappers.splice(0)) w.unmount(); Antd.Modal.destroyAll(); Antd.message.destroy(); await settle(); document.body.replaceChildren(); user.setStoredCurrentUser(null) })
const anchored = ['连接与模型', '高级能力', 'Prompt', '模型价格表']
const expected = ['连接与模型', '高级能力', 'Prompt', '图片 OCR 缓存', '模型价格表']
function regions(w) { return w.findAll('h2').filter(h => anchored.includes(h.text())).map(h => h.element.closest('section')) }
test('four anchored AI regions plus upstream OCR cache panel preserve exact headings and unique per-instance IDs', async () => {
  const a = await start(), b = await start()
  for (const w of [a, b]) {
    assert.deepEqual(w.findAll('h2').map(h => h.text()), expected, 'four prior regions plus OCR cache heading must be explicit and ordered')
    const rs = regions(w); assert.equal(rs.length, 4)
    const links = w.findAll('a[href^="#"]')
    for (let i = 0; i < rs.length; i++) {
      const section = rs[i], heading = section.querySelector('h2')
      assert.ok(section.id, 'each region has a native fragment target')
      assert.ok(links.some(a => a.attributes('href') === '#' + section.id), 'each region has a native in-page link')
      assert.ok(heading.id && section.getAttribute('aria-labelledby') === heading.id, 'region names its real heading')
    }
    assert.equal(w.findAll('[role="tab"], .ant-tabs, .ant-collapse').length, 0, 'no hidden tab/collapse regions')
  }
  const ids = [...a.element.querySelectorAll('[id]'), ...b.element.querySelectorAll('[id]')].map(x => x.id)
  assert.equal(new Set(ids).size, ids.length, 'two mounted page instances cannot share IDs')
})
test('upstream field order, fixed readonly sampling, and draft changes survive four-region composition', async () => {
  const w = await start()
  const labels = w.findAll('.ai-config-form .ant-form-item-label').map(x => x.text())
  assert.deepEqual(labels, ['是否启用', '服务地址', 'API Key', '默认模型', '备用模型', '模型列表', '模型最大调用次数', '最大搜索次数', '每轮重试次数', '联网搜索单价（元/次）', '本地题库检索单价（元/次）', '网页读取次数', '网页读取单价（元/次）', '图片 OCR 单价（元/次）', '自动压缩上下文', '压缩执行顺序', '压缩触发比例', '压缩目标比例', '压缩最大尝试次数', 'Prompt 设置', '温度', 'Top P'])
  for (const label of ['温度', 'Top P']) {
    const item = w.findAll('.ant-form-item').find(x => x.find('.ant-form-item-label').exists() && x.find('.ant-form-item-label').text() === label)
    const input = item.find('input'); assert.equal(input.element.value, '0'); assert.equal(input.element.readOnly, true); assert.equal(input.element.disabled, true)
  }
  assert.equal(w.vm.hasUnsavedChanges, false)
  await w.find('textarea[placeholder="留空时将自动使用系统内置 Prompt"]').setValue('SYNTHETIC_UNSAVED')
  assert.equal(w.vm.hasUnsavedChanges, true)
  assert.ok(w.findAll('.ant-alert-message').some(x => x.text().includes('未保存')))
  console.log('SECTION_PRESERVATION ' + JSON.stringify({ labels, temperature: w.vm.formState.temperature, topP: w.vm.formState.topP, dirty: w.vm.hasUnsavedChanges }))
})
