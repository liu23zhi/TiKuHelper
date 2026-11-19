/* Actual attachment component and API transport; no image bytes leave this memory fixture. */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
require.extensions['.css'] = module => { module.exports = {} }
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const axios = require('axios')
const image = new Blob(['synthetic-image'], { type: 'image/png' })
const originalAdapter = axios.defaults.adapter, originalCreate = URL.createObjectURL, originalRevoke = URL.revokeObjectURL
const calls = [], wrappers = [], created = [], revoked = [], unexpected = [], runtime = []
const approved = { id: 41, visible: true, scanState: 'approved' }
function response(config, data, status = 200) { return { config, data, status, headers: {}, request: { memoryOnly: true } } }
let serve = config => response(config, image)
axios.defaults.adapter = async config => {
  calls.push({ url: config.url, method: config.method })
  if (config.method === 'get' && /^\/api\/ticket-attachments\/\d+(\/admin)?$/.test(config.url)) return serve(config)
  unexpected.push(`${config.method} ${config.url}`)
  throw new Error('Unmatched attachment fixture request')
}
URL.createObjectURL = blob => { created.push(blob); return `blob:http://localhost/attachment-${created.length}` }
URL.revokeObjectURL = value => revoked.push(value)
const Attachment = require('../../src/components/TicketAttachment.vue')
const ImagePicker = require('../../src/components/TicketImagePicker.vue')
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(attachment, props = {}) {
  const wrapper = mount(Attachment, { attachTo: document.body, props: { attachment, ...props }, global: { plugins: [Antd], config: { errorHandler: error => runtime.push(error) } } })
  wrappers.push(wrapper); await settle()
  return wrapper
}
function assertPlaceholder(wrapper, reason) {
  assert.equal(wrapper.find('img').exists(), false, 'placeholders never render an empty or broken img')
  assert.equal(wrapper.get('.ticket-attachment-placeholder').attributes('aria-label'), reason)
  assert.match(wrapper.get('.ticket-attachment-hint').text(), new RegExp(reason))
  const icon = wrapper.get('.ticket-attachment-icon')
  assert.equal(icon.attributes('viewBox'), '0 0 1024 1024')
  assert.match(icon.get('path').attributes('d'), /^M402\.57 905\.35H176\.1/)
  assert.equal(icon.find('rect').exists(), false, 'the error icon has no lock overlay')
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  await settle(); Antd.message.destroy(); document.body.replaceChildren()
  assert.deepEqual(unexpected.splice(0), []); assert.deepEqual(runtime.splice(0), [])
  calls.length = created.length = revoked.length = 0
  serve = config => response(config, image)
})
after(() => { axios.defaults.adapter = originalAdapter; URL.createObjectURL = originalCreate; URL.revokeObjectURL = originalRevoke })

test('pending, failed and sensitive images render their reasons without downloading', async () => {
  for (const [scanState, reason] of [
    ['pending', '图片正在进行安全检测，暂不显示'],
    ['failed', '图片未通过安全检测，已隐藏'],
    ['sensitive', '此图片包含敏感内容，已隐藏'],
  ]) {
    const wrapper = await open({ id: 41, visible: false, scanState })
    assertPlaceholder(wrapper, reason)
    assert.equal(wrapper.find('button').exists(), false, 'regular users cannot reveal hidden images')
  }
  const custom = await open({ id: 42, visible: false, hiddenReason: '图片已被清理，请重新上传' })
  assertPlaceholder(custom, '图片已被清理，请重新上传')
  assert.deepEqual(calls, [], 'no request is made for any invisible image')
})

