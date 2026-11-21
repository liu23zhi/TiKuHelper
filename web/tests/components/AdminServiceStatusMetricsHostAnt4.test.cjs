/* Real administrator status page, real diagnostics component, and Antd; only read APIs are synthetic. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
let statusReads = 0, metricsReads = 0, metricsResponses = []
const wrappers = []
const zeroCache = { hits: 0, misses: 0, shared: 0 }
function mockModule(relative, exports) {
  const filename = path.join(root, relative)
  const module = new Module(filename)
  module.loaded = true
  module.exports = exports
  require.cache[filename] = module
}
mockModule('src/api/AdminServiceStatusApi.js', {
  getAdminServiceStatus: async () => {
    statusReads += 1
    return { status: 200, data: {
      schemaVersion: 1, restartedAt: '2026-11-20T00:00:00Z', retained: 0, capacity: 1440, points: [],
      summary: { status: 'healthy', successRate: 1, totalRequests: 9, successRequests: 9, failedRequests: 0, avgLatencyMs: 12, windowMinutes: 5, breakdown: { upstream: 0, timeout: 0, auth: 0, quota: 0 } },
    } }
  },
})
mockModule('src/api/TikuComponentApi.js', {
  getSystemMetrics: async () => {
    metricsReads += 1
    if (metricsResponses.length) return metricsResponses.shift()
    return { status: 200, data: {
      schemaVersion: 1, startedAt: '2026-11-20T00:00:00Z', uptimeSeconds: 60,
      http: { total: 120, inFlight: 1, failed: 0, latencyCount: 119, latencyTotalMillis: 1190, latencyMaxMillis: 20, buckets: [] },
      cache: { search: zeroCache, fallback: zeroCache }, billing: { attempts: 0, successes: 0, failures: 0 },
    } }
  },
})
const Page = require(path.join(root, 'src/pages/AdminServiceStatus/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function open(identity) {
  user.setStoredCurrentUser(identity)
  // Keep the real diagnostic and Antd surfaces; only the canvas renderer is unavailable in jsdom.
  const wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd], stubs: { PortraitChart: true } } })
  wrappers.push(wrapper)
  await settle()
  return wrapper
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  await settle()
  user.clearStoredCurrentUser()
  Antd.message.destroy()
  Antd.Modal.destroyAll()
  document.body.replaceChildren()
  statusReads = 0
  metricsReads = 0
  metricsResponses = []
})

test('administrator server status hosts read-only runtime diagnostics and supports explicit metrics refresh', async () => {
  const wrapper = await open({ id: 17, role: 'admin', username: 'SYNTHETIC_ADMIN' })
  assert.match(wrapper.text(), /服务器状态详情/)
  assert.equal(statusReads, 1)
  assert.equal(metricsReads, 1)
  const panel = wrapper.get('.system-metrics-panel')
  assert.equal(panel.get('[data-metric="http-total"]').text(), '120')
  assert.equal(panel.find('form, input, textarea').exists(), false)
  const refresh = panel.get('button[data-action="refresh-system-metrics"]')
  assert.equal(refresh.attributes('type'), 'button')
  metricsResponses = [{ status: 403, message: 'SYNTHETIC_RAW_FORBIDDEN' }]
  await refresh.trigger('click'); await settle()
  assert.equal(metricsReads, 2)
  assert.match(panel.text(), /无权读取运行诊断/)
  const retry = panel.get('button[data-action="retry-request"]')
  assert.match(retry.text(), /重试/)
  assert.equal(statusReads, 1, 'failed metrics refresh does not reload route status')
  metricsResponses = [{ status: 200, data: {
    schemaVersion: 1, startedAt: '2026-11-20T00:00:00Z', uptimeSeconds: 60,
    http: { total: 123, inFlight: 0, failed: 0, latencyCount: 123, latencyTotalMillis: 1230, latencyMaxMillis: 20, buckets: [] },
    cache: { search: zeroCache, fallback: zeroCache }, billing: { attempts: 0, successes: 0, failures: 0 },
  } }]
  await retry.trigger('click'); await settle()
  assert.equal(metricsReads, 3)
  assert.equal(panel.get('[data-metric="http-total"]').text(), '123')
  assert.doesNotMatch(panel.text(), /无权读取运行诊断/)
  assert.equal(statusReads, 1, 'metrics refresh does not reload the unrelated API status')
})

test('non-administrator status page does not reveal or read runtime diagnostics', async () => {
  const wrapper = await open({ id: 18, role: 'user', username: 'SYNTHETIC_USER' })
  assert.equal(wrapper.find('.system-metrics-panel').exists(), false)
  assert.equal(statusReads, 0)
  assert.equal(metricsReads, 0)
})
