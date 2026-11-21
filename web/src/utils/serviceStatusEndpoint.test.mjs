/* 接口族分桶（search / other）的前端行为测试（task_doc/138 §4.2.2 / §4.3 / §4.4）。
 *
 * 本文件存在的全部理由是这一个判据：
 *
 *   samples === 0                → 没有任何采样数据 → 置灰 + 不显示任何百分比
 *   samples > 0, totalRequests=0 → 采样到了、这套接口没人用 → 不置灰、成功率 '--'
 *   samples > 0, totalRequests>0 → 真实成功率（含 0%，即全失败）→ 不置灰
 *
 * 前两者 totalRequests 都是 0，只有 samples 能分开；后两者 samples 都 > 0，
 * 只有 totalRequests 能分开。所以任何一个字段单独用都会错，必须两者都用。
 *
 * 期望值一律**写字面量**（'--'、'0%'、true/false、'unsampled'），
 * 不引用被测模块导出的常量或状态名，否则常量/枚举漂移时期望值与被测值同步变化，
 * 断言恒真、变异不红（本项目 client_kind 那轮踩过：FAIL_COUNT 从 3 修到 7）。
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildEndpointView,
  buildServiceStatusEndpointsView,
  buildServiceStatusSeriesView,
  buildServiceStatusView,
} from './serviceStatusSummary.mjs'

/** 一份合法的 summary 响应体（含分桶），字段对齐 internal/controller。 */
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
    endpoints: {
      search: { totalRequests: 120, successRequests: 118, avgLatencyMs: 90, samples: 12 },
      other: { totalRequests: 80, successRequests: 80, avgLatencyMs: 400, samples: 12 },
    },
    ...overrides,
  }
}

/** 一个 search 桶（overrides 覆盖其中的字段）。 */
function bucket(overrides = {}) {
  return { totalRequests: 120, successRequests: 118, avgLatencyMs: 90, samples: 12, ...overrides }
}

test('有采样有请求：measured，成功率是真实值，不置灰', () => {
  const view = buildEndpointView('search', bucket())

  assert.equal(view.state, 'measured')
  assert.equal(view.greyed, false)
  assert.equal(view.rateText, '98.33%')
  assert.equal(view.samples, 12)
  assert.equal(view.totalRequests, 120)
  assert.equal(view.samplesText, '12')
  assert.equal(view.totalText, '120')
  assert.equal(view.latencyText, '90 毫秒')
  assert.equal(view.hint, '')
  assert.equal(view.statusLabel, '正常')
  assert.equal(view.tone, 'ok')
})

test('搜题接口状态按自身成功率分档，不受全站 other 接口成功率影响', () => {
  const degraded = buildEndpointView('search', bucket({ totalRequests: 100, successRequests: 94 }))
  const down = buildEndpointView('search', bucket({ totalRequests: 100, successRequests: 69 }))
  const healthyBoundary = buildEndpointView('search', bucket({ totalRequests: 100, successRequests: 95 }))
  const degradedBoundary = buildEndpointView('search', bucket({ totalRequests: 10, successRequests: 7 }))

  assert.equal(healthyBoundary.statusLabel, '正常')
  assert.equal(degraded.statusLabel, '降级')
  assert.equal(degraded.tone, 'warn')
  assert.equal(degradedBoundary.statusLabel, '降级')
  assert.equal(down.statusLabel, '异常')
  assert.equal(down.tone, 'danger')
})

test('★S3：有采样但成功率恰好 0% → measured、0%、且**不置灰**', () => {
  // 这是最该报警的状态：采样到了请求，全部失败。
  const view = buildEndpointView('search', bucket({
    totalRequests: 50, successRequests: 0, avgLatencyMs: 1890, samples: 4,
  }))

  // ① 必须是 measured，绝不是 unsampled。
  assert.equal(view.state, 'measured')
  assert.notEqual(view.state, 'unsampled')
  // ② 必须正常显示 0%，不是占位。
  assert.equal(view.rateText, '0%')
  assert.notEqual(view.rateText, '--')
  // ③ 不得置灰 —— 显灰会让「服务全挂」看起来像「没数据」。
  assert.equal(view.greyed, false)
  // ④ 不得出现「暂无…采样数据」这类文案。
  assert.equal(view.hint, '')
  assert.doesNotMatch(view.hint, /暂无/)
  assert.equal(view.statusLabel, '异常')
  assert.equal(view.tone, 'danger')
})

test('★S2：samples=0 且 totalRequests=0 → unsampled、置灰、绝不显示 0%', () => {
  const view = buildEndpointView('search', bucket({
    totalRequests: 0, successRequests: 0, avgLatencyMs: 0, samples: 0,
  }))

  assert.equal(view.state, 'unsampled')
  assert.equal(view.greyed, true)
  // 关键：置灰时**不能**显示 0% —— 那会让人以为服务挂了。
  assert.equal(view.rateText, '--')
  assert.notEqual(view.rateText, '0%')
  assert.match(view.hint, /暂无搜题接口采样数据/)
  assert.match(view.hint, /重启后从零开始/)
  assert.equal(view.samplesText, '0')
  assert.equal(view.statusLabel, '暂无采样数据')
  assert.equal(view.tone, 'muted')
})

