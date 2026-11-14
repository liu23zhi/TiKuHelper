import test from 'node:test'
import assert from 'node:assert/strict'

import {
  LEVEL_TABLE_STATUS,
  USER_USAGE_TAB_ORDER,
  USER_USAGE_DEFAULT_TAB,
  buildCurrentLevelText,
  buildDeviceLimitHint,
  buildDeviceLimitText,
  buildLevelMetricText,
  buildLevelOverviewRows,
  buildLevelOverviewStatusText,
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

// ─────────────────────── 138 §1：等级一览表 ───────────────────────

/** 5 档真实等级表（门槛取自 138 §10.2 的真实库实测值）。 */
const OVERVIEW_LEVELS = [
  { id: 5, name: 'Lv5 铂金', rank: 5, enabled: true, deviceLimit: 0, minRechargeTotal: 5000, minConsumptionTotal: 3000 },
  { id: 1, name: 'Lv1 普通', rank: 1, enabled: true, deviceLimit: 1, minRechargeTotal: 0, minConsumptionTotal: 0 },
  // 停用档：rank 夹在中间，专门验证「按 rank 升序」不会被它带偏。
  { id: 9, name: '停用档', rank: 3, enabled: false, deviceLimit: 99, minRechargeTotal: 50, minConsumptionTotal: 0 },
  { id: 3, name: 'Lv3 白银', rank: 3, enabled: true, deviceLimit: 5, minRechargeTotal: 500, minConsumptionTotal: 200 },
  { id: 2, name: 'Lv2 青铜', rank: 2, enabled: true, deviceLimit: 3, minRechargeTotal: 100, minConsumptionTotal: 50 },
  { id: 4, name: 'Lv4 黄金', rank: 4, enabled: true, deviceLimit: 10, minRechargeTotal: 2000, minConsumptionTotal: 1000 },
]

test('等级一览列出全部启用级并按 rank 升序，停用级不出现（L1/L5）', () => {
  const { rows, hasLevels } = buildLevelOverviewRows(OVERVIEW_LEVELS, { levelId: 2, rank: 2, rechargeTotal: 120, consumptionTotal: 60 })
  assert.equal(hasLevels, true)
  // 全部启用级都在（不是只列当前级），且 rank 严格升序。
  assert.deepEqual(rows.map(row => row.name), ['Lv1 普通', 'Lv2 青铜', 'Lv3 白银', 'Lv4 黄金', 'Lv5 铂金'])
  assert.deepEqual(rows.map(row => row.rank), [1, 2, 3, 4, 5])
  // 停用档（id=9 / rank=3）不在列表里 —— 列出它会让用户对着永远达不到的档位算差额。
  assert.equal(rows.some(row => row.id === 9), false, '停用的等级不得出现')
  assert.equal(rows.some(row => row.name === '停用档'), false)
})

test('等级一览的「当前」标记按 id 比对且唯一（L2）', () => {
  const { rows, currentIndex } = buildLevelOverviewRows(OVERVIEW_LEVELS, { levelId: 2, rank: 2, rechargeTotal: 120, consumptionTotal: 60 })
  const currentRows = rows.filter(row => row.isCurrent)
  assert.equal(currentRows.length, 1, '「← 当前」必须唯一')
  assert.equal(currentRows[0].id, 2)
  assert.equal(currentIndex, rows.findIndex(row => row.id === 2))
  assert.equal(rows[currentIndex].status, LEVEL_TABLE_STATUS.current)

  // ⚠️ 关键对照：rank 写重时不得标出两行。
  // 传一个与 id 序号不一致的 rank，若实现只按 rank 比对，就会标出 rank=2 那一行
  //（而 summary.levelId=3 指的是 id=3）—— 这正是「唯一」判据要挡的形态。
  const duplicatedRank = [
    { id: 1, name: 'A', rank: 1, enabled: true, deviceLimit: 0, minRechargeTotal: 0, minConsumptionTotal: 0 },
    { id: 2, name: 'B', rank: 2, enabled: true, deviceLimit: 0, minRechargeTotal: 10, minConsumptionTotal: 0 },
    { id: 3, name: 'C', rank: 2, enabled: true, deviceLimit: 0, minRechargeTotal: 20, minConsumptionTotal: 0 },
  ]
  const duplicated = buildLevelOverviewRows(duplicatedRank, { levelId: 3, rank: 2, rechargeTotal: 0, consumptionTotal: 0 })
  assert.equal(duplicated.rows.filter(row => row.isCurrent).length, 1, 'rank 写重时也必须只有一个当前行')
  assert.equal(duplicated.rows.filter(row => row.isCurrent)[0].id, 3, 'id 优先于 rank')

  // 无等级用户（levelId=0）：一个当前标记都没有，不能默认标第一档。
  const none = buildLevelOverviewRows(OVERVIEW_LEVELS, { levelId: 0, rank: 0, rechargeTotal: 0, consumptionTotal: 0 })
  assert.equal(none.rows.filter(row => row.isCurrent).length, 0)
  assert.equal(none.currentIndex, -1)
})

test('等级一览对每个档位**分别**给出两个差额，绝不取 min（L3）', () => {
  // 这是本任务的红线①。后端判定是 AND（service/user_level.go:96-101 两条 continue）：
  // 两个门槛必须**同时**满足。
  //
  // 构造一个「取 min 就能被发现」的场景：充值差 300、消费差 150。
  //   - 正确实现：rechargeGap=300 且 consumptionGap=150，两个都在。
  //   - 取 min 的实现：只会给出 150（或把两个字段都填成 150），
  //     用户补上 150 消费后仍然升不了级 —— 那是错误的行动指引。
  const levels = [
    { id: 1, name: '当前档', rank: 1, enabled: true, deviceLimit: 0, minRechargeTotal: 0, minConsumptionTotal: 0 },
    { id: 2, name: '目标档', rank: 2, enabled: true, deviceLimit: 0, minRechargeTotal: 400, minConsumptionTotal: 250 },
  ]
  const { rows } = buildLevelOverviewRows(levels, { levelId: 1, rank: 1, rechargeTotal: 100, consumptionTotal: 100 })
  const target = rows.find(row => row.id === 2)
  assert.equal(target.rechargeGap, 300, '充值差额必须是 400-100=300')
  assert.equal(target.consumptionGap, 150, '消费差额必须是 250-100=150')
  // 两个差额都不许等于对方（那是取 min 的实现特征）。
  assert.notEqual(target.rechargeGap, target.consumptionGap)
  assert.notEqual(target.rechargeGap, 150, '充值差额被换成 min 值 —— 界面会给错行动指引')
  assert.equal(target.satisfied, false, '两个维度都未达标，AND 语义下不算达成')
  // 状态文案必须同时出现两个差额（含各自的「差」字样与数字）。
  const statusText = buildLevelOverviewStatusText(target)
  assert.match(statusText, /充值差 ¥300/)
  assert.match(statusText, /消费差 ¥150/)
})

test('等级一览在单维度未达标时明说「另一维度已达标」（AND 语义）', () => {
  // 充值达标、消费没达标：不能提示「还差 ¥0」让用户以为可以升级，
  // 也不能只显示消费差额而不说明充值维度不用管了。
  const levels = [
    { id: 1, name: '当前档', rank: 1, enabled: true, deviceLimit: 0, minRechargeTotal: 0, minConsumptionTotal: 0 },
    { id: 2, name: '目标档', rank: 2, enabled: true, deviceLimit: 0, minRechargeTotal: 100, minConsumptionTotal: 200 },
  ]
  const { rows } = buildLevelOverviewRows(levels, { levelId: 1, rank: 1, rechargeTotal: 150, consumptionTotal: 50 })
  const target = rows.find(row => row.id === 2)
  assert.equal(target.rechargeGap, 0)
  assert.equal(target.consumptionGap, 150)
  assert.equal(target.satisfied, false, '只达标一个维度不算升级')
  const text = buildLevelOverviewStatusText(target)
  assert.match(text, /充值已达标/)
  assert.match(text, /消费差 ¥150/)
  assert.doesNotMatch(text, /充值差/)
})

test('等级一览的设备数把 0 渲染成「不限」而不是「0 台」（L4）', () => {
  const { rows } = buildLevelOverviewRows(OVERVIEW_LEVELS, { levelId: 1, rank: 1, rechargeTotal: 0, consumptionTotal: 0 })
  const unlimited = rows.find(row => row.name === 'Lv5 铂金')
  assert.equal(unlimited.deviceLimit, 0)
  assert.equal(unlimited.deviceLimitText, '不限')
  assert.notEqual(unlimited.deviceLimitText, '0 台')
  // 正数仍带「台」单位。
  assert.equal(rows.find(row => row.name === 'Lv2 青铜').deviceLimitText, '3 台')
})

test('等级一览已达成档位显示「已达成」，当前档显示「← 当前」', () => {
  const { rows } = buildLevelOverviewRows(OVERVIEW_LEVELS, { levelId: 3, rank: 3, rechargeTotal: 600, consumptionTotal: 220 })
  // Lv1 / Lv2 的门槛都已被超过 → 已达成。
  assert.equal(buildLevelOverviewStatusText(rows[0]), '已达成')
  assert.equal(buildLevelOverviewStatusText(rows[1]), '已达成')
  assert.equal(rows[0].status, LEVEL_TABLE_STATUS.achieved)
  // 当前档优先显示「← 当前」而不是「已达成」（两个描述都真，但当前更重要）。
  assert.equal(buildLevelOverviewStatusText(rows[2]), '← 当前')
  // 未达成的档位显示差额。
  assert.match(buildLevelOverviewStatusText(rows[3]), /充值差/)
})

test('等级一览在服务器未下发档位时返回空并明确告知', () => {
  for (const levels of [[], null, undefined]) {
    const result = buildLevelOverviewRows(levels, { levelId: 1, rank: 1 })
    assert.equal(result.hasLevels, false)
    assert.deepEqual(result.rows, [])
    assert.equal(result.currentIndex, -1)
  }
  // 只有停用级也等于「没有可用档位」。
  const onlyDisabled = buildLevelOverviewRows(
    [{ id: 1, name: '停用', rank: 1, enabled: false, deviceLimit: 0, minRechargeTotal: 0, minConsumptionTotal: 0 }],
    { levelId: 1, rank: 1 },
  )
  assert.equal(onlyDisabled.hasLevels, false)
})

test('等级一览的差额保留 6 位、不出现浮点尾数', () => {
  const levels = [
    { id: 1, name: '当前档', rank: 1, enabled: true, deviceLimit: 0, minRechargeTotal: 0, minConsumptionTotal: 0 },
    { id: 2, name: '目标档', rank: 2, enabled: true, deviceLimit: 0, minRechargeTotal: 0.3, minConsumptionTotal: 0 },
  ]
  const { rows } = buildLevelOverviewRows(levels, { levelId: 1, rank: 1, rechargeTotal: 0, consumptionTotal: 0 })
  // 0.3 - 0 直接相减在浮点下是 0.30000000000000004。
  assert.equal(rows[1].rechargeGap, 0.3)
  // 已达标维度不出现负数（负数在界面上会读成「超出要求 -50 元」）。
  assert.equal(rows[0].rechargeGap, 0)
  assert.equal(rows[0].consumptionGap, 0)
})
