// 用户画像的数据处理层（T-17 / task_doc/98）。
//
// # 为什么是纯函数而不是直接写 echarts 配置
//
// 本仓前端当前无法完整构建（worktree 里的 web/node_modules 是空的，且 @babel/core
// 与 eslint 都缺），装 echarts 也验证不了真实渲染 —— 只是让 package.json 多一行、
// 并把包污染进父级目录。所以图表层做成**可注入的纯函数**：
//   - 输入是后端接口原样返回的 JSON；
//   - 输出是「与图表库无关」的系列数据（labels + values + 空态判定）；
//   - 真正的 echarts option 由页面在运行时拼（或在将来能构建的环境里补）。
//
// 这样测试只断言**聚合口径与空态语义**，而这两条正是 T-17 的核心判据。
//
// # 三条不可协商的性质（与后端一一对应）
//
//  1. **空数组表示「没有数据」**：必须渲染成「暂无数据」，不能画成 0。
//     后端已保证无数据时序列字段是 `[]`、标量是 `null`；本层不得把它们「补零」。
//  2. **null 与 0 不同**：金额 null 是「没有账单」，0 是「有账单但金额为 0」。
//  3. **整体与单用户是同一个函数**：`buildPortraitView` 不区分 scope，
//     两种视角走同一条代码路径（对应后端「共用同一套聚合函数」）。
//
// 本文件必须保持**零依赖**（不 import vue / echarts / ant-design），
// 这样 `node --test` 能在没有任何 node_modules 的环境里跑。

/** 画像的两个视角。与后端 JSON 的 scope 字段同值。 */
export const PORTRAIT_SCOPE_OVERALL = 'overall'
export const PORTRAIT_SCOPE_USER = 'user'

/** 空态文案。集中一处，避免两个视角各写一份不一致的提示。 */
export const PORTRAIT_EMPTY_HINT = '暂无数据'

/**
 * isPortraitValueMissing：判断一个标量是否「没有数据」。
 *
 * 只把 null / undefined 视为缺失 —— **0 不缺失**。这是判据 ② 的前端面：
 * 把 0 当缺失会让「有账单但金额为 0」显示成「暂无数据」。
 */
export function isPortraitValueMissing(value) {
  return value === null || value === undefined
}

/**
 * readPortraitScalar：把标量读成「有值或 null」，绝不补 0。
 *
 * @param {*} value 后端返回的标量（可能是 null）
 * @param {(raw: number) => *} [format] 有值时的格式化函数
 */
export function readPortraitScalar(value, format) {
  if (isPortraitValueMissing(value)) return null
  const numeric = Number(value)
  if (!Number.isFinite(numeric)) return null
  return typeof format === 'function' ? format(numeric) : numeric
}

/**
 * buildPortraitSeries：把一个序列字段转成图表无关的系列数据。
 *
 * @param {Array<object>} items 后端返回的序列（可能为空数组）
 * @param {{labelKey?: string, valueKey?: string, label?: (item: object) => string}} [options]
 * @returns {{labels: string[], values: number[], total: number, empty: boolean}}
 *
 * ⚠️ `empty` 只看输入是否真的为空，**不看 total 是否为 0**：
 * 一小时内全站都是 0 元消费但仍有一条账单时，total=0 但图应该画出来。
 */
export function buildPortraitSeries(items, options) {
  const list = Array.isArray(items) ? items.filter(item => item && typeof item === 'object') : []
  const labelKey = options?.labelKey || 'label'
  const valueKey = options?.valueKey || 'count'
  const labelOf = typeof options?.label === 'function' ? options.label : null

  const labels = []
  const values = []
  let total = 0
  for (const item of list) {
    const value = Number(item[valueKey])
    // 非数值项直接跳过：宁可少一个扇区，也不要在图里画 NaN（echarts 会静默画出空洞）。
    if (!Number.isFinite(value)) continue
    labels.push(labelOf ? labelOf(item) : String(item[labelKey] ?? item.bucket ?? ''))
    values.push(value)
    total += value
  }
  return { labels, values, total, empty: labels.length === 0 }
}

/**
 * buildPortraitCharts：把整份画像响应转成四个图表的数据描述。
 *
 * 返回的每个图都带 `empty`，页面据此决定「画图」还是「显示暂无数据」。
 * 这是判据 ② 在前端的唯一落点：**不在这里补任何 0**。
 *
 * @param {object} portrait 后端 GET /admin/portrait/overview 或 /admin/users/:id/portrait 的原样响应
 */
