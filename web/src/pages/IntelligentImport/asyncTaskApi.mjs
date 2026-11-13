// 智能导入页的异步任务传输层（CSV / xlsx 走服务端异步，JSON 走浏览器侧同步）。
//
// # 为什么要有这一层，而不是直接在 index.vue 里写 request(...)
//
// 页面已有两条「同步、浏览器侧」的题目通道（TikuComponent 的 JSON 导入 / 导出选中），
// 本轮新增的是第三条「异步、服务端」通道。三条通道并存时最容易发生的事故是
// **路径与参数形状被写歪**（少一个 resource、把 dryRun 当字符串传、忘记 masked 默认值），
// 而这种错误在页面上只表现为「点了没反应」。
//
// 所以把「路径 + 参数形状 + 结果解读」全部收在这个纯函数模块里：
// 它不 import Vue、不碰 DOM，可以用 node --test 直接钉住每一个字节。
//
// # 后端契约（internal/controller/async_task.go 头注释）
//
//	POST /adapter-service/import/tasks                 提交导入（multipart：resource/dryRun/confirmWeakPassword/file）
//	POST /adapter-service/export/tasks                  提交导出（JSON：resource/format/masked/limit）
//	GET  /adapter-service/export/tasks/:taskId          查单个任务（导入与导出共用）
//	GET  /adapter-service/export/tasks                  任务列表
//	GET  /adapter-service/export/tasks/:taskId/download 下载结果文件（过期 → 410）
//	GET  /adapter-service/export/tasks/:taskId/errors   下载失败行报告
//	GET  /adapter-service/import/template               下载导入模板
//
// ⚠️ 这 7 条路由的注册不在 async_task.go 里（该文件明确写了「路由注册不在本文件」），
// 由集成方统一挂到 router.go。前端在这里先按上述路径写死，两侧对齐后即可工作。

import request from '../../utils/request'

// ─────────────────────────── 常量 ───────────────────────────

// 导入与导出的路径前缀。分开写是因为导入是 multipart、导出是 JSON，
// 两者的默认 header 与超时语义不同，共用一个常量反而容易串味。
export const IMPORT_TASKS_PATH = '/adapter-service/import/tasks'
export const EXPORT_TASKS_PATH = '/adapter-service/export/tasks'
export const IMPORT_TEMPLATE_PATH = '/adapter-service/import/template'

// 题库的行数上限（与后端 service.QuestionImportMaxRows / QuestionExportMaxRows 对齐）。
//
// 前端只用来**提示**与**提前拦截明显超限的文件**，真正的闸门在后端 ——
// 前端数字与后端不一致时，唯一正确的结果是后端说了算，所以这里绝不因为
// 「本地觉得超了」就跳过请求（那样会把「后端放宽了但前端还在挡」变成线上事故）。
export const QUESTION_IMPORT_MAX_ROWS = 5000
export const QUESTION_EXPORT_MAX_ROWS = 10000

// 轮询间隔与上限（111 Q12-1 选 B：立即返回 taskId，前端轮询）。
//
// 首轮延迟略短于稳态：任务通常 1-2 秒内就完成，等满一个周期才第一次查
// 会让「明明已经好了」的界面白等半秒。
export const TASK_POLL_INITIAL_MS = 400
export const TASK_POLL_INTERVAL_MS = 1500
export const TASK_POLL_MAX_ATTEMPTS = 200

// 任务终态：进入这些状态就停止轮询。
const TERMINAL = new Set(['succeeded', 'failed', 'cancelled'])

// ─────────────────────────── 参数形状 ───────────────────────────

// 逐行结果里的 resource 取值（与后端 service.ImportResourceList 一致）。
export const IMPORT_RESOURCES = ['questions', 'blacklist', 'users']

// 导出只支持 CSV：JSON 由前端本地生成（后端对这个组合直接 400）。
export const EXPORT_FORMATS = ['csv']

// readBoolean 把表单/复选框的值归一成布尔。
//
// 必须显式归一：multipart 表单里的布尔最终变成字符串 "true"/"false"，
// 后端 parseAsyncTaskBool 也认 "1"/"on"，但前端传 undefined 会变成字面量
// `undefined` —— 后端不认，静默落成 false。dryRun 落成 false 意味着
// 「预览一下」变成「真写库」，这是本层最危险的一个静默降级。
export function readBoolean(value) {
  if (value === true || value === 1) return true
  if (typeof value === 'string') return ['true', '1', 'on', 'yes'].includes(value.trim().toLowerCase())
  return false
}

