/* 异步任务传输层契约：真实 asyncTaskApi.mjs，只把 axios adapter 换成合成实现。
 * 零 socket / 零 provider：任何请求都被这里的 adapter 接住并断言。 */
const { test, beforeEach, afterEach } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const root = process.env.TIKU_WEB_SOURCE || path.resolve(__dirname, '../..')
process.env.NODE_PATH = [path.join(root, 'node_modules'), path.resolve(__dirname, '../../node_modules'), process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
require('node:module').Module._initPaths()
// 装载 src/ 下的 ESM 变换（把 import 改写成 CommonJS require）：
// asyncTaskApi.mjs 是 ESM，直接 require 会走 Node 的 ESM 解析器并因
// 「无扩展名的相对导入」失败。这个 helper 是仓内既有的同一套变换。
require(path.join(root, 'tests/helpers/vue-dom.cjs'))

const axios = require('axios')
const create = axios.create
let requests = [], respond
axios.create = config => create({
  ...config,
  adapter: async request => {
    requests.push(request)
    assert.equal(typeof respond, 'function', 'adapter 必须被用例显式驱动，不许走真实网络')
    return respond(request)
  },
})

const api = require(path.join(root, 'src/pages/IntelligentImport/asyncTaskApi.mjs'))
axios.create = create

const ok = (config, data, status = 200) => ({ config, status, statusText: String(status), headers: {}, data })

beforeEach(() => { requests = []; respond = () => ok({}, {}) })
afterEach(() => { axios.create = create })

function fakeFile(name, size = 1024) {
  // FormData.append 要求 Blob/File；用 Blob + name 模拟 a-upload 给的 File。
  const blob = new Blob([new Uint8Array(size)], { type: 'text/csv' })
  blob.name = name
  blob.size = size
  return blob
}

// ─────────────────── 路径契约（7 条路由，逐条钉住） ───────────────────

test('ASYNC_API import/export/template 路径与后端 async_task.go 头注释一致', () => {
  assert.equal(api.IMPORT_TASKS_PATH, '/adapter-service/import/tasks')
  assert.equal(api.EXPORT_TASKS_PATH, '/adapter-service/export/tasks')
  assert.equal(api.IMPORT_TEMPLATE_PATH, '/adapter-service/import/template')
})

test('ASYNC_API 上限常量与后端 service 常量对齐（题库 1 万导出 / 5 千导入）', () => {
  assert.equal(api.QUESTION_IMPORT_MAX_ROWS, 5000)
  assert.equal(api.QUESTION_EXPORT_MAX_ROWS, 10000)
})

// ─────────────────── 导入表单（multipart） ───────────────────

test('ASYNC_API submitImportTask issues one POST to /import/tasks with resource+dryRun+file', async () => {
  respond = config => ok(config, { taskId: 'imp-0000000000001', status: 'pending', total: 3 })
  const file = fakeFile('questions.csv')
  const result = await api.submitImportTask({ resource: 'questions', file, dryRun: false })

  assert.equal(requests.length, 1, '一次调用只能发一个请求')
  assert.equal(requests[0].method, 'post')
  assert.equal(requests[0].url, '/adapter-service/import/tasks')
  // 绝不手写 multipart Content-Type：手写的那个没有 boundary，
  // 后端会解析出空 body。真正的边界由浏览器在发送时补，所以这里只钉
  // 「我们没有自己塞一个 multipart/form-data 头」。
  const contentType = String(requests[0].headers?.['Content-Type'] || '')
  assert.ok(!contentType.startsWith('multipart/form-data'),
    `不得手写 multipart Content-Type（会缺 boundary），实际 ${contentType}`)
  const body = requests[0].data
  assert.ok(body instanceof FormData, 'body 必须是 FormData（axios 才会让浏览器补 boundary）')
  assert.equal(body.get('resource'), 'questions')
  assert.equal(body.get('dryRun'), 'false')
  assert.equal(body.get('confirmWeakPassword'), 'false')
  assert.ok(body.get('file'), 'file 字段必须存在（后端 c.FormFile("file")）')
  assert.equal(result.data.taskId, 'imp-0000000000001')
})

test('ASYNC_API dryRun 与 confirmWeakPassword 必须是字符串 true/false，不能是 undefined', async () => {
  respond = config => ok(config, { taskId: 'imp-0000000000002', status: 'pending' })
  await api.submitImportTask({ resource: 'questions', file: fakeFile('a.csv'), dryRun: true, confirmWeakPassword: true })
  const body = requests[0].data
  // 后端 parseAsyncTaskBool 认 "true"/"1"/"on"；收到字面量 "undefined" 会静默落成 false，
  // 而这正是「预览变成真写库」的事故形态。
  assert.equal(body.get('dryRun'), 'true')
  assert.equal(body.get('confirmWeakPassword'), 'true')
})

test('ASYNC_API 不支持的 resource 在发出请求之前就拒绝', async () => {
  assert.throws(() => api.buildImportTaskForm({ resource: 'billing', file: fakeFile('a.csv') }), /导入资源不受支持/)
  assert.throws(() => api.buildImportTaskForm({ resource: 'questions' }), /请选择要导入的文件/)
  assert.equal(requests.length, 0, '非法入参不得触发任何请求')
})

// ─────────────────── 导出载荷（JSON） ───────────────────

test('ASYNC_API buildExportTaskPayload 恒带 format=csv，且不带 limit/masked 时不伪造默认值', () => {
  const payload = api.buildExportTaskPayload({ resource: 'questions' })
  assert.deepEqual(payload, { resource: 'questions', format: 'csv' })
  // masked 的默认值只能由后端决定（service.resolveExportMasked(nil) → true）。
  // 前端写 masked:true 会在后端改默认时把旧默认硬套回去。
  assert.equal('masked' in payload, false)
  assert.equal('limit' in payload, false)
})

test('ASYNC_API buildExportTaskPayload 显式传 masked=false 时必须原样透传（明文导出）', () => {
  const payload = api.buildExportTaskPayload({ resource: 'questions', masked: false })
  assert.equal(payload.masked, false)
  const enabled = api.buildExportTaskPayload({ resource: 'questions', masked: 'true' })
  assert.equal(enabled.masked, true)
})

test('ASYNC_API buildExportTaskPayload 只接受正整数的 limit', () => {
  assert.equal(api.buildExportTaskPayload({ resource: 'questions', limit: 100 }).limit, 100)
  for (const bad of [0, -1, '12abc', NaN, null, undefined]) {
    assert.equal('limit' in api.buildExportTaskPayload({ resource: 'questions', limit: bad }), false,
      `limit=${String(bad)} 不应被写进载荷`)
  }
})

test('ASYNC_API submitExportTask posts exactly one JSON request to /export/tasks', async () => {
  respond = config => ok(config, { taskId: 'exp-0000000000001', status: 'pending', masked: true }, 202)
  await api.submitExportTask({ resource: 'questions' })
  assert.equal(requests.length, 1)
  assert.equal(requests[0].method, 'post')
  assert.equal(requests[0].url, '/adapter-service/export/tasks')
  const body = typeof requests[0].data === 'string' ? JSON.parse(requests[0].data) : requests[0].data
  assert.deepEqual(body, { resource: 'questions', format: 'csv' })
})

// ─────────────────── 查询 / 列表 / 模板 ───────────────────

test('ASYNC_API fetchAsyncTask 把 taskId 拼进路径并拒绝非法编号', async () => {
  respond = config => ok(config, { taskId: 'imp-0000000000001', status: 'running' })
  await api.fetchAsyncTask('imp-0000000000001')
  assert.equal(requests[0].url, '/adapter-service/export/tasks/imp-0000000000001')
  assert.equal(requests[0].method, 'get')
  for (const bad of ['', '   ', '../etc/passwd', 'a/b', null, 42]) {
    assert.throws(() => api.fetchAsyncTask(bad), /任务编号无效/, `taskId=${String(bad)} 必须被拒`)
  }
})

test('ASYNC_API listAsyncTasks 只透传白名单过滤参数', async () => {
  respond = config => ok(config, { data: [], total: 0 })
  await api.listAsyncTasks({ type: 'import', status: 'succeeded', resource: 'questions', page: 2, pageSize: 20, evil: 'x' })
  assert.equal(requests[0].url, '/adapter-service/export/tasks')
  assert.deepEqual(requests[0].params, { type: 'import', status: 'succeeded', resource: 'questions', page: 2, pageSize: 20 })
})

test('ASYNC_API fetchImportTemplate 用 blob 拉模板（否则 401 会下载成 JSON 错误页）', async () => {
  respond = config => ok(config, new Blob(['question,type\n'], { type: 'text/csv' }))
  await api.fetchImportTemplate('questions')
  assert.equal(requests[0].url, '/adapter-service/import/template')
  assert.equal(requests[0].params.resource, 'questions')
  assert.equal(requests[0].params.format, 'csv')
  assert.equal(requests[0].responseType, 'blob')
  assert.throws(() => api.fetchImportTemplate('billing'), /导入资源不受支持/)
})

// ─────────────────── 结果解读 ───────────────────

test('ASYNC_API isTaskSettled 只把 succeeded/failed/cancelled 当终态', () => {
  for (const status of ['succeeded', 'failed', 'cancelled', 'SUCCEEDED']) {
    assert.equal(api.isTaskSettled({ status }), true, `${status} 应为终态`)
  }
  for (const status of ['pending', 'running', '', undefined, 'queued']) {
    assert.equal(api.isTaskSettled({ status }), false, `${String(status)} 不应是终态`)
  }
})

test('ASYNC_API describeTask 区分预览/失败/执行中，失败必须带原因', () => {
  assert.equal(api.describeTask({ status: 'pending' }), '任务排队中')
  assert.match(api.describeTask({ status: 'running', total: 10, processed: 4 }), /任务执行中（4\/10）/)
  assert.equal(api.describeTask({ status: 'succeeded', succeeded: 3, skipped: 1, failed: 0 }), '任务完成：成功 3，跳过 1')
  // failed 必须暴露 errorMessage，只说「任务失败」会让用户无从下手。
  assert.equal(api.describeTask({ status: 'failed', errorMessage: '文件第 2 行格式错误' }), '任务失败：文件第 2 行格式错误')
  assert.equal(api.describeTask({ status: 'cancelled' }), '任务已取消')
  assert.equal(api.describeTask(null), '任务状态未知')
})

test('ASYNC_API taskDownloadUrl / taskErrorReportUrl 只接受本站相对路径', () => {
  const task = { downloadUrl: '/adapter-service/export/tasks/x/download', errorReportUrl: '/adapter-service/export/tasks/x/errors' }
  assert.equal(api.taskDownloadUrl(task), '/adapter-service/export/tasks/x/download')
  assert.equal(api.taskErrorReportUrl(task), '/adapter-service/export/tasks/x/errors')
  // 跨站地址一律丢弃：结果文件绝不能因为后端拼错而被带到第三方。
  assert.equal(api.taskDownloadUrl({ downloadUrl: 'https://evil.invalid/x.csv' }), '')
  assert.equal(api.taskDownloadUrl({ downloadUrl: '//evil.invalid/x.csv' }), '')
  assert.equal(api.taskDownloadUrl({}), '')
})

// ─────────────────── 轮询 ───────────────────

test('ASYNC_API pollAsyncTask 到终态即停，且不重复查询已终态任务', async () => {
  const sequence = [
    { status: 'pending' },
    { status: 'running', total: 5, processed: 2 },
    { status: 'succeeded', succeeded: 5 },
  ]
  let index = 0
  const sleeps = []
  const result = await api.pollAsyncTask('imp-0000000000001', {
    fetchTask: async () => { const task = sequence[index]; index += 1; return { data: task, status: 200 } },
    sleep: async ms => { sleeps.push(ms) },
  })
  assert.equal(index, 3, '读到最后一次终态就停，不再多发一次查询')
  assert.equal(result.task.status, 'succeeded')
  assert.equal(result.stopped, false)
  // 首轮短延迟、之后稳态间隔。
  assert.deepEqual(sleeps, [api.TASK_POLL_INITIAL_MS, api.TASK_POLL_INTERVAL_MS, api.TASK_POLL_INTERVAL_MS])
})

test('ASYNC_API pollAsyncTask 在 shouldStop 为真时立刻返回 stopped 且不再查询', async () => {
  let calls = 0
  const result = await api.pollAsyncTask('imp-0000000000001', {
    fetchTask: async () => { calls += 1; return { data: { status: 'pending' } } },
    shouldStop: () => true,
    sleep: async () => {},
  })
  assert.equal(result.stopped, true)
  assert.equal(calls, 0, '已经该停了就不许再发查询')
})

test('ASYNC_API pollAsyncTask 单次失败不中止，连续三次才放弃并保留原错误', async () => {
  let calls = 0
  const failure = new Error('network blip')
  const result = await api.pollAsyncTask('imp-0000000000001', {
    fetchTask: async () => {
      calls += 1
      if (calls <= 2) throw failure
      return { data: { status: 'succeeded', succeeded: 1 } }
    },
    sleep: async () => {},
  })
  assert.equal(calls, 3)
  assert.equal(result.task.status, 'succeeded', '抖动两次后仍应读到终态')

  let failing = 0
  const dead = await api.pollAsyncTask('imp-0000000000001', {
    fetchTask: async () => { failing += 1; throw failure },
    sleep: async () => {},
  })
  assert.equal(failing, 3, '连续三次失败后放弃，不许无限重试')
  assert.equal(dead.error, failure)
})
