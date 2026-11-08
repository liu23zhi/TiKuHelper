/* Real Vue/Antd4 interactions; only the API boundary is synthetic. No browser/live IO. */
const { test } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const web = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(web, 'tests/helpers/vue-dom.cjs'))
// Match the existing helper's pseudo-free jsdom adapter on the global entry point too.
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const { createRouter, createMemoryHistory } = require('vue-router')
const Antd = require('ant-design-vue')
const currentUser = require(path.join(web, 'src/utils/currentUser.js'))
const row = (id, question) => ({ id, question, type: 0, options: '["甲","乙"]', answer: '["甲"]', source_name: 'SYNTHETIC', extra: '' })
let pending = [], writes = []
const apiPath = path.join(web, 'src/api/TikuComponentApi.js')
const api = new Module(apiPath)
api.loaded = true
api.exports = {
  getCourses: () => Promise.resolve({ status: 200, data: [] }),
  getQuestions: query => new Promise((resolve, reject) => pending.push({ query: { ...query }, resolve, reject })),
  delQuestions: (...args) => { writes.push(args); throw new Error('UNEXPECTED_WRITE') },
  updateQuestions: (...args) => { writes.push(args); throw new Error('UNEXPECTED_WRITE') },
  createQuestions: (...args) => { writes.push(args); throw new Error('UNEXPECTED_WRITE') },
  importQuestions: (...args) => { writes.push(args); throw new Error('UNEXPECTED_WRITE') },
}
require.cache[apiPath] = api
const pagePath = path.join(web, 'src/pages/TikuComponent/index.vue')
async function settle() { await nextTick(); await flushPromises(); await new Promise(resolve => setTimeout(resolve, 5)); await nextTick() }
function button(w, label) {
  const b = w.findAll('button').find(item => item.text().replace(/\s/g, '') === label)
  assert.ok(b, 'actual button ' + label)
  return b.element
}
async function click(node) { node.click(); await settle() }
function complete(request, items, total = items.length) { request.resolve({ status: 200, data: { items, total } }) }
function bulkStates(w) { return ['导出选中', '批量编辑课程名', '批量删除'].map(label => button(w, label).disabled) }
function witness(name, w) {
  console.log('TIKU_LIFETIME_WITNESS ' + JSON.stringify({ name, questions: w.find('tbody').text(), selected: [...w.vm.selectedRowKeys], loading: w.vm.tabLoading, selectAllLoading: w.vm.selectAllLoading, disabled: bulkStates(w), queries: pending.map(x => x.query), writes: writes.length, nativeBrowser: false }))
}
async function search(w, text) {
  await w.find('input[placeholder="搜索问题"]').setValue(text)
  await click(button(w, '搜索'))
  return pending.at(-1)
}
async function fixture(run) {
  currentUser.setStoredCurrentUser({ id: 7301, username: 'synthetic-tiku-admin', role: 'admin' })
  pending = []; writes = []
  delete require.cache[pagePath]
  const Tiku = require(pagePath)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: Tiku }] })
  await router.push('/'); await router.isReady()
  const w = mount(Tiku, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false } } })
  try {
    await settle()
    assert.equal(pending.length, 1)
    complete(pending[0], [row(101, '初始题目')], 2); await settle()
    await run(w)
  } finally {
    const clear = w.findAll('button').find(item => item.text().replace(/\s/g, '') === '取消全部选择')
    if (clear && !clear.element.disabled) await click(clear.element)
    w.unmount(); currentUser.clearStoredCurrentUser(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle()
    router.options.history.destroy(); document.body.replaceChildren()
    assert.deepEqual(writes, [], 'synthetic read-only fixture made zero mutation calls')
  }
}
test('TL01 older filter response cannot overwrite the newer rendered rows or total', () => fixture(async w => {
  const old = await search(w, '旧筛选')
  const current = await search(w, '新筛选')
  assert.equal(old.query.question, '旧筛选'); assert.equal(current.query.question, '新筛选')
  complete(current, [row(202, '新筛选结果')], 1); await settle()
  assert.ok(w.find('tbody').text().includes('新筛选结果'))
  complete(old, [row(303, '旧筛选迟到结果')], 7); await settle()
  witness('TL01-newer-result-owns-view', w)
  assert.ok(w.find('tbody').text().includes('新筛选结果'), 'TL01_LATEST_FILTER_OWNS_ROWS')
  assert.equal(w.vm.page.total, 1, 'latest query owns result count')
  assert.equal(w.vm.tabLoading, false)
}))
test('TL02 an obsolete completion cannot clear loading while the newer filter is pending', () => fixture(async w => {
  const old = await search(w, '旧请求')
  const current = await search(w, '未完成的新请求')
  complete(old, [row(303, '旧请求结果')], 7); await settle()
  witness('TL02-current-request-still-loading', w)
  assert.equal(w.vm.tabLoading, true, 'TL02_PENDING_LATEST_RETAINS_LOADING')
  complete(current, [], 0); await settle()
  assert.equal(w.vm.tabLoading, false)
  assert.equal(w.vm.page.total, 0)
  assert.ok(w.find('.ant-empty').exists(), 'real Antd empty state remains reachable')
}))
test('TL03 a retired read error cannot toast over newer success; current error still allows refresh recovery', () => fixture(async w => {
  const old = await search(w, '旧错误')
  const current = await search(w, '新成功')
  complete(current, [row(202, '新成功结果')]); await settle()
  old.reject(new Error('SYNTHETIC_STALE_ERROR')); await settle()
  witness('TL03-retired-error-suppressed', w)
  assert.equal(document.body.textContent.includes('获取题目列表失败'), false, 'TL03_STALE_ERROR_NO_TOAST')
  const failure = await search(w, '当前失败')
  failure.reject(new Error('SYNTHETIC_CURRENT_ERROR')); await settle()
  assert.ok(document.body.textContent.includes('获取题目列表失败'))
  assert.equal(w.vm.tabLoading, false)
  await click(button(w, '刷新'))
  assert.equal(pending.at(-1).query.question, '当前失败', 'retry retains literal filter')
  complete(pending.at(-1), [row(404, '重试已恢复')]); await settle()
  assert.ok(w.find('tbody').text().includes('重试已恢复'))
}))
test('TL04 filter reset retires pending select-all and late rows cannot re-enable bulk actions', () => fixture(async w => {
  await click(button(w, '全选所有页'))
  const all = pending.at(-1)
  assert.equal(all.query.pageSize, 100)
  const current = await search(w, '新范围')
  complete(current, [row(202, '新范围题目')]); await settle()
  complete(all, [row(101, '旧范围题目')]); await settle()
  witness('TL04-filter-reset-retires-all-pages', w)
  assert.deepEqual([...w.vm.selectedRowKeys], [], 'TL04_OLD_FILTER_CANNOT_RESELECT')
  assert.deepEqual(bulkStates(w), [true, true, true])
  assert.equal(w.vm.selectAllLoading, false)
}))
test('TL05 explicit cancel-all retires pending select-all and honors the newer user intent', () => fixture(async w => {
  await click(w.find('tbody input[type="checkbox"]').element)
  assert.deepEqual([...w.vm.selectedRowKeys], [101])
  await click(button(w, '全选所有页'))
  const all = pending.at(-1)
  await click(button(w, '取消全部选择'))
  complete(all, [row(101, '初始题目'), row(202, '跨页题目')]); await settle()
  witness('TL05-user-cancel-wins', w)
  assert.deepEqual([...w.vm.selectedRowKeys], [], 'TL05_CANCEL_ALL_CANNOT_BE_UNDONE_BY_LATE_READ')
  assert.deepEqual(bulkStates(w), [true, true, true])
  assert.equal(w.vm.selectAllLoading, false)
}))
test('TL06 unmount retires pending list errors without late toasts', async () => {
  await fixture(async w => {
    const stale = await search(w, '卸载前')
    w.unmount()
    stale.reject(new Error('SYNTHETIC_UNMOUNTED_ERROR')); await settle()
    assert.equal(document.body.textContent.includes('获取题目列表失败'), false, 'TL06_UNMOUNTED_REQUEST_NO_TOAST')
  })
})