// buildImportTaskForm 组装 POST /import/tasks 的 multipart 表单。
//
// 顺序与「必填 / 可选」的语义无关（multipart 无序），但每一项都必须存在：
// 后端 resource 缺失直接 400，dryRun 缺失按 false 处理。
export function buildImportTaskForm({ resource, file, dryRun = false, confirmWeakPassword = false } = {}) {
  if (!IMPORT_RESOURCES.includes(resource)) throw new Error('导入资源不受支持')
  if (!file) throw new Error('请选择要导入的文件')
  const form = new FormData()
  form.append('resource', resource)
  form.append('dryRun', readBoolean(dryRun) ? 'true' : 'false')
  form.append('confirmWeakPassword', readBoolean(confirmWeakPassword) ? 'true' : 'false')
  // filename 由浏览器从 File 自动带上；不额外 append 一个同名 file，
  // 那会让后端 c.FormFile("file") 拿到两段，取第一段（可能是不带名字的）。
  form.append('file', file, file.name || 'import.csv')
  return form
}

// buildExportTaskPayload 组装 POST /export/tasks 的 JSON 体。
//
// masked 的默认值**不在前端决定**：不传就是 undefined，后端
// service.resolveExportMasked(nil) → DefaultExportMasked(true)。
// 在这里写 `masked: masked ?? true` 会让默认值出现两个来源，
// 将来后端改默认（比如按资源分级）时前端会把旧默认值硬套回去。
export function buildExportTaskPayload({ resource, masked, limit } = {}) {
  if (typeof resource !== 'string' || !resource.trim()) throw new Error('导出资源不能为空')
  const payload = { resource: resource.trim(), format: 'csv' }
  if (masked !== undefined && masked !== null) payload.masked = readBoolean(masked)
  // limit 为 0/undefined 时不传：让后端按资源上限取默认，
  // 前端硬编码一个数字会在「题库 1 万、其它 10 万」的差别上出错。
  const parsedLimit = Number(limit)
  if (Number.isInteger(parsedLimit) && parsedLimit > 0) payload.limit = parsedLimit
  return payload
}

// ─────────────────────────── 请求 ───────────────────────────

// submitImportTask 提交一个异步导入任务（文件上传）。
//
// errorFeedback: 'local' —— 页面自己有持久失败态，不要让全局拦截器再弹一次。
// 不设 Content-Type：axios 检测到 FormData 会自己写 multipart/form-data 并带 boundary，
// 手写会把 boundary 抹掉，后端解析出空 body。
export function submitImportTask({ resource, file, dryRun, confirmWeakPassword } = {}) {
  return request({
    errorFeedback: 'local',
    url: IMPORT_TASKS_PATH,
    method: 'post',
    data: buildImportTaskForm({ resource, file, dryRun, confirmWeakPassword }),
  })
}

// submitExportTask 提交一个异步导出任务（CSV）。
export function submitExportTask(options = {}) {
  return request({
    errorFeedback: 'local',
    url: EXPORT_TASKS_PATH,
    method: 'post',
    data: buildExportTaskPayload(options),
  })
}

// fetchAsyncTask 查单个任务状态（导入与导出共用同一个查询接口）。
export function fetchAsyncTask(taskId) {
  const id = readTaskId(taskId)
  return request({
    errorFeedback: 'local',
    url: `${EXPORT_TASKS_PATH}/${encodeURIComponent(id)}`,
    method: 'get',
  })
}

// listAsyncTasks 任务列表（管理页：可按 type / status / resource 过滤）。
export function listAsyncTasks(params = {}) {
  const query = {}
  for (const key of ['type', 'status', 'resource']) {
    if (typeof params[key] === 'string' && params[key].trim()) query[key] = params[key].trim()
  }
  if (Number.isInteger(params.page) && params.page > 0) query.page = params.page
  if (Number.isInteger(params.pageSize) && params.pageSize > 0) query.pageSize = params.pageSize
  return request({
    errorFeedback: 'local',
    url: EXPORT_TASKS_PATH,
    method: 'get',
    params: query,
  })
}

// fetchImportTemplate 下载导入模板（CSV 表头）。
//
// 用 blob 而不是直接 <a download>：模板接口要求管理员身份，
// 裸链接触发的是浏览器自己的 GET（不带 axios 的凭据与错误处理），
// 401/403 会变成「下载到一个 JSON 错误页」。
export function fetchImportTemplate(resource, format = 'csv') {
  if (!IMPORT_RESOURCES.includes(resource)) throw new Error('导入资源不受支持')
  return request({
    errorFeedback: 'local',
    url: IMPORT_TEMPLATE_PATH,
    method: 'get',
    params: { resource, format },
    responseType: 'blob',
  })
}