test('★S2 对照组：samples=0 但 totalRequests>0 → 仍必须置灰（不能用 totalRequests 判）', () => {
  // 这个形态在真实系统里不该出现（有请求就必然有采样），但它是**辨别实现**的关键：
  // 只用 totalRequests===0 判灰的实现会在这里给出「不置灰 + 编造成功率」，必然红。
  // 反之若实现只用 samples 判，这里正确显灰。
  const view = buildEndpointView('search', bucket({
    totalRequests: 99, successRequests: 90, avgLatencyMs: 10, samples: 0,
  }))

  assert.equal(view.state, 'unsampled', 'samples=0 就是没有采样数据，与 totalRequests 无关')
  assert.equal(view.greyed, true)
  assert.equal(view.rateText, '--', '没有采样就不该给出任何百分比')
  assert.notEqual(view.rateText, '90.91%')
})

test('★samples>0 且 totalRequests=0 → idle：采样到了但没人用，不置灰且不显示 0%', () => {
  // 这是「服务正常运行、只是暂时没人搜题」。显灰会成为误报，显示 0% 也会。
  const view = buildEndpointView('search', bucket({
    totalRequests: 0, successRequests: 0, avgLatencyMs: 0, samples: 7,
  }))

  assert.equal(view.state, 'idle')
  assert.notEqual(view.state, 'unsampled')
  assert.equal(view.greyed, false, '采样到了就不该置灰')
  assert.equal(view.rateText, '--', '0 请求算不出成功率，不能伪造 0%')
  assert.notEqual(view.rateText, '0%')
  assert.equal(view.samplesText, '7')
  assert.match(view.hint, /没有搜题请求/)
  assert.doesNotMatch(view.hint, /暂无搜题接口采样数据/)
  assert.equal(view.statusLabel, '暂无请求')
  assert.equal(view.tone, 'muted')
})

test('三态必须互不相同：unsampled / idle / measured 在两两对照上都可区分', () => {
  const unsampled = buildEndpointView('search', bucket({ totalRequests: 0, samples: 0 }))
  const idle = buildEndpointView('search', bucket({ totalRequests: 0, samples: 7 }))
  const measured = buildEndpointView('search', bucket({ totalRequests: 10, successRequests: 10, samples: 7 }))

  // state / greyed / rateText / hint 四个维度都要能分开至少一种情况。
  assert.notEqual(unsampled.state, idle.state)
  assert.notEqual(unsampled.state, measured.state)
  assert.notEqual(idle.state, measured.state)

  assert.notEqual(unsampled.greyed, idle.greyed)
  assert.notEqual(unsampled.greyed, measured.greyed)
  // idle 与 measured 都不置灰 —— 它们的差别在 rateText。
  assert.equal(idle.greyed, measured.greyed)
  assert.notEqual(idle.rateText, measured.rateText)

  assert.notEqual(unsampled.hint, idle.hint)
})

test('缺桶 / 桶非对象 → unsampled 且置灰（不抛异常，页面不能整页崩）', () => {
  for (const bad of [undefined, null, [], 'x', 42, {}]) {
    const view = buildEndpointView('search', bad)
    assert.equal(view.state, 'unsampled', `应判 unsampled: ${JSON.stringify(bad)}`)
    assert.equal(view.greyed, true)
    assert.equal(view.rateText, '--')
    assert.equal(view.totalText, '--')
  }
  // {} 里 samples 缺字段 → 同样显灰（fail-closed）。
  const missingSamples = buildEndpointView('search', { totalRequests: 10, successRequests: 10, avgLatencyMs: 5 })
  assert.equal(missingSamples.greyed, true, 'samples 缺失必须 fail-closed 显灰')
  assert.equal(missingSamples.rateText, '--')
})

test('samples 是布尔之外的整数：数字按原值保留，非法值 fail-closed', () => {
  assert.equal(buildEndpointView('search', bucket({ samples: 0 })).samples, 0)
  assert.equal(buildEndpointView('search', bucket({ samples: 1440 })).samples, 1440)
  // 负数 samples 不合法 → 显灰。
  assert.equal(buildEndpointView('search', bucket({ samples: -1 })).greyed, true)
  // 字符串数字按数字解读（后端偶有 string 化的兼容需求）。
  assert.equal(buildEndpointView('search', bucket({ samples: '3' })).samples, 3)
})

test('summary 视图必须带出 endpoints.search（S4 前端面）', () => {
  const view = buildServiceStatusView(payload())

  assert.ok(view.endpoints, 'view 必须带 endpoints')
  assert.ok(view.endpoints.search, 'view.endpoints.search 必须存在')
  assert.equal(view.endpoints.search.state, 'measured')
  assert.equal(view.endpoints.search.rateText, '98.33%')
  assert.equal(view.endpoints.search.greyed, false)

  // 序列化后字段名必须是契约里的名字（页面读的是这些）。
  const text = JSON.stringify(view.endpoints)
  for (const key of ['search', 'other', 'samples', 'totalRequests']) {
    assert.ok(text.includes(key), `分桶视图缺少字段 ${key}`)
  }
})

