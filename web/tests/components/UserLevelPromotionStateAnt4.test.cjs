const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
require('../helpers/vue-dom.cjs')
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createMemoryHistory, createRouter } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')
const apiPath = path.resolve(__dirname, '../../src/api/UserLevelApi.js')
const api = new Module(apiPath)
let source = 'empty'
const writes = []
api.loaded = true
api.exports = {
  getUserLevels: async () => ({ data: [] }),
  getRechargePromotionTiers: async () => ({ data: { source, rows: [], effective: [] } }),
  replaceRechargePromotionTiers: async rows => {
    writes.push(rows)
    return { data: { source: 'empty', rows: [], effective: [] } }
  },
  createUserLevel: async () => ({}),
  updateUserLevel: async () => ({}),
  deleteUserLevel: async () => ({}),
  reevaluateUserLevels: async () => ({}),
  previewRechargePromotionTier: async () => ({}),
}
require.cache[apiPath] = api
const Page = require('../../src/pages/UserLevel/index.vue')
const mounted = []
async function open(nextSource) {
  source = nextSource
  user.setStoredCurrentUser({ id: 91001, role: 'admin', username: 'fixture-admin', status: 'enabled' })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/user-level', component: Page }] })
  await router.push('/user-level')
  await router.isReady()
  const wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd, router] } })
  mounted.push({ wrapper, router })
  await flushPromises()
  await nextTick()
  return wrapper
}
afterEach(() => {
  for (const { wrapper, router } of mounted.splice(0)) {
    wrapper.unmount()
    router.options.history.destroy()
  }
  user.clearStoredCurrentUser()
  writes.length = 0
  document.body.replaceChildren()
})
test('empty promotion source stays empty and explains that no gift will be issued', async () => {
  const wrapper = await open('empty')
  assert.equal(wrapper.vm.tierSource, 'empty')
  assert.match(wrapper.text(), /当前没有可用充值赠送档位，充值不会发放赠金/)
  assert.doesNotMatch(wrapper.text(), /正在使用系统默认六档|回退到系统默认六档/)
  await wrapper.vm.saveTiers()
  await nextTick()
  assert.deepEqual(writes, [[]])
  assert.equal(wrapper.vm.tierSource, 'empty')
  assert.match(wrapper.text(), /档位表已清空，充值赠送已停止/)
})
test('unavailable promotion configuration never claims default gifts are active', async () => {
  const wrapper = await open('unavailable')
  assert.equal(wrapper.vm.tierSource, 'unavailable')
  assert.match(wrapper.text(), /配置当前不可用，系统不会发放赠金/)
  assert.doesNotMatch(wrapper.text(), /正在使用系统默认六档|暂使用系统默认六档/)
})
