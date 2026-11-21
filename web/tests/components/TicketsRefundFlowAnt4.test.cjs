/* Actual Tickets page, Vue/Antd and production API helpers; all HTTP uses an Axios memory adapter. */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const { webcrypto } = require('node:crypto')
require('../helpers/vue-dom.cjs')
Object.defineProperty(globalThis, 'crypto', { configurable: true, value: webcrypto })
require.extensions['.css'] = module => { module.exports = {} }
for (const name of ['DOMParser', 'HTMLVideoElement', 'HTMLAudioElement', 'HTMLInputElement', 'HTMLTextAreaElement', 'HTMLButtonElement', 'FileReader', 'File']) global[name] = window[name]
window.scrollTo = () => {}
global.FormData = window.FormData
global.getComputedStyle = element => window.getComputedStyle(element)
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createMemoryHistory, createRouter } = require('vue-router')
const Antd = require('ant-design-vue')
const Swal = require('sweetalert2')
const axios = require('axios')
const currentUser = require('../../src/utils/currentUser.js')

const owner = { id: 78111, username: 'SYNTHETIC_REFUND_OWNER', role: 'user', status: 'enabled', phone: '13800000000' }
const orders = [1, 2, 3, 4].map(id => ({
  id, orderNo: `SYNTHETIC_REFUND_${id}`, paidCents: 750,
  paidAt: `2026-0${id}-01T00:00:00Z`, originalSupported: id !== 4,
  originalUnsupportedReason: id === 4 ? '该充值渠道不支持原路退款' : '',
}))
orders.push(...[
  [5, '创建时间超过60天', 'paid'],
  [6, '订单已退款', 'refunded'],
  [7, '缺少第三方单号', 'paid'],
].map(([id, originalUnsupportedReason, status]) => ({
  id, orderNo: `SYNTHETIC_REFUND_${id}`, paidCents: 750,
  paidAt: '2026-05-01T00:00:00Z', createdAt: '2026-05-01T00:00:00Z', status,
  originalSupported: false, originalUnsupportedReason,
})))
const agreement = '退款收款码使用承诺书\n\n本人承诺收款码所属账户为本人账户。\n本协议仅用于本次人工退款申请。'
const pngBytes = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADElEQVR42mP8z8BQDwAE/wGJxJrtGQAAAABJRU5ErkJggg==', 'base64')
const image = new Blob([pngBytes], { type: 'image/png' })
const imageDataUrl = `data:image/png;base64,${pngBytes.toString('base64')}`
const priorAdapter = axios.defaults.adapter
const priorCreate = URL.createObjectURL, priorRevoke = URL.revokeObjectURL
const calls = [], pages = [], unexpected = [], runtime = []
let nextAttachment = 400, phoneStatus = { required: false, phoneMasked: '' }
let smsStatus = 200
let summaryStatus = 200
const summary = { balanceCents: 2599, refundableCents: 2301, nonRefundableCents: 298, nonRefundableReason: '赠送金额及已消费赠金不可退款' }
let holdPhone = false, phoneResolve = null
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
  if (url === '/api/ticket-refunds/summary' && method === 'get') return response(config, summaryStatus === 200 ? summary : { message: 'SYNTHETIC_SUMMARY_UNAVAILABLE' }, summaryStatus)
  if (url === '/api/ticket-refunds/phone-verification' && method === 'get') {
    const snapshot = { ...phoneStatus }
    if (holdPhone) return new Promise(resolve => { phoneResolve = () => resolve(response(config, snapshot)) })
    return response(config, { ...phoneStatus })
  }
  if (url === '/api/ticket-refunds/phone-verification/sms-code' && method === 'post') return response(config, {}, smsStatus)
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
  if (url === '/api/ticket-attachments' && method === 'post') return response(config, { data: { id: ++nextAttachment } }, 201)
  if (/^\/api\/ticket-attachments\/\d+$/.test(url) && method === 'get') return response(config, image)
  unexpected.push(`${method} ${url}`)
  throw new Error('Fixture denies unmatched HTTP')
}
URL.createObjectURL = () => 'blob:http://localhost/synthetic-refund-signature'
URL.revokeObjectURL = () => {}

