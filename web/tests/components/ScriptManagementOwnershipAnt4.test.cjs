const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), Module = require('node:module')
require('../helpers/vue-dom.cjs')
for (const name of ['HTMLInputElement', 'HTMLButtonElement', 'HTMLTextAreaElement']) global[name] = window[name]
const { mount, flushPromises } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createMemoryHistory, createRouter, RouterView } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')

const admin = { id: 111, role: 'admin', username: 'fixture_admin' }
const normal = { id: 222, role: 'user', username: 'fixture_user' }
const ownerCommand = 'powershell -EncodedCommand ZgA='
const row = (name, ownerId) => ({ id: 7, ownerId, name, scheme: 'https', baseDomain: 'example.test', shortCode: 'a', versionId: 5, command: ownerCommand })
const okay = data => ({ status: 200, data })
const reads = [], writes = [], purchases = [], redemptions = [], updates = [], policyWrites = [], deletes = [], mounts = []
let holdReads = false, holdWrites = false, activationPriceFixture = 12.345678
let listFixture = [row('现有脚本', 111)]

const apiPath = path.resolve(__dirname, '../../src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = {
  getScripts(params) {
    if (!holdReads) return Promise.resolve(okay(listFixture))
    return new Promise(resolve => reads.push({ params, resolve }))
  },
  getScriptVersions: async () => okay([{ id: 5, version: 'synthetic', nativeLeaseProtocol: true, isCurrent: true }]),
  getScriptDomainSettings: async () => okay({ domains: ['example.test'], allowWildcard: true }),
  getScriptEscrowPolicy: async () => okay({ targetMicros: 80000000, minimumMicros: 60000000, multiplierMicros: 1000000 }),
  getScriptActivationCodePrice: async () => okay({ enabled: true, price: activationPriceFixture }),
  updateScriptEscrowPolicy: async payload => { policyWrites.push(payload); return okay(payload) },
  updateScriptActivationCodePrice: async payload => okay({ enabled: Number(payload.price) > 0, price: Number(payload.price) }),
  purchaseScriptActivationCode(key, quote) {
    return new Promise(resolve => purchases.push({ key, quote, resolve }))
  },
  async redeemScriptActivationCode(payload) { redemptions.push(payload); return okay({ id: 555 }) },
  async updateScript(payload) { updates.push(payload); return okay({}) },
  deleteScript(payload) {
    return new Promise((resolve, reject) => deletes.push({ payload, resolve, reject }))
  },
  createScript(payload, key) {
    if (!holdWrites) return Promise.resolve(okay({}))
    return new Promise(resolve => writes.push({ payload, key, resolve }))
  },
}
require.cache[apiPath] = api
const PAGE = require('../../src/pages/ScriptManagement/index.vue')

async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(account = admin) {
  user.setStoredCurrentUser(account)
  const history = createMemoryHistory(), router = createRouter({ history, routes: [
    { path: '/adapter/scripts', component: PAGE },
    { path: '/other', component: { render: () => h('div', 'Other') } },
    { path: '/login', component: { render: () => h('div', 'Login') } },
  ] })
  await router.push('/adapter/scripts'); await router.isReady()
  const wrapper = mount(RouterView, { attachTo: document.body, global: { plugins: [router, Antd] } })
  mounts.push({ wrapper, history })
  await settle()
  return { wrapper, router, page: wrapper.findComponent(PAGE), state: wrapper.findComponent(PAGE).vm.$.setupState }
}

afterEach(async () => {
  for (const { wrapper, history } of mounts.splice(0)) { wrapper.unmount(); history.destroy() }
  Antd.Modal.destroyAll(); Antd.message.destroy()
  await settle()
  user.clearStoredCurrentUser()
  document.body.replaceChildren()
  reads.length = writes.length = purchases.length = redemptions.length = updates.length = policyWrites.length = deletes.length = 0; holdReads = holdWrites = false; activationPriceFixture = 12.345678
  listFixture = [row('现有脚本', 111)]
})

