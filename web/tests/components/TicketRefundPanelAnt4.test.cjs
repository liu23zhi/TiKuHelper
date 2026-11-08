/* Real Vue/Antd and production transport functions, isolated Axios adapter. */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
for (const name of ['HTMLInputElement', 'HTMLButtonElement', 'HTMLTextAreaElement']) global[name] = window[name]
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createMemoryHistory, createRouter } = require('vue-router')
const Antd = require('ant-design-vue')
const axios = require('axios')
const currentUser = require('../../src/utils/currentUser.js')

const base = '/adapter-service/ticket-refunds'
const owner = { id: 781, role: 'user', status: 'enabled' }
const admin = { id: 782, role: 'admin', status: 'enabled' }
const fakeOrder = { id: 14, orderNo: 'FIXTURE_ORDER_14', paidCents: 3200, paidAt: '2026-11-01T00:00:00Z' }
const fakeOriginal = { id: 1, orderId: 14, userId: owner.id, kind: 'original', requestedCents: 1250, status: 'pending_review' }
const fakeManual = { id: 2, userId: owner.id, kind: 'manual', requestedCents: 1500, status: 'pending_review' }
const image = new Blob([Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADElEQVR42mP8z8BQDwAE/wGJxJrtGQAAAABJRU5ErkJggg==', 'base64')], { type: 'image/png' })
const previousAdapter = axios.defaults.adapter
const prevCreate = URL.createObjectURL, prevRevoke = URL.revokeObjectURL
const calls = [], wrappers = [], revoke = [], created = []
let orders, records, statusOrders, proofWait, ownerEpoch, unexpected, rejectMutation

function response(config, data, status = 200) {
  return { status, data, config, headers: {}, request: { memoryOnly: true } }
}

axios.defaults.adapter = async (config) => {
  const method = config.method, url = config.url
  const data = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ method, url, data, actor: currentUser.getStoredCurrentUser()?.id, retryPolicy: config.retryPolicy })
  if (url === '/adapter-service/tickets' && method === 'get') return response(config, { items: [], total: 0 })
  if (url === base + '/eligible-orders' && method === 'get') return response(config, orders, statusOrders)
  if (url === base && method === 'get') return response(config, records)
  if (url === base && method === 'post') {
    if (rejectMutation) return response(config, { message: 'PRIVATE_PROVIDER_DETAIL_NOT_FOR_DISPLAY' }, 503)
    records = [data.kind === 'manual' ? { ...fakeManual } : { ...fakeOriginal }]
    return response(config, records[0], 201)
  }
  if (/\/proof$/.test(url) && method === 'post') {
    records = records.map((item) => ({ ...item, hasProof: true }))
    return response(config, {}, 201)
  }
  if (/\/proof$/.test(url) && method === 'get') {
    if (proofWait) await proofWait
    return response(config, image)
  }
  if (/\/process$/.test(url) && method === 'post' && data.action === 'submit') {
    records = records.map((item) => ({ ...item, status: 'unknown' }))
    return response(config, records[0], 202)
  }
  if (/\/process$/.test(url) && method === 'post' && data.action === 'cancel_unsent') {
    records = records.map((item) => ({ ...item, status: 'failed' }))
    return response(config, records[0], 200)
  }
  if (/\/(decision|reconcile|process)$/.test(url) && method === 'post') return response(config, {})
  unexpected.push(method + ' ' + url)
  throw new Error('Fixture denies all unmatched network')
}
const Panel = require('../../src/pages/Tickets/TicketRefundPanel.vue')
const Tickets = require('../../src/pages/Tickets/index.vue')

URL.createObjectURL = blob => { created.push(blob); return 'blob:http://localhost/refund-fixture' }
URL.revokeObjectURL = url => { revoke.push(url) }

function resetFixtures() {
  orders = [fakeOrder]
  records = []
  statusOrders = 200
  proofWait = null
  ownerEpoch = 1
  unexpected = []
  rejectMutation = false
}
resetFixtures()
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(isAdmin = false) {
  currentUser.setStoredCurrentUser(isAdmin ? admin : owner)
  const wrapper = mount(Panel, {
    attachTo: document.body,
    props: { isAdmin, captureOwner() { const expected = ownerEpoch; return () => ownerEpoch === expected } },
    global: { plugins: [Antd] },
  })
  wrappers.push(wrapper)
  await settle()
  return wrapper
}

afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  currentUser.clearStoredCurrentUser()
  await settle()
  document.body.replaceChildren()
  assert.deepEqual(unexpected, [])
  calls.length = revoke.length = created.length = 0
  resetFixtures()
})
after(() => { axios.defaults.adapter = previousAdapter; URL.createObjectURL = prevCreate; URL.revokeObjectURL = prevRevoke })

const writes = () => calls.filter((c) => c.method === 'post')
const stateOf = wrapper => wrapper.vm.$.setupState

test('ordinary tickets load independently; opening the refund panel binds it to the active owner', async () => {
  currentUser.setStoredCurrentUser(owner)
  const history = createMemoryHistory()
  const router = createRouter({ history, routes: [{ path: '/adapter/tickets/:id?', component: Tickets }] })
  await router.push('/adapter/tickets')
  await router.isReady()
  const wrapper = mount(Tickets, { attachTo: document.body, global: { plugins: [Antd, router] } })
  wrappers.push(wrapper)
  try {
    await settle()
    assert.equal(calls.some((call) => call.url.startsWith(base)), false)
    const opener = wrapper.findAll('button').find((button) => button.text().replace(/\s/g, '') === '退款工单')
    assert.ok(opener)
    await opener.trigger('click')
    await settle()
    assert.ok(calls.some((call) => call.url === base && call.actor === owner.id))
    assert.match(wrapper.text(), /可原路申请的订单/)
    currentUser.setStoredCurrentUser({ ...owner, id: 992 })
    await settle()
    assert.equal(calls.some((call) => call.url.startsWith(base) && call.actor === 992), false)
    assert.doesNotMatch(wrapper.text(), /可原路申请的订单/)
  } finally {
    history.destroy()
  }
})

test('partial amount uses exact cents, rejects excess locally, and posts the selected order once', async () => {
  const wrapper = await open()
  const state = stateOf(wrapper)
  assert.match(wrapper.text(), /可原路申请的订单：1 笔/)
  state.selectOrder('14')
  state.setRequestedYuan('32.01')
  await state.submitApplication()
  assert.equal(writes().length, 0)
  assert.match(wrapper.text(), /不能超过原订单实付金额/)
  state.setRequestedYuan('12.50')
  await wrapper.find('form').trigger('submit')
  await settle()
  assert.deepEqual(writes().map(({ method, url, data, retryPolicy }) => ({ method, url, data, retryPolicy })), [{
    method: 'post', url: base, data: { kind: 'original', orderId: 14, requestedCents: 1250 }, retryPolicy: 'single',
  }])
  assert.match(wrapper.text(), /申请已提交，等待审核/)
  assert.match(wrapper.text(), /¥12\.50/)
})

test('unavailable or malformed eligibility never opens the manual form', async () => {
  statusOrders = 503
  let wrapper = await open()
  assert.match(wrapper.text(), /可退款订单未确认/)
  assert.equal(wrapper.find('form').exists(), false)
  assert.equal(writes().length, 0)
  wrapper.unmount()
  wrappers.length = 0
  statusOrders = 200
  orders = [{ orderNo: 'malformed' }]
  wrapper = await open()
  assert.match(wrapper.text(), /可退款订单未确认/)
  assert.equal(wrapper.find('form').exists(), false)
})

test('manual proof uses only the protected refund URL; display URL is revoked on close', async () => {
  orders = []
  const wrapper = await open()
  const state = stateOf(wrapper)
  assert.match(wrapper.text(), /提交人工退款申请/)
  state.setRequestedYuan('15')
  await state.submitApplication()
  assert.deepEqual(writes()[0].data, { kind: 'manual', requestedCents: 1500 })
  const file = new File([image], 'synthetic-proof.png', { type: 'image/png' })
  const target = { value: 'synthetic-proof.png', files: [file] }
  await state.uploadProof(state.rows[0], { target })
  assert.equal(target.value, '')
  assert.equal(writes()[1].url, base + '/2/proof')
  assert.equal(writes()[1].data.get('file').name, 'synthetic-proof.png')
  assert.ok(calls.every((call) => call.url.startsWith(base)))
  assert.match(wrapper.text(), /查看受限凭证/)
  await state.viewProof(state.rows[0])
  await settle()
  assert.deepEqual(created, [image])
  assert.equal(state.proofUrl, 'blob:http://localhost/refund-fixture')
  state.closeProof()
  await settle()
  assert.deepEqual(revoke, ['blob:http://localhost/refund-fixture'])
  assert.equal(state.proofUrl, '')
})

