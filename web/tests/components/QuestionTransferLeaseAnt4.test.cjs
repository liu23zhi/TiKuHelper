const { test } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const axios = require('axios')
const user = require('../../src/utils/currentUser.js')

test('question task history sends no ordinary-user request and retires a pending administrator response', async () => {
  const pending = []
  const originalAdapter = axios.defaults.adapter
  axios.defaults.adapter = config => {
    assert.equal(config.method, 'get')
    assert.equal(config.url, '/api/export/tasks')
    return new Promise(resolve => pending.push(data => resolve({ config, status: 200, headers: {}, data })))
  }
  const Panel = require('../../src/pages/TikuComponent/QuestionTransferPanel.vue')
  const ConsolePanel = require('../../src/components/ConsolePanel.vue')
  const settle = async () => { await flushPromises(); await nextTick() }
  const admin = id => ({ id, role: 'admin', status: 'enabled' })
  user.setStoredCurrentUser({ id: 5050, role: 'user', status: 'enabled' })
  const wrapper = mount(Panel, { global: { plugins: [Antd], components: { ConsolePanel } } })
  try {
    await settle()
    assert.equal(pending.length, 0)
    assert.equal(wrapper.find('table').exists(), false)
    user.setStoredCurrentUser(admin(5051)); await settle()
    assert.equal(pending.length, 1)
    user.setStoredCurrentUser(admin(5052)); await settle()
    assert.equal(pending.length, 2)
    pending[0]({ data: [{ taskId: 'old-admin-1', type: 'export', format: 'csv', status: 'succeeded' }] }); await settle()
    assert.doesNotMatch(wrapper.text(), /CSV|csv/)
    pending[1]({ data: [{ taskId: 'new-admin-2', type: 'export', format: 'json', status: 'succeeded' }] }); await settle()
    assert.match(wrapper.text(), /json/)
    assert.doesNotMatch(wrapper.text(), /CSV|csv/)
  } finally {
    wrapper.unmount()
    user.clearStoredCurrentUser()
    axios.defaults.adapter = originalAdapter
  }
})
