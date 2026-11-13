import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  buildSensitiveWordCacheListParams,
  formatSensitiveWordCacheDate,
  formatSensitiveWordCacheWords,
  sensitiveWordCacheStateColor,
} from './sensitiveWordCacheHelpers.mjs'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const readSource = relativePath => readFile(path.join(__dirname, '..', relativePath), 'utf8')

// ─────────────────────── 三态筛选参数 ───────────────────────

test('list params keep only the three known states', () => {
  // 判据 ①：三态筛选。非法值必须被丢弃（不是下发给后端换一个 400）。
  for (const state of ['hit', 'clean', 'failed']) {
    assert.equal(buildSensitiveWordCacheListParams({ state }, 1, 20).state, state)
  }
  for (const state of ['', 'bogus', 'HITS', null, undefined, 0, {}]) {
    assert.equal(buildSensitiveWordCacheListParams({ state }, 1, 20).state, undefined)
  }
  // 大小写/空白归一化后仍合法。
  assert.equal(buildSensitiveWordCacheListParams({ state: '  HIT ' }, 1, 20).state, 'hit')
})

test('list params drop empty filters and invalid paging', () => {
  const empty = buildSensitiveWordCacheListParams({}, 0, 0)
  assert.deepEqual(empty, {})
  const trimmed = buildSensitiveWordCacheListParams(
    { keyword: '  boom  ', checkedFrom: '2026-01-01', checkedTo: ' 2026-02-01 ' }, 2, 50,
  )
  assert.deepEqual(trimmed, {
    keyword: 'boom', checkedFrom: '2026-01-01', checkedTo: '2026-02-01', page: 2, pageSize: 50,
  })
  // 负数/小数/非数字页码不下发，让后端用默认值。
  for (const page of [-1, 0, 1.5, 'abc', null]) {
    assert.equal(buildSensitiveWordCacheListParams({}, page, 20).page, undefined)
  }
})

// ─────────────────────── 展示 helper ───────────────────────

test('state colors distinguish hit / clean / failed', () => {
  const colors = new Set(['hit', 'clean', 'failed'].map(sensitiveWordCacheStateColor))
  assert.equal(colors.size, 3, '三态必须是三种不同颜色')
  assert.equal(sensitiveWordCacheStateColor('hit'), 'red')
  assert.equal(sensitiveWordCacheStateColor('clean'), 'green')
  assert.equal(sensitiveWordCacheStateColor('failed'), 'orange')
  assert.equal(sensitiveWordCacheStateColor('unknown'), 'default')
})

test('dates render in Shanghai time and unknown values stay visible', () => {
  // 2026-01-01T00:00:00Z == 上海 08:00:00。
  assert.match(formatSensitiveWordCacheDate('2026-01-01T00:00:00Z'), /08:00:00/)
  assert.equal(formatSensitiveWordCacheDate(''), '—')
  assert.equal(formatSensitiveWordCacheDate(null), '—')
  // 脏值原样透出，不伪装成 '—'（那样就看不出数据坏了）。
  assert.equal(formatSensitiveWordCacheDate('not-a-date'), 'not-a-date')
})

test('words never render as an empty string', () => {
  assert.equal(formatSensitiveWordCacheWords(['敏感', '违规']), '敏感、违规')
  assert.equal(formatSensitiveWordCacheWords([]), '—')
  assert.equal(formatSensitiveWordCacheWords(null), '—')
  assert.equal(formatSensitiveWordCacheWords(['', '  ']), '—')
})

// ─────────────────────── 页面接线（判据 ②） ───────────────────────

test('sensitive word page never renders an original-text column', async () => {
  const source = await readSource(path.join('pages', 'SensitiveWord', 'index.vue'))

  // 判据 ②：MaskedText 可看但原文不可看。
  // 这里断言的是「页面**构造不出**原文列」—— 数据源里根本没有原文字段，
  // 所以哪怕前端想显示也显示不出来（后端断言在 Go 测试里）。
  assert.match(source, /dataIndex: 'maskedText'/)
  for (const forbidden of ['dataIndex: \'question\'', 'dataIndex: \'answer\'', 'dataIndex: \'originalText\'', 'dataIndex: \'rawText\'']) {
    assert.equal(source.includes(forbidden), false, `页面不得出现原文列 ${forbidden}`)
  }
  // 页面必须显式告诉管理员"这里没有原文"，避免被当成漏显示。
  assert.match(source, /不保存题目\/答案原文/)
})

test('sensitive word page wires three-state filter and pagination to the admin API', async () => {
  const page = await readSource(path.join('pages', 'SensitiveWord', 'index.vue'))
  const api = await readSource(path.join('api', 'SensitiveWordApi.js'))

  assert.match(page, /SENSITIVE_WORD_CACHE_STATES/)
  assert.match(page, /getSensitiveWordCacheEntries/)
  assert.match(page, /getSensitiveWordCacheStatus/)
  assert.match(page, /buildSensitiveWordCacheListParams/)
  assert.match(page, /:pagination="pagination"/)
  assert.match(page, /@change="onTableChange"/)

  // 后端契约路径必须与 controller 一致。
  assert.match(api, /url: '\/adapter-service\/admin\/sensitive-word-cache'/)
  assert.match(api, /url: '\/adapter-service\/admin\/sensitive-word-cache\/status'/)
  // 三态枚举必须与后端 service.SensitiveWordCacheState* 对齐。
  assert.match(api, /SENSITIVE_WORD_CACHE_STATES = Object\.freeze\(\['hit', 'clean', 'failed'\]\)/)
  // 这两条接口是只读的：文件里不得出现 post/put/delete（缓存写入归 service 层）。
  assert.doesNotMatch(api, /method: 'post'/)
  assert.doesNotMatch(api, /method: 'put'/)
  assert.doesNotMatch(api, /method: 'delete'/)
})
