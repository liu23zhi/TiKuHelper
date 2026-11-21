const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')
let wrapper, reads = [], pending = []
const apiPath = path.resolve(__dirname, '../../src/api/ArchivedUserApi.js')
const api = new Module(apiPath); api.loaded = true
const account = { id: 83001, username: 'ARCHIVED_USER', role: 'user', status: 'enabled', phone: '138****1111', archivedReason: 'admin_delete', registerSource: 'public', registerIp: '127.0.0.1', lastLoginIp: '127.0.0.2' }
api.exports = {
  getArchivedUsers: async () => ({ status: 200, data: { data: [account], total: 1 } }),
  getArchivedUserDetail: async id => { reads.push(id); return { status: 200, data: { data: account, bindings: [{ id: 71, kind: 'phone', valueMasked: '138****1111', state: 'released' }] } } },
  getArchivedUserIdentity: id => new Promise(resolve => pending.push({ id, resolve })),
  restoreArchivedUser: () => { throw new Error('unexpected write') },
}
require.cache[apiPath] = api
const Page = require('../../src/pages/ArchivedUsers/index.vue')
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
afterEach(async () => { wrapper?.unmount(); user.clearStoredCurrentUser(); for (const item of pending) item.resolve({ status: 403 }); await settle(); reads = []; pending = []; document.body.replaceChildren(); Antd.message.destroy() })

test('archived identity is fetched only by explicit click, cleared on close, and stale reads cannot reveal after administrator change', async () => {
  user.setStoredCurrentUser({ id: 84001, role: 'admin', status: 'enabled' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/archive', component: { template: '<div />' } }] })
  await router.push('/archive'); await router.isReady()
  wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd, router] } }); await settle()
  const detail = wrapper.findAll('button').find(item => item.text() === '详情'); assert.ok(detail)
  await detail.trigger('click'); await settle()
  assert.deepEqual(reads, [83001]); assert.equal(pending.length, 0)
  assert.match(document.body.textContent, /注册 IP/); assert.match(document.body.textContent, /138\*\*\*\*1111/)
  document.querySelector('[data-action="reveal-archived-identity"]').click(); await settle()
  assert.equal(pending.length, 1)
  pending[0].resolve({ status: 200, data: { userId: 83001, phone: '13800001111', wechatId: null, bindings: [{ id: 71, value: '13800001111' }] } }); await settle()
  assert.match(document.body.textContent, /13800001111/)
  document.querySelector('[data-action="hide-archived-identity"]').click(); await settle()
  assert.equal(document.body.textContent.includes('13800001111'), false)
  document.querySelector('[data-action="reveal-archived-identity"]').click(); await settle()
  user.setStoredCurrentUser({ id: 84002, role: 'admin', status: 'enabled' }); await settle()
  pending[1].resolve({ status: 200, data: { userId: 83001, phone: '13800001111', bindings: [{ id: 71, value: '13800001111' }] } }); await settle()
  assert.equal(wrapper.vm.identityRevealed, false); assert.equal(wrapper.vm.detailUser, null)
  assert.equal(document.body.textContent.includes('13800001111'), false)
  const nextDetail = wrapper.findAll('button').find(item => item.text() === '详情'); await nextDetail.trigger('click'); await settle()
  document.querySelector('[data-action="reveal-archived-identity"]').click(); await settle()
  pending[2].resolve({ status: 200, data: { userId: 83001, bindings: [{ id: 71, value: '13800001111' }] } }); await settle()
  wrapper.vm.setDetailOpen(false); await settle()
  assert.equal(wrapper.vm.identityRevealed, false); assert.equal(wrapper.vm.detailUser, null); assert.equal(wrapper.vm.identityCurrent.phone, undefined)
})
