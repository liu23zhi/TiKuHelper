const { test } = require('node:test')
const assert = require('node:assert/strict')
require('../helpers/vue-dom.cjs')
const { mount, flushPromises, config } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const axios = require('axios')
const { registerConsoleUI } = require('../../src/registerUI.js')
const { setStoredCurrentUser, clearStoredCurrentUser } = require('../../src/utils/currentUser.js')

const base = '/api/admin/review-rebate'
const pageSize = 20
const queryFor = (status, page) => ({ status, page, pageSize })
const rowFor = (id, status) => ({ id, status, userId: 102, source: 'edge' })
const fixtureRows = [
  rowFor(9123, 'rejected'), rowFor(9122, 'rejected'),
  ...Array.from({ length: 21 }, (_, i) => rowFor(9121 - i, 'pending_review')),
]
const firstPending = Array.from({ length: pageSize }, (_, i) => 9121 - i)

async function settle() {
  await nextTick()
  await flushPromises()
  await nextTick()
}

test('manual claims pager retains applied filters and resets to page one on filter change', async () => {
  const blocked = []
  const reads = []
  const originalAdapter = axios.defaults.adapter
  const names = ['ConsolePageHeading', 'ConsolePanel', 'ConsoleSummaryGrid', 'ConsoleTableShell']
  const originalComponents = Object.fromEntries(names.map(name => [name, config.global.components[name]]))
  for (const name of names) delete config.global.components[name]
  let wrapper, router
  axios.defaults.adapter = async request => {
    if (request.method !== 'get' || !['/programs', '/rules', '/claims'].some(path => request.url === base + path)) {
      blocked.push([request.method, request.url])
      throw new Error('read-only fixture: unapproved transport')
    }
    let data
    if (request.url === base + '/programs') {
      data = [{ id: 1, name: 'owned fixture', source: 'edge', enabled: false, minAgentConfidence: 0.6 }]
    } else if (request.url === base + '/rules') {
      data = []
    } else {
      const params = { ...request.params }
      const page = Number(params.page ?? 1)
      const size = Number(params.pageSize ?? pageSize)
      const selected = fixtureRows.filter(row => (!params.status || params.status === row.status) &&
        (!params.userId || Number(params.userId) === row.userId))
      reads.push({ params, page, size })
      data = { data: selected.slice((page - 1) * size, page * size), total: selected.length, page, pageSize: size }
    }
    return { status: 200, data, config: request }
  }
  try {
    // request.js creates the client at module load; install the owned adapter first.
    const ReviewRebateAdmin = require('../../src/pages/ReviewRebateAdmin/index.vue')
    setStoredCurrentUser({ id: 101, role: 'admin', status: 'enabled' })
    router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/review-rebate-admin', component: ReviewRebateAdmin }],
    })
    await router.push('/review-rebate-admin')
    wrapper = mount(ReviewRebateAdmin, {
      attachTo: document.body,
      global: { plugins: [router, { install: registerConsoleUI }] },
    })
    await settle()
    const chooseStatus = async label => {
      await wrapper.get('.claim-filter-form .ant-select-selector').trigger('mousedown')
      await settle()
      const item = [...document.querySelectorAll('.ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option')]
        .find(node => node.textContent.trim() === label)
      assert.ok(item, 'actual Ant Select option for ' + label)
      item.click()
      await settle()
    }
    const visibleIds = () => wrapper.get('[aria-label="评价返现提交列表"]').findAll('tr[data-row-key]')
      .map(row => Number(row.attributes('data-row-key')))
    const pager = '[aria-label="评价返现提交分页"]'

    await chooseStatus('待人工复核')
    assert.deepEqual([reads.at(-1).params.status, reads.at(-1).page, reads.at(-1).size], ['pending_review', 1, pageSize])
    assert.deepEqual(visibleIds(), firstPending)
    const next = wrapper.find(pager + ' .ant-pagination-next button')
    console.log('T09_PAGER_PRECONDITION=' + JSON.stringify({ firstPage: reads.at(-1), rows: visibleIds(), nextControl: next.exists() }))
    assert.ok(next.exists(), 'T09 intended RED: filtered first 20 of 21 must offer the real Next control')

    const draft = wrapper.get('.claim-filter-form input[placeholder="精确账户 ID"]')
    const beforeDraft = reads.length
    await draft.setValue('999')
    await settle()
    assert.equal(reads.length, beforeDraft, 'unsubmitted user ID is only a draft')
    await next.trigger('click')
    await settle()
    const page2 = reads.at(-1)
    assert.deepEqual(page2.params, queryFor('pending_review', 2), 'Next must keep the applied filter, not send the draft')
    assert.deepEqual(visibleIds(), [9101])
    assert.equal(wrapper.get(pager + ' .ant-pagination-item-active').text(), '2')

    await draft.setValue('')
    await chooseStatus('已拒绝')
    const changed = reads.at(-1)
    assert.deepEqual([changed.params.status, changed.page, changed.size], ['rejected', 1, pageSize])
    assert.deepEqual(visibleIds(), [9123, 9122])
    assert.equal(wrapper.find(pager).exists(), false, 'two rejected records need no pager')
    console.log('T09_PAGER_BEHAVIOR=' + JSON.stringify({ nextQuery: page2.params, pageTwoRows: [9101], changedFilter: changed.params.status, changedPage: changed.page, changedRows: visibleIds(), blocked }))
  } finally {
    wrapper?.unmount()
    router?.options.history.destroy()
    clearStoredCurrentUser()
    document.body.replaceChildren()
    axios.defaults.adapter = originalAdapter
    Object.assign(config.global.components, originalComponents)
    assert.deepEqual(blocked, [], 'pager/filter must not attempt a write or unknown request')
  }
})