const activeLease = id => ({ status: 409, data: { code: 'SCRIPT_ACTIVE_LEASE_CONFIRM_REQUIRED', activeRunSessionId: id } })
const escrowRow = () => ({ ...row('冻结金脚本', 111), billingMode: 'escrow' })
function clickActiveClose(label) {
  const modal = [...document.querySelectorAll('.ant-modal')].find(el => el.textContent.includes('客户端正在运行'))
  assert.ok(modal, 'second independent confirmation is visible')
  const button = [...modal.querySelectorAll('button')].find(el => el.textContent.replace(/\s+/g, '') === label)
  assert.ok(button, label)
  button.click()
}

test('an offline escrow script closes after one request without a second confirmation', async () => {
  listFixture = [escrowRow()]
  const { state } = await open()
  const operation = state.handleDelete(state.rows[0])
  assert.deepEqual(deletes[0].payload, { id: 7 })
  listFixture = []
  deletes[0].resolve(okay({ state: 'closed', refundedMicros: 80000000 }))
  await operation; await settle()
  assert.equal(deletes.length, 1)
  assert.equal(state.activeCloseConfirmation, null)
  assert.equal(state.rows.length, 0)
  assert.match(document.body.textContent, /结算完成/)
})

test('an active lease opens a second confirmation, and cancelling never submits approval', async () => {
  listFixture = [escrowRow()]
  const { state } = await open()
  const operation = state.handleDelete(state.rows[0])
  deletes[0].resolve(activeLease('9007199254740993'))
  await operation; await settle()
  assert.equal(deletes.length, 1)
  assert.match(document.body.textContent, /立即吊销令牌并通知客户端退出/)
  clickActiveClose('取消')
  await settle()
  assert.equal(state.activeCloseConfirmation, null)
  assert.equal(deletes.length, 1)
  assert.equal(state.rows[0].name, '冻结金脚本')
})

test('confirmation binds to one run and a changed lease requires a fresh user decision', async () => {
  listFixture = [escrowRow()]
  const { state } = await open()
  const operation = state.handleDelete(state.rows[0])
  deletes[0].reject({ response: activeLease('9007199254740993') })
  await operation; await settle()
  clickActiveClose('确认关闭')
  await settle()
  assert.deepEqual(deletes[1].payload, { id: 7, confirmActiveSessionId: '9007199254740993' })
  deletes[1].resolve(activeLease('9007199254740995'))
  await settle()
  assert.equal(deletes.length, 2, 'a new lease is not confirmed automatically')
  clickActiveClose('确认关闭')
  await settle()
  assert.deepEqual(deletes[2].payload, { id: 7, confirmActiveSessionId: '9007199254740995' })
  listFixture = [{ ...escrowRow(), enabled: false, escrowClosingAt: '2026-11-08T00:00:00Z' }]
  deletes[2].resolve({ status: 202, data: { state: 'settling' } })
  await settle()
  assert.equal(state.activeCloseConfirmation, null)
  assert.equal(state.mayCopyCommand(state.rows[0]), false)
  assert.match(document.body.textContent, /待结算/)
  assert.doesNotMatch(document.body.textContent, /已返还|结算完成/)
})

test('changing accounts retires a pending confirmation and cannot reuse its session ID', async () => {
  listFixture = [escrowRow()]
  const { state } = await open()
  const operation = state.handleDelete(state.rows[0])
  deletes[0].resolve(activeLease('42'))
  await operation; await settle()
  assert.ok(state.activeCloseConfirmation)
  listFixture = [{ ...escrowRow(), ownerId: normal.id }]
  user.setStoredCurrentUser(normal)
  await settle()
  await state.confirmActiveScriptClose()
  assert.equal(state.activeCloseConfirmation, null)
  assert.equal(deletes.length, 1)
})

test('script identity change clears rows and prevents late all-scope result publication', async () => {
  const { state, page } = await open()
  assert.equal(state.rows[0].name, '现有脚本')
  assert.doesNotMatch(page.text(), /EncodedCommand/, 'launcher credentials are not printed in the table')
  assert.equal(state.mayCopyCommand(state.rows[0]), true)
  assert.equal(state.mayCopyCommand(row('其他账号', 222)), false)
  holdReads = true
  state.toggleScope()
  assert.deepEqual(reads[0].params, { scope: 'all' })
  assert.deepEqual(state.rows, [])
  user.setStoredCurrentUser(normal)
  await settle()
  assert.deepEqual(reads[1].params, { scope: 'self' })
  reads[0].resolve(okay([row('迟到管理员数据', 111)]))
  await settle()
  assert.deepEqual(state.rows, [])
  assert.equal(state.loading, true, 'old request does not clear new loading')
  reads[1].resolve(okay([row('新账号脚本', 222)]))
  await settle()
  assert.equal(state.rows[0].name, '新账号脚本')
  assert.equal(state.loading, false)
})

