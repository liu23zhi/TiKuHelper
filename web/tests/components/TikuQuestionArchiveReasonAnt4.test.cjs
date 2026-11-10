/* 题库删除（= 归档）的 reason 契约回归。
 *
 * 为什么必须单独有这一组：后端 DeleteQuestion 强制 reason 必填
 * （internal/controller/tiku.go:303 `reason := c.Query("reason")`，<2 字符 400），
 * 而前端曾经既不收集也不传 —— 管理员点删除必定 400，功能实际不可用，
 * CI 却全绿（后端只测自己，前端没有对应断言）。
 *
 * 本文件用两种互补判据：
 *   ① 真实执行 TikuComponentApi.js 源码（不 mock api 层），拦截 axios config，
 *      断言**传输层的真实 URL 与 params** —— 这样才能抓住
 *      「reason 没放进 params 被 axios 当 config 静默丢弃」这个高频错误。
 *   ② 直接执行 index.vue 的 deleteQuestion 方法源码，断言行为：
 *      取消 / 校验失败不发请求，合法原因才带 reason 发请求，且文案已对齐软删除口径。
 */
const { test } = require('node:test')
const assert = require('node:assert/strict')
const path = require('node:path')
const fs = require('node:fs')
const { pathToFileURL } = require('node:url')

const root = path.resolve(__dirname, '../..')
const apiPath = path.join(root, 'src/api/TikuComponentApi.js')
const reasonHelperPath = path.join(root, 'src/utils/questionArchiveReason.mjs')
const pagePath = path.join(root, 'src/pages/TikuComponent/index.vue')

// 预设选项的期望值。硬编码在这里（而不是从被测模块读）是有意的：
// 如果哪天有人把预设改成「违规 / 重复」这类缩写，后端 archiveReasonCode 的
// switch 就匹配不上、archived_reason 落进 admin_delete 默认分支，
// 归档列表按分类筛选永远筛不出来 —— 这条断言就是为了让那种改动变红。
const ARCHIVE_REASON_PRESETS_EXPECTED = ['内容错误', '重复题库', '违规内容', '其他']

// ---------------------------------------------------------------------------
// 装载真实 API 源码（与既有 utils/ticketHelpers.test.mjs 的 loadTicketApi 同款做法）
// ---------------------------------------------------------------------------

// 把 TikuComponentApi.js 的 ESM 转成可注入 request 的普通函数体。
// 只剥离 import/export 语法，函数体一字不改 —— 测的就是真实实现。
function loadTikuApi(request) {
  let source = fs.readFileSync(apiPath, 'utf8')
  // 先取出 buildDeleteQuestionParams 的真实实现，供源码里同名引用使用。
  const helperSource = fs.readFileSync(reasonHelperPath, 'utf8').replace(/export /g, '')
  source = source
    .replace(/import[\s\S]*?from\s*["'][^"']+["'];?\r?\n/g, '')
    .replace(/export function/g, 'function')
  const factory = new Function(
    'request',
    `${helperSource}\n${source}\nreturn { delQuestions };`
  )
  return factory(request)
}

// captureDelete 返回一个记录 axios config 的 request 替身。
function captureDelete() {
  const calls = []
  const request = config => {
    calls.push(config)
    return Promise.resolve({ status: 200, data: {} })
  }
  return { calls, request }
}

// ---------------------------------------------------------------------------
// ① 传输层：DELETE 必须带 params.reason
// ---------------------------------------------------------------------------

test('delQuestions 把 reason 放进 axios config 的 params（而不是丢在顶层）', async () => {
  const { calls, request } = captureDelete()
  const api = loadTikuApi(request)

  await api.delQuestions({ id: 7 }, '内容错误')

  assert.equal(calls.length, 1, '应当发出恰好一次请求')
  const config = calls[0]
  assert.equal(config.method, 'delete')
  assert.equal(config.url, '/adapter-service/questions/7')

  // 核心断言：reason 必须在 params 里。
  // 若实现写成 `{ reason }`（顶层），axios 会把它当成未知 config 键静默丢弃，
  // 后端 c.Query("reason") 拿到空串 → 400。这条断言正是拦这个的。
  assert.deepEqual(config.params, { reason: '内容错误' })

  // 反向断言：顶层不能出现 reason（否则就是那个经典错误写法）。
  assert.equal(config.reason, undefined, 'reason 不得出现在 config 顶层')
})

test('delQuestions 归一化 reason：去首尾空格、保留中间空格', async () => {
  const { calls, request } = captureDelete()
  const api = loadTikuApi(request)

  await api.delQuestions({ id: 12 }, '  重复  题库  ')
  assert.deepEqual(calls[0].params, { reason: '重复  题库' })
})

