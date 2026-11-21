const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { config, mount, flushPromises } = require('@vue/test-utils')
const { createRouter, createMemoryHistory } = require('vue-router')
const axios = require('axios')
const { registerConsoleUI } = require('../../src/registerUI.js')
const { setStoredCurrentUser, clearStoredCurrentUser } = require('../../src/utils/currentUser.js')

for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) delete config.global.components[name]
globalThis.getComputedStyle = window.getComputedStyle

const calls = []
let claims
let postStatus
let stores
let page
let history
const base = '/api/user/review-rebate'
axios.defaults.adapter = async config => {
  const operation = `${config.method} ${config.url}`
  calls.push({ operation, data: config.data, source: config.params?.source })
  let status = 200
  let data
  switch (operation) {
    case `get ${base}/program`:
      data = {
        program: { id: 4, enabled: true, name: '商店评价', storeUrl: 'https://example.com/store', maxClaimsPerUser: 3, maxAmountPerUser: 2, minAgentConfidence: 0.9 },
        rules: [{ id: 10, minStars: 5, maxStars: 5, minContentScore: 8, maxContentScore: 10, priority: 1, amount: 2, remark: 'INTERNAL_RULE' }],
        usage: { approvedCount: 0, approvedAmount: 0, rejectedCount: 0 },
      }
      if (stores.length) {
        const selected = stores.find(store => store.source === config.params?.source) || stores[0]
        Object.assign(data.program, selected)
        data.stores = stores
        data.canClaim = selected.canClaim
      }
      break
    case `get ${base}/claims`:
      data = { data: claims, total: claims.length, page: 1, pageSize: 20 }
      break
    case `post ${base}/claims`:
      status = postStatus
      data = { id: 41, status: 'submitted', createdAt: '2026-11-21T10:00:00Z', agentConfidence: 0.01, matchedRuleId: 10 }
      claims = [data]
      break
    default:
      throw new Error(`Unexpected fixture operation ${operation}`)
  }
  return { status, data, headers: {}, config, request: { memoryOnly: true } }
}

const ReviewRebate = require('../../src/pages/ReviewRebate/index.vue')

async function open() {
  setStoredCurrentUser({ id: 73, role: 'user', status: 'enabled' })
  history = createMemoryHistory()
  const router = createRouter({ history, routes: [{ path: '/review-rebate', component: ReviewRebate }] })
  await router.push('/review-rebate')
  page = mount(ReviewRebate, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] } })
  await flushPromises()
  return page
}

async function selectScreenshot() {
  const input = page.get('input[type="file"]')
  const file = new window.File(['screenshot'], 'review.png', { type: 'image/png' })
  Object.defineProperty(input.element, 'files', { configurable: true, value: [file] })
  await input.trigger('change')
  await flushPromises()
  return file
}

const originallyFormData = globalThis.FormData
afterEach(() => {
  page?.unmount()
  history?.destroy()
  clearStoredCurrentUser()
  document.body.replaceChildren()
  globalThis.FormData = originallyFormData
  calls.length = 0
  claims = []
  postStatus = 201
  stores = []
})
claims = []
postStatus = 201
stores = []

test('Ant4 submit sends the selected screenshot in multipart and refreshes the activity and claims', async () => {
  globalThis.FormData = window.FormData
  await open()
  const file = await selectScreenshot()
  await page.get('input[type="url"]').setValue('https://example.com/review/41')
  const submit = page.get('[data-submit-button]')
  assert.equal(submit.element.disabled, false)
  submit.element.click()
  await flushPromises()

  const submitCall = calls.find(call => call.operation === `post ${base}/claims`)
  assert.ok(submitCall, 'the native submit button must reach the claims API through Ant Form')
  assert.equal(submitCall.data.get('file'), file)
  assert.equal(submitCall.data.get('claimedReviewUrl'), 'https://example.com/review/41')
  assert.deepEqual(calls.map(call => call.operation), [
    `get ${base}/program`, `get ${base}/claims`, `post ${base}/claims`, `get ${base}/program`, `get ${base}/claims`,
  ])
  assert.match(page.text(), /已提交/)
  assert.equal(page.get('input[type="url"]').element.value, '')
  assert.equal(submit.element.disabled, true)
})

test('duplicate submission retains its warning and refreshes the prior claim', async () => {
  globalThis.FormData = window.FormData
  postStatus = 409
  await open()
  await selectScreenshot()
  page.get('[data-submit-button]').element.click()
  await flushPromises()
  assert.match(page.text(), /该评价可能已提交过/)
  assert.match(page.text(), /41/)
  assert.equal(calls.filter(call => call.operation === `get ${base}/claims`).length, 2)
})

test('the screenshot picker uses a visible button and a hidden file input', async () => {
  await open()
  const picker = page.findAll('button').find(button => /选择截图/.test(button.text()))
  assert.ok(picker)
  assert.equal(picker.element.disabled, false)
  assert.equal(window.getComputedStyle(page.get('input[type="file"]').element).display, 'none')
})

test('the user page shows claim progress without rule, scoring or internal amounts', async () => {
  claims = [{ id: 41, status: 'submitted', agentConfidence: 0.01, matchedRuleId: 10 }]
  await open()
  assert.match(page.text(), /已提交/)
  assert.doesNotMatch(page.text(), /当前规则|INTERNAL_RULE|置信度|命中规则|内容分|每账号可领金额|0\.01/)
})

test('switching stores uses the selected store eligibility and sends its source', async () => {
  globalThis.FormData = window.FormData
  stores = [
    { source: 'edge', name: 'Edge 扩展', storeUrl: 'https://example.com/edge', canClaim: false },
    { source: 'chrome', name: 'Chrome 扩展', storeUrl: 'https://example.com/chrome', canClaim: true },
  ]
  await open()
  assert.ok(page.get('[data-submit-button]').element.disabled)
  assert.match(page.text(), /领取上限/)
  const chrome = page.findAll('input[type="radio"]').find(input => input.element.value === 'chrome')
  assert.ok(chrome)
  await chrome.setValue(true)
  await flushPromises()
  assert.match(page.text(), /Chrome 扩展/)
  await selectScreenshot()
  page.get('[data-submit-button]').element.click()
  await flushPromises()
  assert.equal(calls.find(call => call.operation === `post ${base}/claims`)?.data.get('source'), 'chrome')
  assert.deepEqual(calls.filter(call => call.operation === `get ${base}/program`).map(call => call.source), ['active', 'chrome', 'chrome'])
})
