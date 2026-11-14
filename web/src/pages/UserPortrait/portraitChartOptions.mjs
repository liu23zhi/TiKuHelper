// 用户画像的 echarts option 构建层（t200「请添加图表等，这样会直观一些」）。
//
// # 为什么 option 也做成纯函数
//
// 数据层（portraitCharts.mjs）已经把后端 JSON 转成「与图表库无关」的 series；
// 本文件再把 series 转成 echarts option。拆成两层是为了：
//   - 数据口径（哪些桶、空态语义）与视觉表现（颜色、坐标轴、提示框）分别可测；
//   - option 构建不 import echarts 本体，`node --test` 能在**没有任何 node_modules**
//     的环境里跑（本仓 CI 的 test:unit 就是这么跑的）。真正 `init()` 在页面里做。
//
// # 三条硬约束（对应任务书的三条交付要求）
//
//  1. **空数据不画图**：`empty: true` 的系列返回 `null`，页面据此渲染「暂无数据」占位。
//     绝不返回一个只有 0 值的 option —— 那会把「从没发生过」画成「发生过但次数为 0」。
//  2. **深色模式不写死颜色**：全部颜色来自 CSS 变量的**实际取值**（页面传入），
//     本层只负责把它们填进 option。主题切换时页面重新构建 option 即可。
//     ECharts 是 canvas 渲染，CSS 变量对它不生效，所以必须显式传色值 ——
//     但**色值的唯一来源仍是 theme-tokens.css**，本文件不出现任何 #RRGGBB 字面量。
//  3. **移动端自适应**：option 里不放固定像素宽度；容器宽度由 CSS 决定，
//     坐标轴标签在窄屏按 `axisLabelInterval` 抽稀，避免横向溢出。
//
// 本文件必须保持**零依赖**（不 import echarts / vue / ant-design）。

/** 24 小时分布的标题。 */
export const PORTRAIT_HOURS_TITLE = '24 小时时段分布'
/** 题目/消费类型分布的标题。 */
export const PORTRAIT_TYPES_TITLE = '题目类型分布'
/** 趋势图的标题。 */
export const PORTRAIT_TREND_TITLE = '消费趋势'

/**
 * 从主题变量取值表里取一个颜色，带兜底。
 *
 * @param {Record<string,string>} palette 形如 { text: '#1D1D1F', ... }，由页面从 CSS 变量读出
 * @param {string} key
 * @param {string} fallback 取不到时的兜底（仍然由调用方提供，不在此写死业务色）
 */
export function readPortraitColor(palette, key, fallback) {
  const value = palette && typeof palette === 'object' ? palette[key] : undefined
  const text = typeof value === 'string' ? value.trim() : ''
  return text || String(fallback ?? '')
}

/**
 * portraitTooltipOption：统一的提示框样式（跟随主题，不写死深色/浅色）。
 *
 * `tooltip-fg` / `tooltip-bg` 是 theme-tokens.css 里已有的变量，
 * 它们本身就区分了 light/dark 两套取值，所以这里只需原样引用。
 */
export function portraitTooltipOption(palette) {
  return {
    backgroundColor: readPortraitColor(palette, 'tooltipBg', ''),
    borderColor: readPortraitColor(palette, 'border', ''),
    textStyle: { color: readPortraitColor(palette, 'tooltipFg', '') },
  }
}

/**
 * buildPortraitHoursOption：① 24 小时时段分布（柱状）。
 *
 * ⚠️ 横轴只列**后端实际返回的桶**，不补齐成 00:00–23:00 全 24 根。
 * 补齐会造出「凌晨 3 点 0 次」这种从未发生过的观测 —— 与「没有数据」混淆，
 * 正是任务书点名的空态语义问题。真正没有数据时本函数返回 null。
 *
 * @param {{labels: string[], values: number[], empty: boolean}} series 来自 buildPortraitSeries
 * @param {Record<string,string>} palette
 */
