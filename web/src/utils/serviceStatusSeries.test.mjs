/* 脱敏逐分钟序列的前端行为测试。
 *
 * 两条语义必须分开，不能合并成一个「有没有数据」的布尔：
 *   series 端点失败/格式不认  → 不画曲线（页面退回摘要），seriesView = null
 *   端点正常但窗口内没有点    → 画「刚重启、还没采样」的空态，seriesView.isEmpty = true
 * 混成一种，用户就分不清「服务刚起」和「端点坏了」。
 *
 * 期望值一律写字面量（'--'、0、长度数字），不引用被测模块的常量，
 * 否则常量漂移时期望值与被测值同步变，断言恒真、变异不红。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildServiceStatusSeriesView,
  buildServiceStatusView,
  formatMinuteLabel,
  isSupportedServiceStatusSeries,
  loadServiceStatusSeries,
  loadServiceStatusSummary,
  SERVICE_STATUS_ADMIN_PATH,
  SERVICE_STATUS_SERIES_PATH,
  SERVICE_STATUS_SUMMARY_PATH,
} from './serviceStatusSummary.mjs'

/** 一份合法的 series 响应体（字段对齐 controller.publicStatusPoint）。 */
function seriesPayload(overrides = {}) {
  return {
    schemaVersion: 1,
    points: [
      { minute: 29900000, totalRequests: 100, successRequests: 98, avgLatencyMs: 240 },
      { minute: 29900001, totalRequests: 50, successRequests: 25, avgLatencyMs: 890 },
      { minute: 29900002, totalRequests: 200, successRequests: 200, avgLatencyMs: 210 },
    ],
    capacity: 1440,
    retained: 3,
    restartedAt: '2026-11-14T06:00:00+08:00',
    ...overrides,
  }
}

test('series 端点常量与 summary / admin 是三个不同端点', () => {
  assert.equal(SERVICE_STATUS_SERIES_PATH, '/api/service-status/series')
  assert.equal(SERVICE_STATUS_SUMMARY_PATH, '/api/service-status/summary')
  assert.equal(SERVICE_STATUS_ADMIN_PATH, '/api/admin/service-status')
  assert.notEqual(SERVICE_STATUS_SERIES_PATH, SERVICE_STATUS_ADMIN_PATH)
})

test('有数据：三个点都能转成可渲染行，柱高按峰值归一', () => {
  const series = buildServiceStatusSeriesView(seriesPayload())

  assert.ok(series, '合法响应体必须产出视图')
  assert.equal(series.points.length, 3)
  assert.equal(series.isEmpty, false)
  assert.equal(series.retainedText, '3')
  assert.equal(series.capacityText, '1,440')

  // 峰值 200 → 中点 100 是 50%，50 是 25%，200 是 100%。
  assert.deepEqual(series.points.map((point) => point.barHeight), [50, 25, 100])
  // 成功率按点重算。
  assert.deepEqual(series.points.map((point) => point.rateText), ['98%', '50%', '100%'])
  // 明细字段可读。
  assert.equal(series.points[0].totalText, '100')
  assert.equal(series.points[1].latencyText, '890 毫秒')
})

test('全失败的点：成功率是 0% 而不是占位，页面据此标危险色', () => {
  const series = buildServiceStatusSeriesView(seriesPayload({
    points: [{ minute: 29900000, totalRequests: 40, successRequests: 0, avgLatencyMs: 1890 }],
    retained: 1,
  }))
  assert.equal(series.points[0].rateText, '0%')
  assert.notEqual(series.points[0].rateText, '--')
})

test('空序列：isEmpty=true 且 points 是空数组（不补 0 桶）', () => {
  const series = buildServiceStatusSeriesView(seriesPayload({ points: [], retained: 0 }))
  assert.ok(series, '空 points 仍是合法响应')
  assert.equal(series.isEmpty, true)
  assert.equal(series.points.length, 0)
  assert.equal(series.retainedText, '0')
  // capacity 是上限，不随数据变化。
  assert.equal(series.capacityText, '1,440')
})

test('series 视图不透出失败分类字段（即使后端某天加回来）', () => {
  const series = buildServiceStatusSeriesView(seriesPayload())
  const text = JSON.stringify(series)
  for (const forbidden of ['authFailures', 'upstreamFailures', 'timeoutFailures', 'quotaFailures', 'failedRequests']) {
    assert.ok(!text.includes(forbidden), `序列视图不得包含 ${forbidden}`)
  }
})

