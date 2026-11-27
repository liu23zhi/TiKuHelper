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
const admin = { id: 89101, username: 'SYNTHETIC_VIEW_ADMIN', nickname: 'SYNTHETIC_ADMIN_NICK', role: 'admin', status: 'enabled' }
const user = { id: 89102, username: 'SYNTHETIC_VIEW_USER', nickname: 'SYNTHETIC_USER_NICK', role: 'user', status: 'enabled' }
const calls = [], pending = [], pages = [], unexpected = [], runtime = [], holds = new Set()
const originalAdapter = axios.defaults.adapter
let refundRecord = null, appealTarget = null

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
    const own = params.scope === 'self' || currentUser.getStoredCurrentUser()?.role !== 'admin'
    result = response(config, { items: [{ id: own ? 71 : 72, subject: own ? 'SYNTHETIC_SELF_ROW' : 'SYNTHETIC_MANAGEMENT_ROW', userName: 'Synthetic', status: 'open', ...(refundRecord ? { category: 'refund', refund: { refundId: refundRecord.id, ...refundRecord } } : {}), updatedAt: '2026-11-19T00:00:00Z' }], total: 1 })
  } else if (/^\/api\/tickets\/\d+$/.test(url) && method === 'get') {
    kind = 'detail'
    const id = Number(url.split('/').pop())
    result = response(config, {
      ticket: { id, subject: 'SYNTHETIC_SELF_DETAIL', status: 'open', ...(refundRecord ? { category: 'refund' } : appealTarget ? { category: 'blacklist_appeal' } : {}), updatedAt: '2026-11-19T00:00:00Z' },
      ...(appealTarget ? { appealTarget } : {}),
      ...(refundRecord ? { refund: { refundId: refundRecord.id, ...refundRecord } } : {}),
      messages: [
        { id: 1, body: 'SYNTHETIC_SELF_BODY', userId: 89102, authorRole: 'user', authorName: 'SYNTHETIC_CURRENT_USER_NICK', createdAt: '2026-11-19T00:00:00Z' },
        { id: 2, body: 'SYNTHETIC_ADMIN_BODY', userId: 89101, authorRole: 'admin', authorName: 'SYNTHETIC_ADMIN_NAME', createdAt: '2026-11-19T01:00:00Z' },
        { id: 3, body: 'SYNTHETIC_SYSTEM_BODY', userId: 89102, authorRole: 'system', authorName: '系统自动创建', createdAt: '2026-11-19T02:00:00Z' },
      ],
      user: { id: 89102, username: 'SYNTHETIC_VIEW_USER', nickname: 'SYNTHETIC_USER_NICK', createdAt: '2026-01-02T03:04:05Z', lastLoginAt: '2026-11-20T20:00:00Z', lastLoginIp: '192.0.2.10' },
      notification: { id: 9, status: 'skipped', lastError: 'SYNTHETIC_ADMIN_ONLY_FAILURE' },
    })
  } else if (url === '/api/tickets/config' && method === 'get') {
    kind = 'config'; result = response(config, { enabled: false, configured: false, mentionedList: [] })
  } else if (url === '/api/tickets' && method === 'post') {
    kind = 'create'; result = response(config, { id: 73 }, 201)
  } else if (url === '/api/tickets/appeal-verification' && method === 'post') {
    kind = 'verify'; result = response(config, { data: { proof: 'a'.repeat(64), expiresAt: new Date(Date.now() + 300000).toISOString(), registeredAt: '2026-01-02T03:04:05Z', maskedPhone: '138****1234' } })
  } else if (url === '/api/ticket-refunds/581' && method === 'get') {
    result = response(config, { ...refundRecord })
  } else if (url === '/api/ticket-refunds/581/decision' && method === 'post') {
    refundRecord = { ...refundRecord, status: data.action === 'approve' ? 'frozen' : 'rejected', approvedCents: refundRecord.requestedCents }
    result = response(config, { ...refundRecord })
  } else if (url === '/api/ticket-refunds/581/process' && method === 'post') {
    refundRecord = { ...refundRecord, status: 'succeeded' }
    result = response(config, { ...refundRecord })
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
    { path: '/tickets/:id?', meta: { viewMode: 'self' }, component: { render: () => null } },
    { path: '/ticket-admin/:id?', meta: { viewMode: 'admin' }, component: { render: () => null } },
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
  calls.length = pending.length = unexpected.length = runtime.length = 0; holds.clear(); refundRecord = null; appealTarget = null
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
  const { wrapper } = await openPage(admin, '/ticket-admin')
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
  assert.deepEqual(listCalls().map(call => call.params), [{ page: 1, pageSize: 20, scope: 'self' }])
  assert.equal(calls.filter(call => call.url === '/api/tickets/config').length, 0)
  await click(button('发起工单'))
  assert.equal(state(wrapper, 'createVisible'), true)
})

