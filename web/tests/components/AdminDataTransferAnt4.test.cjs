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
const apiPath = path.join(root, 'src/pages/IntelligentImport/asyncTaskApi.mjs')
const realApi = require(apiPath)
const calls = []
let response = () => Promise.resolve({ status: 202, data: { taskId: 'exp-0011223344556677', status: 'pending' } })
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  ...realApi,
  submitExportTask: options => { calls.push(['export', options]); return response() },
  submitImportTask: options => { calls.push(['import', options]); return response() },
  pollAsyncTask: async () => ({ task: { taskId: 'exp-0011223344556677', status: 'succeeded', downloadUrl: '/api/export/tasks/exp-0011223344556677/download' } }),
}
require.cache[apiPath] = api
const Component = require(path.join(root, 'src/components/AdminDataTransfer.vue'))
let wrapper
afterEach(async () => {
  wrapper?.unmount(); calls.length = 0
  response = () => Promise.resolve({ status: 202, data: { taskId: 'exp-0011223344556677', status: 'pending' } })
  await flushPromises(); document.body.replaceChildren()
})
function create(props = {}) {
  users.setStoredCurrentUser({ id: 951, role: 'admin', username: 'transfer-admin' })
  wrapper = mount(Component, { props: { resource: 'logs', title: '操作日志', ...props }, attachTo: document.body, global: { plugins: [Antd] } })
  return wrapper
}
test('管理导出默认脱敏，JSON 格式和明文选项真实传入异步任务并显示下载', async () => {
  create()
  assert.equal(calls.length, 0, '打开页面不请求后台任务')
  assert.equal(wrapper.vm.masked, true)
  wrapper.vm.entry.format('json'); wrapper.vm.entry.masked({ target: { checked: false } })
  await wrapper.vm.entry.export(); await nextTick()
  assert.deepEqual(calls, [['export', { resource: 'logs', format: 'json', masked: false }]])
  assert.equal(wrapper.vm.busy, false)
  assert.equal(wrapper.vm.task.status, 'succeeded')
  assert.match(wrapper.vm.downloadUrl, /^\/api\/export\/tasks\//)
})

test('限定格式使用首个可用值，敏感图片 JSON / XLSX 不会回退成 CSV', async () => {
  create({ resource: 'sensitive_images', formats: ['json', 'xlsx'] })
  assert.equal(wrapper.vm.format, 'json')
  await wrapper.vm.entry.export()
  assert.equal(calls[0][1].format, 'json')
  wrapper.vm.entry.format('xlsx')
  await wrapper.vm.entry.export()
  assert.equal(calls[1][1].format, 'xlsx')
})
test('缓存导入完成通知页面刷新；切换类别后旧控件不能继续导入或导出', async () => {
  create({ resource: 'ip_locations', importable: true })
  const file = new Blob(['[]'], { type: 'application/json' }); file.name = 'locations.json'
  assert.equal(wrapper.vm.entry.import(file), false)
  await flushPromises()
  assert.equal(calls[0][0], 'import')
  assert.equal(calls[0][1].file, file)
  assert.equal(wrapper.emitted('imported').length, 1)
  const stale = wrapper.vm.entry
  await wrapper.setProps({ resource: 'phone_locations' })
  stale.import(file); await stale.export()
  assert.equal(calls.length, 1)
  assert.equal(wrapper.vm.task, null)
})
test('身份切换会丢弃未完成请求，旧下载和提交回调失效', async () => {
  create()
  let resolve
  response = () => new Promise(done => { resolve = done })
  const stale = wrapper.vm.entry, pending = stale.export()
  users.setStoredCurrentUser({ id: 952, role: 'admin', username: 'another-admin' })
  await nextTick()
  resolve({ status: 202, data: { taskId: 'exp-0011223344556677', status: 'pending' } })
  await pending
  assert.equal(wrapper.vm.task, null)
  await stale.export(); assert.equal(calls.length, 1)
  let prevented = false
  stale.download({ preventDefault: () => { prevented = true } })
  assert.equal(prevented, true)
})
