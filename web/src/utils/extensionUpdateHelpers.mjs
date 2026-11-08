export const EXTENSION_SOURCES = [
  { source: 'edge', label: 'Microsoft Edge' },
  { source: 'chrome', label: 'Google Chrome' },
]

export const EXTENSION_INTERVAL_MAX_MINUTES = 153722867
export const EXTENSION_INTERVAL_UNITS = [
  { value: 'minute', label: '分钟', minutes: 1 },
  { value: 'hour', label: '小时', minutes: 60 },
  { value: 'day', label: '天', minutes: 1440 },
  { value: 'week', label: '周', minutes: 10080 },
  { value: 'month', label: '月', minutes: 43200 },
]

const extensionIntervalUnitMap = Object.fromEntries(EXTENSION_INTERVAL_UNITS.map(unit => [unit.value, unit]))

export function extensionIntervalToMinutes(value, unit = 'minute') {
  const numeric = Number(value), factor = extensionIntervalUnitMap[unit]?.minutes
  const minutes = numeric * Number(factor)
  return Number.isSafeInteger(minutes) && minutes >= 1 && minutes <= EXTENSION_INTERVAL_MAX_MINUTES ? minutes : Number.NaN
}

export function splitExtensionInterval(minutes) {
  const numeric = Number(minutes)
  if (!Number.isSafeInteger(numeric) || numeric < 1 || numeric > EXTENSION_INTERVAL_MAX_MINUTES) return { value: numeric, unit: 'minute' }
  for (const unit of [...EXTENSION_INTERVAL_UNITS].reverse()) {
    if (numeric % unit.minutes === 0) return { value: numeric / unit.minutes, unit: unit.value }
  }
  return { value: numeric, unit: 'minute' }
}

export function normalizeExtensionConfig(config = {}) {
  return {
    autoUpdate: config.autoUpdate === true,
    intervalMinutes: config.intervalMinutes === undefined ? 14400 : config.intervalMinutes === null ? null : Number(config.intervalMinutes),
    edgeId: String(config.edgeId || '').trim(),
    chromeId: String(config.chromeId || '').trim(),
  }
}

export function normalizeExtensionOverrides(overrides = {}) {
  return Object.fromEntries(['autoUpdate', 'intervalMinutes', 'edgeId', 'chromeId'].map(key => [key, overrides[key] === true]))
}

export function extensionDatabasePayload(form, databaseConfig, overrides) {
  const config = normalizeExtensionConfig(form)
  const stored = normalizeExtensionConfig(databaseConfig)
  for (const key of Object.keys(config)) {
    if (overrides[key]) config[key] = stored[key]
  }
  return config
}

export function getExtensionConfigValidation(config) {
  const minutes = Number(config.intervalMinutes)
  if (!Number.isSafeInteger(minutes) || minutes < 1 || minutes > EXTENSION_INTERVAL_MAX_MINUTES) return Object.freeze({ field: 'intervalMinutes', message: '检查间隔须为至少 1 分钟且不超过系统支持范围。' })
  for (const [key, label] of [['edgeId', 'Edge'], ['chromeId', 'Chrome']]) {
    if (config[key] && !/^[a-p]{32}$/.test(config[key])) return Object.freeze({ field: key, message: `${label} 扩展 ID 须为 32 位 a 到 p 的小写字母，留空可停用。` })
  }
  return Object.freeze({ field: '', message: '' })
}

export function validateExtensionConfig(config) {
  return getExtensionConfigValidation(config).message
}

export function normalizeExtensionSources(sources = []) {
  return EXTENSION_SOURCES.map(item => {
    const status = Array.isArray(sources) ? sources.find(value => value?.source === item.source) : null
    return { ...status, ...item, configured: status?.configured === true, available: status?.available === true }
  })
}

export function extensionStatusText(source) {
  if (!source.configured) return '未启用'
  if (source.updating) return '正在检查'
  if (source.lastError) return source.available ? '检查失败，保留缓存' : '检查失败'
  return source.available ? '缓存可用' : '尚无缓存'
}

export function formatExtensionSize(value) {
  const bytes = Number(value)
  if (!Number.isFinite(bytes) || bytes <= 0) return '-'
  return bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

export function formatExtensionTime(value) {
  const date = new Date(value)
  return value && Number.isFinite(date.getTime()) ? date.toLocaleString('zh-CN', { hour12: false }) : '-'
}
