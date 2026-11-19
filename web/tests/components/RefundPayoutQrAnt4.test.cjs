const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const { webcrypto, createPublicKey, verify, randomBytes } = require('node:crypto')
require('../helpers/vue-dom.cjs')
require.extensions['.css'] = module => { module.exports = {} }
Object.defineProperty(globalThis, 'crypto', { configurable: true, value: webcrypto })
for (const name of ['File', 'FileReader', 'FormData']) globalThis[name] = window[name]
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const axios = require('axios')
const currentUser = require('../../src/utils/currentUser.js')
const owner = { id: 78200, role: 'user', status: 'enabled', username: 'SYNTHETIC_QR_OWNER' }
const imageDataUrl = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADElEQVR42mP8z8BQDwAE/wGJxJrtGQAAAABJRU5ErkJggg=='
const bytes = Buffer.from(imageDataUrl.split(',')[1], 'base64')
const storageKey = `tiku.refund.payoutQr.device.${owner.id}`
const originalAdapter = axios.defaults.adapter
let memory = null, nextId = 700, statusCode = 200, registerWait = null, statusWait = null
const calls = [], pages = [], unexpected = [], challenges = new Map(), runtime = []
function response(config, data, status = 200) { return { config, data, status, headers: {}, request: { memoryOnly: true } } }
axios.defaults.adapter = async config => {
  const url = String(config.url), method = String(config.method), data = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ url, method, data })
  if (url === '/api/ticket-attachments' && method === 'post') return response(config, { data: { id: ++nextId, mediaType: 'image/png' } }, 201)
  if (url === '/api/ticket-refunds/payout-qr/status') {
    const snapshot = memory ? { hasPrevious: true, keyId: memory.keyId, previewDataUrl: imageDataUrl } : { hasPrevious: false }
    if (statusWait) await statusWait
    return response(config, snapshot, statusCode)
  }
  if (url === '/api/ticket-refunds/payout-qr/register') {
    if (registerWait) await registerWait
    memory = { attachmentId: data.attachmentId, keyId: `synthetic-${data.attachmentId}`, publicKey: data.publicKey }
    return response(config, { attachmentId: memory.attachmentId, keyId: memory.keyId })
  }
  if (url === '/api/ticket-refunds/payout-qr/challenge') {
    assert.ok(memory)
    const challengeId = randomBytes(8).toString('hex'), challenge = randomBytes(32)
    challenges.set(challengeId, challenge)
    return response(config, { challengeId, challenge: challenge.toString('base64') })
  }
  if (url === '/api/ticket-refunds/payout-qr/device-unlock') {
    const challenge = challenges.get(data.challengeId)
    challenges.delete(data.challengeId)
    const key = createPublicKey({ key: Buffer.from(memory.publicKey, 'base64'), format: 'der', type: 'spki' })
    assert.ok(challenge && verify('sha256', challenge, { key, dsaEncoding: 'ieee-p1363' }, Buffer.from(data.signature, 'base64')), 'real ECDSA proof verifies against registered SPKI')
    return response(config, { attachmentId: memory.attachmentId, imageDataUrl })
  }
  if (url === '/api/ticket-refunds/payout-qr/recover') return data.password === 'SYNTHETIC_PASSWORD'
    ? response(config, { attachmentId: memory.attachmentId, imageDataUrl }) : response(config, { message: '密码错误' }, 403)
  if (url === '/api/ticket-refunds/payout-qr' && method === 'delete') { memory = null; return response(config, {}) }
  unexpected.push(`${method} ${url}`)
  throw new Error('Unmatched fixture request')
}
const PayoutQr = require('../../src/components/RefundPayoutQr.vue')
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function until(predicate, label) {
  for (let i = 0; i < 100 && !predicate(); i++) { await settle(); await new Promise(resolve => setTimeout(resolve, 2)) }
  assert.ok(predicate(), label)
}
async function open() {
  currentUser.setStoredCurrentUser(owner)
  const wrapper = mount(PayoutQr, { attachTo: document.body, props: { userId: owner.id }, global: { plugins: [Antd], config: { errorHandler: error => runtime.push(error) } } })
  pages.push(wrapper)
  await until(() => wrapper.attributes('aria-busy') === 'false', 'initial backend status loaded')
  return wrapper
}
async function upload(wrapper, type = 'image/png') {
  await wrapper.vm.$.setupState.pick({ target: { files: [new File([bytes], 'synthetic-qr.png', { type })], value: '' } })
  await settle()
}
function clearPages() { for (const page of pages.splice(0)) page.unmount() }
afterEach(async () => {
  clearPages(); await settle(); Antd.message.destroy(); Antd.Modal.destroyAll()
  document.body.replaceChildren(); currentUser.clearStoredCurrentUser(); window.localStorage.clear()
  assert.deepEqual(runtime.splice(0), []); assert.deepEqual(unexpected.splice(0), [])
  memory = null; nextId = 700; statusCode = 200; registerWait = null; statusWait = null; calls.length = 0; challenges.clear()
})
after(() => { axios.defaults.adapter = originalAdapter })

