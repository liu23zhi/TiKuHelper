const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
require.extensions['.css'] = module => { module.exports = {} }
const { mount, flushPromises, config } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createRouter, createMemoryHistory, RouterView } = require('vue-router')
const axios = require('axios'), Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')
const { registerConsoleUI } = require('../../src/registerUI.js')
for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) delete config.global.components[name]
const adapter = axios.defaults.adapter, calls = [], errors = [], wrappers = []
axios.defaults.adapter = async request => {
  calls.push(request)
  let data
  if (request.url === '/api/user/session') data = { status: 'banned', banReason: '异常请求或接口滥用', banExpiresAt: '2027-01-01T00:00:00Z' }
  else if (request.url === '/api/system/storage-status') data = {}
  else if (request.url === '/api/tickets') data = { data: { items: [], total: 0 } }
  else throw new Error('Unexpected fixture HTTP ' + request.url)
  return { config: request, data, status: 200, headers: {} }
}
const Layout = require('../../src/layout/TikuAdapterLayout.vue')
const Tickets = require('../../src/pages/Tickets/index.vue')
async function settle() { await flushPromises(); await nextTick(); await flushPromises() }
afterEach(async () => {
  for (const { wrapper, router } of wrappers.splice(0)) { wrapper.unmount(); router.options.history.destroy() }
  user.clearStoredCurrentUser(); await settle(); Antd.message.destroy(); document.body.replaceChildren()
  assert.deepEqual(errors, []); calls.length = errors.length = 0
})
after(() => { axios.defaults.adapter = adapter })

test('封禁提示读取实际原因，原生申诉按钮可反复直接打开发起表单', async () => {
  user.setStoredCurrentUser({ id: 801, username: 'fixture-banned', role: 'user', status: 'banned' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: Layout, children: [
    { path: 'profile', component: { render: () => h('p', 'Profile') } },
    { path: 'tickets/:id?', component: Tickets, meta: { viewMode: 'self' } },
  ] }] })
  await router.push('/profile'); await router.isReady()
  const wrapper = mount({ render: () => h(RouterView) }, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }], stubs: { PageHeader: true, SiderMenu: true, StorageWarning: true }, config: { errorHandler: error => errors.push(error) } } })
  wrappers.push({ wrapper, router }); await settle()
  assert.match(document.querySelector('[data-account-ban-reason]').textContent, /异常请求或接口滥用/)
  assert.match(wrapper.text(), /2027/)
  const find = text => [...document.querySelectorAll('button')].find(button => button.textContent.replace(/\s/g, '') === text)
  find('提交工单申诉').click(); await settle()
  assert.equal(router.currentRoute.value.path, '/tickets')
  assert.equal(wrapper.findComponent(Tickets).vm.$.setupState.createVisible, true)
  assert.equal(wrapper.findComponent(Tickets).vm.$.setupState.draft.issueType, 'banned')
  const firstRequest = router.currentRoute.value.query.appealRequest
  wrapper.findComponent(Tickets).vm.$.setupState.closeCreate(); await settle()
  find('提交工单申诉').click(); await settle()
  assert.notEqual(router.currentRoute.value.query.appealRequest, firstRequest)
  assert.equal(wrapper.findComponent(Tickets).vm.$.setupState.createVisible, true)
  assert.ok(calls.some(call => call.url === '/api/tickets' && call.params.scope === 'self'))
})