test('普通用户工单详情不显示企业微信通知，且请求使用自助视图作用域', async () => {
  const { wrapper } = await openPage(user)
  await click(button('查看详情'))
  assert.equal(state(wrapper, 'detailTicket')?.id, 71)
  assert.equal(calls.find(call => call.url === '/api/tickets/71')?.params?.scope, 'self')
  assert.equal(state(wrapper, 'notification'), null)
  assert.equal([...document.querySelectorAll('.notification-alert')].filter(visible).length, 0)
  const text = [...document.querySelectorAll('.message-item header strong')].filter(visible).map(node => compact(node.textContent))
  assert.ok(text.includes('SYNTHETIC_CURRENT_USER_NICK'), 'server nickname wins over stale local profile')
  assert.ok(!text.includes('SYNTHETIC_USER_NICK'))
  assert.ok(text.includes('管理员'))
  assert.ok(!text.includes('SYNTHETIC_ADMIN_NAME'))
})

test('管理员在工单反馈自助视图也隔离提醒，而工单管理保留提醒', async () => {
  const self = await openPage(admin)
  await click(button('查看详情'))
  assert.equal(calls.find(call => call.url === '/api/tickets/71')?.params?.scope, 'self')
  assert.equal(state(self.wrapper, 'notification'), null)
  assert.equal([...document.querySelectorAll('.notification-alert')].filter(visible).length, 0)
  for (const { wrapper, router } of pages.splice(0)) { wrapper.unmount(); router.options.history.destroy() }
  document.body.replaceChildren(); calls.length = 0

  const management = await openPage(admin, '/ticket-admin')
  await click(button('查看详情'))
  assert.equal(calls.find(call => call.url === '/api/tickets/72')?.params?.scope, undefined)
  assert.equal(state(management.wrapper, 'notification')?.status, 'skipped')
  assert.equal([...document.querySelectorAll('.notification-alert')].filter(visible).length, 1)
  assert.ok(document.querySelector('[data-ticket-user-summary]')?.textContent.includes('SYNTHETIC_USER_NICK'))
  const text = [...document.querySelectorAll('.message-item header strong')].filter(visible).map(node => compact(node.textContent))
  assert.ok(text.includes('SYNTHETIC_USER_NICK（用户ID：89102）'))
  assert.ok(text.includes('管理员：SYNTHETIC_ADMIN_NAME（用户ID：89101）'))
  assert.ok(text.includes('系统自动创建'))
  assert.ok(!text.some(value => value.startsWith('系统自动创建（')))
})

test('管理详情接收顶层退款关联，直接审核和办理，并且全程只显示退款有效状态', async () => {
  refundRecord = { id: 581, kind: 'original', status: 'pending_review', requestedCents: 500, userId: user.id }
  const { wrapper } = await openPage(admin, '/ticket-admin')
  assert.equal(state(wrapper, 'columns').filter(column => column.title === '状态').length, 1)
  assert.equal(state(wrapper, 'columns').some(column => column.key === 'refundStatus'), false)
  await click(button('查看详情'))
  for (let i = 0; i < 40 && !buttons('审核通过').length; i++) { await new Promise(resolve => setTimeout(resolve, 10)); await settle() }
  assert.equal(document.querySelector('[data-ticket-effective-status]')?.textContent.trim(), '待审核')
  assert.equal(document.querySelector('.detail-actions'), null, 'refund state cannot be overridden by a generic ticket action')
  await click(button('审核通过'))
  await click(document.querySelector('[data-refund-action-confirmation] .ant-btn-primary'))
  assert.equal(document.querySelector('[data-ticket-effective-status]')?.textContent.trim(), '资金已冻结，待处理')
  await click(button('提交原路退款'))
  await click(document.querySelector('[data-refund-action-confirmation] .ant-btn-primary'))
  assert.equal(document.querySelector('[data-ticket-effective-status]')?.textContent.trim(), '退款成功')
  assert.equal(state(wrapper, 'tickets')[0].refund.status, 'succeeded', 'refund settlement must immediately refresh the visible list')
  assert.ok(listCalls().length >= 3, 'each refund mutation revalidates the filtered list')
  assert.equal(calls.filter(call => call.url.endsWith('/decision') && call.method === 'post').length, 1)
  assert.equal(calls.filter(call => call.url.endsWith('/process') && call.method === 'post').length, 1)
})

