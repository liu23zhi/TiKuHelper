const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const axios = require('axios')
const Swal = require('sweetalert2')
const user = require('../../src/utils/currentUser.js')
const pending = [], wrappers = [], detections = [], confirmations = []
const originalAdapter = axios.defaults.adapter, originalFire = Swal.fire
axios.defaults.adapter = config => new Promise(resolve => pending.push({
  config, payload: typeof config.data === 'string' ? JSON.parse(config.data) : config.data,
  resolve: data => resolve({ status: 200, data, headers: {}, config }),
}))
const apiPath = path.resolve(__dirname, '../../src/api/SensitiveImageApi.js')
const api = new Module(apiPath); api.loaded = true
api.exports = { manualDetectSensitiveImage: payload => new Promise(resolve => detections.push({ payload, resolve })) }
require.cache[apiPath] = api
Swal.fire = options => new Promise(resolve => confirmations.push({ options, resolve }))
const Cache = require('../../src/components/CacheRetentionSettings.vue')
const Logs = require('../../src/components/LogRetentionSettingsPanel.vue')
const Detect = require('../../src/components/SensitiveImageDetectButton.vue')
const admin = id => ({ id, role: 'admin', status: 'enabled', balance: 1 })
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function mounted(Component, props = {}) {
  const wrapper = mount(Component, { props, attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(wrapper); await settle(); return wrapper
}
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  for (const entry of pending.splice(0)) entry.resolve({})
  for (const entry of confirmations.splice(0)) entry.resolve({ isConfirmed: false })
  for (const entry of detections.splice(0)) entry.resolve({ status: 200, data: { state: 'clean' } })
  user.clearStoredCurrentUser(); await settle(); document.body.replaceChildren(); Antd.message.destroy()
})

for (const spec of [
  { name: 'image retention', Component: Cache, url: '/api/admin/ticket-image-cache/settings', body: { mode: 'both', maxAgeDays: 180, maxTotalGiB: 20, cleanupIntervalMinutes: 60 }, value: 180, response: value => value },
  { name: 'log retention', Component: Logs, url: '/api/admin/log-settings', body: { enabled: true, maxAgeDays: 30, cleanupIntervalMinutes: 60 }, value: 30, response: value => ({ settings: value }) },
]) {
  test(spec.name + ': real form saves exact payload, preserves balance-only drafts, and retires native callbacks', async () => {
    user.setStoredCurrentUser(admin(80001))
    const wrapper = await mounted(spec.Component)
    assert.equal(pending.length, 1); assert.equal(pending[0].config.url, spec.url)
    assert.equal(wrapper.get('button[type="submit"]').element.disabled, true)
    pending[0].resolve(spec.response(spec.body)); await settle()
    const input = wrapper.findAll('input').find(item => item.element.value === String(spec.value)); assert.ok(input)
    await input.setValue('77'); await input.trigger('blur'); await settle()
    const oldSubmit = wrapper.findComponent(Antd.Form).vm.$.vnode.props.onFinish
    const oldInput = wrapper.findAllComponents(Antd.InputNumber)[0].vm.$.vnode.props['onUpdate:value']
    user.setStoredCurrentUser({ ...admin(80001), balance: 3 }); await settle()
    assert.equal(pending.length, 1, 'balance refresh must not reload or discard draft')
    assert.equal(input.element.value, '77')
    await wrapper.get('form').trigger('submit'); await settle()
    assert.equal(pending.length, 2); assert.equal(pending[1].config.method, 'put')
    assert.deepEqual(pending[1].payload, { ...spec.body, maxAgeDays: 77 })
    pending[1].resolve(spec.response({ ...spec.body, maxAgeDays: 77 })); await settle()
    assert.match(wrapper.text(), /已保存/)
    user.setStoredCurrentUser(admin(80002)); await settle()
    assert.equal(wrapper.vm.loaded, false); assert.equal(wrapper.vm.form.maxAgeDays, spec.value)
    assert.equal(wrapper.text().includes('已保存'), false)
    assert.equal(pending.length, 3)
    await oldSubmit(); oldInput(999); await settle()
    assert.equal(pending.length, 3, 'old native form cannot save under another administrator')
    assert.equal(wrapper.vm.form.maxAgeDays, spec.value, 'old control cannot edit new draft')
    pending[2].resolve(spec.response(spec.body)); await settle()
    user.setStoredCurrentUser({ ...admin(80002), role: 'user' }); await settle()
    assert.equal(wrapper.find('form').exists(), false)
  })

  test(spec.name + ': invalid identities and disabled administrators cannot read; late reads cannot cross owners', async () => {
    for (const identity of [{ role: 'admin' }, { id: -1, role: 'admin' }, { ...admin(80003), status: 'disabled' }]) {
      user.setStoredCurrentUser(identity); const wrapper = await mounted(spec.Component)
      assert.equal(wrapper.find('form').exists(), false); assert.equal(pending.length, 0)
      wrapper.unmount(); wrappers.pop()
    }
    user.setStoredCurrentUser(admin(80004)); const wrapper = await mounted(spec.Component)
    const first = pending[0]
    user.setStoredCurrentUser(admin(80005)); await settle()
    assert.equal(pending.length, 2)
    first.resolve(spec.response({ ...spec.body, maxAgeDays: 999 })); await settle()
    assert.equal(wrapper.vm.loaded, false); assert.equal(wrapper.vm.busy, true); assert.equal(wrapper.vm.form.maxAgeDays, spec.value)
    pending[1].resolve(spec.response(spec.body)); await settle()
    await wrapper.setProps({ disabled: true }); await settle()
    assert.equal(wrapper.find('form').exists(), false); assert.equal(wrapper.vm.loaded, false)
  })
}

