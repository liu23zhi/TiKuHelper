/* 服务状态页视图模型的行为测试。
 *
 * 这一批测的是**值域语义**，不是源码字符串：
 *   - 「全失败」与「无数据」在 successRate 上数值相同（都是 0），只靠数字分不开；
 *   - 唯一可靠的判据是 successRateAvailable 布尔。
 * 所以测试喂真实的响应体给真实函数，断言 dataState / 文案 / 空提示。
 *
 * 所有期望值写字面量（'暂无采样数据'、'0%'、'empty'、'measured'），
 * 不引用被测模块导出的常量 —— 否则常量漂移时期望值与被测值同步变，
 * 断言恒真，变异不会红。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildServiceStatusView,
  describeServiceStatusFailure,
  formatCount,
  formatLatency,
  formatRestartedAt,
  formatSuccessRate,
  isSupportedServiceStatusSummary,
  loadServiceStatusSummary,
  resolveFailureStatus,
  SERVICE_STATUS_ADMIN_PATH,
  SERVICE_STATUS_SUMMARY_PATH,
} from './serviceStatusSummary.mjs'

/** 一份合法的 summary 响应体（字段对齐 internal/controller/service_status.go）。 */
function payload(overrides = {}) {
  return {
    schemaVersion: 1,
    status: 'healthy',
    successRate: 0.99,
    successRateAvailable: true,
    totalRequests: 200,
    successRequests: 198,
    failedRequests: 2,
    avgLatencyMs: 230,
    windowMinutes: 5,
    retained: 12,
    capacity: 1440,
    restartedAt: '2026-11-14T07:30:00+08:00',
    ...overrides,
  }
}

test('端点常量：摘要路径与管理员路径必须是两个不同端点', () => {
  // 字面量断言：路径被改动时（比如误把页面指向 admin）必须红。
  assert.equal(SERVICE_STATUS_SUMMARY_PATH, '/api/service-status/summary')
  assert.equal(SERVICE_STATUS_ADMIN_PATH, '/api/admin/service-status')
  assert.notEqual(SERVICE_STATUS_SUMMARY_PATH, SERVICE_STATUS_ADMIN_PATH)
})

test('无数据：successRateAvailable=false → 「暂无采样数据」，成功率显示占位而非 0%', () => {
  const view = buildServiceStatusView(payload({
    successRateAvailable: false,
    successRate: 0,
    totalRequests: 0,
    successRequests: 0,
    failedRequests: 0,
    status: 'unknown',
  }))

  assert.equal(view.supported, true)
  // 关键判据：这是 empty 而不是 measured。
  assert.equal(view.dataState, 'empty')
  assert.equal(view.statusLabel, '暂无采样数据')
  assert.equal(view.tone, 'muted')
  // 无数据时不能显示「0%」—— 那会让人以为测到了请求且全失败。
  assert.equal(view.rateText, '--')
  assert.equal(view.successRateText, '--')
  // 空提示必须给出原因说明。
  assert.ok(view.emptyHint.length > 0, '无数据时必须给出解释文案')
  assert.match(view.emptyHint, /没有/)
})

test('全失败：successRateAvailable=true 且 successRate=0 → 是 measured（0%），绝不是「暂无采样数据」', () => {
  const view = buildServiceStatusView(payload({
    successRateAvailable: true,
    successRate: 0,
    // 真实后端在这种情形下推导出的就是 down（阈值 <70%）。
    status: 'down',
    totalRequests: 50,
    successRequests: 0,
    failedRequests: 50,
    avgLatencyMs: 1890,
  }))

  // ① 数据状态必须是 measured —— 这条就是「全失败 ≠ 无数据」的核心断言。
  assert.equal(view.dataState, 'measured')
  assert.notEqual(view.dataState, 'empty')

  // ② 成功率必须真实显示 0%，不是占位符。
  assert.equal(view.rateText, '0%')
  assert.equal(view.successRateText, '0%')
  assert.notEqual(view.rateText, '--')

  // ③ 状态文案不能是「暂无采样数据」（那是最该报警的状态被降级成"没数据"）。
  assert.notEqual(view.statusLabel, '暂无采样数据')
  assert.equal(view.statusLabel, '异常')
  assert.equal(view.tone, 'danger')

  // ④ 空提示必须为空 —— 出现「窗口内没有请求」这种话就是语义错了。
  assert.equal(view.emptyHint, '')

  // ⑤ 失败数必须真的展示出来，让用户看到「50 次全失败」。
  assert.equal(view.failedText, '50')
  assert.equal(view.totalText, '50')
  assert.equal(view.successText, '0')
})

