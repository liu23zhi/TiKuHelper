import test from 'node:test'
import assert from 'node:assert/strict'
import { buildRuntimeCharts, buildServiceStatusCharts, latencyIntervals } from './operationalCharts.mjs'
import { buildPortraitCharts } from './portraitCharts.mjs'
import { buildPortraitAllOptions } from '../pages/UserPortrait/portraitChartOptions.mjs'

test('latency intervals difference cumulative counters without double-counting and do not mutate response', () => {
  const buckets = [{ upperMillis: 5, count: 2 }, { upperMillis: 10, count: 5 }, { upperMillis: 9223372036854, count: 9 }]
  const snapshot = structuredClone(buckets)
  const result = latencyIntervals(buckets)
  assert.deepEqual(result.values, [2, 3, 4]); assert.deepEqual(result.labels, ['≤5ms', '5–10ms', '>10ms'])
  assert.equal(result.values.reduce((a, b) => a + b, 0), 9); assert.deepEqual(buckets, snapshot)
  assert.equal(latencyIntervals([{ upperMillis: 5, count: 5 }, { upperMillis: 10, count: 3 }]).empty, true)
})

test('service charts keep absent traffic latency and success-rate gaps, preserve actual zero observations', () => {
  const points = [
    { minute: 1, totalRequests: 0, successRequests: 0, failedRequests: 0, avgLatencyMs: 0 },
    { minute: 2, totalRequests: 4, successRequests: 3, failedRequests: 1, avgLatencyMs: 0 },
  ]
  const charts = buildServiceStatusCharts({ points, summary: { breakdown: { auth: 1, timeout: 0, quota: 0, upstream: 0 } } })
  const get = key => charts.find(chart => chart.key === key).options
  assert.deepEqual(get('requests').series[0].data, [0, 4])
  assert.deepEqual(get('latency').series[0].data, [null, 0])
  assert.deepEqual(get('successRate').series[0].data, [null, 75])
  assert.deepEqual(get('failures').series[0].data, [{ name: '认证失败', value: 1 }])
  assert.ok(buildServiceStatusCharts(null).every(chart => chart.options === null))
})

test('runtime charts use independent measured memory, cache counters, and current tasks', () => {
  const charts = buildRuntimeCharts({ http: { latencyCount: 2, buckets: [{ upperMillis: 5, count: 2 }] }, cache: { search: { hits: 3, misses: 2, shared: 1 } }, memory: { heapAllocBytes: 1048576, heapInuseBytes: 2097152, heapIdleBytes: 3145728, heapReleasedBytes: 4194304, stackInuseBytes: 5242880 }, tasks: { search: { running: 1, succeeded: 4, failed: 2 } } })
  const get = key => charts.find(chart => chart.key === key).options
  assert.deepEqual(get('memory').series[0].data, [1, 2, 3, 4, 5])
  assert.deepEqual(get('cache').series.map(item => item.data), [[3], [2], [1]])
  assert.deepEqual(get('tasks').series.map(item => item.data), [[1], [4], [2]])
  assert.equal(buildRuntimeCharts({ http: { latencyCount: 0, buckets: [] } }).find(chart => chart.key === 'memory').options, null)
  assert.deepEqual(buildRuntimeCharts(null), [])
})

test('portrait charts expose server daily and account buckets with no invented dates or categories', () => {
  const charts = buildPortraitCharts({ daily: [{ date: '2026-11-20', count: 3, amount: 1 }], userStatuses: [{ label: '正常', count: 2 }], userRoles: [{ label: '管理员', count: 1 }], registerSources: [{ label: '批量导入', count: 1 }], tickets: [{ label: '已解决', count: 4 }] })
  assert.deepEqual(charts.daily.labels, ['2026-11-20']); assert.deepEqual(charts.daily.values, [3])
  const options = buildPortraitAllOptions(charts, {})
  assert.deepEqual(options.userStatuses.series[0].data, [{ name: '正常', value: 2 }])
  assert.deepEqual(options.tickets.series[0].data, [{ name: '已解决', value: 4 }])
  assert.equal(options.registerRegions, null)
})
