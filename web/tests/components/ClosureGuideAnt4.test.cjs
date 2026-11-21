const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
require.extensions['.css'] = module => { module.exports = {} }

const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const originalApi = require.cache[apiPath]
const calls = []
let status = null
let failStatus = false
const api = new Module(apiPath)
api.loaded = true
api.exports = new Proxy({}, {
  get(_, name) {
    if (name === '__esModule') return false
    return (...args) => {
      calls.push({ name, args })
      if (name !== 'getAccountClosureStatus') throw new Error('Unexpected API write: ' + String(name))
      return failStatus ? Promise.reject(new Error('SYNTHETIC_STATUS_UNAVAILABLE')) : Promise.resolve({ data: { ...status } })
    }
  },
})
require.cache[apiPath] = api
const ClosureGuide = require(path.join(web, 'src/pages/Tickets/ClosureGuide.vue'))
const wrappers = []

async function settle() {
  await nextTick()
  await flushPromises()
  await new Promise(resolve => setTimeout(resolve, 10))
  await nextTick()
  await flushPromises()
}

async function openGuide() {
  const wrapper = mount(ClosureGuide, { props: { mode: 'guide' }, attachTo: document.body, global: { plugins: [Antd] } })
  wrappers.push(wrapper)
  await settle()
  return wrapper
}

afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  await settle()
  Antd.message.destroy()
  document.body.replaceChildren()
  calls.length = 0
  status = null
  failStatus = false
})
after(() => {
  if (originalApi) require.cache[apiPath] = originalApi
  else delete require.cache[apiPath]
})

test('无绑定手机号时隐藏自助注销承诺并直接提供管理员工单入口', async () => {
  status = {
    status: 'enabled', pending: false, phoneBound: false, eligible: false,
    ineligibleReason: '账号没有绑定手机号，不支持自助注销，请提交工单由管理员处理',
  }
  const wrapper = await openGuide()
  assert.equal(calls.length, 1)
  assert.match(wrapper.text(), /当前账号不支持自助注销/)
  assert.match(wrapper.text(), /没有绑定手机号/)
  assert.match(wrapper.text(), /请提交工单由管理员处理/)
  assert.equal(wrapper.find('.closure-benefits').exists(), false)
  assert.equal(wrapper.find('[data-closure-action="self-service"]').exists(), false)
  const fallback = wrapper.find('[data-closure-action="fallback"]')
  assert.ok(fallback.exists())
  assert.match(fallback.text(), /提交注销工单/)
  await fallback.trigger('click')
  await settle()
  assert.equal(wrapper.emitted('fallback')?.length, 1)
  assert.equal(calls.length, 1, 'continuing with the fallback creates no self-service mutation')
})

test('有绑定手机号且符合条件时才展示自助注销建议和效果说明', async () => {
  status = { status: 'enabled', pending: false, phoneBound: true, eligible: true }
  const wrapper = await openGuide()
  assert.match(wrapper.text(), /推荐使用自助注销/)
  assert.match(wrapper.text(), /立即生效，无需等待管理员处理/)
  assert.match(wrapper.text(), /等待期内可一键撤销/)
  assert.ok(wrapper.find('[data-closure-action="self-service"]').exists())
  assert.ok(wrapper.find('[data-closure-action="fallback"]').exists())
})

test('已在注销等待期时优先显示现有申请状态，即使手机号后来解绑', async () => {
  status = { status: 'closure_pending', pending: true, phoneBound: false, eligible: false, remainingDays: 4, waitDays: 7 }
  const wrapper = await openGuide()
  assert.match(wrapper.text(), /已提交自助注销申请/)
  assert.match(wrapper.text(), /注销等待期/)
  assert.doesNotMatch(wrapper.text(), /当前账号不支持自助注销/)
  assert.equal(wrapper.find('.closure-benefits').exists(), false)
  assert.ok(wrapper.find('[data-closure-action="self-service"]').exists())
})

test('资格查询失败时不展示自助注销保证，并提供重试和工单入口', async () => {
  failStatus = true
  const wrapper = await openGuide()
  assert.match(wrapper.text(), /暂时无法确认自助注销资格/)
  assert.doesNotMatch(wrapper.text(), /立即生效，无需等待管理员处理/)
  assert.equal(wrapper.find('.closure-benefits').exists(), false)
  const fallback = wrapper.find('[data-closure-action="fallback"]')
  assert.match(fallback.text(), /提交注销工单/)
  failStatus = false
  status = { status: 'enabled', pending: false, phoneBound: false, eligible: false, ineligibleReason: '无手机号' }
  await wrapper.find('.ant-alert button').trigger('click')
  await settle()
  assert.equal(calls.length, 2)
  assert.match(wrapper.text(), /当前账号不支持自助注销/)
  assert.equal(wrapper.find('.closure-benefits').exists(), false)
})
