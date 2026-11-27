const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const axios = require('axios')
const currentUser = require('../../src/utils/currentUser.js')

const admin = { id: 821, username: 'admin-fixture', role: 'admin', status: 'enabled' }
const ruleURL = '/api/admin/referral-rebate-rules'
const emptyRules = () => ({ enabled: false, rules: [] })
const emptyPage = () => ({ data: [], total: 0, page: 1, pageSize: 20 })
let policy = emptyRules()
let rebates = []
const calls = [], mounts = []
axios.defaults.adapter = config => {
  const { method, url } = config
  const data = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ method, url, data })
  let body
  if (url === ruleURL) {
    if (method === 'put') policy = structuredClone(data)
    body = structuredClone(policy)
  } else if (url === '/api/admin/referral-rebates') {
    body = { ...emptyPage(), data: rebates, total: rebates.length, summary: { pendingAmount: 4, settledAmount: 6 } }
  } else if (url === '/api/admin/gift-records') {
    body = emptyPage()
  } else if (url === '/api/admin/gift-records/sources') {
    body = []
  } else if (url === '/api/user/referral') {
    body = { enabled: false, code: 'REFCODE8', rate: 0.25123, invitedCount: 1, pendingAmount: 6 }
  } else {
    throw new Error('Unexpected referral fixture request: ' + method + ' ' + url)
  }
  return Promise.resolve({ status: 200, data: body, headers: {}, config, request: { memoryOnly: true } })
}

const Rebates = require('../../src/pages/ReferralRebates/index.vue')
const MyReferral = require('../../src/pages/MyReferral/index.vue')

async function settle() {
  await nextTick(); await flushPromises(); await nextTick(); await flushPromises()
}
async function open(component, user = admin) {
  currentUser.setStoredCurrentUser(user)
  const history = createMemoryHistory()
  const router = createRouter({ history, routes: [{ path: '/fixture', component }] })
  await router.push('/fixture')
  await router.isReady()
  const wrapper = mount(component, { attachTo: document.body, global: { plugins: [router, Antd] } })
  mounts.push({ wrapper, history })
  await settle()
  return wrapper
}
afterEach(async () => {
  for (const { wrapper, history } of mounts.splice(0)) {
    wrapper.unmount()
    history.destroy()
  }
  currentUser.clearStoredCurrentUser()
  Antd.Modal.destroyAll()
  Antd.message.destroy()
  await settle()
  document.body.replaceChildren()
  calls.length = 0
  policy = emptyRules()
  rebates = []
})

test('gift records administer an explicitly ordered list; 0%, unbounded and decimal inputs persist exactly', async () => {
  const wrapper = await open(Rebates)
  assert.ok(wrapper.find('[data-auto-rebate-rules]').exists(), 'gift records must include the rule administration panel')
  const panel = wrapper.get('[data-auto-rebate-rules]')
  const rows = () => panel.findAll('[data-referral-rule]')
  assert.equal(panel.get('[data-auto-switch]').attributes('aria-checked'), 'false')
  assert.match(panel.text(), /暂无自动返佣规则/)
  assert.match(wrapper.text(), /手动补录返佣/)
  assert.equal(calls.filter(call => call.url === ruleURL && call.method === 'get').length, 1)

  await panel.get('[data-auto-switch]').trigger('click')
  await panel.get('[data-add-referral-rule]').trigger('click')
  await rows()[0].get('input[data-rule-upper]').setValue('100.01')
  await rows()[0].get('input[data-rule-rate]').setValue('1.234567')
  await panel.get('[data-add-referral-rule]').trigger('click')
  await rows()[1].get('input[data-rule-lower]').setValue('50.00')
  await rows()[1].get('[data-rule-unbounded]').trigger('click')
  await rows()[1].get('input[data-rule-rate]').setValue('0')
  await rows()[1].get('[data-rule-up]').trigger('click')
  assert.equal(rows()[0].get('input[data-rule-lower]').element.value, '50.00')
  assert.equal(rows()[0].get('input[data-rule-upper]').attributes('disabled'), '')
  panel.get('button[type="submit"]').element.click()
  await settle()

  const writes = () => calls.filter(call => call.url === ruleURL && call.method === 'put')
  assert.deepEqual(writes().map(call => call.data), [{
    enabled: true,
    rules: [
      { enabled: true, lowerCents: 5000, upperCents: null, rateUnits: 0 },
      { enabled: true, lowerCents: 0, upperCents: 10001, rateUnits: 1234567 },
    ],
  }])
  assert.match(panel.text(), /规则已保存/)

  await rows()[0].get('input[data-rule-rate]').setValue('100')
  panel.get('button[type="submit"]').element.click()
  await settle()
  assert.equal(writes().length, 1, '100% never reaches the API')
  assert.match(panel.text(), /小于 100%/)
  await rows()[0].get('input[data-rule-rate]').setValue('0.000001')
  await rows()[0].get('[data-rule-enabled]').trigger('click')
  await rows()[1].get('[data-rule-delete]').trigger('click')
  panel.get('button[type="submit"]').element.click()
  await settle()
  assert.equal(writes().length, 2)
  assert.deepEqual(writes()[1].data, {
    enabled: true,
    rules: [{ enabled: false, lowerCents: 5000, upperCents: null, rateUnits: 1 }],
  })
})

test('ordinary accounts cannot see, read or write rule settings', async () => {
  const wrapper = await open(Rebates, { ...admin, role: 'user' })
  assert.equal(wrapper.find('[data-auto-rebate-rules]').exists(), false)
  assert.ok(calls.every(call => call.url !== ruleURL))
})

test('partially refunded rebate details distinguish retained amount and the fully reversed history', async () => {
  rebates = [
    { id: 1, inviterUserId: 2, orderNo: 'REF-PART-FIXTURE', status: 'settled', baseAmount: 100, rate: 0.1, amount: 10, reversedAmount: 4, refundedCents: 4000 },
    { id: 2, inviterUserId: 2, orderNo: 'REF-FULL-FIXTURE', status: 'reversed', baseAmount: 100, rate: 0.1, amount: 10, reversedAmount: 10, refundedCents: 10000 },
  ]
  const wrapper = await open(Rebates)
  const rows = wrapper.findAll('tr')
  const partial = rows.find(row => row.text().includes('REF-PART-FIXTURE'))
  assert.ok(partial, 'partial order row is rendered')
  assert.equal(partial.get('[data-rebate-net]').text(), '¥ 6')
  assert.match(partial.text(), /原额 ¥ 10.*已冲回 ¥ 4.*已退本金 ¥ 40/)
  assert.match(partial.text(), /已结算/)
  const full = rows.find(row => row.text().includes('REF-FULL-FIXTURE'))
  assert.ok(full, 'fully reversed order row is rendered')
  assert.equal(full.get('[data-rebate-net]').text(), '¥ 0')
  assert.match(full.text(), /已冲回/)
  assert.equal(full.find('button').exists(), false)
})

test('my referral keeps the invite code when automatic rules are off and does not display a legacy scalar rate', async () => {
  const wrapper = await open(MyReferral)
  assert.equal(wrapper.get('[data-invite-code]').text(), 'REFCODE8')
  assert.match(wrapper.text(), /自动返佣未开启/)
  assert.doesNotMatch(wrapper.text(), /25\.123%|该比例/)
})
