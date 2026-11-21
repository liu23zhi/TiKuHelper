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
const preview = { previewId: 'qprev-0011223344556677', targets: [{ userId: 401, username: 'real-server-target', nickname: '目标昵称', role: 'user', status: 'enabled', phone: '138****5678', reason: '测试封禁', expiresAt: null, createdAt: '2026-01-01T00:00:00Z' }] }
let respondPreview = () => Promise.resolve({ status: 200, data: preview })
let respondConfirm = () => Promise.resolve({ status: 200, data: { succeeded: 1 } })
const apiPath = path.join(root, 'src/api/UserBanImportApi.mjs')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  previewUserBanImport: file => { requests.push(['preview', file]); return respondPreview() },
  confirmUserBanImport: id => { requests.push(['confirm', id]); return respondConfirm() },
  deleteUserBanImportPreview: id => { requests.push(['delete', id]); return Promise.resolve({ status: 204 }) },
}
require.cache[apiPath] = api
const Component = require(path.join(root, 'src/components/UserBanImport.vue'))
let wrapper
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
afterEach(async () => {
  wrapper?.unmount(); await settle(); document.body.replaceChildren(); requests.length = 0
  respondPreview = () => Promise.resolve({ status: 200, data: preview })
  respondConfirm = () => Promise.resolve({ status: 200, data: { succeeded: 1 } })
})
function create() {
  users.setStoredCurrentUser({ id: 9300, role: 'admin', username: 'ban-import-admin' })
  wrapper = mount(Component, { attachTo: document.body, global: { plugins: [Antd] } })
}
function file() { const result = new Blob(['[]'], { type: 'application/json' }); result.name = 'bans.json'; return result }

test('真实弹窗显示服务器查询的账号详情，明确勾选后才提交确认', async () => {
  create()
  assert.equal(wrapper.vm.entry.upload(file()), false)
  await settle()
  assert.equal(requests.length, 1)
  assert.match(document.body.textContent, /real-server-target/)
  assert.match(document.body.textContent, /目标昵称/)
  assert.match(document.body.textContent, /138\*\*\*\*5678/)
  assert.match(document.body.textContent, /永久/)
  let confirmButton = [...document.body.querySelectorAll('button')].find(node => node.textContent.includes('确认封禁 1 个账号'))
  assert.ok(confirmButton)
  assert.equal(confirmButton.disabled, true)
  await wrapper.vm.entry.confirm()
  assert.equal(requests.length, 1)
  wrapper.vm.entry.checked({ target: { checked: true } })
  await nextTick()
  confirmButton = [...document.body.querySelectorAll('button')].find(node => node.textContent.includes('确认封禁 1 个账号'))
  assert.equal(confirmButton.disabled, false)
  confirmButton.click(); await settle()
  assert.deepEqual(requests[1], ['confirm', preview.previewId])
  assert.equal(wrapper.vm.visible, false)
  assert.equal(wrapper.emitted('imported').length, 1)
})

test('取消预览不封禁，身份切换后旧上传与确认回调无效', async () => {
  create(); wrapper.vm.entry.upload(file()); await settle()
  wrapper.vm.entry.cancel(); await settle()
  assert.deepEqual(requests.map(value => value[0]), ['preview', 'delete'])
  let resolve
  respondPreview = () => new Promise(done => { resolve = done })
  const stale = wrapper.vm.entry
  stale.upload(file())
  users.setStoredCurrentUser({ id: 9301, role: 'admin', username: 'new-admin' })
  await nextTick()
  resolve({ status: 200, data: preview }); await settle()
  assert.equal(wrapper.vm.visible, false)
  stale.checked({ target: { checked: true } }); await stale.confirm(); stale.upload(file())
  assert.deepEqual(requests.map(value => value[0]), ['preview', 'delete', 'preview'])
})

test('确认结果未知时销毁本地确认资格，不会重复提交封禁', async () => {
  create(); wrapper.vm.entry.upload(file()); await settle()
  respondConfirm = () => Promise.reject({ code: 'ERR_NETWORK' })
  wrapper.vm.entry.checked({ target: { checked: true } }); await wrapper.vm.entry.confirm(); await settle()
  assert.equal(wrapper.vm.previewId, '')
  assert.ok(wrapper.vm.failure)
  wrapper.vm.entry.checked({ target: { checked: true } }); await wrapper.vm.entry.confirm()
  assert.equal(requests.filter(value => value[0] === 'confirm').length, 1)
  assert.match(document.body.textContent, /不会重复提交/)
})

test('取消后重新预览时，旧弹窗的勾选、确认和取消不能作用于新目标', async () => {
  create(); wrapper.vm.entry.upload(file()); await settle()
  const oldDialog = wrapper.vm.entry
  oldDialog.cancel(); await settle()
  respondPreview = () => Promise.resolve({ status: 200, data: { ...preview, previewId: 'qprev-8899aabbccddeeff', targets: [{ ...preview.targets[0], userId: 402, username: 'second-target' }] } })
  wrapper.vm.entry.upload(file()); await settle()
  oldDialog.checked({ target: { checked: true } }); await oldDialog.confirm(); oldDialog.cancel()
  assert.equal(wrapper.vm.confirmed, false)
  assert.equal(wrapper.vm.previewId, 'qprev-8899aabbccddeeff')
  assert.equal(wrapper.vm.visible, true)
  assert.equal(requests.filter(value => value[0] === 'confirm').length, 0)
})
