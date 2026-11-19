/* Actual Tickets page, Vue/Antd and production API helpers; all HTTP uses an Axios memory adapter. */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
require.extensions['.css'] = module => { module.exports = {} }
for (const name of ['HTMLInputElement', 'HTMLTextAreaElement', 'HTMLButtonElement', 'FileReader']) global[name] = window[name]
global.FormData = window.FormData
global.getComputedStyle = element => window.getComputedStyle(element)
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createMemoryHistory, createRouter } = require('vue-router')
const Antd = require('ant-design-vue')
const axios = require('axios')
const currentUser = require('../../src/utils/currentUser.js')

const owner = { id: 78111, username: 'SYNTHETIC_REFUND_OWNER', role: 'user', status: 'enabled', phone: '13800000000' }
const orders = [1, 2, 3, 4].map(id => ({
  id, orderNo: `SYNTHETIC_REFUND_${id}`, paidCents: 750,
  paidAt: `2026-0${id}-01T00:00:00Z`, originalSupported: id !== 4,
  originalUnsupportedReason: id === 4 ? '该充值渠道不支持原路退款' : '',
}))
const agreement = '退款收款码使用承诺书\n\n本人承诺收款码所属账户为本人账户。\n本协议仅用于本次人工退款申请。'
const pngBytes = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADElEQVR42mP8z8BQDwAE/wGJxJrtGQAAAABJRU5ErkJggg==', 'base64')
const image = new Blob([pngBytes], { type: 'image/png' })
const priorAdapter = axios.defaults.adapter
const priorCreate = URL.createObjectURL, priorRevoke = URL.revokeObjectURL
const calls = [], pages = [], unexpected = [], runtime = []
let nextAttachment = 400, phoneStatus = { required: false, phoneMasked: '' }
let holdPhone = false, phoneResolve = null
let reviewUploadWait = null

function response(config, data, status = 200) {
  return { status, data, config, headers: {}, request: { memoryOnly: true } }
}
axios.defaults.adapter = async config => {
  const method = String(config.method), url = String(config.url)
  const data = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ method, url, data })
  if (url === '/api/tickets' && method === 'get') return response(config, { items: [], total: 0 })
  if (url === '/api/tickets' && method === 'post') return response(config, { id: 901 }, 201)
  if (url === '/api/ticket-refunds/eligible-orders') return response(config, orders)
  if (url === '/api/ticket-refunds/phone-verification' && method === 'get') {
    const snapshot = { ...phoneStatus }
    if (holdPhone) return new Promise(resolve => { phoneResolve = () => resolve(response(config, snapshot)) })
    return response(config, { ...phoneStatus })
  }
  if (url === '/api/ticket-refunds/phone-verification/sms-code' && method === 'post') return response(config, {})
  if (url === '/api/ticket-refunds/payout-qr/preview') return response(config, { hasPrevious: false, previewDataUrl: '' })
  if (url === '/api/refund-agreement') return response(config, { text: agreement })
  if (url === '/api/ticket-attachments' && method === 'post') { if (reviewUploadWait) await reviewUploadWait; return response(config, { data: { id: ++nextAttachment, mediaType: 'image/png' } }, 201) }
  if (/^\/api\/ticket-attachments\/\d+$/.test(url) && method === 'get') return response(config, image)
  unexpected.push(`${method} ${url}`)
  throw new Error('Fixture denies unmatched HTTP')
}
URL.createObjectURL = () => 'blob:http://localhost/synthetic-refund-signature'
URL.revokeObjectURL = () => {}