test('TL07 healthy multi-page select-all snapshots the submitted filter and preserves every selected ID', () => fixture(async w => {
  const filtered = await search(w, '已提交筛选')
  complete(filtered, [row(101, '已提交筛选结果')], 101); await settle()
  await click(button(w, '全选所有页'))
  const first = pending.at(-1)
  await w.find('input[placeholder="搜索问题"]').setValue('未提交草稿')
  complete(first, Array.from({ length: 100 }, (_, i) => row(i + 1, '合成题目' + i)), 101); await settle()
  const second = pending.at(-1)
  assert.equal(second.query.pageNo, 1)
  assert.equal(second.query.question, '已提交筛选', 'TL07_ALL_PAGES_USE_ONE_FILTER_SNAPSHOT')
  complete(second, [row(101, '最后一题')], 101); await settle()
  witness('TL07-complete-filter-snapshot', w)
  assert.equal(w.vm.selectedRowKeys.length, 101)
  assert.equal(w.vm.selectedQuestions.length, 101)
  assert.equal(w.vm.allRowsSelected, true)
  assert.deepEqual(bulkStates(w), [false, false, false])
}))
test('TL08 a cancelled older select-all cannot clear loading or selection of a newer select-all', () => fixture(async w => {
  await click(w.find('tbody input[type="checkbox"]').element)
  await click(button(w, '全选所有页'))
  const old = pending.at(-1)
  await click(button(w, '取消全部选择'))
  await click(button(w, '全选所有页'))
  const current = pending.at(-1)
  assert.notEqual(current, old)
  complete(old, [row(303, '旧全选')]); await settle()
  assert.equal(w.vm.selectAllLoading, true, 'new selection operation retains loading')
  assert.deepEqual([...w.vm.selectedRowKeys], [])
  complete(current, [row(101, '当前全选1'), row(202, '当前全选2')]); await settle()
  witness('TL08-newer-all-pages-owns-loading', w)
  assert.equal(w.vm.selectAllLoading, false)
  assert.deepEqual([...w.vm.selectedRowKeys], [101, 202])
}))
test('TL09 unmount releases pending select-all; an old completion cannot affect same-module re-entry', () => fixture(async w => {
  await click(button(w, '全选所有页'))
  const old = pending.at(-1)
  w.unmount()
  const Tiku = require(pagePath)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: Tiku }] })
  await router.push('/'); await router.isReady()
  const fresh = mount(Tiku, { attachTo: document.body, global: { plugins: [router, Antd], stubs: { transition: false, 'transition-group': false } } })
  try {
    await settle()
    complete(pending.at(-1), [row(101, '重新进入题库')], 2); await settle()
    witness('TL09-reentry-selection-released', fresh)
    assert.equal(fresh.vm.selectAllLoading, false, 'TL09_UNMOUNT_RELEASES_SELECTION_LOADING')
    await click(button(fresh, '全选所有页'))
    const current = pending.at(-1)
    complete(old, [row(303, '已卸载全选')]); await settle()
    assert.equal(fresh.vm.selectAllLoading, true)
    assert.deepEqual([...fresh.vm.selectedRowKeys], [])
    complete(current, [row(101, '重新进入题库'), row(202, '重新全选')]); await settle()
    assert.deepEqual([...fresh.vm.selectedRowKeys], [101, 202])
    await click(button(fresh, '取消全部选择'))
  } finally { fresh.unmount(); router.options.history.destroy() }
}))
test('TL10 a manual checkbox change supersedes pending all-pages selection without losing the manual choice', () => fixture(async w => {
  await click(button(w, '全选所有页'))
  const all = pending.at(-1)
  await click(w.find('tbody input[type="checkbox"]').element)
  complete(all, [row(101, '初始题目'), row(202, '不应追加的旧范围题目')]); await settle()
  witness('TL10-manual-selection-wins', w)
  assert.deepEqual([...w.vm.selectedRowKeys], [101])
  assert.equal(w.vm.selectAllLoading, false)
}))
