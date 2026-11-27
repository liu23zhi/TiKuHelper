import { resolveFailureStatus, SERVICE_STATUS_SERIES_PATH } from './serviceStatusSummary.mjs'

export const SEARCH_HISTORY_BUCKET_COUNT = 288
const MINUTE_MS = 60_000
const BUCKET_MINUTES = 5
const UNKNOWN = { id: 'unknown', color: '#94a3b8', label: '暂无采样' }
const NO_REQUESTS = { id: 'none', color: '#d1d5db', label: '无请求' }
const RANGES = [
  [0.3, 't10', '#dc2626'], [0.5, 't30', '#ea580c'], [0.7, 't50', '#f97316'],
  [0.8, 't70', '#f59e0b'], [0.9, 't80', '#eab308'], [0.95, 't90', '#a3e635'],
  [0.97, 't95', '#65a30d'], [0.99, 't97', '#22c55e'], [1, 't99', '#16a34a'],
]

export function searchHistoryTier(rate) {
  if (rate === null || !Number.isFinite(rate)) return NO_REQUESTS
  if (rate === 0) return { id: 't0', color: '#7f1d1d', label: '全部失败' }
  if (rate === 1) return { id: 't100', color: '#15803d', label: '全部成功' }
  const [, id, color] = RANGES.find(([upper]) => rate < upper) || RANGES.at(-1)
  return { id, color, label: '部分失败' }
}

function safeCount(value) {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

// Only the dedicated search field proves the history's scope. Site-wide points
// must never be substituted, even if an older server returns a healthy series.
export function hasSearchStatusHistory(payload) {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)
    || payload.schemaVersion !== 1 || !Array.isArray(payload.searchPoints)
    || payload.searchPoints.length > 1440) return false
  const minutes = new Set()
  return payload.searchPoints.every(point => {
    if (!point || !safeCount(point.minute) || !safeCount(point.totalRequests)
      || !safeCount(point.successRequests) || !safeCount(point.avgLatencyMs)
      || point.successRequests > point.totalRequests
      || minutes.has(point.minute)) return false
    minutes.add(point.minute)
    return true
  })
}

const dates = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
})
const sampleDates = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
})
const localized = new Intl.NumberFormat('zh-CN')
const FAILURE_KEYS = ['upstreamFailures', 'timeoutFailures', 'authFailures', 'quotaFailures']
const MEMORY_KEYS = ['heapAllocBytes', 'heapSysBytes', 'sysBytes', 'stackInuseBytes', 'gcCycles']

