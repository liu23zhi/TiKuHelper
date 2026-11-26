/**
 * 等级体系 / 设备上限的展示 helper（123 号 T-24 + T-26）。
 *
 * 纯函数，**不依赖 vue / ant-design-vue**，可以在 `node --test` 下直接跑
 * （本仓 `web/node_modules` 是空壳，任何 import vue 的测试都跑不起来）。
 *
 * 后端契约（internal/api/router.go）：
 *   GET /api/user/level          当前登录用户自己的等级摘要
 *   GET /api/admin/user-levels   全部等级（含停用级，管理员专属）
 *
 * `UserLevelSummary`（internal/service/user_level.go:638）字段：
 *   levelId / name / rank / deviceLimit /
 *   minRechargeTotal / minConsumptionTotal /   ← 当前**这一级**的门槛
 *   rechargeTotal / consumptionTotal           ← 实际累计值（两个口径）
 */

// ─────────────────────── T-24：可用设备数量 ───────────────────────

/**
 * buildDeviceLimitText 把 `user_levels.device_limit` 渲染成可读文案。
 *
 * ⚠️ 前置真相（必须保留，别改成"已用/上限"）：
 *   1. `device_limit` 的业务消费点当前是 **0** —— `internal/entity/user_level.go:46`
 *      的注释自称「契约占位」，全仓没有任何一处按它限制设备数；
 *   2. `account_device_bindings` 表**当前不存在**。
 *   ⇒ **没有「已用设备数」这个数据**。所以这里只显示**上限**。
 *   已用数等 device-pool（C3）落地后再接；在那之前**不得用 0 或任何
 *   硬编码/假数据占位**（63 号 U-03 红线）。
 *
 * 取值语义（这是本条最容易被写错的地方）：
 *   - `0` = **不限**，不是「0 台」。直接把 0 渲染成 "0 台" 会让用户以为
 *     一个设备都不能用，与后端「0 表示不限制」的语义完全相反。
 *   - 负数 / 非数字 / 布尔：脏数据，渲染占位符 `—`，
 *     既不伪装成 0（会变成"不限"），也不伪装成有效数字。
 *   - 未加载（null/undefined/''）：渲染 `—`，表示"还没读到"，同样不等于"不限"。
 */
export function buildDeviceLimitText(deviceLimit) {
  if (deviceLimit === null || deviceLimit === undefined || deviceLimit === '') return '—'
  // 布尔是 Number(true) === 1 的陷阱：true 会被渲染成 "1 台"，但它不是设备数。
  if (typeof deviceLimit === 'boolean') return '—'
  const value = Number(deviceLimit)
  if (!Number.isFinite(value)) return '—'
  if (value < 0) return '—'
  if (value === 0) return '不限'
  return `${Math.trunc(value)} 台`
}

/**
 * buildDeviceLimitHint 是设备上限的说明文案。
 *
 * 说明里必须点出「已用设备数暂不可用」，否则用户会怀疑页面漏显示
 * （看到"上限 3 台"自然会问"我用了几台"）。这句话是**事实陈述**，
 * 不是兜底占位：数据源确实还不存在。
 */
export function buildDeviceLimitHint(deviceLimit) {
  const value = Number(deviceLimit)
  const unlimited = deviceLimit !== null && deviceLimit !== undefined && deviceLimit !== ''
    && Number.isFinite(value) && value === 0
  return unlimited
    ? '当前等级不限制可用设备数量。'
    : '当前等级允许同时在用的设备数量上限；已用设备数暂不可用。'
}

// ─────────────────────── T-26：个人使用页 Tab ───────────────────────

/**
 * 个人使用页的 Tab 顺序（123 号：「等级提升使用独立的 tab，放在充值下面」）。
 *
 * 顺序是**契约**，不是排版偏好：写成常量而不是散在模板里，
 * 是为了让「顺序被对调」这件事可以被断言抓到（模板里两个 a-tab-pane
 * 谁先谁后，只有肉眼能看出差别，正则断言很容易写歪）。
 */
export const USER_USAGE_TAB_ORDER = Object.freeze(['recharge', 'level'])

/** buildUserUsageTabs 返回渲染用的 Tab 定义，顺序与 USER_USAGE_TAB_ORDER 严格一致。 */
export function buildUserUsageTabs() {
  return [
    { key: 'recharge', title: '充值' },
    { key: 'level', title: '等级提升' },
  ]
}

/** 默认选中的 Tab：充值（保持重构前的首屏行为，不让用户一进来就看不到充值表单）。 */
export const USER_USAGE_DEFAULT_TAB = 'recharge'

