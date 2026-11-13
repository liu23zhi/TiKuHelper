import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import {
  MEDIA_TYPE_OPTIONS,
  REF_FILTER_OPTIONS,
  SCAN_STATE_ROWS,
  buildListParams,
  buildScanStateRows,
  canDeleteBlob,
  describeCleanResult,
  describeRetention,
  formatCacheBytes,
  isValidSha256,
  readCacheCount,
  readSummary,
  scanStateText,
  usagePercent,
} from './ticketImageCacheHelpers.mjs'

// 工单图片缓存管理页的纯展示 helper 测试（T-12）。
//
// ⚠️ 本文件**刻意不 import vue**：`web/node_modules` 是空壳，
// `vue` / `vue-router` / `@vue/test-utils` 一个都加载不了。
// 因此页面里可被自动验证的逻辑全部放在 ticketImageCacheHelpers.mjs，
// index.vue 只负责取值与渲染。指标是这四个数字（判据 ④ 的四项）。

// ── ① 总占用：字节 → 人类可读 ────────────────────────────────────────────────

test('formatCacheBytes renders human readable sizes on the 1024 scale', () => {
  // 1024 进制（与后端 GiB/MiB 口径一致）。用 1000 进制实现会得到
  // 1 KB / 1 MB，本用例会红。
  assert.equal(formatCacheBytes(512), '512 B')
  assert.equal(formatCacheBytes(1024), '1 KiB')
  assert.equal(formatCacheBytes(1536), '1.5 KiB')
  assert.equal(formatCacheBytes(20 * 1024 * 1024), '20 MiB')
  assert.equal(formatCacheBytes(3 * 1024 ** 3), '3 GiB')
})

test('formatCacheBytes never renders NaN or throws on missing input', () => {
  // 判据 ④ 的一部分：空库 / 未加载时页面必须显示 0，不能显示 NaN。
  for (const value of [null, undefined, '', 'abc', -1, NaN, Infinity, {}, []]) {
    assert.equal(formatCacheBytes(value), '0 B', `input ${String(value)} must degrade to 0 B`)
  }
})

test('readCacheCount accepts numeric strings and truncates fractions', () => {
  // sqlite 的 SUM 在某些驱动下返回字符串，页面必须两种都吃。
  assert.equal(readCacheCount('2048'), 2048)
  assert.equal(readCacheCount(2048), 2048)
  assert.equal(readCacheCount(' 17 '), 17)
  assert.equal(readCacheCount(12.9), 12)
  assert.equal(readCacheCount(true), null)
  assert.equal(readCacheCount(-5), null)
  assert.equal(readCacheCount(null), null)
})

// ── 四项汇总：整个筛选范围（判据 ③ ④ 的取值来源） ────────────────────────────

test('readSummary exposes the four required numbers from a backend payload', () => {
  // 与 service.TicketImageCacheListResult 的真实形态一致（含非默认保留策略）。
  const body = {
    items: [{ sha256: 'a'.repeat(64), byteSize: 1000, refCount: 0 }],
    total: 5,
    page: 1,
    pageSize: 1,
    summary: {
      totalCount: 5,
      totalBytes: 31000,
      zeroRefCount: 3,
      scanStates: { pending: 0, scanning: 0, approved: 3, sensitive: 1, failed: 1, other: 0 },
      retention: { maxAgeDays: 90, maxTotalGiB: 5, cleanupIntervalMinutes: 15, maxTotalBytes: 5 * 1024 ** 3 },
    },
  }
  const summary = readSummary(body)
  // ① 总占用（人类可读 + 原始字节）
  assert.equal(summary.totalBytes, 31000)
  assert.equal(summary.totalBytesText, '30.27 KiB')
  // ② 待清理数
  assert.equal(summary.zeroRefCount, 3)
  // ③ 按 scan_state 分组（固定四项都在，含 0 值的 pending）
  const byKey = Object.fromEntries(summary.scanRows.map(row => [row.key, row.count]))
  assert.equal(byKey.pending, 0)
  assert.equal(byKey.approved, 3)
  assert.equal(byKey.sensitive, 1)
  assert.equal(byKey.failed, 1)
  // 四个固定项之和 = 总数（scanning/other 都是 0，不进列表）。
  assert.equal(summary.scanRows.reduce((sum, row) => sum + row.count, 0), 5)
  // ④ 三个保留策略值读的是库里的非默认值
  assert.equal(summary.retention.ageText, '90 天')
  assert.equal(summary.retention.sizeText, '5 GiB')
  assert.equal(summary.retention.intervalText, '15 分钟')
  assert.equal(summary.retention.limitBytes, 5 * 1024 ** 3)
})

