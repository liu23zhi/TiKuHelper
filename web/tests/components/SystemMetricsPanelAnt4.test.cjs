/* Actual Vue/Antd DOM; only getSystemMetrics is synthetic. No network/provider/database calls. */
const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
Module._initPaths()
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const apiPath = path.join(root, 'src/api/TikuComponentApi.js')
let reads = []
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getSystemMetrics: (...args) => new Promise((resolve, reject) => reads.push({ args, resolve, reject })),
}
require.cache[apiPath] = api
const panelPath = process.env.TIKU_SYSTEM_METRICS_COMPONENT || path.join(root, 'src/pages/SystemSettings/SystemMetricsPanel.vue')
const Panel = require(panelPath)
const wrappers = []
const admin = { id: 17, role: 'admin', username: 'SYNTHETIC_ADMIN', balance: 1 }
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
function snapshot(overrides = {}) {
  return {
    schemaVersion: 1, startedAt: '2026-10-28T00:00:00Z', uptimeSeconds: 3600,
    http: { total: 120, inFlight: 3, failed: 4, latencyCount: 100, latencyTotalMillis: 2450, latencyMaxMillis: 160, buckets: [{ upperMillis: 100, count: 80 }] },
    cache: { search: { hits: 12, misses: 5, shared: 2 }, fallback: { hits: 7, misses: 4, shared: 1 } },
    billing: { attempts: 10, successes: 8, failures: 2 },
    tasks: { search: { total: 20, running: 2, succeeded: 17, failed: 1 }, fallback: { total: 9, running: 1, succeeded: 6, failed: 2 } },
    ...overrides,
  }
}
function complete(request, data = snapshot()) { request.resolve({ status: 200, data }) }
async function fixture(identity = admin) {
  user.setStoredCurrentUser(identity)
  const wrapper = mount(Panel, { attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(wrapper)
  await settle()
  return wrapper
}
function value(wrapper, field) { return wrapper.get(`[data-metric="${field}"]`).text() }
async function refresh(wrapper) {
  const button = wrapper.get('button[data-action="refresh-system-metrics"]')
  assert.equal(button.attributes('type'), 'button', 'refresh does not submit an ancestor settings form')
  await button.trigger('click'); await settle()
  return reads.at(-1)
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) if (wrapper.exists()) wrapper.unmount()
  await settle()
  reads = []
  window.localStorage.clear()
  Antd.message.destroy()
  document.body.replaceChildren()
})
test('SM01 admin mount reads once and renders real Console/Antd read-only diagnostic summaries', async () => {
  const w = await fixture()
  assert.equal(reads.length, 1); assert.deepEqual(reads[0].args, [])
  assert.ok(w.find('.ant-spin').exists())
  complete(reads[0]); await settle()
  assert.equal(value(w, 'http-total'), '120'); assert.equal(value(w, 'http-in-flight'), '3')
  assert.equal(value(w, 'http-failed'), '4'); assert.equal(value(w, 'latency-mean'), '24.5 ms')
  assert.equal(value(w, 'latency-max'), '160.0 ms')
  assert.equal(value(w, 'cache-search-hits'), '12'); assert.equal(value(w, 'cache-fallback-shared'), '1')
  assert.equal(value(w, 'billing-attempts'), '10'); assert.equal(value(w, 'billing-failures'), '2')
  assert.equal(value(w, 'tasks-search-running'), '2'); assert.equal(value(w, 'tasks-fallback-running'), '1')
  assert.ok(w.text().includes('诊断事件，不是账务凭证/收入'))
  assert.ok(w.text().includes('不是上游模型输入 Token 缓存命中率'));
  assert.ok(w.text().includes('当前保留任务'));
  assert.ok(w.text().includes('进程重启后清零')); assert.ok(w.text().includes('不轮询'))
  assert.equal(w.find('form, input, textarea, pre, code').exists(), false, 'no draft controls or raw response')
  assert.equal(w.findAll('.console-summary-grid').length, 1)
  user.setStoredCurrentUser({ ...admin, balance: 2 }); await settle(); await settle()
  assert.equal(reads.length, 1, 'display updates do not poll or reload')
  assert.equal(value(w, 'http-total'), '120')
  console.log('SM01_SUMMARIES_OK http=120/3/4 latency=24.5ms cache=12/5/2 billing=10/8/2 tasks-running=2/1 mountGET=1')
})
test('SM02 actual zeros remain zeros; absent optional tasks and absent latency samples are not fabricated', async () => {
  const w = await fixture()
  const zero = { hits: 0, misses: 0, shared: 0 }
  const data = snapshot({ uptimeSeconds: 0, http: { total: 0, inFlight: 0, failed: 0, latencyCount: 0, latencyTotalMillis: 0, latencyMaxMillis: 0, buckets: [] }, cache: { search: zero, fallback: zero }, billing: { attempts: 0, successes: 0, failures: 0 } })
  delete data.tasks
  complete(reads[0], data); await settle()
  for (const field of ['http-total', 'http-in-flight', 'http-failed', 'cache-search-hits', 'cache-fallback-misses', 'billing-successes']) assert.equal(value(w, field), '0')
  assert.equal(value(w, 'latency-mean'), '暂无耗时样本')
  assert.equal(value(w, 'latency-max'), '暂无耗时样本')
  assert.ok(w.text().includes('当前接口未提供任务摘要'))
  assert.equal(w.find('[data-metric="tasks-search-running"]').exists(), false)
})
test('SM03 non-admin and missing identity mount make zero GET and reveal no panel', async () => {
  const w = await fixture({ id: 18, role: 'user' })
  assert.equal(reads.length, 0); assert.equal(w.find('.system-metrics-panel').exists(), false)
  user.setStoredCurrentUser({ role: 'admin' }); await settle()
  assert.equal(reads.length, 0); assert.equal(w.find('.system-metrics-panel').exists(), false)
})
test('SM04 current failure remains visible without raw error and manual retry recovers', async () => {
  const w = await fixture()
  reads[0].reject(new Error('SYNTHETIC_SECRET_NETWORK_PAYLOAD')); await settle()
  assert.ok(w.get('.ant-alert-error').text().includes('读取运行诊断失败'))
  assert.equal(w.text().includes('SYNTHETIC_SECRET'), false)
  await settle(); assert.ok(w.find('.ant-alert-error').exists(), 'failure is persistent, not a transient toast')
  const retry = await refresh(w)
  assert.equal(reads.length, 2); complete(retry, snapshot({ uptimeSeconds: 42 })); await settle()
  assert.equal(w.find('.ant-alert-error').exists(), false); assert.equal(value(w, 'http-total'), '120')
})
test('SM05 403 is a persistent local permission error, never logout or raw server text', async () => {
  const w = await fixture()
  reads[0].resolve({ status: 403, message: 'SYNTHETIC_SECRET_FORBIDDEN' }); await settle()
  assert.ok(w.get('.ant-alert-error').text().includes('无权读取运行诊断'))
  assert.equal(w.text().includes('SYNTHETIC_SECRET'), false)
  assert.deepEqual(user.getStoredCurrentUser(), admin, '403 does not alter current login storage')
  assert.ok(w.find('button[data-action="refresh-system-metrics"]').exists())
  const retry = await refresh(w); complete(retry); await settle()
  assert.equal(w.find('.ant-alert-error').exists(), false)
})
test('SM06 newest successful GET owns DOM even if an older result arrives later', async () => {
  const w = await fixture()
  const old = reads[0]
  w.vm.handleRefresh(); await settle(); const latest = reads.at(-1)
  complete(latest, snapshot({ http: { ...snapshot().http, total: 222 } })); await settle()
  complete(old, snapshot({ http: { ...snapshot().http, total: 999 } })); await settle()
  assert.equal(value(w, 'http-total'), '222')
})
test('SM07 obsolete finally and catch cannot clear newer loading or display old failure', async () => {
  const w = await fixture()
  const old = reads[0]
  w.vm.handleRefresh(); await settle(); const latest = reads.at(-1)
  old.reject(new Error('SYNTHETIC_STALE_SECRET')); await settle()
  assert.equal(w.vm.loading, true); assert.ok(w.find('.ant-spin').exists())
  assert.equal(w.find('.ant-alert-error').exists(), false)
  complete(latest); await settle(); assert.equal(w.vm.loading, false)
})
test('SM08 admin identity replacement clears summaries synchronously with zero automatic GET', async () => {
  const w = await fixture(); complete(reads[0]); await settle()
  user.setStoredCurrentUser({ ...admin, id: 23, username: 'SYNTHETIC_OTHER_ADMIN' })
  assert.equal(w.vm.metrics, null); assert.equal(w.vm.loading, false)
  await settle(); assert.equal(reads.length, 1)
  assert.ok(w.text().includes('点击刷新读取当前进程诊断'))
  complete(await refresh(w)); await settle(); assert.equal(reads.length, 2)
})
test('SM09 demotion clears data and pending success, with zero new GET; promotion requires manual refresh', async () => {
  const w = await fixture(); complete(reads[0]); await settle()
  w.vm.handleRefresh(); await settle(); const pending = reads.at(-1); const before = reads.length
  user.setStoredCurrentUser({ ...admin, role: 'user' })
  assert.equal(w.vm.metrics, null); assert.equal(w.vm.loading, false)
  await settle(); complete(pending); await settle()
  assert.equal(reads.length, before); assert.equal(w.find('.system-metrics-panel').exists(), false)
  user.setStoredCurrentUser(admin); await settle()
  assert.equal(reads.length, before); assert.equal(w.find('[data-metric="http-total"]').exists(), false)
  complete(await refresh(w)); await settle(); assert.equal(value(w, 'http-total'), '120')
  console.log('SM09_DEMOTION_OK summary-cleared late-success-suppressed automaticGET=0 promotion=manual')
})
test('SM10 demotion suppresses late failure; unmount cannot publish pending completion', async () => {
  const w = await fixture(); const old = reads[0]
  user.setStoredCurrentUser({ ...admin, role: 'user' }); await settle()
  old.reject(new Error('SYNTHETIC_STALE_SECRET')); await settle()
  assert.equal(w.vm.errorText, ''); assert.equal(reads.length, 1)
  user.setStoredCurrentUser(admin); await settle(); w.vm.handleRefresh(); await settle()
  const pending = reads.at(-1); w.unmount(); complete(pending); await settle()
  assert.equal(document.body.textContent.includes('HTTP 请求'), false)
})
test('SM11 malformed successful payload is not converted into a false zero snapshot', async () => {
  const w = await fixture()
  complete(reads[0], snapshot({ billing: { attempts: 'not-a-count', successes: 0, failures: 0 } })); await settle()
  assert.ok(w.get('.ant-alert-error').text().includes('诊断数据格式暂不受支持'))
  assert.equal(w.find('[data-metric="http-total"]').exists(), false)
  const retry = await refresh(w); complete(retry); await settle()
  assert.equal(value(w, 'http-total'), '120')
})

