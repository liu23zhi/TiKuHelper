/**
 * 归属地展示相关的纯函数。
 *
 * 抽成纯函数而不是写在页面里，一是便于用 node:test 覆盖，
 * 二是用户管理与日志列表需要完全一致的展示形态。
 */

export const MAX_LOCATION_LOOKUP_KEYS = 100

export function normalizeLocationKey(value) {
  return value === null || value === undefined ? '' : String(value).trim().toLowerCase()
}

/**
 * 从归属地接口响应里取出 address 映射。
 *
 * 接口响应形态为 `{ data: { "<phone|ip>": { address: "云南玉溪 移动", ... } } }`；
 * 任何一层缺失都返回空对象，调用方据此降级为只显示原始值。
 *
 * @param {unknown} response
 * @returns {Record<string, string>}
 */
export function extractLocationAddresses(response, kind = '') {
  const payload = response && typeof response === 'object' ? response.data : undefined
  const rows = kind ? payload?.[kind] : (payload?.data ?? payload)
  if (!rows || typeof rows !== 'object' || Array.isArray(rows)) {
    return {}
  }

  const addresses = {}
  Object.keys(rows).forEach((key) => {
    const row = rows[key]
    const address = row && typeof row === 'object' ? String(row.address ?? '').trim() : ''
    const normalizedKey = normalizeLocationKey(key)
    if (normalizedKey && address) {
      addresses[normalizedKey] = address
    }
  })
  return addresses
}

/**
 * 收集列表行里的去重查询键。
 *
 * 空值会被剔除，IP 大小写统一；支持合并多个字段后去重。
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
  const fields = Array.isArray(field) ? field : [field]
  rows.forEach((row) => {
    fields.forEach((name) => {
      const key = normalizeLocationKey(row && typeof row === 'object' ? row[name] : undefined)
      if (!key || seen.has(key)) {
        return
      }
      seen.add(key)
      keys.push(key)
    })
  })
  return keys
}

/**
 * 把原始值与归属地拼成两行展示文本。
 *
 * - 有归属地：`18287719599\n（云南玉溪 移动）`
 * - 无归属地（未配置密钥 / 查询失败）：只显示原始值
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
  return `${raw}\n（${location}）`
}

/** 拼接批量查询参数：单个 key 用单值参数，多个用逗号分隔的批量参数。 */
export function buildLocationLookupParams(kind, keys) {
  const list = [...new Set((Array.isArray(keys) ? keys : []).map(normalizeLocationKey).filter(Boolean))]
  if (list.length === 0) {
    return null
  }
  const joined = list.join(',')
  return kind === 'ip' ? { ips: joined } : { phones: joined }
}

// 单批失败不影响已成功的结果；归属地增强不会阻塞或中断列表主流程。
export async function lookupLocationAddresses(kind, keys, requestBatch, responseKind = '') {
  const normalized = [...new Set((Array.isArray(keys) ? keys : []).map(normalizeLocationKey).filter(Boolean))]
  const addresses = {}
  for (let offset = 0; offset < normalized.length; offset += MAX_LOCATION_LOOKUP_KEYS) {
    const params = buildLocationLookupParams(kind, normalized.slice(offset, offset + MAX_LOCATION_LOOKUP_KEYS))
    try {
      const response = await requestBatch(params)
      if (response?.status !== undefined && response.status !== 200) continue
      Object.assign(addresses, extractLocationAddresses(response, responseKind))
    } catch {
      // 管理页提供可诊断的刷新入口，列表增强保持静默降级。
    }
  }
  return addresses
}