const Tickets = require('../../src/pages/Tickets/index.vue')
const AgreementDialog = require('../../src/components/RefundAgreementDialog.vue')
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
  node.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
}
function form() { return [...document.querySelectorAll('[data-ticket-step="form"]')].find(visible) }
function order(id) { return document.querySelector(`[data-ticket-refund-order="${id}"]`) }
function allocation(id) { return document.querySelector(`[data-ticket-refund-alloc="${id}"]`) }
function writes() { return calls.filter(call => call.method === 'post' && call.url === '/api/tickets') }
async function openPage() {
  currentUser.setStoredCurrentUser(owner)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/tickets', component: { render: () => null } }] })
  await router.push('/tickets'); await router.isReady()
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
  assert.equal([...document.querySelectorAll('[data-ticket-refund-orders-confirm]')].filter(visible).length, 0, 'choosing a method keeps the order dialog closed')
  assert.equal(calls.filter(call => call.url.endsWith('/eligible-orders')).length, requestsBefore, 'choosing a method does not query records')
  await click(form().querySelector('[data-ticket-refund-orders-open]'))
  const dialog = order(1)?.closest('.ant-modal')
  assert.ok(dialog && visible(dialog), 'the choose-records button opens the actual order dialog')
  assert.notEqual(dialog, form().closest('.ant-modal'), 'orders have their own dialog')
  return dialog
}

afterEach(async () => {
  for (const { wrapper, router } of pages.splice(0)) { wrapper.unmount(); router.options.history.destroy() }
  if (phoneResolve) phoneResolve()
  await settle()
  Swal.close()
  Antd.message.destroy(); Antd.Modal.destroyAll()
  currentUser.clearStoredCurrentUser(); window.localStorage.clear()
  document.body.replaceChildren(); await settle()
  assert.deepEqual(unexpected, [], 'all HTTP stayed in the fixture')
  assert.deepEqual(runtime, [], 'no Vue runtime errors')
  calls.length = unexpected.length = runtime.length = 0
  nextAttachment = 400; phoneStatus = { required: false, phoneMasked: '' }; smsStatus = summaryStatus = 200; holdPhone = false; phoneResolve = null
  payoutQr = payoutChallenge = null; nextPayoutKey = verifiedDeviceUnlocks = 0
})
after(() => { axios.defaults.adapter = priorAdapter; URL.createObjectURL = priorCreate; URL.revokeObjectURL = priorRevoke })

test('退款金额与方式顺序、7.5+2.5分配、手改腾出第三笔，以及提交的真实payload', async () => {
  await openPage(); await openRefund()
  assert.match(compact(form().querySelector('[data-ticket-refund-available]').textContent), /账户余额25\.99元/)
  assert.match(compact(form().querySelector('[data-ticket-refund-available]').textContent), /可退款23\.01元/)
  assert.match(compact(form().querySelector('[data-ticket-refund-available]').textContent), /不可退款2\.98元/)
  assert.match(form().querySelector('[data-ticket-refund-available]').textContent, /赠送金额及已消费赠金不可退款/)
  assert.equal(form().querySelector('input').value, '23.01', 'the maximum refundable amount is prefilled')
  const labels = [...form().querySelectorAll('.ant-form-item-label')].map(node => compact(node.textContent).replace(/^\*/, ''))
  assert.equal(labels[0], '退款金额（元）')
  assert.equal(labels[1], '退回方式')
  const dialog = await chooseOriginal()
  assert.equal(order(4).querySelector('input').disabled, true, 'unsupported order remains visible and disabled')
  assert.match(order(4).textContent, /该充值渠道不支持原路退款/)
  await click(order(1).querySelector('input'))
  assert.equal(allocation(1).value, '7.50')
  assert.match(order(1).textContent, /全额/)
  await click(order(2).querySelector('input'))
  assert.equal(allocation(1).value, '7.50')
  assert.equal(allocation(2).value, '2.50')
  assert.equal(order(3).querySelector('input').disabled, true, 'covered amount prevents a third order')
  await type(allocation(1), '5.00')
  assert.equal(allocation(2).value, '2.50', 'manual edit leaves the other current amount intact')
  assert.equal(order(3).querySelector('input').disabled, false)
  await click(order(3).querySelector('input'))
  assert.deepEqual([1, 2, 3].map(id => allocation(id).value), ['5.00', '2.50', '2.50'])
  await click(button(dialog, '完成选择'))
  await type(form().querySelector('textarea'), 'SYNTHETIC_REFUND_REASON')
  await click(button(form().closest('.ant-modal'), '提交'))
  assert.equal(writes().length, 1)
  assert.deepEqual(writes()[0].data.refundOrderAllocations, [
    { orderId: 1, cents: 500 }, { orderId: 2, cents: 250 }, { orderId: 3, cents: 250 },
  ])
  assert.equal(writes()[0].data.refundRequestedCents, 1000)
});

