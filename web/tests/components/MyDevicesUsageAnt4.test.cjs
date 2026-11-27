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
const axios = require('axios')
const user = require(path.join(web, 'src/utils/currentUser.js'))
const lookups = []
let rows = []
let expiry = { days: 3, maxDays: 7, allowDisable: true }
const expiryUpdates = []
const limitUpdates = []
const quotaReads = []
const limitView = { limit: 1, source: 'user_override', override: 1, upperBound: 6, editable: true, hookReady: true }
const defaultQuota = () => ({ status: 200, data: { schemaReady: true, used: 2, limit: 2, available: 0 } })
let readQuota = defaultQuota
const deleted = []
const blocked = []
let wrapper
const ok = data => ({ status: 200, data })
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  listMyDevices: () => Promise.resolve(ok({ data: { schemaReady: true, devices: rows } })),
  getMyDeviceLimit: () => Promise.resolve(ok({ data: limitView })),
  getMyLocations: params => new Promise(resolve => lookups.push({ params, resolve })),
  blockMyDevice: id => {
    blocked.push(id)
    rows = rows.map(row => row.id !== id ? row : { ...row, status: 'blocked', blockable: false, deletable: false, blocked: true, blockId: 10 })
    return Promise.resolve(ok({}))
  },
  removeMyDevice: () => Promise.resolve(ok({})),
  renameMyDevice: () => Promise.resolve(ok({})),
  unblockMyDevice: () => Promise.resolve(ok({})),
  updateMyDeviceLimit: limit => { limitUpdates.push(limit); return Promise.resolve(ok({ data: { ...limitView, limit, override: limit } })) },
  getMyDeviceExpiry: () => Promise.resolve(ok({ data: expiry })),
  updateMyDeviceExpiry: days => { expiryUpdates.push(days); expiry = { ...expiry, days }; return Promise.resolve(ok({ data: expiry })) },
  deleteMyRemovedDevice: id => { deleted.push(id); rows = rows.filter(row => row.id !== id); return Promise.resolve(ok({ data: { id, deleted: true } })) },
}
require.cache[apiPath] = api

// Other device operations retain the module fixtures above. Quota goes through the real request wrapper.
const adapter = axios.defaults.adapter
let Page
try {
  axios.defaults.adapter = async config => {
    assert.equal(config.url, '/api/user/device-quota')
    assert.equal(config.method, 'get')
    quotaReads.push(config)
    return { ...await readQuota(), config, headers: {} }
  }
  Page = require(path.join(web, 'src/pages/MyDevices/index.vue'))
} finally { axios.defaults.adapter = adapter }

async function settle() {
  await nextTick()
  await flushPromises()
  await nextTick()
  await flushPromises()
  await nextTick()
}

afterEach(async () => {
  if (wrapper?.exists()) wrapper.unmount()
  Antd.Modal.destroyAll()
  Antd.message.destroy()
  await settle()
  document.body.replaceChildren()
  lookups.length = 0
  expiryUpdates.length = 0
  limitUpdates.length = 0
  quotaReads.length = 0
  readQuota = defaultQuota
  user.clearStoredCurrentUser()
  deleted.length = 0
  blocked.length = 0
  expiry = { days: 3, maxDays: 7, allowDisable: true }
})

test('real device page reads nested expiry data, saves custom days and deletes only removed history', async () => {
  user.setStoredCurrentUser({ id: 7302, username: 'expiry-owner', role: 'user' })
  rows = [{ id: 2, deviceId: 'old-device', status: 'removed', deletable: true, revokedReason: 'user_removed' }]
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/my-devices', component: { template: '<div />' } }] })
  await router.push('/my-devices'); await router.isReady()
  wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd, router] } })
  await settle()
  assert.equal(wrapper.vm.expiryLoaded, true)
  assert.equal(wrapper.vm.expiryDays, 3)
  assert.equal(wrapper.vm.expiryMaxDays, 7)
  assert.ok(wrapper.text().includes('管理员上限：7 天'))
  assert.ok(wrapper.text().includes('删除记录'))
  wrapper.vm.expiryDays = 2
  await wrapper.vm.saveExpiry(); await settle()
  assert.deepEqual(expiryUpdates, [2])
  assert.equal(wrapper.vm.expiryDays, 2)
  await wrapper.vm.deleteRemovedDevice(wrapper.vm.listView.devices[0]); await settle()
  assert.deepEqual(deleted, [2])
  assert.equal(wrapper.vm.listView.devices.length, 0)
})

