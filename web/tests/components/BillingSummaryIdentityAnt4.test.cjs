const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict'), path = require('node:path'), Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils'), { nextTick } = require('vue'), Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const adminA = { id: 91, role: 'admin', username: 'fixture-summary-a', balance: 1 }
const adminB = { id: 92, role: 'admin', username: 'fixture-summary-b', balance: 2 }
const ok = data => ({ status: 200, data })
const row = (id, marker) => ({ id, subject: marker, type: 'normal_search', direction: 'expense', amount: 0.123456, cacheHitRate: 0.99 })
const response = (id, marker, totalAmount, currentPageAmount, currentPageCacheHitRate, totalCacheHitRate) => ok({ data: [row(id, marker)], total: 1, page: 1, pageSize: 10, summary: { totalAmount, currentPageAmount, currentPageCacheHitRate, totalCacheHitRate } })
let wrapper, reads = 0, pending = []
const apiPath = path.join(root, 'src/api/TikuComponentApi.js'), api = new Module(apiPath); api.loaded = true
api.exports = {
 getBillingRecords: params => { reads += 1; return reads === 1 ? Promise.resolve(response(91, 'SYNTHETIC_OWNER_A', 3.125678, 1.25, 0.1, 0.46)) : new Promise((resolve, reject) => pending.push({ params, resolve, reject })) },
 getIpLocations: () => Promise.resolve(ok({})), getMyLocations: () => Promise.resolve(ok({})),
}
require.cache[apiPath] = api
const Billing = require(path.join(root, 'src/pages/BillingRecordList/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick() }
function summary() { const value = wrapper.vm.summary; return Object.fromEntries(['totalAmount','currentPageAmount','currentPageCacheHitRate','totalCacheHitRate'].map(key => [key,value[key]])) }
function statistic(title) { const node = [...wrapper.element.querySelectorAll('.ant-statistic')].find(x => x.querySelector('.ant-statistic-title')?.textContent === title); assert.ok(node, 'real statistic ' + title); return node.querySelector('.ant-statistic-content').textContent.replace(/\s+/g, '') }
afterEach(async () => { wrapper?.unmount(); wrapper = undefined; Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); user.clearStoredCurrentUser(); reads = 0; pending = [] })
for (const outcome of ['resolve', 'reject']) test('real billing identity switch clears amounts and weighted rates while B loads; old A ' + outcome + ' cannot publish', async () => {
 user.setStoredCurrentUser(adminA)
 wrapper = mount(Billing, { attachTo: document.body, global: { plugins: [Antd] } }); await settle()
 assert.deepEqual(summary(), { totalAmount: 3.125678, currentPageAmount: 1.25, currentPageCacheHitRate: 0.1, totalCacheHitRate: 0.46 })
 assert.equal(statistic('当前页缓存命中率'), '10%'); assert.equal(statistic('综合缓存命中率（全部页）'), '46%', 'server weighted rate is not the row 99%')
 wrapper.vm.handleLoad(); await settle(); assert.equal(pending.length, 1); const old = pending.shift()
 user.setStoredCurrentUser(adminB); await settle(); assert.equal(pending.length, 1); const current = pending.shift()
 assert.deepEqual(summary(), { totalAmount: 0, currentPageAmount: 0, currentPageCacheHitRate: null, totalCacheHitRate: null })
 assert.equal(wrapper.vm.dataSource.length, 0); assert.equal(wrapper.vm.pagination.total, 0); assert.equal(wrapper.vm.tableLoading, true)
 assert.equal(statistic('累计消费金额（全部页）'), '¥0.000000'); assert.equal(statistic('当前页累计消费金额'), '¥0.000000'); assert.equal(statistic('当前页缓存命中率'), '-'); assert.equal(statistic('综合缓存命中率（全部页）'), '-')
 assert.ok(!wrapper.element.textContent.includes('SYNTHETIC_OWNER_A'))
 if (outcome === 'resolve') old.resolve(response(191, 'SYNTHETIC_RETIRED_A', 99, 99, 0.99, 0.99)); else old.reject(new Error('SYNTHETIC_RETIRED_A_ERROR'))
 await settle(); assert.deepEqual(summary(), { totalAmount: 0, currentPageAmount: 0, currentPageCacheHitRate: null, totalCacheHitRate: null }); assert.equal(wrapper.vm.tableLoading, true, 'old finally must not clear B loading'); assert.equal(wrapper.vm.dataSource.length, 0); assert.ok(!document.body.textContent.includes('SYNTHETIC_RETIRED_A')); assert.ok(!document.body.textContent.includes('获取账单列表失败'))
 current.resolve(response(92, 'SYNTHETIC_OWNER_B', 0.654321, 0.123456, 0.25, 0.75)); await settle()
 assert.deepEqual(summary(), { totalAmount: 0.654321, currentPageAmount: 0.123456, currentPageCacheHitRate: 0.25, totalCacheHitRate: 0.75 }); assert.equal(wrapper.vm.tableLoading, false); assert.equal(wrapper.vm.dataSource[0].id, 92); assert.equal(statistic('当前页缓存命中率'), '25%'); assert.equal(statistic('综合缓存命中率（全部页）'), '75%')
 console.log('BILLING_SUMMARY_OWNER_LEDGER ' + JSON.stringify({ outcome, reads, retiredAmounts: [0,0], retiredRates: [null,null], current: summary(), currentId: wrapper.vm.dataSource[0].id }))
})