test('sensitive detection confirmation, resource changes, late completions and identity changes retain correct ownership', async () => {
  user.setStoredCurrentUser(admin(80011)); const wrapper = await mounted(Detect, { sha: 'a'.repeat(64) })
  await wrapper.get('button').trigger('click'); await settle()
  const oldClick = wrapper.findComponent(Antd.Button).vm.$.vnode.props.onClick
  assert.equal(confirmations.length, 1); assert.equal(wrapper.vm.busy, true)
  await wrapper.setProps({ sha: 'b'.repeat(64) }); await settle()
  assert.equal(wrapper.vm.busy, false); confirmations[0].resolve({ isConfirmed: true }); await settle()
  assert.equal(detections.length, 0, 'old image confirmation cannot detect another image')
  await oldClick(); await settle(); assert.equal(confirmations.length, 1)
  await wrapper.get('button').trigger('click'); await settle(); confirmations[1].resolve({ isConfirmed: true }); await settle()
  assert.deepEqual(detections[0].payload, { imageSha256: 'b'.repeat(64) })
  user.setStoredCurrentUser(admin(80012)); await settle(); assert.equal(wrapper.vm.busy, false)
  detections[0].resolve({ status: 200, data: { state: 'sensitive' } }); await settle()
  assert.equal(wrapper.vm.feedback, ''); assert.equal(wrapper.emitted('detected'), undefined)
  await wrapper.get('button').trigger('click'); await settle(); confirmations[2].resolve({ isConfirmed: true }); await settle()
  detections[1].resolve({ status: 200, data: { result: { state: 'clean' }, affectedAttachments: 2 } }); await settle()
  assert.match(wrapper.text(), /检测正常/); assert.equal(wrapper.emitted('detected').length, 1)
  await wrapper.setProps({ disabled: true }); await settle()
  assert.equal(wrapper.find('button').exists(), false); assert.equal(wrapper.vm.feedback, '')
})
process.on('exit', () => { axios.defaults.adapter = originalAdapter; Swal.fire = originalFire })
