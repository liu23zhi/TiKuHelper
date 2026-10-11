/**
 * 归属地展示相关的纯函数。
 *
 * 抽成纯函数而不是写在页面里，一是便于用 node:test 覆盖，
 * 二是用户管理与日志列表需要完全一致的展示形态。
 */

/** 归属地展示统一使用全角括号，与设计稿一致：`18287719599 （云南玉溪 移动）`。 */
export const LOCATION_OPEN_PAREN = '（'
export const LOCATION_CLOSE_PAREN = '）'

/**
 * 从归属地接口响应里取出 address 映射。
 *
 * 接口响应形态为 `{ data: { "<phone|ip>": { address: "云南玉溪 移动", ... } } }`；
 * 任何一层缺失都返回空对象，调用方据此降级为只显示原始值。
 *
 * @param {unknown} response
 * @returns {Record<string, string>}
 */
export function extractLocationAddresses(response) {
  const rows = response && typeof response === 'object' ? response.data : undefined
  if (!rows || typeof rows !== 'object' || Array.isArray(rows)) {
    return {}
  }

  const addresses = {}
  Object.keys(rows).forEach((key) => {
    const row = rows[key]
    const address = row && typeof row === 'object' ? String(row.address ?? '').trim() : ''
    if (key && address) {
      addresses[key] = address
    }
  })
  return addresses
}

/**
 * 收集列表行里的去重查询键。
 *
 * 空值会被剔除；手机号与 IP 都按原样收集（服务端各自再做归一化与内网过滤）。
 *
 * @param {Array<object>} rows
 * @param {string} field
 * @returns {string[]}
 */
export function collectLocationKeys(rows, field) {
  if (!Array.isArray(rows) || !field) {
    return []
  }

  const seen = new Set()
  const keys = []
  rows.forEach((row) => {
    const value = row && typeof row === 'object' ? row[field] : undefined
    const key = value === null || value === undefined ? '' : String(value).trim()
    if (!key || seen.has(key)) {
      return
    }
    seen.add(key)
    keys.push(key)
  })
  return keys
}

/**
 * 把原始值与归属地拼成展示文本。
 *
 * - 有归属地：`18287719599 （云南玉溪 移动）`
 * - 无归属地（未配置密钥 / 查询失败 / 内网 IP）：只显示原始值，**不加空括号**
 *
 * @param {unknown} rawValue
 * @param {unknown} address
 * @returns {string}
 */
export function formatValueWithLocation(rawValue, address) {
  const raw = rawValue === null || rawValue === undefined ? '' : String(rawValue).trim()
  const location = address === null || address === undefined ? '' : String(address).trim()

  if (!raw) {
    return ''
  }
  if (!location) {
    return raw
  }
  return `${raw} ${LOCATION_OPEN_PAREN}${location}${LOCATION_CLOSE_PAREN}`
}

/** 拼接批量查询参数：单个 key 用单值参数，多个用逗号分隔的批量参数。 */
export function buildLocationLookupParams(kind, keys) {
  const list = Array.isArray(keys) ? keys : []
  if (list.length === 0) {
    return null
  }
  const joined = list.join(',')
  return kind === 'ip' ? { ips: joined } : { phones: joined }
}
