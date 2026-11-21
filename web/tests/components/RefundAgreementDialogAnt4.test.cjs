const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const axios = require('axios')

const previousAdapter = axios.defaults.adapter
const oldCreate = URL.createObjectURL, oldRevoke = URL.revokeObjectURL
const canvasPrototype = window.HTMLCanvasElement.prototype
const oldContext = canvasPrototype.getContext, oldBlob = canvasPrototype.toBlob, oldRect = canvasPrototype.getBoundingClientRect
const calls = [], wrappers = [], created = [], revoked = [], strokes = []
const image = new Blob(['synthetic-signature'], { type: 'image/png' })
let uploadWait = null, previewWait = null, saveWait = null
let agreementText = '默认承诺书全文', custom = false
const context = { scale() {}, clearRect() {}, beginPath() {}, moveTo() {}, lineTo() {}, stroke() { strokes.push('stroke') } }
canvasPrototype.getContext = () => context
canvasPrototype.toBlob = callback => callback(image)
canvasPrototype.getBoundingClientRect = () => ({ left: 0, top: 0, width: 520, height: 200 })
URL.createObjectURL = blob => { created.push(blob); return `blob:http://localhost/signature-${created.length}` }
URL.revokeObjectURL = url => revoked.push(url)

function response(config, data, status = 200) { return { status, data, config, headers: {}, request: {} } }
axios.defaults.adapter = async config => {
  const data = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ method: config.method, url: config.url, data })
  if (config.url === '/api/ticket-attachments' && config.method === 'post') {
    if (uploadWait) await uploadWait
    return response(config, { data: { id: 51 } }, 201)
  }
  if (config.url === '/api/ticket-attachments/51' && config.method === 'get') {
    if (previewWait) await previewWait
    return response(config, image)
  }
  if (config.url === '/api/refund-agreement' && config.method === 'get') {
    return response(config, { text: agreementText, defaultText: '默认承诺书全文', isDefault: !custom })
  }
  if (config.url === '/api/refund-agreement' && config.method === 'put') {
    if (saveWait) await saveWait
    agreementText = data.text.trim()
    custom = agreementText !== '默认承诺书全文'
    return response(config, { text: agreementText, isDefault: !custom })
  }
  throw new Error(`Unexpected request ${config.method} ${config.url}`)
}
const Dialog = require('../../src/components/RefundAgreementDialog.vue')
const Settings = require('../../src/pages/SystemSettings/RefundAgreementPanel.vue')

async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
function element(selector) { const result = document.querySelector(selector); assert.ok(result, selector); return result }
async function click(selector) { element(selector).click(); await settle() }
const state = wrapper => wrapper.vm.$.setupState

async function openDialog(t, props = {}) {
  t.mock.timers.enable({ apis: ['Date', 'setInterval'], now: 1000 })
  const wrapper = mount(Dialog, { attachTo: document.body, props: { visible: true, payoutQrReady: true, text: '承诺书正文\n'.repeat(200), ...props }, global: { plugins: [Antd] } })
  wrappers.push(wrapper)
  await settle()
  const scroll = element('[data-refund-agreement-text]')
  Object.defineProperties(scroll, { clientHeight: { configurable: true, value: 200 }, scrollHeight: { configurable: true, value: 1200 } })
  return wrapper
}

async function scrollBottom() {
  const scroll = element('[data-refund-agreement-text]')
  scroll.scrollTop = 1000
  scroll.dispatchEvent(new Event('scroll'))
  await settle()
}

async function signStroke() {
  const canvas = element('[data-refund-signature-canvas]')
  for (const [type, x, y] of [['pointerdown', 10, 10], ['pointermove', 80, 40], ['pointerup', 80, 40]]) {
    const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y })
    Object.defineProperty(event, 'pointerId', { value: 1 })
    canvas.dispatchEvent(event)
  }
  await settle()
}

afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  Antd.message.destroy()
  await settle()
  document.body.replaceChildren()
  calls.length = created.length = revoked.length = strokes.length = 0
  uploadWait = previewWait = saveWait = null
  agreementText = '默认承诺书全文'; custom = false
})
after(() => {
  axios.defaults.adapter = previousAdapter
  URL.createObjectURL = oldCreate; URL.revokeObjectURL = oldRevoke
  canvasPrototype.getContext = oldContext; canvasPrototype.toBlob = oldBlob; canvasPrototype.getBoundingClientRect = oldRect
})