test('空金额显示真实提示弹窗，填写金额后明确打开全部充值记录并显示灰行原因', async () => {
  await openPage(); await openRefund()
  await type(form().querySelector('input'), '')
  await click(form().querySelector('[data-ticket-refund-method="original"]'))
  assert.equal(calls.filter(call => call.url.endsWith('/eligible-orders')).length, 0, 'method selection makes no records request')
  assert.equal([...document.querySelectorAll('[data-ticket-refund-orders-confirm]')].filter(visible).length, 0)
  await click(form().querySelector('[data-ticket-refund-orders-open]'))
  const popup = Swal.getPopup()
  assert.ok(popup && visible(popup), 'empty amount creates a real SweetAlert dialog')
  assert.equal(popup.querySelector('.swal2-title').textContent, '请先填写退款金额')
  assert.match(popup.textContent, /填写需要申请的退款金额后，再选择充值记录/)
  assert.equal(calls.filter(call => call.url.endsWith('/eligible-orders')).length, 0, 'empty amount never requests records')
  await click(popup.querySelector('.swal2-confirm'))
  await type(form().querySelector('input'), '10.00')
  await click(form().querySelector('[data-ticket-refund-orders-open]'))
  assert.equal(document.querySelectorAll('[data-ticket-refund-order]').length, orders.length, 'all records from the server remain in the dialog')
  for (const record of orders.filter(item => !item.originalSupported)) {
    const row = order(record.id)
    assert.ok(visible(row), `unavailable order ${record.id} is visible`)
    assert.equal(row.querySelector('input').disabled, true)
    assert.equal(row.querySelector(`[data-ticket-refund-order-reason="${record.id}"]`).textContent, record.originalUnsupportedReason)
  }
  assert.equal(calls.filter(call => call.url.endsWith('/eligible-orders')).length, 1)
});

test('可退款金额查询失败有明确错误，重试后显示后端金额', async () => {
  summaryStatus = 400
  await openPage(); await openRefund()
  const available = form().querySelector('[data-ticket-refund-available]')
  assert.equal(form().querySelector('input').value, '', 'failed summary does not guess a maximum')
  assert.match(available.textContent, /SYNTHETIC_SUMMARY_UNAVAILABLE/)
  assert.equal(available.querySelector('strong'), null, 'a failed calculation never implies a zero or cached balance')
  await type(form().querySelector('input'), '18.00')
  summaryStatus = 200
  await click(button(available, '重试'))
  assert.match(compact(available.textContent), /账户余额25\.99元/)
  assert.equal(form().querySelector('input').value, '18.00', 'a retry must preserve manually entered amount')
  assert.equal(calls.filter(call => call.url.endsWith('/summary')).length, 2)
});

test('先选择较新的订单再选择旧订单，仍按充值时间分配', async () => {
  await openPage(); await openRefund(); await chooseOriginal()
  await click(order(2).querySelector('input'))
  assert.equal(allocation(2).value, '7.50')
  await click(order(1).querySelector('input'))
  assert.equal(allocation(1).value, '7.50')
  assert.equal(allocation(2).value, '2.50')
});

