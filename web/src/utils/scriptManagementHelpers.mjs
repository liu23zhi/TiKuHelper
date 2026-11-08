const DEFAULT_SCHEME = 'https'

function text(value) {
  return String(value ?? '').trim()
}

function firstDefined(record, keys, fallback = '') {
  for (const key of keys) {
    if (record?.[key] !== undefined && record?.[key] !== null) return record[key]
  }
  return fallback
}

export function normalizeScriptSlug(value, fallback = 'a') {
  const slug = text(value).toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')
  return slug || fallback
}

export function normalizeBaseDomain(value) {
  let domain = text(value).replace(/^https?:\/\//i, '').replace(/\/.*$/, '').replace(/\.+$/, '')
  return domain.toLowerCase()
}

export function parsePresetParams(value) {
  if (!value) return {}
  if (typeof value === 'object' && !Array.isArray(value)) {
    return Object.fromEntries(Object.entries(value).map(([key, entry]) => [text(key), text(entry)]).filter(([key]) => key))
  }
  if (Array.isArray(value)) {
    return Object.fromEntries(value.map((entry) => {
      if (Array.isArray(entry)) return [text(entry[0]), text(entry[1])]
      return [text(entry?.key), text(entry?.value)]
    }).filter(([key]) => key))
  }
  const raw = text(value)
  if (!raw) return {}
  try {
    const parsed = JSON.parse(raw)
    if (parsed && typeof parsed === 'object') return parsePresetParams(parsed)
  } catch (_) {
    // Fall through to the friendly KEY=VALUE editor format.
  }
  return Object.fromEntries(raw.split(/\r?\n|,/).map((line) => {
    const index = line.indexOf('=')
    if (index < 1) return ['', '']
    return [text(line.slice(0, index)), text(line.slice(index + 1))]
  }).filter(([key]) => key))
}

export function formatPresetParams(value) {
  return Object.entries(parsePresetParams(value)).map(([key, entry]) => `${key}=${entry}`).join('\n')
}

export function normalizeScriptRecord(record = {}) {
  const useWildcardDomain = firstDefined(record, ['useWildcardDomain', 'wildcardDomain', 'wildcard'], true) !== false
  const billingEnabled = firstDefined(record, ['billingEnabled', 'enableBilling'], true) !== false
  const billingUseTikuPrice = firstDefined(record, ['billingUseTikuPrice', 'useTikuPrice', 'useTikuBilling', 'tikuBilling'], true) !== false
  const multiplierRaw = Number(firstDefined(record, ['priceMultiplier', 'answerPriceMultiplier', 'answer_multiplier'], 1))
  const answerPriceRaw = Number(firstDefined(record, ['answerPrice', 'scriptAnswerPrice', 'questionPrice'], 0))
  return {
    ...record,
    id: firstDefined(record, ['id', 'ID'], undefined),
    name: text(firstDefined(record, ['name', 'scriptName'], '未命名脚本')) || '未命名脚本',
    description: text(firstDefined(record, ['description', 'remark'], '')),
    fileKey: text(firstDefined(record, ['fileKey', 'objectKey', 'packageKey'], '')),
    fileName: text(firstDefined(record, ['fileName', 'filename'], '')),
    versionId: firstDefined(record, ['versionId', 'versionID'], undefined),
    version: text(firstDefined(record, ['version', 'versionName'], '')),
    shortCode: text(firstDefined(record, ['shortCode', 'short_code'], '')),
    instruction: text(firstDefined(record, ['instruction', 'command'], '')),
    slug: normalizeScriptSlug(firstDefined(record, ['shortCode', 'short_code', 'slug', 'shortKey', 'key'], 'a')),
    scheme: text(firstDefined(record, ['scheme', 'urlScheme'], DEFAULT_SCHEME)).toLowerCase() === 'http' ? 'http' : DEFAULT_SCHEME,
    baseDomain: normalizeBaseDomain(firstDefined(record, ['baseDomain', 'domain', 'host'], '')),
    useWildcardDomain,
    enabled: firstDefined(record, ['enabled', 'status'], true) !== false && firstDefined(record, ['status'], 'enabled') !== 'disabled',
    ownerId: firstDefined(record, ['ownerId', 'userId'], undefined),
    ownerName: text(firstDefined(record, ['ownerName', 'username', 'owner'], '')),
    presetParams: parsePresetParams(firstDefined(record, ['presetParams', 'parameters', 'configParams'], {})),
    billingEnabled,
    billingUseTikuPrice,
    priceMultiplier: Number.isFinite(multiplierRaw) && multiplierRaw >= 0 ? multiplierRaw : 1,
    answerPriceMultiplier: Number.isFinite(multiplierRaw) && multiplierRaw >= 0 ? multiplierRaw : 1,
    // Kept for old API responses; new writes use the multiplier fields above.
    answerPrice: Number.isFinite(answerPriceRaw) && answerPriceRaw >= 0 ? answerPriceRaw : 0,
    reservedFee: Number(firstDefined(record, ['reservedFee', 'reserved_fee'], 0)) || 0,
    totalUses: Number(firstDefined(record, ['totalUses', 'useCount'], 0)) || 0,
    totalAmount: Number(firstDefined(record, ['totalAmount', 'amountUsed'], 0)) || 0,
    createdAt: firstDefined(record, ['createdAt', 'created_at'], ''),
    updatedAt: firstDefined(record, ['updatedAt', 'updated_at'], ''),
  }
}

export function normalizeScriptRows(response) {
  const data = response?.data
  const rows = Array.isArray(data) ? data : (data?.items || data?.records || data?.rows || [])
  return rows.map(normalizeScriptRecord)
}

export function buildScriptListParams(isAdmin, showAll) {
  return { scope: isAdmin && showAll ? 'all' : 'self' }
}

export function buildScriptDownloadURL(record = {}) {
  const normalized = normalizeScriptRecord(record)
  const domain = normalized.baseDomain
  if (!domain || domain.length > 253 || !domain.split('.').every(label => /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))) return ''
  if (!/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(normalized.slug)) return ''
  const host = `${normalized.slug}.${domain}`
  const path = normalized.useWildcardDomain ? '' : `/${normalized.slug}`
  return `${normalized.scheme}://${host}${path}`
}

