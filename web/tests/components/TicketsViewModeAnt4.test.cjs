/* Real Vue/Antd routes and production HTTP helpers; every request uses memory. */
const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
require.extensions['.css'] = module => { module.exports = {} }
for (const name of ['HTMLInputElement', 'HTMLTextAreaElement', 'HTMLButtonElement', 'FileReader', 'File']) global[name] = window[name]
global.FormData = window.FormData
global.getComputedStyle = element => window.getComputedStyle(element)

const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createMemoryHistory, createRouter } = require('vue-router')
const Antd = require('ant-design-vue')
const axios = require('axios')
const currentUser = require('../../src/utils/currentUser.js')
const admin = { id: 89101, username: 'SYNTHETIC_VIEW_ADMIN', role: 'admin', status: 'enabled' }
const user = { id: 89102, username: 'SYNTHETIC_VIEW_USER', role: 'user', status: 'enabled' }
const calls = [], pending = [], pages = [], unexpected = [], runtime = [], holds = new Set()
const originalAdapter = axios.defaults.adapter

function response(config, data, status = 200) { return { status, data, config, headers: {}, request: { memoryOnly: true } } }
// Install before importing Tickets, whose request module creates an Axios instance.
axios.defaults.adapter = config => {
  const method = String(config.method || 'get').toLowerCase(), url = String(config.url)
  const data = typeof config.data === 'string' ? JSON.parse(config.data) : config.data
  const params = { ...(config.params || {}) }
  calls.push({ method, url, params, data })
  let kind, result
  if (url === '/api/tickets' && method === 'get') {
    kind = params.scope === 'self' ? 'self-list' : 'management-list'
    const own = params.scope === 'self'
    result = response(config, { items: [{ id: own ? 71 : 72, subject: own ? 'SYNTHETIC_SELF_ROW' : 'SYNTHETIC_MANAGEMENT_ROW', userName: 'Synthetic', status: 'open', updatedAt: '2026-11-19T00:00:00Z' }], total: 1 })
  } else if (url === '/api/tickets/config' && method === 'get') {
    kind = 'config'; result = response(config, { enabled: false, configured: false, mentionedList: [] })
  } else if (url === '/api/tickets' && method === 'post') {
    kind = 'create'; result = response(config, { id: 73 }, 201)
  } else {
    unexpected.push(`${method} ${url}`)
    return Promise.reject(new Error('Memory fixture denies unmatched HTTP'))
  }
  if (holds.has(kind)) return new Promise(resolve => pending.push({ kind, resolve, result, done: false }))
  return Promise.resolve(result)
}
const Tickets = require('../../src/pages/Tickets/index.vue')
const compact = value => String(value || '').replace(/\s+/g, '')
function visible(node) {
  if (!node?.isConnected) return false
  for (let el = node; el?.nodeType === 1; el = el.parentElement) {
    if (el.hidden || el.getAttribute('aria-hidden') === 'true' || window.getComputedStyle(el).display === 'none') return false
  }
  return true
}
function buttons(label, root = document.body) { return [...root.querySelectorAll('button')].filter(node => visible(node) && compact(node.textContent) === compact(label)) }
function button(label, root = document.body) {
  const matches = buttons(label, root)
  assert.equal(matches.length, 1, `one visible button: ${label}`)
  assert.equal(matches[0].disabled, false, `enabled button: ${label}`)
  return matches[0]
}
async function settle() { await nextTick(); await flushPromises(); await nextTick(); await flushPromises() }
async function click(node) { assert.ok(node && !node.disabled); node.click(); await settle() }
async function type(node, value) {
  assert.ok(node && !node.disabled)
  node.value = value
  node.dispatchEvent(new Event('input', { bubbles: true }))
  node.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
}
function state(wrapper, key) { return wrapper.vm.$.setupState[key] }
function listCalls() { return calls.filter(call => call.url === '/api/tickets' && call.method === 'get') }
function createCalls() { return calls.filter(call => call.url === '/api/tickets' && call.method === 'post') }
function release(kind) {
  for (const item of pending) if (!item.done && (!kind || item.kind === kind)) { item.done = true; item.resolve(item.result) }
}
async function openPage(actor, route = '/tickets') {
  currentUser.setStoredCurrentUser(actor)
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/tickets', meta: { viewMode: 'self' }, component: { render: () => null } },
    { path: '/admin/tickets', meta: { viewMode: 'admin' }, component: { render: () => null } },
  ] })
  await router.push(route); await router.isReady()
  const wrapper = mount(Tickets, { attachTo: document.body, global: { plugins: [Antd, router], config: { errorHandler: error => runtime.push(error) } } })
  pages.push({ wrapper, router }); await settle()
  return { wrapper, router }
}
afterEach(async () => {
  for (const { wrapper, router } of pages.splice(0)) { wrapper.unmount(); router.options.history.destroy() }
  release(); await settle()
  Antd.message.destroy(); Antd.Modal.destroyAll()
  currentUser.clearStoredCurrentUser(); window.localStorage.clear()
  document.body.replaceChildren(); await settle()
  assert.deepEqual(unexpected, [], 'HTTP never leaves the memory fixture')
  assert.deepEqual(runtime, [], 'no Vue runtime errors')
  assert.ok(pending.every(item => item.done), 'all deferred HTTP settled')
  calls.length = pending.length = unexpected.length = runtime.length = 0; holds.clear()
})
after(() => { axios.defaults.adapter = originalAdapter })

