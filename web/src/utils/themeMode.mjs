export const THEME_CONTROLLER_KEY = Symbol('tiku-theme-controller')

const STORAGE_KEY = 'tiku.theme-mode.v1'
const SYSTEM_QUERY = '(prefers-color-scheme: dark)'
const BACKGROUND = { light: '#F5F5F7', dark: '#111113' }

export function normalizeThemeMode(raw) {
  return raw === 'light' || raw === 'dark' || raw === 'system' ? raw : 'system'
}

export function resolveTheme(mode, systemDark) {
  const normalized = normalizeThemeMode(mode)
  return normalized === 'system' ? (systemDark ? 'dark' : 'light') : normalized
}

export function createThemeController({
  getStorage = () => window.localStorage,
  createMediaQuery = query => window.matchMedia(query),
  root = typeof document === 'undefined' ? null : document.documentElement,
  eventTarget = typeof window === 'undefined' ? null : window,
} = {}) {
  let disposed = false
  const subscribers = new Set()
  const cleanup = []
  let mediaQuery = null
  try { mediaQuery = createMediaQuery(SYSTEM_QUERY) } catch { /* Unsupported environments use light. */ }
  const systemDark = () => {
    try { return Boolean(mediaQuery?.matches) } catch { return false }
  }
  const readMode = () => {
    try { return normalizeThemeMode(getStorage()?.getItem(STORAGE_KEY)) } catch { return 'system' }
  }
  let state = { mode: readMode(), resolved: '' }
  const getState = () => ({ ...state })
  const apply = mode => {
    if (disposed) return
    const resolved = resolveTheme(mode, systemDark())
    const changed = state.mode !== mode || state.resolved !== resolved
    state = { mode, resolved }
    if (root) {
      root.setAttribute('data-theme-mode', mode)
      root.setAttribute('data-theme', resolved)
      root.style.colorScheme = resolved
      root.style.backgroundColor = BACKGROUND[resolved]
    }
    if (changed) {
      for (const subscriber of [...subscribers]) {
        if (disposed) break
        if (subscribers.has(subscriber)) subscriber(getState())
      }
    }
  }
  const onMediaChange = () => {
    if (state.mode === 'system') apply('system')
  }
  const onStorageChange = event => {
    if (disposed || (event.key !== STORAGE_KEY && event.key !== null)) return
    if (event.storageArea) {
      try { if (event.storageArea !== getStorage()) return } catch { return }
    }
    apply(event.key === null ? readMode() : normalizeThemeMode(event.newValue))
  }
  apply(state.mode)
  if (mediaQuery?.addEventListener) {
    mediaQuery.addEventListener('change', onMediaChange)
    cleanup.push(() => mediaQuery.removeEventListener('change', onMediaChange))
  } else if (mediaQuery?.addListener) {
    mediaQuery.addListener(onMediaChange)
    cleanup.push(() => mediaQuery.removeListener(onMediaChange))
  }
  if (eventTarget?.addEventListener) {
    eventTarget.addEventListener('storage', onStorageChange)
    cleanup.push(() => eventTarget.removeEventListener('storage', onStorageChange))
  }
  return {
    getState,
    setMode(raw) {
      if (disposed) return
      const mode = normalizeThemeMode(raw)
      try { getStorage()?.setItem(STORAGE_KEY, mode) } catch { /* The in-memory choice still applies. */ }
      apply(mode)
    },
    subscribe(subscriber) {
      if (disposed || typeof subscriber !== 'function') return () => {}
      subscribers.add(subscriber)
      return () => subscribers.delete(subscriber)
    },
    dispose() {
      if (disposed) return
      disposed = true
      for (const stop of cleanup) stop()
      subscribers.clear()
    },
  }
}
