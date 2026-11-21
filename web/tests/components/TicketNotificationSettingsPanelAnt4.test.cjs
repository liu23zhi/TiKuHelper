const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
require('../helpers/vue-dom.cjs')
require.extensions['.css'] = module => { module.exports = {} }
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const axios = require('axios')
const user = require('../../src/utils/currentUser.js')
const calls = [], notices = []
let deferredRead = null, wrappers = []
const originalAdapter = axios.defaults.adapter
axios.defaults.adapter = config => {
  const method = String(config.method || 'get').toLowerCase()
  const payload = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  calls.push({ method, url: config.url, payload })
  const response = { status: 200, headers: {}, config, data: { enabled: true, configured: true, mentionedList: ['ops'] } }
  if (method === 'get' && deferredRead) return new Promise(resolve => { deferredRead.resolve = () => resolve(response) })
  assert.ok(['/api/tickets/config', '/api/tickets/config/test'].includes(config.url), 'only existing ticket config API')
  return Promise.resolve(response)
}
const Panel = require(path.resolve(__dirname, '../../src/components/TicketNotificationSettingsPanel.vue'))
const originalSuccess = Antd.message.success
Antd.message.success = value => { notices.push(value) }
const actor = id => ({ id, username: `test-${id}`, role: 'admin', status: 'enabled' })
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function mounted() {
  user.setStoredCurrentUser(actor(78001))
  const wrapper = mount(Panel, { attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(wrapper); await settle(); return wrapper
}
function button(wrapper, text) { const result = wrapper.findAll('button').find(item => item.text() === text); assert.ok(result, text); return result }
afterEach(async () => {
  for (const wrapper of wrappers) wrapper.unmount()
  if (deferredRead?.resolve) deferredRead.resolve()
  deferredRead = null; wrappers = []; await settle(); calls.length = notices.length = 0
  user.clearStoredCurrentUser(); document.body.replaceChildren(); Antd.message.destroy()
})
test('shared notification panel loads on demand and saves through the existing ticket config API', async () => {
  const wrapper = await mounted()
  assert.equal(calls.length, 0, 'no hidden notification request while panel is collapsed')
  await button(wrapper, '配置提醒').trigger('click'); await settle()
  assert.deepEqual(calls.map(item => item.method), ['get'])
  await wrapper.get('input[placeholder="输入新地址以替换已保存地址"]').setValue('https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=synthetic')
  await wrapper.get('form').trigger('submit'); await settle()
  const save = calls.find(item => item.method === 'put')
  assert.deepEqual(save.payload, { enabled: true, clearWebhook: false, mentionedList: ['ops'], webhookUrl: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=synthetic' })
  assert.equal(wrapper.get('input[placeholder="输入新地址以替换已保存地址"]').element.value, '', 'saved webhook secret is scrubbed')
  await button(wrapper, '发送测试消息').trigger('click'); await settle()
  assert.ok(calls.some(item => item.method === 'post' && item.url === '/api/tickets/config/test'))
})
test('notification panel retires exposed fields and old callbacks on administrator change', async () => {
  const wrapper = await mounted()
  await button(wrapper, '配置提醒').trigger('click'); await settle()
  await wrapper.get('input[placeholder="输入新地址以替换已保存地址"]').setValue('SYNTHETIC_UNSAVED_WEBHOOK')
  const oldSubmit = wrapper.findComponent(Antd.Form).vm.$.vnode.props.onFinish
  const count = calls.length
  user.setStoredCurrentUser(actor(78002)); await settle()
  assert.equal(wrapper.text().includes('SYNTHETIC_UNSAVED_WEBHOOK'), false)
  assert.equal(wrapper.find('form').exists(), false, 'new administrator must explicitly reopen settings')
  await oldSubmit(); await settle()
  assert.equal(calls.length, count, 'retired native callback must not write under a different account')
})
test('late notification reads cannot reopen or populate another administrator surface', async () => {
  deferredRead = {}
  const wrapper = await mounted()
  await button(wrapper, '配置提醒').trigger('click'); await settle()
  const resolve = deferredRead.resolve
  user.setStoredCurrentUser(actor(78003)); await settle()
  resolve(); await settle()
  assert.equal(wrapper.find('form').exists(), false)
  assert.equal(notices.length, 0)
})
process.on('exit', () => { axios.defaults.adapter = originalAdapter; Antd.message.success = originalSuccess })