// ─────────────────────── T-26：等级提升面板 ───────────────────────

/** buildCurrentLevelText 渲染当前等级名。无等级时给明确文案而不是空白。 */
export function buildCurrentLevelText(summary) {
  const name = String(summary?.name ?? '').trim()
  if (!name) return '暂无等级'
  return name
}

/**
 * buildLevelMetricText 渲染两个口径值。
 *
 * 口径（internal/service/user_level.go:160 起）：
 *   rechargeTotal    = status=paid 的充值额 **减去已成功的退款额**（下限 0）
 *   consumptionTotal = billing_records 里支出类之和
 * 两者都是"累计值"，不是"本月值"，文案里必须写清，否则用户会以为能重置。
 */
export function buildLevelMetricText(summary, metric) {
  const key = metric === 'consumption' ? 'consumptionTotal' : 'rechargeTotal'
  const label = metric === 'consumption' ? '累计消费' : '累计充值'
  const exact=summary?.ledger?.exact?.[metric === 'consumption' ? 'consumption' : 'recharge']
  if (typeof exact === 'string') return {label,value:exact}
  const raw = summary?.[key]
  if (raw === null || raw === undefined || raw === '') return { label, value: '—' }
  const value = Number(raw)
  if (!Number.isFinite(value)) return { label, value: '—' }
  return { label, value }
}

/**
 * pickNextLevel 在等级表里挑出「当前等级的下一档」。
 *
 * 规则与后端判定口径（service.ResolveUserLevel）保持一致：
 *   - 只看 `enabled` 的等级（停用级既不参与晋级，也不该被当成目标）；
 *   - 只看 `rank` 严格大于当前 rank 的（跳级允许，所以取**最小**的更高 rank，
 *     也就是"最近的一档"，而不是 rank 最大的顶级）；
 *   - 当前无等级（rank<=0 / levelId<=0）时，取所有启用级里 rank 最小的一档。
 *
 * 返回 null 表示"没有更高档位了"（已封顶），调用方据此显示"已是最高等级"，
 * 而不是显示一个空的门槛表。
 */
export function pickNextLevel(levels, summary) {
  const rows = Array.isArray(levels) ? levels : []
  const currentRank = Number(summary?.rank ?? 0)
  const hasCurrent = Number(summary?.levelId ?? 0) > 0 && Number.isFinite(currentRank) && currentRank > 0
  let best = null
  for (const row of rows) {
    if (!row || row.enabled === false) continue
    const rank = Number(row.rank)
    if (!Number.isFinite(rank) || rank <= 0) continue
    if (hasCurrent && rank <= currentRank) continue
    if (!best || rank < Number(best.rank)) best = row
  }
  return best
}

/**
 * buildNextLevelRequirement 计算「下一档门槛与差额」。
 *
 * levels 允许为空/未获取：此时返回 `{ available: false, reason }`，
 * **不编造门槛数字**。这条是 63 号 U-03 红线的具体落地 ——
 * 没有数据就如实说没有，不要用 0 或默认档位占位（那会让用户以为
 * "再充 0 元就升级"）。
 *
 * gap 的口径：`max(0, 门槛 - 实际值)`。已达标的维度差额为 0，
 * 不能出现负数（负数在界面上会读成"超出要求 -50 元"，语义混乱）。
 */
function exactLevelGaps(level, summary) {
  const values=[level.minRechargeExact,level.minConsumptionExact,summary?.ledger?.exact?.recharge,summary?.ledger?.exact?.consumption]
  if (!values.every(v=>typeof v==='string' && /^\d+\.\d{6}$/.test(v)))return null
  const [r,c,haveR,haveC]=values.map(v=>BigInt(v.replace('.','')))
  const gap=(a,b)=>{const n=(a>b?a-b:0n).toString().padStart(7,'0');return n.slice(0,-6)+'.'+n.slice(-6)}
  return {minRechargeTotal:values[0],minConsumptionTotal:values[1],rechargeGap:gap(r,haveR),consumptionGap:gap(c,haveC),satisfied:haveR>=r&&haveC>=c}
}

