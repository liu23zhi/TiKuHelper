import { buildPortraitHoursOption, buildPortraitTypesOption, portraitSeriesPalette, portraitTooltipOption } from '../pages/UserPortrait/portraitChartOptions.mjs'

const finite = value => typeof value === 'number' && Number.isFinite(value) && value >= 0
const series = (labels, values) => ({ labels, values, empty: labels.length === 0 })
const histogram = (labels, values, palette, unit = '') => {
  const option = buildPortraitHoursOption(series(labels, values), palette)
  if (option) { option.yAxis.name = unit; option.series[0].name = unit || '次数' }
  return option
}
function grouped(labels, entries, palette, unit = '次') {
  if (!labels.length) return null
  const option = histogram(labels, entries[0].values, palette, unit)
  option.grid.top = 44
  option.legend = { top: 0, textStyle: { color: palette.textMuted } }
  const colors = portraitSeriesPalette(palette)
  option.series = entries.map((entry, i) => ({ name: entry.name, data: entry.values, type: 'bar', barMaxWidth: 24, itemStyle: { color: colors[i % colors.length] } }))
  return option
}
function timeline(points, entries, palette, unit) {
  if (!points.length) return null
  const option = grouped(points.map(point => new Date(point.minute * 60000).toLocaleTimeString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false, hour: '2-digit', minute: '2-digit' })), entries, palette, unit)
  option.tooltip = { trigger: 'axis', ...portraitTooltipOption(palette) }
  option.xAxis.axisLabel.interval = 'auto'
  option.xAxis.boundaryGap = false
  option.series = option.series.map(entry => ({ ...entry, type: 'line', showSymbol: false, connectNulls: false, smooth: false, sampling: 'lttb' }))
  return option
}

export function buildServiceStatusCharts(payload, palette = {}) {
  const points = Array.isArray(payload?.points) ? payload.points.filter(point => finite(point?.minute) && finite(point.totalRequests)).slice(-1440) : []
  const breakdown = payload?.summary?.breakdown
  const failureEntries = [['upstream', '上游失败'], ['timeout', '超时失败'], ['auth', '认证失败'], ['quota', '额度失败']].filter(([key]) => finite(breakdown?.[key]) && breakdown[key] > 0)
  return [
    { key: 'requests', title: '24 小时 API 请求趋势', hint: '按分钟展示全部保留采样点；服务重启前的数据不在当前进程中。', options: timeline(points, [
      { name: '请求', values: points.map(point => point.totalRequests) },
      { name: '成功', values: points.map(point => point.successRequests) },
      { name: '失败', values: points.map(point => point.failedRequests) },
    ], palette, '次') },
    { key: 'latency', title: '24 小时 API 平均延迟', hint: '没有请求的分钟保留为空，不将没有样本视为 0 毫秒。', options: timeline(points, [
      { name: '平均延迟', values: points.map(point => point.totalRequests > 0 ? point.avgLatencyMs : null) },
    ], palette, '毫秒') },
    { key: 'successRate', title: '24 小时 API 成功率', hint: '成功率按每分钟成功请求 / 请求总数计算，没有请求时不计算。', options: timeline(points, [
      { name: '成功率', values: points.map(point => point.totalRequests > 0 ? Number((point.successRequests / point.totalRequests * 100).toFixed(2)) : null) },
    ], palette, '%') },
    { key: 'failures', title: '当前窗口失败分类', hint: '显示服务端当前统计窗口的失败事件；不是账务或用户量。', options: buildPortraitTypesOption(series(failureEntries.map(([, label]) => label), failureEntries.map(([key]) => breakdown[key])), palette) },
  ]
}

// The server publishes cumulative latency buckets. Differencing yields mutually exclusive intervals.
export function latencyIntervals(buckets) {
  if (!Array.isArray(buckets)) return series([], [])
  const valid = buckets.filter(bucket => finite(bucket?.upperMillis) && finite(bucket.count)).slice().sort((a, b) => a.upperMillis - b.upperMillis)
  let prior = 0, lower = 0
  const labels = [], values = []
  for (let i = 0; i < valid.length; i += 1) {
    const bucket = valid[i]
    if (bucket.count < prior) return series([], [])
    labels.push(i === valid.length - 1 && bucket.upperMillis > 86400000 ? `>${lower}ms` : i === 0 ? `≤${bucket.upperMillis}ms` : `${lower}–${bucket.upperMillis}ms`)
    values.push(bucket.count - prior)
    lower = bucket.upperMillis; prior = bucket.count
  }
  return series(labels, values)
}

export function buildRuntimeCharts(metrics, palette = {}) {
  if (!metrics) return []
  const latency = latencyIntervals(metrics.http?.buckets)
  const cacheNames = ['search', 'fallback'].filter(key => metrics.cache?.[key])
  const tasks = ['search', 'fallback'].filter(key => metrics.tasks?.[key])
  const memory = metrics.memory
  return [
    { key: 'latencyBuckets', title: '请求耗时分布', hint: '累计桶已转换为互不重叠的区间，每个完成请求只计一次。', options: metrics.http?.latencyCount > 0 ? histogram(latency.labels, latency.values, palette, '请求数') : null },
    { key: 'cache', title: '缓存结果分布', hint: '命中、未命中与共享执行复用分别展示，不推算上游 Token 缓存。', options: grouped(cacheNames.map(key => key === 'search' ? '搜索缓存' : '回退缓存'), [
      { name: '命中', values: cacheNames.map(key => metrics.cache[key].hits) },
      { name: '未命中', values: cacheNames.map(key => metrics.cache[key].misses) },
      { name: '共享复用', values: cacheNames.map(key => metrics.cache[key].shared) },
    ], palette) },
    { key: 'memory', title: 'Go 进程内存快照', hint: '各项为独立的运行时指标，存在包含关系，不能相加；不代表主机或容器 RSS。', options: memory ? histogram(['堆已分配', '堆使用中', '堆空闲', '堆已归还', '栈使用中'], [memory.heapAllocBytes, memory.heapInuseBytes, memory.heapIdleBytes, memory.heapReleasedBytes, memory.stackInuseBytes].map(value => Number((value / 1048576).toFixed(2))), palette, 'MiB') : null },
    { key: 'tasks', title: '当前保留任务状态', hint: '只统计当前保留的任务，不代表永久任务历史或队列容量。', options: grouped(tasks.map(key => key === 'search' ? '搜索任务' : '回退任务'), [
      { name: '运行中', values: tasks.map(key => metrics.tasks[key].running) },
      { name: '已成功', values: tasks.map(key => metrics.tasks[key].succeeded) },
      { name: '已失败', values: tasks.map(key => metrics.tasks[key].failed) },
    ], palette) },
  ]
}
