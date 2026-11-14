// 脚本激活码管理页的纯逻辑契约。
//
// 必须钉住的四件事（都是「实现坏了会静默出错」的类型）：
//  1. 批量上限与后端一致且在超限时**拒绝**而不是静默 clamp；
//  2. expired 是派生状态，四种状态都有可读文案；
//  3. 列表里的掩码不能被当成全码展示；
//  4. 使用记录时间线按时间排序且动作文案正确（客服要按时间读）。

import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  BATCH_LIMIT,
  statusMeta,
  normalizeBatchCount,
  masksCode,
  buildUsageTimeline,
  summarizeUsage,
  parseExpiryInput,
} from './scriptActivationCodes.mjs'

test('批量数量：合法值通过，边界与非法值全部拒绝', () => {
  assert.equal(normalizeBatchCount(1).ok, true)
  assert.equal(normalizeBatchCount(BATCH_LIMIT).ok, true, '上限值本身必须允许')
  assert.equal(normalizeBatchCount(BATCH_LIMIT).count, BATCH_LIMIT)

  // 超限必须拒绝，且**不能**返回被 clamp 后的值 —— 否则管理员以为生成了 200 个。
  const over = normalizeBatchCount(BATCH_LIMIT + 1)
  assert.equal(over.ok, false)
  assert.equal(over.count, 0, '被拒绝时不能返回 clamp 后的数量')
  assert.match(over.message, new RegExp(String(BATCH_LIMIT)))

  for (const invalid of [0, -1, 1.5, Number.NaN, Infinity, 'abc', null, undefined]) {
    const result = normalizeBatchCount(invalid)
    assert.equal(result.ok, false, `${String(invalid)} 应被拒绝`)
  }
})

test('状态展示：四种状态都有中文文案，未知状态不崩', () => {
  for (const status of ['unused', 'used', 'disabled', 'expired']) {
    const meta = statusMeta(status)
    assert.ok(meta.label && meta.label !== status, `${status} 应有中文文案`)
    assert.ok(meta.color, `${status} 应有颜色`)
  }
  // 未知/空状态要能安全降级（后端将来加状态时前端不应崩）。
  assert.ok(statusMeta('').label)
  assert.ok(statusMeta('something-new').label)
})

test('掩码识别：列表项只带 codeMask 时必须判定为掩码', () => {
  assert.equal(masksCode({ codeMask: '****cdef' }), true)
  // 详情返回了全码时不算掩码。
  assert.equal(masksCode({ codeMask: '****cdef', code: 'TKA-cdef' }), false)
  assert.equal(masksCode({}), false)
  assert.equal(masksCode(null), false)
})

test('使用时间线：按时间升序，动作文案可读', () => {
  const usages = [
    { kind: 'run', at: '2026-01-02T10:00:00Z', username: 'u1', scriptName: 's1' },
    { kind: 'redeem', at: '2026-01-01T10:00:00Z', username: 'u1', scriptName: 's1' },
    { kind: 'download', at: '2026-01-01T12:00:00Z', username: 'u1', scriptName: 's1' },
  ]
  const timeline = buildUsageTimeline(usages)
  assert.deepEqual(timeline.map(item => item.kind), ['redeem', 'download', 'run'])
  assert.deepEqual(timeline.map(item => item.label), ['兑换激活码', '下载脚本', '运行脚本'])
  // 空输入返回空数组（不是 undefined，前端 v-for 需要可迭代）。
  assert.deepEqual(buildUsageTimeline([]), [])
  assert.deepEqual(buildUsageTimeline(null), [])
})

test('详情结论：未使用 / 已使用各有明确一句话', () => {
  const unused = summarizeUsage({ code: { id: 1 }, hasUsage: false, usages: [] })
  assert.equal(unused.used, false)
  assert.match(unused.text, /尚未被使用/)

  const used = summarizeUsage({
    code: { id: 2 },
    hasUsage: true,
    usages: [
      { kind: 'redeem', at: '2026-01-01T10:00:00Z', username: 'alice', scriptName: '题库脚本' },
      { kind: 'run', at: '2026-01-01T11:00:00Z', username: 'alice' },
    ],
  })
  assert.equal(used.used, true)
  assert.match(used.text, /alice/)
  assert.match(used.text, /题库脚本/)
  assert.match(used.text, /1 条后续记录/)
})

test('有效期解析：空 = 永久，可解析 = ISO，不可解析必须拒绝而非当永久', () => {
  // 空值三种形态都表示「永不过期」。
  for (const empty of ['', '   ', null, undefined]) {
    const result = parseExpiryInput(empty)
    assert.equal(result.ok, true)
    assert.equal(result.value, null, '空值必须解析成 null（永久）')
  }

  const parsed = parseExpiryInput('2026-12-31 23:59:59')
  assert.equal(parsed.ok, true)
  assert.ok(typeof parsed.value === 'string' && parsed.value.includes('T'), '应归一化成 ISO 串')

  // 关键反例：不可解析的输入**不能**被当成「永久」——
  // 那会让管理员以为自己设了期限，实际发了一张永不过期的码。
  const invalid = parseExpiryInput('不是日期')
  assert.equal(invalid.ok, false)
  assert.equal(invalid.value, null)
  assert.ok(invalid.message, '拒绝时必须给出可读原因')
})

test('BATCH_LIMIT 是正整数（与后端 100 对齐）', () => {
  assert.equal(Number.isInteger(BATCH_LIMIT), true)
  assert.equal(BATCH_LIMIT, 100)
})