export function buildNextLevelRequirement(levels, summary) {
  const rows = Array.isArray(levels) ? levels : []
  if (!rows.length) {
    return {
      available: false,
      reason: '服务器未下发等级档位，无法计算下一档门槛。',
    }
  }
  const next = pickNextLevel(rows, summary)
  if (!next) {
    return { available: false, capped: true, reason: '已是最高等级。' }
  }
  const exact = exactLevelGaps(next, summary)
  if (exact) return { available: true, levelName: String(next.name ?? '').trim(), rank: Number(next.rank ?? 0), ...exact }
  const rechargeTotal = Number(summary?.rechargeTotal ?? 0)
  const consumptionTotal = Number(summary?.consumptionTotal ?? 0)
  const minRecharge = Number(next.minRechargeTotal ?? 0)
  const minConsumption = Number(next.minConsumptionTotal ?? 0)
  const rechargeGap = Number.isFinite(minRecharge) && Number.isFinite(rechargeTotal)
    ? Math.max(0, Math.round((minRecharge - rechargeTotal) * 1e6) / 1e6)
    : null
  const consumptionGap = Number.isFinite(minConsumption) && Number.isFinite(consumptionTotal)
    ? Math.max(0, Math.round((minConsumption - consumptionTotal) * 1e6) / 1e6)
    : null
  return {
    available: true,
    levelName: String(next.name ?? '').trim(),
    rank: Number(next.rank ?? 0),
    minRechargeTotal: Number.isFinite(minRecharge) ? minRecharge : null,
    minConsumptionTotal: Number.isFinite(minConsumption) ? minConsumption : null,
    rechargeGap,
    consumptionGap,
    // 两个维度**同时**满足才升级（与后端 ResolveUserLevel 的 AND 语义一致）。
    // 任一维度为 null 视为"未知"，不能算达标。
    satisfied: rechargeGap === 0 && consumptionGap === 0,
  }
}

// ─────────────────────── 138 §1：等级一览表 ───────────────────────

/**
 * 等级一览表的行状态取值（**唯一**定义，模板不得另写字面量）。
 */
export const LEVEL_TABLE_STATUS = Object.freeze({
  /** 已达成该档门槛（两个口径都够）。 */
  achieved: 'achieved',
  /** 当前所在档。 */
  current: 'current',
  /** 未达成，仍有差额。 */
  pending: 'pending',
})

/**
 * buildLevelOverviewRows 生成「等级一览表」的行（138 §1.2 / §1.3 的 L1/L2/L5）。
 *
 * 入参 `levels` 是 `GET /admin/user-levels` 的原始行（含停用级），
 * `summary` 是 `GET /user/level` 的当前等级摘要。
 *
 * 三条口径（每条都有对应判据，改之前先读判据）：
 *
 *  ① **只列启用级**（`enabled !== false`），按 `rank` 升序（L1/L5）。
 *     停用级不参与晋级（后端 `ResolveUserLevel` 里同样先过滤），
 *     列出来只会让用户对着一个永远达不到的档位算差额。
 *
 *  ② **「还差多少」两个口径都要给，绝不允许取 min**（L3 / 后端 AND）。
 *     后端 `service/user_level.go:96-101` 是两个独立的 `continue`：
 *       if recharge    < level.MinRechargeTotal    { continue }
 *       if consumption < level.MinConsumptionTotal { continue }
 *     ⇒ 必须**同时**满足。取 min 的后果：用户充值差 300、消费差 150 时
 *     界面提示「还差 ¥150」，用户补上消费后**仍升不了级** —— 这是给了错误
 *     行动指引，比不显示更糟（138 §1.2 原文）。
 *     所以每行都返回 `rechargeGap` 与 `consumptionGap` 两个字段，谁也不合并。
 *
 *  ③ **「当前」标记必须按 id 比对且唯一**（L2）。
 *     判定顺序：先比 `levelId`（后端权威），levelId 不可用时才回落到 rank。
 *     只比 rank 会在两个等级 rank 写重时标出两行 —— 而「唯一」是硬要求；
 *     所以最后再用 `currentIndex` 收敛成「最多一行」。
 *
 * 返回 `{ rows, currentIndex, hasLevels }`：
 *   - `rows`：渲染用数组，每行含 `{ id, name, rank, deviceLimit, deviceLimitText,
 *     minRechargeTotal, minConsumptionTotal, rechargeGap, consumptionGap,
 *     satisfied, isCurrent, status }`
 *   - `currentIndex`：当前等级在 rows 里的下标，-1 表示「当前用户无等级」
 *   - `hasLevels`：false 表示服务器没下发档位（此时不应渲染表格，给原因文案）
 */