test('script save deduplicates clicks and retains the idempotency key only for an unchanged retry', async () => {
  const { state, page } = await open()
  holdWrites = true
  state.openCreate()
  await settle()
  state.setFormField('name', '合成脚本')
  state.setFormField('baseDomain', 'example.test')
  const editor = page.findComponent(Antd.Modal)
  assert.ok(editor.exists())
  editor.vm.$emit('ok')
  editor.vm.$emit('ok')
  assert.equal(writes.length, 1)
  assert.match(writes[0].key, /^script-create-[0-9a-f]{32}$/)
  assert.equal(Object.hasOwn(writes[0].payload, 'command'), false)
  writes[0].resolve({ status: 503 })
  await settle()
  assert.equal(state.modalOpen, true)
  editor.vm.$emit('ok')
  assert.equal(writes[0].key, writes[1].key)
  writes[1].resolve({ status: 503 })
  await settle()
  state.setFormField('name', '修改合成脚本')
  editor.vm.$emit('ok')
  assert.notEqual(writes[1].key, writes[2].key)
  user.clearStoredCurrentUser()
  writes[2].resolve(okay({}))
  await settle()
  assert.equal(state.modalOpen, false)
  assert.equal(state.form.name, '')
  assert.equal(state.rows.length, 0)
})

test('leaving the script page asks before discarding a draft but never traps logout', async () => {
  const { state, router } = await open()
  state.openCreate()
  state.setFormField('name', '待保留草稿')
  const navigation = router.push('/other')
  await settle()
  const cancel = document.querySelector('.ant-modal-confirm .ant-modal-confirm-btns .ant-btn-default')
  assert.ok(cancel)
  cancel.click()
  await navigation
  assert.equal(router.currentRoute.value.path, '/adapter/scripts')
  assert.equal(state.form.name, '待保留草稿')
  await router.push('/login')
  await settle()
  assert.equal(router.currentRoute.value.path, '/login')
})

test('native editor Cancel preserves dirty input until the user explicitly discards it', async () => {
  const { state } = await open()
  state.openCreate()
  await settle()
  const input = document.querySelector('input[placeholder="例如：超星答题助手"]')
  assert.ok(input)
  input.value = '需要保留的脚本草稿'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  const click = (selector, label) => {
    const button = [...document.querySelectorAll(selector)].filter(item => item.textContent.replace(/\s+/g, '') === label).at(-1)
    assert.ok(button, label)
    button.click()
  }
  click('.ant-modal-footer button', '取消')
  await settle()
  assert.equal(state.modalOpen, true)
  assert.match(document.querySelector('.ant-modal-confirm-title').textContent, /有未保存的更改/)
  click('.ant-modal-confirm-btns button', '保留编辑')
  await settle()
  assert.equal(input.value, '需要保留的脚本草稿')
  assert.equal(state.modalOpen, true)
  click('.ant-modal-footer button', '取消')
  await settle()
  click('.ant-modal-confirm-btns button', '放弃并离开')
  await settle()
  assert.equal(state.modalOpen, false)
  assert.equal(state.form.name, '')
})


