const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises, config } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const axios = require('axios')
const { registerConsoleUI } = require('../../src/registerUI.js')
const { setStoredCurrentUser, clearStoredCurrentUser } = require('../../src/utils/currentUser.js')

const base = '/api/admin/review-rebate'
const originalAdapter = axios.defaults.adapter
const row = { id: 42, status: 'pending_review', userId: 102, source: 'edge' }
const admin = { id: 101, role: 'admin', status: 'enabled' }
const note = '合成审核备注'
for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) delete config.global.components[name]

const reads = []
const decisions = []
const unexpected = []
let wrapper, router, status, finishRefresh, pauseRefresh
axios.defaults.adapter = async request => {
  const reply = (status, data) => ({ status, data, config: request })
  if (request.method === 'get') {
    if (request.url === base + '/programs') return reply(200, { data: [{ id: 1, source: 'edge' }] })
    if (request.url === base + '/rules') return reply(200, { data: [] })
    if (request.url === base + '/claims') {
      reads.push(request.params)
      if (pauseRefresh && decisions.length) {
        await new Promise(resolve => { finishRefresh = resolve })
        return reply(200, { total: 1, data: [{ ...row, status: 'rejected' }] })
      }
      return reply(200, { total: 1, data: [row] })
    }
  } else if (request.method === 'post' && request.url === base + '/claims/42/decision') {
    decisions.push(JSON.parse(request.data))
    return reply(status, { message: 'synthetic fixture only' })
  }
  unexpected.push([request.method, request.url])
  throw new Error('No request may leave the owned fixture')
}

const Review = require('../../src/pages/ReviewRebateAdmin/index.vue')
const dialog = () => document.querySelector('[role="dialog"]')
const submit = () => [...(dialog()?.querySelectorAll('button') ?? [])].find(button => button.textContent.trim() === '提交结论')

async function settle() {
  await nextTick()
  await flushPromises()
  await nextTick()
}

async function openReject() {
  setStoredCurrentUser(admin)
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/review-rebate-admin', component: Review }] })
  await router.push('/review-rebate-admin')
  wrapper = mount(Review, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] } })
  await settle()
  await wrapper.get('input[placeholder="精确账户 ID"]').setValue('102')
  await wrapper.get('form.claim-filter-form').trigger('submit')
  await settle()
  assert.deepEqual(reads.at(-1), { userId: '102' })

  const reject = wrapper.get('tr[data-row-key="42"]').findAll('button').find(button => button.text() === '拒绝')
  assert.ok(reject)
  await reject.trigger('click')
  await settle()
  assert.match(dialog()?.textContent || '', /拒绝这条评价返现/)
  const input = dialog().querySelector('textarea')
  input.value = note
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  assert.equal(submit()?.disabled, false)
}

afterEach(async () => {
  finishRefresh?.()
  wrapper?.unmount()
  router?.options.history.destroy()
  clearStoredCurrentUser()
  await settle()
  document.body.replaceChildren()
  wrapper = router = finishRefresh = undefined
  reads.length = decisions.length = 0
  assert.deepEqual(unexpected.splice(0), [])
})
after(() => { axios.defaults.adapter = originalAdapter })

test('a confirmed review conflict retires the stale confirmation before refreshing the applied filter', async () => {
  status = 409
  pauseRefresh = true
  await openReject()
  submit().click()
  await settle()
  assert.deepEqual(decisions, [{ action: 'reject', note }])
  assert.equal(typeof finishRefresh, 'function', '409 must refresh the authenticated list')
  assert.deepEqual(reads.at(-1), { userId: '102' })
  console.log('T09_CONFLICT_DIALOG_DURING_REFRESH=' + Boolean(dialog()))
  assert.equal(dialog(), null, 'server-confirmed state change must retire the stale approval/rejection intent immediately')
  assert.equal(wrapper.vm.decisionForm.note, '')
  finishRefresh()
  await settle()
  assert.match(wrapper.text(), /该提交状态已变更/)
  assert.match(wrapper.get('tr[data-row-key="42"]').text(), /已拒绝/)
  assert.equal(wrapper.get('tr[data-row-key="42"]').findAll('button').length, 0)
  assert.equal(decisions.length, 1, 'refresh only, never repeat the decision')
})

test('a non-conflict failure keeps the same confirmation and draft for the administrator', async () => {
  status = 422
  pauseRefresh = false
  await openReject()
  const readCount = reads.length
  submit().click()
  await settle()
  assert.deepEqual(decisions, [{ action: 'reject', note }])
  assert.equal(reads.length, readCount)
  assert.match(dialog()?.textContent || '', /拒绝这条评价返现/)
  assert.equal(dialog()?.querySelector('textarea')?.value, note)
  assert.equal(submit()?.disabled, false)
})