test('a readable image waits for a usable URL and releases it after a decode error', async () => {
  let finish
  serve = config => new Promise(resolve => { finish = () => resolve(response(config, image)) })
  const wrapper = await open(approved)
  assertPlaceholder(wrapper, '图片正在加载，请稍候')
  assert.equal(wrapper.attributes('aria-busy'), 'true')
  finish(); await settle()
  const img = wrapper.get('img')
  assert.equal(img.attributes('src'), 'blob:http://localhost/attachment-1')
  assert.equal(wrapper.find('.ticket-attachment-placeholder').exists(), false)
  await img.trigger('error')
  assertPlaceholder(wrapper, '图片加载失败，请稍后重试')
  assert.deepEqual(revoked, ['blob:http://localhost/attachment-1'], 'broken image URL is revoked')
  assert.equal(wrapper.get('[role="alert"]').text(), '图片加载失败，请稍后重试')
})

test('HTTP and invalid image responses use the error placeholder without an img', async () => {
  serve = config => response(config, { message: '图片已过期' }, 404)
  const missing = await open(approved)
  assertPlaceholder(missing, '图片已过期')
  serve = config => response(config, new Blob(['not-an-image'], { type: 'text/plain' }))
  const invalid = await open({ ...approved, id: 42 })
  assertPlaceholder(invalid, '未能读取该图片')
  assert.deepEqual(created, [], 'invalid response data never becomes an object URL')
})

test('an admin reveals a hidden image through the audited endpoint and sees its caption', async () => {
  const wrapper = await open({ id: 43, visible: false, scanState: 'sensitive' }, { admin: true })
  assert.deepEqual(calls, [], 'admin images are also hidden until explicitly requested')
  await wrapper.get('button').trigger('click'); await settle()
  assert.deepEqual(calls, [{ url: '/api/ticket-attachments/43/admin', method: 'get' }])
  assert.equal(wrapper.find('img').exists(), true, 'successful reveal must display even though server visibility remains false')
  assert.match(wrapper.get('figcaption').text(), /仅供管理员核查/)
  assert.equal(wrapper.classes().includes('is-hidden'), false)
  await wrapper.setProps({ admin: false }); await settle()
  assertPlaceholder(wrapper, '此图片包含敏感内容，已隐藏')
  assert.equal(wrapper.find('button').exists(), false)
  assert.deepEqual(revoked, ['blob:http://localhost/attachment-1'])
})

test('a late read cannot reintroduce an image after its visibility is revoked', async () => {
  let finish
  serve = config => new Promise(resolve => { finish = () => resolve(response(config, image)) })
  const wrapper = await open(approved)
  await wrapper.setProps({ attachment: { ...approved, visible: false, scanState: 'failed' } }); await settle()
  assertPlaceholder(wrapper, '图片未通过安全检测，已隐藏')
  assert.equal(wrapper.attributes('aria-busy'), 'false')
  finish(); await settle()
  assertPlaceholder(wrapper, '图片未通过安全检测，已隐藏')
  assert.deepEqual(created, [], 'retired response does not create or expose an image URL')
})

test('the shared image chooser hides native controls and forwards selection through its button', async () => {
  const wrapper = mount(ImagePicker, { attachTo: document.body, props: { max: 4 } })
  wrappers.push(wrapper)
  const input = wrapper.get('input[type="file"]').element
  assert.equal(input.hidden, true)
  assert.equal(input.getAttribute('aria-hidden'), 'true')
  assert.equal(input.tabIndex, -1)
  assert.equal(input.multiple, true)
  assert.equal(window.getComputedStyle(input).display, 'none')
  assert.match(wrapper.get('button').text(), /最多 4 张/)
  let opened = 0
  input.addEventListener('click', event => { event.preventDefault(); opened++ })
  wrapper.get('button').element.click(); await settle()
  assert.equal(opened, 1)
  const selected = new window.File(['synthetic'], 'shot.png', { type: 'image/png' })
  Object.defineProperty(input, 'files', { configurable: true, value: [selected] })
  input.dispatchEvent(new Event('change', { bubbles: true })); await settle()
  assert.equal(wrapper.emitted('change')[0][0].target.files[0], selected)
  await wrapper.setProps({ disabled: true })
  assert.equal(input.disabled, true)
  wrapper.get('button').element.click(); await settle()
  assert.equal(opened, 1, 'a disabled picker cannot open file selection')
})