test('管理员工单反馈可发起，只读取本人列表，不显示提醒设置', async () => {
  const { wrapper } = await openPage(admin)
  assert.equal(buttons('发起工单').length, 1)
  assert.equal(buttons('提醒设置').length, 0)
  assert.deepEqual(listCalls().map(call => call.params), [{ page: 1, pageSize: 20, scope: 'self' }])
  assert.equal(calls.filter(call => call.url === '/api/tickets/config').length, 0)
  await click(button('发起工单'))
  assert.equal(state(wrapper, 'createVisible'), true)
  assert.ok([...document.querySelectorAll('[data-ticket-step="type"]')].some(visible))
})

test('管理员工单管理没有发起入口，直接调用打开方法也保持关闭', async () => {
  const { wrapper } = await openPage(admin, '/admin/tickets')
  assert.equal(buttons('发起工单').length, 0)
  assert.equal(buttons('提醒设置').length, 1)
  assert.deepEqual(listCalls().map(call => call.params), [{ page: 1, pageSize: 20 }])
  assert.equal(calls.filter(call => call.url === '/api/tickets/config').length, 1)
  state(wrapper, 'openCreate')(); await settle()
  assert.equal(state(wrapper, 'createVisible'), false)
  assert.equal([...document.querySelectorAll('[data-ticket-step="type"]')].filter(visible).length, 0)
  assert.equal(createCalls().length, 0)
})

test('普通用户工单反馈保留发起入口，列表不附加管理员scope参数', async () => {
  const { wrapper } = await openPage(user)
  assert.equal(buttons('发起工单').length, 1)
  assert.equal(buttons('提醒设置').length, 0)
  assert.deepEqual(listCalls().map(call => call.params), [{ page: 1, pageSize: 20 }])
  assert.equal(calls.filter(call => call.url === '/api/tickets/config').length, 0)
  await click(button('发起工单'))
  assert.equal(state(wrapper, 'createVisible'), true)
})

test('管理员切换管理路由关闭创建并使旧列表、创建请求和回调失效', async () => {
  holds.add('self-list'); holds.add('management-list'); holds.add('create')
  const { wrapper, router } = await openPage(admin)
  await click(button('发起工单'))
  const oldCreateOwner = state(wrapper, 'captureCreate')(), queuedOpen = state(wrapper, 'openCreate')
  await click(document.querySelector('[data-ticket-type-option="general"]'))
  await click(button('下一步'))
  const form = [...document.querySelectorAll('[data-ticket-step="form"]')].find(visible)
  assert.ok(form, 'actual general ticket form')
  await type(form.querySelector('input'), 'SYNTHETIC_PENDING_TICKET')
  await type(form.querySelector('textarea'), 'SYNTHETIC_PENDING_BODY')
  await click(button('提交', form.closest('.ant-modal')))
  assert.equal(createCalls().length, 1, 'one authorized self-view creation was dispatched')
  assert.equal(state(wrapper, 'createLoading'), true)

  await router.push('/admin/tickets'); await settle()
  assert.equal(state(wrapper, 'createVisible'), false)
  assert.equal(oldCreateOwner(), false, 'old create ownership is retired on view switch')
  queuedOpen(); await settle()
  assert.equal(state(wrapper, 'createVisible'), false, 'queued open cannot create in management view')
  assert.equal(buttons('发起工单').length, 0)
  assert.deepEqual(listCalls().map(call => call.params), [{ page: 1, pageSize: 20, scope: 'self' }, { page: 1, pageSize: 20 }])
  assert.equal(state(wrapper, 'listLoading'), true)

  release('self-list'); await settle()
  assert.equal(state(wrapper, 'listLoading'), true, 'old finally cannot release current management loading')
  assert.equal(state(wrapper, 'tickets').length, 0, 'old self rows cannot publish into management view')
  release('management-list'); await settle()
  assert.equal(state(wrapper, 'tickets')[0].subject, 'SYNTHETIC_MANAGEMENT_ROW')
  const beforeCompletion = calls.length
  release('create'); await settle()
  assert.equal(calls.length, beforeCompletion, 'old create completion cannot trigger a continuation list read')
  assert.equal(state(wrapper, 'createVisible'), false)
  assert.equal(state(wrapper, 'tickets')[0].subject, 'SYNTHETIC_MANAGEMENT_ROW')
  assert.equal([...document.querySelectorAll('.ant-message-success')].some(node => /工单已提交/.test(node.textContent)), false, 'retired create success stays silent')
})