test('signing requires both ten seconds and the bottom of the agreement', async t => {
  const wrapper = await openDialog(t)
  assert.equal(element('[data-refund-agreement-sign]').disabled, true)
  t.mock.timers.tick(10000)
  await settle()
  assert.equal(element('[data-refund-agreement-sign]').disabled, true, 'elapsed time alone must not unlock signing')
  await scrollBottom()
  assert.equal(element('[data-refund-agreement-sign]').disabled, false)
  await click('[data-refund-agreement-sign]')
  assert.ok(element('[data-refund-signature]'))
  assert.equal(element('[data-refund-signature-confirm]').disabled, true, 'blank signatures cannot be submitted')
  assert.equal(wrapper.emitted('signed'), undefined)
})

test('an unsigned agreement hides legacy signature placeholders and cannot be signed without a payout image', async t => {
  const wrapper = await openDialog(t, { payoutQrReady: false, text: '本人承诺条款。\n\n申请人（以电子签名方式确认）：见签名图像\n签署日期：提交本次退款申请之日' })
  assert.doesNotMatch(element('[data-refund-agreement-text]').textContent, /申请人（以电子签名方式确认）|签署日期|见签名图像/)
  await scrollBottom(); t.mock.timers.tick(10000); await settle()
  assert.equal(element('[data-refund-agreement-sign]').disabled, true)
  assert.match(element('[data-refund-agreement-progress]').textContent, /上传或解锁收款码/)
  await state(wrapper).openSignaturePad(); await settle()
  assert.equal(document.querySelector('[data-refund-signature]'), null)
  await wrapper.setProps({ payoutQrReady: true }); await settle()
  assert.equal(element('[data-refund-agreement-sign]').disabled, false)
})

test('canceling the signature leaves the agreement open and ready to sign again', async t => {
  const wrapper = await openDialog(t)
  await scrollBottom()
  t.mock.timers.tick(9750)
  await settle()
  assert.equal(element('[data-refund-agreement-sign]').disabled, true)
  t.mock.timers.tick(250)
  await settle()
  await click('[data-refund-agreement-sign]')
  await signStroke()
  assert.equal(strokes.length, 1)
  await click('[data-refund-signature-cancel]')
  assert.equal(document.querySelector('[data-refund-signature]'), null)
  assert.ok(element('[data-refund-agreement]'))
  assert.equal(wrapper.emitted('close'), undefined)
  await click('[data-refund-agreement-sign]')
  assert.equal(element('[data-refund-signature-confirm]').disabled, true, 'canceled handwriting must be cleared')
  await click('.refund-signature-dialog .ant-modal-close')
  assert.equal(document.querySelector('[data-refund-signature]'), null)
  assert.equal(wrapper.emitted('close'), undefined, 'the signature close icon must not close the agreement')
})

test('time spent with the document hidden does not count toward reading', async t => {
  await openDialog(t)
  await scrollBottom()
  const hidden = Object.getOwnPropertyDescriptor(document, 'hidden')
  try {
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    t.mock.timers.tick(10000); await settle()
    assert.equal(element('[data-refund-agreement-sign]').disabled, true)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    t.mock.timers.tick(10000); await settle()
    assert.equal(element('[data-refund-agreement-sign]').disabled, false)
  } finally {
    if (hidden) Object.defineProperty(document, 'hidden', hidden)
    else delete document.hidden
  }
})

test('confirmation uploads only the signature, closes both dialogs, and shows it when reopened', async t => {
  const wrapper = await openDialog(t)
  await scrollBottom(); t.mock.timers.tick(10000); await settle()
  await click('[data-refund-agreement-sign]')
  await signStroke()
  assert.equal(element('[data-refund-signature-confirm]').disabled, false)
  await click('[data-refund-signature-confirm]')
  assert.equal(wrapper.emitted('signed')[0][0], 51)
  assert.equal(wrapper.emitted('signed')[0][1], '1970-01-01T00:00:11.000Z')
  assert.equal(wrapper.emitted('close').length, 1)
  assert.equal(document.querySelector('[data-refund-signature]'), null)
  const upload = calls.find(call => call.method === 'post')
  assert.equal(upload.data.get('file').name, 'refund-signature.png')
  assert.deepEqual(created, [image])
  await wrapper.setProps({ visible: false, signedAttachmentId: 51 }); await settle()
  await wrapper.setProps({ visible: true }); await settle()
  const preview = element('[data-refund-agreement-signed] img')
  assert.equal(preview.getAttribute('src'), 'blob:http://localhost/signature-1')
  assert.match(element('[data-refund-agreement-signed]').textContent, /申请人（以电子签名方式确认）：/)
  assert.match(element('[data-refund-agreement-signed]').textContent, /签署日期：.*1970/)
  assert.equal(document.querySelector('[data-refund-agreement-sign]'), null)
  assert.equal(document.querySelector('.refund-signature__canvas--grid'), null)
  assert.equal(calls.filter(call => call.method === 'get').length, 0, 'reopening the same draft uses its local signature')
  await wrapper.setProps({ visible: false, signedAttachmentId: 0 }); await settle()
  await wrapper.setProps({ visible: true }); await settle()
  assert.equal(document.querySelector('[data-refund-agreement-signed]'), null)
  assert.equal(element('[data-refund-agreement-sign]').disabled, true, 'a new application must read and sign again')
  assert.equal(state(wrapper).signaturePreviewUrl, '')
  assert.equal(revoked.length, 1)
})