// The authenticated API constructs the launcher and controls whether the caller
// may receive one. Do not reconstruct executable code from a draft, a URL, or
// the legacy instruction field.
export function buildScriptCommand(record = {}) {
  const command = typeof record.command === 'string' ? record.command : ''
  return command.length <= 16384 && /^powershell -EncodedCommand [A-Za-z0-9+/]+={0,2}$/.test(command) ? command : ''
}

export function buildScriptSavePayload(form = {}) {
  const payload = {
    name: text(form.name),
    description: text(form.description),
    scheme: text(form.scheme).toLowerCase() === 'http' ? 'http' : DEFAULT_SCHEME,
    baseDomain: normalizeBaseDomain(form.baseDomain),
    useWildcardDomain: form.useWildcardDomain !== false,
    enabled: form.enabled !== false,
    presetParams: parsePresetParams(form.presetParams),
    billingEnabled: form.billingEnabled !== false,
    billingUseTikuPrice: form.billingUseTikuPrice !== false,
    useTikuPrice: form.billingUseTikuPrice !== false,
    priceMultiplier: Number.isFinite(Number(form.priceMultiplier ?? form.answerPriceMultiplier)) && Number(form.priceMultiplier ?? form.answerPriceMultiplier) >= 0
      ? Number(form.priceMultiplier ?? form.answerPriceMultiplier)
      : 1,
    answerPriceMultiplier: Number.isFinite(Number(form.priceMultiplier ?? form.answerPriceMultiplier)) && Number(form.priceMultiplier ?? form.answerPriceMultiplier) >= 0
      ? Number(form.priceMultiplier ?? form.answerPriceMultiplier)
      : 1,
    versionId: Number(form.versionId || 0),
    parameters: parsePresetParams(form.presetParams),
  }
  if (form.id !== undefined && form.id !== null && String(form.id) !== '') payload.id = form.id
  return payload
}

export function buildScriptBillingText(record = {}) {
  const normalized = normalizeScriptRecord(record)
  if (!normalized.billingEnabled) return '按原渠道费率'
  return normalized.billingUseTikuPrice ? '与题库一致（1X）' : `独立 ${normalized.priceMultiplier}X 题库标准`
}