test('delQuestions 缺 reason 时不抛错，交给后端统一回 400', async () => {
  const { calls, request } = captureDelete()
  const api = loadTikuApi(request)

  // 不传 reason —— 前端不应在这里崩，后端才是权威校验点。
  await api.delQuestions({ id: 3 })
  assert.deepEqual(calls[0].params, { reason: '' })
})

// ---------------------------------------------------------------------------
// ② 页面行为：直接执行 index.vue 里的 deleteQuestion 源码
// ---------------------------------------------------------------------------

// 抽出 deleteQuestion 方法源码，并包装成对象字面量的一个方法。
//
// 只取这一个方法而不是挂载整个页面：页面 900+ 行、依赖十几个 composable，
// 挂载成本高且与本次改动无关。而「发不发请求、带什么 reason、提示什么文案」
// 全部收敛在这一个方法里，把它单独拎出来执行就能覆盖。
//
// 用「对象字面量 + 方法简写」包装而不是拼函数声明：方法里的 `this.xxx` 天然
// 指向包装对象，不必手工重写 this；也避免手工配对大括号（第一版就栽在这）。
function extractDeleteQuestion() {
  const source = fs.readFileSync(pagePath, 'utf8')
  const start = source.indexOf('    async deleteQuestion(id) {')
  assert.ok(start > 0, 'index.vue 里应当存在 async deleteQuestion(id) 方法')
  const endMarker = '\n    },'
  const end = source.indexOf(endMarker, start)
  assert.ok(end > start, 'deleteQuestion 方法体应当有闭合')
  // 取到 `}` 为止（不含结尾逗号），并去掉起始缩进。
  const body = source.slice(start, end + endMarker.length - 1).replace(/^\s+/, '')
  // 冒烟校验：包装后必须能被解析，否则说明抽取边界错了（早失败比后面逐个用例炸清楚）。
  assert.doesNotThrow(
    () => new Function(`return ({ ${body} });`),
    '抽出的 deleteQuestion 源码必须可被解析'
  )
  return body
}

// buildPageHarness 构造 deleteQuestion 的运行环境。
//
// 关键：依赖全部是**真实实现**（validateArchiveReason / normalizeArchiveReason 来自
// src/utils/questionArchiveReason.mjs），只有 Swal / delQuestions / message 是替身 ——
// 这样「校验阈值」与「请求参数」测的是与页面同一份代码，不会口径分叉。
async function buildPageHarness(options = {}) {
  const {
    swalValue = '内容错误',
    swalConfirmed = true,
    deleteStatus = 200,
    deleteMessage = '',
  } = options

  // ⚠️ Windows 下 import() 只接受 file:// URL（裸盘符路径会报
  // ERR_UNSUPPORTED_ESM_URL_SCHEME: Received protocol 'c:'），必须转成 URL。
  const reasonModule = await import(pathToFileURL(reasonHelperPath).href)
  const { validateArchiveReason, normalizeArchiveReason, ARCHIVE_REASON_PRESETS } = reasonModule

  const deleteCalls = []
  const prompts = []
  const successes = []
  const errors = []
  let fetchCount = 0

  const harness = {
    deleteCalls,
    prompts,
    successes,
    errors,
    get fetchCount() {
      return fetchCount
    },
    captureQuestionOwner: () => () => true,
    // 记录弹窗配置（断言文案与 input 设置），并返回预设的 Swal 结果。
    showQuestionPrompt: async promptOptions => {
      prompts.push(promptOptions)
      return swalConfirmed ? { isConfirmed: true, value: swalValue } : { isDismissed: true }
    },
    selectedRowKeys: [7, 8],
    fetchData: async () => {
      fetchCount += 1
    },
    // 被测的 delQuestions 替身：记录 (data, reason) 两个入参的真实形态。
    delQuestions: async (data, reason) => {
      deleteCalls.push({ data, reason })
      return { status: deleteStatus, message: deleteMessage, data: {} }
    },
    message: {
      success: text => successes.push(text),
      error: text => errors.push(text),
    },
    // 注入到方法体里的自由变量。
    deps: { validateArchiveReason, normalizeArchiveReason, ARCHIVE_REASON_PRESETS },
  }
  return harness
}