test('real device table renders first/recent IP with geolocation and absolute times, ignoring superseded lookup results', async () => {
  user.setStoredCurrentUser({ id: 7301, username: 'device-owner', role: 'user' })
  rows = [{
    id: 1, deviceId: 'my-device', status: 'active',
    firstIp: '172.18.0.1', lastIp: '192.168.1.1',
    firstSeenAt: '2026-11-01T01:02:03Z', lastSeenAt: '2026-11-02T01:02:03Z',
  }]
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/my-devices', component: { template: '<div />' } }] })
  await router.push('/my-devices')
  await router.isReady()
  wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd, router] } })
  await settle()
  const headers = wrapper.findAll('th').map(cell => cell.text())
  for (const label of ['首次使用 IP', '最近使用 IP', '首次使用时间', '最近使用时间']) assert.ok(headers.includes(label), label)
  assert.ok(wrapper.text().includes('2026-11-01 09:02:03'))
  assert.ok(wrapper.text().includes('2026-11-02 09:02:03'))
  assert.ok(!wrapper.text().includes('刚刚'))
  assert.deepEqual(lookups[0].params, { ips: '172.18.0.1,192.168.1.1' })
  lookups[0].resolve(ok({ ip: { '172.18.0.1': { address: '私有地址' }, '192.168.1.1': { address: '私有地址' } } }))
  await settle()
  assert.deepEqual(wrapper.findAll('.device-ip').map(cell => cell.text()), ['172.18.0.1\n（私有地址）', '192.168.1.1\n（私有地址）'])

  rows = [{ ...rows[0], firstIp: '', lastIp: '198.51.100.1' }]
  await wrapper.vm.reload()
  await settle()
  const older = lookups[1]
  rows = [{ ...rows[0], lastIp: '198.51.100.2' }]
  await wrapper.vm.reload()
  await settle()
  lookups[2].resolve(ok({ ip: { '198.51.100.2': { address: '当前归属地' } } }))
  await settle()
  older.resolve(ok({ ip: { '198.51.100.1': { address: '旧响应归属地' } } }))
  await settle()
  assert.deepEqual(wrapper.findAll('.device-ip').map(cell => cell.text()), ['未记录', '198.51.100.2\n（当前归属地）'])
  assert.equal(wrapper.vm.ipLocations['198.51.100.1'], undefined, 'superseded list lookup cannot publish')
})

test('removed device can be blacklisted from its row, with precise impact and recent use in confirmation', async () => {
  user.setStoredCurrentUser({ id: 7303, username: 'history-owner', role: 'user' })
  rows = [
    { id: 3, deviceId: 'extension:history-fixture', status: 'removed', deletable: true, lastSeenAt: '2026-11-02T01:02:03Z' },
    { id: 4, deviceId: 'extension:blocked-fixture', status: 'blocked', blockId: 9, blocked: true },
  ]
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/my-devices', component: { template: '<div />' } }] })
  await router.push('/my-devices'); await router.isReady()
  wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd, router] } })
  await settle()
  assert.match(wrapper.text(), /共 2 台（使用中 0 台，已移除 1 台，已拉黑 1 台）/)
  const history = wrapper.findAll('tbody tr').find(row => row.text().includes('已移除'))
  const action = history.findAll('button').find(button => button.text().replace(/\s/g, '') === '拉黑')
  assert.ok(action, 'removed history retains a blacklist action')
  await action.trigger('click')
  await settle()
  assert.match(document.body.textContent, /extension:history-fixture/)
  assert.match(document.body.textContent, /2026-11-02 09:02:03/)
  assert.match(document.body.textContent, /当前账号/)
  assert.deepEqual(blocked, [])
  await wrapper.vm.confirmBlockDevice(); await settle()
  assert.deepEqual(blocked, [3])
  assert.match(wrapper.text(), /共 2 台（使用中 0 台，已移除 0 台，已拉黑 2 台）/)
  wrapper.vm.statusFilter = 'removed'; await settle()
  assert.match(wrapper.text(), /共 0 台（使用中 0 台，已移除 0 台，已拉黑 0 台）/)
  assert.match(wrapper.text(), /暂无符合条件的设备/)
})


