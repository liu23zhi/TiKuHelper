const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises, config } = require('@vue/test-utils')
for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) delete config.global.components[name]
const { h, nextTick } = require('vue')
const { createRouter, createMemoryHistory, RouterView } = require('vue-router')
const axios = require('axios')
const user = require('../../src/utils/currentUser.js')
const { registerConsoleUI } = require('../../src/registerUI.js')
const calls = [], wrappers = []
const originalAdapter = axios.defaults.adapter
axios.defaults.adapter = async config => {
  calls.push(config)
  let data = {}
  if (config.url.endsWith('/script-activation-codes')) data = { items: [{ id: 1, codeMask: '****e35a', status: 'unused' }], total: 1 }
  else if (config.url.endsWith('/script-activation-codes/1')) data = { code: { id: 1, code: 'TKA-' + 'a'.repeat(32), status: 'unused' }, usages: [] }
  else if (config.url === '/api/admin/devices') data = { data: [{ id: 1, userId: 3, deviceId: 'fixture-device', status: 'revoked', revokedAt: '2026-11-21T00:00:00Z' }], total: 1 }
  else if (config.url.endsWith('/portrait')) data = { scope: 'user', targetUserId: 3, totals: {}, base: { userId: 3, username: 'fixture-user' }, insights: [{ key: 'devices', title: '设备绑定状态', total: 0, items: [] }] }
  return { config, data, status: 200, headers: {} }
}
const Codes = require('../../src/pages/ScriptActivationCode/index.vue')
const Devices = require('../../src/pages/DeviceAdmin/index.vue')
const Portrait = require('../../src/pages/UserPortrait/index.vue')
async function settle() { await flushPromises(); await nextTick() }
async function open(Component, props, name) {
  user.setStoredCurrentUser({ id: 1, role: 'admin', status: 'enabled' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/fixture', name, component: { render: () => h(Component, props) } }] })
  await router.push('/fixture'); await router.isReady()
  const wrapper = mount(RouterView, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }], stubs: { PortraitChart: true } } })
  wrappers.push({ wrapper, router }); await settle(); return wrapper
}
afterEach(async () => {
  wrappers.splice(0).forEach(({ wrapper, router }) => { wrapper.unmount(); router.options.history.destroy() })
  user.clearStoredCurrentUser(); calls.length = 0; await settle(); document.body.replaceChildren()
})
test('activation code detail opens a real modal with registered labeled content', async () => {
  const wrapper = await open(Codes, {}, 'script-activation-codes')
  const button = wrapper.findAll('button').find(b => b.text().replace(/\s/g, '') === '详情'); assert.ok(button)
  button.element.click(); await settle()
  const dialog = document.querySelector('.ant-modal')
  assert.ok(dialog); assert.match(dialog.textContent, /激活码详情/)
  assert.match(dialog.textContent, /备注/); assert.match(dialog.textContent, /TKA-/)
  assert.equal(document.querySelector('.ant-drawer'), null)
})
test('embedded device management keeps target scope and all administrator actions', async () => {
  const wrapper = await open(Devices, { userId: 3, embedded: true }, 'user-list')
  const query = calls.find(c => c.url === '/api/admin/devices')
  assert.equal(query.params.userId, '3')
  for (const action of ['封禁', '删除记录']) assert.ok(wrapper.findAll('button').some(b => b.text().replace(/\s/g, '') === action), action)
  assert.doesNotMatch(wrapper.text(), /数据导入导出/)
})
test('the shared portrait view loads the requested user and displays the same inventory charts', async () => {
  const wrapper = await open(Portrait, { userId: 3, embedded: true }, 'user-list')
  assert.equal(calls.filter(c => c.url === '/api/admin/users/3/portrait').length, 1)
  assert.equal(calls.filter(c => c.url.endsWith('/overview')).length, 0)
  assert.match(wrapper.text(), /fixture-user/)
  assert.ok(wrapper.findAllComponents({ name: 'PortraitChart' }).some(chart => chart.props('title') === '设备绑定状态'))
})
test('a same-account ban removes an already loaded portrait and stops reads until access is restored', async () => {
  const wrapper = await open(Portrait, { userId: 3, embedded: true }, 'user-list')
  assert.match(wrapper.text(), /fixture-user/)
  const count = calls.length
  user.setStoredCurrentUser({ id: 1, role: 'admin', status: 'banned' }); await settle()
  assert.doesNotMatch(wrapper.text(), /fixture-user|设备绑定状态/)
  assert.equal(calls.length, count)
  user.setStoredCurrentUser({ id: 1, role: 'admin', status: 'enabled' }); await settle()
  assert.match(wrapper.text(), /fixture-user/)
  assert.equal(calls.filter(c => c.url === '/api/admin/users/3/portrait').length, 2)
})
process.on('exit', () => { axios.defaults.adapter = originalAdapter })