test('admin scope requests review, never resubmits an unknown original result, and requires manual reference', async () => {
  records = [
    fakeOriginal,
    { ...fakeOriginal, id: 5, status: 'unknown' },
    { ...fakeManual, id: 6, status: 'frozen', hasProof: true },
  ]
  const wrapper = await open(true)
  const state = stateOf(wrapper)
  assert.equal(wrapper.find('form').exists(), false)
  assert.equal(calls.some((c) => c.url.endsWith('eligible-orders')), false)
  assert.match(wrapper.text(), /结果待核对，资金仍冻结/)
  state.setReviewYuan(state.rows[0], '12.49')
  await state.review(state.rows[0], 'approve')
  await state.process(state.rows[1], 'submit')
  await state.process(state.rows[1], 'query')
  await state.reconcile(state.rows[2], 'succeeded')
  state.setReconcileReference(state.rows[2], 'FIXTURE-RECON-1')
  await state.reconcile(state.rows[2], 'succeeded')
  assert.deepEqual(writes().map(({ url, data }) => ({ url, data })), [
    { url: base + '/1/decision', data: { action: 'approve', approvedCents: 1249 } },
    { url: base + '/5/process', data: { action: 'query' } },
    { url: base + '/6/reconcile', data: { outcome: 'succeeded', reference: 'FIXTURE-RECON-1' } },
  ])
})

test('unknown submit is not retried and does not display raw provider details', async () => {
  orders = []
  rejectMutation = true
  const wrapper = await open()
  const state = stateOf(wrapper)
  state.setRequestedYuan('15')
  await state.submitApplication()
  await state.submitApplication()
  assert.equal(writes().length, 1)
  assert.match(wrapper.text(), /操作结果未确认/)
  assert.doesNotMatch(wrapper.text(), /PRIVATE_PROVIDER_DETAIL_NOT_FOR_DISPLAY/)
})

test('202 original-channel submission remains an unknown outcome and switches to query-only', async () => {
  records = [{ ...fakeOriginal, status: 'frozen', approvedCents: 1250 }]
  const wrapper = await open(true)
  const state = stateOf(wrapper)
  await state.process(state.rows[0], 'submit')
  await state.process(state.rows[0], 'cancel_unsent')
  assert.equal(state.rows[0].status, 'unknown')
  assert.match(wrapper.text(), /结果待核对，资金仍冻结/)
  assert.doesNotMatch(wrapper.text(), /退款成功|提交原路退款/)
  await state.process(state.rows[0], 'submit')
  assert.deepEqual(writes().map(({ data }) => data), [{ action: 'submit' }])
  await state.process(state.rows[0], 'query')
  assert.deepEqual(writes().map(({ data }) => data), [{ action: 'submit' }, { action: 'query' }])
})

test('administrator may explicitly cancel only an unsubmitted original refund hold', async () => {
  records = [{ ...fakeOriginal, status: 'frozen', approvedCents: 1250 }]
  const wrapper = await open(true)
  const state = stateOf(wrapper)
  assert.match(wrapper.text(), /撤销未提交退款并解冻/)
  await state.process(state.rows[0], 'cancel_unsent')
  await state.process(state.rows[0], 'cancel_unsent')
  assert.deepEqual(writes().map(({ data }) => data), [{ action: 'cancel_unsent' }])
  assert.match(wrapper.text(), /退款未完成，冻结已解除/)
  assert.doesNotMatch(wrapper.text(), /撤销未提交退款并解冻|退款成功/)
})

test('owner retirement blocks already-issued proof display and old event handlers', async () => {
  orders = []
  records = [{ ...fakeManual, hasProof: true }]
  let resume
  proofWait = new Promise(resolve => { resume = resolve })
  const wrapper = await open()
  const state = stateOf(wrapper)
  const viewing = state.viewProof(state.rows[0])
  await settle()
  ownerEpoch += 1
  currentUser.setStoredCurrentUser({ ...owner, id: 991 })
  resume()
  await viewing
  await settle()
  assert.deepEqual(created, [])
  const before = calls.length
  await state.submitApplication()
  await state.load()
  assert.equal(calls.length, before)
})
