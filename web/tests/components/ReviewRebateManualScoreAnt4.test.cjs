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
let wrapper, router, rows, scoreReplies, writes
axios.defaults.adapter = async config => {
  if (config.method === 'get') {
    if (config.url === base + '/claims') return { status: 200, data: { data: rows.map(value => ({ ...value })) }, config }
    if (config.url === base + '/programs') return { status: 200, data: { data: [{ id: 1, name: 'fixture', source: 'edge', minAgentConfidence: 0.6 }] }, config }
    if (config.url === base + '/rules') return { status: 200, data: { data: [] }, config }
  } else {
    writes.push([config.method, config.url])
    const result = scoreReplies.shift()
    assert.ok(result, 'only explicitly queued scoring clicks may write')
    return { ...await result, config }
  }
  throw new Error('unexpected request')
}
const Review = require('../../src/pages/ReviewRebateAdmin/index.vue')
const item = (id, status, agentScoreState, score = {}) => ({
  id, status, agentScoreState, source: 'edge', userId: 102,
  agentScorer: '', agentStars: 0, agentContentScore: 0, agentConfidence: 0, ...score,
})
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
const row = id => wrapper.get('tr[data-row-key="' + id + '"]')
const button = (id, name) => row(id).findAll('button').find(control => control.text() === name)

async function open() {
  writes = []
  scoreReplies = []
  rows = [
    item(41, 'submitted', 'unscored'),
    item(42, 'submitted', 'running'),
    item(43, 'submitted', 'failed'),
    item(44, 'scored', 'scored', { agentScorer: 'agent', agentStars: 4, agentContentScore: 7, agentConfidence: 0.9 }),
    item(45, 'approved', 'unscored'),
  ]
  setStoredCurrentUser({ id: 101, role: 'admin', status: 'enabled' })
  router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/review-rebate-admin', component: Review }] })
  await router.push('/review-rebate-admin')
  wrapper = mount(Review, { attachTo: document.body, global: { plugins: [router, { install: registerConsoleUI }] } })
  await settle()
}

afterEach(() => {
  wrapper?.unmount()
  router?.options.history.destroy()
  clearStoredCurrentUser()
  document.body.replaceChildren()
})
after(() => { axios.defaults.adapter = original })

test('the administrator queue renders durable scoring states without auto-scoring or changing review actions', async () => {
  await open()
  assert.match(row(41).text(), /待评分/)
  assert.match(row(42).text(), /评分中/)
  assert.match(row(43).text(), /评分失败/)
  assert.match(row(44).text(), /已评分/)
  assert.ok(button(41, '开始评分'))
  assert.ok(button(43, '重试评分'))
  for (const id of [42, 44, 45]) {
    assert.equal(button(id, '开始评分'), undefined)
    assert.equal(button(id, '重试评分'), undefined)
  }
  assert.ok(button(43, '拒绝'), 'failed scoring must not block manual rejection')
  assert.deepEqual(writes, [])
})

test('manual scoring locks duplicate clicks, refreshes failure and retries into a confirmed score', async () => {
  await open()
  let finish
  scoreReplies.push(new Promise(resolve => { finish = resolve }))
  const start = button(41, '开始评分')
  assert.ok(start, 'an unscored claim needs a manual start button')
  // Simulate two fast clicks before Vue has applied its render update.
  const clicks = [start.trigger('click'), start.trigger('click')]
  await Promise.all(clicks)
  await settle()
  assert.match(row(41).text(), /评分中/)
  assert.deepEqual(writes, [['post', base + '/claims/41/score']])

  rows[0] = item(41, 'submitted', 'failed')
  finish({ status: 502, data: { message: 'fixture score failure' } })
  await settle()
  assert.match(row(41).text(), /评分失败/)

  rows[0] = item(41, 'scored', 'scored', { agentScorer: 'agent', agentStars: 4, agentContentScore: 7, agentConfidence: 0.9 })
  scoreReplies.push(Promise.resolve({ status: 200, data: rows[0] }))
  await button(41, '重试评分').trigger('click')
  await settle()
  assert.match(row(41).text(), /已评分/)
  assert.match(row(41).text(), /4 星 \/ 7 分/)
  assert.equal(button(41, '重试评分'), undefined)
  assert.deepEqual(writes, Array.from({ length: 2 }, () => ['post', base + '/claims/41/score']))
})