test('人工退款读取短信可用性、上传本机收款码、必须签名且下次申请重新签署', async () => {
  const wrapper = await openPage(); await openRefund()
  await type(form().querySelector('input'), '10.00')
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  assert.ok(calls.some(call => call.url.endsWith('/phone-verification') && call.method === 'get'))
  assert.match(form().querySelector('[data-ticket-refund-phone]').textContent, /可继续申请人工退款/)
  assert.equal(form().querySelector('input[autocomplete="one-time-code"]'), null)
  assert.ok(form().querySelector('[data-ticket-payout-qr-add]'), 'empty server state displays the add-image tile')
  const qrFileInput = form().querySelector('[data-ticket-payout-qr-input]')
  assert.equal(qrFileInput.type, 'file')
  assert.equal(qrFileInput.hidden, true, 'the raw native file picker button is hidden')
  assert.equal(qrFileInput.getAttribute('aria-hidden'), 'true')
  assert.equal(qrFileInput.getAttribute('tabindex'), '-1')
  assert.equal(window.getComputedStyle(qrFileInput).display, 'none')
  assert.equal(visible(qrFileInput), false, 'the upload tile is the only visible file chooser')
  await pickQr()
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'local upload is immediately displayed')
  const remembered = JSON.parse(window.localStorage.getItem(`tiku.refund.payoutQr.device.${owner.id}`))
  assert.equal(remembered.keyId, payoutQr.keyId)
  assert.equal(remembered.privateKey.kty, 'EC')
  assert.equal(window.localStorage.getItem(`tiku.refund.payoutQr.${owner.id}`), null, 'the previous plaintext image cache is removed')
  assert.ok(!JSON.stringify(window.localStorage).includes(pngBytes.toString('base64')), 'local storage remembers the device key without the QR image')
  await type(form().querySelector('textarea'), 'SYNTHETIC_MANUAL_REFUND_REASON')
  await click(button(form().closest('.ant-modal'), '提交'))
  assert.equal(writes().length, 0, 'a checkbox without signing cannot submit a refund')
  await click(form().querySelector('[data-ticket-refund-agreement-check]'))
  assert.ok([...document.querySelectorAll('[data-refund-agreement]')].some(visible))
  assert.equal(form().querySelector('[data-ticket-refund-agreement-check]').checked, false, 'reading alone does not count as signed')
  const child = wrapper.findComponent(AgreementDialog)
  child.vm.$emit('signed', 990)
  await settle()
  assert.equal(form().querySelector('[data-ticket-refund-agreement-check]').checked, true)
  await click(form().querySelector('[data-ticket-refund-agreement-review]'))
  assert.ok([...document.querySelectorAll('[data-refund-agreement-signed]')].some(visible), 'reopening this draft shows its signature')
  await click(document.querySelector('.refund-agreement-dialog .ant-modal-close'))
  await click(button(form().closest('.ant-modal'), '取消'))
  await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  await waitFor(() => form()?.querySelector('[data-ticket-payout-qr-preview]'), 'same-device challenge unlock completes')
  assert.equal(form().querySelector('[data-ticket-refund-agreement-check]').checked, false, 'a new draft must sign again')
  assert.equal(form().querySelector('[data-ticket-refund-agreement-review]'), null)
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'the same device remembers its uploaded QR image')
  assert.equal(verifiedDeviceUnlocks, 1, 'automatic restoration uses a valid cryptographic signature')
  assert.equal(calls.filter(call => call.url.endsWith('/payout-qr/recover')).length, 0, 'local QR reuse does not ask for a password')
  await type(form().querySelector('input'), '10.00')
  await type(form().querySelector('textarea'), 'SYNTHETIC_SECOND_MANUAL_REASON')
  await click(form().querySelector('[data-ticket-refund-agreement-check]'))
  wrapper.findComponent(AgreementDialog).vm.$emit('signed', 991)
  await settle()
  await click(button(form().closest('.ant-modal'), '提交'))
  assert.equal(writes().length, 1)
  const payload = writes()[0].data
  assert.equal(payload.refundMethod, 'manual')
  assert.equal(payload.refundSignatureAttachmentId, 991)
  assert.equal(payload.refundAgreementText, agreement)
  assert.equal(payload.refundPayoutQrAttachmentId, 402, 'a remembered image gets a fresh pending attachment for this application')
  assert.equal(payoutQr.attachmentId, 401, 'the remembered original remains separate from application evidence')
  assert.equal(payload.refundOrderAllocations, undefined)
});

test('更换设备显示锁，密码解锁只恢复当前申请，删除后回到上传方框', async () => {
  await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  await pickQr()
  const rememberedOriginal = payoutQr.attachmentId
  await click(button(form().closest('.ant-modal'), '取消'))
  window.localStorage.removeItem(`tiku.refund.payoutQr.device.${owner.id}`)
  await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  assert.ok(form().querySelector('[data-ticket-payout-qr-lock]'), 'another device sees an image lock')
  assert.ok(form().querySelector('[data-ticket-payout-qr-blurred]'), 'the server blur is a placeholder until password recovery')
  assert.equal(form().querySelector('[data-ticket-payout-qr-preview]'), null, 'an unverified device has no clear image')
  assert.equal(calls.filter(call => call.url.endsWith('/payout-qr/challenge')).length, 0, 'no local key cannot claim the uploading device')
  await click(form().querySelector('[data-ticket-payout-qr-tile]'))
  const password = [...document.querySelectorAll('input[autocomplete="current-password"]')].find(visible)
  assert.ok(password, 'clicking a locked image opens password recovery')
  await type(password, 'SYNTHETIC_PASSWORD')
  await click(password.closest('.ant-modal').querySelector('.ant-modal-footer .ant-btn-primary'))
  await waitFor(() => form()?.querySelector('[data-ticket-payout-qr-preview]'), 'password recovery displays the QR image')
  assert.equal(payoutQr.attachmentId, rememberedOriginal, 'recovery preserves the remembered original')
  assert.equal(window.localStorage.getItem(`tiku.refund.payoutQr.device.${owner.id}`), null, 'password recovery does not register this device as the uploader')
  await click(button(form().closest('.ant-modal'), '取消'))
  await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  assert.ok(form().querySelector('[data-ticket-payout-qr-lock]'), 'the next application on this device requires recovery again')
  assert.ok(form().querySelector('[data-ticket-payout-qr-delete]'), 'the existing image exposes a delete control')
  await click(form().querySelector('[data-ticket-payout-qr-delete]'))
  assert.equal(payoutQr, null, 'delete reaches the server')
  assert.ok(form().querySelector('[data-ticket-payout-qr-add]'), 'deleting returns to the add-image tile')
  assert.equal(form().querySelector('[data-ticket-payout-qr-lock]'), null)
});

