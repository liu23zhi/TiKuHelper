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
let wrapper, pending = { billing: [], logs: [] }
const apiPath = path.join(root, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
const queued = kind => params => new Promise((resolve, reject) => pending[kind].push({ params, resolve, reject }))
api.exports = {
  getBillingRecords: queued('billing'), getLogList: queued('logs'),
  getIpLocations: () => Promise.resolve({ status: 200, data: {} }),
  getMyLocations: () => Promise.resolve({ status: 200, data: {} }),
}
require.cache[apiPath] = api
const ok = (id, total = 1, page = 1, amount = 0.123456) => ({
  status: 200, data: { data: id ? [{
    id, subject: 'SYNTHETIC_BILL_' + id, description: 'SYNTHETIC_LOG_' + id,
    module: 'search', action: 'search', type: 'normal_search', direction: 'expense', amount,
    createdAt: '2026-10-28T01:00:00+08:00', createTime: '2026-10-28T01:00:00+08:00',
  }] : [], total, page, pageSize: 10, summary: { totalAmount: amount, currentPageAmount: amount } },
})
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function mounted(kind) {
  const file = path.join(root, 'src/pages', kind === 'billing' ? 'BillingRecordList' : 'LogList', 'index.vue')
  delete require.cache[file]
  user.setStoredCurrentUser({ id: 7301, role: 'admin', username: 'synthetic-system-lists' })
  wrapper = mount(require(file), { attachTo: document.body, global: { plugins: [Antd] } })
  await settle()
  assert.equal(pending[kind].length, 1)
}
async function search(value) {
  await wrapper.find('.ant-input-search input').setValue(value)
  await wrapper.find('.ant-input-search-button').trigger('click')
  await settle()
}
function observed(label) {
  const vm = wrapper.vm
  const result = { label, keyword: vm.filters.keyword, rows: vm.dataSource.map(row => row.id),
    page: vm.pagination.current, total: vm.pagination.total,
    loading: vm.tableLoading ?? vm.tabLoading, error: vm.loadError || '',
    ...(vm.summary ? { totalAmount: vm.summary.totalAmount, currentPageAmount: vm.summary.currentPageAmount } : {}) }
  console.log('OBSERVATION ' + JSON.stringify(result))
  return result
}
afterEach(async () => {
  wrapper?.unmount(); wrapper = undefined
  for (const request of [...pending.billing, ...pending.logs]) request.resolve(ok(null, 0))
  await settle(); pending = { billing: [], logs: [] }
  Antd.message.destroy(); Antd.Modal.destroyAll(); document.body.replaceChildren(); user.clearStoredCurrentUser()
})
for (const kind of ['billing', 'logs']) {
  test(kind + ': latest search rows, pagination and billing totals survive an older page success', async () => {
    await mounted(kind)
    pending[kind][0].resolve(ok(10, 20, 1, 6)); await settle()
    wrapper.vm[kind === 'billing' ? 'handleTableChange' : 'handleTableChange']({ current: 2, pageSize: 10 })
    await settle()
    await search(' latest ')
    assert.equal(pending[kind].length, 3)
    assert.deepEqual(pending[kind][2].params, { keyword: 'latest', page: 1, pageSize: 10 })
    pending[kind][2].resolve(ok(91, 1, 1, 0.123456)); await settle()
    pending[kind][1].resolve(ok(70, 20, 2, 6)); await settle()
    const result = observed(kind + '-late-success')
    assert.deepEqual(result.rows, [91], 'LATEST_QUERY_ROWS: old page must not replace the current search')
    assert.equal(result.page, 1); assert.equal(result.total, 1)
    assert.equal(result.loading, false)
    if (kind === 'billing') {
      assert.equal(result.totalAmount, 0.123456); assert.equal(result.currentPageAmount, 0.123456)
      assert.ok(wrapper.text().includes('0.123456'), 'real Antd six-digit amount')
    }
  })
  test(kind + ': old failure and finally cannot release a newer loading state or show a stale error', async () => {
    await mounted(kind); await search('latest')
    pending[kind][0].reject(new Error('SYNTHETIC_OLD_GET_FAILURE')); await settle()
    const result = observed(kind + '-old-finally')
    assert.equal(result.loading, true, 'LATEST_QUERY_LOADING: retired finally cannot end the current request')
    assert.equal(document.body.textContent.includes(kind === 'billing' ? '获取账单列表失败' : '获取操作日志失败'), false)
    pending[kind][1].resolve(ok(91)); await settle()
    assert.deepEqual(wrapper.vm.dataSource.map(row => row.id), [91])
  })
}
test('logs: failed filtered read is not an empty success and native retry preserves exact filters/page', async () => {
  await mounted('logs'); pending.logs[0].resolve(ok(10, 20)); await settle()
  await search('retained')
  pending.logs[1].resolve({ status: 503, message: 'SYNTHETIC_FAILURE' }); await settle()
  const failed = observed('logs-status-failure')
  assert.ok(wrapper.text().includes('获取操作日志失败，请重试。'), 'PERSISTENT_LOG_ERROR: explain failure rather than stale/empty success')
  assert.deepEqual(failed.rows, [], 'previous filter rows cannot be labelled with new filter')
  assert.equal(failed.total, 0); assert.equal(failed.loading, false)
  assert.equal(wrapper.text().includes('当前还没有日志记录'), false, 'failed is distinct from successful empty')
  const retry = wrapper.find('[data-action="retry-request"]')
  assert.ok(retry.exists(), 'native retry button is reachable')
  await retry.trigger('click'); await settle()
  assert.equal(pending.logs.length, 3)
  assert.deepEqual(pending.logs[2].params, pending.logs[1].params)
  pending.logs[2].resolve(ok(92, 1)); await settle()
  const recovered = observed('logs-recovered')
  assert.deepEqual(recovered.rows, [92]); assert.equal(recovered.error, '')
  assert.equal(wrapper.text().includes('获取操作日志失败，请重试。'), false)
})
test('logs: successful empty, then rejected read, then empty recovery have distinct rendered states', async () => {
  await mounted('logs'); pending.logs[0].resolve(ok(null, 0)); await settle()
  assert.ok(wrapper.text().includes('当前还没有日志记录'))
  wrapper.vm.handleLogList(); await settle()
  pending.logs[1].reject(new Error('SYNTHETIC_REJECT')); await settle()
  observed('logs-rejected')
  assert.ok(wrapper.text().includes('获取操作日志失败，请重试。'))
  assert.equal(wrapper.text().includes('当前还没有日志记录'), false)
  wrapper.vm.handleLogList(); await settle()
  pending.logs[2].resolve(ok(null, 0)); await settle()
  assert.ok(wrapper.text().includes('当前还没有日志记录'))
  assert.equal(wrapper.vm.loadError, '')
  observed('logs-empty-recovered')
})

for (const [label, nextUser] of [
  ['identity', { id: 7302, role: 'user', username: 'synthetic-next-owner' }],
  ['role', { id: 7301, role: 'user', username: 'synthetic-system-lists' }],
]) {
  test('logs owner: ' + label + ' change to ordinary synchronously clears admin data and forbids further GET', async () => {
    await mounted('logs')
    pending.logs[0].resolve(ok(10, 20)); await settle()
    wrapper.vm.ipLocations['192.0.2.1'] = 'SYNTHETIC_OLD_ADMIN_LOCATION'
    await search('owner-filter')
    assert.equal(pending.logs.length, 2)
    user.setStoredCurrentUser(nextUser)
    const immediateRows = wrapper.vm.dataSource.map(row => row.id)
    await settle()
    const afterChange = observed('logs-owner-' + label + '-changed')
    const clearedLocations = Object.keys(wrapper.vm.ipLocations)
    wrapper.vm.handleLogList(); await settle()
    pending.logs[1].resolve(ok(70, 20, 2, 6)); await settle()
    const afterLate = observed('logs-owner-' + label + '-late-admin')
    assert.deepEqual(immediateRows, [], 'LOG_OWNER_SYNC_CLEAR: identity/role changes must remove prior rows synchronously')
    assert.deepEqual(afterChange.rows, [])
    assert.deepEqual(clearedLocations, [])
    assert.deepEqual(afterLate.rows, [], 'LOG_OWNER_LATE_ADMIN: retired admin data cannot publish into user scope')
    assert.equal(afterLate.loading, false)
    assert.equal(afterLate.total, 0)
    assert.equal(wrapper.vm.isAdmin, false)
    assert.equal(pending.logs.length, 2, 'ordinary principal cannot initiate or automatically reload log GET')
    assert.ok(wrapper.text().includes('当前账号无日志管理权限'), 'permission denial is not a network error or empty result')
    assert.equal(wrapper.text().includes('当前还没有日志记录'), false)
    assert.equal(wrapper.findAll('button').some(button => button.text().replace(/\s/g, '') === '重试'), false)
  })
}
test('logs owner: clearing identity retires pending data and does not permit another GET without a principal', async () => {
  await mounted('logs'); pending.logs[0].resolve(ok(10, 20)); await settle()
  wrapper.vm.handleLogList(); await settle()
  user.clearStoredCurrentUser(); await settle()
  wrapper.vm.handleLogList(); await settle()
  pending.logs[1].resolve(ok(70)); await settle()
  const result = observed('logs-owner-cleared')
  assert.deepEqual(result.rows, [], 'LOG_OWNER_LOGOUT_CLEAR: old GET must not restore data after logout')
  assert.equal(result.loading, false)
  assert.equal(pending.logs.length, 2, 'no principal cannot initiate an additional log read')
})
test('logs owner: new administrator reloads once and only its own deferred GET may publish', async () => {
  await mounted('logs'); pending.logs[0].resolve(ok(10, 20)); await settle()
  await search('old-admin-filter')
  user.setStoredCurrentUser({ id: 7302, role: 'admin', username: 'synthetic-next-admin' }); await settle()
  const afterChange = observed('logs-next-admin-changed')
  pending.logs[1].reject(new Error('SYNTHETIC_RETIRED_ADMIN_FAILURE')); await settle()
  const afterLate = observed('logs-next-admin-old-failure')
  if (pending.logs[2]) { pending.logs[2].resolve(ok(92)); await settle() }
  assert.deepEqual(afterChange.rows, [])
  assert.deepEqual(afterLate.rows, []); assert.equal(afterLate.loading, true)
  assert.equal(document.body.textContent.includes('获取操作日志失败'), false)
  assert.equal(pending.logs.length, 3)
  assert.deepEqual(pending.logs[2].params, { page: 1, pageSize: 10 })
  assert.deepEqual(wrapper.vm.dataSource.map(row => row.id), [92])
})
test('logs owner: same administrator balance-only publication preserves rows, location and in-flight GET', async () => {
  await mounted('logs'); pending.logs[0].resolve(ok(10, 20)); await settle()
  wrapper.vm.ipLocations['192.0.2.1'] = 'SYNTHETIC_CURRENT_LOCATION'
  wrapper.vm.handleLogList(); await settle()
  user.setStoredCurrentUser({ id: 7301, role: 'admin', username: 'synthetic-system-lists', balance: 3.123456 }); await settle()
  const retained = observed('logs-balance-only-retained')
  assert.deepEqual(retained.rows, [10]); assert.equal(retained.loading, true)
  assert.equal(wrapper.vm.ipLocations['192.0.2.1'], 'SYNTHETIC_CURRENT_LOCATION')
  assert.equal(pending.logs.length, 2)
  pending.logs[1].resolve(ok(91)); await settle()
  assert.deepEqual(wrapper.vm.dataSource.map(row => row.id), [91])
  assert.equal(wrapper.vm.tabLoading, false)
})
