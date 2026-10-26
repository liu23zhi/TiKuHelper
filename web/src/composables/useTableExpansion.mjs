import { reactive, unref, watch } from 'vue'
import { tableExpansionKey, tableTextWidths } from '../utils/tableLayout.mjs'

export function useTableExpansion(rowsRef, rowKey = row => row.id) {
  const expanded = reactive(new Set())
  const idOf = row => row && typeof row === 'object' ? rowKey(row) : row
  const rows = () => Array.isArray(unref(rowsRef)) ? unref(rowsRef) : []
  const isExpanded = (row, field) => expanded.has(tableExpansionKey(idOf(row), field))
  const toggle = (row, field) => {
    const key = tableExpansionKey(idOf(row), field)
    if (expanded.has(key)) expanded.delete(key)
    else expanded.add(key)
  }
  const columnWidth = (field, collapsed = tableTextWidths.collapsed, full = tableTextWidths.expanded) => (
    rows().some(row => isExpanded(row, field)) ? full : collapsed
  )
  const clear = () => expanded.clear()
  // Retain expansion on refresh, while bounding state to the visible page.
  watch(() => rows().map(row => String(rowKey(row))), ids => {
    const visible = new Set(ids)
    for (const key of expanded) {
      if (!visible.has(JSON.parse(key)[0])) expanded.delete(key)
    }
  })
  return { isExpanded, toggle, columnWidth, clear }
}
