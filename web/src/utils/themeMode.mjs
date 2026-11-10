export const THEME_CONTROLLER_KEY = Symbol('tiku-theme-controller')

const STORAGE_KEY = 'tiku.theme-mode.v1'
// Motion lives in the same controller and the same `tiku.*.v1` storage family,
// but in its own key: `tiku.theme-mode.v1` must keep holding a bare mode string
// (the head bootstrap script and the theme contract read it as a scalar). One
// controller, two independent channels — a motion choice never rewrites the
// theme choice and vice versa.
export const MOTION_STORAGE_KEY = 'tiku.motion-mode.v1'
const SYSTEM_QUERY = '(prefers-color-scheme: dark)'
const MOTION_QUERY = '(prefers-reduced-motion: reduce)'
const BACKGROUND = { light: '#F5F5F7', dark: '#111113' }

export function normalizeThemeMode(raw) {
  return raw === 'light' || raw === 'dark' || raw === 'system' ? raw : 'system'
}

export function resolveTheme(mode, systemDark) {
  const normalized = normalizeThemeMode(mode)
  return normalized === 'system' ? (systemDark ? 'dark' : 'light') : normalized
}

// Three levels, no coercion: 'on' and 'off' are explicit application choices,
// anything else (null, malformed, unknown) is the 'auto' default.
export function normalizeMotionMode(raw) {
  return raw === 'on' || raw === 'off' ? raw : 'auto'
}

// 'auto' follows the OS. An explicit 'off' is always off. An explicit 'on' is
// reported as off when the OS asks for reduced motion: the system signal usually
// exists for medical reasons (vestibular disorders), so it outranks the app.
export function resolveMotion(mode, systemReduced) {
  if (normalizeMotionMode(mode) === 'off') return 'off'
  return systemReduced ? 'off' : 'on'
}

export function createThemeController({
  getStorage = () => window.localStorage,
  createMediaQuery = query => window.matchMedia(query),
  root = typeof document === 'undefined' ? null : document.documentElement,
  eventTarget = typeof window === 'undefined' ? null : window,
} = {}) {
  let disposed = false
  const subscribers = new Set()
  const motionSubscribers = new Set()
  const cleanup = []
  let mediaQuery = null
  let motionQuery = null
  try { mediaQuery = createMediaQuery(SYSTEM_QUERY) } catch { /* Unsupported environments use light. */ }
  try { motionQuery = createMediaQuery(MOTION_QUERY) } catch { /* No reduced-motion signal: follow the app choice. */ }
  const systemDark = () => {
    try { return Boolean(mediaQuery?.matches) } catch { return false }
  }
  const systemReducedMotion = () => {
    try { return Boolean(motionQuery?.matches) } catch { return false }
  }
  const readMode = () => {
    try { return normalizeThemeMode(getStorage()?.getItem(STORAGE_KEY)) } catch { return 'system' }
  }
  const readMotion = () => {
    try { return normalizeMotionMode(getStorage()?.getItem(MOTION_STORAGE_KEY)) } catch { return 'auto' }
  }
  // `state` is the theme channel only, unchanged in shape: existing consumers and
  // the theme contract read exactly { mode, resolved }.
  let state = { mode: readMode(), resolved: '' }
  let motionState = { mode: readMotion(), resolved: '' }
  const getState = () => ({ ...state })
  const getMotionState = () => ({ ...motionState })
  const notifyTheme = () => {
    for (const subscriber of [...subscribers]) {
      if (disposed) return
      if (subscribers.has(subscriber)) subscriber(getState())
    }
  }
  const notifyMotion = () => {
    for (const subscriber of [...motionSubscribers]) {
      if (disposed) return
      if (motionSubscribers.has(subscriber)) subscriber(getMotionState())
    }
  }
  const apply = (mode, motion = motionState.mode) => {
    if (disposed) return
    const resolved = resolveTheme(mode, systemDark())
    const motionResolved = resolveMotion(motion, systemReducedMotion())
    const themeChanged = state.mode !== mode || state.resolved !== resolved
    const motionChanged = motionState.mode !== motion || motionState.resolved !== motionResolved
    state = { mode, resolved }
    motionState = { mode: motion, resolved: motionResolved }
    if (root) {
      root.setAttribute('data-theme-mode', mode)
      root.setAttribute('data-theme', resolved)
      // data-motion carries the *choice*, not the effective value: the
      // prefers-reduced-motion block must still see an explicit 'on' in order to
      // override it, and 'auto' must defer to the media query naturally.
      root.setAttribute('data-motion', motion)
      root.style.colorScheme = resolved
      root.style.backgroundColor = BACKGROUND[resolved]
    }
    if (themeChanged) notifyTheme()
    if (motionChanged) notifyMotion()
  }
  const onThemeMediaChange = () => {
    if (state.mode === 'system') apply('system')
  }
  // A reduced-motion change must reach the motion channel even when the theme is
  // pinned to an explicit light/dark choice.
  const onMotionMediaChange = () => apply(state.mode)
  const onStorageChange = event => {
    if (disposed || (event.key !== STORAGE_KEY && event.key !== MOTION_STORAGE_KEY && event.key !== null)) return
    if (event.storageArea) {
      try { if (event.storageArea !== getStorage()) return } catch { return }
    }
    if (event.key === MOTION_STORAGE_KEY) {
      apply(state.mode, normalizeMotionMode(event.newValue))
      return
    }
    if (event.key === null) {
      apply(readMode(), readMotion())
      return
    }
    apply(normalizeThemeMode(event.newValue), motionState.mode)
  }
  apply(state.mode, motionState.mode)
  for (const [query, handler] of [[mediaQuery, onThemeMediaChange], [motionQuery, onMotionMediaChange]]) {
    if (query?.addEventListener) {
      query.addEventListener('change', handler)
      cleanup.push(() => query.removeEventListener('change', handler))
    } else if (query?.addListener) {
      query.addListener(handler)
      cleanup.push(() => query.removeListener(handler))
    }
  }
  if (eventTarget?.addEventListener) {
    eventTarget.addEventListener('storage', onStorageChange)
    cleanup.push(() => eventTarget.removeEventListener('storage', onStorageChange))
  }
  return {
    getState,
    getMotionState,
    setMode(raw) {
      if (disposed) return
      const mode = normalizeThemeMode(raw)
      try { getStorage()?.setItem(STORAGE_KEY, mode) } catch { /* The in-memory choice still applies. */ }
      apply(mode)
    },
    setMotion(raw) {
      if (disposed) return
      const motion = normalizeMotionMode(raw)
      try { getStorage()?.setItem(MOTION_STORAGE_KEY, motion) } catch { /* The in-memory choice still applies. */ }
      apply(state.mode, motion)
    },
    subscribe(subscriber) {
      if (disposed || typeof subscriber !== 'function') return () => {}
      subscribers.add(subscriber)
      return () => subscribers.delete(subscriber)
    },
    subscribeMotion(subscriber) {
      if (disposed || typeof subscriber !== 'function') return () => {}
      motionSubscribers.add(subscriber)
      return () => motionSubscribers.delete(subscriber)
    },
    dispose() {
      if (disposed) return
      disposed = true
      for (const stop of cleanup) stop()
      subscribers.clear()
      motionSubscribers.clear()
    },
  }
}