test('锁定收款码可以直接替换；已解锁图片单击打开替换文件选择', async () => {
  await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  await pickQr()
  const oldKeyId = payoutQr.keyId
  await click(button(form().closest('.ant-modal'), '取消'))
  window.localStorage.removeItem(`tiku.refund.payoutQr.device.${owner.id}`)
  await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  assert.ok(form().querySelector('[data-ticket-payout-qr-lock]'))
  const input = form().querySelector('[data-ticket-payout-qr-input]')
  let chooserOpened = 0
  input.addEventListener('click', event => { event.preventDefault(); chooserOpened++ })
  await click(form().querySelector('[data-ticket-payout-qr-replace]'))
  assert.equal(chooserOpened, 1, 'direct replacement opens the chooser while locked')
  await pickQr('replacement.png')
  assert.notEqual(payoutQr.keyId, oldKeyId, 'a replacement registers a new key with its image')
  assert.equal(calls.filter(call => call.url.endsWith('/payout-qr/recover')).length, 0, 'replacement requires no password recovery')
  await click(form().querySelector('[data-ticket-payout-qr-tile]'))
  assert.equal(chooserOpened, 2, 'clicking the unlocked image replaces it')
  assert.equal([...document.querySelectorAll('input[autocomplete="current-password"]')].filter(visible).length, 0)
});

test('短信可用时显示绑定手机号并发送验证码，发送后限制重复点击', async () => {
  phoneStatus = { required: true, phoneMasked: '138****0000' }
  await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  assert.match(form().querySelector('[data-ticket-refund-phone]').textContent, /138\*{4}0000/)
  const smsInput = form().querySelector('input[autocomplete="one-time-code"]')
  assert.ok(smsInput)
  await click(button(form(), '发送验证码'))
  assert.equal(calls.filter(call => call.url.endsWith('/phone-verification/sms-code') && call.method === 'post').length, 1)
  const resend = [...form().querySelectorAll('button')].find(node => /秒后重发/.test(node.textContent))
  assert.ok(resend?.disabled, 'cooldown prevents duplicate sends')
  await type(smsInput, '123456')
  assert.equal(smsInput.value, '123456')
});

test('关闭旧申请后，其迟到的手机号状态不能覆盖新申请', async () => {
  holdPhone = true; phoneStatus = { required: true, phoneMasked: '138****0000' }
  await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  const releaseOld = phoneResolve
  assert.equal(typeof releaseOld, 'function')
  await click(button(form().closest('.ant-modal'), '取消'))
  holdPhone = false; phoneStatus = { required: false, phoneMasked: '' }
  await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  releaseOld(); await settle()
  assert.equal(form().querySelector('input[autocomplete="one-time-code"]'), null)
  assert.match(form().querySelector('[data-ticket-refund-phone]').textContent, /可继续申请人工退款/)
});

test('短信发送HTTP失败时不启动成功冷却', async () => {
  phoneStatus = { required: true, phoneMasked: '138****0000' }; smsStatus = 429
  await openPage(); await openRefund()
  await click(form().querySelector('[data-ticket-refund-method="manual"]'))
  await click(button(form(), '发送验证码'))
  assert.equal(calls.filter(call => call.url.endsWith('/phone-verification/sms-code') && call.method === 'post').length, 1)
  assert.equal(button(form(), '发送验证码').disabled, false, 'a rejected send does not claim the code was sent')
  assert.ok(!/秒后重发/.test(form().querySelector('[data-ticket-refund-phone]').textContent))
});
