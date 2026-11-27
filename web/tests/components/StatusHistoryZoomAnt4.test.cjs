const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = path.resolve(__dirname, '../..')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')
const fixtures = require('../fixtures/t13-status-vectors.json')

let wrapper, router, keys
const calls = []
const mock = (relative, methods) => {
  const filename = path.join(web, relative)
  const m = new Module(filename)
  m.loaded = true
  m.exports = methods
  require.cache[filename] = m
}
function reader(kind) {
  return async () => {
    calls.push(kind)
    const data = typeof keys[kind] === 'string' ? fixtures[keys[kind]] : keys[kind]
    return data ? { status: 200, data } : { status: 503 }
  }
}
mock('src/api/ServiceStatusApi.js', { getServiceStatusSummary: reader('summary'), getServiceStatusSeries: reader('series') })
mock('src/api/AdminServiceStatusApi.js', { getAdminServiceStatus: reader('admin') })
mock('src/api/TikuComponentApi.js', { getSystemMetrics: reader('metrics') })
const Public = require('../../src/pages/ServiceStatus/index.vue')
const Admin = require('../../src/pages/AdminServiceStatus/index.vue')
const mixed = { summary: 'mixed-summary', series: 'mixed-series', admin: 'mixed-admin', metrics: 'mixed-metrics' }
const band = () => wrapper.get('[data-status-history-band]')
const zoom = () => wrapper.get('[data-history-zoom]')
const toggle = () => wrapper.get('[data-history-zoom-toggle]')
async function settle() {
  await nextTick(); await flushPromises(); await nextTick(); await flushPromises(); await nextTick()
}
async function open(page, fixture, role = page === Admin ? 'admin' : 'user') {
  keys = { ...fixture }
  user.setStoredCurrentUser({ id: 121, role, status: 'enabled' })
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/status', component: page }] })
  await router.push('/status')
  wrapper = mount(page, { attachTo: document.body, global: { plugins: [Antd, router], stubs: { PortraitChart: true } } })
  await settle()
}
afterEach(async () => {
  wrapper?.unmount()
  wrapper = undefined
  router?.options.history.destroy()
  user.clearStoredCurrentUser()
  Antd.Modal.destroyAll()
  Antd.message.destroy()
  await settle()
  document.body.replaceChildren()
  calls.length = 0
})

test('public timeline uses provider asOf and only search points; keyboard exposes minute detail', async () => {
  await open(Public, mixed)
  const history = wrapper.get('[data-status-history]')
  assert.equal(history.attributes('data-history-scope'), 'search')
  assert.equal(history.findAll('[data-status-history-cell]').length, 288)
  assert.match(history.get('[data-status-history-active]').text(), /13:00.*13:04.*66\.7%.*3 次请求/)
  await band().trigger('focus')
  assert.equal(toggle().attributes('aria-expanded'), 'true')
  assert.doesNotMatch(wrapper.text(), /认证失败|额度失败|超时失败|堆分配|进程采样/)
  const minute = n => zoom().get('[data-history-minute="' + n + '"]').text()
  assert.match(minute(29929260), /部分失败/)
  assert.match(minute(29929261), /暂无采样/)
  assert.match(minute(29929262), /已采样无请求/)
  assert.match(minute(29929263), /全部成功.*1 次请求/)
  await band().trigger('keydown', { key: 'Home' })
  assert.match(history.get('[data-status-history-active]').text(), /1 次请求/)
  await band().trigger('keydown', { key: 'End' })
  await band().trigger('keydown', { key: 'Escape' })
  assert.equal(wrapper.find('[data-history-zoom]').exists(), false)
  assert.deepEqual(calls.sort(), ['series', 'summary'])
})

test('restart and stale/rollback requests stay unknown without manufacturing idle', async () => {
  await open(Public, { summary: 'restart-summary', series: 'restart-series' })
  assert.match(wrapper.get('[data-status-history-active]').text(), /暂无采样/)
  assert.match(wrapper.get('[data-history-restart]').text(), /重启.*清零/)
  keys.series = 'rewind-unknown-public-series'
  await wrapper.get('[data-action="refresh-service-status"]').trigger('click')
  await settle()
  await toggle().trigger('click')
  assert.match(zoom().get('[data-history-minute="29927000"]').text(), /暂无采样/)
  keys.series = 'rewind-catchup-public-series'
  await wrapper.get('[data-action="refresh-service-status"]').trigger('click')
  await settle()
  assert.match(zoom().get('[data-history-minute="29927000"]').text(), /全部失败/)
})