export function buildPortraitHoursOption(series, palette) {
  if (!series || series.empty) return null
  return {
    tooltip: { trigger: 'axis', ...portraitTooltipOption(palette) },
    grid: { left: 8, right: 12, top: 16, bottom: 8, containLabel: true },
    xAxis: {
      type: 'category',
      data: series.labels,
      axisLine: { lineStyle: { color: readPortraitColor(palette, 'border', '') } },
      axisTick: { show: false },
      axisLabel: {
        color: readPortraitColor(palette, 'textMuted', ''),
        // 窄屏抽稀：24 个「HH:00」标签在 320px 里必然重叠。
        interval: series.labels.length > 12 ? 'auto' : 0,
        hideOverlap: true,
      },
    },
    yAxis: {
      type: 'value',
      minInterval: 1,
      axisLabel: { color: readPortraitColor(palette, 'textMuted', '') },
      splitLine: { lineStyle: { color: readPortraitColor(palette, 'border', '') } },
    },
    series: [
      {
        name: '次数',
        type: 'bar',
        data: series.values,
        barMaxWidth: 24,
        itemStyle: { color: readPortraitColor(palette, 'primary', ''), borderRadius: [4, 4, 0, 0] },
      },
    ],
  }
}

/**
 * buildPortraitTypesOption：② 题目类型分布（环形）。
 *
 * 环图而不是饼图：中心可以放合计，且窄屏下视觉更稳。
 * 空数据同样返回 null。
 *
 * @param {{labels: string[], values: number[], total: number, empty: boolean}} series
 */
export function buildPortraitTypesOption(series, palette) {
  if (!series || series.empty) return null
  // 全 0 的序列对环图无意义（所有扇区角为 0，画出来是一片空白圆环），
  // 但「全 0」与「空」语义不同：这里保留图，因为它确实有分类，只是数值为 0。
  // 因此不做额外过滤，交给 echarts 显示为空心环（下方 title 会显示合计 0）。
  return {
    tooltip: { trigger: 'item', ...portraitTooltipOption(palette) },
    legend: {
      type: 'scroll',
      bottom: 0,
      textStyle: { color: readPortraitColor(palette, 'textMuted', '') },
      // 移动端：图例可横向滚动，不换行挤压画布。
      pageTextStyle: { color: readPortraitColor(palette, 'textMuted', '') },
    },
    color: portraitSeriesPalette(palette),
    series: [
      {
        name: '类型',
        type: 'pie',
        radius: ['45%', '70%'],
        center: ['50%', '45%'],
        avoidLabelOverlap: true,
        itemStyle: {
          borderColor: readPortraitColor(palette, 'surface', ''),
          borderWidth: 1,
        },
        label: {
          color: readPortraitColor(palette, 'textMuted', ''),
          formatter: '{b}: {c}',
          // 窄屏下外标签会互相压住，改为图例 + 提示框承载信息。
          show: series.labels.length <= 6,
        },
        labelLine: { show: series.labels.length <= 6 },
        data: series.labels.map((label, index) => ({ name: label, value: series.values[index] })),
      },
    ],
  }
}

/**
 * buildPortraitTrendOption：③ 消费/答题趋势（折线）。
 *
 * ⚠️ 后端当前**没有**按天的趋势字段（已核实：user_portrait.go 全文无 trend/daily/by_date）。
 * 因此本图的横轴用后端**确实提供**的时间维度（小时桶或星期桶），
 * 并在页面上如实标注口径，不臆造「今日 vs 昨日」这类不存在的数据。
 *
 * 任务书红线「不改后端字段（缺字段写进报告让我决定）」——
 * 这里就是如实降级，等后端补了按天聚合再切横轴。
 *
 * @param {{labels: string[], values: number[], empty: boolean}} series
 * @param {Record<string,string>} palette
 * @param {{name?: string}} [options]
 */