// 执行抽出来的 deleteQuestion 方法。
async function runDeleteQuestion(harness, id) {
  const body = extractDeleteQuestion()

  // 方法体里的 `this.xxx` 指向 harness；自由变量通过 new Function 的参数注入。
  const host = new Function(
    'delQuestions',
    'message',
    'validateArchiveReason',
    'normalizeArchiveReason',
    'ARCHIVE_REASON_PRESETS',
    `return ({ ${body} });`
  )(
    harness.delQuestions,
    harness.message,
    harness.deps.validateArchiveReason,
    harness.deps.normalizeArchiveReason,
    harness.deps.ARCHIVE_REASON_PRESETS
  )
  return host.deleteQuestion.call(harness, id)
}

test('页面：取消删除时不发请求、不改选中、不刷新列表', async () => {
  const harness = await buildPageHarness({ swalConfirmed: false })
  await runDeleteQuestion(harness, 7)

  assert.equal(harness.deleteCalls.length, 0, '取消后不得发出删除请求')
  assert.deepEqual(harness.selectedRowKeys, [7, 8], '取消后选中行不应变化')
  assert.equal(harness.fetchCount, 0, '取消后不应刷新列表')
})

test('页面：原因不足 2 字符时弹窗自己拦下（inputValidator 返回提示）', async () => {
  const harness = await buildPageHarness()
  await runDeleteQuestion(harness, 7)

  // 弹窗必须配置 input + inputValidator：校验发生在 Swal 里，
  // 不合法时弹窗保持打开、不发请求。这里断言配置存在且确实会拒绝短原因。
  const prompt = harness.prompts[0]
  assert.ok(prompt, '应当弹出删除确认框')
  assert.equal(prompt.input, 'text', '必须收集原因（input: text）')
  assert.equal(typeof prompt.inputValidator, 'function', '必须配置 inputValidator')

  // 与后端 archiveReasonMinLength = 2 对齐：1 个字符拒绝，2 个字符通过。
  assert.equal(prompt.inputValidator(''), '请填写删除原因（至少 2 个字符）')
  assert.equal(prompt.inputValidator('   '), '请填写删除原因（至少 2 个字符）')
  assert.equal(prompt.inputValidator('重'), '请填写删除原因（至少 2 个字符）')
  assert.equal(prompt.inputValidator('重复'), null, '2 个汉字应当通过（按字符不按字节）')
  assert.equal(prompt.inputValidator('  重复  '), null, '去空格后长度够即可')

  // 预设下拉必须与后端 archiveReasonCode 的四类逐字对齐。
  assert.deepEqual(prompt.inputOptions.presets, ARCHIVE_REASON_PRESETS_EXPECTED)
})

test('页面：确认后把原因作为第二个参数传给 delQuestions', async () => {
  const harness = await buildPageHarness({ swalValue: '  违规内容 ' })
  await runDeleteQuestion(harness, 7)

  assert.equal(harness.deleteCalls.length, 1, '确认后应当发出一次删除请求')
  const call = harness.deleteCalls[0]
  assert.deepEqual(call.data, { id: 7 })
  // 归一化后（去首尾空格）传给 api 层，由 api 层放进 params。
  assert.equal(call.reason, '违规内容')
})

test('页面：后端返回非 200 时提示失败且不刷新列表', async () => {
  const harness = await buildPageHarness({ deleteStatus: 400, deleteMessage: '请填写删除原因（至少 2 个字符）' })
  await runDeleteQuestion(harness, 7)

  assert.equal(harness.fetchCount, 0, '删除失败不应刷新列表')
  // 固定文案「删除失败」，不回显后端 message —— 与既有 TikuPrincipalLifecycleAnt4 的
  // TF02 断言（HTTP_REJECTED_DELETE 必须在页面上出现「删除失败」）保持同一口径。
  assert.deepEqual(harness.errors, ['删除失败'])
})

test('页面：删除成功后文案改为「已归档」，并与软删除口径一致', async () => {
  const harness = await buildPageHarness()
  await runDeleteQuestion(harness, 7)

  assert.deepEqual(harness.successes, ['已归档'], '成功提示应与「归档」而非「删除」口径一致')
  assert.equal(harness.fetchCount, 1, '删除成功后应刷新列表')
  assert.deepEqual(harness.selectedRowKeys, [8], '成功删除的那一行应从选中集合移除')

  // 文案缺陷 2 的钉子：弹窗不得再写「不可撤销」。
  const prompt = harness.prompts[0]
  assert.ok(
    !String(prompt.text).includes('不可撤销'),
    `删除确认文案不得再写「不可撤销」（Q77-1 已改为软删除可恢复），实际：${prompt.text}`
  )
  assert.ok(
    String(prompt.text).includes('恢复'),
    `删除确认文案应说明可从归档列表恢复，实际：${prompt.text}`
  )
})
