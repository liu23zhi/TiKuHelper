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
const apiPath = path.join(web, 'src/api/ServiceStatusApi.js')
let summaryResult
let seriesResult
let wrapper
const calls = []
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getServiceStatusSummary: () => { calls.push('summary'); return Promise.resolve(summaryResult) },
  getServiceStatusSeries: () => { calls.push('series'); return Promise.resolve(seriesResult) },
}
require.cache[apiPath] = api

const summary = () => ({ status: 200, data: {
  schemaVersion: 1, status: 'healthy', successRate: 1, successRateAvailable: true,
  totalRequests: 3, successRequests: 3, avgLatencyMs: 12, windowMinutes: 5,
  endpoints: { search: { totalRequests: 3, successRequests: 3, avgLatencyMs: 12, samples: 1 } },
} })
async function settle() {
  await nextTick()
  await flushPromises()
  await nextTick()
  await flushPromises()
  await nextTick()
}
async function openPage() {
  user.setStoredCurrentUser({ id: 7302, username: 'status-owner', role: 'user' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/service-status', component: { template: '<div />' } }] })
  await router.push('/service-status')
  await router.isReady()
  wrapper = mount(require(path.join(web, 'src/pages/ServiceStatus/index.vue')), { attachTo: document.body, global: { plugins: [Antd, router] } })
  await settle()
}
afterEach(async () => {
  if (wrapper?.exists()) wrapper.unmount()
  Antd.Modal.destroyAll()
  Antd.message.destroy()
  await settle()
  document.body.replaceChildren()
  calls.length = 0
})

test('status page renders a search-only 24-hour band and supports keyboard inspection', async () => {
  summaryResult = summary()
  seriesResult = { status: 200, data: {
    schemaVersion: 1,
    points: [{ minute: Math.floor(Date.now() / 60000), totalRequests: 99, successRequests: 0, avgLatencyMs: 30 }],
    searchPoints: [{ minute: Math.floor(Date.now() / 60000), totalRequests: 3, successRequests: 3, avgLatencyMs: 12 }],
  } }
  await openPage()
  assert.deepEqual(calls.sort(), ['series', 'summary'])
  assert.ok(wrapper.text().includes('最近 24 小时'))
  const cells = wrapper.findAll('[data-search-history-cell]')
  assert.equal(cells.length, 288)
  assert.equal(cells.at(-1).attributes('data-history-tier'), 't100')
  assert.equal(cells[0].attributes('data-history-tier'), 'none')
  const band = wrapper.find('[data-search-history-band]')
  assert.equal(band.attributes('role'), 'slider')
  await band.trigger('keydown', { key: 'Home' })
  assert.match(wrapper.find('[data-search-history-detail]').text(), /无请求/)
  await band.trigger('keydown', { key: 'End' })
  assert.match(wrapper.find('[data-search-history-detail]').text(), /100%.*3 次请求/)
  assert.ok(!wrapper.text().includes('99 次请求'), 'must never mix site-wide series into search history')
})

test('series failure or an older response preserves the summary and shows unavailable gray history', async () => {
  for (const response of [{ status: 503 }, { status: 200, data: { schemaVersion: 1, points: [{ minute: Math.floor(Date.now() / 60000), totalRequests: 99, successRequests: 99, avgLatencyMs: 5 }] } }]) {
    summaryResult = summary()
    seriesResult = response
    await openPage()
    assert.equal(wrapper.find('[data-metric="search-success-rate"]').text(), '100%')
    assert.match(wrapper.find('[data-search-history-hint]').text(), /历史状态暂不可用/)
    assert.equal(wrapper.findAll('[data-search-history-cell]').length, 288)
    assert.ok(wrapper.findAll('[data-search-history-cell]').every(cell => cell.attributes('data-history-tier') === 'none'))
    wrapper.unmount()
  }
})

test('summary failure does not discard successful search history', async () => {
  summaryResult = { status: 500 }
  seriesResult = { status: 200, data: { schemaVersion: 1, searchPoints: [{ minute: Math.floor(Date.now() / 60000), totalRequests: 2, successRequests: 0, avgLatencyMs: 10 }] } }
  await openPage()
  assert.equal(wrapper.findAll('[data-search-history-cell]').at(-1).attributes('data-history-tier'), 't0')
  assert.match(wrapper.find('[data-search-history-detail]').text(), /0%.*2 次请求/)
  assert.ok(wrapper.find('[role="alert"]').exists())
})

test('refresh failure keeps the last rendered summary and history snapshot', async () => {
  summaryResult = summary()
  const minute = Math.floor(Date.now() / 60000)
  seriesResult = { status: 200, data: { schemaVersion: 1, searchPoints: [{ minute, totalRequests: 2, successRequests: 2, avgLatencyMs: 10 }] } }
  await openPage()
  assert.equal(wrapper.findAll('[data-search-history-cell]').at(-1).attributes('data-history-tier'), 't100')

  summaryResult = { status: 503 }
  seriesResult = { status: 503 }
  await wrapper.find('[data-action="refresh-service-status"]').trigger('click')
  await settle()
  assert.equal(wrapper.find('[data-metric="search-success-rate"]').text(), '100%')
  assert.equal(wrapper.findAll('[data-search-history-cell]').at(-1).attributes('data-history-tier'), 't100')
  assert.ok(wrapper.find('[role="alert"]').exists())
})