test('summary 缺 endpoints 字段（旧后端）→ 显灰而不是抛异常', () => {
  const view = buildServiceStatusView(payload({ endpoints: undefined }))
  assert.equal(view.endpoints.search.greyed, true)
  assert.equal(view.endpoints.search.state, 'unsampled')
  // 顶层其它字段照常渲染（不受影响）。
  assert.equal(view.dataState, 'measured')
  assert.equal(view.rateText, '99%')
})

test('summary 顶层 measured 但 search 桶 unsampled：两者互不影响（分桶是独立维度）', () => {
  // 这是上线初期的真实形态：只有 other 有采样，search 的分桶源还没接上。
  const view = buildServiceStatusView(payload({
    endpoints: {
      search: { totalRequests: 0, successRequests: 0, avgLatencyMs: 0, samples: 0 },
      other: { totalRequests: 200, successRequests: 198, avgLatencyMs: 230, samples: 12 },
    },
  }))

  // 顶层：measured（有请求、成功率 99%）。
  assert.equal(view.dataState, 'measured')
  assert.equal(view.rateText, '99%')
  // search 桶：置灰。
  assert.equal(view.endpoints.search.greyed, true)
  assert.equal(view.endpoints.search.rateText, '--')
  // other 桶：正常。
  assert.equal(view.endpoints.other.greyed, false)
  assert.equal(view.endpoints.other.rateText, '99%')
  assert.notEqual(view.endpoints.search.greyed, view.endpoints.other.greyed)
})

test('格式不受支持的响应体：endpoints 也要给出可渲染的置灰视图', () => {
  for (const bad of [null, undefined, [], 'text', 42, { schemaVersion: 2 }, { schemaVersion: 1 }]) {
    const view = buildServiceStatusView(bad)
    assert.equal(view.supported, false)
    assert.ok(view.endpoints && view.endpoints.search, '不支持的响应体也要有 endpoints.search 供模板读取')
    assert.equal(view.endpoints.search.greyed, true)
    assert.equal(view.endpoints.search.rateText, '--')
  }
})

test('S4：series 视图也带 endpoints（不能只改 summary 漏掉 series）', () => {
  const series = buildServiceStatusSeriesView({
    schemaVersion: 1,
    points: [{ minute: 29900000, totalRequests: 100, successRequests: 98, avgLatencyMs: 240 }],
    capacity: 1440,
    retained: 1,
    restartedAt: '2026-11-14T06:00:00+08:00',
    endpoints: {
      search: { totalRequests: 60, successRequests: 59, avgLatencyMs: 80, samples: 3 },
      other: { totalRequests: 40, successRequests: 40, avgLatencyMs: 400, samples: 3 },
    },
  })

  assert.ok(series, '合法响应体必须产出视图')
  assert.ok(series.endpoints, 'series 视图必须带 endpoints')
  assert.equal(series.endpoints.search.state, 'measured')
  assert.equal(series.endpoints.search.samples, 3)
  assert.equal(series.endpoints.search.greyed, false)
  // 与 summary 走同一套三态判定（不许两套口径）。
  assert.equal(series.endpoints.search.rateText, '98.33%')
})

test('S4：series 缺 endpoints 时同样显灰而不是抛异常', () => {
  const series = buildServiceStatusSeriesView({
    schemaVersion: 1,
    points: [],
    capacity: 1440,
    retained: 0,
    restartedAt: '2026-11-14T06:00:00+08:00',
  })
  assert.ok(series)
  assert.equal(series.endpoints.search.greyed, true)
  assert.equal(series.endpoints.search.rateText, '--')
})

test('buildServiceStatusEndpointsView 同时返回两个桶，且 kind 固定两个', () => {
  const endpoints = buildServiceStatusEndpointsView(payload())
  assert.deepEqual(Object.keys(endpoints).sort(), ['other', 'search'])
  assert.equal(endpoints.search.key, 'search')
  assert.equal(endpoints.other.key, 'other')
  // 标题是字面量，改名必须红（页面文案由它决定）。
  assert.equal(endpoints.search.label, '搜题接口')
  assert.equal(endpoints.other.label, '其它接口')
})

test('分桶视图不透出 failedRequests（与公开序列端点同一条取舍）', () => {
  const view = buildServiceStatusView(payload({
    endpoints: {
      search: { totalRequests: 10, successRequests: 9, failedRequests: 1, avgLatencyMs: 5, samples: 2 },
      other: { totalRequests: 5, successRequests: 5, failedRequests: 0, avgLatencyMs: 5, samples: 2 },
    },
  }))
  const text = JSON.stringify(view.endpoints)
  assert.ok(!text.includes('failedRequests'), '公开分桶视图不得包含 failedRequests')
  assert.ok(!text.includes('"failed'), '任何 failed* 字段都不该出现')
})