export function buildPortraitCharts(portrait) {
  const data = portrait && typeof portrait === 'object' ? portrait : {}

  // 小时分布：后端只返回**有数据的小时**，所以横轴就是返回的桶，不补齐 24 个。
  // 补齐会造出「凌晨 3 点 0 次」这种从未发生过的观测，与「没有数据」混淆。
  const hours = buildPortraitSeries(data.hours, {
    label: item => `${String(item.bucket).padStart(2, '0')}:00`,
  })

  const weekdays = buildPortraitSeries(data.weekdays, { labelKey: 'label' })

  // 区域分布：中文明细可能很长，图表用 label 全称（后端已给「未知地区」兜底）。
  const regions = buildPortraitSeries(data.regions)

  const billingTypes = buildPortraitSeries(data.billingTypes)

  return {
    hours: { ...hours, title: '时间段分布（按小时）' },
    weekdays: { ...weekdays, title: '时间段分布（按星期）' },
    regions: { ...regions, title: '区域分布' },
    billingTypes: { ...billingTypes, title: '消费类型分布' },
  }
}

/**
 * buildPortraitTiles：把概览标量转成「指标卡」列表。
 *
 * 每张卡都带 `missing`，页面据此显示「暂无数据」而不是 0。
 * 这个区分很重要：`recordCount: null` 表示「这个范围没有任何账单」，
 * `recordCount: 0` 则是「确实查过，结果就是 0 条」—— 后者在现有后端实现里不会出现
 * （0 条时后端也返回 null），但本层不得把两者混为一谈（将来后端改口径时前端不用再改）。
 */
export function buildPortraitTiles(portrait, formatters) {
  const data = portrait && typeof portrait === 'object' ? portrait : {}
  const totals = data.totals && typeof data.totals === 'object' ? data.totals : {}
  const formatMoney = formatters?.money
  const formatCount = formatters?.count

  /** 单张卡：有值走格式化，无值给 missing。 */
  const tile = (key, title, raw, formatter) => {
    const value = readPortraitScalar(raw, formatter)
    return { key, title, value, missing: value === null }
  }

  const tiles = [
    tile('recordCount', '账单行数（答题量）', totals.recordCount, formatCount),
    tile('amount', '金额合计', totals.amount, formatMoney),
    tile('consumptionCount', '消费笔数', totals.consumptionCount, formatCount),
    tile('ticketCount', '工单数', totals.ticketCount, formatCount),
  ]
  // 整体画像才有的两张卡：单用户视角下后端返回 null，这里按 missing 处理，
  // 页面用 `showOverallOnly` 决定是否渲染（而不是渲染成 0）。
  tiles.unshift(tile('userCount', '用户总数', totals.userCount, formatCount))
  tiles.push(tile('activeUserCount', '有过账单的用户数', totals.activeUserCount, formatCount))
  return tiles
}

/**
 * portraitScopeHint：给出当前视角的说明文案。
 *
 * 「一个页面的两种视角」需要一个可读的标题，否则用户不知道自己看的是全站还是某个人。
 */
export function portraitScopeHint(portrait) {
  const data = portrait && typeof portrait === 'object' ? portrait : {}
  if (data.scope === PORTRAIT_SCOPE_USER) {
    const base = data.base && typeof data.base === 'object' ? data.base : {}
    const name = String(base.nickname || base.username || '').trim()
    const id = Number(data.targetUserId)
    if (name) return `单用户画像：${name}${Number.isInteger(id) && id > 0 ? `（#${id}）` : ''}`
    if (Number.isInteger(id) && id > 0) return `单用户画像：#${id}`
    return '单用户画像'
  }
  return '整体用户画像（全站）'
}

/**
 * normalizePortraitUserId：把「用户搜索框选中项 / URL 上的 id」归一成正整数或空串。
 *
 * 空串表示「整体画像」。非法输入一律当空串（回到整体视角），
 * 不抛错 —— 用户在搜索框里打字的过程中不该看到报错。
 */
export function normalizePortraitUserId(raw) {
  const text = String(raw ?? '').trim()
  if (!text) return ''
  const value = Number(text)
  if (!Number.isInteger(value) || value <= 0) return ''
  return value
}

/**
 * buildPortraitUserOption：用户搜索框的候选项。
 *
 * 只做展示层归一（名称兜底为 `#id`），不做权限判断 ——
 * 权限在后端（98 §0 四层），前端藏菜单只是第 ④ 层的可见性，不是安全边界。
 */
export function buildPortraitUserOption(user) {
  if (!user || typeof user !== 'object') return null
  const id = Number(user.id)
  if (!Number.isInteger(id) || id <= 0) return null
  const name = String(user.nickname || user.username || '').trim()
  return {
    value: id,
    label: name ? `${name}（#${id}）` : `#${id}`,
    username: String(user.username || ''),
  }
}