test('readSummary reports zeros for an empty database without throwing', () => {
  // 判据 ④：后端在空库/未迁移时返回全 0，页面必须原样显示 0。
  const summary = readSummary({
    items: [],
    total: 0,
    page: 1,
    pageSize: 20,
    summary: {
      totalCount: 0,
      totalBytes: 0,
      zeroRefCount: 0,
      scanStates: {},
      retention: { maxAgeDays: 180, maxTotalGiB: 20, cleanupIntervalMinutes: 60, maxTotalBytes: 20 * 1024 ** 3 },
    },
  })
  assert.equal(summary.totalBytesText, '0 B')
  assert.equal(summary.zeroRefCount, 0)
  assert.equal(summary.totalCount, 0)
  assert.deepEqual(summary.scanRows.map(row => [row.key, row.count]), [
    ['pending', 0], ['approved', 0], ['sensitive', 0], ['failed', 0],
  ])
  // 保留策略仍然显示（配置在 system_settings 里，与 blobs 表无关）。
  assert.equal(summary.retention.ageText, '180 天')
  assert.equal(summary.usagePercent, 0)
})

test('readSummary degrades to zeros when the summary object is missing entirely', () => {
  // 后端少回 summary 时不能抛异常，也不能让四项变成 undefined（模板会渲染空白）。
  for (const body of [null, undefined, {}, { summary: null }, { summary: 'oops' }]) {
    const summary = readSummary(body)
    assert.equal(summary.totalBytesText, '0 B')
    assert.equal(summary.zeroRefCount, 0)
    assert.equal(summary.totalCount, 0)
    assert.equal(summary.scanRows.length, 4)
    assert.equal(summary.retention.ageText, '—')
    assert.equal(summary.retention.sizeText, '—')
    assert.equal(summary.retention.intervalText, '—')
  }
})

// ── ③ 按 scan_state 分组 ──────────────────────────────────────────────────────

test('buildScanStateRows always shows the four fixed buckets and hides empty extras', () => {
  const rows = buildScanStateRows({ pending: 2, approved: 7, sensitive: 1, failed: 0, scanning: 0, other: 0 })
  assert.deepEqual(rows.map(row => row.key), ['pending', 'approved', 'sensitive', 'failed'])
  assert.equal(rows.find(row => row.key === 'approved').count, 7)
  assert.equal(rows.find(row => row.key === 'failed').count, 0)

  // scanning / other 有值时出现：它们是「分项之和 != 总数」的解释项。
  const withExtras = buildScanStateRows({ approved: 1, scanning: 3, other: 2 })
  assert.deepEqual(withExtras.map(row => row.key), ['pending', 'approved', 'sensitive', 'failed', 'scanning', 'other'])
  assert.equal(withExtras.find(row => row.key === 'scanning').count, 3)
  assert.equal(withExtras.find(row => row.key === 'other').count, 2)
})

test('buildScanStateRows surfaces the backend other bucket and zero-fills missing keys', () => {
  // 未知 scan_state 由**后端**归入 other（见 service.buildTicketImageCacheSummary），
  // 页面要把它显示出来 —— 否则「分项之和 != 总数」就永远解释不清。
  const rows = buildScanStateRows({ approved: 1, other: 9 })
  assert.equal(rows.find(row => row.key === 'other').count, 9)
  // 缺失的键必须兜底成 0（渲染空白会让表格出现空洞）。
  const zeroed = buildScanStateRows({ approved: 1 })
  assert.equal(zeroed.find(row => row.key === 'pending').count, 0)
  assert.equal(zeroed.find(row => row.key === 'failed').count, 0)
})

