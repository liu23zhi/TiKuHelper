const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const admin = { id: 921, role: 'admin', username: 'synthetic-columns-admin' }
const normal = { id: 922, role: 'user', username: 'synthetic-columns-user' }
const question = { id: 921, type: 4, question: 'SYNTHETIC_COLUMN_QUESTION', options: '[]', answer: '["SYNTHETIC_ANSWER"]', source_name: 'synthetic source', extra: 'SYNTHETIC_EXTRA' }
const billing = { id: 921, subject: 'SYNTHETIC_COLUMN_BILLING', username: admin.username, amount: 0.123456, direction: 'expense', type: 'normal_search', referenceNo: 'SYNTHETIC_REFERENCE', createdAt: '2026-10-28 10:00:00' }
const log = { id: 921, module: 'search', action: 'search', username: admin.username, detail: 'SYNTHETIC_COLUMN_LOG', clientIp: '192.0.2.92', createTime: '2026-10-28T10:00:00+08:00' }
const ok = data => Promise.resolve({ status: 200, data })
let reads = 0
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = {
  getCourses: () => ok([]), getQuestions: () => { reads++; return ok({ items: [question], total: 1 }) },
  getBillingRecords: () => { reads++; return ok({ data: [billing], total: 1, page: 1, pageSize: 10, summary: {totalAmount: 0.123456, currentPageAmount: 0.123456, currentPageCacheHitRate: null, totalCacheHitRate: null} }) },
  getLogList: () => { reads++; return ok({ data: [log], total: 1, page: 1, pageSize: 10 }) },
  getIpLocations: () => ok({}), getMyLocations: () => ok({}),
  delQuestions: () => { throw new Error('UNEXPECTED_WRITE') }, updateQuestions: () => { throw new Error('UNEXPECTED_WRITE') },
  createQuestions: () => { throw new Error('UNEXPECTED_WRITE') }, importQuestions: () => { throw new Error('UNEXPECTED_WRITE') },
  updateBillingStatementVisibility: () => { throw new Error('UNEXPECTED_WRITE') },
}
require.cache[apiPath] = api
const cases = [
  { page: 'TikuComponent', keys: ['id','type','question','answer','source_name','extra','action'], optional: '拓展参数', optionalKey: 'extra', required: ['问题','答案','操作'] },
  { page: 'BillingRecordList', keys: ['id','username','typeLabel','subject','amount','source','searchMode','referenceNo','effectiveMultiplier','cacheHitRate','accessTokenName','createdAt','clientIp','feeDetail'], optional: '访问令牌名称', optionalKey: 'accessTokenName', required: ['项目','金额','操作'], mobile: '.mobile-billing-card' },
  { page: 'LogList', keys: ['createTime','module','status','username','clientIp','qid','billingIds','summary','project','hitProvider','billingProvider','detail','requestId','mode','requestSource','answerCount','requestSnapshot','responseSnapshot'], optional: '原始请求', optionalKey: 'requestSnapshot', required: ['时间','状态','说明','项目','命中渠道 / 错误原因'], mobile: '.mobile-log-card' },
]
let wrapper
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 30)); await nextTick() }
function button(label) {
  const found = [...document.querySelectorAll('button')].find(node => node.textContent.replace(/\s/g, '') === label.replace(/\s/g, ''))
  assert.ok(found, 'REAL_COLUMN_CONTROL: ' + label); return found
}
async function click(node) { node.click(); await settle() }
function checkbox(label) {
  const found = [...document.querySelectorAll('label')].find(node => node.textContent.replace(/\s/g, '').includes(label.replace(/\s/g, '')) && node.querySelector('input[type="checkbox"]'))
  assert.ok(found, 'REAL_COLUMN_CHECKBOX: ' + label)
  return found.querySelector('input[type="checkbox"]')
}
function keys() { return wrapper.findComponent(Antd.Table).props('columns').map(column => column.key) }
async function mountPage(item, identity = admin) {
  user.setStoredCurrentUser(identity); reads = 0
  const Page = require(path.join(web, 'src/pages', item.page, 'index.vue'))
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/component', component: Page }] })
  await router.push('/adapter/component'); await router.isReady()
  wrapper = mount(Page, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false } } })
  await settle()
}
afterEach(async () => {
  wrapper?.unmount(); wrapper = undefined; Antd.message.destroy(); Antd.Modal.destroyAll()
  await settle(); document.body.innerHTML = ''; user.clearStoredCurrentUser(); localStorage.clear()
})
for (const item of cases) test(item.page + ': real settings hide, restore, prioritize and keep required columns without changing data', async () => {
  await mountPage(item)
  assert.deepEqual(keys(), item.keys, 'DEFAULT_ORIGINAL_FULL_COLUMN_ORDER')
  const originalRows = JSON.stringify(wrapper.vm.data || wrapper.vm.dataSource)
  const originalReads = reads
  const originalWidth = wrapper.vm.tableScrollX
  await click(button('列设置'))
  for (const label of item.required) assert.equal(checkbox(label).disabled, true, 'REQUIRED_COLUMN_LOCKED: ' + label)
  const optional = checkbox(item.optional)
  assert.equal(optional.checked, true)
  await click(optional)
  assert.equal(keys().includes(item.optionalKey), false, 'OPTIONAL_COLUMN_ACTUALLY_HIDDEN')
  assert.ok(wrapper.vm.tableScrollX < originalWidth, 'VISIBLE_COLUMN_WIDTH_RECOMPUTED')
  await click(checkbox(item.optional))
  assert.deepEqual(keys(), item.keys, 'LOW_FREQUENCY_FIELD_RECOVERABLE_IN_ORIGINAL_ORDER')
  await click(button('常用列'))
  assert.ok(keys().length < item.keys.length, 'PRIORITY_PRESET_REDUCES_WIDTH')
  for (const label of item.required) assert.equal(checkbox(label).checked, true)
  if (item.mobile) assert.ok(wrapper.find(item.mobile).text().length > 0, 'MOBILE_CARD_REMAINS_USABLE')
  await click(button('恢复全部列'))
  assert.deepEqual(keys(), item.keys, 'RESET_RESTORES_ALL_ORIGINAL_FIELDS')
  assert.equal(wrapper.vm.tableScrollX, originalWidth, 'RESTORED_WIDTH_RETAINS_ORIGINAL_EXPANSION_RULES')
  assert.equal(JSON.stringify(wrapper.vm.data || wrapper.vm.dataSource), originalRows, 'COLUMN_CONTROLS_DO_NOT_MUTATE_RECORDS')
  assert.equal(reads, originalReads, 'COLUMN_CONTROLS_HAVE_NO_API_READ_OR_WRITE')
  console.log('LIST_COLUMN_WITNESS ' + JSON.stringify({ page: item.page, default: item.keys, restored: keys(), requiredLocked: item.required, reads }))
})
test('BillingRecordList: user settings cannot expose admin-only columns, including after live role retirement', async () => {
  const item = cases[1]
  await mountPage(item)
  await click(button('列设置'))
  await click(button('常用列'))
  user.setStoredCurrentUser(normal); await settle()
  assert.deepEqual(keys(), item.keys.filter(key => !['username','effectiveMultiplier','clientIp'].includes(key)), 'NEW_OWNER_DEFAULT_IS_ROLE_FILTERED_FULL_SET')
  await click(button('列设置'))
  assert.equal([...document.querySelectorAll('label')].some(node => ['最终倍率','来源 IP','用户'].includes(node.textContent.trim()) && node.querySelector('input[type="checkbox"]')), false, 'ROLE_FILTERED_CHOICES_ONLY')
  await click(button('恢复全部列'))
  assert.equal(keys().includes('clientIp'), false)
  assert.equal(keys().includes('effectiveMultiplier'), false)
  assert.equal(keys().includes('username'), false)
  console.log('LIST_COLUMN_ROLE_WITNESS ' + JSON.stringify({ role: 'user', keys: keys() }))
})
