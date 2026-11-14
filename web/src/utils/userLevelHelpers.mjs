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
