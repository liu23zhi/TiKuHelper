const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path'), Module = require('node:module')
require('../helpers/vue-dom.cjs')
for (const name of ['HTMLInputElement', 'HTMLButtonElement', 'HTMLTextAreaElement']) global[name] = window[name]
const { mount, flushPromises } = require('@vue/test-utils')
const { h, nextTick } = require('vue')
const { createMemoryHistory, createRouter, RouterView } = require('vue-router')
const Antd = require('ant-design-vue')
const user = require('../../src/utils/currentUser.js')

const admin = { id: 111, role: 'admin', username: 'fixture_admin' }
const normal = { id: 222, role: 'user', username: 'fixture_user' }
const ownerCommand = 'powershell -EncodedCommand ZgA='
const row = (name, ownerId) => ({ id: 7, ownerId, name, scheme: 'https', baseDomain: 'example.test', shortCode: 'a', versionId: 5, command: ownerCommand })
const okay = data => ({ status: 200, data })
const reads = [], writes = [], mounts = []
let holdReads = false, holdWrites = false

const apiPath = path.resolve(__dirname, '../../src/api/TikuComponentApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = {
  getScripts(params) {
    if (!holdReads) return Promise.resolve(okay([row('现有脚本', 111)]))
    return new Promise(resolve => reads.push({ params, resolve }))
  },
  getScriptVersions: async () => okay([{ id: 5, version: 'synthetic' }]),
  getScriptDomainSettings: async () => okay({ domains: ['example.test'], allowWildcard: true }),
  createScript(payload, key) {
    if (!holdWrites) return Promise.resolve(okay({}))
    return new Promise(resolve => writes.push({ payload, key, resolve }))
  },
}
require.cache[apiPath] = api
const PAGE = require('../../src/pages/ScriptManagement/index.vue')

async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function open(account = admin) {
  user.setStoredCurrentUser(account)
  const history = createMemoryHistory(), router = createRouter({ history, routes: [
    { path: '/adapter/scripts', component: PAGE },
    { path: '/other', component: { render: () => h('div', 'Other') } },
    { path: '/login', component: { render: () => h('div', 'Login') } },
  ] })
  await router.push('/adapter/scripts'); await router.isReady()
  const wrapper = mount(RouterView, { attachTo: document.body, global: { plugins: [router, Antd] } })
  mounts.push({ wrapper, history })
  await settle()
  return { wrapper, router, page: wrapper.findComponent(PAGE), state: wrapper.findComponent(PAGE).vm.$.setupState }
}

afterEach(async () => {
  for (const { wrapper, history } of mounts.splice(0)) { wrapper.unmount(); history.destroy() }
  Antd.Modal.destroyAll(); Antd.message.destroy()
  await settle()
  user.clearStoredCurrentUser()
  document.body.replaceChildren()
  reads.length = writes.length = 0; holdReads = holdWrites = false
})

test('script identity change clears rows and prevents late all-scope result publication', async () => {
  const { state, page } = await open()
  assert.equal(state.rows[0].name, '现有脚本')
  assert.doesNotMatch(page.text(), /EncodedCommand/, 'launcher credentials are not printed in the table')
  assert.equal(state.mayCopyCommand(state.rows[0]), true)
  assert.equal(state.mayCopyCommand(row('其他账号', 222)), false)
  holdReads = true
  state.toggleScope()
  assert.deepEqual(reads[0].params, { scope: 'all' })
  assert.deepEqual(state.rows, [])
  user.setStoredCurrentUser(normal)
  await settle()
  assert.deepEqual(reads[1].params, { scope: 'self' })
  reads[0].resolve(okay([row('迟到管理员数据', 111)]))
  await settle()
  assert.deepEqual(state.rows, [])
  assert.equal(state.loading, true, 'old request does not clear new loading')
  reads[1].resolve(okay([row('新账号脚本', 222)]))
  await settle()
  assert.equal(state.rows[0].name, '新账号脚本')
  assert.equal(state.loading, false)
})

test('script save deduplicates clicks and retains the idempotency key only for an unchanged retry', async () => {
  const { state, page } = await open()
  holdWrites = true
  state.openCreate()
  await settle()
  state.setFormField('name', '合成脚本')
  state.setFormField('baseDomain', 'example.test')
  const editor = page.findComponent(Antd.Modal)
  assert.ok(editor.exists())
  editor.vm.$emit('ok')
  editor.vm.$emit('ok')
  assert.equal(writes.length, 1)
  assert.match(writes[0].key, /^script-create-[0-9a-f]{32}$/)
  assert.equal(Object.hasOwn(writes[0].payload, 'command'), false)
  writes[0].resolve({ status: 503 })
  await settle()
  assert.equal(state.modalOpen, true)
  editor.vm.$emit('ok')
  assert.equal(writes[0].key, writes[1].key)
  writes[1].resolve({ status: 503 })
  await settle()
  state.setFormField('name', '修改合成脚本')
  editor.vm.$emit('ok')
  assert.notEqual(writes[1].key, writes[2].key)
  user.clearStoredCurrentUser()
  writes[2].resolve(okay({}))
  await settle()
  assert.equal(state.modalOpen, false)
  assert.equal(state.form.name, '')
  assert.equal(state.rows.length, 0)
})

test('leaving the script page asks before discarding a draft but never traps logout', async () => {
  const { state, router } = await open()
  state.openCreate()
  state.setFormField('name', '待保留草稿')
  const navigation = router.push('/other')
  await settle()
  const cancel = document.querySelector('.ant-modal-confirm .ant-modal-confirm-btns .ant-btn-default')
  assert.ok(cancel)
  cancel.click()
  await navigation
  assert.equal(router.currentRoute.value.path, '/adapter/scripts')
  assert.equal(state.form.name, '待保留草稿')
  await router.push('/login')
  await settle()
  assert.equal(router.currentRoute.value.path, '/login')
})

test('native editor Cancel preserves dirty input until the user explicitly discards it', async () => {
  const { state } = await open()
  state.openCreate()
  await settle()
  const input = document.querySelector('input[placeholder="例如：超星答题助手"]')
  assert.ok(input)
  input.value = '需要保留的脚本草稿'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  const click = (selector, label) => {
    const button = [...document.querySelectorAll(selector)].filter(item => item.textContent.replace(/\s+/g, '') === label).at(-1)
    assert.ok(button, label)
    button.click()
  }
  click('.ant-modal-footer button', '取消')
  await settle()
  assert.equal(state.modalOpen, true)
  assert.match(document.querySelector('.ant-modal-confirm-title').textContent, /有未保存的更改/)
  click('.ant-modal-confirm-btns button', '保留编辑')
  await settle()
  assert.equal(input.value, '需要保留的脚本草稿')
  assert.equal(state.modalOpen, true)
  click('.ant-modal-footer button', '取消')
  await settle()
  click('.ant-modal-confirm-btns button', '放弃并离开')
  await settle()
  assert.equal(state.modalOpen, false)
  assert.equal(state.form.name, '')
})
