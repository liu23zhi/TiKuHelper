import { resolveFailureStatus, SERVICE_STATUS_SERIES_PATH } from './serviceStatusSummary.mjs'

export const SEARCH_HISTORY_BUCKET_COUNT = 288
const BUCKET_MS = 5 * 60 * 1000
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
  return { id, color, label: '部分成功' }
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

function timeLabel(ms) {
  return new Date(ms).toLocaleString('zh-CN', {
    timeZone: 'Asia/Shanghai', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', hour12: false,
  }).replace(/\//g, '-')
}

export function buildSearchStatusHistory(payload, nowMs = Date.now()) {
  const now = Number.isFinite(nowMs) ? nowMs : Date.now()
  // Include this minute: a new search should not wait for the next five-minute
  // boundary before appearing. Both clients use the same aligned 288 buckets.
  const end = (Math.floor(now / BUCKET_MS) + 1) * BUCKET_MS
  const start = end - SEARCH_HISTORY_BUCKET_COUNT * BUCKET_MS
  const available = hasSearchStatusHistory(payload)
  const buckets = Array.from({ length: SEARCH_HISTORY_BUCKET_COUNT }, (_, index) => ({
    startMs: start + index * BUCKET_MS, total: 0, succeeded: 0,
  }))
  if (available) {
    for (const point of payload.searchPoints) {
      const ms = point.minute * 60000
      if (ms < start || ms > now) continue
      const index = Math.floor((ms - start) / BUCKET_MS)
      if (index < 0 || index >= buckets.length) continue
      buckets[index].total += point.totalRequests
      buckets[index].succeeded += point.successRequests
    }
  }
  for (const bucket of buckets) {
    const rate = bucket.total > 0 ? bucket.succeeded / bucket.total : null
    const tier = searchHistoryTier(rate)
    const percent = rate === null ? '' : `${Number((rate * 100).toFixed(1))}%`
    bucket.tier = tier.id
    bucket.color = tier.color
    bucket.label = `${timeLabel(bucket.startMs)} – ${timeLabel(Math.min(bucket.startMs + BUCKET_MS, now))}`
    bucket.detail = `${bucket.label} · ${!available ? '历史状态暂不可用' : (rate === null ? '无请求' : `${percent} · ${bucket.total.toLocaleString('zh-CN')} 次请求`)}`
  }
  return {
    available,
    buckets,
    hint: available
      ? '每格 5 分钟，按请求数加权统计成功率；灰色表示无请求。服务重启后历史会重新积累。'
      : '历史状态暂不可用。灰色为占位，不能据此判断是否有请求；请稍后刷新。',
  }
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