test('closing a signature during upload prevents a late response from signing the application', async t => {
  const wrapper = await openDialog(t)
  await scrollBottom(); t.mock.timers.tick(10000); await settle()
  await click('[data-refund-agreement-sign]'); await signStroke()
  let finish
  uploadWait = new Promise(resolve => { finish = resolve })
  await click('[data-refund-signature-confirm]')
  assert.equal(calls.filter(call => call.method === 'post').length, 1)
  await click('[data-refund-signature-cancel]')
  finish(); await settle()
  assert.equal(wrapper.emitted('signed'), undefined)
  assert.equal(wrapper.emitted('close'), undefined)
  assert.equal(created.length, 0)
  assert.equal(state(wrapper).uploading, false)
})

test('a pending signature preview cannot leak into a new unsigned application', async t => {
  let finish
  previewWait = new Promise(resolve => { finish = resolve })
  const wrapper = await openDialog(t, { signedAttachmentId: 51 })
  await wrapper.setProps({ visible: false, signedAttachmentId: 0 }); await settle()
  finish(); await settle()
  await wrapper.setProps({ visible: true }); await settle()
  assert.equal(created.length, 0)
  assert.equal(state(wrapper).signaturePreviewUrl, '')
  assert.equal(document.querySelector('[data-refund-agreement-signed]'), null)
})

test('editing the agreement restarts reading and invalidates an open signature pad', async t => {
  const wrapper = await openDialog(t)
  await scrollBottom(); t.mock.timers.tick(10000); await settle()
  await click('[data-refund-agreement-sign]')
  await wrapper.setProps({ text: '更新后的承诺书\n'.repeat(200) }); await settle()
  assert.equal(document.querySelector('[data-refund-signature]'), null)
  assert.equal(element('[data-refund-agreement-sign]').disabled, true)
  assert.equal(state(wrapper).remainingSeconds, 10)
})

test('administrator can save and restore the full agreement, with serialized writes', async () => {
  const wrapper = mount(Settings, { attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(wrapper); await settle()
  const textarea = wrapper.find('[data-refund-agreement-textarea]')
  assert.equal(textarea.element.value, '默认承诺书全文')
  assert.equal(wrapper.find('[data-refund-agreement-restore]').element.disabled, true)
  await textarea.setValue('本人承诺：本次收款账户为本人账户。')
  assert.equal(wrapper.find('[data-refund-agreement-restore]').element.disabled, false, 'unsaved edits to a default agreement can also be restored')
  let finish
  saveWait = new Promise(resolve => { finish = resolve })
  await wrapper.find('[data-refund-agreement-save]').trigger('click'); await settle()
  assert.equal(calls.filter(call => call.method === 'put').length, 1)
  assert.equal(wrapper.findAll('button').find(button => button.text().includes('重新读取')).element.disabled, true)
  finish(); await settle()
  assert.ok(wrapper.find('[data-refund-agreement-custom]').exists())
  await wrapper.find('[data-refund-agreement-restore]').trigger('click'); await settle()
  assert.equal(textarea.element.value, '默认承诺书全文')
  assert.equal(calls.filter(call => call.method === 'put').length, 1, 'restoration is saved only after explicit save')
  await wrapper.find('[data-refund-agreement-save]').trigger('click'); await settle()
  assert.ok(wrapper.find('[data-refund-agreement-default]').exists())
})

test('administrator can save an agreement longer than twenty thousand characters without a UI cap', async () => {
  const wrapper = mount(Settings, { attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(wrapper); await settle()
  const textarea = wrapper.find('[data-refund-agreement-textarea]')
  const longText = '退款条款'.repeat(6000)
  assert.equal(textarea.attributes('maxlength'), undefined)
  assert.equal(textarea.attributes('show-count'), undefined)
  await textarea.setValue(longText)
  assert.equal(textarea.element.value, longText)
  assert.equal(wrapper.find('[data-refund-agreement-save]').element.disabled, false)
  await wrapper.find('[data-refund-agreement-save]').trigger('click'); await settle()
  assert.equal(calls.filter(call => call.method === 'put').length, 1)
  assert.equal(agreementText, longText)
})