test('全失败与无数据必须给出不同的视图（同一份数值 0 的两种语义不能合并）', () => {
  const empty = buildServiceStatusView(payload({
    successRateAvailable: false, successRate: 0, totalRequests: 0, failedRequests: 0, status: 'unknown',
  }))
  const allFailed = buildServiceStatusView(payload({
    successRateAvailable: true, successRate: 0, totalRequests: 50, failedRequests: 50, status: 'down',
  }))

  // successRate 两者都是 0，但视图必须在四个维度上都不同。
  assert.notEqual(empty.dataState, allFailed.dataState)
  assert.notEqual(empty.statusLabel, allFailed.statusLabel)
  assert.notEqual(empty.tone, allFailed.tone)
  assert.notEqual(empty.rateText, allFailed.rateText)
  assert.notEqual(empty.emptyHint, allFailed.emptyHint)
})

test('降级与正常的文案/语义色按后端 status 走，不由前端重算阈值', () => {
  const degraded = buildServiceStatusView(payload({ status: 'degraded', successRate: 0.8 }))
  assert.equal(degraded.statusLabel, '降级')
  assert.equal(degraded.tone, 'warn')
  assert.equal(degraded.rateText, '80%')

  const healthy = buildServiceStatusView(payload({ status: 'healthy', successRate: 1 }))
  assert.equal(healthy.statusLabel, '正常')
  assert.equal(healthy.tone, 'ok')
  assert.equal(healthy.rateText, '100%')
})

test('未知 status 取值：兜底为「未知状态」，不谎称正常也不谎称异常', () => {
  const view = buildServiceStatusView(payload({ status: 'maintenance' }))
  assert.equal(view.statusLabel, '未知状态')
  assert.equal(view.tone, 'muted')
  // 但仍然按 measured 展示数字（数据本身是有效的，只是状态词不认识）。
  assert.equal(view.dataState, 'measured')
})

test('格式不受支持的响应体：supported=false 且不编造任何数字', () => {
  for (const bad of [null, undefined, [], 'text', 42, {}, { schemaVersion: 2 }, { schemaVersion: 1 }]) {
    const view = buildServiceStatusView(bad)
    assert.equal(view.supported, false, `应拒绝: ${JSON.stringify(bad)}`)
    assert.equal(view.dataState, 'unknown')
    assert.equal(view.rateText, '--')
    assert.equal(view.totalText, '--')
    assert.equal(view.statusLabel, '未知状态')
  }
  // successRateAvailable 必须是布尔，缺字段不该被当成 false 悄悄放行。
  assert.equal(isSupportedServiceStatusSummary({ ...payload(), successRateAvailable: undefined }), false)
  assert.equal(isSupportedServiceStatusSummary({ ...payload(), successRateAvailable: 'true' }), false)
  // 负数计数不合法。
  assert.equal(isSupportedServiceStatusSummary({ ...payload(), totalRequests: -1 }), false)
})

test('摘要端点不返回 points，视图模型也不得暴露逐分钟字段', () => {
  const view = buildServiceStatusView(payload())
  const serialized = JSON.stringify(view)
  for (const forbidden of ['points', 'startMinute', 'endMinute', 'breakdown']) {
    assert.ok(!serialized.includes(forbidden), `摘要视图不得包含 ${forbidden}`)
  }
  // 失败的四种分类只在 admin 端点里有，摘要视图固定不展示。
  assert.equal(view.showBreakdown, false)
})

