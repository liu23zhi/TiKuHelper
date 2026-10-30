export const EXTENSION_SOURCES = [
  { source: 'edge', label: 'Microsoft Edge' },
  { source: 'chrome', label: 'Google Chrome' },
]

export function normalizeExtensionConfig(config = {}) {
  return {
    autoUpdate: config.autoUpdate === true,
    intervalMinutes: config.intervalMinutes === undefined ? 360 : config.intervalMinutes === null ? null : Number(config.intervalMinutes),
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

export function validateExtensionConfig(config) {
  const minutes = Number(config.intervalMinutes)
  if (!Number.isInteger(minutes) || minutes < 1 || minutes > 10080) return '检查间隔须为 1 到 10080 分钟的整数。'
  for (const [key, label] of [['edgeId', 'Edge'], ['chromeId', 'Chrome']]) {
    if (config[key] && !/^[a-p]{32}$/.test(config[key])) return `${label} 扩展 ID 须为 32 位 a 到 p 的小写字母，留空可停用。`
  }
  return ''
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