test('封禁账号可通过申诉深链直接打开表单并提交自己的申诉', async () => {
  const { wrapper } = await openPage({ ...user, status: 'banned', banReason: '异常请求',
    createdAt: '2026-04-02T10:30:00Z', phone: '13900001372', wechatId: 'synthetic-private-wechat-id' }, '/tickets?appeal=1')
  assert.equal(state(wrapper, 'createVisible'), true)
  assert.equal(state(wrapper, 'draft').issueType, 'banned')
  const form = [...document.querySelectorAll('[data-ticket-step="form"]')].find(visible)
  assert.ok(form)
  const summary = form.querySelector('[data-ticket-appeal-account]')
  assert.ok(summary)
  assert.equal(buttons('为本账号申诉', summary)[0]?.getAttribute('aria-pressed'), 'true')
  assert.ok(buttons('为其他账号申诉', summary)[0]?.disabled)
  assert.match(summary.textContent, /注册时间.*2026/)
  assert.match(summary.textContent, /绑定手机号.*\*+1372/)
  assert.match(summary.textContent, /微信.*已绑定/)
  assert.doesNotMatch(summary.textContent, /13900001372|synthetic-private-wechat-id/)
  assert.equal([...document.querySelectorAll('input[type=password]')].filter(visible).length, 0)
  await type(form.querySelector('textarea'), '请核对账号封禁情况')
  await click(button('提交', form.closest('.ant-modal')))
  assert.equal(createCalls().length, 1)
  assert.equal(createCalls()[0].data.accountIssue, 'ban_appeal')
  assert.equal(createCalls()[0].data.appealProof, undefined)
})

test('用户通过独立验证为另一个封禁账号申诉，不替换登录身份', async () => {
  const before = { ...user }
  await openPage(user, '/tickets?appeal=1')
  const summary = document.querySelector('[data-ticket-appeal-account]')
  assert.equal(buttons('为本账号申诉', summary)[0]?.getAttribute('aria-pressed'), 'true')
  assert.equal([...document.querySelectorAll('input[type=password]')].filter(visible).length, 0)
  assert.match(summary.textContent, /本账号未封禁/)
  const initialForm = [...document.querySelectorAll('[data-ticket-step="form"]')].find(visible)
  await type(initialForm.querySelector('textarea'), '尝试为本账号提交封禁申诉')
  await click(button('提交', initialForm.closest('.ant-modal')))
  assert.equal(createCalls().length, 0, 'non-eligible self selection does not submit')
  assert.equal([...document.querySelectorAll('input[type=password]')].filter(visible).length, 0, 'self submit never opens the other-account form')
  assert.equal(calls.filter(call => call.url === '/api/tickets/appeal-verification').length, 0)
  await click(button('为其他账号申诉', summary))
  for (let i = 0; i < 30 && !buttons('验证账号').length; i++) { await new Promise(resolve => setTimeout(resolve, 10)); await settle() }
  const verification = document.querySelector('.ant-modal input[type=password]')?.closest('.ant-modal')
  assert.ok(verification)
  await type(verification.querySelector('input:not([type=password])'), 'SYNTHETIC_TARGET')
  await type(verification.querySelector('input[type=password]'), 'synthetic-password')
  await click(button('验证账号', verification)); await settle()
  const form = [...document.querySelectorAll('[data-ticket-step="form"]')].find(visible)
  assert.match(form.textContent, /138\*\*\*\*1234/)
  assert.match(form.textContent, /2026/)
  assert.deepEqual(currentUser.getStoredCurrentUser(), before)
  await type(form.querySelector('textarea'), '为已验证账号提交封禁申诉')
  await click(button('提交', form.closest('.ant-modal')))
  assert.equal(createCalls()[0].data.accountIssue, 'ban_appeal')
  assert.equal(createCalls()[0].data.appealProof, 'a'.repeat(64))
  assert.equal(calls.filter(call => /login|logout/.test(call.url)).length, 0)
})