test('scanStateText maps every known state and passes unknown ones through verbatim', () => {
  assert.equal(scanStateText('pending'), '待检测')
  assert.equal(scanStateText('scanning'), '检测中')
  assert.equal(scanStateText('approved'), '已通过')
  assert.equal(scanStateText('sensitive'), '判定敏感')
  assert.equal(scanStateText('failed'), '检测失败')
  assert.equal(scanStateText(''), '—')
  assert.equal(scanStateText(null), '—')
  // 未知状态原样显示（显示「未知」等于把排查线索删掉）。
  assert.equal(scanStateText('brand_new'), 'brand_new')
  // 六个桶都有对应文案，漏一个会红。
  assert.equal(SCAN_STATE_ROWS.length, 6)
})

// ── ④ 三个保留策略值 + 占比 ───────────────────────────────────────────────────

test('describeRetention never renders 0 for a missing setting', () => {
  // 0 天 / 0 GiB 会被读成「已经该把图片删光了」，而真实含义是「没读到配置」。
  const missing = describeRetention(null)
  assert.equal(missing.ageText, '—')
  assert.equal(missing.sizeText, '—')
  assert.equal(missing.intervalText, '—')
  assert.equal(missing.limitBytes, null)

  const partial = describeRetention({ maxAgeDays: 180 })
  assert.equal(partial.ageText, '180 天')
  assert.equal(partial.sizeText, '—')
  assert.equal(partial.limitBytes, null)
})

test('describeRetention derives the byte limit when only GiB is present', () => {
  const retention = describeRetention({ maxAgeDays: 30, maxTotalGiB: 20, cleanupIntervalMinutes: 60 })
  assert.equal(retention.limitBytes, 20 * 1024 ** 3)
  assert.equal(retention.sizeText, '20 GiB')
})

test('usagePercent reports null rather than a misleading ratio', () => {
  assert.equal(usagePercent(5, 10), 50)
  assert.equal(usagePercent(0, 10), 0)
  assert.equal(usagePercent(20, 10), 100)
  // 上限未知/为 0 是「没读到配置」，不是「已经爆了」。
  assert.equal(usagePercent(5, null), null)
  assert.equal(usagePercent(5, 0), null)
  assert.equal(usagePercent(null, 10), null)
})

// ── 判据 ① ②：ref_count 保护的前端闸门 ────────────────────────────────────────

test('canDeleteBlob blocks every item with a positive ref count', () => {
  // 与后端同一判据：refCount > 0 就不给删（真正的保护在后端）。
  assert.equal(canDeleteBlob({ refCount: 0 }), true)
  assert.equal(canDeleteBlob({ refCount: 1 }), false)
  assert.equal(canDeleteBlob({ refCount: 7 }), false)
  assert.equal(canDeleteBlob({ refCount: '3' }), false)
  assert.equal(canDeleteBlob({ refCount: 0, referenced: false }), true)
  // refCount 缺失时不能默认放行 —— 那等于「读不到 = 可以删」。
  assert.equal(canDeleteBlob({}), false)
  assert.equal(canDeleteBlob(null), false)
  assert.equal(canDeleteBlob({ refCount: -1 }), false)
})

// ── 判据 ③：「立即清理」两个数字必须分开 ──────────────────────────────────────

test('describeCleanResult keeps the two purge counts distinguishable', () => {
  const text = describeCleanResult({ available: true, agePurged: 1, sizePurged: 2 })
  // 合成一个 total 的实现会写成「共清理 3 条」，本断言会红。
  assert.match(text, /按时间清理 1 条/)
  assert.match(text, /按大小清理 2 条/)
  assert.doesNotMatch(text, /共清理 3 条/)

  // 只有一条策略生效时，两个数字仍各自出现（0 也要出现）。
  assert.equal(describeCleanResult({ available: true, agePurged: 0, sizePurged: 4 }), '按时间清理 0 条，按大小清理 4 条')
  assert.equal(describeCleanResult({ available: true, agePurged: 6, sizePurged: 0 }), '按时间清理 6 条，按大小清理 0 条')
})

