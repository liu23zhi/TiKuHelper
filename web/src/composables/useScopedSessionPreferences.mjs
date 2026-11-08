import { computed, unref, watch } from 'vue'
const valueOf = value => typeof value === 'function' ? value() : unref(value)
function sessionStore() { try { return typeof window === 'undefined' ? null : window.sessionStorage } catch { return null } }
const allowedPrimitive = value => value === null || value === undefined || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value)) || (typeof value === 'string' && value.length <= 64)
const forbidden = /^(?:keyword|password|token|secret|cookie|authorization|phone|email|search)$/i

// Only explicit enum/number/bool fields; never pass keyword text or credential fields.
// Call after the page identity-owner hook so owner clearing precedes restoration.
export function useScopedSessionPreferences({ pageKey, scope, fields, storage = sessionStore() }) {
  const entries = Object.entries(fields).filter(([key]) => !forbidden.test(key))
  const defaults = Object.fromEntries(entries.map(([key, field]) => [key, field.get()]))
  const scopeKey = computed(() => {
    const identity = valueOf(scope)
    return identity ? 'tikuhelper:session:v1:' + pageKey + ':' + encodeURIComponent(String(identity)) : ''
  })
  let restoring = false
  let activeKey = ''
  const visitedKeys = new Set()
  const valid = (field, value) => allowedPrimitive(value) && typeof field.validate === 'function' && field.validate(value)
  const restorePreferences = () => {
    restoring = true
    activeKey = scopeKey.value
    if (!activeKey) {
      for (const visited of visitedKeys) { try { storage?.removeItem(visited) } catch { /* Optional storage. */ } }
      visitedKeys.clear()
    } else visitedKeys.add(activeKey)
    let values = {}
    if (activeKey) {
      try {
        const raw = storage?.getItem(activeKey)
        const saved = raw && raw.length <= 16384 ? JSON.parse(raw) : null
        if (saved?.version === 1 && saved.values && typeof saved.values === 'object' && !Array.isArray(saved.values)) values = saved.values
      } catch { /* Corrupt or blocked session storage must not prevent page access. */ }
    }
    try {
      for (const [key, field] of entries) field.set(Object.prototype.hasOwnProperty.call(values, key) && valid(field, values[key]) ? values[key] : defaults[key])
    } finally { restoring = false }
  }
  watch(scopeKey, restorePreferences, { immediate: true, flush: 'sync' })
  watch(() => entries.map(([, field]) => field.get()), () => {
    if (restoring || !activeKey || activeKey !== scopeKey.value) return
    const values = {}
    for (const [key, field] of entries) {
      const value = field.get()
      if (value !== undefined && valid(field, value)) values[key] = value
    }
    try { storage?.setItem(activeKey, JSON.stringify({ version: 1, values })) } catch { /* Preferences are optional, not a server state update. */ }
  }, { flush: 'sync' })
  const clearPreferences = () => {
    if (activeKey) { try { storage?.removeItem(activeKey) } catch { /* Optional storage. */ } }
    restorePreferences()
  }
  return { scopeKey, restorePreferences, clearPreferences }
}
