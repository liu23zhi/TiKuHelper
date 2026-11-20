/* External-copy transaction fixture: real SystemSettings + real metrics panel + real Antd. */
const { test } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const pagePath = process.env.TIKU_SYSTEM_SETTINGS_COMPONENT || path.join(root, 'src/pages/SystemSettings/index.vue')
// External copied parent resolves only its existing relative children to the unchanged source tree.
const originalResolve = Module._resolveFilename
Module._resolveFilename = function(request, parent, ...rest) {
  if (parent?.filename === pagePath && request.startsWith('./') && request !== './SystemMetricsPanel.vue') request = path.join(root, 'src/pages/SystemSettings', request)
  return originalResolve.call(this, request, parent, ...rest)
}
let metricsReads = 0, settingsReads = 0, paymentReads = 0, writes = 0
const apiPath = path.join(root, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
const write = () => { writes += 1; throw Error('SYNTHETIC_UNEXPECTED_MUTATION') }
api.exports = {
  getSystemMetrics: () => { metricsReads += 1; return Promise.resolve({ status: 200, data: {} }) },
  getSystemSettings: () => { settingsReads += 1; return Promise.resolve({ status: 200, data: { siteName: '原始站点' } }) },
  getPaymentMethods: () => { paymentReads += 1; return Promise.resolve({ status: 200, data: [] }) },
  createPaymentMethod: write, deletePaymentMethod: write, updatePaymentMethod: write, updateSystemSettings: write,
}
require.cache[apiPath] = api
const Page = require(pagePath)
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
const zero = { hits: 0, misses: 0, shared: 0 }
const data = { schemaVersion: 1, startedAt: '2026-10-28T00:00:00Z', uptimeSeconds: 0, http: { total: 0, inFlight: 0, failed: 0, latencyCount: 0, latencyTotalMillis: 0, latencyMaxMillis: 0, buckets: [] }, cache: { search: zero, fallback: zero }, billing: { attempts: 0, successes: 0, failures: 0 } }
test('SM_PARENT system settings no longer hosts runtime diagnostics or reads metrics', async () => {
  user.setStoredCurrentUser({ id: 17, role: 'admin', username: 'SYNTHETIC_ADMIN' })
  const w = mount(Page, { attachTo: document.body, global: { plugins: [Antd] } })
  try {
    await settle()
    assert.equal(w.find('.system-metrics-panel').exists(), false, 'SM_PARENT_SETTINGS_MUST_NOT_HOST_DIAGNOSTICS')
    assert.equal(metricsReads, 0, 'SM_PARENT_SETTINGS_MUST_NOT_READ_DIAGNOSTICS')
    const input = w.get('input[placeholder="例如 题库"]')
    await input.setValue('未保存的系统草稿')
    assert.equal(input.element.value, '未保存的系统草稿')
    assert.equal(input.element.value, '未保存的系统草稿')
    assert.equal(w.vm.formState.siteName, '未保存的系统草稿')
    assert.equal(settingsReads, 1); assert.equal(paymentReads, 1); assert.equal(metricsReads, 0); assert.equal(writes, 0)
    assert.equal(user.getStoredCurrentUser().id, 17)
    console.log('SM_PARENT_WITNESS metricsGET=0 settingsGET=1 paymentGET=1 writes=0 draft=未保存的系统草稿 diagnostics=server-status-only')
  } finally {
    w.unmount(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren(); user.clearStoredCurrentUser(); window.localStorage.clear()
    Module._resolveFilename = originalResolve
  }
})
