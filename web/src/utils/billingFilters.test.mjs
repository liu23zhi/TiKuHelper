import test from 'node:test'
import assert from 'node:assert/strict'

import {
  BILLING_FILTER_FIELDS,
  BILLING_RECORD_TYPE_VALUES,
  BILLING_SEARCH_MODE_OPTIONS,
  BILLING_TYPE_OPTIONS,
  buildBillingFilterParams,
  buildBillingFilters,
  buildBillingTypeLabel,
  buildPersonalBillingParams,
  countBillingActiveFilters,
  createBillingFilters,
  hasBillingActiveFilters,
  normalizeBillingKeyword,
  resetBillingFilters,
} from './billingFilters.mjs'

// ─────────────────────── 类型取值（B1-4 / B3）───────────────────────

test('账单类型取值恰好是后端真实写入的 9 个，不含自造值', () => {
  // 这 9 个值来自 BillingRecordList 原有 typeOptions，并与后端
  // internal/service/billing_record.go 里真实写入的 type 一一对应。
  // 写字面量（而不是引用常量自比）是为了让「有人改错了其中一个」必然变红 ——
  // 期望值若也从同一常量取，常量漂移时两侧同步漂移，断言恒真。
  assert.deepEqual([...BILLING_RECORD_TYPE_VALUES], [
    'normal_search',
    'recharge',
    'registration_bonus',
    'balance_adjustment',
    'referral_rebate',
    'referral_rebate_reversal',
    'review_rebate',
    'script_reservation',
    'script_refund',
  ])
  assert.equal(BILLING_RECORD_TYPE_VALUES.length, 9, '不多不少 9 个')
  // 选项与取值必须一一对应：多一个选项就是自造值，少一个就是漏筛。
  assert.deepEqual(BILLING_TYPE_OPTIONS.map(option => option.value), [...BILLING_RECORD_TYPE_VALUES])
})

test('类型选项文案与页面原有文案逐条一致', () => {
  // 文案漂移同样是缺陷：同一种类型在两页显示不同中文名，用户会以为是两种东西。
  assert.deepEqual(BILLING_TYPE_OPTIONS.map(option => `${option.value}=${option.label}`), [
    'normal_search=题目消费',
    'recharge=充值入账',
    'registration_bonus=新用户赠送',
    'balance_adjustment=管理员调整余额',
    'referral_rebate=邀请返佣',
    'referral_rebate_reversal=邀请返佣冲回',
    'review_rebate=评价返现',
    'script_reservation=脚本预扣',
    'script_refund=脚本退款',
  ])
})

test('类型选项是冻结的，调用方不能就地改出第二份', () => {
  assert.equal(Object.isFrozen(BILLING_TYPE_OPTIONS), true)
  assert.equal(Object.isFrozen(BILLING_TYPE_OPTIONS[0]), true)
  assert.throws(() => { BILLING_TYPE_OPTIONS.push({ label: '自造', value: 'made_up' }) }, TypeError)
  assert.throws(() => { BILLING_TYPE_OPTIONS[0].value = 'made_up' }, TypeError)
})

test('搜索模式选项同样是共享的单一份定义', () => {
  assert.deepEqual(BILLING_SEARCH_MODE_OPTIONS.map(option => option.value), ['normal', 'deep', 'deep_native', 'deep_mcp'])
  assert.equal(Object.isFrozen(BILLING_SEARCH_MODE_OPTIONS), true)
})

// ─────────────────────── 筛选条件与参数 ───────────────────────

test('新建筛选条件是 9 个已知字段的空值，且互不共享引用', () => {
  const first = createBillingFilters()
  const second = createBillingFilters()
  assert.deepEqual(Object.keys(first).sort(), [...BILLING_FILTER_FIELDS].sort())
  first.keyword = 'mutated'
  assert.equal(second.keyword, '', '两次调用不能返回同一个对象，否则一页的筛选会污染另一页')
})

test('筛选参数剔除空值，页码由显式参数决定', () => {
  assert.deepEqual(buildBillingFilterParams({}, 1, 10), { page: 1, pageSize: 10 })
  assert.deepEqual(
    buildBillingFilterParams({ keyword: '  alice  ', type: 'recharge', createdFrom: '2026-11-01', createdTo: '', requestId: null }, 2, 20),
    { keyword: 'alice', type: 'recharge', createdFrom: '2026-11-01', page: 2, pageSize: 20 },
  )
  // filters 里混进 page/pageSize 不得覆盖显式参数。
  assert.deepEqual(buildBillingFilterParams({ page: 99, pageSize: 999 }, 3, 50), { page: 3, pageSize: 50 })
})

