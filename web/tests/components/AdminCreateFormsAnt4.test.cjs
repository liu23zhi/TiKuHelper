const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory, RouterView } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')
const writes = [], mounts = []
const ok = data => ({ status: 200, data })
function mock(relative, exports) {
  const filename = path.resolve(__dirname, relative)
  const mod = new Module(filename); mod.loaded = true; mod.exports = exports
  require.cache[filename] = mod
}
mock('../../src/api/ScriptActivationCodeApi.js', {
  getScriptActivationCodes: async () => ok({ items: [], total: 0 }),
  createScriptActivationCodes: async data => { writes.push(data); return ok({ items: [{ id: 1, code: 'fixture-code' }] }) },
  getScriptActivationCodeDetail: async () => ok({}), updateScriptActivationCode: async () => ok({}),
})
mock('../../src/api/ReferralApi.js', {
  getReferralRebates: async () => ok({ items: [], total: 0 }),
  getGiftRecords: async () => ok({ items: [], total: 0 }), getGiftRecordSources: async () => ok([]),
  createManualReferralRebate: async data => { writes.push(data); return ok({ amount: data.amount }) },
  settleReferralRebate: async () => ok({}),
})
mock('../../src/api/ReviewRebateApi.js', {
  getReviewRebatePrograms: async () => ok({ data: [] }),
  getReviewRebateRules: async () => ok({ data: [] }),
  getReviewRebateClaims: async () => ok({ data: [], total: 0 }),
  createReviewRebateProgram: async data => { writes.push(data); return { status: 201, data: { ...data, id: 7 } } },
  updateReviewRebateProgram: async () => { throw new Error('empty program must use create') },
  createReviewRebateRule: async () => ok({}), updateReviewRebateRule: async () => ok({}),
  deleteReviewRebateRule: async () => ok({}), decideReviewRebateClaim: async () => ok({}),
})
const Codes = require('../../src/pages/ScriptActivationCode/index.vue')
const Rebates = require('../../src/pages/ReferralRebates/index.vue')
const Review = require('../../src/pages/ReviewRebateAdmin/index.vue')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(component, name) {
  user.setStoredCurrentUser({ id: 1, username: 'admin', role: 'admin', status: 'enabled' })
  const history = createMemoryHistory()
  const router = createRouter({ history, routes: [{ path: '/fixture', name, component }] })
  await router.push('/fixture'); await router.isReady()
  const wrapper = mount(RouterView, { attachTo: document.body, global: { plugins: [router, Antd] } })
  mounts.push({ wrapper, history }); await settle()
  return { wrapper, state: wrapper.findComponent(component).vm.$.setupState }
}
afterEach(async () => {
  for (const { wrapper, history } of mounts.splice(0)) { wrapper.unmount(); history.destroy() }
  user.clearStoredCurrentUser(); Antd.message.destroy(); Antd.Modal.destroyAll()
  await settle(); document.body.replaceChildren(); writes.length = 0
})
test('activation code Generate native submit reaches the API once', async () => {
  const { wrapper, state } = await open(Codes, 'script-activation-codes')
  state.createForm.count = 2
  wrapper.find('button[type="submit"]').element.click()
  await settle()
  assert.equal(writes.length, 1)
  assert.deepEqual(writes[0], { count: 2, remark: '', expiresAt: null })
  assert.equal(wrapper.find('textarea').element.value, 'fixture-code')
})
test('manual rebate native submit reaches the API with the entered amount', async () => {
  const { wrapper, state } = await open(Rebates, 'referral-rebates')
  Object.assign(state.manualForm, { beneficiaryUserId: '2', amount: '1.25', day: '2026-11-21', remark: 'fixture' })
  const button = wrapper.findAll('button[type="submit"]').find(item => item.text().replace(/\s/g, '') === '补录')
  assert.ok(button)
  button.element.click(); await settle()
  assert.equal(writes.length, 1)
  assert.deepEqual(writes[0], { beneficiaryUserId: 2, amount: 1.25, day: '2026-11-21', remark: 'fixture' })
  assert.match(wrapper.text(), /已补录/)
})
test('first review rebate program can be created by the visible Save button', async () => {
  const { wrapper, state } = await open(Review, 'review-rebate-admin')
  assert.equal(state.programMissing, true)
  state.programForm.name = '首次活动'
  await settle()
  const button = wrapper.findAll('button').find(item => item.text().replace(/\s/g, '') === '保存活动配置')
  assert.ok(button); button.element.click(); await settle()
  assert.equal(writes.length, 1)
  assert.equal(writes[0].name, '首次活动')
  assert.equal(state.programMissing, false)
  assert.doesNotMatch(wrapper.text(), /数据库中插入|尚未提供.*创建/)
})
