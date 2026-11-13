import test from 'node:test'
import assert from 'node:assert/strict'

import {
  USER_USAGE_TAB_ORDER,
  USER_USAGE_DEFAULT_TAB,
  buildCurrentLevelText,
  buildDeviceLimitHint,
  buildDeviceLimitText,
  buildLevelMetricText,
  buildNextLevelRequirement,
  buildUserUsageTabs,
  pickNextLevel,
} from './userLevelHelpers.mjs'

// ─────────────────────── T-24：可用设备数量 ───────────────────────

test('device limit renders 0 as unlimited instead of "0 台"', () => {
  // 这是 T-24 判据 ① 的核心：后端 0 = 不限。
  // 变异方向：去掉 value === 0 的分支 → 这里会得到 "0 台" 而变红。
  assert.equal(buildDeviceLimitText(0), '不限')
  assert.notEqual(buildDeviceLimitText(0), '0 台')
})

test('device limit renders positive limits with the 台 unit', () => {
  assert.equal(buildDeviceLimitText(1), '1 台')
  assert.equal(buildDeviceLimitText(3), '3 台')
  assert.equal(buildDeviceLimitText(128), '128 台')
  // 小数是脏数据，向下取整而不是显示 "2.5 台"。
  assert.equal(buildDeviceLimitText(2.5), '2 台')
})

test('device limit keeps unknown values distinct from unlimited', () => {
  // 未加载 / 脏数据必须是 '—'：伪装成"不限"会让用户以为真的没限制。
  for (const value of [null, undefined, '', NaN, 'abc', -1, true, false]) {
    assert.equal(buildDeviceLimitText(value), '—', `value=${String(value)}`)
  }
})

test('device limit hint tells the truth about missing used-device data', () => {
  // 「已用设备数」当前确实不存在（device_limit 消费点 0 处、account_device_bindings 表不存在）。
  // 文案必须如实说明，而不是假装页面上有那个数。
  assert.match(buildDeviceLimitHint(3), /已用设备数暂不可用/)
  assert.match(buildDeviceLimitHint(0), /不限制可用设备数量/)
})

// ─────────────────────── T-26：Tab 顺序 ───────────────────────

test('user usage tab order puts recharge above level', () => {
  // 判据 ① 与判据 ④（变异：对调顺序 → 这条红）。
  assert.deepEqual(USER_USAGE_TAB_ORDER, ['recharge', 'level'])
  const tabs = buildUserUsageTabs()
  assert.deepEqual(tabs.map(tab => tab.key), [...USER_USAGE_TAB_ORDER])
  assert.equal(tabs[0].title, '充值')
  assert.equal(tabs[1].title, '等级提升')
  assert.equal(tabs[0].key, USER_USAGE_DEFAULT_TAB)
})

// ─────────────────────── T-26：等级提升面板 ───────────────────────

test('current level falls back to an explicit no-level label', () => {
  assert.equal(buildCurrentLevelText({ name: '基础会员' }), '基础会员')
  assert.equal(buildCurrentLevelText({ name: '  基础会员  ' }), '基础会员')
  assert.equal(buildCurrentLevelText({ name: '' }), '暂无等级')
  assert.equal(buildCurrentLevelText(null), '暂无等级')
})

test('level metrics expose both backend calibers and never invent numbers', () => {
  const summary = { rechargeTotal: 128.5, consumptionTotal: 0 }
  assert.deepEqual(buildLevelMetricText(summary, 'recharge'), { label: '累计充值', value: 128.5 })
  // 0 是有效值（真的没消费过），必须原样给出，不能变成 '—'。
  assert.deepEqual(buildLevelMetricText(summary, 'consumption'), { label: '累计消费', value: 0 })
  // 缺失值不能被当成 0。
  assert.deepEqual(buildLevelMetricText({}, 'recharge'), { label: '累计充值', value: '—' })
})

test('next level skips disabled levels and picks the nearest higher rank', () => {
  const levels = [
    { id: 1, name: '基础', rank: 1, enabled: true, minRechargeTotal: 0, minConsumptionTotal: 0 },
    { id: 3, name: '停用档', rank: 3, enabled: false, minRechargeTotal: 50, minConsumptionTotal: 0 },
    { id: 2, name: '进阶', rank: 2, enabled: true, minRechargeTotal: 100, minConsumptionTotal: 20 },
    { id: 4, name: '高级', rank: 4, enabled: true, minRechargeTotal: 500, minConsumptionTotal: 100 },
  ]
  // 当前 rank=1 → 最近一档是 rank=2（不是 rank=4，也不是停用的 rank=3）。
  const next = pickNextLevel(levels, { levelId: 1, rank: 1 })
  assert.equal(next?.id, 2)
  // 当前无等级 → 取启用级里 rank 最小的。
  assert.equal(pickNextLevel(levels, { levelId: 0, rank: 0 })?.id, 1)
  // 已到最高启用级 → null（界面显示"已是最高等级"，而不是空门槛表）。
  assert.equal(pickNextLevel(levels, { levelId: 4, rank: 4 }), null)
})

test('next level requirement reports gaps for both calibers', () => {
  const levels = [
    { id: 2, name: '进阶', rank: 2, enabled: true, minRechargeTotal: 100, minConsumptionTotal: 20 },
  ]
  const req = buildNextLevelRequirement(levels, {
    levelId: 1, rank: 1, rechargeTotal: 30, consumptionTotal: 25,
  })
  assert.equal(req.available, true)
  assert.equal(req.minRechargeTotal, 100)
  assert.equal(req.minConsumptionTotal, 20)
  assert.equal(req.rechargeGap, 70)
  // 已达标的维度差额为 0，不能出现负数。
  assert.equal(req.consumptionGap, 0)
  // 两个维度都要满足（AND 语义），只达标一个不算升级。
  assert.equal(req.satisfied, false)
})

test('next level requirement marks capped instead of fabricating a threshold', () => {
  const capped = buildNextLevelRequirement(
    [{ id: 1, name: '唯一档', rank: 1, enabled: true, minRechargeTotal: 0, minConsumptionTotal: 0 }],
    { levelId: 1, rank: 1, rechargeTotal: 0, consumptionTotal: 0 },
  )
  assert.equal(capped.available, false)
  assert.equal(capped.capped, true)
  assert.equal('minRechargeTotal' in capped, false, '封顶时不得下发任何门槛数字')
})

test('next level requirement refuses to invent thresholds without level data', () => {
  // 63 号 U-03 红线：没有数据就说没有，不能用 0/默认档位占位。
  for (const levels of [[], null, undefined]) {
    const req = buildNextLevelRequirement(levels, { levelId: 1, rank: 1, rechargeTotal: 5 })
    assert.equal(req.available, false)
    assert.match(req.reason, /无法计算下一档门槛/)
    assert.equal('rechargeGap' in req, false)
  }
})
