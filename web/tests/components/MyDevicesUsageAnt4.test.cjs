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
const lookups = []
let rows = []
let expiry = { days: 3, maxDays: 7, allowDisable: true }
const expiryUpdates = []
const deleted = []
let wrapper
const ok = data => ({ status: 200, data })
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  listMyDevices: () => Promise.resolve(ok({ data: { schemaReady: true, devices: rows } })),
  getMyDeviceLimit: () => Promise.resolve(ok({ data: { limit: 1, source: 'user_override', override: 1, editable: true, hookReady: true } })),
  getMyLocations: params => new Promise(resolve => lookups.push({ params, resolve })),
  blockMyDevice: () => Promise.resolve(ok({})),
  removeMyDevice: () => Promise.resolve(ok({})),
  renameMyDevice: () => Promise.resolve(ok({})),
  unblockMyDevice: () => Promise.resolve(ok({})),
  updateMyDeviceLimit: () => Promise.resolve(ok({})),
  getMyDeviceExpiry: () => Promise.resolve(ok({ data: expiry })),
  updateMyDeviceExpiry: days => { expiryUpdates.push(days); expiry = { ...expiry, days }; return Promise.resolve(ok({ data: expiry })) },
  deleteMyRemovedDevice: id => { deleted.push(id); rows = rows.filter(row => row.id !== id); return Promise.resolve(ok({ data: { id, deleted: true } })) },
}
require.cache[apiPath] = api

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
  deleted.length = 0
  expiry = { days: 3, maxDays: 7, allowDisable: true }
})

test('real device page reads nested expiry data, saves custom days and deletes only removed history', async () => {
  user.setStoredCurrentUser({ id: 7302, username: 'expiry-owner', role: 'user' })
  rows = [{ id: 2, deviceId: 'old-device', status: 'removed', deletable: true, revokedReason: 'user_removed' }]
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/my-devices', component: { template: '<div />' } }] })
  await router.push('/my-devices'); await router.isReady()
  wrapper = mount(require(path.join(web, 'src/pages/MyDevices/index.vue')), { attachTo: document.body, global: { plugins: [Antd, router] } })
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
  wrapper = mount(require(path.join(web, 'src/pages/MyDevices/index.vue')), { attachTo: document.body, global: { plugins: [Antd, router] } })
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