test('backend empty state uploads with a key and same-device remount proves possession without password', async () => {
  const page = await open()
  assert.ok(page.find('[data-ticket-payout-qr-add]').exists())
  assert.equal(page.find('[data-ticket-payout-qr-lock]').exists(), false)
  assert.equal(page.find('input[type="file"]').classes().includes('refund-payout__input'), true)
  await upload(page, '')
  assert.ok(page.find('[data-ticket-payout-qr-preview]').exists(), 'valid PNG without browser MIME is displayed')
  const stored = JSON.parse(localStorage.getItem(storageKey))
  assert.ok(stored.privateKey.d && stored.keyId === memory.keyId)
  assert.equal(localStorage.getItem(`tiku.refund.payoutQr.${owner.id}`), null, 'no legacy cleartext image cache')
  assert.ok(!localStorage.getItem(storageKey).includes('imageDataUrl'))
  clearPages()
  const restored = await open()
  assert.ok(restored.find('[data-ticket-payout-qr-preview]').exists())
  assert.equal(calls.filter(call => call.url.endsWith('/device-unlock')).length, 1)
  assert.equal(calls.filter(call => call.url.endsWith('/recover')).length, 0)
  const id = await restored.vm.ensureDraft()
  assert.notEqual(id, memory.attachmentId, 'each application gets a fresh image attachment')
  assert.equal(calls.filter(call => call.url.endsWith('/register')).length, 1, 'cloning application evidence does not rebind memory')
})

test('the native file input stays hidden under global input styles and tile activation still opens it', async () => {
  const style = document.createElement('style')
  style.textContent = 'input[type="file"] { display: inline-block !important; }'
  document.head.append(style)
  try {
    const page = await open()
    const input = page.get('[data-ticket-payout-qr-input]').element
    assert.equal(input.hidden, true)
    assert.equal(input.getAttribute('aria-hidden'), 'true')
    assert.equal(input.tabIndex, -1)
    assert.equal(window.getComputedStyle(input).display, 'none', 'inline hiding survives broad global input styles')
    let opened = 0
    input.addEventListener('click', event => { event.preventDefault(); opened++ })
    await page.get('[data-ticket-payout-qr-tile]').trigger('click')
    assert.equal(opened, 1, 'the tile activates the hidden native input')
    Object.defineProperty(input, 'files', { configurable: true, value: [new File([bytes], 'synthetic-qr.png', { type: 'image/png' })] })
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await until(() => page.find('[data-ticket-payout-qr-preview]').exists(), 'hidden input change still uploads and displays the image')
  } finally { style.remove() }
})

test('another device sees a lock and password recovery grants only the current session', async () => {
  const first = await open(); await upload(first); clearPages(); localStorage.clear()
  const page = await open()
  assert.ok(page.find('[data-ticket-payout-qr-lock]').exists())
  assert.equal(page.find('[data-ticket-payout-qr-preview]').exists(), false)
  await page.get('[data-ticket-payout-qr-tile]').trigger('click')
  await settle()
  const input = document.querySelector('input[autocomplete="current-password"]')
  assert.ok(input, 'clicking a locked image opens password dialog')
  input.value = 'SYNTHETIC_PASSWORD'; input.dispatchEvent(new Event('input', { bubbles: true }))
  await settle(); await page.vm.$.setupState.unlock(); await settle()
  assert.ok(page.find('[data-ticket-payout-qr-preview]').exists())
  assert.equal(localStorage.getItem(storageKey), null, 'recovering does not assign original-device status')
  clearPages()
  assert.ok((await open()).find('[data-ticket-payout-qr-lock]').exists(), 'next application on another device needs verification again')
})

test('a locked image can be replaced without unlocking and deletion stays removed on reopen', async () => {
  const first = await open(); await upload(first); const oldKey = memory.keyId
  clearPages(); localStorage.clear()
  const page = await open()
  assert.ok(page.find('[data-ticket-payout-qr-replace]').exists())
  await upload(page)
  assert.notEqual(memory.keyId, oldKey)
  assert.equal(calls.filter(call => call.url.endsWith('/recover')).length, 0)
  assert.ok(page.find('[data-ticket-payout-qr-preview]').exists())
  await page.get('[data-ticket-payout-qr-delete]').trigger('click')
  await until(() => !memory && page.find('[data-ticket-payout-qr-add]').exists(), 'delete clears server memory and UI')
  assert.equal(localStorage.getItem(storageKey), null)
  clearPages()
  assert.ok((await open()).find('[data-ticket-payout-qr-add]').exists())
})

test('private key is persisted only after successful registration', async () => {
  const page = await open()
  let finish
  registerWait = new Promise(resolve => { finish = resolve })
  const pending = upload(page)
  await until(() => calls.some(call => call.url.endsWith('/register')), 'registration reached')
  assert.equal(localStorage.getItem(storageKey), null)
  assert.equal(page.find('[data-ticket-payout-qr-preview]').exists(), false)
  finish(); await pending
  assert.ok(localStorage.getItem(storageKey))
  assert.ok(page.find('[data-ticket-payout-qr-preview]').exists())
})

test('a failed status lookup offers a retry instead of claiming there is no saved image', async () => {
  statusCode = 503
  const page = await open()
  assert.ok(page.find('[role="alert"]').exists())
  assert.equal(page.get('[data-ticket-payout-qr-tile]').attributes('aria-label'), '重试加载收款码')
  statusCode = 200
  await page.get('[data-ticket-payout-qr-tile]').trigger('click')
  await until(() => page.attributes('aria-busy') === 'false' && !page.find('[role="alert"]').exists(), 'retry resolves state')
})

test('a late empty status cannot erase a device key just saved by another tab', async () => {
  let finish
  statusWait = new Promise(resolve => { finish = resolve })
  const pendingPage = open()
  await until(() => calls.some(call => call.url.endsWith('/status')), 'empty status requested')
  const savedByOtherTab = JSON.stringify({ keyId: 'synthetic-new-key', privateKey: { kty: 'EC', d: 'synthetic-private' } })
  localStorage.setItem(storageKey, savedByOtherTab)
  finish()
  const page = await pendingPage
  assert.ok(page.find('[data-ticket-payout-qr-add]').exists())
  assert.equal(localStorage.getItem(storageKey), savedByOtherTab, 'the next status query can still use the new device binding')
})
