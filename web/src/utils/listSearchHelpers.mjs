/**
 * 列表页搜索参数工具。
 *
 * 这些函数是纯函数，便于单测覆盖「空值剔除、trim、页码重置、LIKE 转义」等规则，
 * 保证前端与服务端对关键词的理解一致。
 */

/** 与后端 SearchKeywordMaxLength 保持一致。 */
export const SEARCH_KEYWORD_MAX_LENGTH = 100

/**
 * 归一化关键词：去首尾空白、截断到上限；空值返回空串（表示不参与筛选）。
 */
export function normalizeSearchKeyword(value) {
  const trimmed = String(value ?? '').trim()
  if (!trimmed) {
    return ''
  }
  return trimmed.length > SEARCH_KEYWORD_MAX_LENGTH
    ? trimmed.slice(0, SEARCH_KEYWORD_MAX_LENGTH)
    : trimmed
}

/**
 * 组装请求参数：空值（空串 / null / undefined）一律剔除，
 * 避免把 `keyword=` 这类空参数发给后端。
 *
 * 分页由显式参数决定，filters 里同名的 page / pageSize 不会覆盖它，
 * 以免调用方传入的页码被意外改写。
 */
export function buildSearchParams(filters = {}, page = 1, pageSize = 10) {
  const params = {}

  Object.entries(filters || {}).forEach(([key, rawValue]) => {
    if (key === 'page' || key === 'pageSize') {
      return
    }
    if (rawValue === null || rawValue === undefined) {
      return
    }
    if (typeof rawValue === 'string') {
      const trimmed = rawValue.trim()
      if (!trimmed) {
        return
      }
      params[key] = trimmed
      return
    }
    if (typeof rawValue === 'number' && Number.isFinite(rawValue)) {
      params[key] = rawValue
      return
    }
    if (typeof rawValue === 'boolean') {
      params[key] = rawValue
    }
  })

  const normalizedPage = Number(page)
  const normalizedPageSize = Number(pageSize)
  params.page = Number.isFinite(normalizedPage) && normalizedPage > 0 ? normalizedPage : 1
  params.pageSize = Number.isFinite(normalizedPageSize) && normalizedPageSize > 0 ? normalizedPageSize : 10

  return params
}

/**
 * 判断筛选条件是否发生了变化；变化时需要把页码重置回第 1 页，
 * 否则会出现「筛完后停在不存在的页码上显示空列表」。
 */
export function shouldResetPage(previousFilters, nextFilters) {
  const normalize = (filters) =>
    Object.entries(filters || {})
      .map(([key, value]) => [key, value === null || value === undefined ? '' : String(value).trim()])
      .filter(([, value]) => value !== '')
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
      .map(([key, value]) => `${key}=${value}`)
      .join('&')

  return normalize(previousFilters) !== normalize(nextFilters)
}

/** 统计「已生效的筛选条件数」，用于在界面上提示当前处于筛选状态。 */
export function countActiveFilters(filters = {}) {
  return Object.values(filters || {}).filter((value) => {
    if (value === null || value === undefined) {
      return false
    }
    if (typeof value === 'string') {
      return value.trim() !== ''
    }
    return true
  }).length
}
