/**
 * 敏感词检测记录页的展示 helper（123 号 T-27）。
 *
 * 纯函数、**不依赖 vue / @vue/compiler-sfc**，可以在 `node --test` 下直接跑
 * （本仓 `web/node_modules` 是空壳）。
 *
 * 后端契约（internal/controller/sensitive_word_cache.go）：
 *   GET /api/admin/sensitive-word-cache?keyword=&state=&checkedFrom=&checkedTo=&page=&pageSize=
 *   三态：hit（命中）/ clean（未命中）/ failed（失败）
 */

/** 三态 → antd 标签颜色。 */
export function sensitiveWordCacheStateColor(state) {
  switch (state) {
    case 'hit':
      return 'red'
    case 'clean':
      return 'green'
    case 'failed':
      return 'orange'
    default:
      return 'default'
  }
}

/**
 * buildSensitiveWordCacheListParams 组装列表查询参数。
 *
 * 三条硬约束：
 *  1. **空筛选不下发**。空串参数会让后端把 `state=""` 当成"不筛选"，
 *     行为上无害，但会让 URL 变脏、也让「当前筛选」的显示难判断；
 *  2. **state 只允许三态白名单**。非法值不下发 —— 后端会返回 400，
 *     但前端不该主动制造 400；这里直接丢弃，等于"用户没选"；
 *  3. page/pageSize 必须是正整数，否则不带（让后端用默认值），
 *     避免 page=0 触发 400 让用户看到莫名其妙的错误。
 */
export function buildSensitiveWordCacheListParams(filters, page, pageSize) {
  const params = {}
  const keyword = String(filters?.keyword ?? '').trim()
  if (keyword) params.keyword = keyword
  const state = String(filters?.state ?? '').trim().toLowerCase()
  if (state === 'hit' || state === 'clean' || state === 'failed') params.state = state
  const from = String(filters?.checkedFrom ?? '').trim()
  if (from) params.checkedFrom = from
  const to = String(filters?.checkedTo ?? '').trim()
  if (to) params.checkedTo = to
  const pageNumber = Number(page)
  if (Number.isInteger(pageNumber) && pageNumber > 0) params.page = pageNumber
  const sizeNumber = Number(pageSize)
  if (Number.isInteger(sizeNumber) && sizeNumber > 0) params.pageSize = sizeNumber
  return params
}

/**
 * formatSensitiveWordCacheDate 把后端时间渲染成上海时区的可读时间。
 *
 * 后端下发的 CheckedAt 是 Go time.Time 的 RFC3339（带时区），所以直接
 * 用 toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' }) 就够 ——
 * 不要手工加减 8 小时（夏令时/时区偏移一变就错）。
 * 空值给 '—'，非法日期原样返回（让人能看见脏值，而不是伪装成 '—'）。
 */
export function formatSensitiveWordCacheDate(value) {
  if (value === null || value === undefined || value === '') return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return String(value)
  return date.toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false })
}

/**
 * formatSensitiveWordCacheWords 把命中词数组渲染成一行文本。
 *
 * 空数组 → '—'（而不是空字符串：空字符串在表格里看起来像"列没渲染"）。
 */
export function formatSensitiveWordCacheWords(words) {
  if (!Array.isArray(words) || words.length === 0) return '—'
  const cleaned = words.map(word => String(word ?? '').trim()).filter(Boolean)
  return cleaned.length ? cleaned.join('、') : '—'
}
