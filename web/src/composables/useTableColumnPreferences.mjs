import { computed, ref, unref, watch } from 'vue'

const valueOf = value => typeof value === 'function' ? value() : unref(value)
const keyOf = column => {
  const key = column?.key ?? column?.dataIndex
  return Array.isArray(key) ? key.join('.') : key === undefined || key === null ? null : String(key)
}
function browserStorage() {
  try { return typeof window === 'undefined' ? null : window.localStorage } catch { return null }
}

// Persist display keys only. Available columns (including role filtering) stay authoritative.
export function useTableColumnPreferences({ pageKey, columns, scope, requiredKeys = [], priorityKeys = [], storage = browserStorage() }) {
  const choice = ref(null)
  const visitedKeys = new Set()
  const allColumns = computed(() => Array.isArray(valueOf(columns)) ? valueOf(columns) : [])
  const keys = computed(() => [...new Set(allColumns.value.map(keyOf).filter(key => key !== null))])
  const required = computed(() => {
    const requested = valueOf(requiredKeys)
    const available = keys.value.filter(key => (Array.isArray(requested) ? requested : []).map(String).includes(key))
    return available.length ? available : keys.value.slice(0, 1)
  })
  const scopeKey = computed(() => {
    const identity = valueOf(scope)
    return identity ? 'tikuhelper:columns:v1:' + pageKey + ':' + encodeURIComponent(String(identity)) : ''
  })
  const normalize = selection => {
    const selected = new Set((Array.isArray(selection) ? selection : keys.value).map(String))
    required.value.forEach(key => selected.add(key))
    return keys.value.filter(key => selected.has(key))
  }
  watch(scopeKey, key => {
    choice.value = null
    if (!key) {
      for (const visited of visitedKeys) { try { storage?.removeItem(visited) } catch { /* Optional storage. */ } }
      visitedKeys.clear()
      return
    }
    visitedKeys.add(key)
    try {
      const raw = storage?.getItem(key)
      if (!raw || raw.length > 16384) return
      const saved = JSON.parse(raw)
      if (saved?.version === 1 && Array.isArray(saved.selectedKeys) && saved.selectedKeys.length <= 256 && saved.selectedKeys.every(key => typeof key === 'string')) choice.value = saved.selectedKeys
    } catch { /* Storage is optional; invalid preferences fall back to all source columns. */ }
  }, { immediate: true, flush: 'sync' })
  const selectedKeys = computed(() => normalize(choice.value))
  const visibleColumns = computed(() => {
    const selected = new Set(selectedKeys.value)
    return allColumns.value.filter(column => keyOf(column) === null || selected.has(keyOf(column)))
  })
  const columnOptions = computed(() => keys.value.map(key => {
    const column = allColumns.value.find(column => keyOf(column) === key)
    return { key, label: typeof column?.title === 'string' ? column.title : column?.columnLabel || key, disabled: required.value.includes(key) }
  }))
  const resetColumns = () => {
    choice.value = null
    if (scopeKey.value) { try { storage?.removeItem(scopeKey.value) } catch { /* Current view remains usable. */ } }
  }
  const setSelectedKeys = selection => {
    const normalized = normalize(selection)
    choice.value = normalized
    if (!scopeKey.value) return
    try { storage?.setItem(scopeKey.value, JSON.stringify({ version: 1, selectedKeys: normalized })) } catch { /* No row data or secrets are stored; keep this session usable. */ }
  }
  const showPriorityColumns = () => {
    const preferred = valueOf(priorityKeys)
    if (Array.isArray(preferred) && preferred.length) setSelectedKeys(preferred)
    else resetColumns()
  }
  return { visibleColumns, selectedKeys, columnOptions, scopeKey, setSelectedKeys, resetColumns, showPriorityColumns }
}