const Tickets = require('../../src/pages/Tickets/index.vue')
const compact = text => String(text || '').replace(/\s+/g, '')
function visible(node) {
  if (!node?.isConnected) return false
  for (let el = node; el?.nodeType === 1; el = el.parentElement) {
    if (el.hidden || el.getAttribute('aria-hidden') === 'true' || window.getComputedStyle(el).display === 'none') return false
  }
  return true
}
function button(root, label) {
  const matches = [...root.querySelectorAll('button')].filter(node => visible(node) && compact(node.textContent) === compact(label))
  assert.equal(matches.length, 1, `one visible button: ${label}`)
  assert.equal(matches[0].disabled, false, `button enabled: ${label}`)
  return matches[0]
}
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function click(node) { assert.ok(node && !node.disabled, 'enabled native click target'); node.click(); await settle() }
async function type(node, value) {
  assert.ok(node && !node.disabled, 'enabled native input')
  node.value = value
  node.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
}
function form() { return [...document.querySelectorAll('[data-ticket-step="form"]')].find(visible) }
function order(id) { return document.querySelector(`[data-ticket-refund-order="${id}"]`) }
function allocation(id) { return document.querySelector(`[data-ticket-refund-alloc="${id}"]`) }
async function openPage() {
  currentUser.setStoredCurrentUser(owner)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/adapter/tickets', component: { render: () => null } }] })
  await router.push('/adapter/tickets'); await router.isReady()
  const wrapper = mount(Tickets, {
    attachTo: document.body,
    global: { plugins: [Antd, router], config: { errorHandler: error => runtime.push(error) } },
  })
  pages.push({ wrapper, router }); await settle()
  return wrapper
}
async function openRefund() {
  await click(button(document.body, '发起工单'))
  await click(document.querySelector('[data-ticket-type-option="refund"]'))
  await click(button(document.body, '下一步'))
  assert.ok(form(), 'actual refund form is visible')
}
async function chooseOriginal() {
  await type(form().querySelector('input'), '10.00')
  await click(form().querySelector('[data-ticket-refund-method="original"]'))
  const dialog = order(1)?.closest('.ant-modal')
  assert.ok(dialog && visible(dialog), 'original method opens the actual order dialog')
  assert.notEqual(dialog, form().closest('.ant-modal'), 'orders have their own dialog')
  return dialog
}

afterEach(async () => {
  for (const { wrapper, router } of pages.splice(0)) { wrapper.unmount(); router.options.history.destroy() }
  if (phoneResolve) phoneResolve()
  await settle()
  Antd.message.destroy(); Antd.Modal.destroyAll()
  currentUser.clearStoredCurrentUser(); window.localStorage.clear()
  document.body.replaceChildren(); await settle()
  assert.deepEqual(unexpected, [], 'all HTTP stayed in the fixture')
  assert.deepEqual(runtime, [], 'no Vue runtime errors')
  calls.length = unexpected.length = runtime.length = 0
  reviewUploadWait = null; nextAttachment = 400; phoneStatus = { required: false, phoneMasked: '' }; holdPhone = false; phoneResolve = null
})
after(() => { axios.defaults.adapter = priorAdapter; URL.createObjectURL = priorCreate; URL.revokeObjectURL = priorRevoke })

test('method switch while QR upload is pending must not leave busy stuck', async () => {
  const wrapper = await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  let finish
  reviewUploadWait = new Promise(resolve => { finish = resolve })
  const setup = wrapper.vm.$.setupState
  const pending = setup.onPickPayoutQr({ target: { files: [new window.File([pngBytes], 'synthetic-qr.png', { type: 'image/png' })], value: 'fixture' } })
  for (let attempt = 0; attempt < 20 && !calls.some(c => c.url === '/api/ticket-attachments'); attempt++) await settle()
  assert.equal(setup.payoutQrBusy, true)
  await click(form().querySelector('[data-ticket-refund-method="original"]'))
  await click(document.querySelector('[data-ticket-refund-orders-confirm]'))
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  finish(); await pending; await settle()
  assert.equal(setup.payoutQrBusy, false, 'manual page must allow another upload after returning')
  reviewUploadWait = null
  await setup.onPickPayoutQr({ target: { files: [new window.File([pngBytes], 'replacement.png', { type: 'image/png' })], value: 'fixture' } })
  await settle()
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'a replacement can be uploaded after switching methods')
})

test('a valid PNG file with an empty browser MIME type remains reusable locally', async () => {
  const wrapper = await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  const setup = wrapper.vm.$.setupState
  await setup.onPickPayoutQr({ target: { files: [new window.File([pngBytes], 'synthetic-qr.png', { type: '' })], value: 'fixture' } })
  await settle()
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'upload is visible')
  await click(button(form().closest('.ant-modal'), '取消'))
  await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'same-device PNG must restore without password even when browser omitted its MIME')
})

test('editing a partial refund amount preserves the current input text until blur', async () => {
  await openPage(); await openRefund(); await chooseOriginal()
  await click(order(1).querySelector('input'))
  await type(allocation(1), '2')
  assert.equal(allocation(1).value, '2', 'do not inject decimals and move the caret while the user is typing')
  await type(allocation(1), '2.35')
  assert.equal(allocation(1).value, '2.35')
  const setup = pages[pages.length - 1].wrapper.vm.$.setupState
  assert.equal(setup.draft.refundAllocations[0].cents, 750, 'input alone must not commit a partially edited amount')
  allocation(1).dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
  assert.equal(setup.draft.refundAllocations[0].cents, 235, 'change commits the completed decimal value')
  assert.equal(setup.draft.refundAllocations[0].manual, true)
})