async function mountSettings() {
  user.setStoredCurrentUser({ id: 7351, username: 'fixture-quota-owner', role: 'user' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/my-devices', component: { template: '<div />' } }] })
  await router.push('/my-devices')
  await router.isReady()
  wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd, router] } })
  await settle()
}

const usedSlots = () => wrapper.get('[data-device-usage]').text()

test('the grouped settings show slots separately from history and keep both original save actions', async () => {
  rows = [
    { id: 1, deviceId: 'fixture-active', deviceLabel: 'active row', status: 'active' },
    { id: 2, deviceId: 'fixture-removed', deviceLabel: 'removed row', status: 'removed' },
  ]
  await mountSettings()
  const usage = wrapper.get('[data-device-usage]')
  const card = usage.element.closest('.console-panel')
  assert.ok(card.contains(wrapper.get('[data-device-limit]').element))
  assert.ok(card.contains(wrapper.get('[data-device-expiry-state]').element))
  assert.equal(usedSlots(), '2 个')
  assert.equal(quotaReads.length, 1)
  const config = quotaReads[0]
  assert.equal(config.withCredentials, true)
  assert.equal(config.params, undefined)
  assert.equal(config.data, undefined)
  const [limitInput, expiryInput] = card.querySelectorAll('input[role=spinbutton]')
  assert.deepEqual([limitInput.getAttribute('aria-valuemin'), limitInput.getAttribute('aria-valuemax')], ['0', '6'])
  assert.deepEqual([expiryInput.getAttribute('aria-valuemin'), expiryInput.getAttribute('aria-valuemax')], ['0', '7'])

  wrapper.vm.statusFilter = 'removed'
  wrapper.vm.expiryDays = 0
  await settle()
  assert.deepEqual(wrapper.findAll('tbody .device-label').map(label => label.text()), ['removed row'])
  assert.equal(usedSlots(), '2 个')
  assert.match(wrapper.get('[data-device-expiry-state]').text(), /已启用.*3/)
  wrapper.vm.limitDraft = 2
  await wrapper.vm.saveDeviceLimit()
  assert.deepEqual(limitUpdates, [2])
  await wrapper.vm.saveExpiry()
  assert.deepEqual(expiryUpdates, [0])
  assert.equal(wrapper.get('[data-device-expiry-state]').text(), '已关闭')
})

for (const [name, status, data, expected] of [
  ['ready zero', 200, { schemaReady: true, used: 0 }, '0 个'],
  ['not migrated', 200, { schemaReady: false, used: 0 }, '暂不可用'],
  ['invalid or absent server count', 200, { schemaReady: true, available: 4 }, '暂不可用'],
  ['service failure with zero-shaped data', 503, { schemaReady: true, used: 0 }, '暂不可用'],
]) {
  test('quota displays ' + name + ' without falling back to history row count', async () => {
    rows = [{ id: 1, deviceId: 'fixture-active', status: 'active' }]
    readQuota = () => ({ status, data })
    await mountSettings()
    assert.equal(usedSlots(), expected)
    assert.equal(wrapper.get('.limit-editor button').element.disabled, false)
    assert.equal(wrapper.get('.expiry-section button').element.disabled, false)
  })
}

test('reading quota does not disable settings and a late prior account response cannot overwrite the current one', async () => {
  rows = []
  const pending = []
  readQuota = () => new Promise(resolve => { pending.push(used => resolve({ status: 200, data: { schemaReady: true, used } })) })
  try {
    await mountSettings()
    assert.equal(usedSlots(), '读取中…')
    assert.equal(wrapper.get('.limit-editor button').element.disabled, false)
    assert.equal(wrapper.get('.expiry-section button').element.disabled, false)
    user.setStoredCurrentUser({ id: 7352, username: 'fixture-later-quota-owner', role: 'user' })
    await settle()
    assert.equal(pending.length, 2)
    assert.equal(quotaReads[0].signal.aborted, true)
    pending[1](4)
    await settle()
    pending[0](99)
    await settle()
    assert.equal(usedSlots(), '4 个')
    user.clearStoredCurrentUser()
    await settle()
    assert.equal(usedSlots(), '暂不可用')
  } finally {
    for (const finish of pending) finish(0)
  }
})