test('describeCleanResult explains an unwired cleaner instead of pretending zero work', () => {
  // available=false 与「0/0 没有可清理项」完全不同形：前者要去找开发。
  const text = describeCleanResult({
    available: false,
    agePurged: 0,
    sizePurged: 0,
    message: '清理能力尚未接线（等待 T-11 的 PruneTicketAttachmentsByAge / PruneTicketAttachmentsBySize）',
  })
  assert.match(text, /尚未接线/)
  assert.match(text, /T-11/)
  assert.doesNotMatch(text, /按时间清理 0 条/)
})

// ── 请求参数与输入校验 ────────────────────────────────────────────────────────

test('buildListParams omits empty filters so the request log stays readable', () => {
  assert.deepEqual(buildListParams({ mediaType: '', scanState: '', refFilter: '', keyword: '' }), {})
  assert.deepEqual(buildListParams({}), {})
  assert.deepEqual(
    buildListParams({ mediaType: ' image/png ', scanState: 'approved', refFilter: 'zero', keyword: 'AB12' }),
    { mediaType: 'image/png', scanState: 'approved', refFilter: 'zero', keyword: 'ab12' },
  )
})

test('isValidSha256 accepts only 64 lowercase hex chars', () => {
  assert.equal(isValidSha256('a'.repeat(64)), true)
  assert.equal(isValidSha256('A'.repeat(64)), true) // 归一化后再校验
  assert.equal(isValidSha256('a'.repeat(63)), false)
  assert.equal(isValidSha256('a'.repeat(65)), false)
  assert.equal(isValidSha256('g'.repeat(64)), false)
  assert.equal(isValidSha256(''), false)
  assert.equal(isValidSha256(null), false)
  assert.equal(isValidSha256(undefined), false)
})

test('option lists cover the documented values', () => {
  assert.deepEqual(MEDIA_TYPE_OPTIONS.map(option => option.value),
    ['image/png', 'image/jpeg', 'image/gif', 'image/webp'])
  // refFilter 的取值必须与后端 TicketImageCacheRef* 常量一致。
  assert.deepEqual(REF_FILTER_OPTIONS.map(option => option.value), ['', 'zero', 'referenced'])
})

// ── 结构钉子：页面必须真的用这些 helper，且模板里有四个数字 ──────────────────

test('the page wires every helper result into the template', async () => {
  const source = await readFile(new URL('../pages/TicketImageCache/index.vue', import.meta.url), 'utf8')
  // 页面必须 import 这些 helper 而不是自己重写一份格式化（两份判据必然漂移）。
  for (const name of [
    'formatCacheBytes', 'readSummary', 'describeCleanResult', 'canDeleteBlob',
    'buildListParams', 'scanStateText', 'isValidSha256', 'MEDIA_TYPE_OPTIONS', 'REF_FILTER_OPTIONS',
  ]) {
    assert.match(source, new RegExp(`\\b${name}\\b`), `page must use ${name}`)
  }
  // 四个数字在模板里各有落点。
  assert.match(source, /totalBytesText/, '① 总占用')
  assert.match(source, /zeroRefCount/, '② 待清理数')
  assert.match(source, /scanRows/, '③ scan_state 分组')
  assert.match(source, /retention\.ageText/, '④ 保留策略-时间')
  assert.match(source, /retention\.sizeText/, '④ 保留策略-大小')
  // 「立即清理一次」按钮存在。
  assert.match(source, /立即清理一次/)
  assert.match(source, /cleanTicketImageCacheNow/)
})

test('the api module exposes exactly the four endpoints the backend provides', async () => {
  const source = await readFile(new URL('../api/TicketImageCacheApi.js', import.meta.url), 'utf8')
  // 四条路径必须与 router.go 待装配的原文一致。
  assert.match(source, /\/adapter-service\/admin\/ticket-image-cache'/)
  assert.match(source, /method: 'delete'/)
  assert.match(source, /batch-delete/)
  assert.match(source, /\/clean'/)
  // 错误提示交给页面（页面有持久失败态与重试入口）：四条请求各带一次。
  // 用带逗号的形态计数，避开文件头注释里那一处同名字样。
  assert.equal((source.match(/errorFeedback: 'local',/g) || []).length, 4)
})