test('筛选参数丢弃未知字段（不把无关 query 透传给后端）', () => {
  assert.deepEqual(
    buildBillingFilterParams({ keyword: 'x', 未知维度: 'y', scope: 'self', role: 'admin' }, 1, 10),
    { keyword: 'x', page: 1, pageSize: 10 },
  )
})

test('已启用条件数只数有效值', () => {
  assert.equal(countBillingActiveFilters({}), 0)
  assert.equal(countBillingActiveFilters(createBillingFilters()), 0)
  assert.equal(countBillingActiveFilters({ keyword: '', type: undefined, createdFrom: '  ' }), 0)
  assert.equal(countBillingActiveFilters({ keyword: 'x', type: 'recharge' }), 2)
  assert.equal(hasBillingActiveFilters({ keyword: 'x' }), true)
  assert.equal(hasBillingActiveFilters({}), false)
})

test('重置保留对象身份，以免 v-model 绑定失效', () => {
  const filters = buildBillingFilters({ keyword: 'x', type: 'recharge', createdFrom: '2026-11-01' })
  const same = resetBillingFilters(filters)
  assert.equal(same, filters, '必须返回同一个对象')
  assert.deepEqual(filters, createBillingFilters())
})

test('关键词归一化与后端口径一致（去空白 + 截断）', () => {
  assert.equal(normalizeBillingKeyword('  abc  '), 'abc')
  assert.equal(normalizeBillingKeyword(''), '')
  assert.equal(normalizeBillingKeyword(null), '')
  assert.equal(normalizeBillingKeyword('a'.repeat(120)).length, 100)
})

// ─────────────────────── 个人使用页的 scope 约束（B5）───────────────────────

test('个人使用页参数必须始终带 scope=self，且不能被筛选条件覆盖', () => {
  // B5 判据：去掉 user_id / scope 条件普通用户就会拿到全站账单。
  // 后端 controller/billing_record.go:34-36 读到 scope=self 才强制降级角色。
  assert.deepEqual(buildPersonalBillingParams({}, 1, 10), { page: 1, pageSize: 10, scope: 'self' })
  assert.deepEqual(
    buildPersonalBillingParams({ keyword: 'x', type: 'recharge' }, 2, 20),
    { keyword: 'x', type: 'recharge', page: 2, pageSize: 20, scope: 'self' },
  )
  // 调用方误把整个 query 对象当 filters 传入时，scope 也不得被改掉。
  assert.equal(buildPersonalBillingParams({ scope: 'admin' }, 1, 10).scope, 'self')
  assert.equal(buildPersonalBillingParams({ scope: '' }, 1, 10).scope, 'self')
  assert.equal(buildPersonalBillingParams({ scope: undefined }, 1, 10).scope, 'self')
})

test('个人使用页参数的分页归一化与既有 helper 完全一致', () => {
  // 不重新实现一遍：非法 page/pageSize 必须与 buildPersonalUsageBillingRecordParams 同结果。
  for (const [page, pageSize] of [[0, 0], [-1, -1], ['x', 'y'], [3, 20]]) {
    const actual = buildPersonalBillingParams({}, page, pageSize)
    assert.deepEqual({ page: actual.page, pageSize: actual.pageSize, scope: actual.scope },
      { page: Number(page) > 0 ? Number(page) : 1, pageSize: Number(pageSize) > 0 ? Number(pageSize) : 10, scope: 'self' },
      `page=${String(page)} pageSize=${String(pageSize)}`)
  }
})

// ─────────────────────── 类型文案 ───────────────────────

test('类型文案未知值原样返回，不编造中文名', () => {
  assert.equal(buildBillingTypeLabel('recharge'), '充值入账')
  assert.equal(buildBillingTypeLabel('normal_search'), '题目消费')
  assert.equal(buildBillingTypeLabel('future_type'), 'future_type')
  assert.equal(buildBillingTypeLabel(''), '')
  assert.equal(buildBillingTypeLabel(null), '')
})
