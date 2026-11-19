/* Actual Tickets page, Vue/Antd and production API helpers; all HTTP uses an Axios memory adapter. */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const { webcrypto } = require('node:crypto')
require('../helpers/vue-dom.cjs')
Object.defineProperty(globalThis, 'crypto', { configurable: true, value: webcrypto })
require.extensions['.css'] = module => { module.exports = {} }
for (const name of ['HTMLInputElement', 'HTMLTextAreaElement', 'HTMLButtonElement', 'FileReader', 'File']) global[name] = window[name]
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
const imageDataUrl = `data:image/png;base64,${pngBytes.toString('base64')}`
const priorAdapter = axios.defaults.adapter
const priorCreate = URL.createObjectURL, priorRevoke = URL.revokeObjectURL
const calls = [], pages = [], unexpected = [], runtime = []
let nextAttachment = 400, phoneStatus = { required: false, phoneMasked: '' }
let holdPhone = false, phoneResolve = null
let reviewUploadWait = null
let payoutQr = null, payoutChallenge = null, nextPayoutKey = 0, verifiedDeviceUnlocks = 0

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
  if (url === '/api/ticket-refunds/summary' && method === 'get') return response(config, { balanceCents: 2599, refundableCents: 2301, nonRefundableCents: 298, nonRefundableReason: '赠送金额及已消费赠金不可退款' })
  if (url === '/api/ticket-refunds/phone-verification' && method === 'get') {
    const snapshot = { ...phoneStatus }
    if (holdPhone) return new Promise(resolve => { phoneResolve = () => resolve(response(config, snapshot)) })
    return response(config, { ...phoneStatus })
  }
  if (url === '/api/ticket-refunds/phone-verification/sms-code' && method === 'post') return response(config, {})
  if (url === '/api/ticket-refunds/payout-qr/status' && method === 'get') return response(config, { hasPrevious: Boolean(payoutQr), keyId: payoutQr?.keyId || '', previewDataUrl: payoutQr ? imageDataUrl : '' })
  if (url === '/api/ticket-refunds/payout-qr/register' && method === 'post') {
    assert.ok(calls.some(call => call.url === '/api/ticket-attachments' && call.method === 'post'), 'registration follows an accepted upload')
    const publicKey = await webcrypto.subtle.importKey('spki', Buffer.from(data.publicKey, 'base64'), { name: 'ECDSA', namedCurve: 'P-256' }, false, ['verify'])
    payoutQr = { attachmentId: data.attachmentId, keyId: `synthetic-key-${++nextPayoutKey}`, publicKey }
    return response(config, { attachmentId: payoutQr.attachmentId, keyId: payoutQr.keyId })
  }
  if (url === '/api/ticket-refunds/payout-qr/challenge' && method === 'post') {
    assert.ok(payoutQr, 'only a remembered QR can issue a challenge')
    payoutChallenge = { challengeId: `synthetic-challenge-${verifiedDeviceUnlocks}`, challenge: Buffer.from(webcrypto.getRandomValues(new Uint8Array(32))).toString('base64') }
    return response(config, payoutChallenge)
  }
  if (url === '/api/ticket-refunds/payout-qr/device-unlock' && method === 'post') {
    assert.equal(data.challengeId, payoutChallenge?.challengeId)
    const signature = Buffer.from(data.signature, 'base64')
    assert.equal(signature.length, 64, 'P-256 signature uses raw r || s')
    assert.equal(await webcrypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, payoutQr.publicKey, signature, Buffer.from(payoutChallenge.challenge, 'base64')), true, 'same-device recovery proves possession of the registered private key')
    payoutChallenge = null; verifiedDeviceUnlocks++
    return response(config, { attachmentId: payoutQr.attachmentId, imageDataUrl })
  }
  if (url === '/api/ticket-refunds/payout-qr/recover' && method === 'post') {
    assert.equal(data.password, 'SYNTHETIC_PASSWORD')
    return response(config, { attachmentId: payoutQr.attachmentId, imageDataUrl })
  }
  if (url === '/api/ticket-refunds/payout-qr' && method === 'delete') { payoutQr = null; payoutChallenge = null; return response(config, {}) }
  if (url === '/api/refund-agreement') return response(config, { text: agreement })
  if (url === '/api/ticket-attachments' && method === 'post') { if (reviewUploadWait) await reviewUploadWait; return response(config, { data: { id: ++nextAttachment, mediaType: 'image/png' } }, 201) }
  if (/^\/api\/ticket-attachments\/\d+$/.test(url) && method === 'get') return response(config, image)
  unexpected.push(`${method} ${url}`)
  throw new Error('Fixture denies unmatched HTTP')
}
URL.createObjectURL = () => 'blob:http://localhost/synthetic-refund-signature'
URL.revokeObjectURL = () => {}

