const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const users = require(path.join(root, 'src/utils/currentUser.js'))
const requests = []
const successful = suffix => ({ status: 200, data: { success: true, requestId: `ocr-request-${suffix}`, toolResult: [{ tool: 'image_ocr', text: `识别文本${suffix}` }], ocrCacheEntry: { urlHash: `hash-${suffix}` } } })
let recognize = url => Promise.resolve(successful(url))
let documentPreview = () => Promise.resolve({ status: 200, data: { images: [{ name: 'image1.png', contentType: 'image/png', data: 'aW1hZ2U=' }] } })
const apiPath = path.join(root, 'src/api/OCRBatchApi.mjs')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  recognizeOCRURL: url => { requests.push(['recognize', url]); return recognize(url) },
  previewOCRDocument: file => { requests.push(['preview', file]); return documentPreview() },
  uploadOCRImage: file => { requests.push(['upload', file]); return Promise.resolve(successful('upload')) },
}
require.cache[apiPath] = api
const Component = require(path.join(root, 'src/components/OCRBatchRecognition.vue'))
let wrapper
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
afterEach(async () => {
  wrapper?.unmount(); await settle(); document.body.replaceChildren(); requests.length = 0
  recognize = url => Promise.resolve(successful(url))
})
function create() {
  users.setStoredCurrentUser({ id: 9300, role: 'admin', username: 'ocr-batch-admin' })
  wrapper = mount(Component, { attachTo: document.body, global: { plugins: [Antd] } })
  wrapper.vm.entry.open()
}
function txt(text) { return { name: 'images.txt', size: text.length, text: async () => text } }

test('TXT预览不调用识别，明确开始后按顺序执行并保存逐条请求编号', async () => {
  create(); wrapper.vm.entry.upload(txt('https://img.example/a.png\nhttps://img.example/b.png')); await settle()
  assert.equal(requests.length, 0)
  assert.equal(wrapper.vm.pendingCount, 2)
  assert.match(document.body.textContent, /当前管理员余额/)
  assert.match(document.body.textContent, /开始识别（2 张，逐张计费）/)
  await wrapper.vm.entry.start(); await settle()
  assert.deepEqual(requests.map(value => value[1]), ['https://img.example/a.png', 'https://img.example/b.png'])
  assert.equal(wrapper.vm.completedCount, 2)
  assert.ok(wrapper.vm.items.every(item => item.requestId && item.status === 'saved'))
  assert.equal(wrapper.emitted('completed').length, 1)
})

test('DOCX只提取预览，确认开始后上传内嵌图片走付费图片端点', async () => {
  create(); const file = new Blob(['docx']); file.name = 'sample.docx'
  wrapper.vm.entry.upload(file); await settle()
  assert.deepEqual(requests.map(value => value[0]), ['preview'])
  assert.equal(wrapper.vm.items[0].name, 'image1.png')
  await wrapper.vm.entry.start(); await settle()
  assert.deepEqual(requests.map(value => value[0]), ['preview', 'upload'])
  assert.equal(requests[1][1].name, 'image1.png')
})

test('停止只阻止后续图片；网络结果未知不会重复识别同一图片', async () => {
  create(); wrapper.vm.entry.upload(txt('https://img.example/a.png\nhttps://img.example/b.png')); await settle()
  let resolve
  recognize = () => new Promise(done => { resolve = done })
  const running = wrapper.vm.entry.start()
  wrapper.vm.entry.stop()
  resolve(successful('first')); await running; await settle()
  assert.equal(requests.length, 1)
  assert.equal(wrapper.vm.pendingCount, 1)
  recognize = () => Promise.reject({ code: 'ERR_NETWORK' })
  await wrapper.vm.entry.start(); await settle()
  assert.equal(wrapper.vm.items[1].status, 'unknown')
  await wrapper.vm.entry.start()
  assert.equal(requests.length, 2)
})

test('身份切换会停止批量队列并丢弃旧识别结果', async () => {
  create(); wrapper.vm.entry.upload(txt('https://img.example/a.png\nhttps://img.example/b.png')); await settle()
  let resolve
  recognize = () => new Promise(done => { resolve = done })
  const stale = wrapper.vm.entry, pending = stale.start()
  users.setStoredCurrentUser({ id: 9301, role: 'admin', username: 'new-admin' }); await nextTick()
  resolve(successful('old-user')); await pending; await settle()
  assert.equal(requests.length, 1)
  assert.equal(wrapper.vm.items.length, 0)
  await stale.start(); stale.upload(txt('https://img.example/c.png'))
  assert.equal(requests.length, 1)
})

test('识别文本和扣费保存提示同时展示，明确失败保留请求编号', async () => {
  create(); wrapper.vm.entry.upload(txt('https://img.example/a.png\nhttps://img.example/b.png')); await settle()
  recognize = url => {
    if (url.endsWith('a.png')) {
      const response = successful('warning')
      response.data.billingWarning = '扣费待核对'
      response.data.ocrCacheWarning = '缓存保存待核对'
      return Promise.resolve(response)
    }
    return Promise.resolve({ status: 200, data: { success: false, requestId: 'failed-request', toolResult: [] } })
  }
  await wrapper.vm.entry.start(); await settle()
  assert.match(document.body.textContent, /识别文本warning/)
  assert.match(document.body.textContent, /扣费待核对 缓存保存待核对/)
  assert.equal(wrapper.vm.items[1].status, 'failed')
  assert.equal(wrapper.vm.items[1].requestId, 'failed-request')
  assert.match(document.body.textContent, /识别失败，请按请求编号核对账单和原始请求结果/)
})
