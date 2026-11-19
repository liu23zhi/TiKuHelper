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
let smsStatus = 200
let holdPhone = false, phoneResolve = null

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
  if (url === '/api/ticket-refunds/phone-verification/sms-code' && method === 'post') return response(config, {}, smsStatus)
  if (url === '/api/ticket-refunds/payout-qr/preview') return response(config, { hasPrevious: false, previewDataUrl: '' })
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
  nextAttachment = 400; phoneStatus = { required: false, phoneMasked: '' }; smsStatus = 200; holdPhone = false; phoneResolve = null
})
after(() => { axios.defaults.adapter = priorAdapter; URL.createObjectURL = priorCreate; URL.revokeObjectURL = priorRevoke })

test('退款金额与方式顺序、7.5+2.5分配、手改腾出第三笔，以及提交的真实payload', async () => {
  await openPage(); await openRefund()
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
  const fileInput = form().querySelector('[data-ticket-payout-qr-input]')
  Object.defineProperty(fileInput, 'files', { configurable: true, value: [new window.File([pngBytes], 'synthetic-qr.png', { type: 'image/png' })] })
  fileInput.dispatchEvent(new Event('change', { bubbles: true }))
  for (let attempt = 0; attempt < 12 && !form().querySelector('[data-ticket-payout-qr-preview]'); attempt++) await settle()
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'local upload is immediately displayed')
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
  assert.equal(form().querySelector('[data-ticket-refund-agreement-check]').checked, false, 'a new draft must sign again')
  assert.equal(form().querySelector('[data-ticket-refund-agreement-review]'), null)
  assert.ok(form().querySelector('[data-ticket-payout-qr-preview]'), 'the same device remembers its uploaded QR image')
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
  assert.equal(payload.refundOrderAllocations, undefined)
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