export function buildLevelOverviewRows(levels, summary) {
  const source = Array.isArray(levels) ? levels : []
  // ① 启用过滤 + rank 升序。停用级（enabled === false）不参与晋级。
  const enabled = source
    .filter((row) => row && row.enabled !== false)
    .slice()
    .sort((a, b) => {
      const rankA = Number(a.rank)
      const rankB = Number(b.rank)
      const safeA = Number.isFinite(rankA) ? rankA : Number.MAX_SAFE_INTEGER
      const safeB = Number.isFinite(rankB) ? rankB : Number.MAX_SAFE_INTEGER
      return safeA - safeB
    })

  if (!enabled.length) {
    return { rows: [], currentIndex: -1, hasLevels: false }
  }

  const levelId = Number(summary?.levelId ?? 0)
  const currentRank = Number(summary?.rank ?? 0)
  const hasLevelId = Number.isFinite(levelId) && levelId > 0
  const hasRank = Number.isFinite(currentRank) && currentRank > 0

  // 用户实际累计值。缺失视为 0（而不是 null）：差额是「还要多少」，
  // 用户没钱时就是全额门槛，这与后端「累计值默认 0」一致。
  const rechargeTotal = toFiniteNumber(summary?.rechargeTotal, 0)
  const consumptionTotal = toFiniteNumber(summary?.consumptionTotal, 0)

  // ③ 先按 id 定位（权威），定位不到再按 rank 回落。
  let currentIndex = -1
  if (hasLevelId) {
    currentIndex = enabled.findIndex((row) => Number(row.id) === levelId)
  }
  if (currentIndex < 0 && hasRank) {
    currentIndex = enabled.findIndex((row) => Number(row.rank) === currentRank)
  }

  const rows = enabled.map((row, index) => {
    const minRecharge = toFiniteNumber(row.minRechargeTotal, 0)
    const minConsumption = toFiniteNumber(row.minConsumptionTotal, 0)
    const deviceLimit = row.deviceLimit
    // ② 两个差额分别算，**不取 min**、不合并成一个字段。
    const rechargeGap = roundGap(Math.max(0, minRecharge - rechargeTotal))
    const consumptionGap = roundGap(Math.max(0, minConsumption - consumptionTotal))
    const exact=exactLevelGaps(row,summary)
    const satisfied = exact?.satisfied ?? (rechargeGap === 0 && consumptionGap === 0)
    const isCurrent = index === currentIndex
    return {
      id: row.id,
      name: String(row.name ?? '').trim(),
      rank: Number.isFinite(Number(row.rank)) ? Number(row.rank) : 0,
      // 设备上限：0 由 buildDeviceLimitText 渲染成「不限」，**不是**「0 台」。
      deviceLimit,
      deviceLimitText: buildDeviceLimitText(deviceLimit),
      minRechargeTotal: minRecharge,
      minConsumptionTotal: minConsumption,
      rechargeGap,
      consumptionGap,
      ...(exact ?? {}),
      satisfied,
      isCurrent,
      status: isCurrent
        ? LEVEL_TABLE_STATUS.current
        : (satisfied ? LEVEL_TABLE_STATUS.achieved : LEVEL_TABLE_STATUS.pending),
    }
  })

  return { rows, currentIndex, hasLevels: true }
}

/**
 * buildLevelOverviewStatusText 渲染「状态」列（含差额明细）。
 *
 * ⚠️ 两个差额**同时**出现在一行里（`充值差 ¥300 / 消费差 ¥150`），
 * 而不是只显示其中一个或最小值。已达标的那一维明确写「充值已达标」，
 * 让用户知道「这个维度不用再管了」—— 这是 AND 语义下唯一不误导的表达。
 */
export function buildLevelOverviewStatusText(row) {
  if (!row) return '—'
  if (row.isCurrent) return '← 当前'
  if (row.satisfied) return '已达成'
  const parts = []
  parts.push(row.rechargeGap > 0 ? `充值差 ¥${formatGapNumber(row.rechargeGap)}` : '充值已达标')
  parts.push(row.consumptionGap > 0 ? `消费差 ¥${formatGapNumber(row.consumptionGap)}` : '消费已达标')
  return parts.join(' / ')
}

function toFiniteNumber(value, fallback) {
  const number = Number(value)
  return Number.isFinite(number) ? number : fallback
}

/** 差额保留 6 位（与后端 MoneyPrecision 一致），避免 0.30000000000000004。 */
function roundGap(value) {
  return Math.round(value * 1e6) / 1e6
}

/** 差额展示：整数不带小数，有小数时最多 6 位并去掉尾零。 */
function formatGapNumber(value) {
  if (typeof value === "string" && /^\d+\.\d{6}$/.test(value)) return value.replace(/0+$/, "").replace(/\.$/, "")
  const number = Number(value)
  if (!Number.isFinite(number)) return '0'
  const fixed = number.toFixed(6).replace(/0+$/, '').replace(/\.$/, '')
  return fixed === '' || fixed === '-0' ? '0' : fixed
}