test('数字格式化：分辨率与占位符', () => {
  assert.equal(formatSuccessRate(0.9923), '99.23%')
  assert.equal(formatSuccessRate(1), '100%')
  assert.equal(formatSuccessRate(0), '0%')
  assert.equal(formatSuccessRate(0.7), '70%')
  assert.equal(formatSuccessRate(null), '--')
  assert.equal(formatSuccessRate('bad'), '--')

  assert.equal(formatLatency(230), '230 毫秒')
  assert.equal(formatLatency(0), '0 毫秒')
  assert.equal(formatLatency(1890), '1.9 秒')
  assert.equal(formatLatency(null), '--')

  assert.equal(formatCount(1440), '1,440')
  assert.equal(formatCount(0), '0')
  assert.equal(formatCount(null), '--')

  assert.equal(formatRestartedAt(''), '')
  assert.equal(formatRestartedAt('not-a-date'), '')
  assert.ok(formatRestartedAt('2026-11-14T07:30:00+08:00').length > 0)
})

test('restartedAt 与采样点/容量必须渲染（数据起点是本页的必备信息）', () => {
  const view = buildServiceStatusView(payload({ retained: 12, capacity: 1440 }))
  assert.ok(view.restartedAtText.length > 0, 'restartedAt 必须格式化出可读时间')
  assert.equal(view.retainedText, '12')
  assert.equal(view.capacityText, '1,440')
  assert.equal(view.windowText, '最近 5 分钟')
})

test('401/403 分流文案不同（页面内部要区分登录失效与权限不足）', () => {
  assert.match(describeServiceStatusFailure(401), /登录/)
  assert.match(describeServiceStatusFailure(403), /无权/)
  assert.notEqual(describeServiceStatusFailure(401), describeServiceStatusFailure(403))
  assert.match(describeServiceStatusFailure(500), /异常/)
  assert.match(describeServiceStatusFailure(0), /失败/)

  // 状态码提取要兼容 axios 响应、异常对象、裸数字三种形态。
  assert.equal(resolveFailureStatus(403), 403)
  assert.equal(resolveFailureStatus({ status: 401 }), 401)
  assert.equal(resolveFailureStatus({ response: { status: 500 } }), 500)
  assert.equal(resolveFailureStatus(new Error('boom')), 0)
})

test('只请求 summary 端点：绝不 fallback 到 admin 端点', async () => {
  const calls = []
  const result = await loadServiceStatusSummary(async (path) => {
    calls.push(path)
    return { status: 200, data: payload() }
  })

  assert.equal(result.ok, true)
  assert.deepEqual(calls, ['/api/service-status/summary'])
  // 最关键的一条：全程没有出现 admin 路径。
  assert.ok(!calls.includes(SERVICE_STATUS_ADMIN_PATH), '不得请求管理员端点')
  assert.ok(!calls.some((path) => path.includes('admin')), '任何请求路径都不得含 admin')
})

test('摘要请求失败时也不降级去要 admin 明细', async () => {
  const calls = []
  const result = await loadServiceStatusSummary(async (path) => {
    calls.push(path)
    return { status: 500, data: null }
  })

  assert.equal(result.ok, false)
  assert.equal(result.status, 500)
  assert.deepEqual(calls, ['/api/service-status/summary'], '失败后只允许请求一次 summary')
  assert.ok(!calls.some((path) => path.includes('admin')), '失败路径也不得请求管理员端点')
})

test('403 时给出「无权」文案而不是去试 admin 端点', async () => {
  const calls = []
  const result = await loadServiceStatusSummary(async (path) => {
    calls.push(path)
    return { status: 403, data: null }
  })
  assert.equal(result.ok, false)
  assert.equal(result.status, 403)
  assert.match(result.message, /无权/)
  assert.equal(calls.length, 1)
})

test('抛出异常（网络中断）也要落成失败结果，不冒泡到页面', async () => {
  const result = await loadServiceStatusSummary(async () => {
    throw Object.assign(new Error('network'), { response: { status: 502 } })
  })
  assert.equal(result.ok, false)
  assert.equal(result.status, 502)
  assert.ok(result.message.length > 0)
})

test('格式不受支持的 200 响应判为失败，不把脏数据画到页面上', async () => {
  const result = await loadServiceStatusSummary(async () => ({ status: 200, data: { schemaVersion: 9 } }))
  assert.equal(result.ok, false)
  assert.match(result.message, /格式/)
})

test('未提供 fetch 函数时安全返回失败（不抛异常）', async () => {
  const result = await loadServiceStatusSummary(undefined)
  assert.equal(result.ok, false)
  assert.equal(result.data, null)
})
