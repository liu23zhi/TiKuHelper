const { test, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const Module = require('node:module')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
require(path.join(root, 'tests/helpers/vue-dom.cjs'))
globalThis.getComputedStyle = window.getComputedStyle
const { mount, flushPromises } = require('@vue/test-utils')
const { nextTick } = require('vue')
const Antd = require('ant-design-vue')
const user = require(path.join(root, 'src/utils/currentUser.js'))
const calls = [], polls = [], wrappers = []
let listRows = [], listTotals = {}, listOverride = null
function mock(filename, exports) { const module = new Module(filename); module.loaded = true; module.exports = exports; require.cache[filename] = module }
mock(path.join(root, 'src/api/TikuComponentApi.js'), {
  getCourses: async () => { calls.push({ kind: 'courses' }); return { status: 200, data: [] } },
  getQuestions: async query => { calls.push({ kind: 'questions', query }); return { status: 200, data: { items: [], total: 0 } } },
})
mock(require.resolve('vue-router'), { useRouter: () => ({ push() {} }) })
const taskApiPath = path.join(root, 'src/pages/IntelligentImport/asyncTaskApi.mjs')
const realTaskApi = require(taskApiPath)
mock(taskApiPath, {
  ...realTaskApi,
  listAsyncTasks: async query => {
    calls.push({ kind: 'list', query })
    if (listOverride) return listOverride(query)
    return { data: { data: listRows.filter(task => !query.type || task.type === query.type), total: listTotals[query.status] || 0 } }
  },
  submitImportTask: async query => { calls.push({ kind: 'import', query }); return { data: { taskId: 'import-1', status: 'pending' } } },
  submitExportTask: async query => { calls.push({ kind: 'export', query }); return { data: { taskId: 'export-1', status: 'pending' } } },
  pollAsyncTask: (id, options) => new Promise(resolve => polls.push({ id, options, resolve })),
})
const Page = require(path.join(root, 'src/pages/TikuComponent/index.vue'))
async function settle() { await nextTick(); await flushPromises(); await nextTick() }
async function page() {
  user.setStoredCurrentUser({ id: 7001, role: 'admin' })
  const wrapper = mount(Page, { attachTo: document.body, global: { plugins: [Antd] } }); wrappers.push(wrapper)
  await settle(); return wrapper
}
afterEach(async () => {
  for (const wrapper of wrappers) wrapper.unmount()
  wrappers.length = 0
  for (const poll of polls.splice(0)) poll.resolve({ stopped: true })
  calls.length = 0; listRows = []; listTotals = {}; listOverride = null
  user.clearStoredCurrentUser(); Antd.message.destroy(); Antd.Modal.destroyAll(); await settle(); document.body.replaceChildren()
})

test('题库任务摘要使用服务端待处理总数并限制到题库资源', async () => {
  listTotals = { pending: 13, running: 7 }
  listRows = [{ taskId: 'done-import', type: 'import', status: 'succeeded', succeeded: 2, createdAt: '2026-11-20', resource: 'questions', downloadUrl: '/api/export/tasks/done-import/download' }]
  const wrapper = await page()
  assert.equal(wrapper.vm.pendingTaskCount, 20, '不能用当前任务列表行数冒充全部待处理任务')
  assert.ok(calls.filter(call => call.kind === 'list').every(call => call.query.resource === 'questions'))
  assert.match(wrapper.vm.latestImportStatus, /成功 2/)
  assert.equal(wrapper.find('a[href="/api/export/tasks/done-import/download"]').exists(), true)
})

test('题库异步预览保留布尔参数并在身份切换后停止发布旧结果', async () => {
  const wrapper = await page()
  wrapper.vm.setAsyncDryRun({ target: { checked: true } })
  const submitted = wrapper.vm.submitAsyncImport({ file: { name: 'question.csv' }, onSuccess() {} })
  await settle()
  assert.equal(calls.find(call => call.kind === 'import').query.dryRun, true)
  assert.equal(calls.find(call => call.kind === 'import').query.resource, 'questions')
  const poll = polls.at(-1)
  user.setStoredCurrentUser({ id: 7002, role: 'admin' })
  assert.equal(poll.options.shouldStop(), true)
  poll.resolve({ task: { taskId: 'import-1', type: 'import', status: 'succeeded', succeeded: 99 } })
  await submitted; await settle()
  assert.equal(wrapper.vm.asyncTasks.some(task => task.taskId === 'import-1'), false)
  assert.equal(wrapper.vm.asyncNotice, '')
})

test('题库身份切换会为新管理员重新读取任务列表', async () => {
  await page()
  const initial = calls.filter(call => call.kind === 'list').length
  user.setStoredCurrentUser({ id: 7002, role: 'admin' }); await settle()
  assert.equal(calls.filter(call => call.kind === 'list').length, initial + 4)
})

test('较早的任务列表响应不能覆盖新刷新结果', async () => {
  const pending = []
  listOverride = query => query.status === 'pending'
    ? new Promise(resolve => pending.push(resolve))
    : Promise.resolve({ data: { data: [], total: 0 } })
  const wrapper = await page()
  const refresh = wrapper.vm.refreshAsyncTasks(); await settle()
  assert.equal(pending.length, 2)
  pending[1]({ data: { data: [], total: 7 } }); await refresh; await settle()
  pending[0]({ data: { data: [], total: 99 } }); await settle()
  assert.equal(wrapper.vm.pendingTaskCount, 7)
})

for (const dryRun of [false, true]) test(`异步导入完成仅在真实写入后刷新当前筛选与课程：dryRun=${dryRun}`, async () => {
  const wrapper = await page()
  wrapper.vm.searchValue = { question: '当前题干', courseName: '当前课程' }
  wrapper.vm.setAsyncDryRun({ target: { checked: dryRun } })
  const readsBefore = calls.filter(call => call.kind === 'questions').length
  const coursesBefore = calls.filter(call => call.kind === 'courses').length
  const submitted = wrapper.vm.submitAsyncImport({ file: { name: 'question.csv' }, onSuccess() {} }); await settle()
  polls.at(-1).resolve({ task: { taskId: 'import-1', type: 'import', status: 'succeeded', succeeded: 1, dryRun } })
  await submitted; await settle()
  assert.equal(calls.filter(call => call.kind === 'questions').length, readsBefore + (dryRun ? 0 : 1))
  assert.equal(calls.filter(call => call.kind === 'courses').length, coursesBefore + (dryRun ? 0 : 1))
  if (!dryRun) {
    assert.equal(calls.filter(call => call.kind === 'questions').at(-1).query.courseName, '当前课程')
    assert.equal(calls.filter(call => call.kind === 'questions').at(-1).query.question, '当前题干')
  }
})
