import test from 'node:test'
import assert from 'node:assert/strict'
import {
  buildSearchStatusHistory,
  hasSearchStatusHistory,
  loadSearchStatusHistory,
  searchHistoryTier,
} from './searchStatusHistory.mjs'

const NOW = Date.UTC(2026, 10, 20, 5, 2, 0)
const minute = Math.floor(NOW / 60000)

test('24小时色带固定288格，当前5分钟窗口可见，按请求数加权', () => {
  const view = buildSearchStatusHistory({ schemaVersion: 1, searchPoints: [
    { minute, totalRequests: 1, successRequests: 1, avgLatencyMs: 10 },
    { minute: minute - 1, totalRequests: 3, successRequests: 0, avgLatencyMs: 10 },
  ] }, NOW)
  assert.equal(view.available, true)
  assert.equal(view.buckets.length, 288)
  assert.equal(view.buckets.at(-1).tier, 't10')
  assert.match(view.buckets.at(-1).detail, /25% · 4 次请求/)
  assert.equal(view.buckets[0].tier, 'none')
})

test('会按时间戳过滤环形缓冲区中的 24 小时外旧点', () => {
  const oldMinute = minute - (24 * 60 + 10)
  const view = buildSearchStatusHistory({ schemaVersion: 1, searchPoints: [
    { minute: oldMinute, totalRequests: 100, successRequests: 0, avgLatencyMs: 10 },
    { minute, totalRequests: 1, successRequests: 1, avgLatencyMs: 10 },
  ] }, NOW)
  assert.equal(view.buckets.at(-1).tier, 't100')
  assert.ok(view.buckets.every((bucket, index) => index === 287 || bucket.total === 0))
})

test('全失败、全成功和无请求使用互不混淆的颜色档位', () => {
  assert.equal(searchHistoryTier(0).id, 't0')
  assert.equal(searchHistoryTier(1).id, 't100')
  assert.equal(searchHistoryTier(null).id, 'none')
  const empty = buildSearchStatusHistory({ schemaVersion: 1, searchPoints: [] }, NOW)
  assert.ok(empty.buckets.every(bucket => bucket.tier === 'none'))
  assert.match(empty.buckets.at(-1).detail, /无请求/)
})

test('严格拒绝旧全站points，缺搜题字段显示历史不可用', () => {
  assert.equal(hasSearchStatusHistory({ points: [] }), false)
  const view = buildSearchStatusHistory({ schemaVersion: 1, points: [{ minute, totalRequests: 99, successRequests: 99 }] }, NOW)
  assert.equal(view.available, false)
  assert.ok(view.buckets.every(bucket => bucket.tier === 'none'))
  assert.match(view.hint, /历史状态暂不可用/)
  assert.equal(hasSearchStatusHistory({ schemaVersion: 1, searchPoints: [{ minute, totalRequests: 1, successRequests: 1 }] }), false)
})

test('系列请求失败或字段不全不会影响摘要调用方', async () => {
  assert.deepEqual(await loadSearchStatusHistory(async () => ({ status: 503 })), { ok: false, data: null })
  assert.deepEqual(await loadSearchStatusHistory(async () => ({ status: 200, data: { schemaVersion: 1, points: [] } })), { ok: false, data: null })
  const result = await loadSearchStatusHistory(async () => ({ status: 200, data: { schemaVersion: 1, searchPoints: [] } }))
  assert.equal(result.ok, true)
  assert.deepEqual(result.data, { schemaVersion: 1, searchPoints: [] })
})
