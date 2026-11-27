const { test, afterEach, after } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises, config } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const axios = require('axios')
const { registerConsoleUI } = require('../../src/registerUI.js')
const { setStoredCurrentUser, clearStoredCurrentUser } = require('../../src/utils/currentUser.js')

for (const name of ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']) delete config.global.components[name]
const base = '/api/admin/review-rebate'
const original = axios.defaults.adapter
const defaultZeros = { agentScorer: '', agentStars: 0, agentContentScore: 0, agentConfidence: 0 }
const rows = [
  { ...defaultZeros, id: 42, status: 'submitted' },
  { ...defaultZeros, id: 43, status: 'pending_review', agentScorer: 'agent' },
  { ...defaultZeros, id: 44, status: 'rejected' },
  { ...defaultZeros, id: 45, status: 'scored' },
  { ...defaultZeros, id: 46, status: 'scored', agentScorer: 'stub', agentStars: 5, agentContentScore: 7, agentConfidence: 0.9 },
  { ...defaultZeros, id: 47, status: 'scraping', agentScorer: 'unrecognized-legacy', agentContentScore: 4 },
  { ...defaultZeros, id: 48, status: 'approved', agentScorer: 'agent', agentStars: 4, agentContentScore: 7, agentConfidence: 0.8 },
  { ...defaultZeros, id: 49, status: 'approved', agentRationale: 'legacy score evidence, not a source label', agentStars: 4, agentContentScore: 7, agentConfidence: 1 },
  { ...defaultZeros, id: 50, status: 'rejected', agentRationale: 'legacy zero-score evidence' },
  { ...defaultZeros, id: 51, status: 'rejected', reviewNote: 'manual decision, not a score' },
  { ...defaultZeros, id: 52, status: 'approved', agentRationale: ' \t ', agentStars: 4, agentContentScore: 7, agentConfidence: 1 },
].map(row => ({ source: 'edge', userId: 102, ...row }))
let wrapper, router
const writes = []
axios.defaults.adapter = async config => {
  if (config.method !== 'get') {
    writes.push(config.method)
    throw new Error('read-only fixture: mutations are forbidden')
  }
  let result
  if (config.url === base + '/claims') result = rows
  else if (config.url === base + '/programs') result = [{ id: 1, name: 'fixture', source: 'edge', minAgentConfidence: 0.6 }]
  else if (config.url === base + '/rules') result = []
  else throw new Error('unexpected read')
  return { status: 200, data: { data: result }, config }
}
const Review = require('../../src/pages/ReviewRebateAdmin/index.vue')
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function open() {
  setStoredCurrentUser({ id: 101, role: 'admin', status: 'enabled' })
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/review-rebate-admin', component: Review }] })
  await router.push('/review-rebate-admin')
  wrapper = mount(Review, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] } })
  await settle()
}
const row = id => wrapper.get('tr[data-row-key="' + id + '"]')
const cells = id => row(id).findAll('td').slice(5, 7).map(cell => cell.text())
const text = () => document.querySelector('[role="dialog"]')?.textContent || ''
async function peek(id) {
  const control = row(id).findAll('button').find(button => button.text() === '拒绝')
  assert.ok(control, 'opening the modal must preserve existing review action availability')
  await control.trigger('click')
  await settle()
}
afterEach(() => {
  wrapper?.unmount()
  router?.options.history.destroy()
  clearStoredCurrentUser()
  document.body.replaceChildren()
  assert.deepEqual(writes.splice(0), [], 'no decisions or scoring requests should be issued')
})
after(() => { axios.defaults.adapter = original })

test('table separates confirmed scores, real zeros, and rows without evidence of a score', async () => {
  await open()
  const actual = rows.filter(({ id }) => id <= 48).map(({ id }) => cells(id))
  console.log('T09_TABLE_SCORE_CELLS=' + JSON.stringify(actual))
  assert.deepEqual(actual, [
    ['—', '—'],
    ['0 星 / 0 分', '0.00 低于门槛'],
    ['—', '—'],
    ['0 星 / 0 分', '0.00 低于门槛'],
    ['5 星 / 7 分 占位分数 · 非模型产出，不可作为核验依据', '0.90'],
    ['—', '—'],
    ['4 星 / 7 分', '0.80'],
  ])
})

test('manual review confirmation displays no inferred score or low-confidence tag on an unscored record', async () => {
  await open()
  await peek(42)
  const visible = text()
  console.log('T09_CONFIRM_UNSCORED=' + JSON.stringify(visible.replace(/\s+/g, ' ')))
  assert.match(visible, /Agent 打分\s*—/)
  assert.match(visible, /置信度\s*—/)
  assert.doesNotMatch(visible, /低于门槛/)
})

test('manual review confirmation preserves verified zeros and the threshold warning', async () => {
  await open()
  await peek(43)
  const visible = text()
  console.log('T09_CONFIRM_REAL_ZERO=' + JSON.stringify(visible.replace(/\s+/g, ' ')))
  assert.match(visible, /Agent 打分\s*0 星 \/ 0 分/)
  assert.match(visible, /置信度\s*0\.00\s*低于门槛/)
})

test('legacy terminal rows retain persisted scores only when the dedicated rationale is nonblank', async () => {
  await open()
  const actual = [49, 50, 51, 52].map(cells)
  console.log('T09_LEGACY_SCORE_CELLS=' + JSON.stringify(actual))
  assert.deepEqual(actual, [
    ['4 星 / 7 分 来源未知', '1.00'],
    ['0 星 / 0 分', '0.00 低于门槛'],
    ['—', '—'],
    ['—', '—'],
  ])
  assert.doesNotMatch(actual.flat().join(' '), /legacy score evidence|legacy zero-score evidence|manual decision/)
})