test('wallet purchase retries one key per editor, and retiring the account clears the grant and ignores a late code', async () => {
  const { state, page } = await open()
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  const pending = state.buyActivationCode()
  state.buyActivationCode()
  assert.equal(purchases.length, 1)
  assert.match(purchases[0].key, /^script-activation-purchase-[0-9a-f]{32}$/)
  purchases[0].resolve({ status: 503 })
  await pending
  const retry = state.buyActivationCode()
  assert.equal(purchases.length, 2)
  assert.equal(purchases[0].key, purchases[1].key)
  assert.equal(purchases[0].quote, '12.345678')
  assert.equal(purchases[1].quote, purchases[0].quote)
  user.setStoredCurrentUser(normal)
  purchases[1].resolve(okay({ entitlementId: 777, price: 12.345678, code: 'sac_' + 'a'.repeat(64) }))
  await retry; await settle()
  assert.equal(state.modalOpen, false)
  assert.equal(state.activationCode, '')
  assert.equal(state.form.entitlementId, undefined)
  assert.doesNotMatch(page.text(), /sac_[a-f]{64}/)
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  const next = state.buyActivationCode()
  assert.notEqual(purchases[1].key, purchases[2].key)
  purchases[2].resolve({ status: 503 })
  await next
})

test('redeeming sends code only to redemption; creation uses entitlement and edit does not mutate billing mode', async () => {
  const { state, page } = await open()
  holdWrites = true
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  state.setFormField('name', '一次合成脚本')
  state.setFormField('baseDomain', 'example.test')
  state.setFormField('runDurationMinutes', 35)
  state.setFormField('notifyOnDenied', true)
  await settle()
  const code = 'sac_' + 'c'.repeat(64)
  state.setActivationCode(code)
  await state.redeemActivationCode()
  assert.deepEqual(redemptions[0], { code })
  assert.equal(state.activationCode, '')
  assert.equal(state.form.entitlementId, 555)
  page.findComponent(Antd.Modal).vm.$emit('ok')
  assert.equal(writes.length, 1)
  assert.equal(writes[0].payload.billingMode, 'activation_code')
  assert.equal(writes[0].payload.entitlementId, 555)
  assert.equal(writes[0].payload.runDurationMinutes, 35)
  assert.equal(writes[0].payload.notifyOnDenied, true)
  for (const field of ['billingEnabled', 'priceMultiplier', 'answerPriceMultiplier', 'billingUseTikuPrice', 'useTikuPrice', 'autoTopUp', 'code']) {
    assert.equal(Object.hasOwn(writes[0].payload, field), false, field)
  }
  assert.equal(JSON.stringify(writes[0].payload).includes(code), false)
  writes[0].resolve(okay({}))
  await settle()
  state.rows[0].billingMode = 'escrow'
  state.openEdit(state.rows[0])
  state.setFormField('billingMode', 'activation_code')
  assert.equal(state.form.billingMode, 'escrow')
  state.setFormField('autoTopUp', true)
  await settle()
  page.findComponent(Antd.Modal).vm.$emit('ok')
  await settle()
  assert.equal(updates[0].autoTopUp, true)
  assert.equal(Object.hasOwn(updates[0], 'billingMode'), false)
  assert.equal(Object.hasOwn(updates[0], 'entitlementId'), false)
  assert.equal(Object.hasOwn(updates[0], 'answerPriceMultiplier'), false)
})

test('admin escrow fields are exact six decimals and reject a minimum above the target', async () => {
  const { state } = await open()
  state.setPolicyField('target', '80.000001')
  state.setPolicyField('minimum', '90')
  await state.saveEscrowPolicy()
  assert.equal(policyWrites.length, 0)
  state.setPolicyField('minimum', '60.100002')
  state.setPolicyField('multiplier', '1.100005')
  await state.saveEscrowPolicy()
  assert.deepEqual(policyWrites[0], { targetMicros: 80000001, minimumMicros: 60100002, multiplierMicros: 1100005 })
})


test('a resolved HTTP stale-quote conflict refreshes the quote and requires a new purchase intent', async () => {
  const { state } = await open()
  state.openCreate()
  state.setFormField('billingMode', 'activation_code')
  const first = state.buyActivationCode()
  assert.equal(purchases[0].quote, '12.345678')
  activationPriceFixture = 13
  purchases[0].resolve({ status: 409, data: { code: 'SCRIPT_ACTIVATION_PRICE_CHANGED' } })
  await first; await settle()
  assert.equal(state.purchaseUncertain, false)
  assert.equal(state.activationPrice.price, '13')
  const newIntent = state.buyActivationCode()
  assert.equal(purchases[1].quote, '13')
  assert.notEqual(purchases[1].key, purchases[0].key)
  purchases[1].resolve({ status: 503 })
  await newIntent
})