test('admin full-history zoom joins real process samples without filling unknown minutes and refreshes independently', async () => {
  await open(Admin, mixed)
  assert.equal(wrapper.get('[data-status-history]').attributes('data-history-scope'), 'admin')
  assert.match(wrapper.get('[data-status-history-active]').text(), /75%.*4 次请求/)
  await toggle().trigger('click')
  const row = minute => zoom().get('[data-history-minute="' + minute + '"]')
  assert.match(row(29929260).text(), /超时失败.*1/)
  assert.match(row(29929261).text(), /暂无采样.*暂无进程采样/)
  assert.match(row(29929263).text(), /进程采样.*7.*1,008 B/)
  assert.deepEqual(calls.sort(), ['admin', 'metrics'])
  keys.admin = 'rewind-unknown-admin'
  await wrapper.get('[data-action="refresh-admin-service-status"]').trigger('click')
  await settle()
  assert.match(row(29927000).text(), /暂无采样.*进程采样/)
  assert.equal(calls.filter(k => k === 'metrics').length, 1)
  keys.admin = 'rewind-catchup-admin'
  await wrapper.get('[data-action="refresh-admin-service-status"]').trigger('click')
  await settle()
  assert.match(row(29927000).text(), /部分失败.*上游失败.*1/)
  keys.admin = 'restart-admin'
  await wrapper.get('[data-action="refresh-admin-service-status"]').trigger('click')
  await settle()
  assert.match(wrapper.get('[data-history-restart]').text(), /重启.*清零/)
  assert.equal(calls.filter(k => k === 'metrics').length, 1)
})

test('non-admin never loads or renders privileged history and snapshots', async () => {
  await open(Admin, mixed, 'user')
  assert.equal(wrapper.find('[data-status-history]').exists(), false)
  assert.equal(wrapper.find('.system-metrics-panel').exists(), false)
  assert.deepEqual(calls, [])
})

test('summary from before a same-generation rewind cannot fill the series snapshot', async () => {
  const beforeRewind = fixtures['rewind-catchup-public-series']
  const staleSummary = {
    ...fixtures['mixed-summary'],
    asOf: beforeRewind.asOf,
    restartedAt: beforeRewind.restartedAt,
    endpoints: beforeRewind.endpoints,
  }
  await open(Public, { summary: staleSummary, series: 'rewind-unknown-public-series' })
  assert.equal(wrapper.get('[data-metric="search-total-requests"]').text(), '1', 'never mix the pre-rewind summary with the series clock')
  assert.equal(wrapper.get('[data-metric="search-success-rate"]').text(), '100%')
  assert.match(wrapper.get('[data-status-history-active]').text(), /23:16.*23:20.*1 次请求/)
  keys.series = 'rewind-catchup-public-series'
  await wrapper.get('[data-action="refresh-service-status"]').trigger('click')
  await settle()
  assert.equal(wrapper.get('[data-metric="search-total-requests"]').text(), '2')
  assert.equal(wrapper.get('[data-metric="search-success-rate"]').text(), '50%')
})

test('a legacy server with no asOf never renders timed health from browser now', async () => {
  const series = { ...fixtures['mixed-series'], asOf: undefined }
  const summary = { ...fixtures['mixed-summary'], asOf: undefined }
  await open(Public, { summary, series })
  assert.equal(wrapper.get('[data-status-tone]').attributes('data-status-tone'), 'muted')
  assert.match(wrapper.get('[data-status-history-active]').text(), /暂无可确认时窗.*暂无采样/)
  assert.equal(wrapper.get('[data-search-history-updated]').text(), '服务端时窗截至 —')
  assert.ok(wrapper.findAll('[data-status-history-cell]').every(cell => cell.attributes('data-history-tier') === 'unknown'))
})

test('historic samples cannot make a current five-minute gap look idle', async () => {
  const historic = structuredClone(fixtures['mixed-series'])
  historic.searchPoints = historic.searchPoints.filter(point => point.minute < 29929260)
  historic.endpoints.search = { totalRequests: 0, successRequests: 0, avgLatencyMs: 0, samples: 3 }
  await open(Public, { summary: 'mixed-summary', series: historic })
  assert.equal(wrapper.get('[data-endpoint-state]').attributes('data-endpoint-state'), 'unsampled')
  assert.match(wrapper.get('[data-status-history-active]').text(), /暂无采样/)
  const idle = structuredClone(historic)
  idle.searchPoints.push({ minute: 29929262, totalRequests: 0, successRequests: 0, avgLatencyMs: 0 })
  keys.series = idle
  await wrapper.get('[data-action="refresh-service-status"]').trigger('click')
  await settle()
  assert.equal(wrapper.get('[data-endpoint-state]').attributes('data-endpoint-state'), 'idle')
  assert.match(wrapper.get('[data-endpoint-hint]').text(), /已采样 1 分钟/)
})