test('SM12 UTC browser renders startup and sample timestamps in fixed Shanghai +08', async () => {
  const previousTZ = process.env.TZ
  const NativeDate = globalThis.Date
  process.env.TZ = 'UTC'
  globalThis.Date = class extends NativeDate {
    constructor(...args) { super(...(args.length ? args : ['2026-10-28T01:02:03Z'])) }
    static now() { return NativeDate.parse('2026-10-28T01:02:03Z') }
  }
  try {
    assert.equal(new Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC')
    const w = await fixture()
    complete(reads[0]); await settle()
    assert.equal(w.vm.startedAtText, '2026/10/28 08:00:00')
    assert.equal(w.vm.sampledAt, '2026/10/28 09:02:03')
    assert.ok(w.get('.metrics-meta').text().includes('进程启动：2026/10/28 08:00:00'))
    assert.ok(w.get('.metrics-meta').text().includes('本次读取：2026/10/28 09:02:03'))
    assert.ok(w.text().includes('HTTP 状态码 ≥400'))
    assert.ok(w.text().includes('仅非空收费批次的事务调用事件'))
    assert.ok(w.text().includes('不代表扣费笔数、到账金额或收入'))
    console.log('SM12_UTC_WITNESS browserTZ=UTC startup=2026/10/28_08:00:00 sampled=2026/10/28_09:02:03 displayTZ=Asia/Shanghai')
  } finally {
    globalThis.Date = NativeDate
    if (previousTZ === undefined) delete process.env.TZ
    else process.env.TZ = previousTZ
  }
})