const Tickets = require('../../src/pages/Tickets/index.vue')
const RefundPayoutQr = require('../../src/components/RefundPayoutQr.vue')
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
async function waitFor(predicate, label) {
  for (let attempt = 0; attempt < 100; attempt++) {
    await settle()
    if (predicate()) return
    await new Promise(resolve => setTimeout(resolve, 5))
  }
  assert.fail(label)
}
async function pickQr(name = 'synthetic-qr.png', type = 'image/png') {
  const input = form().querySelector('[data-ticket-payout-qr-input]')
  assert.ok(input && !input.disabled, 'QR file input is available')
  Object.defineProperty(input, 'files', { configurable: true, value: [new window.File([pngBytes], name, { type })] })
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await waitFor(() => form()?.querySelector('[data-ticket-payout-qr-preview]') && form().querySelector('[data-ticket-payout-qr]').getAttribute('aria-busy') === 'false', 'QR upload completes')
}
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
  const requestsBefore = calls.filter(call => call.url.endsWith('/eligible-orders')).length
  await click(form().querySelector('[data-ticket-refund-method="original"]'))
  assert.equal([...document.querySelectorAll('[data-ticket-refund-orders-confirm]')].filter(visible).length, 0, 'method selection does not open records')
  assert.equal(calls.filter(call => call.url.endsWith('/eligible-orders')).length, requestsBefore)
  await click(form().querySelector('[data-ticket-refund-orders-open]'))
  const dialog = order(1)?.closest('.ant-modal')
  assert.ok(dialog && visible(dialog), 'choose-records opens the actual order dialog')
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
  payoutQr = payoutChallenge = null; nextPayoutKey = verifiedDeviceUnlocks = 0
  reviewUploadWait = null; nextAttachment = 400; phoneStatus = { required: false, phoneMasked: '' }; holdPhone = false; phoneResolve = null
})
after(() => { axios.defaults.adapter = priorAdapter; URL.createObjectURL = priorCreate; URL.revokeObjectURL = priorRevoke })

test('method switch while QR upload is pending must not leave busy stuck', async () => {
  const wrapper = await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  let finish
  reviewUploadWait = new Promise(resolve => { finish = resolve })
  const setup = wrapper.vm.$.setupState
  const pending = wrapper.findComponent(RefundPayoutQr).vm.$.setupState.pick({ target: { files: [new window.File([pngBytes], 'synthetic-qr.png', { type: 'image/png' })], value: 'fixture' } })
  await waitFor(() => calls.some(c => c.url === '/api/ticket-attachments'), 'initial QR upload reaches the held HTTP request')
  assert.equal(setup.payoutQrBusy, true)
  await type(form().querySelector('input'), '10.00')
  await click(form().querySelector('[data-ticket-refund-method="original"]'))
  await click(form().querySelector('[data-ticket-refund-orders-open]'))
  await click(document.querySelector('[data-ticket-refund-orders-confirm]'))
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  finish(); await pending; await settle()
  assert.equal(setup.payoutQrBusy, false, 'manual page must allow another upload after returning')
  reviewUploadWait = null
  await pickQr('replacement.png')
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'a replacement can be uploaded after switching methods')
})

test('a valid PNG file with an empty browser MIME type remains reusable locally', async () => {
  await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  await pickQr('synthetic-qr.png', '')
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'upload is visible')
  await click(button(form().closest('.ant-modal'), '取消'))
  await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  await waitFor(() => form()?.querySelector('[data-ticket-payout-qr-preview]'), 'same-device PNG restores via device challenge')
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'same-device PNG must restore without password even when browser omitted its MIME')
  assert.equal(verifiedDeviceUnlocks, 1, 'restoration validates the registered asymmetric key')
  assert.equal(window.localStorage.getItem(`tiku.refund.payoutQr.${owner.id}`), null, 'no plaintext image cache remains')
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