test('格式不认的响应返回 null（不画曲线），不是空视图', () => {
  for (const bad of [null, undefined, [], 'x', 42, {}, { schemaVersion: 2, points: [] }, { schemaVersion: 1 }]) {
    assert.equal(buildServiceStatusSeriesView(bad), null, `应拒绝: ${JSON.stringify(bad)}`)
  }
  // 点里缺字段也算不支持 —— 宁可退回摘要，也不画一条字段不全的曲线。
  assert.equal(isSupportedServiceStatusSeries(seriesPayload({
    points: [{ minute: 1, totalRequests: 1 }],
  })), false)
  // 负数不合法。
  assert.equal(isSupportedServiceStatusSeries(seriesPayload({
    points: [{ minute: 1, totalRequests: -1, successRequests: 0, avgLatencyMs: 0 }],
  })), false)
})

test('分钟标签按分钟数换算成 HH:MM', () => {
  // 29900000 分钟 ≈ 2026 年某个时刻；只断言形状，不锁具体时区偏移。
  assert.match(formatMinuteLabel(29900000), /^\d{2}:\d{2}$/)
  assert.equal(formatMinuteLabel(null), '--')
  assert.equal(formatMinuteLabel(-1), '--')
})

test('只请求 series 端点，失败也不回退 admin', async () => {
  const calls = []
  const result = await loadServiceStatusSeries(async (path) => {
    calls.push(path)
    return { status: 200, data: seriesPayload() }
  })
  assert.equal(result.ok, true)
  assert.deepEqual(calls, ['/api/service-status/series'])
  assert.ok(!calls.some((path) => path.includes('admin')))
})

test('series 报错时返回 ok:false / series:null，不抛异常（页面不能整页崩）', async () => {
  for (const response of [{ status: 500 }, { status: 403 }, { status: 404 }]) {
    const calls = []
    const result = await loadServiceStatusSeries(async (path) => {
      calls.push(path)
      return response
    })
    assert.equal(result.ok, false, `status=${response.status} 应判失败`)
    assert.equal(result.series, null)
    assert.ok(result.message.length > 0)
    assert.deepEqual(calls, ['/api/service-status/series'], '失败后只允许请求一次')
    assert.ok(!calls.some((path) => path.includes('admin')))
  }
  // 抛异常的形态同样要落成失败结果。
  const thrown = await loadServiceStatusSeries(async () => {
    throw Object.assign(new Error('network'), { response: { status: 502 } })
  })
  assert.equal(thrown.ok, false)
  assert.equal(thrown.series, null)
})

test('summary 与 series 是独立请求：一个失败不影响另一个的判定', async () => {
  // 摘要成功、曲线挂掉 —— 这是最常见的降级形态，必须仍能拿到摘要。
  const summary = await loadServiceStatusSummary(async () => ({
    status: 200,
    data: {
      schemaVersion: 1, status: 'healthy', successRate: 0.99, successRateAvailable: true,
      totalRequests: 200, successRequests: 198, failedRequests: 2, avgLatencyMs: 230,
      windowMinutes: 5, retained: 3, capacity: 1440, restartedAt: '2026-11-14T06:00:00+08:00',
    },
  }))
  const series = await loadServiceStatusSeries(async () => ({ status: 500 }))

  assert.equal(summary.ok, true)
  assert.equal(series.ok, false)
  // 摘要的「全失败 vs 无数据」语义不受曲线失败影响。
  const view = buildServiceStatusView(summary.data)
  assert.equal(view.dataState, 'measured')
  assert.equal(view.rateText, '99%')
})

test('「全失败≠无数据」在加入 series 后没有回归', () => {
  const base = {
    schemaVersion: 1, successRate: 0, totalRequests: 50, successRequests: 0,
    failedRequests: 50, avgLatencyMs: 1890, windowMinutes: 5, retained: 1,
    capacity: 1440, restartedAt: '2026-11-14T06:00:00+08:00',
  }
  const allFailed = buildServiceStatusView({ ...base, status: 'down', successRateAvailable: true })
  const empty = buildServiceStatusView({ ...base, status: 'unknown', successRateAvailable: false, totalRequests: 0, failedRequests: 0 })

  assert.equal(allFailed.dataState, 'measured')
  assert.equal(allFailed.rateText, '0%')
  assert.equal(allFailed.statusLabel, '异常')
  assert.equal(allFailed.emptyHint, '')

  assert.equal(empty.dataState, 'empty')
  assert.equal(empty.statusLabel, '暂无采样数据')
  assert.equal(empty.rateText, '--')
  assert.notEqual(allFailed.dataState, empty.dataState)
})