test('申诉返回类型选择后销毁验证结果，迟到的验证不能带回旧目标', async () => {
  const { wrapper } = await openPage(user, '/tickets?appeal=1')
  await click(button('为其他账号申诉'))
  state(wrapper, 'onAppealVerified')({ proof: 'a'.repeat(64), expiresAt: new Date(Date.now()+300000).toISOString() })
  await settle(); assert.ok(state(wrapper, 'appealVerification'))
  await click(button('上一步'))
  assert.equal(state(wrapper, 'appealVerification'), null)
  state(wrapper, 'onAppealVerified')({ proof: 'b'.repeat(64) }); await settle()
  assert.equal(state(wrapper, 'appealVerification'), null)
  assert.equal(createCalls().length, 0)
})

test('取消主动进入的其他账号验证后恢复本账号，迟到结果不改变目标', async () => {
  const { wrapper } = await openPage(user, '/tickets?appeal=1')
  await click(button('为其他账号申诉'))
  for (let i = 0; i < 30 && !buttons('验证账号').length; i++) { await new Promise(resolve => setTimeout(resolve, 10)); await settle() }
  const verification = document.querySelector('.ant-modal input[type=password]')?.closest('.ant-modal')
  assert.ok(verification)
  await click(button('取消', verification))
  const summary = document.querySelector('[data-ticket-appeal-account]')
  assert.equal(buttons('为本账号申诉', summary)[0]?.getAttribute('aria-pressed'), 'true')
  assert.equal([...document.querySelectorAll('input[type=password]')].filter(visible).length, 0)
  state(wrapper, 'onAppealVerified')({ proof: 'a'.repeat(64), expiresAt: new Date(Date.now() + 300000).toISOString() })
  assert.equal(state(wrapper, 'appealVerification'), null)
  assert.equal(calls.filter(call => call.method === 'post').length, 0)
})

test('管理员申诉详情展示目标状态和完整业务资料，处理面板指向该目标', async () => {
  appealTarget = { id: 992, username: 'SYNTHETIC_TARGET', role: 'user', status: 'banned', banned: true, banReason: '异常请求', phone: '13800131234', createdAt: '2026-01-02T03:04:05Z', balance: 5, giftBalance: 2 }
  await openPage(admin, '/ticket-admin'); await click(button('查看详情')); await settle()
  for (let i = 0; i < 30 && !document.querySelector('[data-ticket-appeal-target] .ant-descriptions'); i++) { await new Promise(resolve => setTimeout(resolve, 10)); await settle() }
  const target = document.querySelector('[data-ticket-appeal-target]')
  assert.match(target.textContent, /已封禁.*异常请求/)
  assert.match(target.textContent, /13800131234/)
  assert.match(target.textContent, /充值本金/)
  assert.match(document.querySelector('.ticket-account-actions').textContent, /992/)
})

test('账号问题默认选中登录异常，且可以选择其它问题', async () => {
  const { wrapper } = await openPage(user)
  await click(button('发起工单'))
  await click(document.querySelector('[data-ticket-type-option="account"]'))
  await click(button('下一步'))
  const form = [...document.querySelectorAll('[data-ticket-step="form"]')].find(visible)
  assert.ok(form)
  assert.equal(state(wrapper, 'draft').issueType, 'login')
  assert.match(compact(form.querySelector('.ant-select-selection-item')?.textContent), /登录异常/)
  const select = form.querySelector('input[role="combobox"]')
  assert.ok(select)
  select.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
  select.click()
  await settle()
  const option = [...document.querySelectorAll('.ant-select-item-option')].find(node => visible(node) && compact(node.textContent) === '其它问题')
  assert.ok(option)
  option.click()
  await settle()
  assert.equal(state(wrapper, 'draft').issueType, 'other')
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

  await router.push('/ticket-admin'); await settle()
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