export function buildPortraitTrendOption(series, palette, options) {
  if (!series || series.empty) return null
  return {
    tooltip: { trigger: 'axis', ...portraitTooltipOption(palette) },
    grid: { left: 8, right: 12, top: 16, bottom: 8, containLabel: true },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      data: series.labels,
      axisLine: { lineStyle: { color: readPortraitColor(palette, 'border', '') } },
      axisTick: { show: false },
      axisLabel: {
        color: readPortraitColor(palette, 'textMuted', ''),
        interval: series.labels.length > 12 ? 'auto' : 0,
        hideOverlap: true,
      },
    },
    yAxis: {
      type: 'value',
      minInterval: 1,
      axisLabel: { color: readPortraitColor(palette, 'textMuted', '') },
      splitLine: { lineStyle: { color: readPortraitColor(palette, 'border', '') } },
    },
    series: [
      {
        name: String(options?.name || '次数'),
        type: 'line',
        data: series.values,
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        lineStyle: { width: 2, color: readPortraitColor(palette, 'primary', '') },
        itemStyle: { color: readPortraitColor(palette, 'primary', '') },
        areaStyle: { opacity: 0.12, color: readPortraitColor(palette, 'primary', '') },
      },
    ],
  }
}

/**
 * portraitSeriesPalette：环图的多色序列。
 *
 * 只用主题里**已定义**的语义色（primary/danger/warning/success/info），
 * 这样深色模式下不用另配一套 —— theme-tokens.css 已经为两套主题各自取值。
 * 不引入新的业务色字面量。
 */
export function portraitSeriesPalette(palette) {
  return [
    readPortraitColor(palette, 'primary', ''),
    readPortraitColor(palette, 'warningFg', ''),
    readPortraitColor(palette, 'successFg', ''),
    readPortraitColor(palette, 'dangerFg', ''),
    readPortraitColor(palette, 'infoFg', ''),
    readPortraitColor(palette, 'textMuted', ''),
  ].filter(Boolean)
}

/**
 * buildPortraitAllOptions：把三个图一次性构建出来。
 *
 * 返回值里每个键都可能是 `null`（表示空数据 → 页面画「暂无数据」占位）。
 * 页面只需遍历这个对象，不需要自己判断空态 —— 空态判断只有一个落点。
 *
 * @param {object} charts 来自 buildPortraitCharts() 的结果
 * @param {Record<string,string>} palette 从 CSS 变量读出的颜色表
 */
export function buildPortraitAllOptions(charts, palette) {
  const source = charts && typeof charts === 'object' ? charts : {}
  return {
    hours: buildPortraitHoursOption(source.hours, palette),
    types: buildPortraitTypesOption(source.billingTypes, palette),
    // 趋势：优先用小时桶（横轴更细）；小时桶为空时退回星期桶。
    // 两者都空 → null（页面显示「暂无数据」）。
    trend: buildPortraitTrendOption(
      source.hours && !source.hours.empty ? source.hours : source.weekdays,
      palette,
      { name: '次数' },
    ),
  }
}

/**
 * PORTRAIT_PALETTE_VARS：页面需要从 CSS 变量读出的键 → 变量名映射。
 *
 * 集中在这里，使「页面读哪些颜色」只定义一次；
 * 新增颜色时只改这一处，页面代码不见 `var(--...)` 字面量散落。
 */
export const PORTRAIT_PALETTE_VARS = {
  text: '--text',
  textMuted: '--text-muted',
  border: '--border',
  surface: '--surface',
  primary: '--primary',
  successFg: '--success-fg',
  warningFg: '--warning-fg',
  dangerFg: '--danger-fg',
  infoFg: '--info-fg',
  tooltipFg: '--tooltip-fg',
  tooltipBg: '--tooltip-bg',
}

/**
 * readPortraitPalette：从某个 DOM 元素上把主题色读成普通对象。
 *
 * ECharts 走 canvas，CSS 变量对它无效，必须先把 `getComputedStyle` 的实际值读出来。
 * **依赖注入 `readStyle` 与 `element`**，因此本函数可在 node 里用假对象测（不需要 jsdom）。
 *
 * @param {{element: object|null, readStyle: (el: object, prop: string) => string}} deps
 */
export function readPortraitPalette(deps) {
  const element = deps?.element || null
  const readStyle = deps?.readStyle
  const palette = {}
  for (const [key, cssVar] of Object.entries(PORTRAIT_PALETTE_VARS)) {
    let raw = ''
    if (element && typeof readStyle === 'function') {
      try {
        raw = String(readStyle(element, cssVar) ?? '').trim()
      } catch {
        // 读样式失败不该让整页白屏：留空，由 readPortraitColor 的 fallback 兜。
        raw = ''
      }
    }
    palette[key] = raw
  }
  return palette
}