// readTaskId 校验任务 ID 形状（后端生成的是 16 位十六进制前缀 + 序号）。
//
// 不校验就拼进 URL 会让 `../` 之类的东西进到路径里。
function readTaskId(taskId) {
  const value = typeof taskId === 'string' ? taskId.trim() : ''
  if (!/^[A-Za-z0-9_-]{1,64}$/.test(value)) throw new Error('任务编号无效')
  return value
}

// ─────────────────────────── 结果解读 ───────────────────────────

// taskPayload 取出 request 包装后的真实响应体。
//
// utils/request 统一返回 { data, status, message }（见 requestHelpers.buildResponsePayload），
// 而这里要的是 data 里的任务对象。抽出来是为了让「包装形态变了」只有一处要改。
export function taskPayload(response) {
  const body = response?.data
  return body && typeof body === 'object' ? body : null
}

// isTaskSettled 判断任务是否已进入终态。
export function isTaskSettled(task) {
  return Boolean(task) && TERMINAL.has(String(task.status || '').toLowerCase())
}

// describeTask 把任务行翻译成一句可展示的中文摘要。
//
// 计数只在**有值**时出现：导出任务在跑完前 Total/Succeeded 都是 0，
// 显示「已处理 0 / 共 0」会让人以为任务卡死了。
export function describeTask(task) {
  if (!task) return '任务状态未知'
  const status = String(task.status || '').toLowerCase()
  const total = Number.isFinite(task.total) ? task.total : 0
  const processed = Number.isFinite(task.processed) ? task.processed : 0
  const elapsed = processed > 0 && total > 0 ? `（${processed}/${total}）` : ''
  switch (status) {
    case 'pending':
      return '任务排队中'
    case 'running':
      return `任务执行中${elapsed}`
    case 'succeeded': {
      const pieces = [`成功 ${Number.isFinite(task.succeeded) ? task.succeeded : 0}`]
      if (task.skipped) pieces.push(`跳过 ${task.skipped}`)
      if (task.failed) pieces.push(`失败 ${task.failed}`)
      return `任务完成：${pieces.join('，')}`
    }
    case 'failed':
      return `任务失败${task.errorMessage ? '：' + task.errorMessage : ''}`
    case 'cancelled':
      return '任务已取消'
    default:
      return `任务状态：${status || '未知'}`
  }
}

// taskDownloadUrl 取出结果文件下载地址（后端只在成功且有文件时给）。
//
// 只接受「相对本站的路径」：跨站地址会被浏览器当成外链，
// 而这里的数据来自后端响应，一旦某天后端拼错成绝对 URL，
// 前端静默跳出去就是把结果文件交给第三方。
export function taskDownloadUrl(task) {
  return readLocalPath(task?.downloadUrl)
}

// taskErrorReportUrl 取出失败行报告地址（同上）。
export function taskErrorReportUrl(task) {
  return readLocalPath(task?.errorReportUrl)
}

function readLocalPath(value) {
  if (typeof value !== 'string') return ''
  const trimmed = value.trim()
  if (!trimmed.startsWith('/') || trimmed.startsWith('//')) return ''
  return trimmed
}

// pollAsyncTask 轮询到终态。
//
// 三个必须有的行为：
//  1. `shouldStop()` 每轮检查 —— 页面被切走/身份切换后必须停下，
//     否则旧身份的轮询会继续刷新一个已经不该存在的界面；
//  2. 单轮失败**不立即中止**：网络抖动一次就让用户重头点一次导出体验太差，
//     但连续失败到上限就放弃（避免无限重试掩盖后端 5xx）；
//  3. 返回最后一次读到的任务对象，即使失败也返回，让调用方能把
//     「失败原因」显示出来而不是只说「查询失败」。
export async function pollAsyncTask(taskId, options = {}) {
  const {
    fetchTask = fetchAsyncTask,
    shouldStop = () => false,
    interval = TASK_POLL_INTERVAL_MS,
    initialDelay = TASK_POLL_INITIAL_MS,
    maxAttempts = TASK_POLL_MAX_ATTEMPTS,
    sleep = defaultSleep,
  } = options
  let lastTask = null
  let consecutiveFailures = 0
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    if (shouldStop()) return { stopped: true, task: lastTask }
    await sleep(attempt === 0 ? initialDelay : interval)
    if (shouldStop()) return { stopped: true, task: lastTask }
    try {
      const response = await fetchTask(taskId)
      const task = taskPayload(response)
      if (task) {
        lastTask = task
        if (isTaskSettled(task)) return { stopped: false, task }
      }
      consecutiveFailures = 0
    } catch (error) {
      consecutiveFailures += 1
      // 连续失败 3 次才放弃：单次抖动不值得让用户重做。
      if (consecutiveFailures >= 3) return { stopped: false, task: lastTask, error }
    }
  }
  return { stopped: false, task: lastTask, expired: true }
}

function defaultSleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms))
}