export function parseStatusTimestamp(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(value)) return null
  const ms = Date.parse(value)
  return Number.isFinite(ms) && ms >= 0 ? ms : null
}
export function formatStatusTime(ms) {
  return Number.isFinite(ms) ? dates.format(ms).replace(/\//g, '-') : '—'
}
export const formatStatusCount = n => safeCount(n) ? localized.format(n) : '—'
export const formatStatusBytes = n => safeCount(n) ? `${localized.format(n)} B` : '—'
const formatMinute = minute => formatStatusTime(minute * MINUTE_MS)
const readCount = n => safeCount(n) ? n : null

function requestRows(series, first, last, administrator) {
  const points = new Map()
  const duplicate = new Set()
  if (!Array.isArray(series) || series.length > 1440) return points
  for (const point of series) {
    if (!safeCount(point?.minute) || point.minute < first || point.minute > last) continue
    if (points.has(point.minute)) {
      duplicate.add(point.minute)
      continue
    }
    if (!safeCount(point.totalRequests) || !safeCount(point.successRequests)
      || point.successRequests > point.totalRequests || !safeCount(point.avgLatencyMs)) continue
    const item = { minute: point.minute, totalRequests: point.totalRequests, successRequests: point.successRequests, avgLatencyMs: point.avgLatencyMs }
    if (administrator) for (const key of ['failedRequests', ...FAILURE_KEYS]) item[key] = readCount(point[key])
    points.set(point.minute, item)
  }
  for (const minute of duplicate) points.delete(minute)
  return points
}

// The provider's endpoint samples field can count historical observations.
// A five-minute overview gets its coverage from the same search series slots.
export function currentSearchSnapshot(payload) {
  const asOfMs = parseStatusTimestamp(payload?.asOf)
  if (asOfMs === null || !hasSearchStatusHistory(payload)) return null
  const last = Math.floor(asOfMs / MINUTE_MS)
  const restartMs = parseStatusTimestamp(payload.restartedAt)
  const first = Math.max(last - (BUCKET_MINUTES - 1), restartMs === null ? -Infinity : Math.floor(restartMs / MINUTE_MS))
  const rows = [...requestRows(payload.searchPoints, first, last, false).values()]
  if (!rows.length) return null
  const total = rows.reduce((sum, point) => sum + point.totalRequests, 0)
  const success = rows.reduce((sum, point) => sum + point.successRequests, 0)
  const bucket = payload.endpoints?.search
  if (!safeCount(bucket?.avgLatencyMs) || bucket.totalRequests !== total || bucket.successRequests !== success) return null
  return { totalRequests: total, successRequests: success, avgLatencyMs: bucket.avgLatencyMs, samples: rows.length }
}

function runtimeRows(series, first, last, asOfMs, restartMs) {
  const points = new Map()
  const duplicate = new Set()
  if (!Array.isArray(series) || series.length > 1440) return points
  for (const point of series) {
    const sampledAt = parseStatusTimestamp(point?.sampledAt)
    if (!safeCount(point?.minute) || point.minute < first || point.minute > last || sampledAt === null
      || Math.floor(sampledAt / MINUTE_MS) !== point.minute || sampledAt > asOfMs
      || (restartMs !== null && sampledAt < restartMs)) continue
    if (points.has(point.minute)) { duplicate.add(point.minute); continue }
    points.set(point.minute, {
      sampledAt: sampleDates.format(sampledAt), uptimeSeconds: readCount(point.uptimeSeconds), goroutines: readCount(point.goroutines),
      memory: Object.fromEntries(MEMORY_KEYS.map(key => [key, readCount(point.memory?.[key])])),
    })
  }
  for (const minute of duplicate) points.delete(minute)
  return points
}

function makeBucket(firstMinute, requests, runtime) {
  if (firstMinute === null) return { ...UNKNOWN, tier: UNKNOWN.id, label: '窗口未确认', detail: '暂无可确认时窗 · 暂无采样', minutes: [] }
  const minutes = Array.from({ length: BUCKET_MINUTES }, (_, offset) => {
    const minute = firstMinute + offset
    const request = requests.get(minute)
    const rate = request?.totalRequests ? request.successRequests / request.totalRequests : null
    const status = !request ? UNKNOWN.label : (rate === null ? '已采样无请求' : searchHistoryTier(rate).label)
    return { minute, time: formatMinute(minute), request, process: runtime.get(minute), status }
  })
  const observed = minutes.filter(row => row.request)
  const total = observed.reduce((sum, row) => sum + row.request.totalRequests, 0)
  const success = observed.reduce((sum, row) => sum + row.request.successRequests, 0)
  const rate = total ? success / total : null
  const tier = observed.length ? searchHistoryTier(rate) : UNKNOWN
  const label = `${minutes[0].time} – ${minutes.at(-1).time}`
  const message = total ? `${tier.label} · ${Number((rate * 100).toFixed(1))}% · ${localized.format(total)} 次请求`
    : (observed.length ? '已采样无请求' : UNKNOWN.label)
  return {
    startMs: firstMinute * MINUTE_MS, total, tier: tier.id, color: tier.color, label, minutes,
    detail: `${label} · ${message} · 覆盖 ${observed.length}/5 分钟`,
  }
}

// End at the current natural minute of the provider clock, not a browser
// timestamp or a clock-aligned five-minute boundary independent of the summary.
export function buildStatusHistory(payload, scope = 'search') {
  const administrator = scope === 'admin'
  const available = administrator
    ? payload?.schemaVersion === 1 && Array.isArray(payload.points) && payload.points.length <= 1440
    : hasSearchStatusHistory(payload)
  const asOfMs = available ? parseStatusTimestamp(payload.asOf) : null
  const restartMs = available ? parseStatusTimestamp(payload.restartedAt) : null
  const last = asOfMs === null ? null : Math.floor(asOfMs / MINUTE_MS)
  const first = last === null ? null : last - SEARCH_HISTORY_BUCKET_COUNT * BUCKET_MINUTES + 1
  const earliest = first === null ? null : Math.max(first, restartMs === null ? first : Math.floor(restartMs / MINUTE_MS))
  const requests = asOfMs === null ? new Map() : requestRows(administrator ? payload.points : payload.searchPoints, earliest, last, administrator)
  const runtime = administrator && asOfMs !== null ? runtimeRows(payload.processPoints, earliest, last, asOfMs, restartMs) : new Map()
  const buckets = Array.from({ length: SEARCH_HISTORY_BUCKET_COUNT }, (_, i) =>
    makeBucket(first === null ? null : first + i * BUCKET_MINUTES, requests, runtime))
  return {
    available: asOfMs !== null, buckets, series: [...requests.values()].sort((a, b) => a.minute - b.minute),
    updatedText: formatStatusTime(asOfMs),
    restartText: restartMs === null ? '' : `服务于 ${formatStatusTime(restartMs)} 启动或重启；重启后历史采样清零并重新积累。`,
    hint: asOfMs !== null
      ? '每格 5 个自然分钟。成功率仅按已采样请求计算；缺失分钟不计为零。'
      : '历史状态暂不可用：服务端未提供可确认时窗，不能据此判断是否有请求。',
    axis: first === null ? [] : [first, first + 360, first + 720, first + 1080, last].map(formatMinute),
  }
}

export function buildSearchStatusHistory(payload) {
  return buildStatusHistory(payload)
}

export async function loadSearchStatusHistory(fetchSeries) {
  try {
    const response = await fetchSeries(SERVICE_STATUS_SERIES_PATH)
    if (resolveFailureStatus(response) === 200 && hasSearchStatusHistory(response.data)) {
      return { ok: true, data: response.data }
    }
  } catch {
    // Optional history must not make the current summary disappear.
  }
  return { ok: false, data: null }
}
